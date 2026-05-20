// Package mosesclient is the typed Go wrapper for the subset of the
// moses-backend HTTP + WebSocket surface that moses-chat-bot consumes.
//
// All callers should use these typed errors with errors.Is so they can
// distinguish "user revoked the API key" (ErrUnauthorized → mark the
// chat_relay_link inactive and prompt the user to re-link) from
// transient server faults (ErrServerError → retry with backoff).
package mosesclient

import "errors"

// Sentinel errors. Use errors.Is to check; the client wraps the raw
// HTTP status / parsed body inside an *APIError that exposes the
// upstream code + message.
var (
	// ErrUnauthorized — moses-backend returned HTTP 401. The API key was
	// revoked, expired, or never valid. Callers MUST treat this as
	// "stop using the key" — flip the link inactive with
	// deactivation_reason = "platform_401" and tell the user to re-link.
	ErrUnauthorized = errors.New("mosesclient: unauthorized (401)")

	// ErrForbidden — HTTP 403. The key authenticates fine but lacks the
	// required RBAC permission (e.g. trying to start autopilot without
	// CREATE AUTONOMOUS_SESSIONS).
	ErrForbidden = errors.New("mosesclient: forbidden (403)")

	// ErrNotFound — HTTP 404. Returned for missing conversations,
	// missing autonomous sessions (esp. on GET /autonomous/active when
	// no session is active), or unknown API key IDs on DELETE.
	ErrNotFound = errors.New("mosesclient: not found (404)")

	// ErrRateLimited — HTTP 429. The APIError carries a RetryAfter
	// duration parsed from the Retry-After header if present.
	ErrRateLimited = errors.New("mosesclient: rate limited (429)")

	// ErrServerError — HTTP 5xx. Transient by convention; callers may
	// retry with backoff.
	ErrServerError = errors.New("mosesclient: server error (5xx)")

	// ErrWSDisconnected — the WebSocket subscriber gave up reconnecting
	// after exceeding its max attempts. Surfaced on the Events channel
	// before the channel closes.
	ErrWSDisconnected = errors.New("mosesclient: websocket disconnected (max retries exceeded)")

	// ErrWSAuthFailed — the WebSocket initial handshake was rejected
	// with HTTP 401/403 by moses-backend. The token is bad; no point
	// retrying.
	ErrWSAuthFailed = errors.New("mosesclient: websocket auth failed")
)

// APIError is the rich error returned by HTTP methods. The sentinel
// returned by Unwrap is one of the Err* values above so callers can
// errors.Is(err, ErrUnauthorized) etc.; the full struct exposes the
// upstream status, parsed code, and message for logs.
type APIError struct {
	Status     int    // HTTP status code (401, 404, 500, ...)
	Code       string // upstream error code, e.g. "invalid_app_context"
	Message    string // upstream human-readable message
	RetryAfter int    // seconds, parsed from Retry-After header (429 only)
	sentinel   error  // one of the Err* sentinels above
}

// Error implements the error interface.
func (e *APIError) Error() string {
	if e.Message != "" {
		return e.sentinel.Error() + ": " + e.Message
	}
	return e.sentinel.Error()
}

// Unwrap exposes the sentinel for errors.Is.
func (e *APIError) Unwrap() error { return e.sentinel }

// classifyHTTPStatus maps an HTTP status code to the corresponding
// sentinel. Returns nil when status is 2xx.
func classifyHTTPStatus(status int) error {
	switch {
	case status >= 200 && status < 300:
		return nil
	case status == 401:
		return ErrUnauthorized
	case status == 403:
		return ErrForbidden
	case status == 404:
		return ErrNotFound
	case status == 429:
		return ErrRateLimited
	case status >= 500:
		return ErrServerError
	default:
		// 400-class non-auth — surface as a generic server-side error so
		// callers see a typed *APIError without us having to invent a
		// new sentinel for every 4xx. The Status field carries the
		// detail.
		return ErrServerError
	}
}
