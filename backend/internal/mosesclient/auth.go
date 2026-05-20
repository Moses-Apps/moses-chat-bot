package mosesclient

import "net/http"

// Auth is the strategy interface that decorates outbound HTTP requests
// with the credentials moses-backend's AuthMiddlewareWithAPIKey expects.
// Two implementations ship: BearerAuth (the standard one for user-owned
// MCP API keys — moses-backend accepts both Authorization: Bearer and
// X-API-Key for the same key) and APIKeyHeader (X-API-Key only, used
// for service-account keys where the platform team explicitly chose
// the header path).
type Auth interface {
	Apply(*http.Request)
}

// BearerAuth sets the Authorization: Bearer <token> header. This is
// the preferred shape for user-minted API keys travelling from
// moses-chat-bot to moses-backend (SPEC §4, step 9).
type BearerAuth struct{ Token string }

// Apply implements Auth.
func (b BearerAuth) Apply(r *http.Request) {
	if b.Token == "" {
		return
	}
	r.Header.Set("Authorization", "Bearer "+b.Token)
}

// APIKeyHeader sets the X-API-Key header. Use for callers that have
// already standardised on the header-only shape (e.g. some
// service-account flows). Functionally equivalent to BearerAuth on the
// moses-backend side but kept separate so the wire is predictable in
// logs.
type APIKeyHeader struct{ Token string }

// Apply implements Auth.
func (a APIKeyHeader) Apply(r *http.Request) {
	if a.Token == "" {
		return
	}
	r.Header.Set("X-API-Key", a.Token)
}

// noAuth is the zero-auth strategy used when callers explicitly opt
// out (e.g. /health probes). Not exported — pass nil to NewClient to
// get the same effect.
type noAuth struct{}

func (noAuth) Apply(*http.Request) {}
