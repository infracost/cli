package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/google/go-github/v83/github"
	"github.com/infracost/cli/pkg/config/process"
	"github.com/infracost/cli/pkg/plugins"
)

type Config struct {
	Target string `env:"UPDATE_MANIFEST_PATH" default:"docs/manifest.json"`

	Plugin  string `env:"UPDATE_MANIFEST_PLUGIN"`
	Version string `env:"UPDATE_MANIFEST_VERSION"`

	GithubToken string `env:"GITHUB_TOKEN"`

	Latest bool `env:"UPDATE_MANIFEST_LATEST" default:"true"`
}

func main() {
	var cfg Config
	if diags := process.PreProcess(&cfg, nil); diags.Critical().Len() > 0 {
		_, _ = fmt.Fprintln(os.Stderr, diags)
		os.Exit(1)
	}
	process.Process(&cfg)

	switch {
	case cfg.Plugin == "":
		_, _ = fmt.Fprintln(os.Stderr, "Plugin must be specified")
		os.Exit(1)
	case cfg.Version == "":
		_, _ = fmt.Fprintln(os.Stderr, "Version must be specified")
		os.Exit(1)
	case cfg.GithubToken == "":
		_, _ = fmt.Fprintln(os.Stderr, "Github token must be specified")
		os.Exit(1)
	}

	_, _ = fmt.Fprintf(os.Stderr, "Updating %s to %s at %s.\n", cfg.Plugin, cfg.Version, cfg.Target)

	var manifest plugins.Manifest
	if data, err := os.ReadFile(cfg.Target); err != nil {
		if !os.IsNotExist(err) {
			_, _ = fmt.Fprintf(os.Stderr, "Error reading manifest: %v\n", err)
			os.Exit(1)
		}
	} else {
		if err := json.Unmarshal(data, &manifest); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "Error parsing manifest: %v\n", err)
			os.Exit(1)
		}
	}

	version := strings.TrimPrefix(cfg.Version, "v")   // 0.1.0
	tag := fmt.Sprintf("%s/v%s", cfg.Plugin, version) // infracost-parser-plugin/v0.1.0

	client := github.NewClient(nil).WithAuthToken(cfg.GithubToken)
	release, _, err := client.Repositories.GetReleaseByTag(context.Background(), "infracost", "cli", tag)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "Error fetching release: %v\n", err)
		os.Exit(1)
	}

	if manifest.Plugins == nil {
		manifest.Plugins = make(map[string]plugins.Plugin)
	}

	plugin, ok := manifest.Plugins[cfg.Plugin]
	if !ok {
		plugin = plugins.Plugin{
			Latest:   version, // if we're making a completely fresh one, then it's definitely latest.
			Versions: make(map[string]plugins.Version),
		}
	}

	re := regexp.MustCompile(fmt.Sprintf(`^%s-([^-]+)-([^.]+)\.`, regexp.QuoteMeta(cfg.Plugin)))

	v := plugins.Version{
		Artifacts: make(map[string]plugins.Artifact),
	}
	for _, asset := range release.Assets {
		name := asset.GetName()
		if strings.HasSuffix(name, ".sha256") {
			continue
		}
		matches := re.FindStringSubmatch(name)
		if matches == nil {
			continue
		}
		// Map keys stay <goos>_<goarch> (underscore) since the runtime looks up
		// platforms as runtime.GOOS + "_" + runtime.GOARCH.
		osArch := fmt.Sprintf("%s_%s", matches[1], matches[2])

		a := plugins.Artifact{
			URL:  fmt.Sprintf("https://infracost.io/downloads/%s/%s", tag, name),
			SHA:  strings.TrimPrefix(asset.GetDigest(), "sha256:"),
			Name: name,
		}
		v.Artifacts[osArch] = a
		_, _ = fmt.Fprintf(os.Stderr, "  %s: url=%s sha=%s\n", osArch, a.URL, a.SHA)
	}
	plugin.Versions[version] = v

	if cfg.Latest {
		plugin.Latest = version
	}

	manifest.Plugins[cfg.Plugin] = plugin

	data, err := json.Marshal(manifest)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "Error serializing manifest: %v\n", err)
		os.Exit(1)
	}

	if err := os.MkdirAll(filepath.Dir(cfg.Target), 0750); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "Error creating directory: %v\n", err)
		os.Exit(1)
	}

	if err := os.WriteFile(cfg.Target, data, 0600); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "Error writing manifest: %v\n", err)
		os.Exit(1)
	}
}
