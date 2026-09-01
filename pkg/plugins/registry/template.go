package registry

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/infracost/cli/pkg/logging"
)

// versionResponseLimit caps how much of a versionUrl response is read. It
// mirrors the 1KB limit fetchPluginVersion uses in pkg/plugins/install.go.
const versionResponseLimit = 1024

// expandURL substitutes the {version}, {os}, and {arch} placeholders in a
// download/checksums/version URL template.
func expandURL(tmpl, version, goos, goarch string) string {
	return strings.NewReplacer(
		"{version}", version,
		"{os}", goos,
		"{arch}", goarch,
	).Replace(tmpl)
}

// DownloadURL resolves the component's archive URL for a concrete version and
// platform.
func (c Component) DownloadURL(version, goos, goarch string) string {
	return expandURL(c.Download, version, goos, goarch)
}

// ChecksumsURL resolves the component's SHA256 sidecar URL for a concrete
// version and platform.
func (c Component) ChecksumsURL(version, goos, goarch string) string {
	return expandURL(c.Checksums, version, goos, goarch)
}

// versionURLFor resolves the entry's versionUrl template for the given
// platform, with {version} set to "latest" (the endpoint reports the latest
// shared release version for the whole repository).
func (e *Entry) versionURLFor(goos, goarch string) string {
	return expandURL(e.VersionURL, "latest", goos, goarch)
}

// ResolveVersion fetches the entry's latest shared release version from its
// versionUrl. Every component shares this version — the manifest cannot define
// independent per-component versions. Parsing mirrors the per-binary version
// endpoint convention in pkg/plugins/install.go (the first whitespace-delimited
// field of the first 1KB of the response).
func (e *Entry) ResolveVersion(ctx context.Context, client *http.Client, goos, goarch string) (string, error) {
	if e.VersionURL == "" {
		return "", fmt.Errorf("plugin %q has no versionUrl to resolve its latest version", e.Name)
	}

	url := e.versionURLFor(goos, goarch)
	logging.Debugf("resolving plugin %q version from %s", e.Name, url)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil) //nolint:gosec // G107: URL comes from the trusted registry manifest
	if err != nil {
		return "", fmt.Errorf("failed to create HTTP request: %w", err)
	}

	resp, err := client.Do(req) //nolint:gosec // G704: request originates from the registry manifest
	if err != nil {
		return "", fmt.Errorf("failed to resolve plugin %q version: %w", e.Name, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("failed to resolve plugin %q version from %s: unexpected HTTP status %s", e.Name, url, resp.Status)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, versionResponseLimit))
	if err != nil {
		return "", fmt.Errorf("failed to read plugin %q version response: %w", e.Name, err)
	}

	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return "", fmt.Errorf("empty version response for plugin %q from %s", e.Name, url)
	}
	return fields[0], nil
}
