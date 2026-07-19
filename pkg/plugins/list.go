package plugins

import (
	"context"
	"os"
	"path/filepath"
)

// ListItem describes a plugin known to the CLI — either because it's in the
// required set, or because it was discovered in the plugin directory.
type ListItem struct {
	// Key is a short identifier for the plugin (e.g. "terraform", "aws").
	// For required plugins this comes from the required set; for discovered
	// plugins it falls back to the binary filename.
	Key string

	// Name is the plugin's display name. For installed plugins this comes
	// from GetPluginInfo; otherwise it falls back to the binary filename.
	Name string

	// Type is "parser" or "provider" (or empty for required-but-uninstalled
	// when the type is unknown).
	Type string

	// Path is the on-disk location the plugin would be loaded from.
	Path string

	// Installed is true when a binary exists at Path.
	Installed bool

	// Required is true when the plugin is in the CLI's auto-managed set
	// (auto-installed and auto-updated). False for third-party plugins a
	// user has dropped into the plugin directory.
	Required bool

	// Version is the version reported by the plugin via GetPluginInfo, or
	// "unknown" when the plugin is installed but can't be queried.
	Version string
}

// List returns every plugin the CLI knows about: each entry in the required
// set (whether installed or not) plus any extra plugins found in the plugin
// directory.
func (c *Config) List() []ListItem {
	dir := c.PluginDir()

	type info struct {
		typ     string
		name    string
		version string
	}
	infoByPath := make(map[string]info)

	mgr := NewManager(ManagerOptions{Dir: dir, SkipInstall: true})
	defer mgr.Close()

	if parsers, err := mgr.LoadParserPlugins(context.Background()); err == nil {
		for _, p := range parsers {
			infoByPath[p.Path] = info{typ: pluginTypeParser, name: p.Info.GetName(), version: p.Info.GetVersion()}
		}
	}
	if providers, err := mgr.LoadProviderPlugins(context.Background()); err == nil {
		for _, p := range providers {
			infoByPath[p.Path] = info{typ: pluginTypeProvider, name: p.Info.GetName(), version: p.Info.GetVersion()}
		}
	}

	items := make([]ListItem, 0, len(requiredPlugins))
	seen := make(map[string]struct{}, len(requiredPlugins))

	for _, required := range requiredPlugins {
		binary := pluginBinaryName(required.Name)
		path := filepath.Join(dir, binary)
		seen[binary] = struct{}{}

		installed := flatPluginBinaryExists(path)
		item := ListItem{
			Key:       required.Key,
			Name:      required.DisplayName,
			Type:      required.Type,
			Path:      path,
			Installed: installed,
			Required:  true,
		}
		if i, ok := infoByPath[path]; ok {
			if i.name != "" {
				item.Name = i.name
			}
			item.Version = i.version
		}
		if installed && item.Version == "" {
			item.Version = "unknown"
		}
		items = append(items, item)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return items
	}

	for _, entry := range entries {
		if entry.IsDir() || isPluginSidecar(entry.Name()) {
			continue
		}
		if _, ok := seen[entry.Name()]; ok {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		item := ListItem{
			Key:       entry.Name(),
			Name:      entry.Name(),
			Path:      path,
			Installed: flatPluginBinaryExists(path),
			Required:  false,
		}
		if i, ok := infoByPath[path]; ok {
			item.Type = i.typ
			if i.name != "" {
				item.Name = i.name
			}
			item.Version = i.version
		}
		if item.Installed && item.Version == "" {
			item.Version = "unknown"
		}
		items = append(items, item)
	}

	return items
}
