// Package scanrun runs the post-auth scan pipeline (run parameters →
// scanner → format → cache) so callers like the `scan` CLI command and
// the TUI converge on identical semantics.
//
// The caller is responsible for resolving credentials and the active
// organization first (via cfg.Auth.Token + orgresolve.Resolve). Both of
// those may prompt interactively (multi-org pickers, browser auth) and
// therefore cannot run inside a spinner or inside a bubbletea program.
// scanrun is the part that's safe to wrap in a spinner.
package scanrun

import (
	"context"
	"fmt"
	"time"

	"github.com/infracost/cli/internal/api"
	"github.com/infracost/cli/internal/api/events"
	"github.com/infracost/cli/internal/config"
	"github.com/infracost/cli/internal/format"
	"github.com/infracost/cli/internal/scanner"
	"github.com/infracost/cli/internal/vcs"
	"github.com/infracost/cli/pkg/logging"
	"golang.org/x/oauth2"
)

// Stage labels passed to OnProgress so callers can render their own progress UI.
const (
	StageLoadingParams   = "Loading run parameters..."
	StageScanning        = "Scanning..."
	StagePersistingCache = "Saving results..."
)

// Options controls a scan run.
type Options struct {
	// AbsoluteDir is the absolute path of the directory to scan. Required.
	AbsoluteDir string

	// Source is an authenticated token source for the active user. Required.
	// Callers obtain this from cfg.Auth.Token(ctx) before invoking Run.
	Source oauth2.TokenSource

	// BranchName overrides the branch reported to the dashboard. When empty,
	// the helper derives it from the git working tree at AbsoluteDir.
	BranchName string

	// Bypass forces a fresh scan even if a cached result exists for
	// AbsoluteDir within the configured TTL.
	Bypass bool

	// OnOverride is invoked when the dashboard returns an organization that
	// differs from the user's configured default — i.e. when an explicit
	// --org flag overrode the default. The callback receives the slug of
	// the organization actually being used. nil is safe.
	OnOverride func(orgSlug string)

	// OnProgress is invoked at each pipeline stage so the caller can update
	// its own progress UI. Stage strings are stable constants in this
	// package. nil is safe.
	OnProgress func(stage string)
}

// Result is the outcome of a single scan run.
type Result struct {
	// Output is the cached/serializable shape of the result that the
	// caller should pass to renderers and trackers.
	Output *format.Output

	// Result is the structured scanner result. nil on cache hits since
	// only Output is persisted.
	Result *format.Result

	// RunSeconds is how long the underlying scanner.Scan call took. Zero
	// for cache hits.
	RunSeconds float64

	// PrevForDir is the previous cached result for the same directory, if
	// any. Loaded with stale-allowed semantics so callers can compute
	// directory-scoped diffs without rejecting the snapshot when source
	// files have changed since it was captured. nil when no prior run
	// exists.
	PrevForDir *format.Output

	// FromCache is true when Output came from cfg.Cache.ForPath rather
	// than a fresh scan.
	FromCache bool
}

// Run executes the scan pipeline. The caller is responsible for surfacing
// errors, rendering Result.Output, and emitting any "infracost-run"-style
// telemetry — only side effects internal to the pipeline (events.RegisterMetadata
// for org/repo/branch identifiers, cache writes) happen here.
//
// Caller pre-conditions: cfg.OrgID is set (orgresolve.Resolve has run) and
// opts.Source is a valid authenticated token source.
func Run(ctx context.Context, cfg *config.Config, opts Options) (*Result, error) {
	if opts.AbsoluteDir == "" {
		return nil, fmt.Errorf("scanrun: AbsoluteDir is required")
	}
	if opts.Source == nil {
		return nil, fmt.Errorf("scanrun: Source is required")
	}

	branchName := opts.BranchName
	if branchName == "" {
		branchName = vcs.GetCurrentBranch(opts.AbsoluteDir)
	}
	repositoryURL := vcs.GetRemoteURL(opts.AbsoluteDir)

	// Cache hit short-circuit (unless caller forced a refresh).
	if !opts.Bypass {
		if cached, err := cfg.Cache.ForPath(opts.AbsoluteDir); err == nil {
			prev, _ := loadPrevForDir(cfg, opts.AbsoluteDir)
			return &Result{
				Output:     cached,
				PrevForDir: prev,
				FromCache:  true,
			}, nil
		}
	}

	client := cfg.Dashboard.Client(api.Client(ctx, opts.Source, cfg.OrgID))

	progress(opts, StageLoadingParams)
	runParameters, err := client.RunParameters(ctx, repositoryURL, branchName)
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve run parameters: %w", err)
	}

	// If --org was not provided, use the org from RunParameters.
	// If --org was provided, surface a hook when it overrides the default.
	if cfg.Org == "" {
		cfg.OrgID = runParameters.OrganizationID
	} else if runParameters.OrganizationID != "" && cfg.OrgID != runParameters.OrganizationID {
		if opts.OnOverride != nil {
			if slug := overrideOrgSlug(cfg, cfg.OrgID); slug != "" {
				opts.OnOverride(slug)
			}
		}
	}

	events.RegisterMetadata("orgId", cfg.OrgID)
	events.RegisterMetadata("repoId", repositoryURL)
	events.RegisterMetadata("branchId", branchName)

	progress(opts, StageScanning)
	s := scanner.NewScanner(cfg)
	startTime := time.Now()
	scanResult, err := s.Scan(ctx, runParameters, opts.AbsoluteDir, branchName, opts.Source)
	if err != nil {
		return nil, fmt.Errorf("failed to scan target: %w", err)
	}
	runSeconds := time.Since(startTime).Seconds()

	output := format.ToOutput(scanResult)

	prev, _ := loadPrevForDir(cfg, opts.AbsoluteDir)

	progress(opts, StagePersistingCache)
	if err := cfg.Cache.Write(opts.AbsoluteDir, &output); err != nil {
		logging.Warn("failed to cache results: " + err.Error())
	}

	return &Result{
		Output:     &output,
		Result:     scanResult,
		RunSeconds: runSeconds,
		PrevForDir: prev,
	}, nil
}

func progress(opts Options, stage string) {
	if opts.OnProgress != nil {
		opts.OnProgress(stage)
	}
}

func loadPrevForDir(cfg *config.Config, absoluteDir string) (*format.Output, error) {
	prev, err := cfg.Cache.ForPathAllowStale(absoluteDir)
	if err != nil {
		logging.Infof("could not load previous run data for directory: %v", err)
		return nil, err
	}
	logging.Infof("found previous run data for directory in cache")
	return prev, nil
}

// overrideOrgSlug returns the user-facing slug for the active OrgID by looking
// it up in the user cache. Used to surface override hints; returns "" when the
// cache has no entry for the ID.
func overrideOrgSlug(cfg *config.Config, orgID string) string {
	uc, err := cfg.Auth.LoadUserCache()
	if err != nil || uc == nil {
		return ""
	}
	for _, org := range uc.Organizations {
		if org.ID == orgID {
			return org.Slug
		}
	}
	return ""
}
