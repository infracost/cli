package cmds

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/infracost/cli/internal/config"
	"github.com/infracost/cli/internal/ui"
	"github.com/infracost/cli/pkg/logging"
	"github.com/infracost/cli/pkg/plugins"
	"github.com/infracost/cli/pkg/plugins/registry"
	"github.com/spf13/cobra"
)

// browseRegistryLoad loads the plugin registry for the read-only search/info
// commands. It is a seam so tests can drive listing and resolution without a
// network fetch; production code never reassigns it. The underlying client
// already serves a cached copy (with a staleness warning) when the registry is
// unreachable and returns an error naming the registry URL only when no usable
// cache exists.
var browseRegistryLoad = func(ctx context.Context) (*registry.Registry, error) {
	return registry.NewClient().Load(ctx)
}

// browseVersionHTTPClient bounds the single versionUrl lookup `plugin info`
// makes to resolve an entry's latest release version.
var browseVersionHTTPClient = &http.Client{Timeout: 15 * time.Second}

func pluginsSearchCmd(cfg *config.Config) *cobra.Command {
	var asJSON bool

	cmd := &cobra.Command{
		Use:   "search [query]",
		Short: "Search the Infracost plugin registry",
		Long: "Search the Infracost plugin registry.\n\n" +
			"With no query, lists every plugin in the registry. With a query, filters\n" +
			"to plugins whose name, display name, or description contains it\n" +
			"(case-insensitive). Installed plugins are annotated with their version.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			query := ""
			if len(args) == 1 {
				query = args[0]
			}

			reg, err := browseRegistryLoad(cmd.Context())
			if err != nil {
				return err
			}

			entries := filterRegistryEntries(reg, query)
			byBinary := indexListByBinary(cfg.Plugins.List())

			if asJSON {
				return printSearchJSON(entries, byBinary)
			}
			printSearch(entries, byBinary, query)
			return nil
		},
	}

	cmd.Flags().BoolVar(&asJSON, "json", false, "Output the search results as JSON")
	return cmd
}

func pluginsInfoCmd(cfg *config.Config) *cobra.Command {
	var asJSON bool

	cmd := &cobra.Command{
		Use:   "info <name>",
		Short: "Show details for a plugin in the Infracost registry",
		Long: "Show the full registry metadata for a single plugin: description,\n" +
			"author, official status, homepage, license, its components and their\n" +
			"supported platforms, the latest available version, and per-component\n" +
			"install state.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			reg, err := browseRegistryLoad(cmd.Context())
			if err != nil {
				return err
			}

			entry, err := reg.Resolve(args[0], plugins.RequiredAliases())
			if err != nil {
				return err
			}

			byBinary := indexListByBinary(cfg.Plugins.List())
			latest := resolveLatestVersion(cmd.Context(), entry)

			if asJSON {
				return printInfoJSON(entry, byBinary, latest)
			}
			printInfo(entry, byBinary, latest)
			return nil
		},
	}

	cmd.Flags().BoolVar(&asJSON, "json", false, "Output the plugin details as JSON")
	return cmd
}

// filterRegistryEntries returns the entries matching a case-insensitive
// substring query against name, displayName, and description. An empty query
// returns every entry. Results preserve manifest order and are returned as
// pointers into the registry so Entry methods keep their unexported state.
func filterRegistryEntries(reg *registry.Registry, query string) []*registry.Entry {
	out := make([]*registry.Entry, 0, len(reg.Plugins))
	q := strings.ToLower(strings.TrimSpace(query))
	for i := range reg.Plugins {
		e := &reg.Plugins[i]
		if q == "" || entryMatches(e, q) {
			out = append(out, e)
		}
	}
	return out
}

func entryMatches(e *registry.Entry, lowerQuery string) bool {
	return strings.Contains(strings.ToLower(e.Name), lowerQuery) ||
		strings.Contains(strings.ToLower(e.DisplayName), lowerQuery) ||
		strings.Contains(strings.ToLower(e.Description), lowerQuery)
}

// indexListByBinary indexes known plugins (installed or in the required set) by
// their binary name without any .exe suffix, so a registry component's
// binaryName can be matched against local install state.
func indexListByBinary(items []plugins.ListItem) map[string]plugins.ListItem {
	m := make(map[string]plugins.ListItem, len(items))
	for _, it := range items {
		base := strings.TrimSuffix(filepath.Base(it.Path), ".exe")
		m[base] = it
	}
	return m
}

// componentState is the local install state of a single registry component.
type componentState struct {
	installed bool
	version   string
}

func lookupComponentState(c registry.Component, byBinary map[string]plugins.ListItem) componentState {
	it, ok := byBinary[c.BinaryName]
	if !ok || !it.Installed {
		return componentState{}
	}
	return componentState{installed: true, version: it.Version}
}

// entryState is the aggregate local install state of an entry. Since install is
// all-or-nothing an entry is "installed" only when every component is present;
// a subset present is reported as partial.
type entryState struct {
	installed  bool
	partial    bool
	version    string
	components []componentState
}

func lookupEntryState(e *registry.Entry, byBinary map[string]plugins.ListItem) entryState {
	st := entryState{components: make([]componentState, 0, len(e.Components))}
	installedCount := 0
	for _, c := range e.Components {
		cs := lookupComponentState(c, byBinary)
		st.components = append(st.components, cs)
		if cs.installed {
			installedCount++
			if st.version == "" {
				st.version = cs.version
			}
		}
	}
	st.installed = installedCount > 0 && installedCount == len(e.Components)
	st.partial = installedCount > 0 && installedCount < len(e.Components)
	return st
}

// printSearch renders the human-readable search listing. Content is written to
// stdout (not the ui log writer) so it composes with piping and is capturable.
func printSearch(entries []*registry.Entry, byBinary map[string]plugins.ListItem, query string) {
	fmt.Println()
	if query == "" {
		fmt.Println(ui.Bold(ui.Brand("Plugins in the Infracost registry")))
	} else {
		fmt.Println(ui.Bold(ui.Brand(fmt.Sprintf("Plugins matching %q", query))))
	}
	fmt.Println()

	if len(entries) == 0 {
		if query == "" {
			fmt.Printf("  %s\n", ui.Muted("No plugins found in the registry."))
		} else {
			fmt.Printf("  %s\n", ui.Muted(fmt.Sprintf("No plugins matched %q.", query)))
		}
		return
	}

	for _, e := range entries {
		printSearchRow(e, byBinary)
	}
}

func printSearchRow(e *registry.Entry, byBinary map[string]plugins.ListItem) {
	st := lookupEntryState(e, byBinary)

	trailer := []string{ui.Muted(e.Capabilities())}
	if e.Official {
		trailer = append(trailer, ui.Info("official"))
	} else if e.Author != "" {
		trailer = append(trailer, ui.Muted("by "+e.Author))
	}
	switch {
	case st.installed:
		trailer = append(trailer, ui.Positive("installed "+versionLabel(st.version)))
	case st.partial:
		trailer = append(trailer, ui.Caution("partially installed"))
	}

	fmt.Printf("  %s  %s\n", ui.Accent(pluginListCell(e.Name, 30)), strings.Join(trailer, "  "))

	if e.Description != "" {
		fmt.Printf("      %s\n", ui.Muted(truncateForWidth(e.Description, searchDescWidth())))
	}

	// The entry stays listed even when it can't be installed on this platform;
	// name which component is unavailable so the user understands why.
	if unsupported := e.UnsupportedComponents(runtime.GOOS, runtime.GOARCH); len(unsupported) > 0 {
		fmt.Printf("      %s\n", ui.Caution(fmt.Sprintf("not available for %s/%s (%s)",
			runtime.GOOS, runtime.GOARCH, unsupportedComponentLabel(unsupported))))
	}
	if !e.Installable() {
		fmt.Printf("      %s\n", ui.Caution(e.UninstallableReason()))
	}
}

// searchDescWidth is the visible width descriptions are truncated to in the
// search table, leaving room for the 6-space indent. Falls back to a readable
// fixed width when stdout isn't a TTY.
func searchDescWidth() int {
	w := ui.TerminalContentWidth()
	if w <= 0 {
		w = 100
	}
	w -= 6
	if w < 20 {
		w = 20
	}
	return w
}

// truncateForWidth flattens s to a single line and truncates it to width
// visible columns with a trailing ellipsis. The full text remains available in
// `plugin info` and `--json`.
func truncateForWidth(s string, width int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if width <= 1 {
		return s
	}
	runes := []rune(s)
	if len(runes) <= width {
		return s
	}
	return string(runes[:width-1]) + "…"
}

func unsupportedComponentLabel(comps []registry.Component) string {
	names := make([]string, 0, len(comps))
	for _, c := range comps {
		names = append(names, c.Type)
	}
	return strings.Join(names, ", ")
}

// printInfo renders the full human-readable metadata for one registry entry.
func printInfo(e *registry.Entry, byBinary map[string]plugins.ListItem, latest string) {
	st := lookupEntryState(e, byBinary)

	fmt.Println()
	fmt.Println(ui.Bold(ui.Brand(e.Name)))
	if e.DisplayName != "" && e.DisplayName != e.Name {
		fmt.Printf("  %s\n", ui.Muted(e.DisplayName))
	}
	fmt.Println()

	if e.Description != "" {
		desc := e.Description
		if w := ui.TerminalContentWidth(); w > 4 {
			desc = ui.WrapText(e.Description, w-2)
		}
		for line := range strings.SplitSeq(desc, "\n") {
			fmt.Printf("  %s\n", line)
		}
		fmt.Println()
	}

	printInfoField("Author", authorLabel(e))
	printInfoField("Official", yesNo(e.Official))
	if e.Homepage != "" {
		printInfoField("Homepage", e.Homepage)
	}
	if e.License != "" {
		printInfoField("License", e.License)
	}
	printInfoField("Latest version", versionLabel(latest))
	if e.MinCLIVersion != "" {
		printInfoField("Min CLI version", e.MinCLIVersion)
	}
	printInfoField("Capabilities", e.Capabilities())

	switch {
	case st.installed:
		printInfoField("Installed", "yes ("+versionLabel(st.version)+")")
	case st.partial:
		printInfoField("Installed", "partially — some components missing")
	default:
		printInfoField("Installed", "no")
	}

	if !e.Installable() {
		fmt.Printf("\n  %s\n", ui.Caution(e.UninstallableReason()))
	}

	fmt.Println()
	fmt.Println("  " + ui.Bold("Components:"))
	for i, c := range e.Components {
		printInfoComponent(c, st.components[i])
	}
}

const infoLabelWidth = 16

func printInfoField(label, value string) {
	fmt.Printf("  %s  %s\n", ui.Muted(pluginListCell(label+":", infoLabelWidth)), value)
}

func printInfoComponent(c registry.Component, cs componentState) {
	state := ui.Muted("not installed")
	if cs.installed {
		state = ui.Positive("installed " + versionLabel(cs.version))
	}
	fmt.Printf("    %s %s  %s\n", ui.Accent(pluginListCell(c.Type, 10)), ui.Muted(c.BinaryName), state)
	fmt.Printf("      %s %s\n", ui.Muted("platforms:"), ui.Muted(strings.Join(c.Platforms, ", ")))
	if !c.SupportsPlatform(runtime.GOOS, runtime.GOARCH) {
		fmt.Printf("      %s\n", ui.Caution(fmt.Sprintf("not available for %s/%s", runtime.GOOS, runtime.GOARCH)))
	}
}

// authorLabel names the entry's author, falling back to "Infracost" for
// official entries that omit an explicit author.
func authorLabel(e *registry.Entry) string {
	if e.Author != "" {
		return e.Author
	}
	if e.Official {
		return "Infracost"
	}
	return "unknown"
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// resolveLatestVersion resolves the entry's latest shared release version via
// its versionUrl. It returns "" (rendered as "unknown") when the lookup fails
// or the entry declares no versionUrl, so `plugin info` stays usable offline.
func resolveLatestVersion(ctx context.Context, e *registry.Entry) string {
	if e.VersionURL == "" {
		return ""
	}
	v, err := e.ResolveVersion(ctx, browseVersionHTTPClient, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		logging.Debugf("failed to resolve latest version for %q: %v", e.Name, err)
		return ""
	}
	return v
}

// browseComponentJSON is the stable per-component shape emitted by the --json
// output of search and info.
type browseComponentJSON struct {
	Type                    string   `json:"type"`
	BinaryName              string   `json:"binaryName"`
	Platforms               []string `json:"platforms"`
	SupportsCurrentPlatform bool     `json:"supportsCurrentPlatform"`
	Installed               bool     `json:"installed"`
	Version                 string   `json:"version,omitempty"`
}

// browseEntryJSON is the stable per-entry shape emitted by the --json output of
// search and info. Search omits latestVersion (not resolved for speed); info
// includes it when resolvable.
type browseEntryJSON struct {
	Name             string                `json:"name"`
	DisplayName      string                `json:"displayName"`
	Description      string                `json:"description"`
	Author           string                `json:"author"`
	Official         bool                  `json:"official"`
	Homepage         string                `json:"homepage"`
	License          string                `json:"license"`
	MinCLIVersion    string                `json:"minCliVersion,omitempty"`
	Capabilities     string                `json:"capabilities"`
	Installable      bool                  `json:"installable"`
	Installed        bool                  `json:"installed"`
	InstalledVersion string                `json:"installedVersion,omitempty"`
	LatestVersion    string                `json:"latestVersion,omitempty"`
	Components       []browseComponentJSON `json:"components"`
}

func toBrowseEntryJSON(e *registry.Entry, byBinary map[string]plugins.ListItem, latest string) browseEntryJSON {
	st := lookupEntryState(e, byBinary)

	comps := make([]browseComponentJSON, 0, len(e.Components))
	for i, c := range e.Components {
		platforms := c.Platforms
		if platforms == nil {
			platforms = []string{}
		}
		comps = append(comps, browseComponentJSON{
			Type:                    c.Type,
			BinaryName:              c.BinaryName,
			Platforms:               platforms,
			SupportsCurrentPlatform: c.SupportsPlatform(runtime.GOOS, runtime.GOARCH),
			Installed:               st.components[i].installed,
			Version:                 st.components[i].version,
		})
	}

	return browseEntryJSON{
		Name:             e.Name,
		DisplayName:      e.DisplayName,
		Description:      e.Description,
		Author:           e.Author,
		Official:         e.Official,
		Homepage:         e.Homepage,
		License:          e.License,
		MinCLIVersion:    e.MinCLIVersion,
		Capabilities:     e.Capabilities(),
		Installable:      e.Installable(),
		Installed:        st.installed,
		InstalledVersion: st.version,
		LatestVersion:    latest,
		Components:       comps,
	}
}

func printSearchJSON(entries []*registry.Entry, byBinary map[string]plugins.ListItem) error {
	out := make([]browseEntryJSON, 0, len(entries))
	for _, e := range entries {
		// Latest version is intentionally omitted from search JSON: resolving it
		// would require one network call per entry. Use `plugin info` for it.
		out = append(out, toBrowseEntryJSON(e, byBinary, ""))
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

func printInfoJSON(e *registry.Entry, byBinary map[string]plugins.ListItem, latest string) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(toBrowseEntryJSON(e, byBinary, latest))
}
