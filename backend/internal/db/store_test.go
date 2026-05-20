package db

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

var (
	testPool     *pgxpool.Pool
	testPoolOnce sync.Once
	testPoolErr  error
	testCleanup  func()
)

// setupTestDB returns a pgx pool against either TEST_DATABASE_URL (env-driven)
// or a fresh testcontainer Postgres. Skips the test cleanly when neither path
// is available (e.g. no docker on CI worker).
func setupTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	testPoolOnce.Do(func() {
		ctx := context.Background()

		if dsn := os.Getenv("TEST_DATABASE_URL"); dsn != "" {
			pool, err := Open(ctx, dsn)
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
			tcpostgres.WithInitScripts(),
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

		pool, err := Open(ctx, dsn)
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

// resetDB drops everything created by the schema and re-applies it. Cheap-ish
// (5 tables) and keeps tests isolated without per-test DB creation.
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
	require.NoError(t, ApplySchema(ctx, pool))
}

func TestMain(m *testing.M) {
	code := m.Run()
	if testCleanup != nil {
		testCleanup()
	}
	os.Exit(code)
}

func TestApplySchemaIdempotent(t *testing.T) {
	pool := setupTestDB(t)
	resetDB(t, pool)
	ctx := context.Background()

	require.NoError(t, ApplySchema(ctx, pool))

	var count int
	err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&count)
	require.NoError(t, err)
	require.Equal(t, 5, count, "expected 5 schema files applied")
}

func TestPendingLinkRoundTrip(t *testing.T) {
	pool := setupTestDB(t)
	resetDB(t, pool)
	ctx := context.Background()
	store := NewStore(pool)

	tenantA := uuid.New()
	tenantB := uuid.New()
	userID := uuid.New()
	hint := uuid.New()
	expires := time.Now().Add(60 * time.Second)

	require.NoError(t, store.CreatePendingLink(ctx, tenantA, userID, "123456", []byte("cipher"), "v1", &hint, expires))

	got, err := store.GetPendingLinkByCode(ctx, tenantA, "123456")
	require.NoError(t, err)
	require.Equal(t, "123456", got.Code)
	require.Equal(t, "v1", got.EncryptionKeyID)
	require.Equal(t, []byte("cipher"), got.EncryptedAPIKey)
	require.NotNil(t, got.APIKeyIDHint)
	require.Equal(t, hint, *got.APIKeyIDHint)

	_, err = store.GetPendingLinkByCode(ctx, tenantB, "123456")
	require.Error(t, err, "tenant B must not see tenant A's pending link")
	require.True(t, IsNoRows(err))

	require.NoError(t, store.DeletePendingLink(ctx, tenantA, "123456"))
	_, err = store.GetPendingLinkByCode(ctx, tenantA, "123456")
	require.True(t, IsNoRows(err))
}

func TestPendingLinkCleanup(t *testing.T) {
	pool := setupTestDB(t)
	resetDB(t, pool)
	ctx := context.Background()
	store := NewStore(pool)

	tenant := uuid.New()
	past := time.Now().Add(-time.Minute)
	future := time.Now().Add(time.Hour)

	require.NoError(t, store.CreatePendingLink(ctx, tenant, uuid.New(), "111111", []byte("c"), "v1", nil, past))
	require.NoError(t, store.CreatePendingLink(ctx, tenant, uuid.New(), "222222", []byte("c"), "v1", nil, future))

	deleted, err := store.CleanupExpiredPendingLinks(ctx, time.Now())
	require.NoError(t, err)
	require.Equal(t, int64(1), deleted)
}

func TestChatRelayLinkPartialUnique(t *testing.T) {
	pool := setupTestDB(t)
	resetDB(t, pool)
	ctx := context.Background()
	store := NewStore(pool)

	tenant := uuid.New()
	user := uuid.New()

	first, err := store.CreateLink(ctx, tenant, user, "telegram", "tg-42", []byte("c"), "v1", nil)
	require.NoError(t, err)

	_, err = store.CreateLink(ctx, tenant, user, "telegram", "tg-42", []byte("c"), "v1", nil)
	require.Error(t, err, "second active link for same (provider, provider_user_id) must fail")

	require.NoError(t, store.DeactivateLink(ctx, tenant, first.ID, "user_unlink"))

	_, err = store.CreateLink(ctx, tenant, user, "telegram", "tg-42", []byte("c"), "v1", nil)
	require.NoError(t, err, "after deactivation, a new active link with the same provider_user_id is allowed")
}

func TestChatRelayLinkLookups(t *testing.T) {
	pool := setupTestDB(t)
	resetDB(t, pool)
	ctx := context.Background()
	store := NewStore(pool)

	tenantA := uuid.New()
	tenantB := uuid.New()
	userA := uuid.New()

	created, err := store.CreateLink(ctx, tenantA, userA, "telegram", "tg-100", []byte("c"), "v1", nil)
	require.NoError(t, err)

	byProvider, err := store.GetActiveLinkByProviderUser(ctx, "telegram", "tg-100")
	require.NoError(t, err)
	require.Equal(t, created.ID, byProvider.ID)
	require.Equal(t, tenantA, byProvider.TenantID)

	byUser, err := store.GetActiveLinkByMosesUser(ctx, tenantA, userA, "telegram")
	require.NoError(t, err)
	require.Equal(t, created.ID, byUser.ID)

	_, err = store.GetActiveLinkByMosesUser(ctx, tenantB, userA, "telegram")
	require.True(t, IsNoRows(err), "tenant B must not resolve tenant A's link by moses_user_id")

	list, err := store.ListActiveLinksByMosesUser(ctx, tenantA, userA)
	require.NoError(t, err)
	require.Len(t, list, 1)

	require.NoError(t, store.TouchLastUsed(ctx, tenantA, created.ID))
	refetched, err := store.GetActiveLinkByMosesUser(ctx, tenantA, userA, "telegram")
	require.NoError(t, err)
	require.NotNil(t, refetched.LastUsedAt)
}

func TestChatRelayMessages(t *testing.T) {
	pool := setupTestDB(t)
	resetDB(t, pool)
	ctx := context.Background()
	store := NewStore(pool)

	tenant := uuid.New()
	user := uuid.New()
	link, err := store.CreateLink(ctx, tenant, user, "telegram", "tg-200", []byte("c"), "v1", nil)
	require.NoError(t, err)

	pmsg := "tg-msg-1"
	conv := uuid.New()
	id1, err := store.InsertMessage(ctx, link.ID, "in", &pmsg, &conv, "hello", nil, nil)
	require.NoError(t, err)
	require.NotEqual(t, uuid.Nil, id1)

	dup, err := store.IsDuplicateInbound(ctx, link.ID, pmsg)
	require.NoError(t, err)
	require.True(t, dup)

	notDup, err := store.IsDuplicateInbound(ctx, link.ID, "never-seen")
	require.NoError(t, err)
	require.False(t, notDup)

	_, err = store.InsertMessage(ctx, link.ID, "out", nil, &conv, "hi back", []byte(`{"k":"v"}`), nil)
	require.NoError(t, err)

	byLink, err := store.ListRecentByLink(ctx, link.ID, 10)
	require.NoError(t, err)
	require.Len(t, byLink, 2)

	byUser, err := store.ListRecentByMosesUser(ctx, tenant, user, 10)
	require.NoError(t, err)
	require.Len(t, byUser, 2)

	otherTenant := uuid.New()
	none, err := store.ListRecentByMosesUser(ctx, otherTenant, user, 10)
	require.NoError(t, err)
	require.Len(t, none, 0, "other tenant must see zero messages")
}

func TestProviderChatState(t *testing.T) {
	pool := setupTestDB(t)
	resetDB(t, pool)
	ctx := context.Background()
	store := NewStore(pool)

	tenant := uuid.New()
	user := uuid.New()
	link, err := store.CreateLink(ctx, tenant, user, "telegram", "tg-300", []byte("c"), "v1", nil)
	require.NoError(t, err)

	st1, err := store.GetOrCreate(ctx, link.ID, "chat-1")
	require.NoError(t, err)
	require.Equal(t, "chat-1", st1.ProviderChatID)
	require.False(t, st1.AutopilotEnabled)

	st1Again, err := store.GetOrCreate(ctx, link.ID, "chat-1")
	require.NoError(t, err)
	require.Equal(t, st1.ID, st1Again.ID, "GetOrCreate must be idempotent for same (link, chat)")

	conv := uuid.New()
	require.NoError(t, store.UpdateConversationID(ctx, link.ID, "chat-1", conv))

	session := uuid.New()
	require.NoError(t, store.UpdateAutopilot(ctx, link.ID, "chat-1", &session, true))

	all, err := store.ListByLink(ctx, link.ID)
	require.NoError(t, err)
	require.Len(t, all, 1)
	require.True(t, all[0].AutopilotEnabled)
	require.NotNil(t, all[0].AutopilotSessionID)
	require.Equal(t, session, *all[0].AutopilotSessionID)
	require.NotNil(t, all[0].MosesConversationID)
	require.Equal(t, conv, *all[0].MosesConversationID)

	active, err := store.ListWithActiveAutopilot(ctx)
	require.NoError(t, err)
	require.Len(t, active, 1)

	require.NoError(t, store.UpdateAutopilot(ctx, link.ID, "chat-1", nil, false))
	active2, err := store.ListWithActiveAutopilot(ctx)
	require.NoError(t, err)
	require.Len(t, active2, 0)
}

func TestTenantContextEnforced(t *testing.T) {
	pool := setupTestDB(t)
	resetDB(t, pool)
	ctx := context.Background()
	store := NewStore(pool)

	err := store.CreatePendingLink(ctx, uuid.Nil, uuid.New(), "999999", []byte("c"), "v1", nil, time.Now().Add(time.Minute))
	require.ErrorIs(t, err, ErrMissingTenantContext)

	_, err = store.GetPendingLinkByCode(ctx, uuid.Nil, "x")
	require.ErrorIs(t, err, ErrMissingTenantContext)

	err = store.DeletePendingLink(ctx, uuid.Nil, "x")
	require.ErrorIs(t, err, ErrMissingTenantContext)

	_, err = store.CreateLink(ctx, uuid.Nil, uuid.New(), "telegram", "x", []byte("c"), "v1", nil)
	require.ErrorIs(t, err, ErrMissingTenantContext)

	_, err = store.GetActiveLinkByMosesUser(ctx, uuid.Nil, uuid.New(), "telegram")
	require.ErrorIs(t, err, ErrMissingTenantContext)

	_, err = store.ListActiveLinksByMosesUser(ctx, uuid.Nil, uuid.New())
	require.ErrorIs(t, err, ErrMissingTenantContext)

	err = store.DeactivateLink(ctx, uuid.Nil, uuid.New(), "x")
	require.ErrorIs(t, err, ErrMissingTenantContext)

	err = store.TouchLastUsed(ctx, uuid.Nil, uuid.New())
	require.ErrorIs(t, err, ErrMissingTenantContext)

	_, err = store.ListRecentByMosesUser(ctx, uuid.Nil, uuid.New(), 10)
	require.ErrorIs(t, err, ErrMissingTenantContext)
}

func TestApplySchemaTwice(t *testing.T) {
	pool := setupTestDB(t)
	resetDB(t, pool)
	ctx := context.Background()

	require.NoError(t, ApplySchema(ctx, pool))
	require.NoError(t, ApplySchema(ctx, pool), "second apply must be a no-op")

	var count int
	require.NoError(t, pool.QueryRow(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&count))
	require.Equal(t, 5, count)
}
