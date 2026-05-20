package linker

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
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
)

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

// newTestEnvelope returns an envelope with a deterministic single master key.
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

func TestCreateCode_RoundTrip(t *testing.T) {
	pool := setupTestDB(t)
	resetDB(t, pool)
	ctx := context.Background()
	store := db.NewStore(pool)
	env := newTestEnvelope(t)
	l := New(store, env, nil)

	tenant := uuid.New()
	user := uuid.New()
	code, expiresAt, err := l.CreateCode(ctx, tenant, user, "plat_abc123", nil, 60*time.Second)
	require.NoError(t, err)
	require.Len(t, code, 6)
	require.WithinDuration(t, time.Now().Add(60*time.Second), expiresAt, 5*time.Second)

	status, linkID, err := l.PollCode(ctx, tenant, user, code)
	require.NoError(t, err)
	require.Equal(t, StatusPending, status)
	require.Nil(t, linkID)
}

func TestCreateCode_EmptyAPIKey(t *testing.T) {
	pool := setupTestDB(t)
	resetDB(t, pool)
	ctx := context.Background()
	store := db.NewStore(pool)
	env := newTestEnvelope(t)
	l := New(store, env, nil)

	_, _, err := l.CreateCode(ctx, uuid.New(), uuid.New(), "", nil, 60*time.Second)
	require.ErrorIs(t, err, ErrEmptyAPIKey)
}

func TestCompleteLink_HappyPath(t *testing.T) {
	pool := setupTestDB(t)
	resetDB(t, pool)
	ctx := context.Background()
	store := db.NewStore(pool)
	env := newTestEnvelope(t)
	l := New(store, env, nil)

	tenant := uuid.New()
	user := uuid.New()
	hint := uuid.New()
	code, _, err := l.CreateCode(ctx, tenant, user, "plat_abc123", &hint, 60*time.Second)
	require.NoError(t, err)

	l.RegisterKnown("telegram", "tg-42")
	link, err := l.CompleteLink(ctx, code, "telegram", "tg-42")
	require.NoError(t, err)
	require.Equal(t, tenant, link.TenantID)
	require.Equal(t, user, link.MosesUserID)
	require.Equal(t, "telegram", link.Provider)
	require.Equal(t, "tg-42", link.ProviderUserID)
	require.NotNil(t, link.APIKeyIDHint)
	require.Equal(t, hint, *link.APIKeyIDHint)

	plaintext, err := env.Decrypt(tenant, link.EncryptedAPIKey, link.EncryptionKeyID)
	require.NoError(t, err)
	require.Equal(t, []byte("plat_abc123"), plaintext)

	_, err = store.GetPendingLinkByCode(ctx, tenant, code)
	require.True(t, db.IsNoRows(err), "pending row must be deleted after completion")

	status, linkID, err := l.PollCode(ctx, tenant, user, code)
	require.NoError(t, err)
	require.Equal(t, StatusCompleted, status)
	require.NotNil(t, linkID)
	require.Equal(t, link.ID, *linkID)
}

func TestCompleteLink_UnknownUser_Rejected(t *testing.T) {
	pool := setupTestDB(t)
	resetDB(t, pool)
	ctx := context.Background()
	store := db.NewStore(pool)
	env := newTestEnvelope(t)
	l := New(store, env, nil)

	code, _, err := l.CreateCode(ctx, uuid.New(), uuid.New(), "plat_abc123", nil, 60*time.Second)
	require.NoError(t, err)

	_, err = l.CompleteLink(ctx, code, "telegram", "tg-stranger")
	require.ErrorIs(t, err, ErrUnknownUser)
}

func TestCompleteLink_ExpiredCode_Rejected(t *testing.T) {
	pool := setupTestDB(t)
	resetDB(t, pool)
	ctx := context.Background()
	store := db.NewStore(pool)
	env := newTestEnvelope(t)
	l := New(store, env, nil)

	tenant := uuid.New()
	user := uuid.New()
	code, _, err := l.CreateCode(ctx, tenant, user, "plat_abc123", nil, 50*time.Millisecond)
	require.NoError(t, err)

	l.RegisterKnown("telegram", "tg-exp")
	time.Sleep(80 * time.Millisecond)

	_, err = l.CompleteLink(ctx, code, "telegram", "tg-exp")
	require.ErrorIs(t, err, ErrExpired)

	_, err = store.GetPendingLinkByCode(ctx, tenant, code)
	require.True(t, db.IsNoRows(err), "expired row should have been cleaned up by CompleteLink")
}

func TestCompleteLink_BruteForce_LockoutAfterThreeFailures(t *testing.T) {
	pool := setupTestDB(t)
	resetDB(t, pool)
	ctx := context.Background()
	store := db.NewStore(pool)
	env := newTestEnvelope(t)
	l := New(store, env, nil)

	l.RegisterKnown("telegram", "tg-brute")

	for i := 0; i < 3; i++ {
		_, err := l.CompleteLink(ctx, "deadbe", "telegram", "tg-brute")
		require.ErrorIs(t, err, ErrInvalidCode)
	}

	_, err := l.CompleteLink(ctx, "abcdef", "telegram", "tg-brute")
	require.ErrorIs(t, err, ErrLockedOut)

	tenant := uuid.New()
	user := uuid.New()
	code, _, err := l.CreateCode(ctx, tenant, user, "plat_abc", nil, 60*time.Second)
	require.NoError(t, err)
	_, err = l.CompleteLink(ctx, code, "telegram", "tg-brute")
	require.ErrorIs(t, err, ErrLockedOut, "lockout must keep blocking even valid codes")
}

func TestCompleteLink_AlreadyLinked_RejectsSecondActiveForSameProviderUser(t *testing.T) {
	pool := setupTestDB(t)
	resetDB(t, pool)
	ctx := context.Background()
	store := db.NewStore(pool)
	env := newTestEnvelope(t)
	l := New(store, env, nil)

	tenant := uuid.New()
	userA := uuid.New()
	userB := uuid.New()

	codeA, _, err := l.CreateCode(ctx, tenant, userA, "plat_a", nil, 60*time.Second)
	require.NoError(t, err)
	l.RegisterKnown("telegram", "tg-99")
	_, err = l.CompleteLink(ctx, codeA, "telegram", "tg-99")
	require.NoError(t, err)

	codeB, _, err := l.CreateCode(ctx, tenant, userB, "plat_b", nil, 60*time.Second)
	require.NoError(t, err)
	_, err = l.CompleteLink(ctx, codeB, "telegram", "tg-99")
	require.ErrorIs(t, err, ErrAlreadyLinked)
}

// fakeRevoker captures RevokeAPIKey calls without actually doing HTTP.
type fakeRevoker struct {
	called []uuid.UUID
	err    error
}

func (f *fakeRevoker) RevokeAPIKey(_ context.Context, keyID uuid.UUID) error {
	f.called = append(f.called, keyID)
	return f.err
}

func TestUnlink_DeactivatesAndRevokesBestEffort(t *testing.T) {
	pool := setupTestDB(t)
	resetDB(t, pool)
	ctx := context.Background()
	store := db.NewStore(pool)
	env := newTestEnvelope(t)
	// Use nil moses client to exercise the no-revoke path.
	l := New(store, env, nil)

	tenant := uuid.New()
	user := uuid.New()
	hint := uuid.New()
	code, _, err := l.CreateCode(ctx, tenant, user, "plat", &hint, 60*time.Second)
	require.NoError(t, err)
	l.RegisterKnown("telegram", "tg-unlink")
	link, err := l.CompleteLink(ctx, code, "telegram", "tg-unlink")
	require.NoError(t, err)

	require.NoError(t, l.Unlink(ctx, tenant, user, link.ID))

	links, err := store.ListActiveLinksByMosesUser(ctx, tenant, user)
	require.NoError(t, err)
	require.Len(t, links, 0)
}

func TestUnlink_NotFoundForWrongUser(t *testing.T) {
	pool := setupTestDB(t)
	resetDB(t, pool)
	ctx := context.Background()
	store := db.NewStore(pool)
	env := newTestEnvelope(t)
	l := New(store, env, nil)

	tenant := uuid.New()
	owner := uuid.New()
	stranger := uuid.New()
	code, _, err := l.CreateCode(ctx, tenant, owner, "plat", nil, 60*time.Second)
	require.NoError(t, err)
	l.RegisterKnown("telegram", "tg-owned")
	link, err := l.CompleteLink(ctx, code, "telegram", "tg-owned")
	require.NoError(t, err)

	err = l.Unlink(ctx, tenant, stranger, link.ID)
	require.ErrorIs(t, err, ErrLinkNotFound)
}

func TestCleanupSweeper_DeletesExpired(t *testing.T) {
	pool := setupTestDB(t)
	resetDB(t, pool)
	ctx := context.Background()
	store := db.NewStore(pool)
	env := newTestEnvelope(t)
	l := New(store, env, nil)

	tenant := uuid.New()
	user := uuid.New()
	code, _, err := l.CreateCode(ctx, tenant, user, "plat", nil, 50*time.Millisecond)
	require.NoError(t, err)
	time.Sleep(70 * time.Millisecond)

	deleted, err := l.SweepOnce(ctx)
	require.NoError(t, err)
	require.GreaterOrEqual(t, deleted, int64(1))

	_, err = store.GetPendingLinkByCode(ctx, tenant, code)
	require.True(t, db.IsNoRows(err))
}

func TestPollCode_UnknownCode(t *testing.T) {
	pool := setupTestDB(t)
	resetDB(t, pool)
	ctx := context.Background()
	store := db.NewStore(pool)
	env := newTestEnvelope(t)
	l := New(store, env, nil)

	status, linkID, err := l.PollCode(ctx, uuid.New(), uuid.New(), "ffffff")
	require.NoError(t, err)
	require.Equal(t, StatusUnknown, status)
	require.Nil(t, linkID)
}

func TestPollCode_Expired(t *testing.T) {
	pool := setupTestDB(t)
	resetDB(t, pool)
	ctx := context.Background()
	store := db.NewStore(pool)
	env := newTestEnvelope(t)
	l := New(store, env, nil)

	tenant := uuid.New()
	user := uuid.New()
	code, _, err := l.CreateCode(ctx, tenant, user, "plat", nil, 30*time.Millisecond)
	require.NoError(t, err)
	time.Sleep(60 * time.Millisecond)

	status, _, err := l.PollCode(ctx, tenant, user, code)
	require.NoError(t, err)
	require.Equal(t, StatusExpired, status)
}
