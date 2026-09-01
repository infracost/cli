package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/infracost/cli/internal/cache"
	"github.com/infracost/cli/pkg/logging"
)

const (
	// DefaultURL is the stable raw URL the CLI fetches registry.json from when
	// RegistryURLEnv is unset.
	DefaultURL = "https://raw.githubusercontent.com/infracost/plugins-registry/main/registry.json"

	// RegistryURLEnv overrides DefaultURL for testing and air-gapped mirrors,
	// mirroring the INFRACOST_CLI_PLUGIN_BASE_URL convention.
	RegistryURLEnv = "INFRACOST_CLI_PLUGIN_REGISTRY_URL"

	// DefaultTTL is how long a cached manifest is treated as fresh before the
	// client re-fetches.
	DefaultTTL = 24 * time.Hour

	// maxManifestSize caps how many bytes the client will read from a manifest
	// response, so a misbehaving host can't exhaust memory.
	maxManifestSize = 10 << 20 // 10 MB

	// httpTimeout bounds a single manifest fetch.
	httpTimeout = 30 * time.Second
)

// Client fetches the registry manifest over HTTPS and caches it on disk. A
// stale or unreachable registry degrades gracefully: the cached copy is used
// with a warning, and a hard error is returned only when no usable cache
// exists.
type Client struct {
	// URL is where registry.json is fetched from.
	URL string
	// HTTPClient performs the fetch. Defaults to a client with httpTimeout.
	HTTPClient *http.Client
	// CachePath is where the fetched manifest is cached. Empty disables caching.
	CachePath string
	// TTL is how long a cached manifest is fresh. Zero means DefaultTTL.
	TTL time.Duration
	// now returns the current time; overridable in tests.
	now func() time.Time
}

// NewClient returns a Client configured from the environment: the manifest URL
// honours RegistryURLEnv (falling back to DefaultURL) and the manifest is
// cached under the CLI cache root.
func NewClient() *Client {
	url := os.Getenv(RegistryURLEnv)
	if url == "" {
		url = DefaultURL
	}
	return &Client{
		URL:        url,
		HTTPClient: &http.Client{Timeout: httpTimeout},
		CachePath:  cache.PluginRegistryCacheFile(),
		TTL:        DefaultTTL,
		now:        time.Now,
	}
}

func (c *Client) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return &http.Client{Timeout: httpTimeout}
}

func (c *Client) ttl() time.Duration {
	if c.TTL <= 0 {
		return DefaultTTL
	}
	return c.TTL
}

func (c *Client) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// cacheEnvelope wraps the raw manifest bytes with the fetch time and source
// URL. Storing the URL lets the client ignore a cache written for a different
// registry (e.g. when RegistryURLEnv points at a test server).
type cacheEnvelope struct {
	URL       string          `json:"url"`
	FetchedAt time.Time       `json:"fetchedAt"`
	Manifest  json.RawMessage `json:"manifest"`
}

// Load returns the current registry manifest. It serves a fresh cached copy
// when one exists, otherwise fetches over HTTPS and refreshes the cache. When
// the fetch or the fetched manifest fails, it falls back to any parseable
// cached copy with a staleness warning; only when no usable cache exists does
// it return an error.
func (c *Client) Load(ctx context.Context) (*Registry, error) {
	env, haveCache := c.readCache()

	if haveCache && c.clock().Sub(env.FetchedAt) < c.ttl() {
		reg, err := Parse(env.Manifest)
		if err == nil {
			logging.Debugf("using fresh cached plugin registry from %s", c.URL)
			return reg, nil
		}
		logging.Debugf("cached plugin registry failed to parse (%v) — refetching", err)
	}

	data, contentType, fetchErr := c.fetch(ctx)
	if fetchErr != nil {
		if reg, ok := c.fallback(env, haveCache); ok {
			logging.Warnf("using cached plugin registry from %s (fetched %s) — failed to refresh: %v", c.URL, env.FetchedAt.Format(time.RFC3339), fetchErr)
			return reg, nil
		}
		return nil, fmt.Errorf("failed to fetch plugin registry from %s: %w", c.URL, fetchErr)
	}

	reg, err := Parse(data)
	if err != nil {
		if fbReg, ok := c.fallback(env, haveCache); ok {
			logging.Warnf("using cached plugin registry from %s — the latest manifest failed to parse: %v", c.URL, err)
			return fbReg, nil
		}
		return nil, newParseError(c.URL, contentType, data, err)
	}

	c.writeCache(data)
	return reg, nil
}

// fallback returns a parseable cached manifest, if one is available, for use
// when a live fetch or parse fails.
func (c *Client) fallback(env cacheEnvelope, haveCache bool) (*Registry, bool) {
	if !haveCache {
		return nil, false
	}
	reg, err := Parse(env.Manifest)
	if err != nil {
		return nil, false
	}
	return reg, true
}

// fetch performs the HTTPS GET and returns the body along with the response
// Content-Type (used to build a helpful parse-error message).
func (c *Client) fetch(ctx context.Context) (data []byte, contentType string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL, nil) //nolint:gosec // G107: URL is the configured registry URL
	if err != nil {
		return nil, "", fmt.Errorf("failed to create HTTP request: %w", err)
	}

	resp, err := c.httpClient().Do(req) //nolint:gosec // G704: request originates from the configured registry URL
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, resp.Header.Get("Content-Type"), fmt.Errorf("unexpected HTTP status %s", resp.Status)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxManifestSize))
	if err != nil {
		return nil, resp.Header.Get("Content-Type"), fmt.Errorf("failed to read manifest body: %w", err)
	}
	return body, resp.Header.Get("Content-Type"), nil
}

// readCache reads and unmarshals the cached envelope. It returns ok=false when
// no cache is configured, the file is missing/corrupt, or the cache was written
// for a different registry URL.
func (c *Client) readCache() (cacheEnvelope, bool) {
	var env cacheEnvelope
	if c.CachePath == "" {
		return env, false
	}
	data, err := os.ReadFile(filepath.Clean(c.CachePath))
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			logging.Debugf("failed to read plugin registry cache %q: %v", c.CachePath, err)
		}
		return env, false
	}
	if err := json.Unmarshal(data, &env); err != nil {
		logging.Debugf("corrupt plugin registry cache %q: %v", c.CachePath, err)
		return env, false
	}
	if env.URL != c.URL || len(env.Manifest) == 0 {
		return cacheEnvelope{}, false
	}
	return env, true
}

// writeCache atomically stores the fetched manifest. Best-effort: cache write
// failures are logged, never fatal.
func (c *Client) writeCache(manifest []byte) {
	if c.CachePath == "" {
		return
	}

	env := cacheEnvelope{URL: c.URL, FetchedAt: c.clock(), Manifest: json.RawMessage(manifest)}
	data, err := json.Marshal(env)
	if err != nil {
		logging.Debugf("failed to encode plugin registry cache: %v", err)
		return
	}

	if err := os.MkdirAll(filepath.Dir(c.CachePath), 0o750); err != nil {
		logging.Debugf("failed to create plugin registry cache dir: %v", err)
		return
	}

	tmp := c.CachePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		logging.Debugf("failed to write plugin registry cache: %v", err)
		return
	}
	if err := os.Rename(tmp, c.CachePath); err != nil {
		_ = os.Remove(tmp)
		logging.Debugf("failed to install plugin registry cache: %v", err)
	}
}

// newParseError builds a manifest parse error that names the registry URL and
// includes the response content type and a short byte preview — the key hint
// when a URL returns HTML (a proxy login page) instead of JSON.
func newParseError(url, contentType string, body []byte, cause error) error {
	if contentType == "" {
		contentType = "unknown"
	}
	return fmt.Errorf("failed to parse plugin registry from %s: %w (content-type: %s, first bytes: %s)", url, cause, contentType, preview(body))
}

// preview returns a short, printable snippet of a response body for error
// messages: the first 120 runes, control characters stripped, quoted.
func preview(body []byte) string {
	const limit = 120
	s := string(body)
	if !utf8.ValidString(s) {
		return strconv.Quote(fmt.Sprintf("<%d non-UTF-8 bytes>", len(body)))
	}
	trimmed := false
	if len(s) > limit {
		s = s[:limit]
		trimmed = true
	}
	// Collapse newlines/tabs so the snippet stays on one line.
	cleaned := make([]rune, 0, len(s))
	for _, r := range s {
		if r == '\n' || r == '\r' || r == '\t' {
			r = ' '
		}
		cleaned = append(cleaned, r)
	}
	out := strconv.Quote(string(cleaned))
	if trimmed {
		out += "..."
	}
	return out
}
