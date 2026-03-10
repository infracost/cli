package scanner

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/infracost/config"
	"github.com/infracost/proto/gen/go/infracost/usage"
)

const (
	repoConfigFilename         = "infracost.yml"
	repoConfigTemplateFilename = "infracost.yml.tmpl"
)

// LoadUsageData loads usage data from a usage config file (merging on top of the supplied defaults)
// The defaults are expected to come from the Infracost Cloud Platform
func LoadUsageData(r io.Reader, defaults *usage.Usage) (*usage.Usage, error) {
	return config.LoadUsageYAML(r, defaults)
}

func LoadOrGenerateRepositoryConfig(dir string, opts ...config.GenerationOption) (*config.Config, error) {

	env := make(map[string]string, len(os.Environ()))
	for _, kv := range os.Environ() {
		key, val, _ := strings.Cut(kv, "=")
		env[key] = val
	}

	configPath := filepath.Join(dir, repoConfigFilename)
	if fileExists(configPath) {
		c, err := config.LoadConfigFile(configPath, dir, env)
		if err != nil {
			return nil, fmt.Errorf("failed to load config: %w", err)
		}
		return c, nil
	}

	templatePath := filepath.Join(dir, repoConfigTemplateFilename)
	opts = append(
		opts,
		config.WithEnvVars(env),
		// TODO: add more options later
	)

	// load the template if it exists, but don't fail if it doesn't - we'll just generate a config with defaults
	if fileExists(templatePath) {
		configContent, err := os.ReadFile(templatePath) // #nosec G304 -- this is a file that the user has explicitly added to their repo
		if err != nil {
			return nil, fmt.Errorf("infracost config template exists, but is not readable: %w", err)
		}
		opts = append(opts, config.WithTemplate(string(configContent)))
	}

	c, err := config.Generate(dir, opts...)
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
