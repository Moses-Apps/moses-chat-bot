// Package middleware — platform_auth (CHAT-y3u follow-up).
//
// The workspace-tool surface (/api/v1/push/*, /api/v1/workspace/*) is reachable
// from the public internet via the ingress (helm/templates/ingress.yaml routes
// /api/ → backend service). Before this middleware existed, those endpoints
// trusted the X-Moses-Tenant-ID header as authoritative, which let any external
// caller enumerate / write into any tenant.
//
// RequirePlatformAPIKey constant-time-compares the inbound Authorization
// bearer token against the MOSES_PLATFORM_API_KEY that the approved
// `moses-platform` integration grant mounts into this pod. Only after that
// gate passes does the MosesHeaders middleware's X-Moses-Tenant-ID header
// become meaningful (the platform proxy is the only caller that reaches this
// codepath with a matching bearer).
//
// Fail-closed semantics:
//   - expectedToken == "" (env unset)  → every request 503'd with a clear log
//     line. Refusing to serve a missing-key startup is intentional — a silent
//     "no auth" mode is exactly the bug we're closing.
//   - BOT_PLATFORM_AUTH_DISABLED=true (local-dev escape hatch) bypasses the
//     bearer check but logs a Warn on every request so the misconfiguration
//     can't hide in production.
//
// This file MUST NOT import other middleware in this package (no cycles); it
// is constructed in cmd/server/main.go and wrapped around the push mux BEFORE
// MosesHeaders.
package middleware

import (
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
)

// platformAuthDisabledEnv is the env var that turns OFF the bearer gate. Only
// honored in local-dev; the warn log on every request is the audit trail.
const platformAuthDisabledEnv = "BOT_PLATFORM_AUTH_DISABLED"

// authDisabledOnce ensures we only log the "auth disabled" startup banner
// once per process even if RequirePlatformAPIKey is invoked multiple times
// (defensive — main.go wires it once today).
var authDisabledOnce sync.Once

// RequirePlatformAPIKey returns a middleware that enforces a bearer-token
// gate on the workspace-tool surface.
//
//   - expectedToken is the MOSES_PLATFORM_API_KEY value mounted into the pod.
//     Empty → middleware fails-closed (503).
//   - The compare is constant-time (crypto/subtle) to avoid timing-leak signal
//     on the bearer token.
//
// Callers should wrap their workspace mux with this BEFORE MosesHeaders so
// the tenant header is only trusted after the bearer check passes.
func RequirePlatformAPIKey(expectedToken string) func(http.Handler) http.Handler {
	devBypass := os.Getenv(platformAuthDisabledEnv) == "true"
	if devBypass {
		authDisabledOnce.Do(func() {
			slog.Warn("BOT_PLATFORM_AUTH_DISABLED=true; workspace-tool surface bearer gate is OFF — local-dev ONLY",
				slog.String("middleware", "RequirePlatformAPIKey"))
		})
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Dev escape hatch: skip the check but log every request so the
			// misconfiguration is obvious in any production-leaning log scrape.
			if devBypass {
				slog.Warn("workspace-tool request bypassing bearer auth (BOT_PLATFORM_AUTH_DISABLED=true)",
					slog.String("path", r.URL.Path),
					slog.String("method", r.Method),
					slog.String("remote", r.RemoteAddr))
				next.ServeHTTP(w, r)
				return
			}

			// Fail-closed if the env var wasn't mounted into the pod. This is the
			// safer behavior: refusing to serve until the integration grant is
			// approved beats silently accepting unauthenticated traffic.
			if expectedToken == "" {
				slog.Error("workspace-tool request rejected: MOSES_PLATFORM_API_KEY is not configured",
					slog.String("path", r.URL.Path),
					slog.String("method", r.Method))
				writePlatformAuthError(w, http.StatusServiceUnavailable,
					"platform integration not configured", "platform_key_unset")
				return
			}

			got := extractPlatformBearer(r)
			if got == "" {
				writePlatformAuthError(w, http.StatusUnauthorized,
					"missing Authorization bearer", "unauthenticated")
				return
			}

			if subtle.ConstantTimeCompare([]byte(got), []byte(expectedToken)) != 1 {
				writePlatformAuthError(w, http.StatusUnauthorized,
					"invalid platform bearer", "unauthenticated")
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// extractPlatformBearer pulls "Bearer <token>" from Authorization. We
// intentionally do NOT fall back to the access_token cookie used by
// RequireUser — the workspace-tool surface is server-to-server only.
func extractPlatformBearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if h == "" {
		return ""
	}
	const prefix = "Bearer "
	if !strings.HasPrefix(h, prefix) {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(h, prefix))
}

// writePlatformAuthError emits the same {error, code} JSON shape the
// workspace handlers use, so MM-facing logs stay consistent.
func writePlatformAuthError(w http.ResponseWriter, status int, msg, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg, "code": code})
}
