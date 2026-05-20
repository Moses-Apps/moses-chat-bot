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
// getMeOK toggles whether getMe returns a valid bot or a 401.
type stubTelegram struct {
	srv         *httptest.Server
	getMeOK     bool
	setWebhook  int32
	myCommands  int32
	delWebhook  int32
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
			s.setWebhook++
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if v, ok := body["secret_token"].(string); ok {
				s.lastSecret = v
			}
			if v, ok := body["url"].(string); ok {
				s.lastWebhook = v
			}
			fmt.Fprint(w, `{"ok":true,"result":true}`)
		case strings.HasSuffix(r.URL.Path, "/setMyCommands"):
			s.myCommands++
			fmt.Fprint(w, `{"ok":true,"result":true}`)
		case strings.HasSuffix(r.URL.Path, "/deleteWebhook"):
			s.delWebhook++
			fmt.Fprint(w, `{"ok":true,"result":true}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(s.srv.Close)
	return s
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

	// Webhook + commands registered with Telegram.
	require.Equal(t, int32(1), stub.setWebhook)
	require.Equal(t, int32(1), stub.myCommands)
	require.Equal(t,
		"https://moses.example/apps/t/moses-chat-bot"+telegram.WebhookPath(),
		stub.lastWebhook)

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

	// The webhook secret persisted is the one Telegram was told to echo.
	require.Equal(t, cfg.WebhookSecret, stub.lastSecret)

	// The live adapter is registered and verifies against the stored secret.
	adapter := svc.ActiveAdapter()
	require.NotNil(t, adapter)
	require.Equal(t, cfg.WebhookSecret, adapter.WebhookSecret())
	_, registered := reg.Get(telegram.ProviderName)
	require.True(t, registered, "adapter must be in the shared registry for the relay sender")
}

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
	require.GreaterOrEqual(t, stub.delWebhook, int32(1), "deleteWebhook must be attempted")
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
