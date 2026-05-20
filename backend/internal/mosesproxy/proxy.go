// Package mosesproxy implements the /__moses/invoke handler that
// receives platform-action invokes from the in-iframe Moses SDK and
// forwards them pod-to-pod to moses-backend with the user's JWT
// preserved.
//
// Trust boundary: this proxy runs inside the app's own pod, behind the
// app's nginx, on the same origin as the iframe that calls it. The
// user's JWT travels iframe → app-backend → moses-backend; it does not
// leave the cluster on the second hop (cluster service DNS).
//
// Stdlib only — no third-party imports. Keep it that way.
//
// VENDORED COPY — do not edit in place.
// Canonical source: moses-default-app-templates/shared/mosesproxy-go/proxy.go
// Synced: 2026-05-19 (T-MOSES-1 / moses-chat-bot-uvc).
// Refresh by re-copying from the canonical path; the file is intentionally
// kept byte-identical (modulo this header) to that source so cross-app
// behaviour stays uniform.
package mosesproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// Well-known endpoint path the in-iframe SDK posts to. Templates SHOULD
// mount NewHandler at this path so the SDK contract holds; the constant
// is exported so callers can refer to it instead of hard-coding.
const InvokePath = "/__moses/invoke"

// RequestedWithHeader is the custom header the SDK sets so the proxy
// can distinguish in-iframe SDK calls from drive-by cross-origin POSTs.
// Custom headers cannot be set by a vanilla <form> or <img> CSRF gadget;
// requiring the header closes the cookie-based CSRF window on the
// app-backend hop. See Config.RequireRequestedWith.
const RequestedWithHeader = "X-Requested-With"

// RequestedWithValue is the value the SDK uses for RequestedWithHeader.
const RequestedWithValue = "moses-iframe"

// maxBodyBytes caps the inbound request body. The forwarded payload is
// {actionId, variables}; 64 KiB is several orders of magnitude above
// the legitimate use case but bounded enough to make a denial-of-memory
// attempt impractical.
const maxBodyBytes = 64 * 1024

// forwardTimeout bounds the app-backend → moses-backend hop.
const forwardTimeout = 30 * time.Second

// Config captures the platform-injected env contract. Build from
// ConfigFromEnv() in main() and pass to NewHandler.
type Config struct {
	// InternalAPIBase is the cluster-DNS base URL for moses-backend.
	// e.g. http://moses-backend.moses.svc.cluster.local:8080
	InternalAPIBase string

	// AppSlug is this template's platform-action slug (the segment in
	// /api/v1/apps/{slug}/...). Injected by the provisioner as
	// MOSES_APP_SLUG.
	AppSlug string

	// ChartID is the chart this deployment belongs to. Forwarded in the
	// invoke body so moses-backend can resolve the right conversation
	// scope.
	ChartID string

	// TenantID is the tenant UUID. Optional; forwarded as the
	// X-Tenant-ID header when set, which helps moses-backend's
	// multi-tenant resolution.
	TenantID string

	// HTTPClient is an optional override used by tests. When nil, a
	// fresh http.Client with forwardTimeout is constructed per handler.
	HTTPClient *http.Client

	// RequireRequestedWith, when true, requires the inbound POST to
	// carry X-Requested-With: moses-iframe (see RequestedWithHeader /
	// RequestedWithValue). Closes the same-origin CSRF window on the
	// /__moses/invoke endpoint, at the cost of a coordinated SDK
	// change.
	//
	// OFF BY DEFAULT for v1. The SDK shipped by CHAT-pswm.2 does NOT
	// currently set the header. Once the SDK is updated to send it,
	// flip this on in template main() and treat the gate as part of
	// the contract.
	RequireRequestedWith bool
}

// ConfigFromEnv reads the platform-injected env contract. Tests build
// Config directly.
func ConfigFromEnv() Config {
	return Config{
		InternalAPIBase: os.Getenv("MOSES_INTERNAL_API_BASE"),
		AppSlug:         os.Getenv("MOSES_APP_SLUG"),
		ChartID:         os.Getenv("MOSES_CHART_ID"),
		TenantID:        os.Getenv("MOSES_TENANT_ID"),
	}
}

// Validate checks that the required env fields are set. Returns an
// error the handler surfaces as 503 moses_unconfigured.
func (c Config) Validate() error {
	if c.InternalAPIBase == "" {
		return errors.New("MOSES_INTERNAL_API_BASE is not set — moses-proxy disabled")
	}
	if c.AppSlug == "" {
		return errors.New("MOSES_APP_SLUG is not set — moses-proxy disabled")
	}
	return nil
}

// inboundRequest is the JSON shape the iframe SDK posts.
type inboundRequest struct {
	ActionID  string                 `json:"actionId"`
	Variables map[string]interface{} `json:"variables"`
}

// forwardRequest is the JSON shape moses-backend's user-scoped invoke
// endpoint accepts. ChartID is omitted when empty so we don't surface
// "" to the upstream.
type forwardRequest struct {
	Variables map[string]interface{} `json:"variables,omitempty"`
	ChartID   string                 `json:"chartId,omitempty"`
}

// NewHandler returns an http.HandlerFunc. Mount it at /__moses/invoke
// — the well-known path the SDK at CHAT-pswm.2 posts to.
//
// The handler is intentionally minimal: extract the user JWT, validate
// the request shape, forward pod-to-pod with a 30s timeout, pass the
// upstream response through verbatim (modulo Set-Cookie, which is
// stripped — see below).
func NewHandler(cfg Config) http.HandlerFunc {
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: forwardTimeout}
	}

	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST only")
			return
		}
		if err := cfg.Validate(); err != nil {
			writeErr(w, http.StatusServiceUnavailable, "moses_unconfigured", err.Error())
			return
		}
		if cfg.RequireRequestedWith && r.Header.Get(RequestedWithHeader) != RequestedWithValue {
			writeErr(w, http.StatusForbidden, "missing_requested_with",
				"X-Requested-With: moses-iframe header is required")
			return
		}

		jwt := extractUserJWT(r)
		if jwt == "" {
			writeErr(w, http.StatusUnauthorized, "no_user_jwt",
				"no Authorization Bearer or access_token cookie")
			return
		}

		var in inboundRequest
		dec := json.NewDecoder(io.LimitReader(r.Body, maxBodyBytes))
		if err := dec.Decode(&in); err != nil {
			writeErr(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}
		if in.ActionID == "" {
			writeErr(w, http.StatusBadRequest, "missing_action_id", "actionId is required")
			return
		}

		body, err := json.Marshal(forwardRequest{Variables: in.Variables, ChartID: cfg.ChartID})
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "marshal_failed", err.Error())
			return
		}

		url := strings.TrimRight(cfg.InternalAPIBase, "/") +
			"/api/v1/apps/" + cfg.AppSlug + "/actions/" + in.ActionID + "/invoke"

		ctx, cancel := context.WithTimeout(r.Context(), forwardTimeout)
		defer cancel()

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "request_build_failed", err.Error())
			return
		}
		req.Header.Set("Authorization", "Bearer "+jwt)
		req.Header.Set("Content-Type", "application/json")
		if cfg.TenantID != "" {
			req.Header.Set("X-Tenant-ID", cfg.TenantID)
		}

		resp, err := client.Do(req)
		if err != nil {
			writeErr(w, http.StatusBadGateway, "moses_unreachable", err.Error())
			return
		}
		defer resp.Body.Close()

		// Pass-through: status, Content-Type, body. We deliberately do
		// NOT forward Set-Cookie headers from moses-backend back to the
		// iframe — moses-backend may issue session cookies bound to its
		// own origin which would be both useless and leaky if surfaced
		// under the app's origin via this proxy.
		if ct := resp.Header.Get("Content-Type"); ct != "" {
			w.Header().Set("Content-Type", ct)
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}
}

// extractUserJWT returns the JWT from Authorization: Bearer <jwt>
// first, then from the access_token cookie. The app backend does NOT
// validate the JWT (it does not have MOSES_JWT_SECRET); moses-backend
// validates on receive. This is a pass-through proxy.
//
// Bearer wins over cookie so an explicit caller (e.g. a programmatic
// test, or the Tauri shim that injects Authorization) is honoured even
// when a stale cookie is present.
func extractUserJWT(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if len(auth) >= 7 && strings.EqualFold(auth[:7], "Bearer ") {
		token := strings.TrimSpace(auth[7:])
		if token != "" {
			return token
		}
	}
	if c, err := r.Cookie("access_token"); err == nil && c.Value != "" {
		return c.Value
	}
	return ""
}

// writeErr writes a structured JSON error envelope. Shape matches the
// envelope moses-backend itself emits so the SDK at CHAT-pswm.2 can
// surface {error, code, hint} uniformly regardless of which hop
// failed.
func writeErr(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error": msg,
		"code":  code,
	})
}
