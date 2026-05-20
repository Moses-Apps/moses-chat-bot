package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"moses-chat-bot/backend/internal/provider"
)

// newTestAPI wires APIClient at the given httptest.Server URL.
func newTestAPI(t *testing.T, h http.HandlerFunc) (*APIClient, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	client := NewAPIClient("test-token", srv.Client())
	client.baseURL = srv.URL
	return client, srv
}

func TestAPI_SendMessage_RequestShape(t *testing.T) {
	var gotPath, gotContentType string
	var gotBody map[string]any

	client, _ := newTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotContentType = r.Header.Get("Content-Type")
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok": true, "result": {"message_id": 1}}`)
	})

	err := client.SendMessage(context.Background(), SendMessageParams{
		ChatID: "42",
		Text:   "hello",
	})
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if gotPath != "/bottest-token/sendMessage" {
		t.Fatalf("unexpected path: %s", gotPath)
	}
	if gotContentType != "application/json" {
		t.Fatalf("unexpected content-type: %s", gotContentType)
	}
	if gotBody["chat_id"] != "42" || gotBody["text"] != "hello" {
		t.Fatalf("unexpected body: %+v", gotBody)
	}
}

func TestAPI_SendMessage_4xx_TypedError(t *testing.T) {
	client, _ := newTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"ok": false, "error_code": 400, "description": "Bad Request: chat not found"}`)
	})

	err := client.SendMessage(context.Background(), SendMessageParams{ChatID: "x", Text: "y"})
	var tgErr *telegramError
	if !errors.As(err, &tgErr) {
		t.Fatalf("expected *telegramError, got %T: %v", err, err)
	}
	if tgErr.ErrorCode != 400 || !strings.Contains(tgErr.Description, "chat not found") {
		t.Fatalf("unexpected telegramError: %+v", tgErr)
	}
}

func TestAPI_SendMessage_429_RateLimited(t *testing.T) {
	client, _ := newTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "3")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"ok": false, "error_code": 429, "description": "Too Many Requests", "parameters": {"retry_after": 3}}`)
	})

	err := client.SendMessage(context.Background(), SendMessageParams{ChatID: "x", Text: "y"})
	retry, ok := IsRateLimited(err)
	if !ok {
		t.Fatalf("expected rate-limited error, got %v", err)
	}
	if retry != 3*time.Second {
		t.Fatalf("Retry-After=%s, want 3s", retry)
	}
}

func TestAPI_Network_Error_Wrapped(t *testing.T) {
	// Server that immediately closes the connection.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Fatal("hijack unsupported")
		}
		conn, _, err := hj.Hijack()
		if err != nil {
			t.Fatal(err)
		}
		_ = conn.Close()
	}))
	t.Cleanup(srv.Close)

	client := NewAPIClient("tok", srv.Client())
	client.baseURL = srv.URL

	err := client.SendMessage(context.Background(), SendMessageParams{ChatID: "x", Text: "y"})
	if !errors.Is(err, provider.ErrProviderUnavailable) {
		t.Fatalf("expected ErrProviderUnavailable, got %v", err)
	}
}

func TestAPI_GetWebhookInfo_DecodesResult(t *testing.T) {
	client, _ := newTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/bottest-token/getWebhookInfo" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok": true, "result": {"url": "https://example.com/hook", "has_custom_certificate": false, "pending_update_count": 0}}`)
	})

	info, err := client.GetWebhookInfo(context.Background())
	if err != nil {
		t.Fatalf("GetWebhookInfo: %v", err)
	}
	if info.URL != "https://example.com/hook" {
		t.Fatalf("URL=%q", info.URL)
	}
}

func TestAPI_SetWebhook_SendsExpectedPayload(t *testing.T) {
	var gotBody map[string]any
	client, _ := newTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/bottest-token/setWebhook" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok": true, "result": true}`)
	})

	err := client.SetWebhook(context.Background(), SetWebhookParams{
		URL:            "https://example.com/hook",
		SecretToken:    "s3cr3t",
		AllowedUpdates: []string{"message"},
	})
	if err != nil {
		t.Fatalf("SetWebhook: %v", err)
	}
	if gotBody["url"] != "https://example.com/hook" {
		t.Fatalf("url=%v", gotBody["url"])
	}
	if gotBody["secret_token"] != "s3cr3t" {
		t.Fatalf("secret_token=%v", gotBody["secret_token"])
	}
	upd, _ := gotBody["allowed_updates"].([]any)
	if len(upd) != 1 || upd[0] != "message" {
		t.Fatalf("allowed_updates=%v", gotBody["allowed_updates"])
	}
}

func TestAPI_DeleteWebhook_OK(t *testing.T) {
	client, _ := newTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/bottest-token/deleteWebhook" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		fmt.Fprint(w, `{"ok": true, "result": true}`)
	})
	if err := client.DeleteWebhook(context.Background()); err != nil {
		t.Fatalf("DeleteWebhook: %v", err)
	}
}

func TestAPI_GetMe_OK(t *testing.T) {
	client, _ := newTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/bottest-token/getMe" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		fmt.Fprint(w, `{"ok":true,"result":{"id":777,"is_bot":true,"username":"moses_demo_bot"}}`)
	})
	me, err := client.GetMe(context.Background())
	if err != nil {
		t.Fatalf("GetMe: %v", err)
	}
	if me.ID != 777 || me.Username != "moses_demo_bot" || !me.IsBot {
		t.Fatalf("unexpected BotUser: %+v", me)
	}
}

func TestAPI_GetMe_InvalidToken_TypedError(t *testing.T) {
	client, _ := newTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"ok":false,"error_code":401,"description":"Unauthorized"}`)
	})
	_, err := client.GetMe(context.Background())
	if err == nil {
		t.Fatal("expected error for invalid token")
	}
	te, ok := AsTelegramError(err)
	if !ok {
		t.Fatalf("expected *telegramError, got %T: %v", err, err)
	}
	if te.Code() != 401 {
		t.Fatalf("expected error_code 401, got %d", te.Code())
	}
}

func TestAPI_SetMyCommands_RequestShape(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	client, _ := newTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		fmt.Fprint(w, `{"ok":true,"result":true}`)
	})
	err := client.SetMyCommands(context.Background(), SetMyCommandsParams{
		Commands: []BotCommand{{Command: "start", Description: "Welcome"}},
	})
	if err != nil {
		t.Fatalf("SetMyCommands: %v", err)
	}
	if gotPath != "/bottest-token/setMyCommands" {
		t.Fatalf("unexpected path %s", gotPath)
	}
	cmds, ok := gotBody["commands"].([]any)
	if !ok || len(cmds) != 1 {
		t.Fatalf("unexpected commands payload: %v", gotBody["commands"])
	}
}
