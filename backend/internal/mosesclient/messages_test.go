package mosesclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGetConversationMessages verifies the request shape (path, auth, limit
// query) and the envelope unwrap. The platform returns messages in
// chronological order under a {"messages": [...]} envelope.
func TestGetConversationMessages(t *testing.T) {
	convID := uuid.New()

	var gotPath, gotAuth, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"messages":[
			{"id":"` + uuid.New().String() + `","conversationId":"` + convID.String() + `","role":"user","content":"hello","createdAt":"2026-05-21T10:00:00Z"},
			{"id":"` + uuid.New().String() + `","conversationId":"` + convID.String() + `","role":"assistant","content":"hi back","createdAt":"2026-05-21T10:00:05Z"}
		]}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, BearerAuth{Token: "tok"})
	msgs, err := c.GetConversationMessages(context.Background(), convID, 50)
	require.NoError(t, err)
	require.Len(t, msgs, 2)

	assert.Equal(t, "/api/v1/chat/conversations/"+convID.String()+"/messages", gotPath)
	assert.Equal(t, "Bearer tok", gotAuth)
	assert.Equal(t, "limit=50", gotQuery)

	assert.Equal(t, "user", msgs[0].Role)
	assert.Equal(t, "assistant", msgs[1].Role)
	assert.Equal(t, "hi back", msgs[1].Content)
	assert.True(t, msgs[1].CreatedAt.After(msgs[0].CreatedAt), "messages must arrive chronologically")
}

// TestGetConversationMessages_NoLimit verifies that limit<=0 omits the query
// parameter entirely (the platform then returns the full history).
func TestGetConversationMessages_NoLimit(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"messages":[]}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, BearerAuth{Token: "tok"})
	_, err := c.GetConversationMessages(context.Background(), uuid.New(), 0)
	require.NoError(t, err)
	assert.Empty(t, gotQuery, "limit<=0 must not emit a query parameter")
}

// TestGetConversationMessages_401 verifies auth errors surface as
// ErrUnauthorized so the relay can deactivate the link.
func TestGetConversationMessages_401(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"bad key"}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, BearerAuth{Token: "bad"})
	_, err := c.GetConversationMessages(context.Background(), uuid.New(), 10)
	require.Error(t, err)
	assert.True(t, errIs(err, ErrUnauthorized))
}

// TestGetConversationMessages_404 verifies an unknown conversation surfaces
// as ErrNotFound (the relay collapses that to a zero baseline).
func TestGetConversationMessages_404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"not found"}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, BearerAuth{Token: "tok"})
	_, err := c.GetConversationMessages(context.Background(), uuid.New(), 10)
	require.Error(t, err)
	assert.True(t, errIs(err, ErrNotFound))
}
