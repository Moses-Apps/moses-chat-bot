package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"moses-chat-bot/backend/internal/db"
	"moses-chat-bot/backend/internal/handler/middleware"
	"moses-chat-bot/backend/internal/mosesclient"
	"moses-chat-bot/backend/internal/provider"
	"moses-chat-bot/backend/internal/provider/telegram"
	"moses-chat-bot/backend/internal/service/botconfig"
)

// fakeAuthValidator implements middleware.AuthValidator so RequireUser /
// RequireTenantAdmin can be exercised without a live platform.
type fakeAuthValidator struct {
	userID   uuid.UUID
	tenantID uuid.UUID
	role     string
	global   bool
	err      error
}

func (f *fakeAuthValidator) GetMe(_ context.Context, _ string, _ uuid.UUID) (*mosesclient.Me, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &mosesclient.Me{
		ID:            f.userID.String(),
		IsGlobalAdmin: f.global,
		TenantMemberships: []mosesclient.TenantMembership{
			{TenantID: f.tenantID, Role: f.role},
		},
	}, nil
}

// stubTelegramServer impersonates api.telegram.org for the connect path.
func stubTelegramServer(t *testing.T, getMeOK bool) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/getMe"):
			if !getMeOK {
				w.WriteHeader(http.StatusUnauthorized)
				fmt.Fprint(w, `{"ok":false,"error_code":401,"description":"Unauthorized"}`)
				return
			}
			fmt.Fprint(w, `{"ok":true,"result":{"id":1,"is_bot":true,"username":"moses_demo_bot"}}`)
		default:
			fmt.Fprint(w, `{"ok":true,"result":true}`)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// newTelegramConfigServer wires the three telegram config routes with the same
// middleware chain main.go uses: RequireUser for /info, RequireUser +
// RequireTenantAdmin for /connect.
func newTelegramConfigServer(t *testing.T, validator middleware.AuthValidator, tgStub *httptest.Server) (*httptest.Server, *db.Store) {
	t.Helper()
	pool := setupHandlerTestDB(t)
	resetHandlerDB(t, pool)
	store := db.NewStore(pool)
	env := newTestEnvelope(t)

	reg := provider.NewRegistry()
	svc := botconfig.New(store, env, reg, nil)
	svc.SetAdapterBuilder(func(token, secret string) (*telegram.Adapter, error) {
		return telegram.New(telegram.Config{
			BotToken:      token,
			WebhookSecret: secret,
			BaseURL:       tgStub.URL,
		})
	})

	cfgHandler := NewTelegramConfig(svc, "/apps/t/moses-chat-bot")
	cfgMux := http.NewServeMux()
	cfgHandler.Register(cfgMux)

	root := http.NewServeMux()
	root.Handle("GET /api/v1/provider/telegram/info",
		middleware.RequireUser(validator)(cfgMux))
	adminGated := middleware.RequireUser(validator)(middleware.RequireTenantAdmin(cfgMux))
	root.Handle("POST /api/v1/provider/telegram/connect", adminGated)
	root.Handle("DELETE /api/v1/provider/telegram/connect", adminGated)

	srv := httptest.NewServer(root)
	t.Cleanup(srv.Close)
	return srv, store
}

func doReq(t *testing.T, method, url, body string) *http.Response {
	t.Helper()
	var r *http.Request
	var err error
	if body != "" {
		r, err = http.NewRequest(method, url, strings.NewReader(body))
	} else {
		r, err = http.NewRequest(method, url, nil)
	}
	require.NoError(t, err)
	r.Header.Set("Authorization", "Bearer test-token")
	resp, err := http.DefaultClient.Do(r)
	require.NoError(t, err)
	return resp
}

func TestTelegramInfo_AnyMember_NotConfigured(t *testing.T) {
	tenant, user := uuid.New(), uuid.New()
	validator := &fakeAuthValidator{userID: user, tenantID: tenant, role: "Editor"}
	tg := stubTelegramServer(t, true)
	srv, _ := newTelegramConfigServer(t, validator, tg)

	resp := doReq(t, http.MethodGet, srv.URL+"/api/v1/provider/telegram/info", "")
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var out telegramInfoResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	require.False(t, out.Configured)
}

func TestTelegramConnect_NonAdmin_Forbidden(t *testing.T) {
	tenant, user := uuid.New(), uuid.New()
	validator := &fakeAuthValidator{userID: user, tenantID: tenant, role: "Editor"}
	tg := stubTelegramServer(t, true)
	srv, store := newTelegramConfigServer(t, validator, tg)

	resp := doReq(t, http.MethodPost, srv.URL+"/api/v1/provider/telegram/connect",
		`{"token":"123:abc"}`)
	defer resp.Body.Close()
	require.Equal(t, http.StatusForbidden, resp.StatusCode)

	// Nothing was persisted by a forbidden request.
	_, err := store.GetBotConfig(context.Background(), tenant)
	require.True(t, db.IsNoRows(err))
}

func TestTelegramConnect_Admin_Succeeds(t *testing.T) {
	tenant, user := uuid.New(), uuid.New()
	validator := &fakeAuthValidator{userID: user, tenantID: tenant, role: "TenantAdmin"}
	tg := stubTelegramServer(t, true)
	srv, store := newTelegramConfigServer(t, validator, tg)

	resp := doReq(t, http.MethodPost, srv.URL+"/api/v1/provider/telegram/connect",
		`{"token":"123:valid-token"}`)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var out telegramInfoResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	require.True(t, out.Configured)
	require.Equal(t, "moses_demo_bot", out.Username)

	// Persisted and encrypted.
	cfg, err := store.GetBotConfig(context.Background(), tenant)
	require.NoError(t, err)
	require.NotContains(t, string(cfg.EncryptedToken), "valid-token")
}

func TestTelegramConnect_GlobalAdmin_Succeeds(t *testing.T) {
	tenant, user := uuid.New(), uuid.New()
	// Global admin with a non-admin tenant role still passes.
	validator := &fakeAuthValidator{userID: user, tenantID: tenant, role: "Viewer", global: true}
	tg := stubTelegramServer(t, true)
	srv, _ := newTelegramConfigServer(t, validator, tg)

	resp := doReq(t, http.MethodPost, srv.URL+"/api/v1/provider/telegram/connect",
		`{"token":"123:valid-token"}`)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestTelegramConnect_InvalidToken_BadRequest(t *testing.T) {
	tenant, user := uuid.New(), uuid.New()
	validator := &fakeAuthValidator{userID: user, tenantID: tenant, role: "TenantAdmin"}
	tg := stubTelegramServer(t, false) // getMe → 401
	srv, _ := newTelegramConfigServer(t, validator, tg)

	resp := doReq(t, http.MethodPost, srv.URL+"/api/v1/provider/telegram/connect",
		`{"token":"bogus"}`)
	defer resp.Body.Close()
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestTelegramDisconnect_Admin(t *testing.T) {
	tenant, user := uuid.New(), uuid.New()
	validator := &fakeAuthValidator{userID: user, tenantID: tenant, role: "TenantAdmin"}
	tg := stubTelegramServer(t, true)
	srv, store := newTelegramConfigServer(t, validator, tg)

	// Connect first.
	resp := doReq(t, http.MethodPost, srv.URL+"/api/v1/provider/telegram/connect",
		`{"token":"123:valid-token"}`)
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	// Disconnect.
	resp = doReq(t, http.MethodDelete, srv.URL+"/api/v1/provider/telegram/connect", "")
	defer resp.Body.Close()
	require.Equal(t, http.StatusNoContent, resp.StatusCode)

	_, err := store.GetBotConfig(context.Background(), tenant)
	require.True(t, db.IsNoRows(err))
}

func TestTelegramDisconnect_NonAdmin_Forbidden(t *testing.T) {
	tenant, user := uuid.New(), uuid.New()
	validator := &fakeAuthValidator{userID: user, tenantID: tenant, role: "Editor"}
	tg := stubTelegramServer(t, true)
	srv, _ := newTelegramConfigServer(t, validator, tg)

	resp := doReq(t, http.MethodDelete, srv.URL+"/api/v1/provider/telegram/connect", "")
	defer resp.Body.Close()
	require.Equal(t, http.StatusForbidden, resp.StatusCode)
}

// TestRequireTenantAdmin_NoIdentity asserts the middleware fails closed when
// RequireUser did not run (defense in depth against a routing bug).
func TestRequireTenantAdmin_NoIdentity(t *testing.T) {
	h := middleware.RequireTenantAdmin(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/x", nil))
	require.Equal(t, http.StatusUnauthorized, rec.Code)
}
