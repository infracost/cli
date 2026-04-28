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
	"github.com/spf13/cobra"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"
)

func Policies(cfg *config.Config) *cobra.Command {

	var finopsOnly, taggingOnly bool

	cmd := &cobra.Command{
		Use:   "policies",
		Short: "List all available FinOps and tagging policies",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {

			if finopsOnly && taggingOnly {
				return fmt.Errorf("cannot specify both --finops-only and --tagging-only")
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
				finopsPolicies, taggingPolicies, listErr = s.ListPolicies(ctx, runParameters)
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
				ui.Heading("FinOps Policies")
				fmt.Println()
				if len(finopsPolicies) == 0 {
					fmt.Println(ui.Muted("No FinOps policies found"))
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
					fmt.Printf("%s%s%s %s %s\n", ui.Muted("["), ui.Accent(policy.Provider), ui.Muted("]"), ui.Bold(ui.Accent(name)), ui.Mutedf("(%s)", id))
					fmt.Println(description)
					fmt.Printf("\n  %s\n", ui.Bold(ui.Muted("Applies to")))
					fmt.Printf("    - %s\n", filterToHumanReadableString("branches", branchFilter))
					fmt.Printf("    - %s\n", filterToHumanReadableString("projects", projectFilter))
					fmt.Printf("    - %s\n", mapFilterToHumanReadableString("resources", "with tags", tagFilter))
					if customSettings != "" {
						fmt.Printf("\n  %s\n    %s\n", ui.Bold(ui.Muted("Custom settings")), ui.Code(customSettings))
					}
					fmt.Println()
				}
				fmt.Println()
			}

			if !finopsOnly {
				ui.Heading("Tagging Policies")
				fmt.Println()
				if len(taggingPolicies) == 0 {
					fmt.Println(ui.Muted("No tagging policies found"))
				}
				for _, policy := range taggingPolicies {
					fmt.Printf("%s%s%s %s  %s\n", ui.Muted("["), ui.Accent("CUSTOM"), ui.Muted("]"), ui.Bold(ui.Accent(policy.Name)), ui.Mutedf("(%s)", policy.Id))
					fmt.Println(policy.Message)

					fmt.Printf("\n  %s\n", ui.Bold(ui.Muted("Applies to")))
					fmt.Printf("    - %s\n", filterToHumanReadableString("branches", policy.BranchFilter))
					fmt.Printf("    - %s\n", filterToHumanReadableString("projects", policy.ProjectFilter))
					fmt.Printf("    - %s\n", filterToHumanReadableString("resources", policy.ResourceFilter))

					fmt.Printf("\n  %s\n", ui.Bold(ui.Muted("Requirements")))
					for _, requirement := range policy.Requirements {

						switch requirement.Mandatory {
						case true:
							fmt.Printf("  - Tag %s must be set. ", ui.Accent(requirement.Key))
						case false:
							fmt.Printf("  - Tag %s is not required, but if set: ", ui.Accent(requirement.Key))
						}

						switch requirement.Type {
						case event.TagPolicyRequirement_ANY:
							fmt.Printf("It may use %s.", ui.Italic("any value"))
						case event.TagPolicyRequirement_REGEX:
							fmt.Printf("It may use values matching the regex %s.", ui.Code(requirement.ValueRegex))
						case event.TagPolicyRequirement_LIST:
							fmt.Printf("It may use %s:\n", ui.Italic("any of the following values"))
							values := requirement.AllowedValues
							if len(values) > 5 {
								values = values[:5]
								fmt.Printf("    (showing first 5 of %d values)\n", len(requirement.AllowedValues))
							}
							for _, value := range values {
								fmt.Printf("    - %s\n", value)
							}
						default:
							fmt.Printf("It has an unknown requirement type %s.", requirement.Type)
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

	return cmd

}

func mapFilterToHumanReadableString(plural, requirement string, f *event.MapFilter) string {
	if f == nil || len(f.Include) == 0 && len(f.Exclude) == 0 {
		return fmt.Sprintf("%s %s", ui.Bold("All"), plural)
	}
	var matchType string
	var src map[string]string
	if len(f.Include) > 0 {
		matchType = ui.Positive("matching")
		src = f.Include
	} else {
		matchType = ui.Danger("not matching")
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
		matches = append(matches, fmt.Sprintf("%s=%s", ui.Accent(k), ui.Positive(v)))
	}
	return fmt.Sprintf("%s %s %s %s of %s", cases.Title(language.English).String(plural), requirement, matchType, ui.Bold("all"), joinListWithOxfordComma(matches))
}

func filterToHumanReadableString(plural string, f *event.StringFilter) string {

	if f == nil || len(f.Include) == 0 && len(f.Exclude) == 0 {
		return fmt.Sprintf("%s %s", ui.Bold("All"), plural)
	}

	coloredIncludes := make([]string, len(f.Include))
	for i, include := range f.Include {
		coloredIncludes[i] = ui.Positive(include)
	}
	coloredExcludes := make([]string, len(f.Exclude))
	for i, exclude := range f.Exclude {
		coloredExcludes[i] = ui.Danger(exclude)
	}

	if len(f.Exclude) == 0 {
		return fmt.Sprintf("%s %s %s", ui.Bold("Only"), joinListWithOxfordComma(coloredIncludes), plural)
	}

	if len(f.Include) == 0 {
		return fmt.Sprintf("%s %s except %s", ui.Bold("All"), plural, joinListWithOxfordComma(coloredExcludes))
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
