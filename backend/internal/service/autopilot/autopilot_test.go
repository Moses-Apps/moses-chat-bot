package autopilot

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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"moses-chat-bot/backend/internal/db"
	"moses-chat-bot/backend/internal/mosesclient"
	"moses-chat-bot/backend/internal/provider"
	"moses-chat-bot/backend/internal/service/crypto"
)

// ---------------------------------------------------------------------
// Fake store
// ---------------------------------------------------------------------

type fakeStore struct {
	mu sync.Mutex

	links     map[uuid.UUID]*db.ChatRelayLink             // by link.ID
	chatState map[string]*db.ProviderChatState            // key = linkID|providerChatID
	deactivated map[uuid.UUID]string                      // linkID -> reason

	listErr        error
	getLinkErr     error
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		links:       map[uuid.UUID]*db.ChatRelayLink{},
		chatState:   map[string]*db.ProviderChatState{},
		deactivated: map[uuid.UUID]string{},
	}
}

func (f *fakeStore) seedLink(link *db.ChatRelayLink) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := *link
	f.links[link.ID] = &cp
}

func (f *fakeStore) UpdateAutopilot(_ context.Context, linkID uuid.UUID, providerChatID string, sessionID *uuid.UUID, enabled bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := linkID.String() + "|" + providerChatID
	row, ok := f.chatState[k]
	if !ok {
		return fmt.Errorf("UpdateAutopilot: row not found for %s", k)
	}
	row.AutopilotEnabled = enabled
	if sessionID == nil {
		row.AutopilotSessionID = nil
	} else {
		v := *sessionID
		row.AutopilotSessionID = &v
	}
	row.UpdatedAt = time.Now()
	return nil
}

func (f *fakeStore) GetOrCreate(_ context.Context, linkID uuid.UUID, providerChatID string) (*db.ProviderChatState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := linkID.String() + "|" + providerChatID
	if s, ok := f.chatState[k]; ok {
		cp := *s
		if s.AutopilotSessionID != nil {
			v := *s.AutopilotSessionID
			cp.AutopilotSessionID = &v
		}
		return &cp, nil
	}
	s := &db.ProviderChatState{
		ID:             uuid.New(),
		LinkID:         linkID,
		ProviderChatID: providerChatID,
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}
	f.chatState[k] = s
	cp := *s
	return &cp, nil
}

func (f *fakeStore) ListWithActiveAutopilot(_ context.Context) ([]db.ProviderChatState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := make([]db.ProviderChatState, 0, len(f.chatState))
	for _, s := range f.chatState {
		if s.AutopilotSessionID == nil {
			continue
		}
		cp := *s
		v := *s.AutopilotSessionID
		cp.AutopilotSessionID = &v
		out = append(out, cp)
	}
	return out, nil
}

func (f *fakeStore) GetLinkByIDAnyTenant(_ context.Context, id uuid.UUID) (*db.ChatRelayLink, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getLinkErr != nil {
		return nil, f.getLinkErr
	}
	link, ok := f.links[id]
	if !ok {
		return nil, fmt.Errorf("link not found: %w", errNotFound{})
	}
	cp := *link
	return &cp, nil
}

func (f *fakeStore) DeactivateLink(_ context.Context, _ uuid.UUID, id uuid.UUID, reason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deactivated[id] = reason
	if l, ok := f.links[id]; ok {
		l.IsActive = false
	}
	return nil
}

func (f *fakeStore) deactivationReason(id uuid.UUID) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.deactivated[id]
	return r, ok
}

// errNotFound mimics pgx.ErrNoRows enough for db.IsNoRows to NOT match it —
// that's fine, the sweeper handles non-pgx errors via the warn-and-skip
// branch. For the "link hard-deleted" test path we exercise the IsNoRows
// branch separately by returning the real pgx.ErrNoRows.
type errNotFound struct{}

func (errNotFound) Error() string { return "link not found" }

// ---------------------------------------------------------------------
// Fake mosesclient
// ---------------------------------------------------------------------

// fakeMosesClient is the simplest test double: a struct of fields the
// test sets to drive each call's behaviour. One instance is shared
// between Start/Stop/Status because each test only exercises one path.
type fakeMosesClient struct {
	mu sync.Mutex

	// Pre-flight behaviour for /autonomous/active.
	activeSession *mosesclient.AutonomousSession
	activeErr     error

	// /autonomous/start
	startSession *mosesclient.AutonomousSession
	startErr     error

	// /autonomous/:id
	getSession *mosesclient.AutonomousSession
	getErr     error

	// /autonomous/:id/stop
	stopErr error

	// Call counters
	startCalls       atomic.Int32
	stopCalls        atomic.Int32
	getCalls         atomic.Int32
	getActiveCalls   atomic.Int32

	lastBearer string
}

func (f *fakeMosesClient) StartAutonomous(_ context.Context, _ mosesclient.AutonomousStartOpts) (*mosesclient.AutonomousSession, error) {
	f.startCalls.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.startErr != nil {
		return nil, f.startErr
	}
	return f.startSession, nil
}

func (f *fakeMosesClient) StopAutonomous(_ context.Context, _ uuid.UUID) error {
	f.stopCalls.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stopErr
}

func (f *fakeMosesClient) GetAutonomous(_ context.Context, _ uuid.UUID) (*mosesclient.AutonomousSession, error) {
	f.getCalls.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getErr != nil {
		return nil, f.getErr
	}
	return f.getSession, nil
}

func (f *fakeMosesClient) GetActiveAutonomous(_ context.Context) (*mosesclient.AutonomousSession, error) {
	f.getActiveCalls.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.activeErr != nil {
		return nil, f.activeErr
	}
	return f.activeSession, nil
}

// ---------------------------------------------------------------------
// Fake sender
// ---------------------------------------------------------------------

type sentDM struct {
	LinkID uuid.UUID
	Text   string
}

type fakeSender struct {
	mu   sync.Mutex
	sent []sentDM
}

func (f *fakeSender) SendToLink(_ context.Context, link *db.ChatRelayLink, msg provider.OutboundMessage, _ *uuid.UUID) (uuid.UUID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, sentDM{LinkID: link.ID, Text: msg.Text})
	return uuid.New(), nil
}

func (f *fakeSender) snapshot() []sentDM {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]sentDM, len(f.sent))
	copy(out, f.sent)
	return out
}

// ---------------------------------------------------------------------
// Test fixture
// ---------------------------------------------------------------------

type fixture struct {
	store    *fakeStore
	envelope *crypto.Envelope
	sender   *fakeSender
	moses    *fakeMosesClient
	svc      *Service
	link     *db.ChatRelayLink
}

func newEnvelope(t *testing.T) *crypto.Envelope {
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

func newFixture(t *testing.T) *fixture {
	t.Helper()
	store := newFakeStore()
	env := newEnvelope(t)
	sender := &fakeSender{}
	moses := &fakeMosesClient{}

	tenantID := uuid.New()
	mosesUserID := uuid.New()
	linkID := uuid.New()

	// Encrypt a bearer under the link's tenant so decrypt round-trips.
	ct, keyID, err := env.Encrypt(tenantID, []byte("bearer-"+linkID.String()))
	require.NoError(t, err)

	link := &db.ChatRelayLink{
		ID:              linkID,
		MosesUserID:     mosesUserID,
		TenantID:        tenantID,
		Provider:        "telegram",
		ProviderUserID:  "tg-" + linkID.String()[:8],
		EncryptedAPIKey: ct,
		EncryptionKeyID: keyID,
		IsActive:        true,
	}
	store.seedLink(link)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := New(store, func(bearer string) MosesClient {
		moses.mu.Lock()
		moses.lastBearer = bearer
		moses.mu.Unlock()
		return moses
	}, env, sender, logger)

	return &fixture{
		store:    store,
		envelope: env,
		sender:   sender,
		moses:    moses,
		svc:      svc,
		link:     link,
	}
}

// seedState ensures a chat-state row exists for the fixture link with
// the given session id. Returns the row.
func (fx *fixture) seedState(t *testing.T, providerChatID string, sessionID *uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	_, err := fx.store.GetOrCreate(ctx, fx.link.ID, providerChatID)
	require.NoError(t, err)
	require.NoError(t, fx.store.UpdateAutopilot(ctx, fx.link.ID, providerChatID, sessionID, sessionID != nil))
}

// apiErr builds a *mosesclient.APIError equivalent to what the real
// client returns from a 4xx/5xx response.
func apiErr(status int) error {
	// Use the real classifier indirectly: hit a httptest server that
	// returns the desired status and let the client construct the error.
	// That keeps us honest about errors.Is(err, ErrUnauthorized) etc.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
	}))
	defer srv.Close()
	c := mosesclient.NewClient(srv.URL, mosesclient.BearerAuth{Token: "tok"})
	_, err := c.GetAutonomous(context.Background(), uuid.New())
	if err == nil {
		return errors.New("expected APIError but got nil")
	}
	return err
}

// ---------------------------------------------------------------------
// Start tests
// ---------------------------------------------------------------------

func TestStart_NoExisting_Succeeds_PersistsSession(t *testing.T) {
	fx := newFixture(t)
	newID := uuid.New()
	fx.moses.startSession = &mosesclient.AutonomousSession{
		ID:        newID,
		TenantID:  fx.link.TenantID,
		StartedBy: fx.link.MosesUserID,
		Mode:      "freeform",
		Status:    "active",
	}

	reply, err := fx.svc.Start(context.Background(), fx.link, "chat-1")
	require.NoError(t, err)
	assert.Contains(t, reply, "Autopilot started")
	assert.Contains(t, reply, newID.String()[:8])

	// State persisted.
	state, err := fx.store.GetOrCreate(context.Background(), fx.link.ID, "chat-1")
	require.NoError(t, err)
	require.NotNil(t, state.AutopilotSessionID)
	assert.Equal(t, newID, *state.AutopilotSessionID)
	assert.True(t, state.AutopilotEnabled)

	// Pre-flight + start were called.
	assert.Equal(t, int32(1), fx.moses.getActiveCalls.Load())
	assert.Equal(t, int32(1), fx.moses.startCalls.Load())
}

func TestStart_ExistingOwnedByUser_ReturnsAlreadyRunning_NoNewSession(t *testing.T) {
	fx := newFixture(t)
	existingID := uuid.New()
	fx.moses.activeSession = &mosesclient.AutonomousSession{
		ID:        existingID,
		TenantID:  fx.link.TenantID,
		StartedBy: fx.link.MosesUserID, // same user
		Status:    "active",
	}

	reply, err := fx.svc.Start(context.Background(), fx.link, "chat-1")
	require.NoError(t, err)
	assert.Contains(t, reply, "already running")
	assert.Contains(t, reply, existingID.String()[:8])

	// State persisted to existing.
	state, err := fx.store.GetOrCreate(context.Background(), fx.link.ID, "chat-1")
	require.NoError(t, err)
	require.NotNil(t, state.AutopilotSessionID)
	assert.Equal(t, existingID, *state.AutopilotSessionID)

	// No start call.
	assert.Equal(t, int32(0), fx.moses.startCalls.Load())
}

func TestStart_ExistingOwnedByOtherUser_RefusesWithFriendlyMessage(t *testing.T) {
	fx := newFixture(t)
	fx.moses.activeSession = &mosesclient.AutonomousSession{
		ID:        uuid.New(),
		TenantID:  fx.link.TenantID,
		StartedBy: uuid.New(), // different user
		Status:    "active",
	}

	reply, err := fx.svc.Start(context.Background(), fx.link, "chat-1")
	require.NoError(t, err)
	assert.Contains(t, reply, "another user")
	assert.Contains(t, reply, "/autopilot stop")

	// State NOT persisted.
	state, err := fx.store.GetOrCreate(context.Background(), fx.link.ID, "chat-1")
	require.NoError(t, err)
	assert.Nil(t, state.AutopilotSessionID)

	// No start call.
	assert.Equal(t, int32(0), fx.moses.startCalls.Load())
}

func TestStart_PermissionDenied_403_FromBackend_SurfacesFriendly(t *testing.T) {
	fx := newFixture(t)
	fx.moses.startErr = apiErr(http.StatusForbidden)

	reply, err := fx.svc.Start(context.Background(), fx.link, "chat-1")
	require.NoError(t, err, "Start collapses a 403 into a friendly reply, not an error return")
	assert.Contains(t, reply, "CREATE AUTONOMOUS_SESSIONS")

	// State not persisted.
	state, err := fx.store.GetOrCreate(context.Background(), fx.link.ID, "chat-1")
	require.NoError(t, err)
	assert.Nil(t, state.AutopilotSessionID)
}

// ---------------------------------------------------------------------
// Stop tests
// ---------------------------------------------------------------------

func TestStop_NoSession_FriendlyNoOp(t *testing.T) {
	fx := newFixture(t)
	reply, err := fx.svc.Stop(context.Background(), fx.link, "chat-1")
	require.NoError(t, err)
	assert.Equal(t, "No autopilot active.", reply)
	assert.Equal(t, int32(0), fx.moses.stopCalls.Load())
}

func TestStop_HappyPath_CallsStopAndClearsState(t *testing.T) {
	fx := newFixture(t)
	sessionID := uuid.New()
	fx.seedState(t, "chat-1", &sessionID)

	reply, err := fx.svc.Stop(context.Background(), fx.link, "chat-1")
	require.NoError(t, err)
	assert.Contains(t, reply, "stopped")
	assert.Equal(t, int32(1), fx.moses.stopCalls.Load())

	state, err := fx.store.GetOrCreate(context.Background(), fx.link.ID, "chat-1")
	require.NoError(t, err)
	assert.Nil(t, state.AutopilotSessionID)
	assert.False(t, state.AutopilotEnabled)
}

// ---------------------------------------------------------------------
// Status tests
// ---------------------------------------------------------------------

func TestStatus_NoSession_FriendlyMessage(t *testing.T) {
	fx := newFixture(t)
	reply, err := fx.svc.Status(context.Background(), fx.link, "chat-1")
	require.NoError(t, err)
	assert.Equal(t, "No autopilot active.", reply)
	assert.Equal(t, int32(0), fx.moses.getCalls.Load())
}

func TestStatus_RunningSession_FormatsMessage_WithCounters(t *testing.T) {
	fx := newFixture(t)
	sessionID := uuid.New()
	fx.seedState(t, "chat-1", &sessionID)

	fx.moses.getSession = &mosesclient.AutonomousSession{
		ID:                  sessionID,
		Status:              "active",
		Mode:                "freeform",
		TicketsExecuted:     7,
		TicketsSucceeded:    5,
		TicketsFailed:       1,
		TicketsSkipped:      1,
		MaxConcurrentAgents: 3,
		MaxRetriesPerTicket: 2,
		SessionTimeoutHours: 24,
		CreatedAt:           time.Now().Add(-time.Hour),
	}

	reply, err := fx.svc.Status(context.Background(), fx.link, "chat-1")
	require.NoError(t, err)
	assert.Contains(t, reply, "Status: active")
	assert.Contains(t, reply, "7 done")
	assert.Contains(t, reply, "5 ok")
	assert.Contains(t, reply, "Concurrency: 3")
	assert.Contains(t, reply, sessionID.String()[:8])
}

func TestStatus_BackendReturns404_ClearsStaleSessionId(t *testing.T) {
	fx := newFixture(t)
	sessionID := uuid.New()
	fx.seedState(t, "chat-1", &sessionID)

	fx.moses.getErr = apiErr(http.StatusNotFound)

	reply, err := fx.svc.Status(context.Background(), fx.link, "chat-1")
	require.NoError(t, err)
	assert.Contains(t, reply, "no longer exists")

	state, err := fx.store.GetOrCreate(context.Background(), fx.link.ID, "chat-1")
	require.NoError(t, err)
	assert.Nil(t, state.AutopilotSessionID, "stale session id should be cleared")
}

// ---------------------------------------------------------------------
// Sweeper tests
// ---------------------------------------------------------------------

func TestSweeper_ClearsTerminalSession_DMsUser(t *testing.T) {
	fx := newFixture(t)
	sessionID := uuid.New()
	fx.seedState(t, "chat-1", &sessionID)

	summary := "Autopilot finished: 17 tickets completed in 3h12m"
	fx.moses.getSession = &mosesclient.AutonomousSession{
		ID:               sessionID,
		Status:           "completed",
		Summary:          &summary,
		TicketsExecuted:  17,
		TicketsSucceeded: 17,
	}

	require.NoError(t, fx.svc.SweepTerminalSessions(context.Background()))

	// State cleared.
	state, err := fx.store.GetOrCreate(context.Background(), fx.link.ID, "chat-1")
	require.NoError(t, err)
	assert.Nil(t, state.AutopilotSessionID)
	assert.False(t, state.AutopilotEnabled)

	// DM sent with the persisted summary text.
	sent := fx.sender.snapshot()
	require.Len(t, sent, 1)
	assert.Equal(t, summary, sent[0].Text)
	assert.Equal(t, fx.link.ID, sent[0].LinkID)
}

func TestSweeper_TerminalSession_NoSummary_UsesCounters(t *testing.T) {
	fx := newFixture(t)
	sessionID := uuid.New()
	fx.seedState(t, "chat-1", &sessionID)

	fx.moses.getSession = &mosesclient.AutonomousSession{
		ID:               sessionID,
		Status:           "failed",
		TicketsExecuted:  3,
		TicketsSucceeded: 1,
		TicketsFailed:    2,
	}

	require.NoError(t, fx.svc.SweepTerminalSessions(context.Background()))

	sent := fx.sender.snapshot()
	require.Len(t, sent, 1)
	assert.Contains(t, sent[0].Text, "failed")
	assert.Contains(t, sent[0].Text, "3 tickets")
}

func TestSweeper_404_ClearsAndDMs(t *testing.T) {
	fx := newFixture(t)
	sessionID := uuid.New()
	fx.seedState(t, "chat-1", &sessionID)

	fx.moses.getErr = apiErr(http.StatusNotFound)

	require.NoError(t, fx.svc.SweepTerminalSessions(context.Background()))

	state, err := fx.store.GetOrCreate(context.Background(), fx.link.ID, "chat-1")
	require.NoError(t, err)
	assert.Nil(t, state.AutopilotSessionID)

	sent := fx.sender.snapshot()
	require.Len(t, sent, 1)
	assert.Contains(t, sent[0].Text, "vanished")
}

func TestSweeper_401_DeactivatesLink(t *testing.T) {
	fx := newFixture(t)
	sessionID := uuid.New()
	fx.seedState(t, "chat-1", &sessionID)

	fx.moses.getErr = apiErr(http.StatusUnauthorized)

	require.NoError(t, fx.svc.SweepTerminalSessions(context.Background()))

	reason, ok := fx.store.deactivationReason(fx.link.ID)
	require.True(t, ok, "link should be deactivated")
	assert.Equal(t, "platform_401", reason)

	sent := fx.sender.snapshot()
	require.Len(t, sent, 1)
	assert.Contains(t, sent[0].Text, "revoked")
}

func TestSweeper_RunningSession_NoOp(t *testing.T) {
	fx := newFixture(t)
	sessionID := uuid.New()
	fx.seedState(t, "chat-1", &sessionID)

	fx.moses.getSession = &mosesclient.AutonomousSession{
		ID:     sessionID,
		Status: "active",
	}

	require.NoError(t, fx.svc.SweepTerminalSessions(context.Background()))

	state, err := fx.store.GetOrCreate(context.Background(), fx.link.ID, "chat-1")
	require.NoError(t, err)
	require.NotNil(t, state.AutopilotSessionID, "running sessions must not be cleared")
	assert.Empty(t, fx.sender.snapshot())
}

func TestSweeper_ConcurrentRows_NoRace(t *testing.T) {
	// Build a service with many rows pointing at the same fake client.
	// SweepTerminalSessions is sequential per-row inside, but the test
	// drives multiple sweeps concurrently under -race so the fake's
	// shared state has to be guarded; the test asserts no race + no
	// state corruption.
	fx := newFixture(t)
	for i := 0; i < 8; i++ {
		sid := uuid.New()
		fx.seedState(t, fmt.Sprintf("chat-%d", i), &sid)
	}
	fx.moses.getSession = &mosesclient.AutonomousSession{
		Status: "active", // no-op path
	}

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = fx.svc.SweepTerminalSessions(context.Background())
		}()
	}
	wg.Wait()

	// All 8 rows still active.
	rows, err := fx.store.ListWithActiveAutopilot(context.Background())
	require.NoError(t, err)
	assert.Len(t, rows, 8)
}

// Sanity check: compile-time wiring is sound.
func TestNew_DefaultLogger(t *testing.T) {
	svc := New(newFakeStore(), func(string) MosesClient { return &fakeMosesClient{} }, nil, &fakeSender{}, nil)
	assert.NotNil(t, svc.Logger)
}
