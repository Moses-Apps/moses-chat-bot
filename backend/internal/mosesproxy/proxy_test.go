package mosesproxy

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// upstreamCapture records what the moses-backend stand-in received so
// individual tests can assert on the forwarded shape without each
// reinventing the test server.
type upstreamCapture struct {
	method    string
	path      string
	auth      string
	tenantHdr string
	body      []byte
}

// newUpstream returns an httptest.Server whose handler captures the
// inbound request into *cap and responds with the given status/body.
// Set-Cookie is set on every response so the strip-cookies test can
// verify the proxy drops them.
func newUpstream(t *testing.T, cap *upstreamCapture, status int, respBody string, respCT string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		cap.method = r.Method
		cap.path = r.URL.Path
		cap.auth = r.Header.Get("Authorization")
		cap.tenantHdr = r.Header.Get("X-Tenant-ID")
		cap.body = body
		w.Header().Set("Set-Cookie", "upstream_session=should_not_leak; Path=/")
		if respCT != "" {
			w.Header().Set("Content-Type", respCT)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(respBody))
	}))
}

// newReq builds an inbound request to /__moses/invoke with the given
// JSON body.
func newReq(t *testing.T, body string) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, InvokePath, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	return r
}

// baseConfig points the handler at the given upstream server with
// sensible defaults for the slug/chart/tenant fields. Individual tests
// mutate the returned Config as needed.
func baseConfig(srv *httptest.Server) Config {
	return Config{
		InternalAPIBase: srv.URL,
		AppSlug:         "based-quotes",
		ChartID:         "chart-uuid-123",
		TenantID:        "tenant-uuid-456",
	}
}

// 1. No JWT (no Authorization, no cookie) → 401 no_user_jwt.
func TestNoJWT(t *testing.T) {
	cap := &upstreamCapture{}
	srv := newUpstream(t, cap, 200, `{"ok":true}`, "application/json")
	defer srv.Close()

	w := httptest.NewRecorder()
	NewHandler(baseConfig(srv))(w, newReq(t, `{"actionId":"a","variables":{}}`))

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status: got %d want 401", w.Code)
	}
	var env map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &env)
	if env["code"] != "no_user_jwt" {
		t.Fatalf("code: got %q want no_user_jwt", env["code"])
	}
	if cap.method != "" {
		t.Fatalf("upstream should not have been called; got method %q", cap.method)
	}
}

// 2. Bearer header present → forward includes Authorization: Bearer <jwt>.
func TestBearerForwarded(t *testing.T) {
	cap := &upstreamCapture{}
	srv := newUpstream(t, cap, 200, `{"ok":true}`, "application/json")
	defer srv.Close()

	r := newReq(t, `{"actionId":"chat_prompt","variables":{"q":"hi"}}`)
	r.Header.Set("Authorization", "Bearer my-user-jwt")

	w := httptest.NewRecorder()
	NewHandler(baseConfig(srv))(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", w.Code, w.Body.String())
	}
	if cap.auth != "Bearer my-user-jwt" {
		t.Fatalf("forwarded auth: got %q want %q", cap.auth, "Bearer my-user-jwt")
	}
	if want := "/api/v1/apps/based-quotes/actions/chat_prompt/invoke"; cap.path != want {
		t.Fatalf("forwarded path: got %q want %q", cap.path, want)
	}
}

// 3. Cookie access_token only → forward includes Authorization: Bearer <cookie>.
func TestCookieForwarded(t *testing.T) {
	cap := &upstreamCapture{}
	srv := newUpstream(t, cap, 200, `{"ok":true}`, "application/json")
	defer srv.Close()

	r := newReq(t, `{"actionId":"a","variables":{}}`)
	r.AddCookie(&http.Cookie{Name: "access_token", Value: "cookie-jwt"})

	w := httptest.NewRecorder()
	NewHandler(baseConfig(srv))(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", w.Code, w.Body.String())
	}
	if cap.auth != "Bearer cookie-jwt" {
		t.Fatalf("forwarded auth: got %q want %q", cap.auth, "Bearer cookie-jwt")
	}
}

// 4. BOTH Bearer + cookie → Bearer wins.
func TestBearerWinsOverCookie(t *testing.T) {
	cap := &upstreamCapture{}
	srv := newUpstream(t, cap, 200, `{"ok":true}`, "application/json")
	defer srv.Close()

	r := newReq(t, `{"actionId":"a","variables":{}}`)
	r.Header.Set("Authorization", "Bearer bearer-jwt")
	r.AddCookie(&http.Cookie{Name: "access_token", Value: "cookie-jwt"})

	w := httptest.NewRecorder()
	NewHandler(baseConfig(srv))(w, r)

	if cap.auth != "Bearer bearer-jwt" {
		t.Fatalf("Bearer should win: got %q want %q", cap.auth, "Bearer bearer-jwt")
	}
}

// 5. Missing actionId → 400 missing_action_id.
func TestMissingActionID(t *testing.T) {
	cap := &upstreamCapture{}
	srv := newUpstream(t, cap, 200, `{"ok":true}`, "application/json")
	defer srv.Close()

	r := newReq(t, `{"actionId":"","variables":{}}`)
	r.Header.Set("Authorization", "Bearer x")

	w := httptest.NewRecorder()
	NewHandler(baseConfig(srv))(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d want 400", w.Code)
	}
	var env map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &env)
	if env["code"] != "missing_action_id" {
		t.Fatalf("code: got %q want missing_action_id", env["code"])
	}
	if cap.method != "" {
		t.Fatalf("upstream should not have been called")
	}
}

// 6. Missing MOSES_INTERNAL_API_BASE → 503 moses_unconfigured.
func TestMissingInternalAPIBase(t *testing.T) {
	cfg := Config{AppSlug: "x"}
	r := newReq(t, `{"actionId":"a"}`)
	r.Header.Set("Authorization", "Bearer x")

	w := httptest.NewRecorder()
	NewHandler(cfg)(w, r)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: got %d want 503", w.Code)
	}
	var env map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &env)
	if env["code"] != "moses_unconfigured" {
		t.Fatalf("code: got %q want moses_unconfigured", env["code"])
	}
}

// 7. Missing MOSES_APP_SLUG → 503 moses_unconfigured.
func TestMissingAppSlug(t *testing.T) {
	cfg := Config{InternalAPIBase: "http://upstream"}
	r := newReq(t, `{"actionId":"a"}`)
	r.Header.Set("Authorization", "Bearer x")

	w := httptest.NewRecorder()
	NewHandler(cfg)(w, r)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: got %d want 503", w.Code)
	}
	var env map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &env)
	if env["code"] != "moses_unconfigured" {
		t.Fatalf("code: got %q want moses_unconfigured", env["code"])
	}
}

// 8. Upstream returns 4xx with structured envelope → status + envelope
//    pass through verbatim.
func TestUpstream4xxPassthrough(t *testing.T) {
	cap := &upstreamCapture{}
	body := `{"error":"rate limited","code":"rate_limited","hint":"slow down","retryAfterSeconds":42}`
	srv := newUpstream(t, cap, http.StatusTooManyRequests, body, "application/json")
	defer srv.Close()

	r := newReq(t, `{"actionId":"a","variables":{}}`)
	r.Header.Set("Authorization", "Bearer x")

	w := httptest.NewRecorder()
	NewHandler(baseConfig(srv))(w, r)

	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("status: got %d want 429", w.Code)
	}
	if got := strings.TrimSpace(w.Body.String()); got != body {
		t.Fatalf("body: got %q want %q", got, body)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content-type: got %q want application/json", ct)
	}
}

// 9. Upstream returns 5xx → still passed through (do not swallow).
func TestUpstream5xxPassthrough(t *testing.T) {
	cap := &upstreamCapture{}
	body := `{"error":"oops","code":"internal_error"}`
	srv := newUpstream(t, cap, http.StatusInternalServerError, body, "application/json")
	defer srv.Close()

	r := newReq(t, `{"actionId":"a"}`)
	r.Header.Set("Authorization", "Bearer x")

	w := httptest.NewRecorder()
	NewHandler(baseConfig(srv))(w, r)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status: got %d want 500", w.Code)
	}
	if got := strings.TrimSpace(w.Body.String()); got != body {
		t.Fatalf("body: got %q want %q", got, body)
	}
}

// 10. Upstream unreachable (server closed before call) → 502 moses_unreachable.
func TestUpstreamUnreachable(t *testing.T) {
	cap := &upstreamCapture{}
	srv := newUpstream(t, cap, 200, "", "")
	url := srv.URL
	srv.Close() // close immediately so the URL refuses connection

	cfg := Config{InternalAPIBase: url, AppSlug: "x"}
	r := newReq(t, `{"actionId":"a"}`)
	r.Header.Set("Authorization", "Bearer x")

	w := httptest.NewRecorder()
	NewHandler(cfg)(w, r)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("status: got %d want 502; body=%s", w.Code, w.Body.String())
	}
	var env map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &env)
	if env["code"] != "moses_unreachable" {
		t.Fatalf("code: got %q want moses_unreachable", env["code"])
	}
}

// 11. ChartID is included in forwarded body when env-set; omitted when empty.
func TestChartIDInForwardBody(t *testing.T) {
	t.Run("set", func(t *testing.T) {
		cap := &upstreamCapture{}
		srv := newUpstream(t, cap, 200, `{"ok":true}`, "application/json")
		defer srv.Close()

		r := newReq(t, `{"actionId":"a","variables":{"k":"v"}}`)
		r.Header.Set("Authorization", "Bearer x")

		w := httptest.NewRecorder()
		NewHandler(baseConfig(srv))(w, r)

		var fwd map[string]interface{}
		if err := json.Unmarshal(cap.body, &fwd); err != nil {
			t.Fatalf("forwarded body not JSON: %v; raw=%s", err, cap.body)
		}
		if fwd["chartId"] != "chart-uuid-123" {
			t.Fatalf("chartId: got %v want chart-uuid-123", fwd["chartId"])
		}
		vars, _ := fwd["variables"].(map[string]interface{})
		if vars["k"] != "v" {
			t.Fatalf("variables.k: got %v want v", vars["k"])
		}
	})

	t.Run("empty", func(t *testing.T) {
		cap := &upstreamCapture{}
		srv := newUpstream(t, cap, 200, `{"ok":true}`, "application/json")
		defer srv.Close()

		cfg := baseConfig(srv)
		cfg.ChartID = ""
		r := newReq(t, `{"actionId":"a","variables":{"k":"v"}}`)
		r.Header.Set("Authorization", "Bearer x")

		w := httptest.NewRecorder()
		NewHandler(cfg)(w, r)

		var fwd map[string]interface{}
		if err := json.Unmarshal(cap.body, &fwd); err != nil {
			t.Fatalf("forwarded body not JSON: %v", err)
		}
		if _, ok := fwd["chartId"]; ok {
			t.Fatalf("chartId should be omitted when empty; got %v", fwd["chartId"])
		}
	})
}

// 12. Method != POST → 405.
func TestMethodNotAllowed(t *testing.T) {
	cap := &upstreamCapture{}
	srv := newUpstream(t, cap, 200, "", "")
	defer srv.Close()

	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		r := httptest.NewRequest(method, InvokePath, nil)
		w := httptest.NewRecorder()
		NewHandler(baseConfig(srv))(w, r)
		if w.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s: status got %d want 405", method, w.Code)
		}
		if cap.method != "" {
			t.Fatalf("%s: upstream should not have been called", method)
		}
	}
}

// 13. Body > 64 KB → request body limit triggers, returns 400-shape error.
//
// We can't easily trigger LimitReader's "unexpected EOF" path with
// well-formed JSON that's exactly at the boundary. Instead we send a
// payload whose JSON is intentionally truncated past the 64 KiB cap so
// the json.Decoder sees a partial document and errors with bad_request.
func TestBodyTooLarge(t *testing.T) {
	cap := &upstreamCapture{}
	srv := newUpstream(t, cap, 200, "", "")
	defer srv.Close()

	// Build a JSON body whose `variables.payload` is a string longer
	// than the 64 KiB cap. LimitReader will deliver the first 64 KiB
	// and EOF; json.Decoder will fail because the string literal is
	// unterminated, returning a parse error wrapped as bad_request.
	var sb strings.Builder
	sb.WriteString(`{"actionId":"a","variables":{"payload":"`)
	for sb.Len() < maxBodyBytes+128 {
		sb.WriteString("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
	}
	sb.WriteString(`"}}`)

	r := httptest.NewRequest(http.MethodPost, InvokePath, strings.NewReader(sb.String()))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer x")

	w := httptest.NewRecorder()
	NewHandler(baseConfig(srv))(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d want 400; body=%s", w.Code, w.Body.String())
	}
	var env map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &env)
	if env["code"] != "bad_request" {
		t.Fatalf("code: got %q want bad_request", env["code"])
	}
	if cap.method != "" {
		t.Fatalf("upstream should not have been called for oversized body")
	}
}

// 14. Set-Cookie headers from upstream → stripped (do not pass back to iframe).
func TestStripUpstreamSetCookie(t *testing.T) {
	cap := &upstreamCapture{}
	srv := newUpstream(t, cap, 200, `{"ok":true}`, "application/json")
	defer srv.Close()

	r := newReq(t, `{"actionId":"a"}`)
	r.Header.Set("Authorization", "Bearer x")

	w := httptest.NewRecorder()
	NewHandler(baseConfig(srv))(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200", w.Code)
	}
	if got := w.Header().Values("Set-Cookie"); len(got) != 0 {
		t.Fatalf("Set-Cookie should be stripped; got %v", got)
	}
}

// --- additional coverage / contract tests --------------------------------

// TenantID is forwarded as X-Tenant-ID when set; omitted when empty.
func TestTenantIDHeaderForwarding(t *testing.T) {
	t.Run("set", func(t *testing.T) {
		cap := &upstreamCapture{}
		srv := newUpstream(t, cap, 200, `{"ok":true}`, "application/json")
		defer srv.Close()

		r := newReq(t, `{"actionId":"a"}`)
		r.Header.Set("Authorization", "Bearer x")

		w := httptest.NewRecorder()
		NewHandler(baseConfig(srv))(w, r)

		if cap.tenantHdr != "tenant-uuid-456" {
			t.Fatalf("X-Tenant-ID: got %q want tenant-uuid-456", cap.tenantHdr)
		}
	})
	t.Run("empty", func(t *testing.T) {
		cap := &upstreamCapture{}
		srv := newUpstream(t, cap, 200, `{"ok":true}`, "application/json")
		defer srv.Close()

		cfg := baseConfig(srv)
		cfg.TenantID = ""
		r := newReq(t, `{"actionId":"a"}`)
		r.Header.Set("Authorization", "Bearer x")

		w := httptest.NewRecorder()
		NewHandler(cfg)(w, r)

		if cap.tenantHdr != "" {
			t.Fatalf("X-Tenant-ID should be omitted; got %q", cap.tenantHdr)
		}
	})
}

// RequireRequestedWith=true and the header is absent → 403; when
// present, the request is accepted.
func TestRequireRequestedWith(t *testing.T) {
	cap := &upstreamCapture{}
	srv := newUpstream(t, cap, 200, `{"ok":true}`, "application/json")
	defer srv.Close()

	cfg := baseConfig(srv)
	cfg.RequireRequestedWith = true

	t.Run("missing", func(t *testing.T) {
		r := newReq(t, `{"actionId":"a"}`)
		r.Header.Set("Authorization", "Bearer x")

		w := httptest.NewRecorder()
		NewHandler(cfg)(w, r)
		if w.Code != http.StatusForbidden {
			t.Fatalf("status: got %d want 403", w.Code)
		}
		var env map[string]string
		_ = json.Unmarshal(w.Body.Bytes(), &env)
		if env["code"] != "missing_requested_with" {
			t.Fatalf("code: got %q want missing_requested_with", env["code"])
		}
	})

	t.Run("present", func(t *testing.T) {
		r := newReq(t, `{"actionId":"a"}`)
		r.Header.Set("Authorization", "Bearer x")
		r.Header.Set(RequestedWithHeader, RequestedWithValue)

		w := httptest.NewRecorder()
		NewHandler(cfg)(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("status: got %d want 200; body=%s", w.Code, w.Body.String())
		}
	})
}

// Validate() guards both required env fields.
func TestConfigValidate(t *testing.T) {
	cases := []struct {
		name  string
		cfg   Config
		valid bool
	}{
		{"empty", Config{}, false},
		{"only-base", Config{InternalAPIBase: "http://x"}, false},
		{"only-slug", Config{AppSlug: "x"}, false},
		{"both", Config{InternalAPIBase: "http://x", AppSlug: "x"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.cfg.Validate()
			if c.valid && err != nil {
				t.Fatalf("want valid, got err: %v", err)
			}
			if !c.valid && err == nil {
				t.Fatal("want err, got nil")
			}
		})
	}
}

// Bad JSON body → 400 bad_request.
func TestBadJSONBody(t *testing.T) {
	cap := &upstreamCapture{}
	srv := newUpstream(t, cap, 200, "", "")
	defer srv.Close()

	r := httptest.NewRequest(http.MethodPost, InvokePath, bytes.NewReader([]byte("{not json")))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer x")

	w := httptest.NewRecorder()
	NewHandler(baseConfig(srv))(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d want 400", w.Code)
	}
}

// Bearer with empty token after "Bearer " → falls back to cookie if
// present, else 401. Defensive against a malformed Authorization
// header.
func TestEmptyBearerFallsBackToCookie(t *testing.T) {
	cap := &upstreamCapture{}
	srv := newUpstream(t, cap, 200, `{"ok":true}`, "application/json")
	defer srv.Close()

	r := newReq(t, `{"actionId":"a"}`)
	r.Header.Set("Authorization", "Bearer ")
	r.AddCookie(&http.Cookie{Name: "access_token", Value: "cookie-jwt"})

	w := httptest.NewRecorder()
	NewHandler(baseConfig(srv))(w, r)
	if cap.auth != "Bearer cookie-jwt" {
		t.Fatalf("forwarded auth: got %q want Bearer cookie-jwt", cap.auth)
	}
}

// Trailing slash on InternalAPIBase is normalised so we don't end up
// with a double-slash in the forwarded URL.
func TestTrailingSlashOnInternalAPIBase(t *testing.T) {
	cap := &upstreamCapture{}
	srv := newUpstream(t, cap, 200, `{"ok":true}`, "application/json")
	defer srv.Close()

	cfg := baseConfig(srv)
	cfg.InternalAPIBase = srv.URL + "/"
	r := newReq(t, `{"actionId":"a"}`)
	r.Header.Set("Authorization", "Bearer x")

	w := httptest.NewRecorder()
	NewHandler(cfg)(w, r)

	if want := "/api/v1/apps/based-quotes/actions/a/invoke"; cap.path != want {
		t.Fatalf("path: got %q want %q", cap.path, want)
	}
}
