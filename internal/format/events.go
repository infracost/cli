package format

import (
	"context"

	"github.com/infracost/cli/internal/api/events"
)

// TrackRun posts an "infracost-run" event with metadata derived from the scan
// output. The following v1 metadata fields are intentionally omitted:
//
// Not yet implemented:
//   - isAWSChina: needs region info from resources.
//   - exampleProjectName: needs pattern matching on project names.
//   - resourceWarnings: needs pricing warnings from providers.
//
// Not applicable to v2:
//   - absHclPercentChange, hclPercentChange, hclProjectRunTimeMs, hclRunTimeMs,
//     tfProjectRunTimeMs, tfRunTimeMs, tfVarPresent: HCL/Terraform timing fields
//     that don't apply to the plugin-based architecture.
//   - terraformBinary, terraformFullVersion, terraformInfracostProviderEnabled,
//     terraformRemoteExecutionModeEnabled: Terraform-specific fields.
//   - ciPercentageThreshold, ciPostCondition, ciScript: CI integration config
//     that isn't part of the v2 CLI.
//   - usageEstimateErrors, usageEstimates, usageSyncs: usage sync fields from
//     the --sync-usage-file flow which doesn't exist in v2.
//   - remediationAttempts, remediationErrors, remediationOpportunities: not yet
//     implemented.
func (o *Output) TrackRun(ctx context.Context, client events.Client, runSeconds float64, outputFormat string, prev *Output) {
	var totalResources int
	var totalSupported int
	var totalNoPrice int
	var totalUnsupported int

	supportedCounts := make(map[string]int)
	unsupportedCounts := make(map[string]int)

	var supportedList []string
	var unsupportedList []string

	for _, p := range o.Projects {
		for _, r := range p.Resources {
			totalResources++

			switch {
			case !r.IsSupported:
				totalUnsupported++
				unsupportedCounts[r.Type]++
				unsupportedList = append(unsupportedList, r.Type)
			case r.IsFree:
				totalNoPrice++
			default:
				totalSupported++
				supportedCounts[r.Type]++
				supportedList = append(supportedList, r.Type)
			}
		}
	}

	extra := []interface{}{
		"runSeconds", runSeconds,
		"outputFormat", outputFormat,
		"projectType", o.projectTypes,
		"totalResources", totalResources,
		"totalSupportedResources", totalSupported,
		"totalNoPriceResources", totalNoPrice,
		"totalUnsupportedResources", totalUnsupported,
		"supportedResourceCounts", supportedCounts,
		"supportedResourcesList", supportedList,
		"unsupportedResourceCounts", unsupportedCounts,
		"unsupportedResourcesList", unsupportedList,
	}

	hasUsageFile := o.estimatedUsageCounts != nil
	extra = append(extra, "hasUsageFile", hasUsageFile)
	if hasUsageFile {
		var totalEstimated int
		var totalUnestimated int
		var estimatedList []string
		var unestimatedList []string

		for key, count := range o.estimatedUsageCounts {
			totalEstimated += count
			for range count {
				estimatedList = append(estimatedList, key)
			}
		}
		for key, count := range o.unestimatedUsageCounts {
			totalUnestimated += count
			for range count {
				unestimatedList = append(unestimatedList, key)
			}
		}

		extra = append(extra,
			"totalEstimatedUsages", totalEstimated,
			"totalUnestimatedUsages", totalUnestimated,
			"estimatedUsageCounts", o.estimatedUsageCounts,
			"estimatedUsageList", estimatedList,
			"unestimatedUsageCounts", o.unestimatedUsageCounts,
			"unestimatedUsageList", unestimatedList,
		)
	}

	if prev != nil {
		diff := computeRunDiff(o, prev)
		extra = append(extra,
			"newResourceCount", diff.newResources,
			"changedResourceCount", diff.changedResources,
			"newIssueCount", diff.newIssues,
			"existingIssueCount", diff.existingIssues,
		)
	}

	client.Push(ctx, "infracost-run", extra...)
}

// TrackDiff compares this output against a previous output and fires a
// "cloud-issue-fixed" event for every policy violation that was present in
// other but is no longer present in this output. Projects are matched by name.
func (o *Output) TrackDiff(ctx context.Context, client events.Client, other *Output) {
	if other == nil {
		return
	}

	// Index previous projects by name for matching.
	prev := make(map[string]*ProjectOutput, len(other.Projects))
	for i := range other.Projects {
		prev[other.Projects[i].ProjectName] = &other.Projects[i]
	}

	for i := range o.Projects {
		p := &o.Projects[i]
		old, ok := prev[p.ProjectName]
		if !ok {
			continue
		}
		trackFinopsDiff(ctx, client, p, old.FinopsResults)
		trackTaggingDiff(ctx, client, p, old.TaggingResults)
	}
}

type runDiffCounts struct {
	newResources     int
	changedResources int
	newIssues        int
	existingIssues   int
}

func computeRunDiff(current, previous *Output) runDiffCounts {
	var counts runDiffCounts

	prev := make(map[string]ProjectOutput, len(previous.Projects))
	for _, p := range previous.Projects {
		prev[p.ProjectName] = p
	}

	for _, p := range current.Projects {
		old, ok := prev[p.ProjectName]
		if !ok {
			// New project — all resources and issues are new.
			counts.newResources += len(p.Resources)
			counts.newIssues += countIssues(p)
			continue
		}

		newRes, changedRes := countResourceDiff(p, old)
		counts.newResources += newRes
		counts.changedResources += changedRes

		newIss, existingIss := countIssueDiff(p, old)
		counts.newIssues += newIss
		counts.existingIssues += existingIss
	}

	return counts
}

func countResourceDiff(current, previous ProjectOutput) (newResources, changedResources int) {
	prevChecksums := make(map[string]string, len(previous.Resources))
	for _, r := range previous.Resources {
		prevChecksums[r.Name] = r.Metadata.DeepChecksum
	}

	for _, r := range current.Resources {
		oldChecksum, ok := prevChecksums[r.Name]
		if !ok {
			newResources++
			continue
		}
		if r.Metadata.DeepChecksum != "" && oldChecksum != "" && r.Metadata.DeepChecksum != oldChecksum {
			changedResources++
		}
	}

	return newResources, changedResources
}

func countIssueDiff(current, previous ProjectOutput) (newIssues, existingIssues int) {
	prevIssues := make(map[string]struct{})

	for _, r := range previous.FinopsResults {
		for _, fr := range r.FailingResources {
			prevIssues[r.PolicySlug+"\x00"+fr.Name] = struct{}{}
		}
	}
	for _, r := range previous.TaggingResults {
		for _, fr := range r.FailingResources {
			prevIssues[r.PolicyID+"\x00"+fr.Address] = struct{}{}
		}
	}

	for _, r := range current.FinopsResults {
		for _, fr := range r.FailingResources {
			key := r.PolicySlug + "\x00" + fr.Name
			if _, ok := prevIssues[key]; ok {
				existingIssues++
			} else {
				newIssues++
			}
		}
	}
	for _, r := range current.TaggingResults {
		for _, fr := range r.FailingResources {
			key := r.PolicyID + "\x00" + fr.Address
			if _, ok := prevIssues[key]; ok {
				existingIssues++
			} else {
				newIssues++
			}
		}
	}

	return newIssues, existingIssues
}

func countIssues(p ProjectOutput) int {
	var total int
	for _, r := range p.FinopsResults {
		total += len(r.FailingResources)
	}
	for _, r := range p.TaggingResults {
		total += len(r.FailingResources)
	}
	return total
}

func trackFinopsDiff(ctx context.Context, client events.Client, p *ProjectOutput, other []FinopsOutput) {
	// Build a set of currently failing (policySlug, resourceName) pairs.
	current := make(map[string]map[string]struct{})
	for _, r := range p.FinopsResults {
		addrs := make(map[string]struct{}, len(r.FailingResources))
		for _, fr := range r.FailingResources {
			addrs[fr.Name] = struct{}{}
		}
		current[r.PolicySlug] = addrs
	}

	repoID, _ := events.GetMetadata[string]("repoId")
	branchID, _ := events.GetMetadata[string]("branchId")
	caller, _ := events.GetMetadata[string]("caller")
	ciPlatform, _ := events.GetMetadata[string]("ciPlatform")
	cliPlatform, _ := events.GetMetadata[string]("cliPlatform")

	for _, prev := range other {
		for _, fr := range prev.FailingResources {
			if cur, ok := current[prev.PolicySlug]; ok {
				if _, still := cur[fr.Name]; still {
					continue
				}
			}
			client.Push(ctx, "cloud-issue-fixed",
				"policyId", prev.PolicyID,
				"policySlug", prev.PolicySlug,
				"type", "finops-policy",
				"projectName", p.ProjectName,
				"resourceAddress", fr.Name,
				"pullRequestId", "",
				"autoFixPullRequest", false,
				"repoId", repoID,
				"branchId", branchID,
				"caller", caller,
				"ciPlatform", ciPlatform,
				"cliPlatform", cliPlatform,
			)
		}
	}
}

func trackTaggingDiff(ctx context.Context, client events.Client, p *ProjectOutput, other []TaggingOutput) {
	// Build a set of currently failing (policyId, resourceAddress) pairs.
	current := make(map[string]map[string]struct{})
	for _, r := range p.TaggingResults {
		addrs := make(map[string]struct{}, len(r.FailingResources))
		for _, fr := range r.FailingResources {
			addrs[fr.Address] = struct{}{}
		}
		current[r.PolicyID] = addrs
	}

	repoID, _ := events.GetMetadata[string]("repoId")
	branchID, _ := events.GetMetadata[string]("branchId")
	caller, _ := events.GetMetadata[string]("caller")
	ciPlatform, _ := events.GetMetadata[string]("ciPlatform")
	cliPlatform, _ := events.GetMetadata[string]("cliPlatform")

	for _, prev := range other {
		for _, fr := range prev.FailingResources {
			if cur, ok := current[prev.PolicyID]; ok {
				if _, still := cur[fr.Address]; still {
					continue
				}
			}
			client.Push(ctx, "cloud-issue-fixed",
				"policyId", prev.PolicyID,
				"type", "tag-policy",
				"projectName", p.ProjectName,
				"resourceAddress", fr.Address,
				"pullRequestId", "",
				"autoFixPullRequest", false,
				"repoId", repoID,
				"branchId", branchID,
				"caller", caller,
				"ciPlatform", ciPlatform,
				"cliPlatform", cliPlatform,
			)
		}
	}
}
