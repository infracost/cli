package cmds

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

const testJobBlock = "infracost:\n  image: " + ciImage + "\n  script:\n    - infracost-ci scan --path .\n"

func TestCIPlaceBlock_TopLevelIsIdempotent(t *testing.T) {
	root := t.TempDir()

	res, err := ciPlaceBlock(root, ".gitlab-ci.yml", "", testJobBlock, ciJobsTopLevel)
	require.NoError(t, err)
	assert.True(t, res.created)
	fresh := readFile(t, root, ".gitlab-ci.yml")
	assert.Contains(t, fresh, ciImage)
	assert.Equal(t, ciBlockVersion, ciConfigVersion(fresh))

	// Into a file that already has unrelated jobs.
	root = t.TempDir()
	existing := "stages:\n  - test\n\nunit-tests:\n  script:\n    - make test\n"
	writeFile(t, root, ".gitlab-ci.yml", existing)

	res, err = ciPlaceBlock(root, ".gitlab-ci.yml", "", testJobBlock, ciJobsTopLevel)
	require.NoError(t, err)
	assert.False(t, res.created)
	second := readFile(t, root, ".gitlab-ci.yml")
	assert.Contains(t, second, existing)
	requireParses(t, second)

	// Re-running replaces the managed span rather than appending a second job.
	res, err = ciPlaceBlock(root, ".gitlab-ci.yml", "", testJobBlock, ciJobsTopLevel)
	require.NoError(t, err)
	assert.True(t, res.unchanged)
	assert.Equal(t, second, readFile(t, root, ".gitlab-ci.yml"))
}

func TestCIPlaceBlock_UnderKeyUsesChildIndentation(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "bitbucket-pipelines.yml", `image: atlassian/default-image:4

pipelines:
  branches:
    main:
      - step:
          name: Build
          script:
            - make build
`)

	body := "pull-requests:\n  '**':\n    - step:\n        name: Infracost\n        image: " + ciImage + "\n"
	_, err := ciPlaceBlock(root, "bitbucket-pipelines.yml", "pipelines", body, ciJobsNone)
	require.NoError(t, err)

	got := readFile(t, root, "bitbucket-pipelines.yml")
	assert.Contains(t, got, "\n  "+ciBlockStart+"\n")
	assert.Contains(t, got, "\n  pull-requests:\n")
	requireParses(t, got)

	var parsed struct {
		Pipelines map[string]any `yaml:"pipelines"`
	}
	require.NoError(t, yaml.Unmarshal([]byte(got), &parsed))
	assert.Contains(t, parsed.Pipelines, "pull-requests")
	assert.Contains(t, parsed.Pipelines, "branches")

	// A second run replaces the nested span at the same indentation.
	_, err = ciPlaceBlock(root, "bitbucket-pipelines.yml", "pipelines", body, ciJobsNone)
	require.NoError(t, err)
	assert.Equal(t, got, readFile(t, root, "bitbucket-pipelines.yml"))
}

func TestCIPlaceBlock_CreatesMissingKey(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "bitbucket-pipelines.yml", "image: atlassian/default-image:4\n")

	_, err := ciPlaceBlock(root, "bitbucket-pipelines.yml", "pipelines", "pull-requests:\n  '**': []\n", ciJobsNone)
	require.NoError(t, err)

	got := readFile(t, root, "bitbucket-pipelines.yml")
	assert.Contains(t, got, "\npipelines:\n")
	requireParses(t, got)
}

// A job named after us that runs something else is not a recipe we published,
// so deleting it would be guessing and the refusal stands.
func TestCIPlaceBlock_RefusesAnUnrecognisedInfracostJob(t *testing.T) {
	root := t.TempDir()
	handWritten := "infracost:\n  image: alpine\n  script:\n    - ./scripts/cost-audit.sh\n"
	writeFile(t, root, ".gitlab-ci.yml", handWritten)

	_, err := ciPlaceBlock(root, ".gitlab-ci.yml", "", testJobBlock, ciJobsTopLevel)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `already defines "infracost" at line 1`)
	assert.Equal(t, handWritten, readFile(t, root, ".gitlab-ci.yml"))
}

// A variable named INFRACOST_API_KEY is not a job, so it must not trip the gate.
func TestCIPlaceBlock_AllowsInfracostVariables(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, ".gitlab-ci.yml", "variables:\n  INFRACOST_API_KEY: $INFRACOST_API_KEY\n")

	_, err := ciPlaceBlock(root, ".gitlab-ci.yml", "", testJobBlock, ciJobsTopLevel)
	require.NoError(t, err)
	requireParses(t, readFile(t, root, ".gitlab-ci.yml"))
}

// The gate looks at the levels the block goes into, not every mapping in the
// file: a nested infracost-prefixed key is not a job.
func TestCIPlaceBlock_AllowsNestedInfracostKeys(t *testing.T) {
	tests := map[string]string{
		"nested settings mapping": "build:\n  variables:\n    infracost_settings:\n      enabled: true\n",
		"gitlab extended variable": "variables:\n  INFRACOST_API_KEY:\n    value: \"\"\n" +
			"    description: \"API key for Infracost\"\n",
	}

	for name, content := range tests {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			writeFile(t, root, ".gitlab-ci.yml", content)

			res, err := ciPlaceBlock(root, ".gitlab-ci.yml", "", testJobBlock, ciJobsTopLevel)
			require.NoError(t, err)
			assert.Empty(t, res.block)
			got := readFile(t, root, ".gitlab-ci.yml")
			assert.Contains(t, got, ciImage)
			requireParses(t, got)
		})
	}
}

// A job under the key we splice into is still a duplicate, so it is refused.
func TestCIPlaceBlock_RefusesAJobUnderTheTargetKey(t *testing.T) {
	root := t.TempDir()
	existing := "pipelines:\n  infracost:\n    - step:\n        script:\n          - echo hi\n"
	writeFile(t, root, "bitbucket-pipelines.yml", existing)

	_, err := ciPlaceBlock(root, "bitbucket-pipelines.yml", "pipelines", "pull-requests:\n  '**': []\n", ciJobsNone)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `already defines "infracost"`)
	assert.Equal(t, existing, readFile(t, root, "bitbucket-pipelines.yml"))
}

// Azure's jobs: and stages: are sequences, so the gate has to read the entry's
// own name rather than a mapping key.
func TestCIPlaceBlock_RefusesAnExistingJobInASequence(t *testing.T) {
	for _, tt := range []struct {
		key      string
		existing string
		want     string
	}{
		{"jobs", "jobs:\n  - job: infracost_diff\n    steps:\n      - script: echo hi\n", "infracost_diff"},
		{"stages", "stages:\n  - stage: infracost\n    jobs: []\n", "infracost"},
	} {
		t.Run(tt.key, func(t *testing.T) {
			root := t.TempDir()
			writeFile(t, root, "azure-pipelines.yml", tt.existing)

			_, err := ciPlaceBlock(root, "azure-pipelines.yml", tt.key, "- job: infracost_scan\n  steps: []\n", ciJobsSequence)
			require.Error(t, err)
			assert.Contains(t, err.Error(), `already defines "`+tt.want+`"`)
			assert.Equal(t, tt.existing, readFile(t, root, "azure-pipelines.yml"))
		})
	}
}

// A hand-removed end sentinel would append a second copy of the block; the
// result must not reach disk.
func TestCIPlaceBlock_RefusesAResultThatWouldNotParse(t *testing.T) {
	root := t.TempDir()
	body := "pull-requests:\n  '**': []\n"

	_, err := ciPlaceBlock(root, "bitbucket-pipelines.yml", "pipelines", body, ciJobsNone)
	require.NoError(t, err)

	// Drop the closing sentinel, as a merge resolution would.
	written := readFile(t, root, "bitbucket-pipelines.yml")
	mangled := strings.ReplaceAll(written, "  "+ciBlockEnd+"\n", "")
	writeFile(t, root, "bitbucket-pipelines.yml", mangled)

	res, err := ciPlaceBlock(root, "bitbucket-pipelines.yml", "pipelines", body, ciJobsNone)
	require.NoError(t, err)
	assert.Contains(t, res.reason, "valid YAML")
	assert.Contains(t, res.block, "pull-requests")
	assert.False(t, res.created)
	assert.Equal(t, mangled, readFile(t, root, "bitbucket-pipelines.yml"))
}

// A merge that kept the start marker from one side and the end marker from the
// other encloses the user's jobs; replacing that span would delete them.
func TestCIPlaceBlock_RefusesASpanHoldingForeignKeys(t *testing.T) {
	root := t.TempDir()
	crossed := ciBlockStart + "\nunit-tests:\n  script:\n    - make test\n" +
		"infracost:\n  image: " + ciImage + "\n" + ciBlockEnd + "\n"
	writeFile(t, root, ".gitlab-ci.yml", crossed)

	res, err := ciPlaceBlock(root, ".gitlab-ci.yml", "", testJobBlock, ciJobsTopLevel)
	require.NoError(t, err)
	assert.Contains(t, res.reason, "unit-tests")
	assert.Contains(t, res.block, ciImage)
	assert.Equal(t, crossed, readFile(t, root, ".gitlab-ci.yml"))
}

// A marker echoed inside a block scalar mispairs the span, so the file is left
// alone rather than spliced by line.
func TestCIPlaceBlock_RefusesDuplicateMarkers(t *testing.T) {
	root := t.TempDir()
	dup := "build:\n  script: |\n    " + ciBlockStart + "\n    make build\n\n" + ciManagedBlock(testJobBlock)
	writeFile(t, root, ".gitlab-ci.yml", dup)

	res, err := ciPlaceBlock(root, ".gitlab-ci.yml", "", testJobBlock, ciJobsTopLevel)
	require.NoError(t, err)
	assert.Contains(t, res.reason, "ambiguous")
	assert.Equal(t, dup, readFile(t, root, ".gitlab-ci.yml"))
}

// The splice appends to the last document while every check reads the first,
// so a multi-document file is not one we can edit.
func TestCIPlaceBlock_RefusesMultipleDocuments(t *testing.T) {
	root := t.TempDir()
	multi := "stages:\n  - test\n---\ninfracost:\n  image: alpine\n"
	writeFile(t, root, ".gitlab-ci.yml", multi)

	res, err := ciPlaceBlock(root, ".gitlab-ci.yml", "", testJobBlock, ciJobsTopLevel)
	require.NoError(t, err)
	assert.Contains(t, res.reason, "YAML documents")
	assert.Equal(t, multi, readFile(t, root, ".gitlab-ci.yml"))
}

func TestCIPlaceBlock_CannotPlaceReturnsTheBlockUnwritten(t *testing.T) {
	root := t.TempDir()
	anchored := "\n.defaults: &defaults\n  image: alpine\n\nbuild:\n  <<: *defaults\n  script:\n    - make\n"
	writeFile(t, root, ".gitlab-ci.yml", anchored)

	res, err := ciPlaceBlock(root, ".gitlab-ci.yml", "", testJobBlock, ciJobsTopLevel)
	require.NoError(t, err)
	assert.Contains(t, res.block, ciImage)
	assert.Contains(t, res.reason, "anchors")
	assert.False(t, res.created)
	assert.Equal(t, anchored, readFile(t, root, ".gitlab-ci.yml"))
}

func TestWriteCIConfigFile_PreservesMode(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "azure-pipelines.yml")
	require.NoError(t, os.WriteFile(path, []byte("trigger: none\n"), 0o644))

	created, unchanged, err := writeCIConfigFile(path, "trigger: none\njobs: []\n")
	require.NoError(t, err)
	assert.False(t, created)
	assert.False(t, unchanged)

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o644), info.Mode().Perm())
}

func TestWriteCIConfigFile_NewFileIsPrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "new.yml")

	created, _, err := writeCIConfigFile(path, "jobs: []\n")
	require.NoError(t, err)
	assert.True(t, created)

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestCIConfigVersion(t *testing.T) {
	assert.Equal(t, 0, ciConfigVersion("jobs: []\n"))
	assert.Equal(t, ciBlockVersion, ciConfigVersion(ciManagedBlock("infracost: {}")))
	assert.Equal(t, ciBlockVersion, ciConfigVersion(ciFileMarker))
}

func readFile(t *testing.T, root, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	require.NoError(t, err)
	return string(b)
}

func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(root, filepath.FromSlash(rel)), []byte(content), 0o644))
}

func requireParses(t *testing.T, content string) {
	t.Helper()
	var out map[string]any
	require.NoError(t, yaml.Unmarshal([]byte(content), &out))
}

// The name × content matrix from the classifier: a job is replaced only when
// it is both named like ours and running Infracost.
func TestCIScanJobs_Classifier(t *testing.T) {
	tests := []struct {
		name    string
		shape   ciJobShape
		content string
		replace []string
		warn    []string
	}{
		{
			name:    "gitlab named and recognised",
			shape:   ciJobsTopLevel,
			content: "infracost:merge-request-checks:\n  image: infracost/infracost:ci-0.10\n  script:\n    - infracost breakdown --path=.\n",
			replace: []string{"infracost:merge-request-checks"},
		},
		{
			name:    "gitlab named, not recognised",
			shape:   ciJobsTopLevel,
			content: "infracost-audit:\n  image: alpine\n  script:\n    - ./scripts/audit.sh\n",
		},
		{
			name:    "gitlab recognised under another name",
			shape:   ciJobsTopLevel,
			content: "cost-check:\n  image: alpine\n  script:\n    - infracost-ci diff --base-path base --head-path head\n",
			warn:    []string{"cost-check"},
		},
		{
			name:    "gitlab neither",
			shape:   ciJobsTopLevel,
			content: "unit-tests:\n  script:\n    - make test\n",
		},
		{
			name:    "azure named and recognised",
			shape:   ciJobsSequence,
			content: "jobs:\n  - job: infracost_pull_request_checks\n    steps:\n      - task: InfracostSetup@2\n",
			replace: []string{"infracost_pull_request_checks"},
		},
		{
			name:    "azure named, not recognised",
			shape:   ciJobsSequence,
			content: "jobs:\n  - job: infracost_audit\n    steps:\n      - script: ./scripts/audit.sh\n",
		},
		{
			name:    "azure recognised under another name",
			shape:   ciJobsSequence,
			content: "jobs:\n  - job: cost_check\n    container: ghcr.io/infracost/ci:0.1\n    steps:\n      - script: echo hi\n",
			warn:    []string{"cost_check"},
		},
		{
			name:    "azure neither",
			shape:   ciJobsSequence,
			content: "jobs:\n  - job: compile\n    steps:\n      - script: make\n",
		},
		{
			// Ours is already managed; only what sits outside is classified.
			name:    "a job inside the managed block is not a job outside it",
			shape:   ciJobsTopLevel,
			content: ciManagedBlock("infracost-diff:\n  image: " + ciImage + "\n  script:\n    - infracost-ci diff\n"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scan := ciScanJobs(tt.content, tt.shape)
			assert.Equal(t, tt.replace, nilIfEmpty(ciJobNames(scan.replace)))
			assert.Equal(t, tt.warn, nilIfEmpty(ciJobNames(scan.warn)))
		})
	}
}

// Bitbucket's recipes live under the user's own pull-requests: key, so nothing
// is classified there and nothing is removed.
func TestCIScanJobs_NoShapeFindsNothing(t *testing.T) {
	scan := ciScanJobs("infracost:\n  image: "+ciImage+"\n", ciJobsNone)
	assert.Empty(t, scan.replace)
	assert.Empty(t, scan.warn)
}

func TestCIRemoveJobs_Ranges(t *testing.T) {
	tests := []struct {
		name    string
		shape   ciJobShape
		content string
		want    string
	}{
		{
			name:  "the recipe's own commentary comes with it",
			shape: ciJobsTopLevel,
			content: "unit-tests:\n  script: make test\n\n" +
				"# Run Infracost on merge requests.\n# It comments with the diff.\n" +
				"infracost:checks:\n  image: infracost/infracost:ci-0.10\n  script: make cost\n",
			want: "unit-tests:\n  script: make test\n",
		},
		{
			name:  "a blank line above the job is kept",
			shape: ciJobsTopLevel,
			content: "unit-tests:\n  script: make test\n\n" +
				"infracost:checks:\n  image: infracost/infracost:ci-0.10\n\nlint:\n  script: make lint\n",
			want: "unit-tests:\n  script: make test\n\nlint:\n  script: make lint\n",
		},
		{
			name:  "the next job keeps its own leading comment",
			shape: ciJobsTopLevel,
			content: "infracost:checks:\n  image: infracost/infracost:ci-0.10\n\n" +
				"# Lint everything.\nlint:\n  script: make lint\n",
			want: "# Lint everything.\nlint:\n  script: make lint\n",
		},
		{
			name:  "two neighbours do not leave a stack of blank lines",
			shape: ciJobsTopLevel,
			content: "infracost:checks:\n  image: infracost/infracost:ci-0.10\n\n" +
				"infracost:update:\n  image: infracost/infracost:ci-0.10\n\nlint:\n  script: make lint\n",
			want: "lint:\n  script: make lint\n",
		},
		{
			name:  "a comment-only tail at the end of the file stays",
			shape: ciJobsTopLevel,
			content: "lint:\n  script: make lint\n\ninfracost:checks:\n  image: infracost/infracost:ci-0.10\n\n" +
				"# Nothing below here yet.\n",
			want: "lint:\n  script: make lint\n\n# Nothing below here yet.\n",
		},
		{
			name:    "the last job in the file",
			shape:   ciJobsTopLevel,
			content: "lint:\n  script: make lint\n\ninfracost:checks:\n  image: infracost/infracost:ci-0.10\n",
			want:    "lint:\n  script: make lint\n",
		},
		{
			name:  "gitlab stages entries go with the jobs that declared them",
			shape: ciJobsTopLevel,
			content: "stages:\n  - test\n  - infracost:checks\n\n" +
				"unit-tests:\n  stage: test\n  script: make test\n\n" +
				"infracost:checks:\n  stage: infracost:checks\n  image: infracost/infracost:ci-0.10\n",
			want: "stages:\n  - test\n\nunit-tests:\n  stage: test\n  script: make test\n",
		},
		{
			name:  "an emptied stages key goes too",
			shape: ciJobsTopLevel,
			content: "stages:\n  - infracost:checks\n\n" +
				"infracost:checks:\n  stage: infracost:checks\n  image: infracost/infracost:ci-0.10\n\n" +
				"lint:\n  script: make lint\n",
			want: "lint:\n  script: make lint\n",
		},
		{
			name:  "an azure job keeps its siblings and its parent key",
			shape: ciJobsSequence,
			content: "trigger:\n  - main\n\njobs:\n  - job: compile\n    steps:\n      - script: make\n" +
				"  # Infracost, from the docs.\n  - job: infracost_cloud_update\n    steps:\n      - task: InfracostSetup@2\n",
			want: "trigger:\n  - main\n\njobs:\n  - job: compile\n    steps:\n      - script: make\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scan := ciScanJobs(tt.content, tt.shape)
			require.NotEmpty(t, scan.replace)
			assert.Equal(t, tt.want, ciRemoveJobs(tt.content, scan.replace, tt.shape, ""))
			requireParses(t, tt.want)
		})
	}
}

// A stage the user named after us but runs their own work in is only touched
// when a job is being removed alongside it.
func TestCIRemoveJobs_StagesNeedAJob(t *testing.T) {
	content := "stages:\n  - infracost:checks\n\nunit-tests:\n  script: make test\n"
	assert.Equal(t, content, ciRemoveJobs(content, nil, ciJobsTopLevel, ""))
}

// A flow sequence puts every entry on the key's own line, so there is no range
// to cut and the line-based removal leaves it alone.
func TestCIRemoveJobs_SkipsFlowStyle(t *testing.T) {
	content := "stages: [infracost:checks, test]\n\ninfracost:checks:\n  image: infracost/infracost:ci-0.10\n"
	scan := ciScanJobs(content, ciJobsTopLevel)
	require.Len(t, scan.replace, 1)
	assert.Equal(t, "stages: [infracost:checks, test]\n", ciRemoveJobs(content, scan.replace, ciJobsTopLevel, ""))
}

// A legacy job under jobs: is removed even though the block is going under
// stages:, and the key the removal empties goes with it.
func TestCIPlaceBlock_RemovesFromJobsWhilePlacingUnderStages(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "azure-pipelines.yml", "trigger:\n  - main\n\njobs:\n  - job: infracost_cloud_update\n    steps:\n      - task: InfracostSetup@2\n\nstages:\n  - stage: build\n    jobs:\n      - job: compile\n        steps:\n          - script: make\n")

	res, err := ciPlaceBlock(root, "azure-pipelines.yml", "stages", "- stage: infracost\n  jobs: []\n", ciJobsSequence)
	require.NoError(t, err)
	assert.Equal(t, []string{"infracost_cloud_update"}, res.replaced)

	got := readFile(t, root, "azure-pipelines.yml")
	requireParses(t, got)
	assert.NotContains(t, got, "jobs:\n\n")
	assert.NotContains(t, got, "InfracostSetup@2")
	assert.Contains(t, got, "- stage: infracost")
	assert.Contains(t, got, "- stage: build")
}

// The removal edits the same string the splice then edits, so a refusal takes
// the removal with it: a file we would not write is a file we do not cut.
func TestCIPlaceBlock_ARefusedWriteRemovesNothing(t *testing.T) {
	root := t.TempDir()
	crossed := "infracost:checks:\n  image: infracost/infracost:ci-0.10\n\n" +
		ciBlockStart + "\nunit-tests:\n  script: make test\n" + ciBlockEnd + "\n"
	writeFile(t, root, ".gitlab-ci.yml", crossed)

	res, err := ciPlaceBlock(root, ".gitlab-ci.yml", "", testJobBlock, ciJobsTopLevel)
	require.NoError(t, err)
	assert.Contains(t, res.reason, "unit-tests")
	assert.Empty(t, res.replaced)
	assert.Equal(t, crossed, readFile(t, root, ".gitlab-ci.yml"))
}

func nilIfEmpty(s []string) []string {
	if len(s) == 0 {
		return nil
	}
	return s
}
