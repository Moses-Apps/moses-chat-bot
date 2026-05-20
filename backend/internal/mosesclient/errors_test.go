package mosesclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHTTPStatusErrorMapping verifies each HTTP status maps to the
// right sentinel and that *APIError carries through the parsed code
// and message.
func TestHTTPStatusErrorMapping(t *testing.T) {
	cases := []struct {
		name     string
		status   int
		body     string
		sentinel error
	}{
		{"401 → ErrUnauthorized", http.StatusUnauthorized, `{"error":"bad key","code":"invalid_api_key"}`, ErrUnauthorized},
		{"403 → ErrForbidden", http.StatusForbidden, `{"error":"no permission","code":"forbidden"}`, ErrForbidden},
		{"404 → ErrNotFound", http.StatusNotFound, `{"error":"missing"}`, ErrNotFound},
		{"429 → ErrRateLimited", http.StatusTooManyRequests, `{"error":"slow down","code":"rate_limited"}`, ErrRateLimited},
		{"500 → ErrServerError", http.StatusInternalServerError, `{"error":"oops"}`, ErrServerError},
		{"503 → ErrServerError", http.StatusServiceUnavailable, `{"error":"down"}`, ErrServerError},
		{"400 → ErrServerError (generic 4xx)", http.StatusBadRequest, `{"error":"bad request"}`, ErrServerError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			c := NewClient(srv.URL, BearerAuth{Token: "t"})
			req, err := c.newRequest(context.Background(), http.MethodGet, "/probe", nil)
			require.NoError(t, err)
			_, err = c.do(req)
			require.Error(t, err)

			assert.True(t, errors.Is(err, tc.sentinel),
				"expected sentinel %v, got %v", tc.sentinel, err)

			var apiErr *APIError
			require.True(t, errors.As(err, &apiErr), "expected *APIError")
			assert.Equal(t, tc.status, apiErr.Status)
			assert.Contains(t, apiErr.Error(), apiErr.Message)
		})
	}
}

// Test429ParsesRetryAfter confirms the Retry-After header surfaces
// on the APIError.
func Test429ParsesRetryAfter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "42")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"throttled"}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, BearerAuth{Token: "t"})
	req, err := c.newRequest(context.Background(), http.MethodGet, "/probe", nil)
	require.NoError(t, err)
	_, err = c.do(req)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrRateLimited))
	var apiErr *APIError
	require.True(t, errors.As(err, &apiErr))
	assert.Equal(t, 42, apiErr.RetryAfter)
}

// TestPlainTextErrorBody falls back to the raw body when JSON parse fails.
func TestPlainTextErrorBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("nginx 500: upstream timed out"))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, BearerAuth{Token: "t"})
	req, err := c.newRequest(context.Background(), http.MethodGet, "/probe", nil)
	require.NoError(t, err)
	_, err = c.do(req)
	require.Error(t, err)
	var apiErr *APIError
	require.True(t, errors.As(err, &apiErr))
	assert.Contains(t, apiErr.Message, "nginx 500")
}
