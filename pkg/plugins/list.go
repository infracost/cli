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
	// from GetPluginInfo; otherwise from the required set.
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

	// Version is the cached plugin version, or empty if unknown.
	Version string
}

// List returns every plugin the CLI knows about: each entry in the required
// set (whether installed or not) plus any extra plugins found in the plugin
// directory.
func (c *Config) List() []ListItem {
	dir := c.PluginDir()

	items := make([]ListItem, 0, len(requiredPlugins))
	seen := make(map[string]struct{}, len(requiredPlugins))

	for _, required := range requiredPlugins {
		binary := pluginBinaryName(required.Name)
		path := filepath.Join(dir, binary)
		seen[binary] = struct{}{}

		installed := flatPluginBinaryExists(path)
		version := cachedPluginVersion(path)
		if installed && version == "" {
			version = "unknown"
		}

		items = append(items, ListItem{
			Key:       required.Key,
			Name:      required.Name,
			Type:      required.Type,
			Path:      path,
			Installed: installed,
			Required:  true,
			Version:   version,
		})
	}

	// Add any extra plugins found in the directory. These aren't in the
	// required set, so we don't know their type until we ask them; defer
	// type discovery to a Manager call below if we have any extras.
	entries, err := os.ReadDir(dir)
	if err != nil {
		return items
	}

	type extra struct {
		path string
		name string
	}
	var extras []extra
	for _, entry := range entries {
		if entry.IsDir() || isPluginSidecar(entry.Name()) {
			continue
		}
		if _, ok := seen[entry.Name()]; ok {
			continue
		}
		extras = append(extras, extra{path: filepath.Join(dir, entry.Name()), name: entry.Name()})
	}

	if len(extras) == 0 {
		return items
	}

	// Connect to extras to read their reported type and name. Any that fail
	// to connect are still listed with an "unknown" type so users can see
	// they're present but broken.
	mgr := NewManager(ManagerOptions{Dir: dir, SkipInstall: true})
	defer mgr.Close()

	parsers, _ := mgr.LoadParserPlugins(context.Background())
	providers, _ := mgr.LoadProviderPlugins(context.Background())

	infoByName := make(map[string]struct {
		typ     string
		name    string
		version string
	}, len(parsers)+len(providers))
	for _, p := range parsers {
		key := pluginBinaryName(p.Info.GetName())
		infoByName[key] = struct {
			typ     string
			name    string
			version string
		}{typ: pluginTypeParser, name: p.Info.GetName(), version: p.Info.GetVersion()}
	}
	for _, p := range providers {
		key := pluginBinaryName(p.Info.GetName())
		infoByName[key] = struct {
			typ     string
			name    string
			version string
		}{typ: pluginTypeProvider, name: p.Info.GetName(), version: p.Info.GetVersion()}
	}

	for _, e := range extras {
		item := ListItem{
			Key:       e.name,
			Name:      e.name,
			Path:      e.path,
			Installed: flatPluginBinaryExists(e.path),
			Required:  false,
			Version:   cachedPluginVersion(e.path),
		}
		if info, ok := infoByName[e.name]; ok {
			item.Type = info.typ
			item.Name = info.name
			if v := info.version; v != "" {
				item.Version = v
			}
		}
		if item.Installed && item.Version == "" {
			item.Version = "unknown"
		}
		items = append(items, item)
	}

	return items
}
