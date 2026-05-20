// Package middleware holds stdlib-http middleware shared by the bot's
// HTTP handlers. v1 ships only RequireUser, which bridges the iframe
// session cookie to (moses_user_id, tenant_id) by forwarding to the
// platform's /api/v1/auth/me endpoint.
//
// Forward-to-platform is the safer of the two paths considered (the
// other being local JWT decode without signature verification). The bot
// pod and moses-backend are in the same cluster, so the extra in-cluster
// hop is cheap and lets the platform remain the single source of truth
// for session validity. If the user is logged out the platform returns
// 401 here and the bot returns 401 to the iframe verbatim — no risk of
// the bot accepting a forged or stale token.
package middleware

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"moses-chat-bot/backend/internal/mosesclient"
)

// ContextKey is the typed key used to stamp identity into request context.
type ContextKey string

const (
	UserIDKey   ContextKey = "moses_user_id"
	TenantIDKey ContextKey = "tenant_id"
	BearerKey   ContextKey = "bearer_token"
)

// AuthValidator is the minimal interface RequireUser needs from the
// mosesclient. Decoupling allows tests to inject a fake without standing
// up an HTTP stub.
type AuthValidator interface {
	GetMe(ctx context.Context, bearer string, tenantID uuid.UUID) (*mosesclient.Me, error)
}

// RequireUser constructs the middleware. The validator is called on
// every request — there's no per-bot session cache yet (cookie revoke
// must propagate quickly; 10ms in-cluster RTT is fine for the linking
// UI's low request rate).
func RequireUser(validator AuthValidator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			bearer := extractBearer(r)
			if bearer == "" {
				writeJSONError(w, http.StatusUnauthorized, "missing credentials")
				return
			}

			var tenantID uuid.UUID
			if raw := r.Header.Get("X-Tenant-ID"); raw != "" {
				parsed, err := uuid.Parse(raw)
				if err != nil {
					writeJSONError(w, http.StatusBadRequest, "invalid X-Tenant-ID")
					return
				}
				tenantID = parsed
			}

			me, err := validator.GetMe(r.Context(), bearer, tenantID)
			if err != nil {
				if errors.Is(err, mosesclient.ErrUnauthorized) {
					writeJSONError(w, http.StatusUnauthorized, "session invalid")
					return
				}
				if errors.Is(err, mosesclient.ErrForbidden) {
					writeJSONError(w, http.StatusForbidden, "forbidden")
					return
				}
				writeJSONError(w, http.StatusBadGateway, "auth lookup failed")
				return
			}

			userID, err := uuid.Parse(me.ID)
			if err != nil {
				writeJSONError(w, http.StatusBadGateway, "auth lookup returned malformed user")
				return
			}

			resolvedTenant := tenantID
			if resolvedTenant == uuid.Nil {
				if len(me.TenantMemberships) == 0 {
					writeJSONError(w, http.StatusForbidden, "no tenant memberships")
					return
				}
				resolvedTenant = me.TenantMemberships[0].TenantID
			} else {
				ok := false
				for _, m := range me.TenantMemberships {
					if m.TenantID == resolvedTenant {
						ok = true
						break
					}
				}
				if !ok && !me.IsGlobalAdmin {
					writeJSONError(w, http.StatusForbidden, "not a member of requested tenant")
					return
				}
			}

			ctx := context.WithValue(r.Context(), UserIDKey, userID)
			ctx = context.WithValue(ctx, TenantIDKey, resolvedTenant)
			ctx = context.WithValue(ctx, BearerKey, bearer)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// extractBearer pulls a bearer token from Authorization or the
// access_token cookie. The iframe SDK uses the cookie path; programmatic
// callers use Authorization.
func extractBearer(r *http.Request) string {
	if h := r.Header.Get("Authorization"); h != "" {
		if strings.HasPrefix(h, "Bearer ") {
			return strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
		}
	}
	if c, err := r.Cookie("access_token"); err == nil && c.Value != "" {
		return c.Value
	}
	return ""
}

// UserID returns the user UUID stamped by RequireUser. Returns uuid.Nil
// if absent (which indicates a routing bug — RequireUser should always
// run before any handler that reads this).
func UserID(ctx context.Context) uuid.UUID {
	v, _ := ctx.Value(UserIDKey).(uuid.UUID)
	return v
}

// TenantID returns the tenant UUID stamped by RequireUser.
func TenantID(ctx context.Context) uuid.UUID {
	v, _ := ctx.Value(TenantIDKey).(uuid.UUID)
	return v
}

// Bearer returns the validated bearer token from request context.
func Bearer(ctx context.Context) string {
	v, _ := ctx.Value(BearerKey).(string)
	return v
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
