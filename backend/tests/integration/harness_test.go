// Package integration_test contains cross-package end-to-end tests for the
// moses-chat-bot backend. It wires the real *db.Store (testcontainer or
// TEST_DATABASE_URL Postgres), the real linker / autopilot / relay services,
// a stubbed Telegram provider (providertest.InMemoryProvider), and a stubbed
// moses-backend (httptest server impersonating the platform's HTTP + WS
// surface).
//
// Scenarios live in e2e_test.go. The harness here only sets up shared state.
//
// Why a separate test directory (and not //go:build integration): keeping
// the file under backend/tests/integration/ lets `go test ./...` pick it up
// by default in CI and locally while still being clearly demarcated from
// the per-package unit tests. The cost is the cross-cluster import path
// for internal packages, which Go allows because both live under the same
// module.
package integration_test

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"moses-chat-bot/backend/internal/db"
	"moses-chat-bot/backend/internal/handler"
	"moses-chat-bot/backend/internal/handler/middleware"
	"moses-chat-bot/backend/internal/mosesclient"
	"moses-chat-bot/backend/internal/provider"
	"moses-chat-bot/backend/internal/provider/providertest"
	"moses-chat-bot/backend/internal/service/autopilot"
	"moses-chat-bot/backend/internal/service/crypto"
	"moses-chat-bot/backend/internal/service/linker"
	"moses-chat-bot/backend/internal/service/relay"
)

// ---------------------------------------------------------------------------
// Postgres testcontainer (singleton across tests in this package)
// ---------------------------------------------------------------------------

var (
	intgPool     *pgxpool.Pool
	intgPoolOnce sync.Once
	intgPoolErr  error
	intgCleanup  func()
)

func setupIntegrationDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	intgPoolOnce.Do(func() {
		ctx := context.Background()
		if dsn := os.Getenv("TEST_DATABASE_URL"); dsn != "" {
			pool, err := db.Open(ctx, dsn)
			if err != nil {
				intgPoolErr = err
				return
			}
			intgPool = pool
			intgCleanup = func() { pool.Close() }
			return
		}
		container, err := tcpostgres.Run(
			ctx,
			"postgres:16-alpine",
			tcpostgres.WithDatabase("moseschatbot"),
			tcpostgres.WithUsername("test"),
			tcpostgres.WithPassword("test"),
			tcpostgres.BasicWaitStrategies(),
			tcpostgres.WithSQLDriver("pgx"),
		)
		if err != nil {
			intgPoolErr = err
			return
		}
		dsn, err := container.ConnectionString(ctx, "sslmode=disable")
		if err != nil {
			intgPoolErr = err
			_ = container.Terminate(ctx)
			return
		}
		readyCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		strat := wait.ForListeningPort("5432/tcp").WithStartupTimeout(20 * time.Second)
		if err := strat.WaitUntilReady(readyCtx, container); err != nil {
			intgPoolErr = err
			_ = container.Terminate(ctx)
			return
		}
		pool, err := db.Open(ctx, dsn)
		if err != nil {
			intgPoolErr = err
			_ = container.Terminate(ctx)
			return
		}
		intgPool = pool
		intgCleanup = func() {
			pool.Close()
			_ = container.Terminate(context.Background())
		}
	})
	if intgPoolErr != nil {
		t.Skipf("integration test DB not available: %v", intgPoolErr)
	}
	if intgPool == nil {
		t.Skip("integration test DB not available")
	}
	return intgPool
}

// resetIntegrationDB drops and re-applies the schema. Cheap (4 tables) and
// guarantees per-test isolation when called from each test's setup.
func resetIntegrationDB(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	const dropSQL = `
		DROP TABLE IF EXISTS provider_chat_state CASCADE;
		DROP TABLE IF EXISTS chat_relay_messages CASCADE;
		DROP TABLE IF EXISTS chat_relay_links CASCADE;
		DROP TABLE IF EXISTS pending_links CASCADE;
		DROP TABLE IF EXISTS schema_migrations CASCADE;
	`
	_, err := pool.Exec(ctx, dropSQL)
	require.NoError(t, err)
	require.NoError(t, db.ApplySchema(ctx, pool))
}

func TestMain(m *testing.M) {
	code := m.Run()
	if intgCleanup != nil {
		intgCleanup()
	}
	os.Exit(code)
}

// ---------------------------------------------------------------------------
// Fake moses-backend (HTTP)
// ---------------------------------------------------------------------------

// mosesBackendStub impersonates the platform's HTTP surface that the bot
// calls into. Each handler reads from the embedded *mockState struct so
// individual tests can flip behaviours (e.g. force 401, set active
// session) without standing up a new server.
type mosesBackendStub struct {
	state *mockState
	srv   *httptest.Server
}

type mockState struct {
	mu sync.Mutex

	// API key minting (frontend would call this; bot does NOT, but the
	// harness exposes it for completeness so a future end-to-end test
	// can pretend to be the frontend).
	mintKey   string
	mintKeyID uuid.UUID
	mintErr   int // HTTP status to return on /api/v1/api-keys; 0 = 201
	mintCount int // number of calls (for rate-limit tests)

	// Chat / streaming
	chatStreamStatus int // returned on POST /ai/chat/stream; 0 = 200
	chatSyncMessage  string

	// Conversations
	createConversationStatus int

	// Autonomous
	activeSession *mosesclient.AutonomousSession // returned by GET /autonomous/active; nil → 404
	startSession  *mosesclient.AutonomousSession // returned by POST /autonomous/start
	startStatus   int                            // 0 = 200
	getSession    *mosesclient.AutonomousSession // returned by GET /autonomous/:id
	getStatus     int
	stopStatus    int

	// Recorded calls (for assertions)
	streamCalls         int
	syncCalls           int
	createConvCalls     int
	startAutoCalls      int
	stopAutoCalls       int
	deleteAPIKeyCalls   int

	// Last request payloads
	lastStreamConv  string
	lastStreamMsg   string
}

// stateSnapshot is the lock-free view returned by mockState.snapshot. It
// holds the counters and last-call fields tests assert on; mutex-bearing
// configuration knobs stay on the parent struct.
type stateSnapshot struct {
	mintCount         int
	streamCalls       int
	syncCalls         int
	createConvCalls   int
	startAutoCalls    int
	stopAutoCalls     int
	deleteAPIKeyCalls int
	lastStreamConv    string
	lastStreamMsg     string
}

func (m *mockState) snapshot() stateSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	return stateSnapshot{
		mintCount:         m.mintCount,
		streamCalls:       m.streamCalls,
		syncCalls:         m.syncCalls,
		createConvCalls:   m.createConvCalls,
		startAutoCalls:    m.startAutoCalls,
		stopAutoCalls:     m.stopAutoCalls,
		deleteAPIKeyCalls: m.deleteAPIKeyCalls,
		lastStreamConv:    m.lastStreamConv,
		lastStreamMsg:     m.lastStreamMsg,
	}
}

// newMosesBackendStub builds the fake HTTP server. The WS endpoint is mounted
// on a SEPARATE httptest.Server (see newMosesWSStub) because the bot wires
// WSPool with the dialer baseURL; we use the same base URL for HTTP and WS
// by combining via mux.
func newMosesBackendStub(t *testing.T) *mosesBackendStub {
	t.Helper()
	state := &mockState{
		mintKeyID:    uuid.New(),
		mintKey:      "mcp-test-" + uuid.NewString(),
		chatSyncMessage: "sync fallback reply",
	}
	stub := &mosesBackendStub{state: state}

	mux := http.NewServeMux()

	// POST /api/v1/api-keys — frontend-style key mint
	mux.HandleFunc("POST /api/v1/api-keys", func(w http.ResponseWriter, r *http.Request) {
		state.mu.Lock()
		state.mintCount++
		stat := state.mintErr
		key := state.mintKey
		kid := state.mintKeyID
		state.mu.Unlock()
		if stat != 0 {
			w.WriteHeader(stat)
			_, _ = w.Write([]byte(`{"error":"rate_limited"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"key": key,
			"id":  kid.String(),
		})
	})

	// DELETE /api/v1/api-keys/{id}
	mux.HandleFunc("DELETE /api/v1/api-keys/", func(w http.ResponseWriter, _ *http.Request) {
		state.mu.Lock()
		state.deleteAPIKeyCalls++
		state.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})

	// POST /api/v1/chat/conversations
	mux.HandleFunc("POST /api/v1/chat/conversations", func(w http.ResponseWriter, _ *http.Request) {
		state.mu.Lock()
		state.createConvCalls++
		stat := state.createConversationStatus
		state.mu.Unlock()
		if stat >= 400 {
			w.WriteHeader(stat)
			return
		}
		conv := mosesclient.Conversation{
			ID:        uuid.New(),
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"conversation": conv})
	})

	// POST /api/v1/ai/chat/stream
	mux.HandleFunc("POST /api/v1/ai/chat/stream", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Message        string `json:"message"`
			ConversationID string `json:"conversationId"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		state.mu.Lock()
		state.streamCalls++
		state.lastStreamConv = body.ConversationID
		state.lastStreamMsg = body.Message
		stat := state.chatStreamStatus
		state.mu.Unlock()
		if stat >= 400 {
			w.WriteHeader(stat)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(mosesclient.ChatStreamAck{
			Status:         "processing",
			ConversationID: body.ConversationID,
		})
	})

	// POST /api/v1/ai/chat — sync fallback
	mux.HandleFunc("POST /api/v1/ai/chat", func(w http.ResponseWriter, _ *http.Request) {
		state.mu.Lock()
		state.syncCalls++
		msg := state.chatSyncMessage
		state.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(mosesclient.ChatSyncResponse{
			Message: msg,
			Role:    "assistant",
		})
	})

	// Autonomous endpoints
	mux.HandleFunc("GET /api/v1/autonomous/active", func(w http.ResponseWriter, _ *http.Request) {
		state.mu.Lock()
		s := state.activeSession
		state.mu.Unlock()
		if s == nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(s)
	})
	mux.HandleFunc("POST /api/v1/autonomous/start", func(w http.ResponseWriter, _ *http.Request) {
		state.mu.Lock()
		state.startAutoCalls++
		stat := state.startStatus
		s := state.startSession
		state.mu.Unlock()
		if stat >= 400 {
			w.WriteHeader(stat)
			return
		}
		if s == nil {
			s = &mosesclient.AutonomousSession{
				ID:        uuid.New(),
				Mode:      "freeform",
				Status:    "active",
				CreatedAt: time.Now(),
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(s)
	})
	mux.HandleFunc("POST /api/v1/autonomous/{id}/stop", func(w http.ResponseWriter, _ *http.Request) {
		state.mu.Lock()
		state.stopAutoCalls++
		stat := state.stopStatus
		state.mu.Unlock()
		if stat == 0 {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(stat)
	})
	mux.HandleFunc("GET /api/v1/autonomous/{id}", func(w http.ResponseWriter, _ *http.Request) {
		state.mu.Lock()
		s := state.getSession
		stat := state.getStatus
		state.mu.Unlock()
		if stat >= 400 {
			w.WriteHeader(stat)
			return
		}
		if s == nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(s)
	})

	// GET /api/v1/auth/me — for middleware.RequireUser if any test uses it.
	// Not used in current scenarios but kept for completeness.
	mux.HandleFunc("GET /api/v1/auth/me", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":                "00000000-0000-0000-0000-000000000001",
			"email":             "test@moses.local",
			"isGlobalAdmin":     false,
			"tenantMemberships": []any{},
		})
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	stub.srv = srv
	return stub
}

func (s *mosesBackendStub) URL() string {
	return s.srv.URL
}

// ---------------------------------------------------------------------------
// Fake WS subscriber (controlled by the harness)
// ---------------------------------------------------------------------------

// fakeWSSubscriber lets a test push WS events into the inbound aggregator.
// One instance per (link.ID, conversation) is created on demand by the
// dialer wrapper below.
type fakeWSSubscriber struct {
	mu        sync.Mutex
	events    chan mosesclient.WSEvent
	closed    bool
	subscribed map[string]bool
}

func newFakeWSSubscriber(buffer int) *fakeWSSubscriber {
	if buffer <= 0 {
		buffer = 16
	}
	return &fakeWSSubscriber{
		events:     make(chan mosesclient.WSEvent, buffer),
		subscribed: make(map[string]bool),
	}
}

func (s *fakeWSSubscriber) Subscribe(_, topicID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.subscribed[topicID] = true
	return nil
}

func (s *fakeWSSubscriber) Events() <-chan mosesclient.WSEvent { return s.events }

func (s *fakeWSSubscriber) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.closed = true
		close(s.events)
	}
	return nil
}

func (s *fakeWSSubscriber) push(ev mosesclient.WSEvent) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	s.events <- ev
}

// wsRouter holds per-bearer fake subscribers so the harness can route
// pushes to the right link. Production calls Dial once per link with the
// link's bearer token — the router returns (and remembers) one subscriber
// per bearer string.
type wsRouter struct {
	mu  sync.Mutex
	byBearer map[string]*fakeWSSubscriber
	byConv   map[string]*fakeWSSubscriber
	lastBearer atomic.Value // string — for tests that just want the latest
}

func newWSRouter() *wsRouter {
	return &wsRouter{
		byBearer: map[string]*fakeWSSubscriber{},
		byConv:   map[string]*fakeWSSubscriber{},
	}
}

// dialer returns a relay.WSDialer that lazily creates a fakeWSSubscriber
// per bearer and remembers it for the test's pushEvent helper.
func (r *wsRouter) dialer() relay.WSDialer {
	return func(_ context.Context, _ string, token string, _ mosesclient.WSConfig) (relay.Subscriber, error) {
		r.mu.Lock()
		defer r.mu.Unlock()
		if sub, ok := r.byBearer[token]; ok {
			return sub, nil
		}
		sub := newFakeWSSubscriber(32)
		r.byBearer[token] = sub
		r.lastBearer.Store(token)
		return sub, nil
	}
}

// rememberConv tags the subscriber with a conversation id so subsequent
// pushes can be addressed by conversation rather than by bearer (more
// natural for tests that don't know the bearer string).
func (r *wsRouter) rememberConv(bearer, conv string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if sub, ok := r.byBearer[bearer]; ok {
		r.byConv[conv] = sub
	}
}

func (r *wsRouter) subscriberForConv(conv string) *fakeWSSubscriber {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.byConv[conv]
}

func (r *wsRouter) subscriberForBearer(bearer string) *fakeWSSubscriber {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.byBearer[bearer]
}

// pushChunk pushes an assistant_message_chunk into the subscriber that
// matches the conversation id. Eventually-polls for up to 2s for the
// subscriber to exist (the bot dials on first chat).
func (r *wsRouter) pushChunk(t *testing.T, convStr, text string) {
	t.Helper()
	sub := r.waitForConv(t, convStr, 2*time.Second)
	sub.push(mosesclient.WSEvent{
		Type:           "assistant_message_chunk",
		ConversationID: convStr,
		Message:        []byte(fmt.Sprintf(`{"content":%q}`, text)),
	})
}

func (r *wsRouter) pushComplete(t *testing.T, convStr string) {
	t.Helper()
	sub := r.waitForConv(t, convStr, 2*time.Second)
	sub.push(mosesclient.WSEvent{
		Type:           "assistant_message_complete",
		ConversationID: convStr,
	})
}

func (r *wsRouter) waitForConv(t *testing.T, conv string, timeout time.Duration) *fakeWSSubscriber {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if sub := r.subscriberForConv(conv); sub != nil {
			return sub
		}
		// Also try mapping latest bearer → conv lazily.
		if bearer, ok := r.lastBearer.Load().(string); ok && bearer != "" {
			if sub := r.subscriberForBearer(bearer); sub != nil {
				r.rememberConv(bearer, conv)
				return sub
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no WS subscriber appeared for conversation %s within %s", conv, timeout)
	return nil
}

// ---------------------------------------------------------------------------
// Harness
// ---------------------------------------------------------------------------

// Harness bundles the full system under test.
type Harness struct {
	Ctx           context.Context
	Cancel        context.CancelFunc
	Pool          *pgxpool.Pool
	Store         *db.Store
	Envelope      *crypto.Envelope
	Telegram      *providertest.InMemoryProvider
	Registry      *provider.Registry
	Sender        *relay.Sender
	Linker        *linker.Linker
	Autopilot     *autopilot.Service
	Inbound       *relay.Inbound
	WSPool        relayWSPool
	Backend       *mosesBackendStub
	WS            *wsRouter
	LinksHandler  *handler.Links
	PushHandler   *handler.Push

	// HTTP servers (started lazily by the helper getters)
	linksSrv *httptest.Server
	pushSrv  *httptest.Server
}

// relayWSPool is a minimal interface so the harness can expose just enough
// of the pool to tests without re-exporting the internal struct.
type relayWSPool interface {
	Stop()
}

// newHarness builds a fully-wired test fixture. Each test resets the DB
// at the top so cross-test pollution is impossible.
func newHarness(t *testing.T) *Harness {
	t.Helper()
	pool := setupIntegrationDB(t)
	resetIntegrationDB(t, pool)

	store := db.NewStore(pool)
	env := newEnvelope(t)
	tg := providertest.NewInMemoryProvider("telegram")
	registry := provider.NewRegistry()
	require.NoError(t, registry.Register(tg))

	backend := newMosesBackendStub(t)
	wsRouter := newWSRouter()

	// linker needs a mosesclient pointed at the stub so RevokeAPIKey (best
	// effort on /unlink) lands on the stub's DELETE handler.
	mosesClient := mosesclient.NewClient(backend.URL(), mosesclient.BearerAuth{Token: "platform-admin"})
	lk := linker.New(store, env, mosesClient)

	sender := relay.NewSender(store, registry, relay.SenderOpts{
		// Per-link cap of 50 → enough for our tests, low enough that the
		// rate-limit scenario can still exercise a deliberate trip.
		PerLinkPerMinute: 50,
	})

	wsPool := relay.NewWSConnPool(relay.WSPoolConfig{
		BaseWS:  backend.URL(),
		IdleTTL: 5 * time.Minute,
		Dialer:  wsRouter.dialer(),
		SubscriberConfig: mosesclient.WSConfig{
			HandshakeTimeout: 100 * time.Millisecond,
			MaxRetries:       1,
			BackoffBase:      10 * time.Millisecond,
			BackoffCap:       100 * time.Millisecond,
			EventBuffer:      32,
		},
	})

	chatFactory := func(bearer string) relay.PerKeyChatClient {
		return mosesclient.NewClient(backend.URL(), mosesclient.BearerAuth{Token: bearer})
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	inbound := relay.NewInbound(
		store, sender, env, lk, registry, chatFactory, wsPool,
		relay.InboundOpts{
			StreamTimeout: 2 * time.Second,
			Logger:        logger,
		},
	)

	autopilotFactory := func(bearer string) autopilot.MosesClient {
		return mosesclient.NewClient(backend.URL(), mosesclient.BearerAuth{Token: bearer})
	}
	autoSvc := autopilot.New(store, autopilotFactory, env, sender, logger)
	inbound.Autopilot = autoSvc

	ctx, cancel := context.WithCancel(context.Background())
	h := &Harness{
		Ctx:          ctx,
		Cancel:       cancel,
		Pool:         pool,
		Store:        store,
		Envelope:     env,
		Telegram:     tg,
		Registry:     registry,
		Sender:       sender,
		Linker:       lk,
		Autopilot:    autoSvc,
		Inbound:      inbound,
		WSPool:       wsPool,
		Backend:      backend,
		WS:           wsRouter,
		LinksHandler: handler.NewLinks(lk, store),
		PushHandler:  handler.NewPush(store, sender),
	}
	t.Cleanup(func() {
		cancel()
		wsPool.Stop()
	})
	return h
}

// userLinksServer stands up an httptest.Server backed by the harness's
// LinksHandler, stamping the supplied identity on every request via the
// test-only middleware mirror.
func (h *Harness) userLinksServer(t *testing.T, userID, tenantID uuid.UUID) *httptest.Server {
	t.Helper()
	protected := http.NewServeMux()
	h.LinksHandler.Register(protected)
	root := http.NewServeMux()
	wrapped := stampIdentity(userID, tenantID)(protected)
	root.Handle("/api/v1/links/", wrapped)
	root.Handle("/api/v1/links", wrapped)
	srv := httptest.NewServer(root)
	t.Cleanup(srv.Close)
	return srv
}

// pushServer stands up the workspace-tool httptest.Server.
func (h *Harness) pushServer(t *testing.T) *httptest.Server {
	t.Helper()
	pushMux := http.NewServeMux()
	h.PushHandler.Register(pushMux)
	root := http.NewServeMux()
	root.Handle("/api/v1/", middleware.MosesHeaders(pushMux))
	srv := httptest.NewServer(root)
	t.Cleanup(srv.Close)
	return srv
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// stampIdentity mirrors the test helper from handler/links_test.go so the
// integration tests do not have to roundtrip through the platform's
// /api/v1/auth/me path.
func stampIdentity(userID, tenantID uuid.UUID) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			ctx = withCtx(ctx, middleware.UserIDKey, userID)
			ctx = withCtx(ctx, middleware.TenantIDKey, tenantID)
			ctx = withCtx(ctx, middleware.BearerKey, "test-bearer")
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// withCtx is a tiny indirection to keep stampIdentity readable; the
// alternative inline call chain is harder to scan.
func withCtx(ctx context.Context, key middleware.ContextKey, val any) context.Context {
	return context.WithValue(ctx, key, val)
}

// newEnvelope mints a fresh in-memory envelope identical to the one used
// by the unit-test packages.
func newEnvelope(t *testing.T) *crypto.Envelope {
	t.Helper()
	raw := make([]byte, 32)
	_, err := rand.Read(raw)
	require.NoError(t, err)
	mk := map[string]any{
		"keys":   map[string]string{"v1": base64.StdEncoding.EncodeToString(raw)},
		"active": "v1",
	}
	b, err := json.Marshal(mk)
	require.NoError(t, err)
	t.Setenv("CHAT_BOT_MASTER_KEY", string(b))
	keys, err := crypto.LoadMasterKeysFromEnv()
	require.NoError(t, err)
	env, err := crypto.NewEnvelope(keys)
	require.NoError(t, err)
	return env
}

// completeLinkE2E walks the full link lifecycle from the user-facing API
// down to chat_relay_links. Used by every scenario that needs a pre-linked
// user. Returns the resulting *db.ChatRelayLink.
func (h *Harness) completeLinkE2E(
	t *testing.T,
	tenantID, mosesUserID uuid.UUID,
	providerUserID, plaintextKey string,
) *db.ChatRelayLink {
	t.Helper()
	code, _, err := h.Linker.CreateCode(h.Ctx, tenantID, mosesUserID, plaintextKey, nil, 60*time.Second)
	require.NoError(t, err)
	h.Linker.RegisterKnown("telegram", providerUserID)
	link, err := h.Linker.CompleteLink(h.Ctx, code, "telegram", providerUserID)
	require.NoError(t, err)
	return link
}

// inboundMsg is a tiny builder for InboundMessage. The Provider field is
// always "telegram"; ProviderChatID defaults to providerUserID (1:1 chat).
func inboundMsg(providerUserID, text, providerMsgID string) provider.InboundMessage {
	return provider.InboundMessage{
		Provider:          "telegram",
		ProviderUserID:    providerUserID,
		ProviderChatID:    providerUserID,
		Text:              text,
		ProviderMessageID: providerMsgID,
		ReceivedAt:        time.Now(),
	}
}

// eventually polls fn until it returns true OR timeout fires. fail message
// uses msg for diagnostics. Mirrors testify/assert.Eventually but with a
// pass-through error so test code can branch.
func eventually(t *testing.T, fn func() bool, timeout time.Duration, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("eventually: %s", msg)
}

// ---------------------------------------------------------------------------
// JSON / HTTP helpers
// ---------------------------------------------------------------------------

func postJSON(t *testing.T, srv *httptest.Server, path string, body any, headers map[string]string) *http.Response {
	t.Helper()
	var rdr *strings.Reader
	if body != nil {
		b, err := json.Marshal(body)
		require.NoError(t, err)
		rdr = strings.NewReader(string(b))
	} else {
		rdr = strings.NewReader("")
	}
	req, err := http.NewRequest(http.MethodPost, srv.URL+path, rdr)
	require.NoError(t, err)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}

func decodeJSON(t *testing.T, resp *http.Response, out any) {
	t.Helper()
	defer resp.Body.Close()
	require.NoError(t, json.NewDecoder(resp.Body).Decode(out))
}

// urlPath is a tiny helper to keep query strings readable in tests.
func urlPath(base, p string, query map[string]string) string {
	u, _ := url.Parse(base + p)
	q := u.Query()
	for k, v := range query {
		q.Set(k, v)
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// silence unused imports that would otherwise show up depending on which
// scenarios end up compiled in.
var (
	_ = errors.New
	_ = io.Discard
)
