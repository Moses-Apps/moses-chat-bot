package mosesclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// DefaultTimeout is the per-request timeout for client HTTP calls.
// StreamChatMessage is fire-and-forget — the platform acknowledges it
// within milliseconds and then runs the Moses Manager turn in its own
// background goroutine — so 30s is ample for every call the client makes.
const DefaultTimeout = 30 * time.Second

// Client is the typed wrapper. Construct with NewClient and reuse
// across goroutines — *http.Client is safe for concurrent use, and the
// Auth strategy is read-only per request.
type Client struct {
	baseURL string       // e.g. "http://moses-backend.moses.svc.cluster.local:8080"
	http    *http.Client // shared, concurrency-safe
	auth    Auth         // applied to every outbound request

	// tenantOverride is an optional X-Tenant-ID header set on every
	// request. Per SPEC §3, the API key already carries the tenant so
	// this is usually empty. Set via WithTenantOverride when a caller
	// genuinely needs to pin a different tenant.
	tenantOverride string
}

// NewClient builds a Client. baseURL is the moses-backend root
// (without trailing slash; if a trailing slash is supplied it's
// trimmed). auth may be nil for unauthenticated probes.
func NewClient(baseURL string, auth Auth) *Client {
	if auth == nil {
		auth = noAuth{}
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: DefaultTimeout},
		auth:    auth,
	}
}

// WithHTTPClient overrides the default *http.Client. Useful for tests
// that need a custom transport or for callers that want a shared pool.
func (c *Client) WithHTTPClient(h *http.Client) *Client {
	c.http = h
	return c
}

// WithTenantOverride pins X-Tenant-ID on every outbound request. Per
// SPEC §3 the API key already carries the tenant so this should be
// the exception, not the rule.
func (c *Client) WithTenantOverride(tenantID string) *Client {
	c.tenantOverride = tenantID
	return c
}

// BaseURL returns the configured base URL (with no trailing slash).
func (c *Client) BaseURL() string { return c.baseURL }

// newRequest builds an authenticated *http.Request with JSON body. A
// nil body produces a body-less request (e.g. GETs).
func (c *Client) newRequest(ctx context.Context, method, path string, body interface{}) (*http.Request, error) {
	url := c.baseURL + path
	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("mosesclient: marshal body: %w", err)
		}
		bodyReader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("mosesclient: build request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	c.auth.Apply(req)
	if c.tenantOverride != "" {
		req.Header.Set("X-Tenant-ID", c.tenantOverride)
	}
	return req, nil
}

// do executes the request and returns the response. On non-2xx status
// the body is consumed and a typed *APIError is returned (matching
// classifyHTTPStatus). On 2xx the caller is responsible for closing
// resp.Body.
func (c *Client) do(req *http.Request) (*http.Response, error) {
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("mosesclient: %s %s: %w", req.Method, req.URL.Path, err)
	}
	if sentinel := classifyHTTPStatus(resp.StatusCode); sentinel != nil {
		defer resp.Body.Close()
		return nil, c.parseError(resp, sentinel)
	}
	return resp, nil
}

// doJSON executes the request and decodes a 2xx JSON response into out
// (which may be nil to discard).
func (c *Client) doJSON(req *http.Request, out interface{}) error {
	resp, err := c.do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil && err != io.EOF {
		return fmt.Errorf("mosesclient: decode %s: %w", req.URL.Path, err)
	}
	return nil
}

// parseError reads a non-2xx response body and constructs an
// *APIError. moses-backend's error envelopes are typically
// {error: "...", code: "..."} — we parse loosely so any 4xx/5xx
// response (even plain text) yields a useful error.
func (c *Client) parseError(resp *http.Response, sentinel error) error {
	apiErr := &APIError{
		Status:   resp.StatusCode,
		sentinel: sentinel,
	}
	// Parse Retry-After for 429.
	if resp.StatusCode == http.StatusTooManyRequests {
		if ra := resp.Header.Get("Retry-After"); ra != "" {
			if secs, err := strconv.Atoi(ra); err == nil {
				apiErr.RetryAfter = secs
			}
		}
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if len(body) == 0 {
		return apiErr
	}
	// Try the structured envelope first.
	var env struct {
		Error   string `json:"error"`
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &env) == nil {
		apiErr.Code = env.Code
		if env.Error != "" {
			apiErr.Message = env.Error
		} else if env.Message != "" {
			apiErr.Message = env.Message
		}
		if apiErr.Message != "" {
			return apiErr
		}
	}
	// Fallback to raw body (clipped).
	apiErr.Message = strings.TrimSpace(string(body))
	if len(apiErr.Message) > 512 {
		apiErr.Message = apiErr.Message[:512]
	}
	return apiErr
}
