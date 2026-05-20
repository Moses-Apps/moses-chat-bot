package mosesclient

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const sessionJSON = `{
	"id":"00000000-0000-0000-0000-000000000aaa",
	"tenant_id":"00000000-0000-0000-0000-0000000000bb",
	"started_by":"00000000-0000-0000-0000-0000000000cc",
	"mode":"freeform",
	"status":"active",
	"tickets_executed":0,
	"tickets_succeeded":0,
	"tickets_failed":0,
	"tickets_skipped":0,
	"max_concurrent_agents":3,
	"max_retries_per_ticket":3,
	"session_timeout_hours":24,
	"max_tickets_per_session":100,
	"auto_review_enabled":true,
	"created_at":"2026-05-19T00:00:00Z",
	"updated_at":"2026-05-19T00:00:00Z"
}`

func TestStartAutonomous(t *testing.T) {
	var gotPath string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(sessionJSON))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, BearerAuth{Token: "tok"})
	maxConcurrent := 5
	sess, err := c.StartAutonomous(context.Background(), AutonomousStartOpts{
		MaxConcurrentAgents: &maxConcurrent,
	})
	require.NoError(t, err)
	require.NotNil(t, sess)
	assert.Equal(t, "/api/v1/autonomous/start", gotPath)
	assert.Equal(t, "active", sess.Status)
	assert.Equal(t, "freeform", sess.Mode)

	var sent map[string]interface{}
	require.NoError(t, json.Unmarshal(gotBody, &sent))
	assert.Equal(t, "freeform", sent["mode"], "Mode must default to 'freeform' when omitted")
	assert.Equal(t, float64(5), sent["max_concurrent_agents"])
}

func TestStopAutonomous(t *testing.T) {
	id := uuid.New()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v1/autonomous/"+id.String()+"/stop", r.URL.Path)
		assert.Equal(t, http.MethodPost, r.Method)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, BearerAuth{Token: "tok"})
	assert.NoError(t, c.StopAutonomous(context.Background(), id))
}

func TestGetAutonomous(t *testing.T) {
	id := uuid.MustParse("00000000-0000-0000-0000-000000000aaa")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v1/autonomous/"+id.String(), r.URL.Path)
		assert.Equal(t, http.MethodGet, r.Method)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sessionJSON))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, BearerAuth{Token: "tok"})
	sess, err := c.GetAutonomous(context.Background(), id)
	require.NoError(t, err)
	assert.Equal(t, id, sess.ID)
}

func TestGetActiveAutonomous_200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v1/autonomous/active", r.URL.Path)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sessionJSON))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, BearerAuth{Token: "tok"})
	sess, err := c.GetActiveAutonomous(context.Background())
	require.NoError(t, err)
	require.NotNil(t, sess)
	assert.Equal(t, "active", sess.Status)
}

func TestGetActiveAutonomous_404IsNilNoError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"no active session"}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, BearerAuth{Token: "tok"})
	sess, err := c.GetActiveAutonomous(context.Background())
	assert.NoError(t, err, "404 from /autonomous/active is the normal 'no active' case")
	assert.Nil(t, sess)
}

func TestGetActiveAutonomous_403Propagates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"no permission"}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, BearerAuth{Token: "tok"})
	_, err := c.GetActiveAutonomous(context.Background())
	require.Error(t, err)
	assert.True(t, errIs(err, ErrForbidden))
}
