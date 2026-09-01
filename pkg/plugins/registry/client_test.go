package registry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestClient returns a Client pointing at url with a fresh temp cache file
// and a controllable clock.
func newTestClient(t *testing.T, url string) (*Client, *atomic.Int64) {
	t.Helper()
	clockNanos := &atomic.Int64{}
	clockNanos.Store(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).UnixNano())
	return &Client{
		URL:        url,
		HTTPClient: http.DefaultClient,
		CachePath:  filepath.Join(t.TempDir(), "plugin-registry.json"),
		TTL:        DefaultTTL,
		now:        func() time.Time { return time.Unix(0, clockNanos.Load()).UTC() },
	}, clockNanos
}

func TestNewClientHonoursEnvOverride(t *testing.T) {
	t.Setenv(RegistryURLEnv, "https://example.test/registry.json")
	c := NewClient()
	assert.Equal(t, "https://example.test/registry.json", c.URL)

	t.Setenv(RegistryURLEnv, "")
	c = NewClient()
	assert.Equal(t, DefaultURL, c.URL)
}

func TestLoadFetchAndCache(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(validManifest))
	}))
	defer srv.Close()

	c, _ := newTestClient(t, srv.URL)

	reg, err := c.Load(context.Background())
	require.NoError(t, err)
	require.Len(t, reg.Plugins, 1)
	assert.Equal(t, int64(1), hits.Load())

	// Second load within TTL is served from cache — no new fetch.
	reg, err = c.Load(context.Background())
	require.NoError(t, err)
	require.Len(t, reg.Plugins, 1)
	assert.Equal(t, int64(1), hits.Load(), "expected the cached manifest to be reused")
}

func TestLoadRefetchesAfterTTL(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(validManifest))
	}))
	defer srv.Close()

	c, clock := newTestClient(t, srv.URL)

	_, err := c.Load(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(1), hits.Load())

	// Advance past the TTL — the next load refetches.
	clock.Store(time.Unix(0, clock.Load()).Add(DefaultTTL + time.Hour).UnixNano())
	_, err = c.Load(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(2), hits.Load())
}

func TestLoadStaleFallbackWhenFetchFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(validManifest))
	}))

	c, clock := newTestClient(t, srv.URL)

	// Prime the cache.
	_, err := c.Load(context.Background())
	require.NoError(t, err)

	// Server goes away and the cache is now stale.
	srv.Close()
	clock.Store(time.Unix(0, clock.Load()).Add(DefaultTTL + time.Hour).UnixNano())

	reg, err := c.Load(context.Background())
	require.NoError(t, err, "should fall back to the stale cache")
	require.Len(t, reg.Plugins, 1)
}

func TestLoadHardErrorWhenNoCache(t *testing.T) {
	// Point at a closed server so the fetch fails, with an empty cache dir.
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	url := srv.URL
	srv.Close()

	c, _ := newTestClient(t, url)
	_, err := c.Load(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to fetch plugin registry from")
	assert.Contains(t, err.Error(), url)
}

func TestLoadParseErrorNamesURLAndContentType(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<!DOCTYPE html><html><body>Login required</body></html>"))
	}))
	defer srv.Close()

	c, _ := newTestClient(t, srv.URL)
	_, err := c.Load(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse plugin registry from")
	assert.Contains(t, err.Error(), srv.URL)
	assert.Contains(t, err.Error(), "text/html")
	assert.Contains(t, err.Error(), "DOCTYPE")
}

func TestLoadIgnoresCacheForDifferentURL(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(validManifest))
	}))
	defer srv.Close()

	c, _ := newTestClient(t, srv.URL)
	_, err := c.Load(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(1), hits.Load())

	// Changing the URL invalidates the cache written for the old URL.
	c.URL = srv.URL + "/other"
	// The handler ignores the path and still returns the manifest, so the load
	// succeeds — but it must have refetched rather than trusting the old cache.
	_, err = c.Load(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(2), hits.Load())
}
