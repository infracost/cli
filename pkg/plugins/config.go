package plugins

import (
	"context"
	"sync"

	"github.com/infracost/cli/pkg/config/process"
)

var _ process.Processor = (*Config)(nil)

// Config holds the user-facing knobs for plugin loading. The actual lifecycle
// (install, discovery, connect, close) is owned by Manager.
type Config struct {
	// BaseURL points to the root URL where plugin archives are hosted.
	BaseURL string `env:"INFRACOST_CLI_PLUGIN_BASE_URL" default:"https://releases.infracost.io"`

	// Cache is where managed (required) plugins are downloaded to.
	Cache string `env:"INFRACOST_CLI_PLUGIN_CACHE_DIRECTORY"`

	// Dir is a flat plugin directory override for local plugin development.
	// When set, downloads are skipped and only plugins already present in
	// the directory are loaded.
	Dir string `env:"INFRACOST_CLI_PLUGIN_DIR"`

	// AutoUpdate controls whether required plugins are updated to the latest
	// version. When false, an existing flat-installed binary is used if
	// available.
	AutoUpdate bool `env:"INFRACOST_CLI_PLUGIN_AUTO_UPDATE" default:"true"`

	managerMu  sync.Mutex
	ensureOnce sync.Once
	ensureErr  error
	manager    *Manager

	// LoadParserPluginForProject lets tests inject a parser plugin without
	// running a real subprocess. When non-nil, ParserPluginForProject calls
	// this instead of the Manager.
	LoadParserPluginForProject func(context.Context, string) (*ParserPlugin, error)

	// LoadProviderPlugins lets tests inject mock provider plugins without
	// running real subprocesses. When non-nil, ProviderPlugins calls this
	// instead of the Manager.
	LoadProviderPlugins func(context.Context) ([]*ProviderPlugin, error)

	// newStager lets tests inject a registryStager with stubbed probing seams
	// so the update/install orchestration can be driven without spawning real
	// plugin subprocesses. When non-nil, newRegistryStager returns it instead
	// of the default. Production code never sets it.
	newStager func() *registryStager
}

func (c *Config) Process() {
	if len(c.Cache) == 0 {
		c.Cache = defaultPluginCachePath()
	}
}

// PluginDir returns the directory the manager loads plugins from.
func (c *Config) PluginDir() string {
	if c.Dir != "" {
		return c.Dir
	}
	if c.Cache == "" {
		return defaultPluginCachePath()
	}
	return c.Cache
}

// pluginStateDir returns the directory the provenance state file lives in. It
// is always the plugin cache directory (honoring
// INFRACOST_CLI_PLUGIN_CACHE_DIRECTORY) — never the INFRACOST_CLI_PLUGIN_DIR
// dev-override directory, which the install/uninstall/update flows refuse to
// manage anyway.
func (c *Config) pluginStateDir() string {
	if c.Cache != "" {
		return c.Cache
	}
	return defaultPluginCachePath()
}

// LoadState reads the provenance state recording how registry entries were
// installed. Reads are tolerant: a missing or corrupt file yields an empty
// state (see loadState).
func (c *Config) LoadState() *State {
	return loadState(c.pluginStateDir())
}

// SaveState atomically writes the provenance state to the plugin cache
// directory.
func (c *Config) SaveState(s *State) error {
	return s.save(c.pluginStateDir())
}

// EnsurePlugins installs any required plugins (when AutoUpdate is enabled and
// Dir is not set as a developer override), then returns the Manager. The
// install runs once and is memoized, so only the first caller's context bounds
// the downloads.
func (c *Config) EnsurePlugins(ctx context.Context) (*Manager, error) {
	c.managerMu.Lock()
	defer c.managerMu.Unlock()

	c.ensureOnce.Do(func() {
		c.manager = NewManager(ManagerOptions{
			Dir:         c.PluginDir(),
			Cache:       c.Cache,
			BaseURL:     c.BaseURL,
			AutoUpdate:  c.AutoUpdate,
			SkipInstall: c.Dir != "",
		})
		c.ensureErr = c.manager.EnsureInstalled(ctx)
	})
	return c.manager, c.ensureErr
}

// ParserPluginForProject returns the parser plugin that handles the given
// project type. Test injectors short-circuit Manager loading.
func (c *Config) ParserPluginForProject(ctx context.Context, projectTypeOrPluginName string) (*ParserPlugin, error) {
	if c.LoadParserPluginForProject != nil {
		return c.LoadParserPluginForProject(ctx, projectTypeOrPluginName)
	}
	manager, err := c.EnsurePlugins(ctx)
	if err != nil {
		return nil, err
	}
	return manager.LoadParserPluginForProject(ctx, projectTypeOrPluginName)
}

// ProviderPlugins returns every loaded provider plugin.
func (c *Config) ProviderPlugins(ctx context.Context) ([]*ProviderPlugin, error) {
	if c.LoadProviderPlugins != nil {
		return c.LoadProviderPlugins(ctx)
	}

	manager, err := c.EnsurePlugins(ctx)
	if err != nil {
		return nil, err
	}
	return manager.LoadProviderPlugins(ctx)
}

// Close releases all plugin subprocess resources.
func (c *Config) Close() {
	c.managerMu.Lock()
	manager := c.manager
	c.managerMu.Unlock()

	if manager != nil {
		manager.Close()
	}
}
