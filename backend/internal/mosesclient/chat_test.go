package mosesclient

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestStreamChatMessage_AckShape verifies the fire-and-forget ack the
// platform returns from /api/v1/ai/chat/stream (per ai_chat_handlers.go:448).
func TestStreamChatMessage_AckShape(t *testing.T) {
	var gotPath, gotAuth string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"processing","conversationId":"conv-1","requestId":"req-1"}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, BearerAuth{Token: "tok"})
	ack, err := c.StreamChatMessage(context.Background(), ChatStreamOpts{
		Message:        "hello bot",
		ConversationID: "conv-1",
		RequestID:      "req-1",
	})
	require.NoError(t, err)
	require.NotNil(t, ack)

	assert.Equal(t, "/api/v1/ai/chat/stream", gotPath)
	assert.Equal(t, "Bearer tok", gotAuth)
	assert.Equal(t, "processing", ack.Status)
	assert.Equal(t, "conv-1", ack.ConversationID)
	assert.Equal(t, "req-1", ack.RequestID)

	var sent map[string]interface{}
	require.NoError(t, json.Unmarshal(gotBody, &sent))
	assert.Equal(t, "hello bot", sent["message"])
	assert.Equal(t, "conv-1", sent["conversationId"])
}

// TestStreamChatMessage_401 verifies auth errors surface as ErrUnauthorized.
func TestStreamChatMessage_401(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"bad key"}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, BearerAuth{Token: "bad"})
	_, err := c.StreamChatMessage(context.Background(), ChatStreamOpts{Message: "x"})
	require.Error(t, err)
	assert.True(t, errIs(err, ErrUnauthorized))
}
