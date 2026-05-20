package mosesclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// wsTestServer is a minimal fake of moses-backend's /api/v1/ai/ws.
// It accepts a single token, echoes subscribe frames as a typed event,
// and exposes a Send method so tests can inject server-pushed events
// (mimicking the platform's broadcast goroutine).
// safeConn wraps *websocket.Conn with a write mutex so the test
// server can write from multiple goroutines (the read loop's
// subscribe-ack writer + pushEvent) without tripping gorilla's
// "no concurrent writers" invariant.
type safeConn struct {
	conn *websocket.Conn
	mu   sync.Mutex
}

func (sc *safeConn) writeJSON(v interface{}) error {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	return sc.conn.WriteJSON(v)
}

type wsTestServer struct {
	t        *testing.T
	srv      *httptest.Server
	upgrader websocket.Upgrader

	// Behaviour knobs
	rejectAuth   bool
	dropOnAccept atomic.Bool // when true, accept then immediately close the conn

	mu          sync.Mutex
	connsByConv map[string][]*safeConn
	allConns    []*safeConn
	gotTokens   []string
}

func newWSTestServer(t *testing.T) *wsTestServer {
	ts := &wsTestServer{
		t:           t,
		upgrader:    websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }},
		connsByConv: map[string][]*safeConn{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/ai/ws", ts.handle)
	ts.srv = httptest.NewServer(mux)
	return ts
}

func (ts *wsTestServer) Close() { ts.srv.Close() }

func (ts *wsTestServer) URL() string {
	return strings.Replace(ts.srv.URL, "http://", "ws://", 1)
}

func (ts *wsTestServer) handle(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	ts.mu.Lock()
	ts.gotTokens = append(ts.gotTokens, token)
	rejectAuth := ts.rejectAuth
	ts.mu.Unlock()

	if rejectAuth {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	conn, err := ts.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}

	if ts.dropOnAccept.Load() {
		_ = conn.Close()
		return
	}

	sc := &safeConn{conn: conn}
	ts.mu.Lock()
	ts.allConns = append(ts.allConns, sc)
	ts.mu.Unlock()

	go ts.readLoop(sc)
}

func (ts *wsTestServer) readLoop(sc *safeConn) {
	defer sc.conn.Close()
	for {
		var msg map[string]interface{}
		if err := sc.conn.ReadJSON(&msg); err != nil {
			return
		}
		// Echo back a confirmation matching the subscribe shape.
		if t, _ := msg["type"].(string); t == "subscribe_conversation" {
			convID, _ := msg["conversation_id"].(string)
			ts.mu.Lock()
			ts.connsByConv[convID] = append(ts.connsByConv[convID], sc)
			ts.mu.Unlock()
			ack := map[string]interface{}{
				"type":            "conversation_subscribed",
				"conversation_id": convID,
				"timestamp":       time.Now().Format(time.RFC3339Nano),
			}
			_ = sc.writeJSON(ack)
		}
	}
}

// pushEvent injects a server-side event to all clients subscribed to
// convID (mimicking moses-backend's sendToSubscribedConnections).
func (ts *wsTestServer) pushEvent(convID string, ev map[string]interface{}) {
	ts.mu.Lock()
	conns := append([]*safeConn(nil), ts.connsByConv[convID]...)
	ts.mu.Unlock()
	for _, c := range conns {
		_ = c.writeJSON(ev)
	}
}

func (ts *wsTestServer) dropAllConns() {
	ts.mu.Lock()
	conns := ts.allConns
	ts.allConns = nil
	ts.connsByConv = map[string][]*safeConn{}
	ts.mu.Unlock()
	for _, c := range conns {
		_ = c.conn.Close()
	}
}

// TestWSConnectSubscribeReceive walks the happy path:
//
//	connect → subscribe(conversation) → receive ack + chunk + complete.
func TestWSConnectSubscribeReceive(t *testing.T) {
	ts := newWSTestServer(t)
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	sub, err := NewWSSubscriber(ctx, ts.URL(), "mcp-token-1", WSConfig{
		MaxRetries:  3,
		BackoffBase: 10 * time.Millisecond,
		BackoffCap:  50 * time.Millisecond,
		EventBuffer: 16,
	})
	require.NoError(t, err)
	defer sub.Close()

	require.NoError(t, sub.Subscribe("conversation", "conv-1"))

	// Drain the ack first.
	ack := mustReceive(t, sub.Events(), 2*time.Second)
	assert.Equal(t, "conversation_subscribed", ack.Type)
	assert.Equal(t, "conv-1", ack.ConversationID)

	// Server pushes a chunk and complete.
	ts.pushEvent("conv-1", map[string]interface{}{
		"type":            "assistant_message_chunk",
		"conversation_id": "conv-1",
		"message":         map[string]string{"content": "Hello "},
		"timestamp":       time.Now().Format(time.RFC3339Nano),
	})
	ts.pushEvent("conv-1", map[string]interface{}{
		"type":            "assistant_message_chunk",
		"conversation_id": "conv-1",
		"message":         map[string]string{"content": "world"},
		"timestamp":       time.Now().Format(time.RFC3339Nano),
	})
	ts.pushEvent("conv-1", map[string]interface{}{
		"type":            "assistant_message_complete",
		"conversation_id": "conv-1",
		"timestamp":       time.Now().Format(time.RFC3339Nano),
	})

	chunk1 := mustReceive(t, sub.Events(), 2*time.Second)
	assert.Equal(t, "assistant_message_chunk", chunk1.Type)
	chunk2 := mustReceive(t, sub.Events(), 2*time.Second)
	assert.Equal(t, "assistant_message_chunk", chunk2.Type)
	done := mustReceive(t, sub.Events(), 2*time.Second)
	assert.Equal(t, "assistant_message_complete", done.Type)

	// Token reached the server query param.
	ts.mu.Lock()
	defer ts.mu.Unlock()
	require.NotEmpty(t, ts.gotTokens)
	assert.Equal(t, "mcp-token-1", ts.gotTokens[0])
}

// TestWS_AuthFailureSurfacesSync verifies a 401 on the WS handshake
// surfaces as ErrWSAuthFailed from NewWSSubscriber (no retry).
func TestWS_AuthFailureSurfacesSync(t *testing.T) {
	ts := newWSTestServer(t)
	defer ts.Close()
	ts.mu.Lock()
	ts.rejectAuth = true
	ts.mu.Unlock()

	_, err := NewWSSubscriber(context.Background(), ts.URL(), "mcp-bad", WSConfig{
		MaxRetries:  2,
		BackoffBase: 5 * time.Millisecond,
		BackoffCap:  10 * time.Millisecond,
	})
	require.Error(t, err)
	assert.True(t, errIs(err, ErrWSAuthFailed))
}

// TestWS_ReconnectWithBackoff verifies the subscriber transparently
// reconnects after the server drops the connection and re-sends the
// remembered subscription.
func TestWS_ReconnectWithBackoff(t *testing.T) {
	ts := newWSTestServer(t)
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	sub, err := NewWSSubscriber(ctx, ts.URL(), "mcp-1", WSConfig{
		MaxRetries:  3,
		BackoffBase: 10 * time.Millisecond,
		BackoffCap:  20 * time.Millisecond,
		EventBuffer: 32,
	})
	require.NoError(t, err)
	defer sub.Close()

	require.NoError(t, sub.Subscribe("conversation", "conv-1"))
	// drain ack
	mustReceive(t, sub.Events(), 1*time.Second)

	// Drop all server-side conns, mimicking a network blip.
	ts.dropAllConns()

	// Give the subscriber time to reconnect + re-subscribe.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		ts.mu.Lock()
		hasConn := len(ts.connsByConv["conv-1"]) > 0
		ts.mu.Unlock()
		if hasConn {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	ts.mu.Lock()
	hasConn := len(ts.connsByConv["conv-1"]) > 0
	ts.mu.Unlock()
	require.True(t, hasConn, "subscriber should re-subscribe after reconnect")

	// Push an event post-reconnect — caller should receive it.
	ts.pushEvent("conv-1", map[string]interface{}{
		"type":            "assistant_message_chunk",
		"conversation_id": "conv-1",
		"message":         map[string]string{"content": "after reconnect"},
		"timestamp":       time.Now().Format(time.RFC3339Nano),
	})

	// Drain ack from the re-subscribe + the chunk.
	gotChunk := false
	for i := 0; i < 4 && !gotChunk; i++ {
		ev := mustReceive(t, sub.Events(), 1*time.Second)
		if ev.Type == "assistant_message_chunk" {
			gotChunk = true
			var msg map[string]string
			require.NoError(t, json.Unmarshal(ev.Message, &msg))
			assert.Equal(t, "after reconnect", msg["content"])
		}
	}
	assert.True(t, gotChunk, "expected a post-reconnect chunk")
}

// TestWS_GivesUpAfterMaxRetries verifies the subscriber emits an error
// event and closes after exceeding MaxRetries.
func TestWS_GivesUpAfterMaxRetries(t *testing.T) {
	ts := newWSTestServer(t)
	defer ts.Close()

	// Make every dial accept then immediately drop the conn so the
	// subscriber sees the read pump return repeatedly.
	ts.dropOnAccept.Store(true)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	sub, err := NewWSSubscriber(ctx, ts.URL(), "mcp-1", WSConfig{
		MaxRetries:  2, // very small for fast test
		BackoffBase: 5 * time.Millisecond,
		BackoffCap:  10 * time.Millisecond,
	})
	require.NoError(t, err, "initial dial succeeded; failures happen on read")
	defer sub.Close()

	// Wait for the events channel to close, signalling permanent
	// disconnect.
	gotDisconnect := false
	timeout := time.After(2 * time.Second)
	for {
		select {
		case ev, ok := <-sub.Events():
			if !ok {
				goto Done
			}
			if ev.Type == "error" {
				var payload map[string]string
				if json.Unmarshal(ev.Message, &payload) == nil &&
					strings.Contains(payload["error"], "websocket disconnected") {
					gotDisconnect = true
				}
			}
		case <-timeout:
			t.Fatal("subscriber did not close after MaxRetries exceeded")
		}
	}
Done:
	assert.True(t, gotDisconnect, "should have emitted ErrWSDisconnected before closing")
}

// TestWS_BuildSubscribeFrame covers each supported topic shape so
// the wire stays in sync with websocket_handlers.go.
func TestWS_BuildSubscribeFrame(t *testing.T) {
	cases := []struct {
		topic, topicID string
		wantType, idK  string
	}{
		{"conversation", "c1", "subscribe_conversation", "conversation_id"},
		{"chart", "ch1", "subscribe_chart", "chart_id"},
		{"execution", "e1", "subscribe_execution", "execution_id"},
		{"chart_meta", "ch1", "subscribe_chart_meta", "chart_id"},
		{"repo", "r1", "subscribe_repo", "repo_id"},
		{"deployments", "ch1", "subscribe_deployments", "chart_id"},
	}
	for _, tc := range cases {
		t.Run(tc.topic, func(t *testing.T) {
			f, err := buildSubscribeFrame(tc.topic, tc.topicID)
			require.NoError(t, err)
			assert.Equal(t, tc.wantType, f["type"])
			assert.Equal(t, tc.topicID, f[tc.idK])
		})
	}

	_, err := buildSubscribeFrame("garbage", "x")
	require.Error(t, err)
}

// TestWS_BuildURLRewrites verifies http/https → ws/wss rewrite.
func TestWS_BuildURLRewrites(t *testing.T) {
	cases := map[string]string{
		"http://x":              "ws://x/api/v1/ai/ws?token=t",
		"https://x":             "wss://x/api/v1/ai/ws?token=t",
		"ws://x":                "ws://x/api/v1/ai/ws?token=t",
		"wss://x":               "wss://x/api/v1/ai/ws?token=t",
		"http://x:8080/prefix/": "ws://x:8080/prefix/api/v1/ai/ws?token=t",
	}
	for in, want := range cases {
		t.Run(in, func(t *testing.T) {
			got, err := buildWSURL(in, "t")
			require.NoError(t, err)
			assert.Equal(t, want, got)
		})
	}
}

// mustReceive blocks until an event arrives or timeout fires.
func mustReceive(t *testing.T, ch <-chan WSEvent, timeout time.Duration) WSEvent {
	t.Helper()
	select {
	case ev, ok := <-ch:
		if !ok {
			t.Fatal("events channel closed unexpectedly")
		}
		return ev
	case <-time.After(timeout):
		t.Fatalf("timed out after %s waiting for event", timeout)
		return WSEvent{}
	}
}
