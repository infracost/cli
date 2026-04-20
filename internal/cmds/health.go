package cmds

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/infracost/cli/internal/api"
	"github.com/infracost/cli/internal/api/dashboard"
	"github.com/infracost/cli/internal/config"
	"github.com/infracost/cli/internal/health"
	"github.com/infracost/cli/internal/update"
	"github.com/infracost/cli/pkg/auth"
	"github.com/infracost/cli/version"
	"github.com/spf13/cobra"
	"golang.org/x/oauth2"
)

func Health(cfg *config.Config) *cobra.Command {
	var verbose, fix, bundle, checkAgents, checkIDE bool
	var scope string

	cmd := &cobra.Command{
		Use:   "health",
		Short: "Run diagnostic checks on your Infracost installation",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if bundle && fix {
				return fmt.Errorf("--bundle and --fix cannot be used together")
			}

			w := cmd.OutOrStdout()

			if bundle {
				verbose = true
				checkAgents = true
				checkIDE = true
			}

			categories := buildCategories(cmd.Context(), cfg, checkAgents, checkIDE, scope)
			report := health.RunChecks(cmd.Context(), categories)
			health.Render(w, report, version.Version, verbose, fix)

			if fix && report.HasFixable() {
				report = health.RunFixes(cmd.Context(), w, categories, report)
				health.Render(w, report, version.Version, verbose, fix)
			}

			if bundle {
				renderBundle(w, cfg)
			}

			if report.Failed() > 0 {
				return fmt.Errorf("%d health check(s) failed", report.Failed())
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&verbose, "verbose", false, "Show full diagnostic detail for every check")
	cmd.Flags().BoolVar(&fix, "fix", false, "Attempt auto-remediation for failing checks")
	cmd.Flags().BoolVar(&bundle, "bundle", false, "Generate a support bundle with full diagnostic output")
	cmd.Flags().BoolVar(&checkAgents, "check-agents", false, "Check AI coding agent integrations")
	cmd.Flags().BoolVar(&checkIDE, "check-ide", false, "Check IDE integrations")
	cmd.Flags().StringVar(&scope, "scope", "user", "Installation scope for --fix: user (global), project, or local")
	return cmd
}

func renderBundle(w io.Writer, cfg *config.Config) {
	_, _ = fmt.Fprintln(w, "\n--- Support Bundle ---")
	_, _ = fmt.Fprintln(w)

	_, _ = fmt.Fprintln(w, "System")
	_, _ = fmt.Fprintf(w, "  os: %s/%s\n", runtime.GOOS, runtime.GOARCH)
	_, _ = fmt.Fprintf(w, "  go: %s\n", runtime.Version())
	if shell := os.Getenv("SHELL"); shell != "" {
		_, _ = fmt.Fprintf(w, "  shell: %s\n", shell)
	}

	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "Environment")
	hasEnv := false
	for _, env := range os.Environ() {
		if strings.HasPrefix(env, "INFRACOST_CLI_") {
			name, _, _ := strings.Cut(env, "=")
			_, _ = fmt.Fprintf(w, "  %s: set\n", name)
			hasEnv = true
		}
	}
	if !hasEnv {
		_, _ = fmt.Fprintln(w, "  (none)")
	}

	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "Cache")
	_, _ = fmt.Fprintf(w, "  token cache: %s (%s)\n", cfg.Auth.TokenCachePath, fileStatus(cfg.Auth.TokenCachePath))
	userStatus := fileStatus(cfg.Auth.UserCachePath)
	if uc, err := cfg.Auth.LoadUserCache(); err == nil && uc != nil {
		if uc.IsStale() {
			userStatus += ", stale"
		} else {
			userStatus += fmt.Sprintf(", updated %s", uc.UpdatedAt.Format(time.RFC3339))
		}
	}
	_, _ = fmt.Fprintf(w, "  user cache: %s (%s)\n", cfg.Auth.UserCachePath, userStatus)
}

func fileStatus(path string) string {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return "not found"
		}
		return fmt.Sprintf("error: %s", err)
	}
	return "exists"
}

func buildCategories(ctx context.Context, cfg *config.Config, checkAgents, checkIDE bool, scope string) []health.Category {
	// Shared state across auth checks.
	var tokenSource oauth2.TokenSource
	var apiUser dashboard.CurrentUser
	var apiElapsed time.Duration

	categories := []health.Category{
		{
			Name: "Authentication",
			Checks: []health.Check{
				{
					Name:     "Credentials found",
					FailName: "No credentials found",
					Fix: func(ctx context.Context) error {
						return RunLogin(ctx, cfg)
					},
					Run: func(_ context.Context) health.Result {
						if len(cfg.Auth.AuthenticationToken) > 0 {
							tokenSource = cfg.Auth.AuthenticationToken
							return health.Result{
								Status:  health.StatusPass,
								Verbose: []string{"source: INFRACOST_CLI_AUTHENTICATION_TOKEN"},
							}
						}
						ts := cfg.Auth.TokenFromCache(ctx)
						if ts == nil {
							return health.Result{
								Status:  health.StatusFail,
								Hint:    "Run `infracost login` to authenticate",
								Verbose: []string{fmt.Sprintf("token cache: %s", cfg.Auth.TokenCachePath)},
							}
						}
						tokenSource = ts
						return health.Result{
							Status:  health.StatusPass,
							Verbose: []string{fmt.Sprintf("token cache: %s", cfg.Auth.TokenCachePath)},
						}
					},
				},
				{
					Name:      "Token valid",
					DependsOn: []int{0},
					Fix: func(ctx context.Context) error {
						return RunLogin(ctx, cfg)
					},
					Run: func(_ context.Context) health.Result {
						// TokenFromCache already validates the JWT. If we
						// reached here the token parsed and is not expired.
						tok, err := tokenSource.Token()
						if err != nil {
							return health.Result{
								Status: health.StatusFail,
								Label:  "Token invalid",
								Hint:   fmt.Sprintf("Run `infracost login` to re-authenticate (%s)", err),
							}
						}
						var verbose []string
						if !tok.Expiry.IsZero() {
							verbose = append(verbose, fmt.Sprintf("expires: %s", tok.Expiry.Format(time.RFC3339)))
						}
						return health.Result{
							Status:  health.StatusPass,
							Verbose: verbose,
						}
					},
				},
				{
					Name:      "Organization accessible",
					FailName:  "Organization not accessible",
					DependsOn: []int{0},
					Run: func(ctx context.Context) health.Result {
						client := cfg.Dashboard.Client(
							api.Client(ctx, tokenSource, cfg.OrgID),
						)
						start := time.Now()
						user, err := client.CurrentUser(ctx)
						apiElapsed = time.Since(start)
						if err != nil {
							return health.Result{
								Status: health.StatusFail,
								Hint:   fmt.Sprintf("API error: %s", err),
							}
						}
						apiUser = user
						if len(user.Organizations) == 0 {
							return health.Result{
								Status:  health.StatusFail,
								Hint:    "No organizations found. Create one at https://dashboard.infracost.io",
								Verbose: []string{fmt.Sprintf("user: %s (%s)", user.Email, user.ID)},
							}
						}
						orgName := user.Organizations[0].Name

						// Build verbose org list, marking the selected org.
						cached := cacheUser(cfg, user)
						selectedSlug, _, _ := currentOrgSlug(cfg, cached.Organizations, cached.SelectedOrgID)
						verbose := []string{
							fmt.Sprintf("user: %s (%s)", user.Email, user.ID),
						}
						for _, org := range cached.Organizations {
							line := fmt.Sprintf("org: %s (%s)", org.Name, org.Slug)
							if org.Slug == selectedSlug {
								line += "  ← selected"
							}
							verbose = append(verbose, line)
						}
						return health.Result{
							Status:  health.StatusPass,
							Detail:  fmt.Sprintf(`"%s"`, orgName),
							Verbose: verbose,
						}
					},
				},
				{
					Name:      "API reachable",
					DependsOn: []int{2},
					Run: func(_ context.Context) health.Result {
						return health.Result{
							Status:  health.StatusPass,
							Detail:  fmt.Sprintf("(%d ms)", apiElapsed.Milliseconds()),
							Verbose: []string{fmt.Sprintf("endpoint: %s", cfg.Dashboard.Endpoint)},
						}
					},
				},
			},
		},
		{
			Name: "CLI",
			Checks: []health.Check{
				{
					Name: "Version",
					Fix:  update.Update,
					Run: func(ctx context.Context) health.Result {
						info, err := update.CheckLatestVersion(ctx)
						if err != nil {
							return health.Result{
								Status:  health.StatusWarning,
								Label:   fmt.Sprintf("Version %s", version.Version),
								Detail:  "(unable to check for updates)",
								Hint:    err.Error(),
							}
						}
						if info.UpToDate {
							return health.Result{
								Status: health.StatusPass,
								Label:  fmt.Sprintf("Version %s (latest)", info.Current),
							}
						}
						return health.Result{
							Status: health.StatusWarning,
							Label:  fmt.Sprintf("Version %s (latest is %s)", info.Current, info.Latest),
							Hint:   "Run `infracost update` to upgrade",
						}
					},
				},
			},
		},
		{
			Name: "Configuration",
			Checks: []health.Check{
				{
					Name: "Config file valid",
					Run: func(_ context.Context) health.Result {
						// Config was already parsed and processed by
						// PersistentPreRun. If we reached the health command
						// it loaded successfully.
						var verbose []string
						if cfg.Currency != "" {
							verbose = append(verbose, fmt.Sprintf("currency: %s", cfg.Currency))
						}
						verbose = append(verbose, fmt.Sprintf("pricing endpoint: %s", cfg.PricingEndpoint))
						return health.Result{
							Status:  health.StatusPass,
							Verbose: verbose,
						}
					},
				},
				{
					Name:     "Default org set",
					FailName: "Default org not set",
					Fix: func(_ context.Context) error {
						source, err := cfg.Auth.Token(ctx)
						if err != nil {
							return err
						}
						return resolveOrg(ctx, cfg, source)
					},
					Run: func(_ context.Context) health.Result {
						// Try to resolve org non-interactively.
						var orgs []auth.CachedOrganization
						var selectedOrgID string

						// Use the API result if we have it.
						if len(apiUser.Organizations) > 0 {
							cached := cacheUser(cfg, apiUser)
							orgs = cached.Organizations
							selectedOrgID = cached.SelectedOrgID
						}

						// Fall back to cached user data.
						if len(orgs) == 0 {
							if uc, err := cfg.Auth.LoadUserCache(); err == nil && uc != nil {
								orgs = uc.Organizations
								selectedOrgID = uc.SelectedOrgID
							}
						}

						if len(orgs) == 0 {
							return health.Result{
								Status: health.StatusWarning,
								Hint:   "No organization data available",
							}
						}

						slug, name, source := currentOrgSlug(cfg, orgs, selectedOrgID)
						if slug != "" {
							label := name
							if label == "" {
								label = slug
							}
							var verbose []string
							switch source {
							case orgSourceFlag:
								verbose = append(verbose, "source: --org flag / INFRACOST_CLI_ORG")
							case orgSourceRepo:
								verbose = append(verbose, "source: .infracost/org")
							case orgSourceGlobal:
								verbose = append(verbose, "source: infracost org switch")
							}
							return health.Result{
								Status:  health.StatusPass,
								Detail:  fmt.Sprintf("(%s)", label),
								Verbose: verbose,
							}
						}

						if len(orgs) == 1 {
							return health.Result{
								Status: health.StatusPass,
								Detail: fmt.Sprintf("(%s)", orgs[0].Name),
							}
						}

						return health.Result{
							Status: health.StatusFail,
							Hint:   "Run `infracost org switch` to select an organization",
						}
					},
				},
			},
		},
	}

	if checkAgents {
		categories = append(categories, buildAgentChecks(cfg, scope))
	}
	if checkIDE {
		categories = append(categories, buildIDEChecks())
	}

	return categories
}

func buildAgentChecks(cfg *config.Config, scope string) health.Category {
	var checks []health.Check
	for _, a := range supportedAgents {
		if !a.enabled || a.check == nil {
			continue
		}
		a := a // capture loop variable
		checks = append(checks, health.Check{
			Name: a.name,
			Fix: func(_ context.Context) error {
				return setupAgent(cfg, a, scope)
			},
			Run: func(_ context.Context) health.Result {
				bin, err := resolveAgentBinary(cfg, a)
				if err != nil {
					return health.Result{
						Status: health.StatusSkipped,
						Hint:   "binary not found on PATH",
					}
				}
				installed, err := a.check(bin)
				if err != nil {
					return health.Result{
						Status:  health.StatusWarning,
						Hint:    fmt.Sprintf("could not verify skills: %s", err),
						Verbose: []string{fmt.Sprintf("binary: %s", bin)},
					}
				}
				if installed {
					return health.Result{
						Status:  health.StatusPass,
						Detail:  "(skills installed)",
						Verbose: []string{fmt.Sprintf("binary: %s", bin)},
					}
				}
				return health.Result{
					Status:  health.StatusWarning,
					Detail:  "(skills not installed)",
					Hint:    fmt.Sprintf("Run `infracost agent setup` to install skills for %s", a.name),
					Verbose: []string{fmt.Sprintf("binary: %s", bin)},
				}
			},
		})
	}
	return health.Category{Name: "AI Agents", Checks: checks}
}

func buildIDEChecks() health.Category {
	var checks []health.Check
	for _, ide := range supportedIDEs {
		if !ide.enabled || ide.check == nil {
			continue
		}
		ide := ide // capture loop variable
		checks = append(checks, health.Check{
			Name: ide.name,
			Fix: func(_ context.Context) error {
				return installIDE(ide)
			},
			Run: func(_ context.Context) health.Result {
				var bin string
				for _, b := range ide.binaries {
					if path, err := exec.LookPath(b); err == nil {
						bin = path
						break
					}
				}
				if bin == "" {
					return health.Result{
						Status: health.StatusSkipped,
						Hint:   "binary not found on PATH",
					}
				}
				installed, err := ide.check(bin)
				if err != nil {
					return health.Result{
						Status:  health.StatusWarning,
						Hint:    fmt.Sprintf("could not verify extension: %s", err),
						Verbose: []string{fmt.Sprintf("binary: %s", bin)},
					}
				}
				if installed {
					return health.Result{
						Status:  health.StatusPass,
						Detail:  "(extension installed)",
						Verbose: []string{fmt.Sprintf("binary: %s", bin)},
					}
				}
				return health.Result{
					Status:  health.StatusWarning,
					Detail:  "(extension not installed)",
					Hint:    "Run `infracost ide setup` to install the extension",
					Verbose: []string{fmt.Sprintf("binary: %s", bin)},
				}
			},
		})
	}
	return health.Category{Name: "IDE Integrations", Checks: checks}
}
