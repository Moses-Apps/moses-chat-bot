package mosesclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBearerAuthHeader confirms BearerAuth populates Authorization.
func TestBearerAuthHeader(t *testing.T) {
	var gotAuth, gotXKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotXKey = r.Header.Get("X-API-Key")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, BearerAuth{Token: "tok-abc"})
	req, err := c.newRequest(context.Background(), http.MethodGet, "/probe", nil)
	require.NoError(t, err)
	_, err = c.do(req)
	require.NoError(t, err)

	assert.Equal(t, "Bearer tok-abc", gotAuth)
	assert.Empty(t, gotXKey, "BearerAuth must not set X-API-Key")
}

// TestAPIKeyHeader confirms APIKeyHeader populates X-API-Key.
func TestAPIKeyHeader(t *testing.T) {
	var gotAuth, gotXKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotXKey = r.Header.Get("X-API-Key")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, APIKeyHeader{Token: "tok-xyz"})
	req, err := c.newRequest(context.Background(), http.MethodGet, "/probe", nil)
	require.NoError(t, err)
	_, err = c.do(req)
	require.NoError(t, err)

	assert.Equal(t, "tok-xyz", gotXKey)
	assert.Empty(t, gotAuth, "APIKeyHeader must not set Authorization")
}

// TestTenantOverrideHeader confirms WithTenantOverride sets X-Tenant-ID.
func TestTenantOverrideHeader(t *testing.T) {
	var gotTenant string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTenant = r.Header.Get("X-Tenant-ID")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, BearerAuth{Token: "t"}).WithTenantOverride("tenant-uuid")
	req, err := c.newRequest(context.Background(), http.MethodGet, "/probe", nil)
	require.NoError(t, err)
	_, err = c.do(req)
	require.NoError(t, err)
	assert.Equal(t, "tenant-uuid", gotTenant)
}

// TestNoAuth confirms a nil Auth produces no auth headers.
func TestNoAuth(t *testing.T) {
	var gotAuth, gotXKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotXKey = r.Header.Get("X-API-Key")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, nil)
	req, err := c.newRequest(context.Background(), http.MethodGet, "/probe", nil)
	require.NoError(t, err)
	_, err = c.do(req)
	require.NoError(t, err)
	assert.Empty(t, gotAuth)
	assert.Empty(t, gotXKey)
}

// TestBaseURLTrimsTrailingSlash confirms trailing-slash normalization.
func TestBaseURLTrimsTrailingSlash(t *testing.T) {
	c := NewClient("http://x/", nil)
	assert.Equal(t, "http://x", c.BaseURL())
}
