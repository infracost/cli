package plugins

import (
	"context"
	"os"
	"path/filepath"
)

// Plugin source classifications, reported in ListItem.Source.
const (
	// SourceRequired is the compiled-in, auto-managed required set.
	SourceRequired = "required"
	// SourceRegistry is a plugin installed via `plugin install` (has a
	// provenance record in the state file).
	SourceRegistry = "registry"
	// SourceUnmanaged is a binary hand-copied into the plugin directory with
	// no provenance record and not part of the required set.
	SourceUnmanaged = "unmanaged"
)

// ListItem describes a plugin known to the CLI — either because it's in the
// required set, or because it was discovered in the plugin directory.
type ListItem struct {
	// Key is a short identifier for the plugin (e.g. "terraform", "aws").
	// For required plugins this comes from the required set; for discovered
	// plugins it falls back to the binary filename.
	Key string `json:"key"`

	// Name is the plugin's display name. For installed plugins this comes
	// from GetPluginInfo; otherwise it falls back to the binary filename.
	Name string `json:"name"`

	// Type is "parser" or "provider" (or empty for required-but-uninstalled
	// when the type is unknown).
	Type string `json:"type"`

	// Path is the on-disk location the plugin would be loaded from.
	Path string `json:"path"`

	// Installed is true when a binary exists at Path.
	Installed bool `json:"installed"`

	// Required is true when the plugin is in the CLI's auto-managed set
	// (auto-installed and auto-updated). False for third-party plugins a
	// user has dropped into the plugin directory.
	Required bool `json:"required"`

	// Version is the version reported by the plugin via GetPluginInfo, or
	// "unknown" when the plugin is installed but can't be queried.
	Version string `json:"version"`

	// Source records where the plugin came from: SourceRequired,
	// SourceRegistry, or SourceUnmanaged. Populated from the provenance state
	// file, with the filesystem winning: a state record for a binary that is
	// not on disk shows as not installed.
	Source string `json:"source"`

	// Official records the registry entry's official flag. Only meaningful
	// when Source is SourceRegistry.
	Official bool `json:"official"`

	// Pinned is true when the entry was installed with an explicit @<version>.
	// Only meaningful when Source is SourceRegistry.
	Pinned bool `json:"pinned"`

	// Author records the registry entry's author. Only meaningful when Source
	// is SourceRegistry.
	Author string `json:"author"`
}

// List returns every plugin the CLI knows about: each entry in the required
// set (whether installed or not) plus any extra plugins found in the plugin
// directory.
func (c *Config) List() []ListItem {
	dir := c.PluginDir()
	state := c.LoadState()

	// Index provenance by the on-disk binary filename (with any .exe suffix)
	// so it lines up with what discovery sees. Each registry component maps to
	// its owning record plus its own type.
	type prov struct {
		rec  *StateRecord
		comp StateComponent
	}
	provByBinary := make(map[string]prov, len(state.Records))
	for i := range state.Records {
		rec := &state.Records[i]
		for _, comp := range rec.Components {
			provByBinary[pluginBinaryName(comp.BinaryName)] = prov{rec: rec, comp: comp}
		}
	}

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
			Source:    SourceRequired,
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

	// Binaries discovered in the plugin directory. Registry installs and
	// hand-copied binaries both land here; provenance tells them apart.
	if entries, err := os.ReadDir(dir); err == nil {
		for _, entry := range entries {
			if entry.IsDir() || isPluginSidecar(entry.Name()) {
				continue
			}
			if _, ok := seen[entry.Name()]; ok {
				continue
			}
			seen[entry.Name()] = struct{}{}

			path := filepath.Join(dir, entry.Name())
			item := ListItem{
				Key:       entry.Name(),
				Name:      entry.Name(),
				Path:      path,
				Installed: flatPluginBinaryExists(path),
				Required:  false,
				Source:    SourceUnmanaged,
			}
			if p, ok := provByBinary[entry.Name()]; ok {
				// Registry install: seed type/version/metadata from provenance,
				// then let a live handshake below override the specifics.
				item.Source = SourceRegistry
				item.Type = p.comp.Type
				item.Name = p.rec.Name
				item.Version = p.rec.Version
				item.Official = p.rec.Official
				item.Pinned = p.rec.Pinned
				item.Author = p.rec.Author
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
	}

	// Provenance records whose binaries are no longer on disk. The filesystem
	// wins over provenance, so these surface as not-installed rows rather than
	// being trusted as installed.
	for i := range state.Records {
		rec := &state.Records[i]
		for _, comp := range rec.Components {
			binary := pluginBinaryName(comp.BinaryName)
			if _, ok := seen[binary]; ok {
				continue
			}
			seen[binary] = struct{}{}
			items = append(items, ListItem{
				Key:       comp.BinaryName,
				Name:      rec.Name,
				Type:      comp.Type,
				Path:      filepath.Join(dir, binary),
				Installed: false,
				Required:  false,
				Source:    SourceRegistry,
				Official:  rec.Official,
				Pinned:    rec.Pinned,
				Author:    rec.Author,
			})
		}
	}

	return items
}
