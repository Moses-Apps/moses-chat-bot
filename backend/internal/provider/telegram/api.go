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

// defaultTimeout is applied to the bundled http.Client when the caller does
// not supply one. The Telegram API is generally fast; 10s is generous enough
// for setWebhook calls but short enough to surface dead networks promptly.
const defaultTimeout = 10 * time.Second

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

// IsRateLimited reports whether err is a *rateLimitedError and returns the
// hinted Retry-After. Helper for the adapter's chunked-send loop.
func IsRateLimited(err error) (time.Duration, bool) {
	var rle *rateLimitedError
	if errors.As(err, &rle) {
		return rle.RetryAfter, true
	}
	return 0, false
}
