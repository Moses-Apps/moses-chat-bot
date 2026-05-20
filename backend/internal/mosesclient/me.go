package mosesclient

import (
	"context"
	"net/http"
	"time"

	"github.com/google/uuid"
)

// TenantMembership mirrors the platform's types.TenantMembership shape.
type TenantMembership struct {
	TenantID uuid.UUID `json:"tenantId"`
	Role     string    `json:"role"`
}

// Me mirrors types.UserWithMemberships from moses-platform-prep — only the
// fields the bot actually consumes. The auth middleware uses this response
// to confirm the inbound JWT/cookie is valid AND to verify the requested
// tenant is in the user's membership set before proceeding.
type Me struct {
	ID                string             `json:"id"`
	Email             string             `json:"email"`
	IsGlobalAdmin     bool               `json:"isGlobalAdmin"`
	TenantMemberships []TenantMembership `json:"tenantMemberships"`
}

// GetMe calls GET /auth/me with the supplied bearer token (which may be a
// session JWT extracted from the user's access_token cookie). It is the
// authoritative validation path: a 200 means the platform considers the
// caller a logged-in user. tenantID may be uuid.Nil; when non-nil it is
// forwarded as X-Tenant-ID so the platform resolves the user's tenant
// context the same way the browser would.
//
// NOTE: the platform mounts auth routes at /auth/* — NOT under the /api/v1
// prefix every other moses-backend endpoint uses. /api/v1/auth/me returns
// 404, which surfaced to users as a 502 "auth lookup failed" on every
// bot request.
//
// Used by the bot's RequireUser middleware to bridge the iframe cookie to
// (moses_user_id, tenant_id) without local JWT signature trust.
func (c *Client) GetMe(ctx context.Context, bearer string, tenantID uuid.UUID) (*Me, error) {
	req, err := c.newRequest(ctx, http.MethodGet, "/auth/me", nil)
	if err != nil {
		return nil, err
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if tenantID != uuid.Nil {
		req.Header.Set("X-Tenant-ID", tenantID.String())
	}

	reqCtx, cancel := context.WithTimeout(req.Context(), 10*time.Second)
	defer cancel()
	req = req.WithContext(reqCtx)

	var me Me
	if err := c.doJSON(req, &me); err != nil {
		return nil, err
	}
	return &me, nil
}
