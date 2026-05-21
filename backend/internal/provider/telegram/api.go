package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"moses-chat-bot/backend/internal/provider"
)

// defaultBaseURL is the canonical Telegram Bot API host. Tests override via
// APIClient.baseURL so a httptest.Server can stand in for the real API.
const defaultBaseURL = "https://api.telegram.org"

// defaultTimeout is the http.Client.Timeout for the bundled client when the
// caller supplies none. It is a BACKSTOP, not the primary bound: every call
// passes a context whose deadline does the real governing — a quick
// setWebhook/sendMessage, or the getUpdates long-poll whose context is
// longPollTimeout + pollHTTPSlack (~45s, see adapter.go).
//
// It MUST comfortably exceed that long-poll budget. http.Client.Timeout is a
// hard wall on the WHOLE request regardless of the context, so a value shorter
// than the long-poll aborts every empty getUpdates client-side with
// "Client.Timeout exceeded while awaiting headers" — silently breaking ALL
// inbound Telegram traffic. (It was 10s, shorter than the 30s long-poll; the
// regression guard TestDefaultTimeoutExceedsLongPoll keeps this honest.)
const defaultTimeout = 60 * time.Second

// APIClient is the typed, minimal wrapper around the Telegram Bot API.
// It is intentionally NOT a generic SDK — only the four methods the relay
// needs are exposed.
type APIClient struct {
	botToken string
	baseURL  string
	hc       *http.Client
}

// NewAPIClient constructs a client that talks to the public Telegram API.
// botToken must be non-empty; callers should validate format up the stack.
func NewAPIClient(botToken string, hc *http.Client) *APIClient {
	if hc == nil {
		hc = &http.Client{Timeout: defaultTimeout}
	}
	return &APIClient{
		botToken: botToken,
		baseURL:  defaultBaseURL,
		hc:       hc,
	}
}

// methodURL returns the fully qualified URL for a given Bot API method.
func (c *APIClient) methodURL(method string) string {
	return fmt.Sprintf("%s/bot%s/%s", c.baseURL, c.botToken, method)
}

// telegramError is returned for non-2xx, non-429 responses. It is wrapped
// so callers can use errors.As to inspect ErrorCode / Description.
type telegramError struct {
	ErrorCode   int
	Description string
	Status      int
}

func (e *telegramError) Error() string {
	return fmt.Sprintf("telegram api error: status=%d code=%d: %s", e.Status, e.ErrorCode, e.Description)
}

// rateLimitedError is a transient signal: callers should sleep RetryAfter
// then retry. Mapped to provider.ErrRateLimited when the cumulative wait
// exceeds the adapter's budget.
type rateLimitedError struct {
	RetryAfter time.Duration
}

func (e *rateLimitedError) Error() string {
	return fmt.Sprintf("telegram rate limited; retry after %s", e.RetryAfter)
}

// callJSON POSTs a JSON-encoded payload to the given method and decodes the
// response envelope. The Result of a successful response is left as raw bytes
// so callers can decode into a method-specific shape.
func (c *APIClient) callJSON(ctx context.Context, method string, payload any) (json.RawMessage, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal %s payload: %w", method, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.methodURL(method), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build %s request: %w", method, err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.hc.Do(req)
	if err != nil {
		// Network-level failures (DNS, connection refused, timeout, etc.).
		return nil, fmt.Errorf("%w: %s: %v", provider.ErrProviderUnavailable, method, err)
	}
	defer resp.Body.Close()

	rawBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read %s body: %w", method, err)
	}

	if resp.StatusCode == http.StatusTooManyRequests {
		// Telegram sometimes echoes Retry-After in the JSON envelope,
		// sometimes only in the header.
		retry := parseRetryAfterHeader(resp.Header.Get("Retry-After"))
		var env struct {
			Parameters *respParameters `json:"parameters,omitempty"`
		}
		_ = json.Unmarshal(rawBody, &env)
		if env.Parameters != nil && env.Parameters.RetryAfter > 0 {
			retry = time.Duration(env.Parameters.RetryAfter) * time.Second
		}
		if retry <= 0 {
			retry = time.Second
		}
		return nil, &rateLimitedError{RetryAfter: retry}
	}

	// Decode the envelope to determine ok / result / description.
	var env struct {
		OK          bool            `json:"ok"`
		Description string          `json:"description"`
		ErrorCode   int             `json:"error_code"`
		Result      json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(rawBody, &env); err != nil {
		return nil, fmt.Errorf("decode %s response: %w (body=%q)", method, err, truncate(rawBody, 256))
	}

	if !env.OK || resp.StatusCode >= 400 {
		return nil, &telegramError{
			ErrorCode:   env.ErrorCode,
			Description: env.Description,
			Status:      resp.StatusCode,
		}
	}

	return env.Result, nil
}

// parseRetryAfterHeader handles both the integer-seconds and HTTP-date forms.
// Only integer-seconds is observed in practice from Telegram; the date form
// is treated as zero (caller falls back to envelope/default).
func parseRetryAfterHeader(v string) time.Duration {
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	return 0
}

// truncate trims b for inclusion in error messages.
func truncate(b []byte, n int) []byte {
	if len(b) <= n {
		return b
	}
	return b[:n]
}

// SendMessageParams is the typed payload for sendMessage.
type SendMessageParams struct {
	ChatID    string `json:"chat_id"`
	Text      string `json:"text"`
	ParseMode string `json:"parse_mode,omitempty"`
}

// SendMessage invokes the Bot API sendMessage method. Errors flow through
// callJSON; see telegramError and rateLimitedError for the typed variants.
func (c *APIClient) SendMessage(ctx context.Context, p SendMessageParams) error {
	_, err := c.callJSON(ctx, "sendMessage", p)
	return err
}

// SetWebhookParams is the typed payload for setWebhook.
type SetWebhookParams struct {
	URL            string   `json:"url"`
	SecretToken    string   `json:"secret_token,omitempty"`
	AllowedUpdates []string `json:"allowed_updates,omitempty"`
}

// SetWebhook registers a webhook URL with Telegram. Idempotent: callers
// should typically pre-check via GetWebhookInfo and skip if unchanged.
func (c *APIClient) SetWebhook(ctx context.Context, p SetWebhookParams) error {
	_, err := c.callJSON(ctx, "setWebhook", p)
	return err
}

// GetWebhookInfo retrieves the currently configured webhook. Telegram does
// not return the secret_token via this method; comparison must rely on URL.
func (c *APIClient) GetWebhookInfo(ctx context.Context) (*WebhookInfo, error) {
	raw, err := c.callJSON(ctx, "getWebhookInfo", struct{}{})
	if err != nil {
		return nil, err
	}
	var info WebhookInfo
	if err := json.Unmarshal(raw, &info); err != nil {
		return nil, fmt.Errorf("decode getWebhookInfo result: %w", err)
	}
	return &info, nil
}

// DeleteWebhook removes the current webhook. Useful for tests and for
// rolling back from a misconfigured deploy.
func (c *APIClient) DeleteWebhook(ctx context.Context) error {
	_, err := c.callJSON(ctx, "deleteWebhook", struct{}{})
	return err
}

// GetUpdatesParams is the typed payload for getUpdates (long-polling).
//
// Offset is max(update_id of the previous batch)+1; passing it acknowledges
// every update below it, so Telegram never re-sends them. Timeout is the
// long-poll hold time in seconds — the call blocks server-side until an
// update arrives or Timeout elapses, which keeps the poll loop cheap.
type GetUpdatesParams struct {
	Offset         int64    `json:"offset,omitempty"`
	Timeout        int      `json:"timeout,omitempty"`
	AllowedUpdates []string `json:"allowed_updates,omitempty"`
}

// GetUpdates invokes the Bot API getUpdates method. It returns the batch of
// updates Telegram has buffered since Offset. A getUpdates call fails with a
// 409 (telegramError) while a webhook is active — callers must DeleteWebhook
// first. The context deadline must comfortably exceed Timeout so the HTTP
// client does not abort a healthy long-poll.
func (c *APIClient) GetUpdates(ctx context.Context, p GetUpdatesParams) ([]Update, error) {
	raw, err := c.callJSON(ctx, "getUpdates", p)
	if err != nil {
		return nil, err
	}
	var updates []Update
	if err := json.Unmarshal(raw, &updates); err != nil {
		return nil, fmt.Errorf("decode getUpdates result: %w", err)
	}
	return updates, nil
}

// BotUser is the subset of the getMe response the bot config flow needs.
// It identifies the bot account behind a token: the numeric id and the
// @username a user types in Telegram to find it.
type BotUser struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name,omitempty"`
	Username  string `json:"username,omitempty"`
}

// GetMe calls the Bot API getMe method. It is the canonical way to validate a
// bot token: a 401 (telegramError, ErrorCode 401) means the token is invalid
// or revoked. On success it returns the bot's identity.
func (c *APIClient) GetMe(ctx context.Context) (*BotUser, error) {
	raw, err := c.callJSON(ctx, "getMe", struct{}{})
	if err != nil {
		return nil, err
	}
	var u BotUser
	if err := json.Unmarshal(raw, &u); err != nil {
		return nil, fmt.Errorf("decode getMe result: %w", err)
	}
	return &u, nil
}

// BotCommand is one entry in the setMyCommands list. Command is the slash
// command without the leading slash; Description is the short hint Telegram
// shows in the command menu.
type BotCommand struct {
	Command     string `json:"command"`
	Description string `json:"description"`
}

// SetMyCommandsParams is the typed payload for setMyCommands.
type SetMyCommandsParams struct {
	Commands []BotCommand `json:"commands"`
}

// SetMyCommands registers the bot's slash-command menu with Telegram. Idempotent
// — calling it again simply replaces the menu.
func (c *APIClient) SetMyCommands(ctx context.Context, p SetMyCommandsParams) error {
	_, err := c.callJSON(ctx, "setMyCommands", p)
	return err
}

// AsTelegramError reports whether err is a *telegramError and, if so, returns
// it. Callers (e.g. botconfig.Connect) use this to distinguish an invalid token
// (HTTP-level 401/4xx from Telegram) from a network failure.
func AsTelegramError(err error) (*telegramError, bool) {
	var te *telegramError
	if errors.As(err, &te) {
		return te, true
	}
	return nil, false
}

// ErrorCode returns the Telegram error_code carried by a *telegramError.
func (e *telegramError) Code() int { return e.ErrorCode }

// IsRateLimited reports whether err is a *rateLimitedError and returns the
// hinted Retry-After. Helper for the adapter's chunked-send loop.
func IsRateLimited(err error) (time.Duration, bool) {
	var rle *rateLimitedError
	if errors.As(err, &rle) {
		return rle.RetryAfter, true
	}
	return 0, false
}
