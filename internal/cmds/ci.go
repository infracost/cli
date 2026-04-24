package cmds

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/infracost/cli/internal/api"
	"github.com/infracost/cli/internal/api/dashboard"
	"github.com/infracost/cli/internal/config"
	"github.com/infracost/cli/internal/ui"
	"github.com/infracost/cli/internal/vcs"
	"github.com/infracost/cli/pkg/auth/browser"
	"github.com/spf13/cobra"
	"golang.org/x/oauth2"
)

// actionsRef is the pinned commit SHA for the infracost/actions composite actions.
const actionsRef = "a1f1aab438c2d0e642e7cd596a1c8750c7c75a5e"

type repoInfo struct {
	owner string
	repo  string
	host  string
}

func (r repoInfo) isGitHub() bool {
	return strings.Contains(r.host, "github.com")
}

func (r repoInfo) slug() string {
	return r.owner + "/" + r.repo
}

func parseRemoteURL(remoteURL string) (repoInfo, error) {
	// SSH: git@github.com:owner/repo.git
	sshRe := regexp.MustCompile(`^git@([^:]+):([^/]+)/([^/]+?)(?:\.git)?$`)
	if m := sshRe.FindStringSubmatch(remoteURL); m != nil {
		return repoInfo{host: m[1], owner: m[2], repo: m[3]}, nil
	}

	// HTTPS: https://github.com/owner/repo.git
	httpsRe := regexp.MustCompile(`^https?://([^/]+)/([^/]+)/([^/]+?)(?:\.git)?$`)
	if m := httpsRe.FindStringSubmatch(remoteURL); m != nil {
		return repoInfo{host: m[1], owner: m[2], repo: m[3]}, nil
	}

	return repoInfo{}, fmt.Errorf("could not parse remote URL %q — expected SSH (git@host:owner/repo.git) or HTTPS (https://host/owner/repo.git) format", remoteURL)
}

// selectSetupOrg resolves the user's organization for setup commands.
// It respects the --org flag (via resolveOrg from org.go), auto-selects when
// there is only one org, and errors when there are multiple without --org set.
// TODO(DEV-232): Replace the multi-org error with an interactive org picker.
func selectSetupOrg(ctx context.Context, cfg *config.Config, source oauth2.TokenSource) (dashboard.Organization, error) {
	if err := resolveOrg(ctx, cfg, source); err != nil {
		return dashboard.Organization{}, err
	}

	client := cfg.Dashboard.Client(api.Client(ctx, source, cfg.OrgID))
	user, err := client.CurrentUser(ctx)
	if err != nil {
		return dashboard.Organization{}, fmt.Errorf("fetching current user: %w", err)
	}

	if len(user.Organizations) == 0 {
		return dashboard.Organization{}, fmt.Errorf("no organizations found for this account — create one at https://dashboard.infracost.io or check that you're logged into the right account with `infracost login`")
	}

	// If --org resolved to a specific org ID, find it.
	if cfg.OrgID != "" {
		for _, org := range user.Organizations {
			if org.ID == cfg.OrgID {
				return org, nil
			}
		}
		return dashboard.Organization{}, fmt.Errorf("organization %q not found — check the value passed to --org", cfg.Org)
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

func ciSetup(cfg *config.Config) *cobra.Command {
	var ciPipeline bool
	var yes bool

	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Set up Infracost CI integration for this repository",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := requireUserLogin(cfg); err != nil {
				return err
			}
			return RunCISetup(cmd.Context(), cfg, ciPipeline, yes)
		},
	}

	cmd.Flags().BoolVar(&ciPipeline, "ci-pipeline", false, "Use CI pipeline mode (GitHub Actions) instead of the app integration")
	cmd.Flags().BoolVar(&yes, "yes", false, "Skip confirmation prompts for non-interactive scripting")

	return cmd
}

// RunCISetup is the core logic for `infracost ci setup`, callable from the
// unified `infracost setup` flow (DEV-230).
func RunCISetup(ctx context.Context, cfg *config.Config, ciPipeline, yes bool) error {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getting working directory: %w", err)
	}

	repoRoot := vcs.GetRepoRoot(cwd)
	if repoRoot == "" {
		return fmt.Errorf("not inside a git repository — run this command from within a git repo")
	}

	remoteURL := vcs.GetRemoteURL(repoRoot)
	if remoteURL == "" {
		return fmt.Errorf("no git remote found — run this from a repository with an origin remote")
	}

	repo, err := parseRemoteURL(remoteURL)
	if err != nil {
		return err
	}

	defaultBranch := vcs.GetDefaultBranch(repoRoot)

	if ciPipeline {
		return runCIPipelineSetup(ctx, cfg, repo, repoRoot, defaultBranch, yes)
	}
	return runCIAppSetup(ctx, cfg, repo)
}

func runCIAppSetup(ctx context.Context, cfg *config.Config, repo repoInfo) error {
	fmt.Println()
	ui.Heading("Scanning repository")

	provider := detectVCSProvider(repo)
	ui.Successf("%s repository  %s", provider, repo.slug())

	source, err := cfg.Auth.Token(ctx)
	if err != nil {
		return fmt.Errorf("authenticating: %w", err)
	}

	org, err := selectSetupOrg(ctx, cfg, source)
	if err != nil {
		return err
	}

	ui.Successf("Infracost org      %s", org.Name)

	// Check if the repo is already connected via the app integration.
	orgClient := cfg.Dashboard.Client(api.Client(ctx, source, org.ID))
	var connected bool
	if err := ui.RunWithSpinnerErr(ctx, "Checking repository...", "Repository checked", func(ctx context.Context) error {
		connected, _ = orgClient.HasRepo(ctx, org.ID, repo.slug())
		return nil
	}); err != nil {
		return err
	}
	if connected {
		ui.Success("App integration already connected")
		fmt.Println()
		fmt.Println("This repository is already sending PR cost estimates.")
		fmt.Printf("To manage settings: https://dashboard.infracost.io/org/%s/repos\n", org.Slug)
		return nil
	}

	fmt.Println()
	fmt.Println("The recommended way to set up Infracost is the app integration.")
	fmt.Println()
	fmt.Println("It works with GitHub, GitLab, and Azure Repos — no YAML or")
	fmt.Println("secrets to manage. Infracost handles everything automatically.")
	fmt.Println()

	dashboardURL := fmt.Sprintf("https://dashboard.infracost.io/org/%s/repos", org.Slug)
	fmt.Println("Opening your dashboard to connect this repository:")
	fmt.Printf("  %s\n", dashboardURL)
	fmt.Println()

	if err := browser.Open(dashboardURL); err != nil {
		ui.Warn("Failed to open browser. Visit the URL above manually.")
	} else {
		ui.Success("Browser opened")
	}

	fmt.Println()
	fmt.Println("Once connected, Infracost will comment on every PR automatically.")
	fmt.Println()
	fmt.Println("Need CI pipeline control instead?  infracost ci setup --ci-pipeline")

	return nil
}

func runCIPipelineSetup(ctx context.Context, cfg *config.Config, repo repoInfo, repoRoot, defaultBranch string, yes bool) error {
	fmt.Println()
	ui.Heading("Scanning repository")

	if !repo.isGitHub() {
		provider := detectVCSProvider(repo)
		ui.Successf("Git repository      %s", repo.slug())
		ui.Failf("CI provider         %s detected — GitHub Actions only for now", provider)
		fmt.Println()
		fmt.Println("Run `infracost ci setup` to use the app integration instead.")
		return fmt.Errorf("%s is not supported for CI pipeline mode", provider)
	}

	ui.Successf("GitHub repository   %s", repo.slug())

	apiKey := os.Getenv("INFRACOST_API_KEY")
	if apiKey == "" {
		ui.Fail("Infracost API key   not found")
		fmt.Println()
		fmt.Println("CI pipeline setup is currently in early access.")
		fmt.Println("To get access, please contact a sales representative.")
		fmt.Println()
		fmt.Println("Already have a key? Set it as an environment variable:")
		fmt.Println("  export INFRACOST_API_KEY=<your-key>")
		return fmt.Errorf("INFRACOST_API_KEY environment variable not set")
	}
	ui.Success("Infracost API key   ready (from INFRACOST_API_KEY)")

	source, err := cfg.Auth.Token(ctx)
	if err != nil {
		return fmt.Errorf("authenticating: %w", err)
	}

	org, err := selectSetupOrg(ctx, cfg, source)
	if err != nil {
		return err
	}
	ui.Successf("Infracost org       %s", org.Name)

	ghPath, _ := exec.LookPath("gh")
	hasGH := ghPath != ""
	if hasGH {
		ui.Success("gh CLI              available")
	} else {
		ui.Warn("gh CLI              not found — secret will need to be set manually")
	}

	workflowDir := filepath.Join(repoRoot, ".github", "workflows")
	diffPath := filepath.Join(workflowDir, "infracost-diff.yml")
	scanPath := filepath.Join(workflowDir, "infracost-scan.yml")

	writeWorkflows := true
	if fileExists(diffPath) || fileExists(scanPath) {
		overwrite, err := promptExistingWorkflows(yes)
		if err != nil {
			return err
		}
		writeWorkflows = overwrite
	}

	fmt.Println()
	ui.Heading("This will:")
	if writeWorkflows {
		ui.Step("Create  .github/workflows/infracost-diff.yml")
		ui.Step("Create  .github/workflows/infracost-scan.yml")
	}
	if hasGH {
		ui.Stepf("Set     INFRACOST_API_KEY secret on %s", repo.slug())
	}

	if !yes {
		var confirm bool
		err := huh.NewConfirm().
			Title("Ready?").
			Affirmative("Yes").
			Negative("No").
			Value(&confirm).
			Run()
		if err != nil {
			if errors.Is(err, huh.ErrUserAborted) {
				return nil
			}
			return err
		}
		if !confirm {
			return nil
		}
	}

	if writeWorkflows {
		if err := os.MkdirAll(workflowDir, 0o750); err != nil { //nolint:gosec // G301: workflows dir needs group read+exec for CI runners
			return fmt.Errorf("creating workflow directory: %w", err)
		}

		if err := os.WriteFile(diffPath, []byte(diffWorkflowContent()), 0o600); err != nil {
			return fmt.Errorf("writing diff workflow: %w", err)
		}
		ui.Success("Created .github/workflows/infracost-diff.yml")

		if err := os.WriteFile(scanPath, []byte(scanWorkflowContent(defaultBranch)), 0o600); err != nil {
			return fmt.Errorf("writing scan workflow: %w", err)
		}
		ui.Success("Created .github/workflows/infracost-scan.yml")
	}

	if hasGH {
		ghCmd := exec.CommandContext(ctx, ghPath, "secret", "set", "INFRACOST_API_KEY", //nolint:gosec // G204: ghPath is from exec.LookPath, not user input
			"--body", apiKey,
			"--repo", repo.slug())
		if err := ghCmd.Run(); err != nil {
			ui.Warn("Failed to set INFRACOST_API_KEY secret via gh")
			fmt.Println()
			printManualSecretInstructions(repo)
		} else {
			ui.Success("Set INFRACOST_API_KEY secret (via gh secret set)")
		}
	} else {
		fmt.Println()
		printManualSecretInstructions(repo)
	}

	fmt.Println()
	if writeWorkflows {
		ui.Heading("Done. Push this commit to see Infracost on your next PR:")
		fmt.Println()
		fmt.Println("  git add .github/workflows/infracost-diff.yml .github/workflows/infracost-scan.yml")
		fmt.Println("  git commit -m \"chore: add Infracost CI integration\"")
		fmt.Println("  git push")
	} else {
		ui.Heading("Done. The INFRACOST_API_KEY secret has been configured.")
	}

	return nil
}

func promptExistingWorkflows(yes bool) (bool, error) {
	if yes {
		return true, nil
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
		Run()
	if err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return false, nil
		}
		return false, err
	}

	return selected == optionUpdate, nil
}

func printManualSecretInstructions(repo repoInfo) {
	ui.Heading("One manual step remaining:")
	fmt.Println("Set the API key as a GitHub secret:")
	fmt.Println()
	fmt.Printf("  gh secret set INFRACOST_API_KEY --body \"$INFRACOST_API_KEY\" \\\n")
	fmt.Printf("      --repo %s\n", repo.slug())
	fmt.Println()
	fmt.Println("Or add it in GitHub:")
	fmt.Printf("  https://github.com/%s/settings/secrets/actions/new\n", repo.slug())
}

func detectVCSProvider(repo repoInfo) string {
	switch {
	case strings.Contains(repo.host, "github.com"):
		return "GitHub"
	case strings.Contains(repo.host, "gitlab"):
		return "GitLab"
	case strings.Contains(repo.host, "dev.azure.com"):
		return "Azure DevOps"
	case strings.Contains(repo.host, "bitbucket"):
		return "Bitbucket"
	default:
		return repo.host
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func diffWorkflowContent() string {
	return `name: Infracost Diff

on:
  pull_request:
    types: [opened, synchronize, reopened, closed]
  workflow_dispatch:
    inputs:
      pr-number:
        description: "Pull request number to scan"
        required: true
        type: number

permissions:
  contents: read
  pull-requests: write

jobs:
  infracost-diff:
    runs-on: ubuntu-latest
    steps:
      - name: Get PR details
        id: pr
        env:
          GH_TOKEN: ${{ github.token }}
          PR_NUMBER: ${{ inputs.pr-number || github.event.pull_request.number }}
        run: |
          if [ -n "${{ inputs.pr-number }}" ]; then
            BASE_REF=$(gh pr view "$PR_NUMBER" --repo "$GITHUB_REPOSITORY" --json baseRefName -q .baseRefName)
            HEAD_REF=$(gh pr view "$PR_NUMBER" --repo "$GITHUB_REPOSITORY" --json headRefName -q .headRefName)
          else
            BASE_REF="${{ github.event.pull_request.base.ref }}"
            HEAD_REF="${{ github.event.pull_request.head.ref }}"
          fi
          echo "base-ref=${BASE_REF}" >> $GITHUB_OUTPUT
          echo "head-ref=${HEAD_REF}" >> $GITHUB_OUTPUT
          echo "pr-number=${PR_NUMBER}" >> $GITHUB_OUTPUT

      - name: Checkout base branch
        if: github.event.action != 'closed'
        uses: actions/checkout@v4
        with:
          ref: ${{ steps.pr.outputs.base-ref }}
          path: base

      - name: Checkout head branch
        if: github.event.action != 'closed'
        uses: actions/checkout@v4
        with:
          ref: ${{ steps.pr.outputs.head-ref }}
          path: head

      - name: Run Infracost Diff
        uses: infracost/actions/diff@` + actionsRef + `
        with:
          api-key: ${{ secrets.INFRACOST_API_KEY }}
          base-path: ${{ github.event.action != 'closed' && 'base' || '' }}
          head-path: ${{ github.event.action != 'closed' && 'head' || '' }}
          pr-number: ${{ steps.pr.outputs.pr-number }}
`
}

func scanWorkflowContent(defaultBranch string) string {
	return `name: Infracost Scan

on:
  push:
    branches: [` + defaultBranch + `]

permissions:
  contents: read

jobs:
  infracost-scan:
    runs-on: ubuntu-latest
    steps:
      - name: Checkout
        uses: actions/checkout@v4

      - name: Run Infracost Scan
        uses: infracost/actions/scan@` + actionsRef + `
        with:
          api-key: ${{ secrets.INFRACOST_API_KEY }}
          path: .
`
}
