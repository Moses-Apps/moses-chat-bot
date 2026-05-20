// Package middleware — moses_headers
//
// This middleware terminates the workspace-tool path: Moses Manager (running
// inside the cluster under MOSES_PLATFORM_API_KEY) reaches the bot via the
// platform's workspace-tool proxy, which validates the platform key and
// injects per-tenant identity headers. The bot trusts those headers to the
// extent that the proxy reaches it on an in-cluster network path — anything
// exposed to the public internet must NOT use this middleware.
//
// Headers (vendored verbatim from moses-default-app-templates/fullstack-chat):
//   - X-Moses-Tenant-ID  (required for tenant-scoped queries)
//   - X-Moses-User-ID    (optional; the calling user inside MM, when applicable)
//   - X-Moses-Chart-ID   (optional; chart scope of the call)
//   - X-Moses-Request-ID (optional; for log correlation)
package middleware

import (
	"context"
	"net/http"
)

// MosesContext holds the parsed Moses-* request headers. Strings (not UUIDs)
// at this layer — handlers parse to uuid.UUID and reject malformed values.
type MosesContext struct {
	TenantID  string
	UserID    string
	ChartID   string
	RequestID string
}

// mosesHeadersKey is the typed context key under which MosesContext is
// stamped. Kept package-private so callers go through GetMosesContext.
type mosesHeadersKey struct{}

// MosesHeaders extracts the Moses-* headers and stamps them into the
// request context. It never rejects — handlers decide whether the absent
// tenant header is fatal (it usually is, but read-only health probes and
// the OpenAPI spec endpoint are exempt and route around this middleware).
func MosesHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), mosesHeadersKey{}, MosesContext{
			TenantID:  r.Header.Get("X-Moses-Tenant-ID"),
			UserID:    r.Header.Get("X-Moses-User-ID"),
			ChartID:   r.Header.Get("X-Moses-Chart-ID"),
			RequestID: r.Header.Get("X-Moses-Request-ID"),
		})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// GetMosesContext returns the parsed MosesContext from request context, or
// the zero value when the middleware wasn't applied (handlers MUST treat the
// zero TenantID as missing/invalid).
func GetMosesContext(ctx context.Context) MosesContext {
	if v, ok := ctx.Value(mosesHeadersKey{}).(MosesContext); ok {
		return v
	}
	return MosesContext{}
}
