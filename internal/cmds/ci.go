package cmds

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/charmbracelet/huh"
	"github.com/infracost/cli/internal/api"
	"github.com/infracost/cli/internal/api/dashboard"
	"github.com/infracost/cli/internal/api/events"
	"github.com/infracost/cli/internal/config"
	"github.com/infracost/cli/internal/ui"
	"github.com/infracost/cli/internal/vcs"
	"github.com/infracost/cli/pkg/auth/browser"
	"github.com/spf13/cobra"
	"golang.org/x/oauth2"
)

type repoInfo struct {
	owner string
	repo  string
	host  string
}

func (r repoInfo) slug() string {
	return r.owner + "/" + r.repo
}

// hostSlug is HOST/OWNER/REPO, the form `gh --repo` needs so a self-hosted
// remote's secret is not written to the same-named repo on github.com.
func (r repoInfo) hostSlug() string {
	return r.host + "/" + r.slug()
}

func parseRemoteURL(remoteURL string) (repoInfo, error) {
	remoteURL = normalizeSSHURL(remoteURL)

	if info, ok := parseAzureRemoteURL(remoteURL); ok {
		return info, nil
	}

	// SSH: git@github.com:owner/repo.git. The owner is greedy so a GitLab
	// subgroup remote keeps every level of its path.
	sshRe := regexp.MustCompile(`^git@([^:]+):(.+)/([^/]+?)(?:\.git)?$`)
	if m := sshRe.FindStringSubmatch(remoteURL); m != nil {
		return repoInfo{host: m[1], owner: m[2], repo: m[3]}, nil
	}

	// HTTPS: https://github.com/owner/repo.git. The userinfo is dropped, not
	// captured — repoInfo.host is printed and passed to gh — and matched
	// greedily, so an @ in a password cannot spill into the host.
	httpsRe := regexp.MustCompile(`^https?://(?:[^/]*@)?([^/@]+)/(.+)/([^/]+?)(?:\.git)?$`)
	if m := httpsRe.FindStringSubmatch(remoteURL); m != nil {
		return repoInfo{host: m[1], owner: m[2], repo: m[3]}, nil
	}

	return repoInfo{}, fmt.Errorf("%w %q — expected SSH (git@host:owner/repo.git) or HTTPS (https://host/owner/repo.git) format", errCIRemoteURLUnparsed, remoteURL)
}

// sshURLRe matches the ssh:// form, which `url.<base>.insteadOf` rewrites
// remotes into and which Azure also documents.
var sshURLRe = regexp.MustCompile(`^(?:ssh|git)://(?:[^/]*@)?([^/:]+)(?::\d+)?/+(.+)$`)

// normalizeSSHURL rewrites an ssh:// remote into the scp-style form the parsers
// take. The port is SSH-only, so it is dropped rather than carried into
// repoInfo.host, which builds web URLs.
func normalizeSSHURL(remoteURL string) string {
	m := sshURLRe.FindStringSubmatch(remoteURL)
	if m == nil {
		return remoteURL
	}
	return "git@" + m[1] + ":" + m[2]
}

// azureHTTPSRe splits an Azure DevOps HTTPS clone URL on its _git marker. What
// precedes the marker is captured whole and read by parseAzureRemoteURL. The
// org@ userinfo is matched greedily, so an @ in a password stays out of the host.
var azureHTTPSRe = regexp.MustCompile(`^https?://(?:[^/]*@)?([^/@]+)/(?:(.+)/)?_git/([^/]+?)(?:\.git)?/?$`)

// azureSSHRe matches git@ssh.dev.azure.com:v3/org/project/repo and the legacy
// org@vs-ssh.visualstudio.com:v3/org/project/repo.
var azureSSHRe = regexp.MustCompile(`^(?:[^:/]*@)?([^:/@]+):v3/([^/]+)/([^/]+)/([^/]+?)(?:\.git)?$`)

// parseAzureRemoteURL handles the Azure DevOps remote forms, which carry a
// project between the organization and the repository. The dashboard keys
// Azure repositories on org/project/repo, so the project rides in repoInfo.repo
// and slug() renders all three.
func parseAzureRemoteURL(remoteURL string) (repoInfo, bool) {
	if m := azureSSHRe.FindStringSubmatch(remoteURL); m != nil && vcsHostKind(m[1]) == hostAzure {
		return repoInfo{
			host:  m[1],
			owner: unescapePathSegment(m[2]),
			repo:  unescapePathSegment(m[3]) + "/" + unescapePathSegment(m[4]),
		}, true
	}

	m := azureHTTPSRe.FindStringSubmatch(remoteURL)
	if m == nil || vcsHostKind(m[1]) != hostAzure {
		return repoInfo{}, false
	}

	host, repo := m[1], m[3]
	var segments []string
	if m[2] != "" {
		segments = strings.Split(m[2], "/")
	}

	var org string
	if strings.HasSuffix(normalizedHost(host), ".visualstudio.com") {
		// The legacy host names the org, and may carry a collection segment
		// ahead of the project.
		org, _, _ = strings.Cut(host, ".")
		if len(segments) > 0 && strings.EqualFold(segments[0], "DefaultCollection") {
			segments = segments[1:]
		}
	} else {
		if len(segments) == 0 {
			return repoInfo{}, false
		}
		org, segments = segments[0], segments[1:]
	}

	if len(segments) > 1 {
		return repoInfo{}, false
	}
	// The project is left out of the URL when it shares the repository's name.
	project := repo
	if len(segments) == 1 {
		project = segments[0]
	}

	return repoInfo{
		host:  host,
		owner: unescapePathSegment(org),
		repo:  unescapePathSegment(project) + "/" + unescapePathSegment(repo),
	}, true
}

// unescapePathSegment decodes the percent-encoding an HTTPS clone URL puts on
// an Azure project name with spaces; the dashboard stores the decoded name. A
// segment holding a separator or a dot segment stays encoded: the slug is a path.
func unescapePathSegment(s string) string {
	decoded, err := url.PathUnescape(s)
	if err != nil || strings.Contains(decoded, "/") || decoded == "." || decoded == ".." {
		return s
	}
	return decoded
}

// resolveSetupOrgWithSpinner resolves the user's organization for setup
// commands, showing a spinner while fetching the org list. The org resolution
// step (resolveOrg) runs outside the spinner because it may prompt
// interactively. It respects the --org flag, auto-selects when there is only
// one org, and errors when there are multiple without --org set.
// TODO(DEV-232): Replace the multi-org error with an interactive org picker.
func resolveSetupOrgWithSpinner(ctx context.Context, cfg *config.Config, source oauth2.TokenSource) (dashboard.Organization, error) {
	if err := resolveOrg(ctx, cfg, source); err != nil {
		return dashboard.Organization{}, err
	}

	client := cfg.Dashboard.Client(api.Client(ctx, source, cfg.OrgID))
	var user dashboard.CurrentUser
	if err := ui.RunWithSpinnerErr(ctx, "Resolving organization...", "", func(ctx context.Context) error {
		var err error
		user, err = client.CurrentUser(ctx)
		return err
	}); err != nil {
		return dashboard.Organization{}, fmt.Errorf("fetching current user: %w", err)
	}

	if len(user.Organizations) == 0 {
		return dashboard.Organization{}, fmt.Errorf("%w for this account — create one at https://dashboard.infracost.io or verify your login with 'infracost auth login'", errCINoOrganizations)
	}

	if cfg.OrgID != "" {
		for _, org := range user.Organizations {
			if org.ID == cfg.OrgID {
				return org, nil
			}
		}
		return dashboard.Organization{}, fmt.Errorf("%w: %q — check the value passed to --org", errCIOrgNotFound, cfg.Org)
	}

	if len(user.Organizations) == 1 {
		return user.Organizations[0], nil
	}

	return dashboard.Organization{}, fmt.Errorf(
		"you belong to multiple organizations — use --org to select one",
	)
}

func CI(cfg *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ci",
		Short: "Manage CI integrations",
	}
	cmd.AddCommand(ciSetup(cfg))
	return cmd
}

// CISetupOptions controls `infracost ci setup`. It replaces the positional
// bools RunCISetup used to take, because the per-platform follow-ups each add
// a knob of their own.
type CISetupOptions struct {
	// Pipeline writes a CI config for the repository instead of connecting it
	// to the app integration.
	Pipeline bool
	// Platform pins the CI platform by id, skipping detection. Implies Pipeline.
	Platform string
	// Yes skips confirmation prompts for non-interactive scripting.
	Yes bool
}

// CISetupResult reports how a CI setup run ended, for the caller's closing
// card and for the outcome event.
type CISetupResult struct {
	Outcome    string
	Configured bool
	Platform   string

	stage          string
	mode           string
	vcsProvider    string
	platformPinned bool
	yes            bool
	startedAt      time.Time
}

// The stages a run passes through. The outcome event names the one it was in
// when it ended, so a failure points at a phase rather than just "error".
const (
	ciStagePreflight = "preflight"
	ciStageAuth      = "auth"
	ciStageOrg       = "org"
	ciStageConnected = "app-connection-check"
	ciStagePlatform  = "platform"
	ciStageAPIKey    = "api-key"
	ciStageConfirm   = "confirm"
	ciStageWrite     = "write"
	ciStageSecret    = "secret"
)

// Sentinels for the setup failures worth telling apart in telemetry. Error
// text carries repo paths and job names, so only these labels are sent.
var (
	errCINotGitRepo          = errors.New("not inside a git repository")
	errCINoGitRemote         = errors.New("no git remote found")
	errCIRemoteURLUnparsed   = errors.New("could not parse remote URL")
	errCINoOrganizations     = errors.New("no organizations found")
	errCIOrgNotFound         = errors.New("organization not found")
	errCIPlatformUnknown     = errors.New("unknown CI platform")
	errCIPlatformNotSelected = errors.New("no CI platform selected")
	errCIAPIKeyMissing       = errors.New("API key environment variable not set")
	errCINotInteractive      = errors.New("non-interactive terminal")
)

// ciSetupEventTimeout bounds the outcome event, which sits in a defer on the
// way out of an interactive command. Losing an event beats stalling the exit.
const ciSetupEventTimeout = 500 * time.Millisecond

func pushCISetupEvent(ctx context.Context, cfg *config.Config, event string, extra ...interface{}) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), ciSetupEventTimeout)
	defer cancel()
	// Same attribution as every other events push: without the token and org
	// the drop-outs being measured arrive unattributed.
	client := api.Client(ctx, cfg.Auth.TokenFromCache(ctx), cfg.OrgID)
	cfg.Events.Client(client).Push(ctx, event, extra...)
}

func pushCISetupOutcome(ctx context.Context, cfg *config.Config, result *CISetupResult, err error) {
	if result.Outcome == "" {
		result.Outcome = "error"
	}
	// A run that failed before startedAt was set has no duration to report.
	var duration float64
	if !result.startedAt.IsZero() {
		duration = time.Since(result.startedAt).Seconds()
	}
	stage := result.stage
	if stage == "" {
		stage = ciStagePreflight
	}
	mode := result.mode
	if mode == "" {
		mode = "unknown"
	}
	vcsProvider := result.vcsProvider
	if vcsProvider == "" {
		vcsProvider = "unknown"
	}
	extra := []interface{}{
		"outcome", result.Outcome,
		"configured", result.Configured,
		"stage", stage,
		"mode", mode,
		"vcsProvider", vcsProvider,
		"platformPinned", result.platformPinned,
		"yes", result.yes,
		// Not "ciPlatform": that key already carries the CI we are running
		// in, and an extra of the same name would overwrite it.
		"targetCIPlatform", result.Platform,
		"durationSeconds", duration,
	}
	if err != nil {
		extra = append(extra, "errorKind", CISetupErrorKind(err))
	}
	pushCISetupEvent(ctx, cfg, "infracost-ci-setup-outcome", extra...)
}

// CISetupErrorKind buckets an error into a bounded label. Never send
// err.Error(): it embeds local paths and the user's own YAML.
func CISetupErrorKind(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, errCIPlatformNotSelected):
		return "platform-not-selected"
	case errors.Is(err, errCIPlatformUnknown):
		return "platform-unknown"
	case errors.Is(err, errCINotGitRepo):
		return "not-git-repo"
	case errors.Is(err, errCINoGitRemote):
		return "no-git-remote"
	case errors.Is(err, errCIRemoteURLUnparsed):
		return "remote-url-unparsed"
	case errors.Is(err, errCINoOrganizations):
		return "no-organizations"
	case errors.Is(err, errCIOrgNotFound):
		return "org-not-found"
	case errors.Is(err, errCIAPIKeyMissing):
		return "api-key-missing"
	case errors.Is(err, errCINotInteractive):
		return "not-interactive"
	case errors.Is(err, huh.ErrUserAborted):
		return "aborted"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "canceled"
	case errors.Is(err, os.ErrPermission):
		return "permission-denied"
	case errors.Is(err, os.ErrNotExist):
		return "file-not-found"
	case errors.Is(err, os.ErrExist):
		return "file-exists"
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return "network"
	}
	return "other"
}

func ciSetup(cfg *config.Config) *cobra.Command {
	var opts CISetupOptions
	var ciPipeline bool

	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Set up Infracost CI integration for this repository",
		Example: `  # Connect this repo to the Infracost app integration (recommended)
  $ infracost ci setup

  # Write a CI config for this repository instead
  $ infracost ci setup --pipeline`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := requireUserLogin(cfg); err != nil {
				return err
			}
			opts.Pipeline = opts.Pipeline || ciPipeline
			result, err := RunCISetup(cmd.Context(), cfg, opts)
			if err != nil {
				return err
			}
			// No card when the user was handed a by-hand recipe: nothing is
			// set up yet, so "Setup complete" would be a lie.
			if !result.Configured {
				return nil
			}
			// Mirror the unified `infracost setup` flow's closing card —
			// CI gets a tailored "open a PR" CTA via the ciSetUp flag.
			fmt.Println()
			fmt.Print(ui.GradientCard(setupCompleteContent("", "", true)))
			return nil
		},
	}

	cmd.Flags().BoolVar(&opts.Pipeline, "pipeline", false, "Write a CI config for this repository instead of using the app integration")
	cmd.Flags().BoolVar(&ciPipeline, "ci-pipeline", false, "Write a CI config for this repository instead of using the app integration")
	_ = cmd.Flags().MarkDeprecated("ci-pipeline", "use --pipeline instead")
	cmd.Flags().StringVar(&opts.Platform, "ci-platform", "",
		fmt.Sprintf("CI platform to configure (%s) — detected from the repository when unset", strings.Join(ciPlatformIDs(), ", ")))
	cmd.Flags().BoolVar(&opts.Yes, "yes", false, "Skip confirmation prompts for non-interactive scripting")

	return cmd
}

// CISetupAvailable reports whether the current working directory
// satisfies the preflight requirements for `infracost ci setup`: it
// sits inside a git repository with a parseable origin remote. The
// unified `infracost setup` flow uses this to decide whether to even
// offer the CI step — asking "Set up CI?" in a directory that can't
// run setup would just frustrate the user with an error a moment later.
func CISetupAvailable() bool {
	cwd, err := os.Getwd()
	if err != nil {
		return false
	}
	repoRoot := vcs.GetRepoRoot(cwd)
	if repoRoot == "" {
		return false
	}
	remoteURL := vcs.GetRemoteURL(repoRoot)
	if remoteURL == "" {
		return false
	}
	if _, err := parseRemoteURL(remoteURL); err != nil {
		return false
	}
	return true
}

// RunCISetup is the core logic for `infracost ci setup`, callable from the
// unified `infracost setup` flow (DEV-230). It reports whether CI was actually
// configured — false means the user was left with manual steps to follow.
func RunCISetup(ctx context.Context, cfg *config.Config, opts CISetupOptions) (result CISetupResult, err error) {
	// The lifecycle opens before preflight so a run that never gets as far as
	// a repository still reports why.
	result.startedAt = time.Now()
	result.stage = ciStagePreflight
	defer func() {
		pushCISetupOutcome(ctx, cfg, &result, err)
	}()

	cwd, err := os.Getwd()
	if err != nil {
		return result, fmt.Errorf("getting working directory: %w", err)
	}

	repoRoot := vcs.GetRepoRoot(cwd)
	if repoRoot == "" {
		return result, fmt.Errorf("%w — run this command from within a git repo", errCINotGitRepo)
	}

	remoteURL := vcs.GetRemoteURL(repoRoot)
	if remoteURL == "" {
		return result, fmt.Errorf("%w — run this from a repository with an origin remote", errCINoGitRemote)
	}

	repo, err := parseRemoteURL(remoteURL)
	if err != nil {
		return result, err
	}

	defaultBranch := vcs.GetDefaultBranch(repoRoot)

	if opts.Platform != "" {
		opts.Pipeline = true
	}
	if !opts.Pipeline && !hasAppIntegration(repo.host) {
		fmt.Println()
		// The host, not the provider: github.acme.com has no app integration
		// even though GitHub does.
		ui.Warnf("%s has no Infracost app integration — setting up a CI pipeline instead.", repo.host)
		opts.Pipeline = true
	}

	result.mode = "app"
	if opts.Pipeline {
		result.mode = "pipeline"
	}
	result.vcsProvider = vcsHostKind(repo.host)
	if result.vcsProvider == "" {
		result.vcsProvider = "unknown"
	}
	result.platformPinned = opts.Platform != ""
	result.yes = opts.Yes

	if opts.Pipeline {
		result.Configured, err = runCIPipelineSetup(ctx, cfg, repo, repoRoot, defaultBranch, opts, &result)
		return result, err
	}
	result.Configured, err = runCIAppSetup(ctx, cfg, repo, &result)
	return result, err
}

// runCIAppSetup reports true only when the repository is already connected.
// Handing the user a dashboard URL to click is not a finished setup.
func runCIAppSetup(ctx context.Context, cfg *config.Config, repo repoInfo, result *CISetupResult) (bool, error) {
	fmt.Println()
	ui.Heading("Scanning repository")

	// Fixed labels, so a long provider name does not stagger the value column.
	ui.Successf("Git repository      %s", repo.slug())
	ui.Successf("VCS provider        %s", detectVCSProvider(repo))

	result.stage = ciStageAuth
	source, err := cfg.Auth.Token(ctx)
	if err != nil {
		return false, fmt.Errorf("authenticating: %w", err)
	}

	result.stage = ciStageOrg
	org, err := resolveSetupOrgWithSpinner(ctx, cfg, source)
	if err != nil {
		return false, err
	}
	events.RegisterMetadata("orgId", org.ID)
	ui.Successf("Infracost org       %s", org.Slug)

	result.stage = ciStageConnected
	// Check if the repo is already connected via the app integration.
	orgClient := cfg.Dashboard.Client(api.Client(ctx, source, org.ID))
	var connected bool
	if err := ui.RunWithSpinnerErr(ctx, "Checking repository connection...", "", func(ctx context.Context) error {
		var err error
		connected, err = orgClient.HasRepo(ctx, org.ID, repo.slug())
		return err
	}); err != nil {
		// A dashboard blip is not a reason to fail the whole setup: an
		// unanswered check just means the pitch below is shown.
		ui.Warnf("Could not check whether this repository is connected: %v", err)
	}
	if connected {
		result.Outcome = "app-connected"
		ui.Success("App integration already connected")
		fmt.Println()
		fmt.Println("This repository is already sending PR cost estimates.")
		fmt.Println("To manage settings, visit:")
		fmt.Printf("  %s\n", ui.Accentf("https://dashboard.infracost.io/org/%s/repos", org.Slug))
		return true, nil
	}

	fmt.Println()
	fmt.Println("The recommended way to set up Infracost is the app integration.")
	fmt.Println()
	fmt.Println("It works with GitHub, GitLab, and Azure Repos — no YAML or")
	fmt.Println("secrets to manage. Infracost handles everything automatically.")
	fmt.Println()

	dashboardURL := fmt.Sprintf("https://dashboard.infracost.io/org/%s/repos", org.Slug)
	fmt.Printf("  %s\n", ui.Code(dashboardURL))
	if ui.PressEnter("\nPress Enter to open in your browser...") {
		if err := browser.Open(dashboardURL); err != nil {
			ui.Warn("Failed to open browser. Visit the URL above manually.")
		} else {
			ui.Success("Browser opened")
		}
	}

	fmt.Println()
	fmt.Println("Once connected, Infracost will comment on every PR automatically.")
	fmt.Println()
	fmt.Println("To use CI pipeline mode instead, run:")
	fmt.Println("  infracost ci setup --pipeline")

	result.Outcome = "app-pitched"
	return false, nil
}

func runCIPipelineSetup(ctx context.Context, cfg *config.Config, repo repoInfo, repoRoot, defaultBranch string, opts CISetupOptions, result *CISetupResult) (bool, error) {
	fmt.Println()
	ui.Heading("Scanning repository")
	ui.Successf("Git repository      %s", repo.slug())

	result.stage = ciStagePlatform
	platform, err := resolveCIPlatform(repoRoot, repo, opts.Platform)
	if err != nil {
		if errors.Is(err, errCIPlatformNotSelected) {
			result.Outcome = "platform-not-selected"
		}
		return false, err
	}
	result.Platform = platform.id
	ui.Successf("CI platform         %s", platform.name)

	result.stage = ciStageAuth
	source, err := cfg.Auth.Token(ctx)
	if err != nil {
		return false, fmt.Errorf("authenticating: %w", err)
	}

	result.stage = ciStageOrg
	org, err := resolveSetupOrgWithSpinner(ctx, cfg, source)
	if err != nil {
		return false, err
	}
	events.RegisterMetadata("orgId", org.ID)
	ui.Successf("Infracost org       %s", org.Slug)

	if platform.writer == nil {
		result.Outcome = "recipe-printed"
		printCIRecipe(platform, org.Slug)
		return false, nil
	}

	jobOpts := ciJobOpts{
		image:         ciImage,
		repo:          repo,
		defaultBranch: defaultBranch,
		apiKeySecret:  ciAPIKeySecret,
	}

	// Only a run that will set the secret itself needs the key's value — with
	// gh missing the value is never read, so demanding it would refuse a setup
	// the manual steps can finish.
	result.stage = ciStageAPIKey
	setter, canSetSecret := platform.writer.(ciSecretSetter)
	canSetSecret = canSetSecret && setter.CanSetSecret()
	if canSetSecret {
		if os.Getenv(ciAPIKeySecret) == "" {
			result.Outcome = "api-key-missing"
			ui.Fail("Infracost API key   not found")
			fmt.Println()
			fmt.Println("To get an API key, visit your organization's CLI tokens page:")
			ui.OpenOrContinue(cliTokensURL(org.Slug))
			fmt.Println()
			fmt.Println("Once you have a key, set it as an environment variable and retry:")
			fmt.Printf("  export %s=<your-key>\n", ciAPIKeySecret)
			return false, fmt.Errorf("%s: %w", ciAPIKeySecret, errCIAPIKeyMissing)
		}
		ui.Successf("Infracost API key   ready (from %s)", ciAPIKeySecret)
	}

	paths, err := platform.writer.ConfigPaths(repoRoot)
	if err != nil {
		return false, err
	}

	result.stage = ciStageConfirm
	writeConfigs := true
	// A file we wrote is an update, not an overwrite, so the prompt is only for
	// one the user or an older composite-action setup left behind.
	if platform.ownsFiles && anyConfigExists(repoRoot, paths) && !ciFilesAreManaged(repoRoot, paths) {
		overwrite, err := promptExistingWorkflows(opts.Yes)
		if err != nil {
			if errors.Is(err, huh.ErrUserAborted) {
				result.Outcome = "declined"
			}
			return false, err
		}
		writeConfigs = overwrite
	}

	// Removing a user's YAML is the one irreversible thing this command does,
	// so it is listed ahead of the confirmation and --yes is an opt-in to it.
	var upgrades []ciLegacyJob
	if upgrader, ok := platform.writer.(ciUpgrader); ok && writeConfigs {
		upgrades = upgrader.Upgrades(repoRoot)
	}

	fmt.Println()
	ui.Heading("This will:")
	if writeConfigs {
		for _, p := range paths {
			verb := "Create"
			if fileExists(filepath.Join(repoRoot, filepath.FromSlash(p))) {
				verb = "Update"
			}
			ui.Stepf("%-7s  %s", verb, p)
		}
		for _, j := range upgrades {
			ui.Stepf("%-7s  %s (line %d)", "Replace", j.name, j.line)
		}
	}
	if canSetSecret {
		ui.Stepf("%-7s  %s secret on %s", "Set", ciAPIKeySecret, repo.slug())
	}

	if !opts.Yes {
		confirmed, err := confirmCISetup()
		if err != nil || !confirmed {
			// Answering No and backing out are the same funnel step.
			if err == nil || errors.Is(err, huh.ErrUserAborted) {
				result.Outcome = "declined"
			}
			return false, err
		}
	}

	result.stage = ciStageWrite
	var written, notes, warnings []string
	var changed, replaced bool
	if writeConfigs {
		results, writeErr := platform.writer.Write(repoRoot, jobOpts)
		// Report what reached disk before surfacing the failure, so a partial
		// write is not left silently in the working tree.
		for _, r := range results {
			if r.block != "" {
				printCIBlockToPaste(r)
				continue
			}
			written = append(written, r.path)
			switch {
			case r.unchanged:
				ui.Successf("Unchanged %s", r.path)
			case r.created:
				ui.Successf("Created %s", r.path)
				changed = true
			default:
				ui.Successf("Updated %s", r.path)
				changed = true
			}
			for _, name := range r.replaced {
				ui.Successf("Replaced %s", name)
				replaced = true
			}
			notes = append(notes, r.notes...)
			warnings = append(warnings, r.warnings...)
		}
		for _, w := range warnings {
			fmt.Println()
			ui.Warn(w)
		}
		if writeErr != nil {
			return false, writeErr
		}
	}

	result.stage = ciStageSecret
	secretSet, secretFailed := false, false
	if canSetSecret {
		if err := setter.SetSecret(ctx, jobOpts); err != nil {
			ui.Warnf("Failed to set the %s secret: %v", ciAPIKeySecret, err)
			secretFailed = true
			result.Outcome = "secret-failed"
		} else {
			ui.Successf("Set %s secret", ciAPIKeySecret)
			secretSet = true
		}
	}
	// Each note is a paragraph of its own, so it is separated the way the
	// writers separate theirs.
	var steps []string
	for _, n := range notes {
		if len(steps) > 0 {
			steps = append(steps, "")
		}
		steps = append(steps, n)
	}
	if !secretSet {
		if len(steps) > 0 {
			steps = append(steps, "")
		}
		steps = append(steps, platform.writer.Steps(repoRoot, jobOpts)...)
	}
	printCISteps(steps)

	fmt.Println()
	switch {
	case changed:
		ui.Heading("Done. Push this commit to see Infracost on your next PR:")
		fmt.Println()
		message := "chore: add Infracost CI integration"
		if replaced {
			message = "chore: upgrade Infracost CI integration"
		}
		fmt.Printf("  git add %s\n", strings.Join(written, " "))
		fmt.Printf("  git commit -m %q\n", message)
		fmt.Println("  git push")
	case len(written) > 0:
		ui.Heading("Done. Your CI config is already up to date.")
	case secretSet:
		ui.Headingf("Done. The %s secret has been configured.", ciAPIKeySecret)
	default:
		ui.Heading("Done. Nothing was changed.")
	}

	// A declined prompt, a block we could not place, or a secret we tried and
	// failed to set all leave work for the user, so they are not "configured".
	configured := (len(written) > 0 || secretSet) && !secretFailed
	if result.Outcome == "" {
		if configured {
			result.Outcome = "configured"
		} else {
			result.Outcome = "nothing-changed"
		}
	}
	return configured, nil
}

// printCIRecipe is the nil-writer path: name the platform, point at its recipe
// and exit 0. Refusing here would just reword the bug this replaced.
//
// The container lines are printed as well as the URL because this is also the
// generic platform's only output, and a bare link is not instructions.
func printCIRecipe(platform ciPlatform, orgSlug string) {
	fmt.Println()
	ui.Headingf("Infracost has a %s recipe to follow by hand:", platform.name)
	fmt.Println()
	fmt.Printf("  %s\n", ui.Accent(platform.docsURL()))
	fmt.Println()
	fmt.Println("Every platform runs the same container:")
	ui.Stepf("image  %s", ciImage)
	ui.Step("on a pull request  infracost-ci diff --base-path base --head-path head")
	ui.Step("on the default branch  infracost-ci scan --path .")
	fmt.Println()
	fmt.Println("It needs two secrets:")
	ui.Step("INFRACOST_CLI_AUTHENTICATION_TOKEN — get a key at")
	fmt.Printf("     %s\n", ui.Code(cliTokensURL(orgSlug)))
	ui.Step("A VCS token the job comments with — the recipe names the one your platform uses")
}

// printCIBlockToPaste is the refusal in §4: a file whose shape we cannot place
// into gets the rendered block printed, and nothing written.
func printCIBlockToPaste(r ciWriteResult) {
	fmt.Println()
	ui.Warnf("Left %s alone because %s", r.path, r.reason)
	fmt.Println()
	fmt.Printf("Add this to %s by hand:\n", r.path)
	fmt.Println()
	fmt.Println(r.block)
}

func printCISteps(steps []string) {
	if len(steps) == 0 {
		return
	}
	fmt.Println()
	ui.Heading("Remaining manual steps:")
	for _, s := range steps {
		fmt.Println(s)
	}
}

func confirmCISetup() (bool, error) {
	if !ui.IsInteractive() {
		return false, fmt.Errorf("cannot confirm in a %w — re-run with --yes to skip the confirmation prompt", errCINotInteractive)
	}

	var confirm bool
	err := huh.NewConfirm().
		Title("Ready?").
		Affirmative("Yes").
		Negative("No").
		Value(&confirm).
		WithTheme(ui.BrandTheme()).
		Run()
	if err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return false, nil
		}
		return false, err
	}
	return confirm, nil
}

func anyConfigExists(repoRoot string, paths []string) bool {
	for _, p := range paths {
		if fileExists(filepath.Join(repoRoot, filepath.FromSlash(p))) {
			return true
		}
	}
	return false
}

func cliTokensURL(orgSlug string) string {
	return fmt.Sprintf("https://dashboard.infracost.io/org/%s/settings/cli-tokens", orgSlug)
}

func promptExistingWorkflows(yes bool) (bool, error) {
	if yes {
		return true, nil
	}

	if !ui.IsInteractive() {
		return false, fmt.Errorf("infracost workflow files already exist and this is a %w, so overwriting cannot be confirmed — re-run with --yes to overwrite, or remove the existing files first", errCINotInteractive)
	}

	const (
		optionUpdate = iota
		optionCancel
	)

	var selected int
	err := huh.NewSelect[int]().
		Title("Infracost workflow already exists. What would you like to do?").
		Options(
			huh.NewOption[int]("Update to latest recommended config", optionUpdate),
			huh.NewOption[int]("Cancel", optionCancel),
		).
		Value(&selected).
		WithTheme(ui.BrandTheme()).
		Run()
	if err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return false, nil
		}
		return false, err
	}

	return selected == optionUpdate, nil
}

func detectVCSProvider(repo repoInfo) string {
	switch vcsHostKind(repo.host) {
	case hostGitHub:
		return "GitHub"
	case hostGitLab:
		return "GitLab"
	case hostAzure:
		return "Azure DevOps"
	case hostBitbucket:
		return "Bitbucket"
	default:
		return repo.host
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
