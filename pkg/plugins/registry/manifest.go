// Package registry models the public Infracost plugin registry: a machine
// readable manifest (registry.json) enumerating every known plugin, where its
// release artifacts live, who authored it, and whether Infracost considers it
// official. The CLI fetches and caches this manifest and consumes it to power
// the plugin install/update/search/info commands.
//
// This package owns the CLI-side copy of the manifest validation rules. The
// registry repository ships a JSON Schema that mirrors these rules for PR CI;
// keeping the rules here means the same validation the schema enforces also
// runs against every fetched manifest.
package registry

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// SupportedSchemaVersion is the highest registry manifest schemaVersion this
// build understands. A manifest declaring a newer version is rejected with an
// upgrade hint rather than parsed against fields this CLI may not recognise.
const SupportedSchemaVersion = 1

// Component types the CLI knows how to install. A component whose type is not
// one of these marks its owning entry uninstallable (see Entry.Installable)
// rather than rejecting the whole manifest — a newer plugin kind must not
// break an older CLI's ability to read the rest of the registry.
const (
	ComponentTypeParser   = "parser"
	ComponentTypeProvider = "provider"
)

// entryNameRE enforces the `<github-owner>/<github-repository>` namespacing.
// Owner and repository each must be non-empty; the owner follows GitHub's
// login rules (alphanumeric or single hyphens, no leading/trailing hyphen) and
// the repository allows the usual repo characters.
var entryNameRE = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?/[A-Za-z0-9._-]+$`)

// platformRE checks a `goos/goarch` pair is well formed (two non-empty,
// slash-separated segments).
var platformRE = regexp.MustCompile(`^[A-Za-z0-9]+/[A-Za-z0-9]+$`)

// ValidEntryName reports whether name satisfies the registry's
// `<github-owner>/<github-repository>` namespacing. It is the exported form of
// the rule validate() enforces, so callers building an entry (e.g. `plugin
// package`) can reject a bad name before assembling one.
func ValidEntryName(name string) bool { return entryNameRE.MatchString(name) }

// Registry is the top-level manifest fetched from the plugins registry repo.
type Registry struct {
	SchemaVersion int     `json:"schemaVersion"`
	Plugins       []Entry `json:"plugins"`
}

// Entry is one plugin — one GitHub repository — in the registry. A repository
// may ship a parser, a provider, or both; every component shares the entry's
// resolved release version.
type Entry struct {
	Name          string      `json:"name"`
	DisplayName   string      `json:"displayName"`
	Description   string      `json:"description"`
	Author        string      `json:"author"`
	Official      bool        `json:"official"`
	Homepage      string      `json:"homepage"`
	License       string      `json:"license"`
	VersionURL    string      `json:"versionUrl"`
	MinCLIVersion string      `json:"minCliVersion,omitempty"`
	Components    []Component `json:"components"`

	// uninstallable is set during parsing when the entry declares a component
	// type this CLI doesn't understand. Such entries still appear in listings
	// (so users can see the plugin exists) but install/update refuse them.
	uninstallable       bool
	uninstallableReason string
}

// Component is one installable artifact within an entry: a parser or provider.
type Component struct {
	// Type is "parser" or "provider" (see ComponentType* constants). An
	// unrecognised value marks the entry uninstallable.
	Type string `json:"type"`
	// BinaryName is the on-disk/archive binary name, including the
	// parser/provider prefix (e.g. "infracost-parser-kubernetes").
	BinaryName string `json:"binaryName"`
	// Platforms are the supported "goos/goarch" pairs for this component.
	Platforms []string `json:"platforms"`
	// Download is a URL template for this component's archive, with {version},
	// {os}, and {arch} placeholders.
	Download string `json:"download"`
	// Checksums is a URL template for the archive's SHA256 sidecar. Required.
	Checksums string `json:"checksums"`
}

// Parse decodes and validates a registry manifest. It rejects manifests with a
// schemaVersion newer than SupportedSchemaVersion (with an upgrade hint) and
// applies the full validation rule set (see validate). Unknown component types
// do not fail the manifest — their entries are marked uninstallable instead.
func Parse(data []byte) (*Registry, error) {
	var reg Registry
	if err := json.Unmarshal(data, &reg); err != nil {
		return nil, fmt.Errorf("invalid registry manifest JSON: %w", err)
	}

	if reg.SchemaVersion < 1 {
		return nil, fmt.Errorf("registry manifest is missing a valid schemaVersion (got %d)", reg.SchemaVersion)
	}
	if reg.SchemaVersion > SupportedSchemaVersion {
		return nil, fmt.Errorf("registry manifest schemaVersion %d is newer than this CLI supports (max %d) — upgrade the Infracost CLI to use this registry", reg.SchemaVersion, SupportedSchemaVersion)
	}

	if err := reg.validate(); err != nil {
		return nil, err
	}
	return &reg, nil
}

// ParseEntry decodes and validates a single manifest entry — the
// manifest-entry.json emitted by `plugin package` — applying the same rule set
// Parse applies to a full manifest (namespaced name, 1–2 components, unique
// binary names, download + checksum templates, well-formed platforms). The
// returned entry carries the same uninstallable flag a manifest entry would, so
// an unknown component type is surfaced rather than rejected. It is the local,
// pre-PR counterpart to fetching the entry from the registry by name.
func ParseEntry(data []byte) (*Entry, error) {
	var e Entry
	if err := json.Unmarshal(data, &e); err != nil {
		return nil, fmt.Errorf("invalid manifest entry JSON: %w", err)
	}
	reg := &Registry{SchemaVersion: SupportedSchemaVersion, Plugins: []Entry{e}}
	if err := reg.validate(); err != nil {
		return nil, err
	}
	return &reg.Plugins[0], nil
}

// validate enforces the manifest rules the registry JSON Schema mirrors:
// namespaced unique names, 1–2 components per entry, no duplicate component
// types within an entry, globally unique binary names, and every component
// carrying download + checksum templates and at least one well-formed
// platform. A component with an unrecognised type does not fail validation;
// its entry is flagged uninstallable.
func (r *Registry) validate() error {
	seenNames := map[string]struct{}{}
	seenBinaries := map[string]string{} // binaryName -> owning entry name

	for i := range r.Plugins {
		e := &r.Plugins[i]

		if !entryNameRE.MatchString(e.Name) {
			return fmt.Errorf("invalid plugin name %q: names must be namespaced as <github-owner>/<github-repository>", e.Name)
		}
		if _, dup := seenNames[e.Name]; dup {
			return fmt.Errorf("duplicate plugin name %q in registry manifest", e.Name)
		}
		seenNames[e.Name] = struct{}{}

		switch {
		case len(e.Components) == 0:
			return fmt.Errorf("plugin %q has no components", e.Name)
		case len(e.Components) > 2:
			return fmt.Errorf("plugin %q declares %d components (an entry may declare at most one parser and one provider)", e.Name, len(e.Components))
		}

		seenTypes := map[string]struct{}{}
		for _, c := range e.Components {
			if c.BinaryName == "" {
				return fmt.Errorf("plugin %q has a component with no binaryName", e.Name)
			}
			if c.Download == "" {
				return fmt.Errorf("plugin %q component %q has no download URL template", e.Name, c.BinaryName)
			}
			if c.Checksums == "" {
				return fmt.Errorf("plugin %q component %q has no checksums URL template", e.Name, c.BinaryName)
			}
			if len(c.Platforms) == 0 {
				return fmt.Errorf("plugin %q component %q lists no platforms", e.Name, c.BinaryName)
			}
			for _, p := range c.Platforms {
				if !platformRE.MatchString(p) {
					return fmt.Errorf("plugin %q component %q has an invalid platform %q (want goos/goarch)", e.Name, c.BinaryName, p)
				}
			}

			if owner, dup := seenBinaries[c.BinaryName]; dup {
				if owner == e.Name {
					return fmt.Errorf("plugin %q declares two components with binaryName %q", e.Name, c.BinaryName)
				}
				return fmt.Errorf("duplicate component binaryName %q (declared by both %q and %q)", c.BinaryName, owner, e.Name)
			}
			seenBinaries[c.BinaryName] = e.Name

			switch c.Type {
			case ComponentTypeParser, ComponentTypeProvider:
				if _, dup := seenTypes[c.Type]; dup {
					return fmt.Errorf("plugin %q declares more than one %s component", e.Name, c.Type)
				}
				seenTypes[c.Type] = struct{}{}
			default:
				e.uninstallable = true
				e.uninstallableReason = fmt.Sprintf("component type %q is not supported by this CLI version — a newer CLI may be required", c.Type)
			}
		}
	}
	return nil
}

// Installable reports whether every component in the entry has a type this CLI
// understands. Uninstallable entries are still listed but must not be
// installed or updated.
func (e *Entry) Installable() bool { return !e.uninstallable }

// UninstallableReason returns a human-readable explanation of why the entry is
// not installable, or "" when it is.
func (e *Entry) UninstallableReason() string { return e.uninstallableReason }

// Component returns the component of the given type, if the entry declares one.
func (e *Entry) Component(typ string) (Component, bool) {
	for _, c := range e.Components {
		if c.Type == typ {
			return c, true
		}
	}
	return Component{}, false
}

// SupportsPlatform reports whether the component lists the given goos/goarch.
func (c Component) SupportsPlatform(goos, goarch string) bool {
	return slices.Contains(c.Platforms, goos+"/"+goarch)
}

// SupportsPlatform reports whether every component in the entry supports the
// given goos/goarch — the entry-level all-components install semantics. Use
// UnsupportedComponents to report which components fail and on what platforms.
func (e *Entry) SupportsPlatform(goos, goarch string) bool {
	for _, c := range e.Components {
		if !c.SupportsPlatform(goos, goarch) {
			return false
		}
	}
	return true
}

// UnsupportedComponents returns the components that do not support the given
// goos/goarch, in declaration order. Empty when the whole entry is supported.
func (e *Entry) UnsupportedComponents(goos, goarch string) []Component {
	var out []Component
	for _, c := range e.Components {
		if !c.SupportsPlatform(goos, goarch) {
			out = append(out, c)
		}
	}
	return out
}

// componentTypes returns the entry's component types in declaration order,
// used for listing capability strings like "parser + provider".
func (e *Entry) componentTypes() []string {
	types := make([]string, 0, len(e.Components))
	for _, c := range e.Components {
		types = append(types, c.Type)
	}
	return types
}

// Capabilities renders the entry's component types as a human-readable string
// (e.g. "parser", "provider", or "parser + provider").
func (e *Entry) Capabilities() string {
	return strings.Join(e.componentTypes(), " + ")
}
