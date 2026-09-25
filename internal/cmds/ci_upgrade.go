package cmds

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// ciUpgrader is implemented by writers that can replace a hand-written
// Infracost job with the managed block.
type ciUpgrader interface {
	Upgrades(repoRoot string) []ciLegacyJob
}

// ciJobShape says where a platform keeps its jobs, which is what the classifier
// enumerates and the remover cuts. A platform without an upgrade path is
// ciJobsNone and nothing about it is read.
type ciJobShape int

const (
	ciJobsNone ciJobShape = iota
	ciJobsTopLevel
	ciJobsSequence
)

// ciSequenceKeys are the Azure keys a job can sit under. Both are read whatever
// azurePlacementKey picks, so a legacy job under jobs: is still found when the
// block is going under stages:.
var ciSequenceKeys = []string{"jobs", "stages"}

// ciGitlabReserved are the top-level GitLab keywords that configure the
// pipeline rather than declare a job, so the classifier never reads one as one.
var ciGitlabReserved = map[string]bool{
	"default": true, "include": true, "stages": true, "variables": true,
	"workflow": true, "image": true, "services": true, "cache": true,
	"before_script": true, "after_script": true,
}

// ciInfracostSignals are the strings a copy-pasted Infracost recipe keeps
// whatever else the user changed about it: the image it runs, the command it
// runs, and the Azure task. A substring scan, not a YAML walk — the published
// recipes vary in shape and a substring is what a copy-paste preserves.
var ciInfracostSignals = []string{
	"ghcr.io/infracost/ci",
	"infracost/infracost",
	"infracost-ci ",
	"infracost breakdown",
	"infracost diff",
	"infracost comment",
	"infracost upload",
	"InfracostSetup@",
}

// ciLegacyJob is a job outside the managed block, with the line range removing
// it would take.
type ciLegacyJob struct {
	name  string
	line  int    // 1-indexed, where the job is declared
	start int    // 0-indexed, first line the removal takes
	end   int    // 0-indexed, one past the last line the removal takes
	text  string // the job's own lines, read for the notes below
	mixed bool   // an Azure stage that also holds jobs we did not publish
}

// ciJobScan splits the jobs outside the managed block by what is to be done
// with them: ours by name and by content are replaced, something else that
// runs Infracost is only warned about.
type ciJobScan struct {
	replace []ciLegacyJob
	warn    []ciLegacyJob
}

// ciScanJobs classifies every job outside the managed block on two axes: is it
// named like ours, and does its content run Infracost. A job named infracost*
// with no content signal is something we did not publish, so it is left for
// ciExistingInfracostJobs to refuse rather than deleted on a guess. A stage
// holding the user's own jobs is left to the same refusal: it is removed whole.
func ciScanJobs(content string, shape ciJobShape) ciJobScan {
	var scan ciJobScan
	for _, j := range ciEnumerateJobs(content, shape) {
		if !ciTextRunsInfracost(j.text) {
			continue
		}
		if !j.mixed && strings.HasPrefix(strings.ToLower(j.name), "infracost") {
			scan.replace = append(scan.replace, j)
			continue
		}
		scan.warn = append(scan.warn, j)
	}
	return scan
}

// ciTextRunsInfracost reports whether text runs Infracost, by the signals a
// copy-paste of a published recipe preserves.
func ciTextRunsInfracost(text string) bool {
	for _, s := range ciInfracostSignals {
		if strings.Contains(text, s) {
			return true
		}
	}
	return false
}

// ciEnumerateJobs returns every job outside the managed block, whether or not
// it has anything to do with Infracost.
func ciEnumerateJobs(content string, shape ciJobShape) []ciLegacyJob {
	if shape == ciJobsNone {
		return nil
	}

	doc, ok := ciDocMapping(content)
	if !ok {
		return nil
	}

	lines := strings.Split(content, "\n")
	spanStart, spanEnd, hasBlock := findCIManagedSpan(lines)
	inBlock := func(line int) bool { return hasBlock && line-1 >= spanStart && line-1 < spanEnd }

	var jobs []ciLegacyJob
	if shape == ciJobsTopLevel {
		for i := 0; i+1 < len(doc.Content); i += 2 {
			k, v := doc.Content[i], doc.Content[i+1]
			// A hidden key is a template other jobs extend from, not a job.
			if ciGitlabReserved[k.Value] || strings.HasPrefix(k.Value, ".") || v.Kind != yaml.MappingNode || inBlock(k.Line) {
				continue
			}
			// Everything below is line-based, so a job that does not start its
			// own line — a flow mapping — has no range that can be cut.
			if !ciStartsKey(lines[k.Line-1], k.Value) {
				continue
			}
			jobs = append(jobs, ciJobAt(lines, k.Value, k.Line, ciNextTopLevelLine(doc, i, lines), 0))
		}
		return jobs
	}

	for _, key := range ciSequenceKeys {
		seq := ciNodeUnderKey(doc, key)
		if seq == nil || seq.Kind != yaml.SequenceNode {
			continue
		}
		keyLine, tail := ciTopLevelKeyLines(doc, key, lines)
		for i, item := range seq.Content {
			next := tail
			if i+1 < len(seq.Content) {
				next = seq.Content[i+1].Line
			}
			if item.Kind != yaml.MappingNode || inBlock(item.Line) || !ciStartsSeqItem(lines, item.Line) {
				continue
			}
			job := ciJobAt(lines, ciSeqItemName(item), item.Line, next, keyLine)
			job.mixed = ciStageHoldsForeignJobs(item)
			jobs = append(jobs, job)
		}
	}
	return jobs
}

// ciStartsKey reports whether line opens the top-level mapping entry for key.
// ciKeyRe is no help here: a GitLab job name carries colons of its own.
func ciStartsKey(line, key string) bool {
	if leadingSpaces(line) != 0 {
		return false
	}
	return strings.HasPrefix(line, key) || strings.HasPrefix(line, `"`+key) || strings.HasPrefix(line, "'"+key)
}

// ciStartsSeqItem reports whether line opens its own sequence entry, which a
// flow sequence written on one line does not: there is no range to cut there.
func ciStartsSeqItem(lines []string, line int) bool {
	return line-1 < len(lines) && strings.HasPrefix(strings.TrimLeft(lines[line-1], " "), "- ")
}

// ciSeqItemName reads the name Azure gives a sequence entry, by the fields it
// names a job, stage or deployment with.
func ciSeqItemName(item *yaml.Node) string {
	for i := 0; i+1 < len(item.Content); i += 2 {
		switch item.Content[i].Value {
		case "job", "stage", "deployment":
			return item.Content[i+1].Value
		}
	}
	return ""
}

// ciStageHoldsForeignJobs reports whether an Azure stage nests a job we did not
// publish. The stage is the unit the removal cuts, so replacing one would take
// the user's jobs with it while the plan named only the stage.
func ciStageHoldsForeignJobs(item *yaml.Node) bool {
	jobs := ciNodeUnderKey(item, "jobs")
	if jobs == nil || jobs.Kind != yaml.SequenceNode {
		return false
	}
	for _, j := range jobs.Content {
		if !strings.HasPrefix(strings.ToLower(ciSeqItemName(j)), "infracost") {
			return true
		}
	}
	return false
}

// ciJobAt builds the removal range for a job declared at line and running to
// nextLine, with floor the 1-indexed line the range may not climb above.
func ciJobAt(lines []string, name string, line, nextLine, floor int) ciLegacyJob {
	start := line - 1
	end := min(max(nextLine-1, start), len(lines))

	indent := leadingSpaces(lines[start])
	// A comment block above the next sibling introduces it, not us, so hand
	// those lines back. Whatever blank lines separate it from the job go with
	// the job, or two removed neighbours leave a stack of them.
	tail := end
	for tail > start && strings.TrimSpace(lines[tail-1]) == "" {
		tail--
	}
	if tail > start && ciIsSiblingComment(lines[tail-1], indent) {
		for tail > start && ciIsSiblingComment(lines[tail-1], indent) {
			tail--
		}
		end = tail
	}
	// Read after the trim above: a signal in the next sibling's comment block
	// would otherwise classify — and delete — a job that carries none itself.
	text := strings.Join(lines[start:end], "\n")
	// The recipe's own commentary above the job comes with it: left behind it
	// describes nothing.
	for start > floor && ciIsSiblingComment(lines[start-1], indent) {
		start--
	}

	return ciLegacyJob{name: name, line: line, start: start, end: end, text: text}
}

// ciIsSiblingComment reports whether line is a comment written at or to the
// left of a job at indent, which is where a job's own commentary sits.
func ciIsSiblingComment(line string, indent int) bool {
	return strings.HasPrefix(strings.TrimSpace(line), "#") && leadingSpaces(line) <= indent
}

// ciRemoveJobs cuts jobs out of content. A key the removal empties goes too,
// unless the block is about to be spliced back into it: an empty jobs: is a
// null the platform rejects.
func ciRemoveJobs(content string, jobs []ciLegacyJob, shape ciJobShape, placementKey string) string {
	if len(jobs) == 0 {
		return content
	}

	lines := strings.Split(content, "\n")
	drop := make([]bool, len(lines))
	for _, j := range jobs {
		for i := j.start; i < j.end && i < len(lines); i++ {
			drop[i] = true
		}
	}

	if shape == ciJobsTopLevel {
		ciDropGitlabStages(content, lines, drop)
	}
	ciDropEmptiedKeys(content, lines, drop, placementKey)

	out := make([]string, 0, len(lines))
	for i, l := range lines {
		if !drop[i] {
			out = append(out, l)
		}
	}
	joined := strings.Join(out, "\n")
	if strings.TrimSpace(joined) == "" {
		return ""
	}
	return strings.TrimRight(joined, "\n") + "\n"
}

// ciDropGitlabStages removes the stages: entries the removed jobs declared. A
// stage a surviving job still names stays whatever it is called: without its
// entry GitLab rejects the pipeline with "chosen stage does not exist".
func ciDropGitlabStages(content string, lines []string, drop []bool) {
	doc, ok := ciDocMapping(content)
	if !ok {
		return
	}
	seq := ciNodeUnderKey(doc, "stages")
	if seq == nil || seq.Kind != yaml.SequenceNode {
		return
	}
	removed, kept := ciGitlabStageUse(doc, drop)
	for _, item := range seq.Content {
		if item.Kind != yaml.ScalarNode || !strings.HasPrefix(strings.ToLower(item.Value), "infracost") {
			continue
		}
		if !removed[item.Value] || kept[item.Value] {
			continue
		}
		if ciStartsSeqItem(lines, item.Line) && item.Line-1 < len(drop) {
			drop[item.Line-1] = true
		}
	}
}

// ciGitlabStageUse splits the stage: values the top-level jobs declare by
// whether the job declaring one is being removed. A job with no stage: runs in
// GitLab's default stage, which is never one of ours.
func ciGitlabStageUse(doc *yaml.Node, drop []bool) (removed, kept map[string]bool) {
	removed, kept = map[string]bool{}, map[string]bool{}
	for i := 0; i+1 < len(doc.Content); i += 2 {
		k, v := doc.Content[i], doc.Content[i+1]
		if v.Kind != yaml.MappingNode {
			continue
		}
		stage := ciNodeUnderKey(v, "stage")
		if stage == nil || stage.Kind != yaml.ScalarNode {
			continue
		}
		if k.Line-1 < len(drop) && drop[k.Line-1] {
			removed[stage.Value] = true
			continue
		}
		kept[stage.Value] = true
	}
	return removed, kept
}

// ciDropEmptiedKeys removes a top-level sequence key whose every entry is being
// dropped. placementKey is spared: the block goes straight back into it.
func ciDropEmptiedKeys(content string, lines []string, drop []bool, placementKey string) {
	doc, ok := ciDocMapping(content)
	if !ok {
		return
	}

	for i := 0; i+1 < len(doc.Content); i += 2 {
		k, v := doc.Content[i], doc.Content[i+1]
		if k.Value == placementKey || v.Kind != yaml.SequenceNode || len(v.Content) == 0 {
			continue
		}
		emptied := true
		for _, item := range v.Content {
			if item.Line-1 >= len(drop) || !drop[item.Line-1] {
				emptied = false
				break
			}
		}
		if !emptied {
			continue
		}
		r := ciJobAt(lines, k.Value, k.Line, ciNextTopLevelLine(doc, i, lines), 0)
		for j := r.start; j < r.end && j < len(drop); j++ {
			drop[j] = true
		}
	}
}

// ciJobsCover reports whether a 1-indexed line falls inside a job already
// slated for removal, so the FIX-740 refusal does not fire on one.
func ciJobsCover(jobs []ciLegacyJob, line int) bool {
	for _, j := range jobs {
		if line-1 >= j.start && line-1 < j.end {
			return true
		}
	}
	return false
}

func ciJobNames(jobs []ciLegacyJob) []string {
	names := make([]string, 0, len(jobs))
	for _, j := range jobs {
		names = append(names, j.name)
	}
	return names
}

// ciPlannedUpgrades lists what a write to relPath would replace.
//
// It runs ciCanPlace only. ciCanReplaceSpan asks whether the managed span holds
// only keys the body defines, and with no rendered body to compare against
// every key in an existing block reads as foreign: the plan would list nothing
// and the write would then remove a job the user was never shown. Over-listing
// is safe — the write reports why it left the file alone and removes nothing.
func ciPlannedUpgrades(repoRoot, relPath string, shape ciJobShape) []ciLegacyJob {
	b, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(relPath))) //nolint:gosec // G304: relPath is a registered config path under the repo root
	if err != nil {
		return nil
	}
	if ciCanPlace(string(b)) != nil {
		return nil
	}
	return ciScanJobs(string(b), shape).replace
}

// ciLegacyAPIKeyVar and ciLegacyTokenVar are the names the v0.1 Azure recipe
// read its secrets under. Nothing renames a pipeline variable for the user, so
// finding one in a replaced job is a note.
const (
	ciLegacyAPIKeyVar = "$(infracostApiKey)" //nolint:gosec // G101: a pipeline variable reference, not a credential
	ciLegacyTokenVar  = "$(githubToken)"     //nolint:gosec // G101: a pipeline variable reference, not a credential
)

var (
	// ciLegacyPathRe reads the scope off the command line rather than off
	// TF_ROOT: a hand-tuned job may scan any subdirectory, and silently
	// rescoping it to the repository root is the quietest way this can be wrong.
	ciLegacyPathRe = regexp.MustCompile(`--path[= ]\s*["']?([^\s"',]+)`)
)

// ciUpgradeNotes are the things the replaced jobs did that the managed block
// does not carry over.
func ciUpgradeNotes(jobs []ciLegacyJob) []string {
	var text strings.Builder
	for _, j := range jobs {
		text.WriteString(j.text)
		text.WriteString("\n")
	}
	removed := text.String()

	var notes []string
	if strings.Contains(removed, ciLegacyAPIKeyVar) {
		notes = append(notes, fmt.Sprintf(
			"The replaced job read %s; the new one reads $(%s).\nRename the pipeline variable, or add it.",
			ciLegacyAPIKeyVar, ciAPIKeySecret))
	}
	if strings.Contains(removed, ciLegacyTokenVar) {
		notes = append(notes, fmt.Sprintf(
			"The replaced job read %s; the new one reads $(GITHUB_TOKEN).\nRename the pipeline variable, or add it.",
			ciLegacyTokenVar))
	}
	for _, p := range ciLegacyPaths(removed) {
		notes = append(notes, fmt.Sprintf(
			"The replaced job scanned %s; the new one scans the repository root.\nAdd --path to the infracost-ci commands to keep that scope.", p))
	}
	return notes
}

// ciLegacyPaths returns the --path scopes worth a note. The default is not one,
// and neither is /tmp: every recipe clones the base branch there rather than
// choosing it as a scope.
func ciLegacyPaths(text string) []string {
	seen := map[string]bool{}
	var paths []string
	for _, m := range ciLegacyPathRe.FindAllStringSubmatch(text, -1) {
		// ${TF_ROOT} renders as $TF_ROOT, so the note names the variable the
		// user recognises.
		p := strings.NewReplacer("${", "$", "}", "").Replace(m[1])
		if p == "" || p == "." || p == "./" || strings.HasPrefix(p, "/tmp") || seen[p] {
			continue
		}
		seen[p] = true
		paths = append(paths, p)
	}
	return paths
}

// ciDuplicateJobWarning names a job that also runs Infracost and is left in
// place, because it is not one we published and deleting it would be guessing.
func ciDuplicateJobWarning(name, relPath string) string {
	// An Azure template entry declares no job, stage or deployment to name.
	if name == "" {
		name = "A job"
	}
	return fmt.Sprintf(
		"%s in %s also runs Infracost.\n     Two jobs means two comments on every pull request — remove it once the new one works.",
		name, relPath)
}

// ciDuplicateWorkflowWarning is the same warning for a whole workflow file,
// which has no one job to name.
func ciDuplicateWorkflowWarning(relPath string) string {
	return fmt.Sprintf(
		"%s also runs Infracost.\n     Two workflows means two comments on every pull request — remove it once the new ones work.",
		relPath)
}

// ciForeignWorkflowWarnings names the other GitHub workflows that run
// Infracost. They are never deleted: a workflow's name may be pinned in branch
// protection, and a check that disappears blocks every merge.
func ciForeignWorkflowWarnings(repoRoot string) []string {
	ours := map[string]bool{githubDiffWorkflowPath: true, githubScanWorkflowPath: true}

	var warnings []string
	for _, pattern := range []string{"*.yml", "*.yaml"} {
		hits, err := filepath.Glob(filepath.Join(repoRoot, ".github", "workflows", pattern))
		if err != nil {
			continue
		}
		for _, hit := range hits {
			rel := ".github/workflows/" + filepath.Base(hit)
			if ours[rel] {
				continue
			}
			b, err := os.ReadFile(hit) //nolint:gosec // G304: hit comes from a glob under the repo root
			if err != nil {
				continue
			}
			if !ciTextRunsInfracost(string(b)) && !strings.Contains(string(b), "infracost/actions/") {
				continue
			}
			warnings = append(warnings, ciDuplicateWorkflowWarning(rel))
		}
	}
	sort.Strings(warnings)
	return warnings
}

// ciFileMarkerRe matches the marker at any version, so a file written by an
// older CLI still reads as ours.
var ciFileMarkerRe = regexp.MustCompile(`^\s*#\s*Managed by infracost ci setup\b`)

// ciFilesAreManaged reports whether every one of paths that exists carries the
// file marker, which makes a re-run an update rather than an overwrite worth
// prompting about.
func ciFilesAreManaged(repoRoot string, paths []string) bool {
	for _, p := range paths {
		abs := filepath.Join(repoRoot, filepath.FromSlash(p))
		if !fileExists(abs) {
			continue
		}
		b, err := os.ReadFile(abs) //nolint:gosec // G304: p is a registered config path under the repo root
		if err != nil || !ciHasFileMarker(string(b)) {
			return false
		}
	}
	return true
}

// ciHasFileMarker reads the head of the file only: the writer puts the marker
// under the workflow's name:, and anything deeper is the user's own text.
func ciHasFileMarker(content string) bool {
	for i, l := range strings.Split(content, "\n") {
		if i >= 5 {
			return false
		}
		if ciFileMarkerRe.MatchString(l) {
			return true
		}
	}
	return false
}

// ciDocMapping returns the first document's top-level mapping.
func ciDocMapping(content string) (*yaml.Node, bool) {
	var root yaml.Node
	if err := yaml.Unmarshal([]byte(content), &root); err != nil || len(root.Content) == 0 {
		return nil, false
	}
	doc := root.Content[0]
	if doc.Kind != yaml.MappingNode {
		return nil, false
	}
	return doc, true
}

// ciNextTopLevelLine returns the 1-indexed line of the top-level key after the
// one at index i, or one past the last line when it is the final key.
func ciNextTopLevelLine(doc *yaml.Node, i int, lines []string) int {
	if i+2 < len(doc.Content) {
		return doc.Content[i+2].Line
	}
	return len(lines) + 1
}

// ciTopLevelKeyLines returns the 1-indexed line of key and of whatever top-level
// key follows it.
func ciTopLevelKeyLines(doc *yaml.Node, key string, lines []string) (keyLine, nextLine int) {
	for i := 0; i+1 < len(doc.Content); i += 2 {
		if doc.Content[i].Value == key {
			return doc.Content[i].Line, ciNextTopLevelLine(doc, i, lines)
		}
	}
	return 0, len(lines) + 1
}
