package scanner

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	repoconfig "github.com/infracost/config"
	"github.com/infracost/proto/gen/go/infracost/usage"
)

const (
	RepoConfigFilename         = "infracost.yml"
	RepoConfigTemplateFilename = "infracost.yml.tmpl"
)

// LoadUsageData loads usage data from a usage config file (merging on top of the supplied defaults).
// The defaults are expected to come from the Infracost Cloud Platform.
func LoadUsageData(r io.Reader, defaults *usage.Usage) (*usage.Usage, error) {
	return repoconfig.LoadUsageYAML(r, defaults)
}

// LoadOrGenerateRepositoryConfig loads an infracost.yml config from the given directory,
// or generates one if it doesn't exist. If an infracost.yml.tmpl template exists, it is
// used as the basis for generation.
func LoadOrGenerateRepositoryConfig(dir string, opts ...repoconfig.GenerationOption) (*repoconfig.Config, error) {
	env := make(map[string]string, len(os.Environ()))
	for _, kv := range os.Environ() {
		key, val, _ := strings.Cut(kv, "=")
		env[key] = val
	}

	configPath := filepath.Join(dir, RepoConfigFilename)
	if fileExists(configPath) {
		c, err := repoconfig.LoadConfigFile(configPath, dir, env)
		if err != nil {
			return nil, fmt.Errorf("failed to load config: %w", err)
		}
		return c, nil
	}

	opts = append(
		opts,
		repoconfig.WithEnvVars(env),
	)

	templatePath := filepath.Join(dir, RepoConfigTemplateFilename)
	if fileExists(templatePath) {
		content, err := os.ReadFile(templatePath) // #nosec G304 -- user-specified template in their repo
		if err != nil {
			return nil, fmt.Errorf("infracost config template exists, but is not readable: %w", err)
		}
		opts = append(opts, repoconfig.WithTemplate(string(content)))
	}

	c, err := repoconfig.Generate(dir, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to generate config: %w", err)
	}
	return c, nil
}

func fileExists(path string) bool {
	stat, err := os.Stat(path)
	if err != nil {
		return false
	}
	return !stat.IsDir()
}