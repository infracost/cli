package cmds

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/infracost/cli/internal/config"
	"github.com/infracost/cli/internal/ui"
	"github.com/infracost/cli/pkg/plugins"
	"github.com/spf13/cobra"
)

// packageFlags collects the parsed `plugin package` flag values.
type packageFlags struct {
	name          string
	version       string
	binaries      []string
	buildDir      string
	out           string
	baseURL       string
	flat          bool
	force         bool
	displayName   string
	description   string
	author        string
	homepage      string
	license       string
	minCLIVersion string
}

func pluginsPackageCmd(cfg *config.Config) *cobra.Command {
	var f packageFlags

	cmd := &cobra.Command{
		Use:   "package --name <owner>/<repo> [flags]",
		Short: "Package a plugin's builds into a publishable release",
		Long: `Package a plugin's built binaries into a registry-shaped release.

Turns parser and/or provider builds into per-component/platform archives in the
layout the installer expects, a .sha256 sidecar for each, a shared latest-version
file, and a manifest-entry.json ready to open as a registry pull request.

Provide binaries either by convention from a --build-dir (files named
<binary-name>_<goos>_<goarch>[.exe]) or explicitly with repeated
--binary <type>:<goos>/<goarch>=<path> flags; the two may be combined. The
component type is taken from the --binary flag, or derived from the binary name
prefix (infracost-parser-* / infracost-provider-*) when scanning a build dir.

Archives are deterministic: the same inputs always produce byte-identical output
and checksums. The current platform's binaries are run through the same checklist
as 'plugin validate' before packaging; other platforms get static checks. The
release version is read from the current-platform binaries unless --version pins
it (a disagreement is an error). Every generated entry is unofficial.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runPluginPackage(cmd, cfg, &f)
		},
	}

	cmd.Flags().StringVar(&f.name, "name", "", "Registry entry name as <github-owner>/<github-repository> (required)")
	cmd.Flags().StringVar(&f.version, "version", "", "Shared release version; read from the current-platform binaries when omitted")
	cmd.Flags().StringArrayVar(&f.binaries, "binary", nil, "A build to package as <type>:<goos>/<goarch>=<path> (repeatable)")
	cmd.Flags().StringVar(&f.buildDir, "build-dir", "", "Directory of builds named <binary-name>_<goos>_<goarch>[.exe] to package")
	cmd.Flags().StringVar(&f.out, "out", "dist", "Output directory for the packaged release")
	cmd.Flags().StringVar(&f.baseURL, "base-url", "", "Hosting root for the download templates (defaults to the configured plugin base URL)")
	cmd.Flags().BoolVar(&f.flat, "flat", false, "Emit a single-namespace layout (e.g. for GitHub Releases) instead of the nested per-binary layout")
	cmd.Flags().BoolVar(&f.force, "force", false, "Overwrite existing artifacts for the same component and version")
	cmd.Flags().StringVar(&f.displayName, "display-name", "", "Human-readable name for the manifest entry (defaults to the repository)")
	cmd.Flags().StringVar(&f.description, "description", "", "Short description for the manifest entry")
	cmd.Flags().StringVar(&f.author, "author", "", "Author for the manifest entry (defaults to the owner)")
	cmd.Flags().StringVar(&f.homepage, "homepage", "", "Homepage URL (defaults to the GitHub repository)")
	cmd.Flags().StringVar(&f.license, "license", "", "License identifier for the manifest entry")
	cmd.Flags().StringVar(&f.minCLIVersion, "min-cli-version", "", "Minimum Infracost CLI version required to install the plugin")
	return cmd
}

func runPluginPackage(cmd *cobra.Command, cfg *config.Config, f *packageFlags) error {
	if f.name == "" {
		return fmt.Errorf("--name is required (as <github-owner>/<github-repository>)")
	}

	components, err := collectPackageComponents(f)
	if err != nil {
		return err
	}

	baseURL := f.baseURL
	if baseURL == "" {
		baseURL = cfg.Plugins.BaseURL
	}

	res, err := plugins.PackageRelease(cmd.Context(), plugins.PackageOptions{
		Name:          f.name,
		Version:       f.version,
		OutDir:        f.out,
		BaseURL:       baseURL,
		Flat:          f.flat,
		Force:         f.force,
		Components:    components,
		DisplayName:   f.displayName,
		Description:   f.description,
		Author:        f.author,
		Homepage:      f.homepage,
		License:       f.license,
		MinCLIVersion: f.minCLIVersion,
	})
	if err != nil {
		var ve *plugins.PackageValidationError
		if errors.As(err, &ve) {
			printPluginValidateHumanFailures(ve.Results)
			return errPluginValidationFailed
		}
		return err
	}

	printPackageResult(res)
	return nil
}

// collectPackageComponents assembles the component list from the --binary flags
// and the --build-dir convention, merging builds that share a binary name.
func collectPackageComponents(f *packageFlags) ([]plugins.PackageComponentInput, error) {
	acc := newComponentAccumulator()

	if f.buildDir != "" {
		if err := acc.addBuildDir(f.buildDir); err != nil {
			return nil, err
		}
	}
	for _, spec := range f.binaries {
		if err := acc.addBinarySpec(spec); err != nil {
			return nil, err
		}
	}

	comps := acc.components()
	if len(comps) == 0 {
		return nil, fmt.Errorf("no builds to package: pass --build-dir and/or --binary")
	}
	return comps, nil
}

// componentAccumulator gathers builds keyed by binary name while preserving the
// order components were first seen, so output ordering is stable.
type componentAccumulator struct {
	order []string
	byBin map[string]*plugins.PackageComponentInput
}

func newComponentAccumulator() *componentAccumulator {
	return &componentAccumulator{byBin: map[string]*plugins.PackageComponentInput{}}
}

// add records one build for a binary name and type, rejecting a type that
// disagrees with an earlier build for the same binary.
func (a *componentAccumulator) add(binaryName, typ string, b plugins.PackageBuild) error {
	c, ok := a.byBin[binaryName]
	if !ok {
		a.order = append(a.order, binaryName)
		a.byBin[binaryName] = &plugins.PackageComponentInput{Type: typ, BinaryName: binaryName, Builds: []plugins.PackageBuild{b}}
		return nil
	}
	if c.Type != typ {
		return fmt.Errorf("binary %q was given as both a %s and a %s", binaryName, c.Type, typ)
	}
	c.Builds = append(c.Builds, b)
	return nil
}

func (a *componentAccumulator) components() []plugins.PackageComponentInput {
	out := make([]plugins.PackageComponentInput, 0, len(a.order))
	for _, bin := range a.order {
		out = append(out, *a.byBin[bin])
	}
	return out
}

// addBinarySpec parses a --binary value of the form
// <type>:<goos>/<goarch>=<path> and records it. The binary name is taken from
// the file's base name (minus any .exe suffix).
func (a *componentAccumulator) addBinarySpec(spec string) error {
	meta, path, ok := strings.Cut(spec, "=")
	if !ok || path == "" {
		return fmt.Errorf("invalid --binary %q: want <type>:<goos>/<goarch>=<path>", spec)
	}
	typ, platform, ok := strings.Cut(meta, ":")
	if !ok {
		return fmt.Errorf("invalid --binary %q: want <type>:<goos>/<goarch>=<path>", spec)
	}
	goos, goarch, ok := strings.Cut(platform, "/")
	if !ok || goos == "" || goarch == "" {
		return fmt.Errorf("invalid --binary %q: platform must be <goos>/<goarch>", spec)
	}

	binaryName := strings.TrimSuffix(filepath.Base(path), ".exe")
	return a.add(binaryName, typ, plugins.PackageBuild{GOOS: goos, GOARCH: goarch, Path: path})
}

// addBuildDir scans a directory for files named <binary-name>_<goos>_<goarch>
// (optionally .exe) and records each as a build, deriving the component type
// from the binary name's prefix.
func (a *componentAccumulator) addBuildDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("failed to read --build-dir %s: %w", dir, err)
	}

	matched := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		binaryName, goos, goarch, ok := parseBuildDirName(e.Name())
		if !ok {
			continue
		}
		typ, ok := plugins.ComponentTypeForBinaryName(binaryName)
		if !ok {
			return fmt.Errorf("cannot derive a component type from build %q: name it infracost-parser-* / infracost-provider-*, or use --binary <type>:<goos>/<goarch>=<path>", e.Name())
		}
		if err := a.add(binaryName, typ, plugins.PackageBuild{GOOS: goos, GOARCH: goarch, Path: filepath.Join(dir, e.Name())}); err != nil {
			return err
		}
		matched++
	}

	if matched == 0 {
		return fmt.Errorf("no builds named <binary-name>_<goos>_<goarch>[.exe] found in --build-dir %s", dir)
	}
	return nil
}

// parseBuildDirName splits a build-dir filename of the form
// <binary-name>_<goos>_<goarch>[.exe] into its parts. The binary name may itself
// contain no underscores, so the platform is taken from the final two
// underscore-separated fields.
func parseBuildDirName(filename string) (binaryName, goos, goarch string, ok bool) {
	base := strings.TrimSuffix(filename, ".exe")

	lastUnderscore := strings.LastIndex(base, "_")
	if lastUnderscore < 0 {
		return "", "", "", false
	}
	goarch = base[lastUnderscore+1:]

	rest := base[:lastUnderscore]
	prevUnderscore := strings.LastIndex(rest, "_")
	if prevUnderscore < 0 {
		return "", "", "", false
	}
	goos = rest[prevUnderscore+1:]
	binaryName = rest[:prevUnderscore]

	if binaryName == "" || goos == "" || goarch == "" {
		return "", "", "", false
	}
	return binaryName, goos, goarch, true
}

// printPluginValidateHumanFailures renders the failing pre-package checklists in
// the same shape as `plugin validate`, without returning an error itself.
func printPluginValidateHumanFailures(results []plugins.ValidationResult) {
	ui.Failf("Pre-package validation failed on the current platform's binaries:")
	for i, res := range results {
		if i > 0 {
			fmt.Println()
		}
		fmt.Printf("%s %s\n", ui.Bold("Validating"), ui.Accent(res.Path))
		for _, c := range res.Checks {
			fmt.Println(renderCheckLine(c))
		}
	}
}

// printPackageResult renders the success summary and the next steps an author
// takes to publish the release.
func printPackageResult(res *plugins.PackageResult) {
	ui.Successf("Packaged %s %s", ui.Accent(res.Entry.Name), res.Version)

	// List the archives grouped by component, in a stable order.
	sorted := append([]plugins.PackagedArtifact(nil), res.Artifacts...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].BinaryName != sorted[j].BinaryName {
			return sorted[i].BinaryName < sorted[j].BinaryName
		}
		return sorted[i].GOOS+"/"+sorted[i].GOARCH < sorted[j].GOOS+"/"+sorted[j].GOARCH
	})
	for _, art := range sorted {
		ui.Stepf("%s %s %s", ui.Bold(art.Type), ui.Muted(art.GOOS+"/"+art.GOARCH), ui.Muted(art.ArchivePath))
	}

	for _, name := range res.StaticOnly {
		ui.Warnf("%s had no current-platform build — it was checked statically only (its binary was not executed).", name)
	}

	fmt.Println()
	ui.Stepf("%s %s", ui.Muted("manifest entry"), ui.Muted(res.ManifestPath))
	fmt.Println()
	fmt.Println(ui.Bold("Next steps:"))
	ui.Hintf(2, "Verify the release round-trips: %s", ui.Code("infracost plugin validate --release "+res.ManifestPath))
	ui.Hintf(2, "Publish the %s tree to your hosting base URL", ui.Muted(filepath.Dir(res.ManifestPath)))
	ui.Hintf(2, "Open a PR adding the manifest entry to the Infracost plugin registry")
}
