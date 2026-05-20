package botconfig

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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
	"moses-chat-bot/backend/internal/provider"
	"moses-chat-bot/backend/internal/provider/telegram"
	"moses-chat-bot/backend/internal/service/crypto"
)

// ---------------------------------------------------------------------------
// Test infrastructure
// ---------------------------------------------------------------------------

var (
	testPool     *pgxpool.Pool
	testPoolOnce sync.Once
	testPoolErr  error
	testCleanup  func()
)

func setupTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	testPoolOnce.Do(func() {
		ctx := context.Background()
		if dsn := os.Getenv("TEST_DATABASE_URL"); dsn != "" {
			pool, err := db.Open(ctx, dsn)
			if err != nil {
				testPoolErr = err
				return
			}
			testPool = pool
			testCleanup = func() { pool.Close() }
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
			testPoolErr = err
			return
		}
		dsn, err := container.ConnectionString(ctx, "sslmode=disable")
		if err != nil {
			testPoolErr = err
			_ = container.Terminate(ctx)
			return
		}
		readyCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		strat := wait.ForListeningPort("5432/tcp").WithStartupTimeout(20 * time.Second)
		if err := strat.WaitUntilReady(readyCtx, container); err != nil {
			testPoolErr = err
			_ = container.Terminate(ctx)
			return
		}
		pool, err := db.Open(ctx, dsn)
		if err != nil {
			testPoolErr = err
			_ = container.Terminate(ctx)
			return
		}
		testPool = pool
		testCleanup = func() {
			pool.Close()
			_ = container.Terminate(context.Background())
		}
	})
	if testPoolErr != nil {
		t.Skipf("test DB not available: %v", testPoolErr)
	}
	if testPool == nil {
		t.Skip("test DB not available")
	}
	return testPool
}

func resetDB(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	const dropSQL = `
		DROP TABLE IF EXISTS telegram_bot_config CASCADE;
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
	if testCleanup != nil {
		testCleanup()
	}
	os.Exit(code)
}

func newTestEnvelope(t *testing.T) *crypto.Envelope {
	t.Helper()
	raw := make([]byte, 32)
	_, err := rand.Read(raw)
	require.NoError(t, err)
	b, err := json.Marshal(map[string]interface{}{
		"keys":   map[string]string{"v1": base64.StdEncoding.EncodeToString(raw)},
		"active": "v1",
	})
	require.NoError(t, err)
	t.Setenv("CHAT_BOT_MASTER_KEY", string(b))
	mk, err := crypto.LoadMasterKeysFromEnv()
	require.NoError(t, err)
	env, err := crypto.NewEnvelope(mk)
	require.NoError(t, err)
	return env
}

// stubTelegram stands up an httptest server impersonating api.telegram.org.
// getMeOK toggles whether getMe returns a valid bot or a 401. All counters are
// accessed via sync/atomic so a running poll-loop goroutine cannot race the
// test assertions.
type stubTelegram struct {
	srv     *httptest.Server
	getMeOK bool

	setWebhookN atomic.Int32
	myCommandsN atomic.Int32
	delWebhookN atomic.Int32
	getUpdatesN atomic.Int32

	mu          sync.Mutex
	lastSecret  string
	lastWebhook string
}

func newStubTelegram(t *testing.T, getMeOK bool) *stubTelegram {
	t.Helper()
	s := &stubTelegram{getMeOK: getMeOK}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/getMe"):
			if !s.getMeOK {
				w.WriteHeader(http.StatusUnauthorized)
				fmt.Fprint(w, `{"ok":false,"error_code":401,"description":"Unauthorized"}`)
				return
			}
			fmt.Fprint(w, `{"ok":true,"result":{"id":555,"is_bot":true,"username":"moses_test_bot"}}`)
		case strings.HasSuffix(r.URL.Path, "/setWebhook"):
			s.setWebhookN.Add(1)
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			s.mu.Lock()
			if v, ok := body["secret_token"].(string); ok {
				s.lastSecret = v
			}
			if v, ok := body["url"].(string); ok {
				s.lastWebhook = v
			}
			s.mu.Unlock()
			fmt.Fprint(w, `{"ok":true,"result":true}`)
		case strings.HasSuffix(r.URL.Path, "/setMyCommands"):
			s.myCommandsN.Add(1)
			fmt.Fprint(w, `{"ok":true,"result":true}`)
		case strings.HasSuffix(r.URL.Path, "/deleteWebhook"):
			s.delWebhookN.Add(1)
			fmt.Fprint(w, `{"ok":true,"result":true}`)
		case strings.HasSuffix(r.URL.Path, "/getUpdates"):
			s.getUpdatesN.Add(1)
			// Empty batch keeps the poll loop idle without delivering work.
			fmt.Fprint(w, `{"ok":true,"result":[]}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *stubTelegram) webhookTarget() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastWebhook
}

func (s *stubTelegram) webhookSecret() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastSecret
}

// newService builds a botconfig.Service whose adapters point at the stub.
func newService(t *testing.T, pool *pgxpool.Pool, env *crypto.Envelope, stub *stubTelegram) (*Service, *provider.Registry) {
	t.Helper()
	reg := provider.NewRegistry()
	svc := New(db.NewStore(pool), env, reg, nil)
	svc.SetAdapterBuilder(func(token, webhookSecret string) (*telegram.Adapter, error) {
		return telegram.New(telegram.Config{
			BotToken:      token,
			WebhookSecret: webhookSecret,
			BaseURL:       stub.srv.URL,
		})
	})
	return svc, reg
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestConnect_ValidToken_EncryptsAtRest(t *testing.T) {
	pool := setupTestDB(t)
	resetDB(t, pool)
	env := newTestEnvelope(t)
	stub := newStubTelegram(t, true)
	svc, reg := newService(t, pool, env, stub)
	ctx := context.Background()

	tenant := uuid.New()
	user := uuid.New()
	const token = "123456:ABC-secret-bot-token"

	info, err := svc.Connect(ctx, tenant, user, token, "https://moses.example/apps/t/moses-chat-bot")
	require.NoError(t, err)
	require.True(t, info.Configured)
	require.Equal(t, "moses_test_bot", info.Username)

	// Default (long-polling) mode: Connect drops any webhook and does NOT
	// register one — getUpdates and a webhook are mutually exclusive.
	require.Equal(t, int32(1), stub.delWebhookN.Load(), "default mode must deleteWebhook")
	require.Equal(t, int32(0), stub.setWebhookN.Load(), "default mode must NOT call setWebhook")
	require.Equal(t, int32(1), stub.myCommandsN.Load())

	// Token is stored ENCRYPTED — never plaintext.
	store := db.NewStore(pool)
	cfg, err := store.GetBotConfig(ctx, tenant)
	require.NoError(t, err)
	require.NotEqual(t, []byte(token), cfg.EncryptedToken, "token must not be stored in plaintext")
	require.NotContains(t, string(cfg.EncryptedToken), token)

	// Round-trips back to the original token under the tenant DEK.
	plain, err := env.Decrypt(tenant, cfg.EncryptedToken, cfg.EncryptionKeyID)
	require.NoError(t, err)
	require.Equal(t, token, string(plain))

	// The live adapter is registered.
	adapter := svc.ActiveAdapter()
	require.NotNil(t, adapter)
	_, registered := reg.Get(telegram.ProviderName)
	require.True(t, registered, "adapter must be in the shared registry for the relay sender")
}

// TestConnect_WebhookMode_OptIn proves that with BOT_WEBHOOK_PUBLIC_URL set,
// Connect registers a webhook (not a poll loop): setWebhook is called with the
// configured base URL + path, and the secret Telegram echoes is the stored one.
func TestConnect_WebhookMode_OptIn(t *testing.T) {
	pool := setupTestDB(t)
	resetDB(t, pool)
	env := newTestEnvelope(t)
	stub := newStubTelegram(t, true)
	svc, _ := newService(t, pool, env, stub)
	svc.SetWebhookPublicURL("https://bot.example.com/apps/t/moses-chat-bot")
	ctx := context.Background()

	tenant := uuid.New()
	_, err := svc.Connect(ctx, tenant, uuid.New(), "tok", "https://ignored.example")
	require.NoError(t, err)

	require.Equal(t, int32(1), stub.setWebhookN.Load(), "webhook mode must call setWebhook")
	require.Equal(t, int32(0), stub.delWebhookN.Load(), "webhook mode must NOT call deleteWebhook")
	require.Equal(t,
		"https://bot.example.com/apps/t/moses-chat-bot"+telegram.WebhookPath(),
		stub.webhookTarget())

	store := db.NewStore(pool)
	cfg, err := store.GetBotConfig(ctx, tenant)
	require.NoError(t, err)
	require.Equal(t, cfg.WebhookSecret, stub.webhookSecret())

	adapter := svc.ActiveAdapter()
	require.NotNil(t, adapter)
	require.Equal(t, cfg.WebhookSecret, adapter.WebhookSecret())
}

// TestConnect_DefaultMode_StartsPollLoop proves that in the default mode a
// wired dispatcher receives traffic via a poll loop Connect launched — i.e.
// getUpdates is being called against Telegram with no webhook in play.
func TestConnect_DefaultMode_StartsPollLoop(t *testing.T) {
	pool := setupTestDB(t)
	resetDB(t, pool)
	env := newTestEnvelope(t)
	stub := newStubTelegram(t, true)
	svc, _ := newService(t, pool, env, stub)
	svc.SetInboundDispatcher(noopDispatcher{})
	ctx := context.Background()

	tenant := uuid.New()
	_, err := svc.Connect(ctx, tenant, uuid.New(), "tok", "https://moses.example")
	require.NoError(t, err)

	// The poll loop runs in a goroutine; wait for it to issue getUpdates.
	require.Eventually(t, func() bool {
		return stub.getUpdatesN.Load() >= 1
	}, 3*time.Second, 20*time.Millisecond, "poll loop must call getUpdates")
	require.Equal(t, int32(0), stub.setWebhookN.Load(), "long-polling mode never sets a webhook")

	// Disconnect stops the loop cleanly.
	require.NoError(t, svc.Disconnect(ctx, tenant))
}

// noopDispatcher is a telegram.InboundDispatcher that swallows every message.
type noopDispatcher struct{}

func (noopDispatcher) HandleInbound(context.Context, provider.InboundMessage) error { return nil }

func TestConnect_InvalidToken_Rejected(t *testing.T) {
	pool := setupTestDB(t)
	resetDB(t, pool)
	env := newTestEnvelope(t)
	stub := newStubTelegram(t, false) // getMe returns 401
	svc, _ := newService(t, pool, env, stub)
	ctx := context.Background()

	tenant := uuid.New()
	user := uuid.New()

	_, err := svc.Connect(ctx, tenant, user, "bogus-token", "https://moses.example")
	require.ErrorIs(t, err, ErrInvalidToken)

	// Nothing persisted, no adapter registered — a bad token is a clean reject.
	store := db.NewStore(pool)
	_, gerr := store.GetBotConfig(ctx, tenant)
	require.True(t, db.IsNoRows(gerr))
	require.Nil(t, svc.ActiveAdapter())
}

func TestConnect_EmptyToken(t *testing.T) {
	pool := setupTestDB(t)
	resetDB(t, pool)
	env := newTestEnvelope(t)
	stub := newStubTelegram(t, true)
	svc, _ := newService(t, pool, env, stub)

	_, err := svc.Connect(context.Background(), uuid.New(), uuid.New(), "   ", "https://moses.example")
	require.ErrorIs(t, err, ErrEmptyToken)
}

func TestInfo_ReflectsConnectionState(t *testing.T) {
	pool := setupTestDB(t)
	resetDB(t, pool)
	env := newTestEnvelope(t)
	stub := newStubTelegram(t, true)
	svc, _ := newService(t, pool, env, stub)
	ctx := context.Background()

	tenant := uuid.New()

	// Not configured.
	info, err := svc.Info(ctx, tenant)
	require.NoError(t, err)
	require.False(t, info.Configured)
	require.Empty(t, info.Username)

	// After connect.
	_, err = svc.Connect(ctx, tenant, uuid.New(), "tok", "https://moses.example")
	require.NoError(t, err)
	info, err = svc.Info(ctx, tenant)
	require.NoError(t, err)
	require.True(t, info.Configured)
	require.Equal(t, "moses_test_bot", info.Username)
}

func TestDisconnect_RemovesConfigAndAdapter(t *testing.T) {
	pool := setupTestDB(t)
	resetDB(t, pool)
	env := newTestEnvelope(t)
	stub := newStubTelegram(t, true)
	svc, reg := newService(t, pool, env, stub)
	ctx := context.Background()

	tenant := uuid.New()
	_, err := svc.Connect(ctx, tenant, uuid.New(), "tok", "https://moses.example")
	require.NoError(t, err)
	require.NotNil(t, svc.ActiveAdapter())

	require.NoError(t, svc.Disconnect(ctx, tenant))
	require.GreaterOrEqual(t, stub.delWebhookN.Load(), int32(1), "deleteWebhook must be attempted")
	require.Nil(t, svc.ActiveAdapter())
	_, registered := reg.Get(telegram.ProviderName)
	require.False(t, registered, "adapter must be removed from the registry")

	store := db.NewStore(pool)
	_, gerr := store.GetBotConfig(ctx, tenant)
	require.True(t, db.IsNoRows(gerr))

	// Disconnecting again is a clean not-configured error.
	require.ErrorIs(t, svc.Disconnect(ctx, tenant), ErrNotConfigured)
}

func TestLoadAtStartup_FromDB(t *testing.T) {
	pool := setupTestDB(t)
	resetDB(t, pool)
	env := newTestEnvelope(t)
	stub := newStubTelegram(t, true)
	ctx := context.Background()

	// First service connects a bot (persists the encrypted token).
	svc1, _ := newService(t, pool, env, stub)
	tenant := uuid.New()
	_, err := svc1.Connect(ctx, tenant, uuid.New(), "persisted-token", "https://moses.example")
	require.NoError(t, err)

	// A fresh service (simulating a restart) hydrates from the DB row.
	svc2, reg2 := newService(t, pool, env, stub)
	adapter, err := svc2.LoadAtStartup(ctx, "", "")
	require.NoError(t, err)
	require.NotNil(t, adapter)
	require.NotNil(t, svc2.ActiveAdapter())
	_, registered := reg2.Get(telegram.ProviderName)
	require.True(t, registered)
}

func TestLoadAtStartup_EnvFallback(t *testing.T) {
	pool := setupTestDB(t)
	resetDB(t, pool)
	env := newTestEnvelope(t)
	stub := newStubTelegram(t, true)
	svc, reg := newService(t, pool, env, stub)
	ctx := context.Background()

	// No DB row — the legacy TELEGRAM_BOT_TOKEN env path registers the adapter.
	adapter, err := svc.LoadAtStartup(ctx, "legacy-env-token", "legacy-secret")
	require.NoError(t, err)
	require.NotNil(t, adapter)
	_, registered := reg.Get(telegram.ProviderName)
	require.True(t, registered)
}

func TestLoadAtStartup_NoConfig_NoAdapter(t *testing.T) {
	pool := setupTestDB(t)
	resetDB(t, pool)
	env := newTestEnvelope(t)
	stub := newStubTelegram(t, true)
	svc, _ := newService(t, pool, env, stub)

	adapter, err := svc.LoadAtStartup(context.Background(), "", "")
	require.NoError(t, err)
	require.Nil(t, adapter)
	require.Nil(t, svc.ActiveAdapter())
}
