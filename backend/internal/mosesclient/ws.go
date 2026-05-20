package mosesclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// WSEvent is the typed mirror of api.WebSocketMessage from
// moses-platform-prep/backend/internal/api/websocket_handlers.go
// (line 145-165). All fields are optional — which fields are
// populated depends on Type / EventType / Topic.
//
// Bot callers care primarily about:
//   - Type=="assistant_message_chunk" (legacy wire) with Message
//     holding chunk text — aggregate by ConversationID.
//   - Type=="assistant_message_complete" — terminal flush.
//   - Type=="connection_established" — initial handshake ack.
//   - Type=="subscribe_error" — subscription rejected (bad ID / RBAC).
//   - Type=="domain_event" with Topic/EventType — the Phase-2 envelope
//     used for cross-cutting events (deployment, ticket, etc.). Not
//     used by the chat-relay path but supported for completeness so
//     the bot can later opt into ticket / deploy notifications.
type WSEvent struct {
	Type           string          `json:"type"`
	ConversationID string          `json:"conversation_id,omitempty"`
	ChartID        string          `json:"chart_id,omitempty"`
	TicketID       string          `json:"ticket_id,omitempty"`
	ExecutionID    string          `json:"execution_id,omitempty"`
	Message        json.RawMessage `json:"message,omitempty"`
	UserID         string          `json:"user_id,omitempty"`
	Timestamp      time.Time       `json:"timestamp"`

	// Phase-2 domain_event envelope. Populated only when Type ==
	// "domain_event".
	Topic     string          `json:"topic,omitempty"`
	TopicID   string          `json:"topic_id,omitempty"`
	EventType string          `json:"event_type,omitempty"`
	Seq       int64           `json:"seq,omitempty"`
	Source    string          `json:"source,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
}

// WSConfig configures the subscriber. Defaults are production-safe;
// tests override Backoff* and MaxRetries to keep runs fast.
type WSConfig struct {
	// HandshakeTimeout caps the WS upgrade dial. Default 10s.
	HandshakeTimeout time.Duration

	// PongWait is how long we wait for a pong response before
	// considering the connection dead. Default 60s (matches the
	// platform's SetReadDeadline at websocket_handlers.go:373).
	PongWait time.Duration

	// PingInterval is the heartbeat frequency. Must be less than
	// PongWait. Default 30s.
	PingInterval time.Duration

	// MaxRetries is the consecutive-failure ceiling before the
	// subscriber gives up and emits ErrWSDisconnected. Default 5.
	MaxRetries int

	// BackoffBase is the initial reconnect backoff. Default 1s.
	BackoffBase time.Duration

	// BackoffCap is the upper bound on the backoff. Default 30s.
	BackoffCap time.Duration

	// EventBuffer is the size of the Events() channel buffer.
	// Default 64.
	EventBuffer int
}

// withDefaults populates zero values with production defaults.
func (c WSConfig) withDefaults() WSConfig {
	if c.HandshakeTimeout == 0 {
		c.HandshakeTimeout = 10 * time.Second
	}
	if c.PongWait == 0 {
		c.PongWait = 60 * time.Second
	}
	if c.PingInterval == 0 {
		c.PingInterval = 30 * time.Second
	}
	if c.MaxRetries == 0 {
		c.MaxRetries = 5
	}
	if c.BackoffBase == 0 {
		c.BackoffBase = 1 * time.Second
	}
	if c.BackoffCap == 0 {
		c.BackoffCap = 30 * time.Second
	}
	if c.EventBuffer == 0 {
		c.EventBuffer = 64
	}
	return c
}

// subscription is a re-sendable subscribe frame remembered across
// reconnects so the subscriber transparently re-subscribes after a
// drop.
type subscription struct {
	frame map[string]interface{}
}

// WSSubscriber holds a long-lived WebSocket connection to
// moses-backend's /api/v1/ai/ws endpoint with auto-reconnect and
// transparent re-subscription on reconnect.
type WSSubscriber struct {
	wsURL   string
	token   string
	cfg     WSConfig
	dialer  *websocket.Dialer
	events  chan WSEvent
	closeCh chan struct{}

	mu            sync.Mutex
	conn          *websocket.Conn
	writeMu       sync.Mutex // serialises WriteJSON / WriteControl on conn
	subscriptions []subscription // remembered for reconnect
	closed        bool
	failureCount  int // consecutive read-pump failures (reset by readPump on first event)
}

// NewWSSubscriber dials moses-backend's WebSocket endpoint and starts
// the read pump + reconnect loop in a goroutine. The returned
// subscriber is safe to call Subscribe / Close on from any goroutine.
//
// baseWS is the moses-backend root expressed as a ws:// or wss:// URL
// (or as http://… — that's auto-rewritten). The /api/v1/ai/ws path
// is appended automatically.
//
// token is the MCP API key (with or without the "mcp-" prefix —
// moses-backend's AuthMiddlewareWithAPIKey accepts both).
//
// The first dial happens inline so a fatal handshake (e.g. 401)
// surfaces synchronously to the caller. Subsequent reconnects happen
// in the background.
func NewWSSubscriber(ctx context.Context, baseWS, token string, cfg WSConfig) (*WSSubscriber, error) {
	cfg = cfg.withDefaults()
	wsURL, err := buildWSURL(baseWS, token)
	if err != nil {
		return nil, err
	}
	s := &WSSubscriber{
		wsURL:   wsURL,
		token:   token,
		cfg:     cfg,
		dialer:  &websocket.Dialer{HandshakeTimeout: cfg.HandshakeTimeout},
		events:  make(chan WSEvent, cfg.EventBuffer),
		closeCh: make(chan struct{}),
	}
	if err := s.dial(ctx); err != nil {
		return nil, err
	}
	go s.run(ctx)
	return s, nil
}

// buildWSURL turns an arbitrary moses-backend base URL into the
// fully-qualified WS endpoint URL with the token query param. Accepts
// http(s):// and rewrites it to ws(s)://.
func buildWSURL(baseWS, token string) (string, error) {
	u, err := url.Parse(baseWS)
	if err != nil {
		return "", fmt.Errorf("mosesclient: parse ws base url: %w", err)
	}
	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	case "ws", "wss":
		// keep as-is
	default:
		return "", fmt.Errorf("mosesclient: unsupported ws scheme %q", u.Scheme)
	}
	// Trim any trailing slash on the path then append the ws endpoint.
	u.Path = strings.TrimRight(u.Path, "/") + "/api/v1/ai/ws"
	q := u.Query()
	q.Set("token", token)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// dial performs a single dial attempt. Maps the handshake error to
// ErrWSAuthFailed when the server returns 401/403 — the token is bad
// and retrying is pointless.
func (s *WSSubscriber) dial(ctx context.Context) error {
	conn, resp, err := s.dialer.DialContext(ctx, s.wsURL, nil)
	if err != nil {
		if resp != nil && (resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden) {
			return fmt.Errorf("%w: status %d", ErrWSAuthFailed, resp.StatusCode)
		}
		return fmt.Errorf("mosesclient: ws dial %s: %w", s.wsURL, err)
	}
	conn.SetReadDeadline(time.Now().Add(s.cfg.PongWait))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(s.cfg.PongWait))
		return nil
	})
	s.mu.Lock()
	s.conn = conn
	subs := append([]subscription(nil), s.subscriptions...)
	s.mu.Unlock()

	// Re-send remembered subscriptions after a reconnect.
	for _, sub := range subs {
		if err := s.writeJSON(sub.frame); err != nil {
			return fmt.Errorf("mosesclient: re-subscribe after reconnect: %w", err)
		}
	}
	return nil
}

// Subscribe sends a subscribe frame for a given topic and topicID.
// Supported topics (verified against websocket_handlers.go:392-470):
//
//   - "conversation"  → frame {"type":"subscribe_conversation","conversation_id":<id>}
//   - "chart"         → frame {"type":"subscribe_chart","chart_id":<id>}
//   - "execution"     → frame {"type":"subscribe_execution","execution_id":<id>}
//   - "chart_meta"    → frame {"type":"subscribe_chart_meta","chart_id":<id>}
//   - "repo"          → frame {"type":"subscribe_repo","repo_id":<id>}
//   - "deployments"   → frame {"type":"subscribe_deployments","chart_id":<id>}
//
// The subscription is remembered and re-sent on reconnect.
func (s *WSSubscriber) Subscribe(topic, topicID string) error {
	frame, err := buildSubscribeFrame(topic, topicID)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.subscriptions = append(s.subscriptions, subscription{frame: frame})
	s.mu.Unlock()
	return s.writeJSON(frame)
}

// buildSubscribeFrame returns the JSON-shape the platform's
// readPump switch expects. Centralised so changes only land in one
// place when the wire evolves.
func buildSubscribeFrame(topic, topicID string) (map[string]interface{}, error) {
	switch topic {
	case "conversation":
		return map[string]interface{}{"type": "subscribe_conversation", "conversation_id": topicID}, nil
	case "chart":
		return map[string]interface{}{"type": "subscribe_chart", "chart_id": topicID}, nil
	case "execution":
		return map[string]interface{}{"type": "subscribe_execution", "execution_id": topicID}, nil
	case "chart_meta":
		return map[string]interface{}{"type": "subscribe_chart_meta", "chart_id": topicID}, nil
	case "repo":
		return map[string]interface{}{"type": "subscribe_repo", "repo_id": topicID}, nil
	case "deployments":
		return map[string]interface{}{"type": "subscribe_deployments", "chart_id": topicID}, nil
	default:
		return nil, fmt.Errorf("mosesclient: unknown subscribe topic %q", topic)
	}
}

// writeJSON sends a frame on the current connection. Caller is
// expected to handle reconnect on error. gorilla/websocket requires
// at most one concurrent writer per connection — writeMu enforces
// that across user-initiated Subscribe calls and the ping goroutine.
func (s *WSSubscriber) writeJSON(v interface{}) error {
	s.mu.Lock()
	conn := s.conn
	s.mu.Unlock()
	if conn == nil {
		return errors.New("mosesclient: ws not connected")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return conn.WriteJSON(v)
}

// Events returns the receive channel. The channel closes when Close
// is called OR when the subscriber gives up reconnecting; in the
// latter case a single ErrWSDisconnected event-of-type "" is dropped
// onto the channel BEFORE close so callers can distinguish a clean
// shutdown from a permanent disconnect.
func (s *WSSubscriber) Events() <-chan WSEvent { return s.events }

// Close requests a graceful shutdown. Safe to call multiple times.
func (s *WSSubscriber) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	conn := s.conn
	s.conn = nil
	close(s.closeCh)
	s.mu.Unlock()
	if conn != nil {
		s.writeMu.Lock()
		_ = conn.WriteControl(
			websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
			time.Now().Add(2*time.Second),
		)
		s.writeMu.Unlock()
		return conn.Close()
	}
	return nil
}

// run is the supervisor loop: it spawns a read pump, blocks until
// that pump returns (connection closed or read error), then if we
// have retries left and Close was not called, dials again with
// exponential backoff.
//
// failureCount tracks CONSECUTIVE failures. It resets when the read
// pump delivers at least one frame — a server that accepts the
// handshake but immediately drops the conn would otherwise reset on
// every successful dial and wedge us in an infinite reconnect loop.
func (s *WSSubscriber) run(ctx context.Context) {
	defer close(s.events)

	for {
		s.readPump(ctx)

		s.mu.Lock()
		closed := s.closed
		failures := s.failureCount
		s.mu.Unlock()
		if closed {
			return
		}
		if ctx.Err() != nil {
			return
		}

		failures++
		s.mu.Lock()
		s.failureCount = failures
		s.mu.Unlock()
		if failures > s.cfg.MaxRetries {
			s.emit(WSEvent{Type: "error", Message: errAsJSON(ErrWSDisconnected)})
			return
		}

		// Exponential backoff capped at BackoffCap.
		backoff := s.cfg.BackoffBase
		for i := 0; i < failures-1 && backoff < s.cfg.BackoffCap; i++ {
			backoff *= 2
		}
		if backoff > s.cfg.BackoffCap {
			backoff = s.cfg.BackoffCap
		}
		select {
		case <-ctx.Done():
			return
		case <-s.closeCh:
			return
		case <-time.After(backoff):
		}

		if err := s.dial(ctx); err != nil {
			// Auth failures are terminal — no point retrying.
			if errors.Is(err, ErrWSAuthFailed) {
				s.emit(WSEvent{Type: "error", Message: errAsJSON(err)})
				return
			}
			// Other dial failures count toward the same retry budget;
			// we'll loop and bump failures again next iteration.
			continue
		}
	}
}

// readPump reads frames from the current connection and forwards
// them to s.events. It also runs the ping ticker so the platform's
// 60s read deadline doesn't trip on idle connections.
//
// Returns when the connection errors or context cancels.
func (s *WSSubscriber) readPump(ctx context.Context) {
	s.mu.Lock()
	conn := s.conn
	s.mu.Unlock()
	if conn == nil {
		return
	}

	pingDone := make(chan struct{})
	go func() {
		ticker := time.NewTicker(s.cfg.PingInterval)
		defer ticker.Stop()
		for {
			select {
			case <-pingDone:
				return
			case <-ctx.Done():
				return
			case <-s.closeCh:
				return
			case <-ticker.C:
				s.writeMu.Lock()
				err := conn.WriteControl(
					websocket.PingMessage, nil,
					time.Now().Add(5*time.Second),
				)
				s.writeMu.Unlock()
				if err != nil {
					return
				}
			}
		}
	}()
	defer close(pingDone)

	firstFrame := true
	for {
		var ev WSEvent
		if err := conn.ReadJSON(&ev); err != nil {
			return
		}
		if firstFrame {
			// We received at least one event — the connection is
			// healthy. Reset the consecutive-failure counter so a
			// later drop doesn't immediately trip MaxRetries.
			s.mu.Lock()
			s.failureCount = 0
			s.mu.Unlock()
			firstFrame = false
		}
		s.emit(ev)
	}
}

// emit drops an event on the events chan with a non-blocking best
// effort; if the buffer is full and nobody is reading, we discard the
// event rather than wedge the read pump. (The platform's broadcast
// goroutine takes the same approach — see ws_handler.go's `default:`
// arm in sendToSubscribedConnections.)
func (s *WSSubscriber) emit(ev WSEvent) {
	select {
	case s.events <- ev:
	default:
		// drop on full buffer
	}
}

// errAsJSON serialises an error string into the Message field shape
// (json.RawMessage) so terminal-error events look like a normal
// WSEvent to callers.
func errAsJSON(err error) json.RawMessage {
	b, _ := json.Marshal(map[string]string{"error": err.Error()})
	return b
}
