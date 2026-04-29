package cmds

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/infracost/cli/internal/api"
	"github.com/infracost/cli/internal/api/dashboard"
	"github.com/infracost/cli/internal/config"
	"github.com/infracost/cli/internal/scanner"
	"github.com/infracost/cli/internal/ui"
	"github.com/infracost/cli/internal/vcs"
	"github.com/infracost/cli/pkg/logging"
	"github.com/infracost/proto/gen/go/infracost/parser/event"
	"github.com/infracost/proto/gen/go/infracost/provider"
	"github.com/liamg/tml"
	"github.com/spf13/cobra"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"
)

func Policies(cfg *config.Config) *cobra.Command {

	var finopsOnly, taggingOnly bool
	var providerFilter []string

	cmd := &cobra.Command{
		Use:   "policies",
		Short: "List all available FinOps and tagging policies",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {

			if finopsOnly && taggingOnly {
				return fmt.Errorf("cannot specify both --finops-only and --tagging-only")
			}

			providers, err := resolveProviderFilter(providerFilter, taggingOnly)
			if err != nil {
				return err
			}

			source, err := cfg.Auth.Token(cmd.Context())
			if err != nil {
				return fmt.Errorf("failed to log in: %w", err)
			}

			// default to current working dir
			target := "."
			if len(args) > 0 {
				target = args[0]
			}

			absoluteDirectory, err := filepath.Abs(filepath.Clean(target))
			if err != nil {
				return fmt.Errorf("failed to get absolute path to target: %w", err)
			}

			if info, err := os.Stat(absoluteDirectory); err != nil {
				if os.IsNotExist(err) {
					return fmt.Errorf("target directory does not exist")
				}
				return fmt.Errorf("failed to get info for target directory: %w", err)
			} else if !info.IsDir() {
				// TODO: should probably generate a minimal config for a single project in this case, but for now just require a directory
				return fmt.Errorf("target is not a directory")
			}

			repositoryURL := vcs.GetRemoteURL(absoluteDirectory)
			branchName := vcs.GetCurrentBranch(absoluteDirectory)

			if err := resolveOrg(cmd.Context(), cfg, source); err != nil {
				return err
			}

			client := cfg.Dashboard.Client(api.Client(cmd.Context(), source, cfg.OrgID))
			var runParameters *dashboard.RunParameters
			var finopsPolicies []scanner.FinOpsPolicy
			var taggingPolicies []scanner.TaggingPolicy

			if err := ui.RunWithSpinnerErr(cmd.Context(), "Loading policies...", "Policies loaded", func(ctx context.Context) error {
				if rp, err := client.RunParameters(ctx, repositoryURL, branchName); err != nil {
					logging.Warnf("Failed to fetch runParameters, gathering policies without them: %s", err.Error())
				} else {
					if cfg.Org == "" {
						cfg.OrgID = rp.OrganizationID
					}
					runParameters = &rp
				}

				s := scanner.NewScanner(cfg)
				var listErr error
				finopsPolicies, taggingPolicies, listErr = s.ListPolicies(ctx, runParameters, providers)
				if listErr != nil {
					return fmt.Errorf("failed to list policies: %w", listErr)
				}
				return nil
			}); err != nil {
				return err
			}

			// clean separation from log lines
			fmt.Println()

			if !taggingOnly {
				tml.Printf("<bold><blue><underline>FinOps Policies</underline></blue></bold>\n\n")
				if len(finopsPolicies) == 0 {
					tml.Println("<dim>No FinOps policies found</dim>")
				}
				for _, policy := range finopsPolicies {
					name := policy.Name
					description := policy.Description
					id := policy.Slug
					var branchFilter *event.StringFilter
					var projectFilter *event.StringFilter
					var tagFilter *event.MapFilter
					var customSettings string
					if policy.Settings != nil {
						if policy.Settings.Name != "" {
							name = policy.Settings.Name
						}
						if policy.Settings.Message != "" {
							description = policy.Settings.Message
						}
						branchFilter = policy.Settings.BranchFilter
						projectFilter = policy.Settings.ProjectFilter
						tagFilter = policy.Settings.TagFilter
						id = policy.Settings.Id
						if policy.Settings.Settings != "" {
							var settings any
							if err := json.Unmarshal([]byte(policy.Settings.Settings), &settings); err == nil {
								if customSettingsBytes, err := json.MarshalIndent(settings, "  ", "    "); err == nil {
									customSettings = string(customSettingsBytes)
									if customSettings == "{}" { // ignore empty objects
										customSettings = ""
									}
									customSettings = strings.ReplaceAll(customSettings, "\n", "\n  ")
								}
							}
						}
					}
					tml.Printf("<dim>[</dim><blue>%s</blue><dim>]</dim> <bold>%s</bold> <dim>(%s)</dim>\n", policy.Provider, name, id)
					fmt.Println(description)
					tml.Printf("\n  Applies to:\n")
					tml.Printf("    - %s\n", filterToHumanReadableString("branches", branchFilter))
					tml.Printf("    - %s\n", filterToHumanReadableString("projects", projectFilter))
					tml.Printf("    - %s\n", mapFilterToHumanReadableString("resources", "with tags", tagFilter))
					if customSettings != "" {
						tml.Printf("\n  Custom settings:\n    <cyan>%s</cyan>\n", customSettings)
					}
					fmt.Println()
				}
				fmt.Println()
			}

			if !finopsOnly {
				tml.Printf("<bold><blue><underline>Tagging Policies</underline></blue></bold>\n\n")
				if len(taggingPolicies) == 0 {
					tml.Println("<dim>No tagging policies found</dim>")
				}
				for _, policy := range taggingPolicies {
					tml.Printf("<dim>[</dim><blue>CUSTOM</blue><dim>]</dim> <bold>%s</bold>  <dim>(%s)</dim>\n", policy.Name, policy.Id)
					fmt.Println(policy.Message)

					tml.Printf("\n  Applies to:\n")
					tml.Printf("    - %s\n", filterToHumanReadableString("branches", policy.BranchFilter))
					tml.Printf("    - %s\n", filterToHumanReadableString("projects", policy.ProjectFilter))
					tml.Printf("    - %s\n", filterToHumanReadableString("resources", policy.ResourceFilter))

					tml.Printf("\n  Requirements:\n")
					for _, requirement := range policy.Requirements {

						switch requirement.Mandatory {
						case true:
							tml.Printf("  - Tag <yellow>%s</yellow> must be set. ", requirement.Key)
						case false:
							tml.Printf("  - Tag <yellow>%s</yellow> is not required, but if set: ", requirement.Key)
						}

						switch requirement.Type {
						case event.TagPolicyRequirement_ANY:
							tml.Printf("It may use <italic>any value</italic>.")
						case event.TagPolicyRequirement_REGEX:
							tml.Printf("It may use values matching the regex <blue><italic>%s</italic></blue>.", requirement.ValueRegex)
						case event.TagPolicyRequirement_LIST:
							tml.Printf("It may use <italic>any of the following values</italic>:\n")
							values := requirement.AllowedValues
							if len(values) > 5 {
								values = values[:5]
								tml.Printf("    (showing first 5 of %d values)\n", len(requirement.AllowedValues))
							}
							for _, value := range values {
								tml.Printf("    - %s\n", value)
							}
						default:
							tml.Printf("It has an unknown requirement type %s.", requirement.Type)
						}
						fmt.Println()
					}

					fmt.Println()
				}
				fmt.Println()
			}

			return nil
		},
	}

	cmd.Flags().BoolVarP(&finopsOnly, "finops-only", "f", false, "Only list FinOps policies")
	cmd.Flags().BoolVarP(&taggingOnly, "tagging-only", "t", false, "Only list tagging policies")
	cmd.Flags().StringSliceVarP(&providerFilter, "providers", "p", nil, "Limit FinOps policy lookup to the given providers (aws, azure, google); reduces which provider plugins are downloaded")

	return cmd

}

func resolveProviderFilter(names []string, taggingOnly bool) ([]provider.Provider, error) {
	if taggingOnly {
		return []provider.Provider{}, nil
	}
	if len(names) == 0 {
		return nil, nil
	}
	mapping := map[string]provider.Provider{
		"aws":    provider.Provider_PROVIDER_AWS,
		"azure":  provider.Provider_PROVIDER_AZURERM,
		"google": provider.Provider_PROVIDER_GOOGLE,
	}
	out := make([]provider.Provider, 0, len(names))
	seen := map[provider.Provider]bool{}
	for _, name := range names {
		p, ok := mapping[strings.ToLower(strings.TrimSpace(name))]
		if !ok {
			return nil, fmt.Errorf("unknown provider %q (must be one of: aws, azure, google)", name)
		}
		if seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out, nil
}

func mapFilterToHumanReadableString(plural, requirement string, f *event.MapFilter) string {
	if f == nil || len(f.Include) == 0 && len(f.Exclude) == 0 {
		return tml.Sprintf("<bold>All</bold> %s", plural)
	}
	var matchType string
	var src map[string]string
	if len(f.Include) > 0 {
		matchType = tml.Sprintf("<green>matching</green>")
		src = f.Include
	} else {
		matchType = tml.Sprintf("<red>not matching</red>")
		src = f.Exclude
	}
	var matches []string
	var keys []string
	for k := range src {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := src[k]
		matches = append(matches, tml.Sprintf("<yellow>%s</yellow>=<green>%s</green>", k, v))
	}
	return tml.Sprintf("%s %s %s <bold>all</bold> of %s", cases.Title(language.English).String(plural), requirement, matchType, joinListWithOxfordComma(matches))
}

func filterToHumanReadableString(plural string, f *event.StringFilter) string {

	if f == nil || len(f.Include) == 0 && len(f.Exclude) == 0 {
		return tml.Sprintf("<bold>All</bold> %s", plural)
	}

	coloredIncludes := make([]string, len(f.Include))
	for i, include := range f.Include {
		coloredIncludes[i] = tml.Sprintf("<green>%s</green>", include)
	}
	coloredExcludes := make([]string, len(f.Exclude))
	for i, exclude := range f.Exclude {
		coloredExcludes[i] = tml.Sprintf("<red>%s</red>", exclude)
	}

	if len(f.Exclude) == 0 {
		return tml.Sprintf("<bold>Only</bold> %s %s", joinListWithOxfordComma(coloredIncludes), plural)
	}

	if len(f.Include) == 0 {
		return tml.Sprintf("<bold>All</bold> %s except %s", plural, joinListWithOxfordComma(coloredExcludes))
	}

	return fmt.Sprintf("%s %s; but not %s", joinListWithOxfordComma(coloredIncludes), cases.Title(language.English).String(plural), joinListWithOxfordComma(coloredExcludes))
}

func joinListWithOxfordComma(items []string) string {
	if len(items) == 0 {
		return ""
	}
	if len(items) == 1 {
		return items[0]
	}
	if len(items) == 2 {
		return items[0] + " and " + items[1]
	}
	return strings.Join(items[:len(items)-1], ", ") + ", and " + items[len(items)-1]
}
