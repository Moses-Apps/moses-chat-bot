package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"moses-chat-bot/backend/internal/db"
	"moses-chat-bot/backend/internal/handler/middleware"
	"moses-chat-bot/backend/internal/service/linker"
)

// stampIdentity is a tiny middleware that mimics what RequireUser would
// stamp on the request context, without round-tripping to the platform.
// Used by every handler test so the table is consistent.
func stampIdentity(userID, tenantID uuid.UUID) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := context.WithValue(r.Context(), middleware.UserIDKey, userID)
			ctx = context.WithValue(ctx, middleware.TenantIDKey, tenantID)
			ctx = context.WithValue(ctx, middleware.BearerKey, "test-bearer")
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func newHandlerServer(t *testing.T, userID, tenantID uuid.UUID) (*httptest.Server, *linker.Linker, *db.Store) {
	t.Helper()
	pool := setupHandlerTestDB(t)
	resetHandlerDB(t, pool)
	store := db.NewStore(pool)
	env := newTestEnvelope(t)
	l := linker.New(store, env, nil)

	links := NewLinks(l, store)
	protected := http.NewServeMux()
	links.Register(protected)
	messages := http.NewServeMux()
	links.RegisterMessages(messages)

	root := http.NewServeMux()
	wrapped := stampIdentity(userID, tenantID)(protected)
	root.Handle("/api/v1/links/", wrapped)
	root.Handle("/api/v1/links", wrapped)
	root.Handle("/api/v1/messages", stampIdentity(userID, tenantID)(messages))

	srv := httptest.NewServer(root)
	t.Cleanup(srv.Close)
	return srv, l, store
}

func TestCreateCodeHandler_OK_Returns201(t *testing.T) {
	tenant := uuid.New()
	user := uuid.New()
	srv, _, _ := newHandlerServer(t, user, tenant)

	body, _ := json.Marshal(map[string]interface{}{
		"apiKey":           "plat_xyz",
		"expiresInSeconds": 60,
	})
	resp, err := http.Post(srv.URL+"/api/v1/links/codes", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	var out struct {
		Code      string    `json:"code"`
		ExpiresAt time.Time `json:"expiresAt"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	require.Len(t, out.Code, 6)
	require.True(t, time.Until(out.ExpiresAt) > 0)
}

func TestCreateCodeHandler_MissingAPIKey_400(t *testing.T) {
	srv, _, _ := newHandlerServer(t, uuid.New(), uuid.New())
	body, _ := json.Marshal(map[string]interface{}{"apiKey": ""})
	resp, err := http.Post(srv.URL+"/api/v1/links/codes", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestPollCodeHandler_Pending_200(t *testing.T) {
	tenant := uuid.New()
	user := uuid.New()
	srv, l, _ := newHandlerServer(t, user, tenant)

	code, _, err := l.CreateCode(context.Background(), tenant, user, "plat", nil, 60*time.Second)
	require.NoError(t, err)

	resp, err := http.Get(srv.URL + "/api/v1/links/codes/" + code)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var out struct {
		Status string `json:"status"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	require.Equal(t, "pending", out.Status)
}

func TestPollCodeHandler_Expired_410(t *testing.T) {
	tenant := uuid.New()
	user := uuid.New()
	srv, l, _ := newHandlerServer(t, user, tenant)

	code, _, err := l.CreateCode(context.Background(), tenant, user, "plat", nil, 30*time.Millisecond)
	require.NoError(t, err)
	time.Sleep(60 * time.Millisecond)

	resp, err := http.Get(srv.URL + "/api/v1/links/codes/" + code)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusGone, resp.StatusCode)
}

func TestPollCodeHandler_Unknown_404(t *testing.T) {
	srv, _, _ := newHandlerServer(t, uuid.New(), uuid.New())
	resp, err := http.Get(srv.URL + "/api/v1/links/codes/000000")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestDeleteLinkHandler_204(t *testing.T) {
	tenant := uuid.New()
	user := uuid.New()
	srv, l, _ := newHandlerServer(t, user, tenant)

	code, _, err := l.CreateCode(context.Background(), tenant, user, "plat", nil, 60*time.Second)
	require.NoError(t, err)
	l.RegisterKnown("telegram", "tg-del")
	link, err := l.CompleteLink(context.Background(), code, "telegram", "tg-del")
	require.NoError(t, err)

	req, err := http.NewRequest(http.MethodDelete, srv.URL+"/api/v1/links/"+link.ID.String(), nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusNoContent, resp.StatusCode)
}

func TestDeleteLinkHandler_BadID_400(t *testing.T) {
	srv, _, _ := newHandlerServer(t, uuid.New(), uuid.New())
	req, err := http.NewRequest(http.MethodDelete, srv.URL+"/api/v1/links/not-a-uuid", nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestListLinksHandler_TenantIsolated(t *testing.T) {
	tenantA := uuid.New()
	tenantB := uuid.New()
	user := uuid.New()
	srv, l, _ := newHandlerServer(t, user, tenantA)

	codeA, _, err := l.CreateCode(context.Background(), tenantA, user, "plat", nil, 60*time.Second)
	require.NoError(t, err)
	l.RegisterKnown("telegram", "tg-iso")
	_, err = l.CompleteLink(context.Background(), codeA, "telegram", "tg-iso")
	require.NoError(t, err)

	resp, err := http.Get(srv.URL + "/api/v1/links")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var listA []map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&listA))
	require.Len(t, listA, 1)

	// Now stand up a second server scoped to tenantB+user — must see zero links.
	srvB, _, _ := newHandlerServerSharing(t, srv, user, tenantB)
	respB, err := http.Get(srvB.URL + "/api/v1/links")
	require.NoError(t, err)
	defer respB.Body.Close()
	require.Equal(t, http.StatusOK, respB.StatusCode)
	var listB []map[string]interface{}
	require.NoError(t, json.NewDecoder(respB.Body).Decode(&listB))
	require.Len(t, listB, 0, "tenant B must not see tenant A's links")
}

func TestSearchMessages_LinkScoped_OK(t *testing.T) {
	tenant := uuid.New()
	user := uuid.New()
	srv, l, store := newHandlerServer(t, user, tenant)
	ctx := context.Background()

	code, _, err := l.CreateCode(ctx, tenant, user, "plat", nil, 60*time.Second)
	require.NoError(t, err)
	l.RegisterKnown("telegram", "tg-msg")
	link, err := l.CompleteLink(ctx, code, "telegram", "tg-msg")
	require.NoError(t, err)

	_, err = store.InsertMessage(ctx, link.ID, "in", nil, nil, "hello from telegram", nil, nil)
	require.NoError(t, err)
	_, err = store.InsertMessage(ctx, link.ID, "out", nil, nil, "reply from moses", nil, nil)
	require.NoError(t, err)

	resp, err := http.Get(srv.URL + "/api/v1/messages?link_id=" + link.ID.String())
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var out struct {
		Messages []struct {
			ID         string `json:"id"`
			LinkID     string `json:"linkId"`
			Direction  string `json:"direction"`
			Text       string `json:"text"`
			OccurredAt string `json:"occurredAt"`
		} `json:"messages"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	require.Len(t, out.Messages, 2)
	for _, m := range out.Messages {
		require.Equal(t, link.ID.String(), m.LinkID)
		require.NotEmpty(t, m.OccurredAt)
		require.Contains(t, []string{"in", "out"}, m.Direction)
	}
}

func TestSearchMessages_ForeignLink_404(t *testing.T) {
	tenant := uuid.New()
	userA := uuid.New()
	userB := uuid.New()
	srvA, l, store := newHandlerServer(t, userA, tenant)
	ctx := context.Background()

	code, _, err := l.CreateCode(ctx, tenant, userA, "plat", nil, 60*time.Second)
	require.NoError(t, err)
	l.RegisterKnown("telegram", "tg-foreign")
	link, err := l.CompleteLink(ctx, code, "telegram", "tg-foreign")
	require.NoError(t, err)
	_, err = store.InsertMessage(ctx, link.ID, "in", nil, nil, "private", nil, nil)
	require.NoError(t, err)

	// userB in the SAME tenant must not read userA's link messages — and the
	// 404 must not leak that the link exists.
	srvB, _, _ := newHandlerServerSharing(t, srvA, userB, tenant)
	resp, err := http.Get(srvB.URL + "/api/v1/messages?link_id=" + link.ID.String())
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestSearchMessages_AcrossUserLinks(t *testing.T) {
	tenant := uuid.New()
	user := uuid.New()
	srv, l, store := newHandlerServer(t, user, tenant)
	ctx := context.Background()

	code, _, err := l.CreateCode(ctx, tenant, user, "plat", nil, 60*time.Second)
	require.NoError(t, err)
	l.RegisterKnown("telegram", "tg-search")
	link, err := l.CompleteLink(ctx, code, "telegram", "tg-search")
	require.NoError(t, err)
	_, err = store.InsertMessage(ctx, link.ID, "in", nil, nil, "searchable", nil, nil)
	require.NoError(t, err)

	resp, err := http.Get(srv.URL + "/api/v1/messages")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var out struct {
		Messages []map[string]interface{} `json:"messages"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	require.Len(t, out.Messages, 1)
	require.Equal(t, "searchable", out.Messages[0]["text"])
}

func TestRateLimit_TripsAfterBurst(t *testing.T) {
	srv, _, _ := newHandlerServer(t, uuid.New(), uuid.New())
	for i := 0; i < 5; i++ {
		body, _ := json.Marshal(map[string]interface{}{"apiKey": "plat"})
		resp, err := http.Post(srv.URL+"/api/v1/links/codes", "application/json", bytes.NewReader(body))
		require.NoError(t, err)
		resp.Body.Close()
		require.Equal(t, http.StatusCreated, resp.StatusCode)
	}
	body, _ := json.Marshal(map[string]interface{}{"apiKey": "plat"})
	resp, err := http.Post(srv.URL+"/api/v1/links/codes", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusTooManyRequests, resp.StatusCode)
}
