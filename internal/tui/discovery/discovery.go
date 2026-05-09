// Package discovery walks the user's home directory looking for
// directories that look like IaC projects, so the TUI's empty-state
// view can present a picker instead of forcing the user to type a
// path. It deliberately avoids any IaC parsing — the heuristic is a
// pure file-glob, fast enough to run unattended on multi-thousand-
// directory $HOMEs.
package discovery

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Project is one IaC project the walker found.
type Project struct {
	Path     string    `json:"path"`     // absolute directory path
	Name     string    `json:"name"`     // basename of Path
	Markers  []string  `json:"markers"`  // file-glob patterns that matched
	LastSeen time.Time `json:"lastSeen"` // when the walker last saw this project
}

// skipDirs lists subdirectories the walker never descends into. The
// goal is to avoid wasting time on caches, build outputs, and vendor
// trees that won't contain authored IaC. Mirrors cache.skipDirs and
// adds a few more well-known noise dirs.
var skipDirs = map[string]bool{
	".git":              true,
	".terraform":        true,
	".terragrunt-cache": true,
	"node_modules":      true,
	".idea":             true,
	".vscode":           true,
	".next":             true,
	".cache":            true,
	"vendor":            true,
	"dist":              true,
	"build":             true,
	"target":            true,
	"__pycache__":       true,
	".venv":             true,
	".tox":              true,
}

// iacMarkers is the set of file-glob patterns that mark a directory
// as an IaC project candidate. We deliberately keep this list narrow:
// false positives clutter the picker; users can always type a path
// manually.
//
// Glob patterns evaluate against entries in a single directory only
// (no recursion). MatchedMarkers below collects which patterns matched
// so the picker can show "this looks like Terraform / CDK / …" hints.
var iacMarkers = []string{
	"*.tf",            // Terraform / OpenTofu
	"*.tfvars",        // Terraform variable files
	"cdk.json",        // AWS CDK
	"infracost.yml",   // Infracost project config
	"infracost.yaml",  //
	"pulumi.yaml",     // Pulumi
	"Pulumi.yaml",     //
	"terragrunt.hcl",  // Terragrunt
	"main.tf.json",    // Terraform JSON syntax
}

// IsIaCProject reports whether dir contains at least one file that
// looks like an IaC source. Pure file-glob — no parsing.
func IsIaCProject(dir string) bool {
	return len(MatchedMarkers(dir)) > 0
}

// MatchedMarkers returns the list of iacMarkers patterns that match
// at least one file directly inside dir. Empty slice when no marker
// matches; the directory isn't an IaC project.
func MatchedMarkers(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var matched []string
	for _, pat := range iacMarkers {
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			ok, err := filepath.Match(pat, e.Name())
			if err == nil && ok {
				matched = append(matched, pat)
				break
			}
		}
	}
	return matched
}

// Walk visits directories under root and yields one Project per
// git-managed directory that contains IaC files anywhere inside it.
// Yielding at the repo root rather than at each individual IaC
// subdirectory keeps the picker readable: a Terraform monorepo with
// modules/, environments/prod/, environments/staging/ etc. shows up
// as a single entry — the repo the user knows by name — instead of
// dozens of nested rows.
//
// Walk respects ctx and stops when it's canceled. Subdirectories
// listed in skipDirs are pruned, recursion is capped at maxDepth from
// root, and symlinks are not followed.
func Walk(ctx context.Context, root string, onFound func(Project)) error {
	const maxDepth = 6
	rootDepth := pathDepth(root)

	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// Permission errors etc. — skip the offending dir, keep walking.
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !d.IsDir() {
			return nil
		}
		if path != root && skipDirs[d.Name()] {
			return filepath.SkipDir
		}
		if pathDepth(path)-rootDepth > maxDepth {
			return filepath.SkipDir
		}
		// Symlinks can produce cycles; treat them like skipped dirs.
		if d.Type()&fs.ModeSymlink != 0 {
			return filepath.SkipDir
		}

		// Once we hit a git-managed dir we either yield it (when it
		// contains IaC anywhere) or skip the whole subtree. Either way
		// we stop descending — IaC files inside a git repo always
		// belong to that repo, never to a separate "project".
		if isGitDir(path) {
			if markers := scanRepoForIaC(ctx, path); len(markers) > 0 {
				onFound(Project{
					Path:     path,
					Name:     filepath.Base(path),
					Markers:  markers,
					LastSeen: time.Now(),
				})
			}
			return filepath.SkipDir
		}
		return nil
	})
}

// isGitDir reports whether dir contains a .git child — either a
// directory (a regular clone) or a file (a worktree pointing at the
// main repo's gitdir). Both indicate a git-managed root.
func isGitDir(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, ".git"))
	if err != nil {
		return false
	}
	return info.IsDir() || info.Mode().IsRegular()
}

// scanRepoForIaC walks a git repo looking for IaC marker files. It
// returns the deduplicated set of marker patterns matched anywhere
// inside the repo, or nil when no markers were found. The walk caps
// at a shallow depth — IaC living deeper than that in a repo is rare,
// and an unbounded second walk would dominate the discovery cost.
func scanRepoForIaC(ctx context.Context, repoRoot string) []string {
	const repoMaxDepth = 4
	repoDepth := pathDepth(repoRoot)
	seen := map[string]struct{}{}

	_ = filepath.WalkDir(repoRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() {
			if path != repoRoot && skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			if pathDepth(path)-repoDepth > repoMaxDepth {
				return filepath.SkipDir
			}
			if d.Type()&fs.ModeSymlink != 0 {
				return filepath.SkipDir
			}
			return nil
		}
		for _, pat := range iacMarkers {
			if ok, _ := filepath.Match(pat, d.Name()); ok {
				seen[pat] = struct{}{}
				break
			}
		}
		return nil
	})

	if len(seen) == 0 {
		return nil
	}
	out := make([]string, 0, len(seen))
	for _, pat := range iacMarkers { // preserve declared order
		if _, ok := seen[pat]; ok {
			out = append(out, pat)
		}
	}
	return out
}

// pathDepth returns the number of separators in path. Used by the
// walker's depth cap; treating root depth as the baseline lets us
// express the cap as "max levels below root".
func pathDepth(path string) int {
	return strings.Count(filepath.Clean(path), string(filepath.Separator))
}
