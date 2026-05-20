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

// TestCreateConversation verifies the request shape, auth header,
// envelope unwrap, and the source/chart_id context merge.
func TestCreateConversation(t *testing.T) {
	chartID := uuid.New()
	convID := uuid.New()
	title := "telegram-bridge"

	var gotPath, gotAuth string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotBody, _ = io.ReadAll(r.Body)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"conversation":{
			"id":"` + convID.String() + `",
			"userId":"` + uuid.New().String() + `",
			"tenantId":"` + uuid.New().String() + `",
			"title":"telegram-bridge",
			"createdAt":"2026-05-19T00:00:00Z",
			"updatedAt":"2026-05-19T00:00:00Z"
		}}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, BearerAuth{Token: "tok"})
	conv, err := c.CreateConversation(context.Background(), CreateConversationOpts{
		Title:   &title,
		Source:  "chat-bot-bridge",
		ChartID: &chartID,
		ExtraContext: map[string]interface{}{
			"telegram_chat_id": "98765",
		},
	})
	require.NoError(t, err)
	require.NotNil(t, conv)

	assert.Equal(t, convID, conv.ID)
	assert.Equal(t, "/api/v1/chat/conversations", gotPath)
	assert.Equal(t, "Bearer tok", gotAuth)

	var sent struct {
		Title   *string         `json:"title"`
		Context json.RawMessage `json:"context"`
	}
	require.NoError(t, json.Unmarshal(gotBody, &sent))
	require.NotNil(t, sent.Title)
	assert.Equal(t, title, *sent.Title)

	var ctxMap map[string]interface{}
	require.NoError(t, json.Unmarshal(sent.Context, &ctxMap))
	assert.Equal(t, "chat-bot-bridge", ctxMap["source"])
	assert.Equal(t, chartID.String(), ctxMap["chart_id"])
	assert.Equal(t, "98765", ctxMap["telegram_chat_id"])
}

// TestCreateConversation_ServerError surfaces typed errors.
func TestCreateConversationServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"bad key"}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, BearerAuth{Token: "bad"})
	_, err := c.CreateConversation(context.Background(), CreateConversationOpts{Source: "x"})
	require.Error(t, err)
	assert.True(t, isUnauthorized(err))
}

// TestRevokeAPIKey_404IsNotError verifies 404 is collapsed to nil
// (best-effort revoke semantic from SPEC §4).
func TestRevokeAPIKey404Collapse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"not found"}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, BearerAuth{Token: "tok"})
	err := c.RevokeAPIKey(context.Background(), uuid.New())
	assert.NoError(t, err, "404 from DELETE /api-keys/:id must be treated as success")
}

// TestRevokeAPIKey_500Surfaces verifies non-404 errors propagate.
func TestRevokeAPIKey500Surfaces(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"oops"}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, BearerAuth{Token: "tok"})
	err := c.RevokeAPIKey(context.Background(), uuid.New())
	require.Error(t, err)
	assert.False(t, isUnauthorized(err))
}

// helper using errors.Is to keep tests readable
func isUnauthorized(err error) bool { return err != nil && (err.Error() != "" && errIs(err, ErrUnauthorized)) }

func errIs(err, target error) bool {
	for e := err; e != nil; {
		if e == target {
			return true
		}
		u, ok := e.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		e = u.Unwrap()
	}
	return false
}
