package mosesclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
)

// AutonomousSession mirrors the subset of types.AutonomousSession the
// bot reports back over Telegram (/autopilot status). Full shape lives
// in moses-platform-prep/backend/internal/types/agent_pod_types.go.
type AutonomousSession struct {
	ID                   uuid.UUID    `json:"id"`
	TenantID             uuid.UUID    `json:"tenant_id"`
	StartedBy            uuid.UUID    `json:"started_by"`
	Mode                 string       `json:"mode"`
	Status               string       `json:"status"`
	TicketsExecuted      int          `json:"tickets_executed"`
	TicketsSucceeded     int          `json:"tickets_succeeded"`
	TicketsFailed        int          `json:"tickets_failed"`
	TicketsSkipped       int          `json:"tickets_skipped"`
	MaxConcurrentAgents  int          `json:"max_concurrent_agents"`
	MaxRetriesPerTicket  int          `json:"max_retries_per_ticket"`
	SessionTimeoutHours  int          `json:"session_timeout_hours"`
	MaxTicketsPerSession int          `json:"max_tickets_per_session"`
	AutoReviewEnabled    bool         `json:"auto_review_enabled"`
	CreatedAt            time.Time    `json:"created_at"`
	UpdatedAt            time.Time    `json:"updated_at"`
	Summary              *string      `json:"summary,omitempty"`
	CompletedAt          optionalTime `json:"completed_at"`
}

// optionalTime decodes a nullable timestamp from moses-backend. It tolerates
// three wire shapes so decoding stays correct regardless of how the platform
// types the field:
//   - null / absent
//   - an RFC3339 string ("2026-05-20T09:51:23Z") — the intended shape
//   - {"Time":"...","Valid":bool} — Go's sql.NullTime leaking through
//     json.Marshal, which the platform currently does for
//     AutonomousSession.completed_at (tracked in moses-platform-prep).
type optionalTime struct {
	Time *time.Time
}

// UnmarshalJSON implements json.Unmarshaler for the shapes documented above.
func (o *optionalTime) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return nil
	}
	if trimmed[0] == '{' {
		var nt struct {
			Time  time.Time `json:"Time"`
			Valid bool      `json:"Valid"`
		}
		if err := json.Unmarshal(trimmed, &nt); err != nil {
			return err
		}
		if nt.Valid && !nt.Time.IsZero() {
			t := nt.Time
			o.Time = &t
		}
		return nil
	}
	var t time.Time
	if err := json.Unmarshal(trimmed, &t); err != nil {
		return err
	}
	o.Time = &t
	return nil
}

// AutonomousStartOpts is the request shape POSTed to
// /api/v1/autonomous/start. Mode is required by the platform handler
// (binding:"required,oneof=freeform"); the bot only ever uses
// "freeform".
type AutonomousStartOpts struct {
	Mode                 string `json:"mode"`
	MaxConcurrentAgents  *int   `json:"max_concurrent_agents,omitempty"`
	MaxRetriesPerTicket  *int   `json:"max_retries_per_ticket,omitempty"`
	SessionTimeoutHours  *int   `json:"session_timeout_hours,omitempty"`
	MaxTicketsPerSession *int   `json:"max_tickets_per_session,omitempty"`
	AutoReviewEnabled    *bool  `json:"auto_review_enabled,omitempty"`
}

// StartAutonomous calls POST /api/v1/autonomous/start.
//
// SPEC §9: callers MUST first call GetActiveAutonomous and refuse if
// a session exists owned by a DIFFERENT user (the platform auto-
// cancels any existing session on Start, so the pre-flight check is
// the only way to honour the "ask them to stop it first" rule).
func (c *Client) StartAutonomous(ctx context.Context, opts AutonomousStartOpts) (*AutonomousSession, error) {
	if opts.Mode == "" {
		opts.Mode = "freeform"
	}
	req, err := c.newRequest(ctx, http.MethodPost, "/api/v1/autonomous/start", opts)
	if err != nil {
		return nil, err
	}
	var session AutonomousSession
	if err := c.doJSON(req, &session); err != nil {
		return nil, err
	}
	return &session, nil
}

// StopAutonomous calls POST /api/v1/autonomous/:id/stop. 404 surfaces
// as ErrNotFound (session already cleaned up — caller usually treats
// that as success).
func (c *Client) StopAutonomous(ctx context.Context, sessionID uuid.UUID) error {
	req, err := c.newRequest(ctx, http.MethodPost, "/api/v1/autonomous/"+sessionID.String()+"/stop", nil)
	if err != nil {
		return err
	}
	return c.doJSON(req, nil)
}

// GetAutonomous calls GET /api/v1/autonomous/:id.
func (c *Client) GetAutonomous(ctx context.Context, sessionID uuid.UUID) (*AutonomousSession, error) {
	req, err := c.newRequest(ctx, http.MethodGet, "/api/v1/autonomous/"+sessionID.String(), nil)
	if err != nil {
		return nil, err
	}
	var session AutonomousSession
	if err := c.doJSON(req, &session); err != nil {
		return nil, err
	}
	return &session, nil
}

// GetActiveAutonomous calls GET /api/v1/autonomous/active. Returns
// (nil, nil) when no session is active — the platform serves 404 for
// that case, but we collapse it to the nil-session convention because
// "no session" is a normal pre-flight outcome, not an error worth
// propagating up to slash-command code.
func (c *Client) GetActiveAutonomous(ctx context.Context) (*AutonomousSession, error) {
	req, err := c.newRequest(ctx, http.MethodGet, "/api/v1/autonomous/active", nil)
	if err != nil {
		return nil, err
	}
	var session AutonomousSession
	if err := c.doJSON(req, &session); err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	if session.ID == uuid.Nil {
		// Platform sometimes returns 200 with an empty body when no
		// session is active (rather than 404). Treat that as nil.
		return nil, nil
	}
	return &session, nil
}
