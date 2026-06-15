package plugins

import "path/filepath"

func (c *Config) PluginDir() string {
	return c.pluginDir()
}

type ListItem struct {
	Key       string
	Name      string
	Type      string
	Path      string
	Installed bool
	Version   string
}

func (c *Config) List() []ListItem {
	items := make([]ListItem, 0, len(pluginSpecs))
	for _, spec := range pluginSpecs {
		path := c.pluginPathOverride(spec)
		if path == "" {
			path = filepath.Join(c.pluginDir(), pluginBinaryName(spec.Name))
		}

		installed := flatPluginBinaryExists(path)
		version := cachedPluginVersion(path)
		if installed && version == "" {
			version = "unknown"
		}

		items = append(items, ListItem{
			Key:       spec.Key,
			Name:      spec.Name,
			Type:      spec.Type,
			Path:      path,
			Installed: installed,
			Version:   version,
		})
	}
	return items
}
