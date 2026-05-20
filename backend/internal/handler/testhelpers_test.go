package handler

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"moses-chat-bot/backend/internal/db"
	"moses-chat-bot/backend/internal/service/crypto"
	"moses-chat-bot/backend/internal/service/linker"
)

var (
	handlerTestPool     *pgxpool.Pool
	handlerTestPoolOnce sync.Once
	handlerTestPoolErr  error
	handlerTestCleanup  func()
)

func setupHandlerTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	handlerTestPoolOnce.Do(func() {
		ctx := context.Background()
		if dsn := os.Getenv("TEST_DATABASE_URL"); dsn != "" {
			pool, err := db.Open(ctx, dsn)
			if err != nil {
				handlerTestPoolErr = err
				return
			}
			handlerTestPool = pool
			handlerTestCleanup = func() { pool.Close() }
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
			handlerTestPoolErr = err
			return
		}
		dsn, err := container.ConnectionString(ctx, "sslmode=disable")
		if err != nil {
			handlerTestPoolErr = err
			_ = container.Terminate(ctx)
			return
		}
		readyCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		strat := wait.ForListeningPort("5432/tcp").WithStartupTimeout(20 * time.Second)
		if err := strat.WaitUntilReady(readyCtx, container); err != nil {
			handlerTestPoolErr = err
			_ = container.Terminate(ctx)
			return
		}
		pool, err := db.Open(ctx, dsn)
		if err != nil {
			handlerTestPoolErr = err
			_ = container.Terminate(ctx)
			return
		}
		handlerTestPool = pool
		handlerTestCleanup = func() {
			pool.Close()
			_ = container.Terminate(context.Background())
		}
	})
	if handlerTestPoolErr != nil {
		t.Skipf("test DB not available: %v", handlerTestPoolErr)
	}
	if handlerTestPool == nil {
		t.Skip("test DB not available")
	}
	return handlerTestPool
}

func resetHandlerDB(t *testing.T, pool *pgxpool.Pool) {
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
	if handlerTestCleanup != nil {
		handlerTestCleanup()
	}
	os.Exit(code)
}

func newTestEnvelope(t *testing.T) *crypto.Envelope {
	t.Helper()
	raw := make([]byte, 32)
	_, err := rand.Read(raw)
	require.NoError(t, err)
	mkJSON := map[string]interface{}{
		"keys":   map[string]string{"v1": base64.StdEncoding.EncodeToString(raw)},
		"active": "v1",
	}
	b, err := json.Marshal(mkJSON)
	require.NoError(t, err)
	t.Setenv("CHAT_BOT_MASTER_KEY", string(b))
	mk, err := crypto.LoadMasterKeysFromEnv()
	require.NoError(t, err)
	env, err := crypto.NewEnvelope(mk)
	require.NoError(t, err)
	return env
}

// newHandlerServerSharing stands up a SECOND handler server backed by the
// same DB pool/store as `existing`, but stamped with a different
// (user, tenant) identity. Used to assert tenant isolation across two
// callers — both writes go to the same table; reads from one identity
// must not see the other's rows.
func newHandlerServerSharing(t *testing.T, _ *httptest.Server, userID, tenantID uuid.UUID) (*httptest.Server, *linker.Linker, *db.Store) {
	t.Helper()
	pool := setupHandlerTestDB(t)
	store := db.NewStore(pool)
	env := newTestEnvelope(t)
	l := linker.New(store, env, nil)

	protected := http.NewServeMux()
	NewLinks(l, store).Register(protected)

	root := http.NewServeMux()
	wrapped := stampIdentity(userID, tenantID)(protected)
	root.Handle("/api/v1/links/", wrapped)
	root.Handle("/api/v1/links", wrapped)

	srv := httptest.NewServer(root)
	t.Cleanup(srv.Close)
	return srv, l, store
}
