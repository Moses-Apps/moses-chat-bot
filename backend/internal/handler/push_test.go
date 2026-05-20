package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"moses-chat-bot/backend/internal/db"
	"moses-chat-bot/backend/internal/handler/middleware"
	"moses-chat-bot/backend/internal/provider"
	"moses-chat-bot/backend/internal/provider/providertest"
	"moses-chat-bot/backend/internal/service/relay"
)

// testPlatformBearer is the bearer the test server gates its workspace mux on.
// Mirrors what MOSES_PLATFORM_API_KEY would carry in production.
const testPlatformBearer = "test-platform-bearer-abc123"

// mosesHeadersWrap wraps the push handler in the same middleware stack as
// production: RequirePlatformAPIKey first (bearer gate), then MosesHeaders.
// Tests using doJSON automatically inject the bearer.
func mosesHeadersWrap(next http.Handler) http.Handler {
	return middleware.RequirePlatformAPIKey(testPlatformBearer)(middleware.MosesHeaders(next))
}

// newPushServer stands up an httptest server with the push routes mounted
// behind the platform-bearer gate + moses-headers middleware. Reused by
// every push test.
func newPushServer(t *testing.T, perLinkPerMinute int) (*httptest.Server, *db.Store, *providertest.InMemoryProvider) {
	t.Helper()
	pool := setupHandlerTestDB(t)
	resetHandlerDB(t, pool)
	store := db.NewStore(pool)

	registry := provider.NewRegistry()
	tg := providertest.NewInMemoryProvider("telegram")
	require.NoError(t, registry.Register(tg))

	opts := relay.SenderOpts{}
	if perLinkPerMinute > 0 {
		// Pin the clock so the rate-limit test is deterministic.
		fixed := time.Date(2026, 5, 19, 0, 0, 0, 0, time.UTC)
		opts.PerLinkPerMinute = perLinkPerMinute
		opts.Clock = func() time.Time { return fixed }
	}
	sender := relay.NewSender(store, registry, opts)

	root := http.NewServeMux()
	pushMux := http.NewServeMux()
	NewPush(store, sender).Register(pushMux)
	root.Handle("/api/v1/", mosesHeadersWrap(pushMux))

	srv := httptest.NewServer(root)
	t.Cleanup(srv.Close)
	return srv, store, tg
}

// seedLink writes a link directly via the store (skipping the linker service).
// Production code never goes this way, but tests need a quick way to set up
// the (tenant, user, link) graph that push exercises.
func seedLink(t *testing.T, store *db.Store, tenantID, userID uuid.UUID, providerName, providerUserID string) *db.ChatRelayLink {
	t.Helper()
	link, err := store.CreateLink(
		context.Background(),
		tenantID,
		userID,
		providerName,
		providerUserID,
		[]byte("ciphertext"), // not decrypted in this test
		"v1",
		nil,
	)
	require.NoError(t, err)
	return link
}

// doJSON sends a JSON request with the test platform bearer + X-Moses-Tenant-ID
// and returns the response. doJSONWithBearer is the lower-level variant for
// auth-gate tests that need to vary the bearer (or omit it).
func doJSON(t *testing.T, srv *httptest.Server, method, path string, body interface{}, tenantHeader string) *http.Response {
	t.Helper()
	return doJSONWithBearer(t, srv, method, path, body, tenantHeader, testPlatformBearer)
}

// doJSONWithBearer is the explicit-bearer variant. Pass "" to omit the
// Authorization header entirely (used by the bearer-rejection tests).
func doJSONWithBearer(t *testing.T, srv *httptest.Server, method, path string, body interface{}, tenantHeader, bearer string) *http.Response {
	t.Helper()
	var rdr *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		require.NoError(t, err)
		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, srv.URL+path, rdr)
	require.NoError(t, err)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if tenantHeader != "" {
		req.Header.Set("X-Moses-Tenant-ID", tenantHeader)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}

// ============================================================================
// POST /api/v1/push/message
// ============================================================================

func TestPushMessage_HappyPath_FansOutAndReturnsResults(t *testing.T) {
	srv, store, tg := newPushServer(t, 0)
	tenant := uuid.New()
	user := uuid.New()
	seedLink(t, store, tenant, user, "telegram", "tg-happy")

	body := map[string]interface{}{
		"moses_user_id": user.String(),
		"text":          "hello from MM",
	}
	resp := doJSON(t, srv, http.MethodPost, "/api/v1/push/message", body, tenant.String())
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var out pushMessageResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	require.Equal(t, 1, out.SentCount)
	require.Len(t, out.Results, 1)
	require.True(t, out.Results[0].Sent)
	require.Equal(t, "telegram", out.Results[0].Provider)
	require.Equal(t, "tg-happy", out.Results[0].ChatID)

	sent := tg.Snapshot()
	require.Len(t, sent, 1)
	require.Equal(t, "hello from MM", sent[0].Msg.Text)
}

func TestPushMessage_EmptyLinks_Returns200ZeroCount(t *testing.T) {
	srv, _, _ := newPushServer(t, 0)
	tenant := uuid.New()
	user := uuid.New() // no link seeded

	body := map[string]interface{}{
		"moses_user_id": user.String(),
		"text":          "anyone there?",
	}
	resp := doJSON(t, srv, http.MethodPost, "/api/v1/push/message", body, tenant.String())
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var out pushMessageResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	require.Equal(t, 0, out.SentCount)
	require.Len(t, out.Results, 0)
}

func TestPushMessage_InvalidTenantHeader_401(t *testing.T) {
	srv, _, _ := newPushServer(t, 0)
	user := uuid.New()

	body := map[string]interface{}{
		"moses_user_id": user.String(),
		"text":          "x",
	}
	// Missing header
	resp := doJSON(t, srv, http.MethodPost, "/api/v1/push/message", body, "")
	defer resp.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	// Malformed header
	resp2 := doJSON(t, srv, http.MethodPost, "/api/v1/push/message", body, "not-a-uuid")
	defer resp2.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp2.StatusCode)
}

func TestPushMessage_BodyValidation_400(t *testing.T) {
	srv, _, _ := newPushServer(t, 0)
	tenant := uuid.New()

	cases := []struct {
		name string
		body interface{}
	}{
		{"missing user", map[string]interface{}{"text": "hi"}},
		{"bad user UUID", map[string]interface{}{"moses_user_id": "not-a-uuid", "text": "hi"}},
		{"empty text", map[string]interface{}{"moses_user_id": uuid.NewString(), "text": ""}},
		{"whitespace text", map[string]interface{}{"moses_user_id": uuid.NewString(), "text": "   "}},
		{"oversize text", map[string]interface{}{"moses_user_id": uuid.NewString(), "text": strings.Repeat("a", maxTextLen+1)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := doJSON(t, srv, http.MethodPost, "/api/v1/push/message", tc.body, tenant.String())
			defer resp.Body.Close()
			require.Equal(t, http.StatusBadRequest, resp.StatusCode)
		})
	}
}

// ============================================================================
// GET /api/v1/workspace/links
// ============================================================================

func TestListLinks_NoFilter_AllActive(t *testing.T) {
	srv, store, _ := newPushServer(t, 0)
	tenant := uuid.New()
	userA := uuid.New()
	userB := uuid.New()
	seedLink(t, store, tenant, userA, "telegram", "tg-a")
	seedLink(t, store, tenant, userB, "telegram", "tg-b")

	resp := doJSON(t, srv, http.MethodGet, "/api/v1/workspace/links", nil, tenant.String())
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var out linksResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	require.Len(t, out.Links, 2)
}

func TestListLinks_WithUserFilter(t *testing.T) {
	srv, store, _ := newPushServer(t, 0)
	tenant := uuid.New()
	userA := uuid.New()
	userB := uuid.New()
	seedLink(t, store, tenant, userA, "telegram", "tg-a")
	seedLink(t, store, tenant, userB, "telegram", "tg-b")

	resp := doJSON(t, srv, http.MethodGet, "/api/v1/workspace/links?moses_user_id="+userA.String(), nil, tenant.String())
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var out linksResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	require.Len(t, out.Links, 1)
	require.Equal(t, userA, out.Links[0].MosesUserID)
}

// ============================================================================
// POST /api/v1/workspace/links/{id}/notify
// ============================================================================

func TestNotifyLink_HappyPath_200(t *testing.T) {
	srv, store, tg := newPushServer(t, 0)
	tenant := uuid.New()
	user := uuid.New()
	link := seedLink(t, store, tenant, user, "telegram", "tg-notify")

	body := map[string]interface{}{"text": "ping"}
	resp := doJSON(t, srv, http.MethodPost, "/api/v1/workspace/links/"+link.ID.String()+"/notify", body, tenant.String())
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var out notifyResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	require.True(t, out.Sent)
	require.NotEqual(t, uuid.Nil, out.MessageRowID)

	sent := tg.Snapshot()
	require.Len(t, sent, 1)
	require.Equal(t, "ping", sent[0].Msg.Text)
}

func TestNotifyLink_CrossTenant_403(t *testing.T) {
	srv, store, _ := newPushServer(t, 0)
	tenantA := uuid.New()
	tenantB := uuid.New()
	user := uuid.New()
	link := seedLink(t, store, tenantA, user, "telegram", "tg-iso")

	body := map[string]interface{}{"text": "ping"}
	// Caller claims tenantB but addresses a link in tenantA → 403 per SPEC §7.
	// The lookup is intentionally unscoped (internal-only) so we can return
	// 403 (tenant mismatch) vs 404 (no such row). 403 is the audit signal —
	// it indicates the platform proxy let a tenant-crossing call through.
	resp := doJSON(t, srv, http.MethodPost, "/api/v1/workspace/links/"+link.ID.String()+"/notify", body, tenantB.String())
	defer resp.Body.Close()
	require.Equal(t, http.StatusForbidden, resp.StatusCode)
}

func TestNotifyLink_NotFound_404(t *testing.T) {
	srv, _, _ := newPushServer(t, 0)
	tenant := uuid.New()
	body := map[string]interface{}{"text": "ping"}
	resp := doJSON(t, srv, http.MethodPost, "/api/v1/workspace/links/"+uuid.NewString()+"/notify", body, tenant.String())
	defer resp.Body.Close()
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestNotifyLink_RateLimited_429(t *testing.T) {
	srv, store, _ := newPushServer(t, 1) // capacity 1 with pinned clock
	tenant := uuid.New()
	user := uuid.New()
	link := seedLink(t, store, tenant, user, "telegram", "tg-rl")

	body := map[string]interface{}{"text": "first"}
	resp1 := doJSON(t, srv, http.MethodPost, "/api/v1/workspace/links/"+link.ID.String()+"/notify", body, tenant.String())
	resp1.Body.Close()
	require.Equal(t, http.StatusOK, resp1.StatusCode)

	body2 := map[string]interface{}{"text": "second"}
	resp2 := doJSON(t, srv, http.MethodPost, "/api/v1/workspace/links/"+link.ID.String()+"/notify", body2, tenant.String())
	defer resp2.Body.Close()
	require.Equal(t, http.StatusTooManyRequests, resp2.StatusCode)
	require.NotEmpty(t, resp2.Header.Get("Retry-After"))
}

// ============================================================================
// GET /api/v1/workspace/messages
// ============================================================================

func TestListRecentMessages_TenantIsolated(t *testing.T) {
	srv, store, _ := newPushServer(t, 0)
	tenantA := uuid.New()
	tenantB := uuid.New()
	userA := uuid.New()
	linkA := seedLink(t, store, tenantA, userA, "telegram", "tg-msg-a")

	// Insert a message under tenantA's link.
	_, err := store.InsertMessage(context.Background(), linkA.ID, "out", nil, nil, "tenantA-msg", nil, nil)
	require.NoError(t, err)

	// tenantA fetches: should see the message.
	respA := doJSON(t, srv, http.MethodGet, "/api/v1/workspace/messages?moses_user_id="+userA.String(), nil, tenantA.String())
	defer respA.Body.Close()
	require.Equal(t, http.StatusOK, respA.StatusCode)
	var outA messagesResponse
	require.NoError(t, json.NewDecoder(respA.Body).Decode(&outA))
	require.Len(t, outA.Messages, 1)
	require.Equal(t, "tenantA-msg", outA.Messages[0].Text)

	// tenantB fetches the SAME userA id: must see zero rows (the join on
	// chat_relay_links.tenant_id filters tenantA's data out).
	respB := doJSON(t, srv, http.MethodGet, "/api/v1/workspace/messages?moses_user_id="+userA.String(), nil, tenantB.String())
	defer respB.Body.Close()
	require.Equal(t, http.StatusOK, respB.StatusCode)
	var outB messagesResponse
	require.NoError(t, json.NewDecoder(respB.Body).Decode(&outB))
	require.Len(t, outB.Messages, 0, "tenant B must not see tenant A's messages")
}

// ============================================================================
// Bearer-token gate (CHAT-y3u follow-up)
//
// The workspace-tool surface is externally reachable via the ingress, so the
// RequirePlatformAPIKey middleware constant-time-checks the inbound bearer
// against MOSES_PLATFORM_API_KEY before any handler runs. Without this gate,
// any caller could spoof X-Moses-Tenant-ID and read/write into arbitrary
// tenants.
// ============================================================================

func TestWorkspaceAuth_RejectsMissingBearer(t *testing.T) {
	srv, _, _ := newPushServer(t, 0)
	tenant := uuid.New()

	// Missing Authorization header entirely. Even with a valid tenant header
	// the request must be 401'd before any tenant lookup occurs.
	resp := doJSONWithBearer(t, srv, http.MethodGet, "/api/v1/workspace/links", nil, tenant.String(), "")
	defer resp.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	var body map[string]string
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.Equal(t, "unauthenticated", body["code"])
}

func TestWorkspaceAuth_RejectsWrongBearer(t *testing.T) {
	srv, _, _ := newPushServer(t, 0)
	tenant := uuid.New()

	resp := doJSONWithBearer(t, srv, http.MethodGet, "/api/v1/workspace/links", nil, tenant.String(), "totally-wrong-bearer")
	defer resp.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	var body map[string]string
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.Equal(t, "unauthenticated", body["code"])
}

func TestWorkspaceAuth_AcceptsCorrectBearer(t *testing.T) {
	srv, store, _ := newPushServer(t, 0)
	tenant := uuid.New()
	user := uuid.New()
	seedLink(t, store, tenant, user, "telegram", "tg-auth-ok")

	// Correct bearer + correct tenant → must reach the handler and return data.
	resp := doJSONWithBearer(t, srv, http.MethodGet, "/api/v1/workspace/links", nil, tenant.String(), testPlatformBearer)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var out linksResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	require.Len(t, out.Links, 1)
}

// TestWorkspaceAuth_FailClosedOnEmptyExpected verifies that when
// MOSES_PLATFORM_API_KEY is unset, every request 503s instead of silently
// allowing through. This is the safety property the gate exists to enforce.
func TestWorkspaceAuth_FailClosedOnEmptyExpected(t *testing.T) {
	// Build a tiny mux gated with an empty expected token (simulating the
	// pod starting without the integration grant approved).
	gated := middleware.RequirePlatformAPIKey("")(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	srv := httptest.NewServer(gated)
	defer srv.Close()

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/workspace/links", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer anything")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)

	var body map[string]string
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.Equal(t, "platform_key_unset", body["code"])
}

// TestWorkspaceAuth_DevBypass verifies BOT_PLATFORM_AUTH_DISABLED=true lets
// requests through without a bearer. The dev path is intentionally lenient
// because real-laptop linker workflows don't have a platform pod to mint a
// bearer; the middleware logs a warn on every request so the misconfig is
// audit-visible.
func TestWorkspaceAuth_DevBypass(t *testing.T) {
	t.Setenv("BOT_PLATFORM_AUTH_DISABLED", "true")
	// expectedToken="" simulates an unset env in dev: bypass must still kick in.
	gated := middleware.RequirePlatformAPIKey("")(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	srv := httptest.NewServer(gated)
	defer srv.Close()

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/workspace/links", nil)
	require.NoError(t, err)
	// No Authorization header at all.
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
}
