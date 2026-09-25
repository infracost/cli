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

	res, err := ciPlaceBlock(root, ".gitlab-ci.yml", "", testJobBlock)
	require.NoError(t, err)
	assert.True(t, res.created)
	fresh := readFile(t, root, ".gitlab-ci.yml")
	assert.Contains(t, fresh, ciImage)
	assert.Equal(t, ciBlockVersion, ciConfigVersion(fresh))

	// Into a file that already has unrelated jobs.
	root = t.TempDir()
	existing := "stages:\n  - test\n\nunit-tests:\n  script:\n    - make test\n"
	writeFile(t, root, ".gitlab-ci.yml", existing)

	res, err = ciPlaceBlock(root, ".gitlab-ci.yml", "", testJobBlock)
	require.NoError(t, err)
	assert.False(t, res.created)
	second := readFile(t, root, ".gitlab-ci.yml")
	assert.Contains(t, second, existing)
	requireParses(t, second)

	// Re-running replaces the managed span rather than appending a second job.
	res, err = ciPlaceBlock(root, ".gitlab-ci.yml", "", testJobBlock)
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
	_, err := ciPlaceBlock(root, "bitbucket-pipelines.yml", "pipelines", body)
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
	_, err = ciPlaceBlock(root, "bitbucket-pipelines.yml", "pipelines", body)
	require.NoError(t, err)
	assert.Equal(t, got, readFile(t, root, "bitbucket-pipelines.yml"))
}

func TestCIPlaceBlock_CreatesMissingKey(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "bitbucket-pipelines.yml", "image: atlassian/default-image:4\n")

	_, err := ciPlaceBlock(root, "bitbucket-pipelines.yml", "pipelines", "pull-requests:\n  '**': []\n")
	require.NoError(t, err)

	got := readFile(t, root, "bitbucket-pipelines.yml")
	assert.Contains(t, got, "\npipelines:\n")
	requireParses(t, got)
}

func TestCIPlaceBlock_RefusesAnExistingInfracostJob(t *testing.T) {
	root := t.TempDir()
	handWritten := "infracost:\n  image: infracost/infracost:latest\n  script:\n    - infracost breakdown --path .\n"
	writeFile(t, root, ".gitlab-ci.yml", handWritten)

	_, err := ciPlaceBlock(root, ".gitlab-ci.yml", "", testJobBlock)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `already defines "infracost" at line 1`)
	assert.Equal(t, handWritten, readFile(t, root, ".gitlab-ci.yml"))
}

// A variable named INFRACOST_API_KEY is not a job, so it must not trip the gate.
func TestCIPlaceBlock_AllowsInfracostVariables(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, ".gitlab-ci.yml", "variables:\n  INFRACOST_API_KEY: $INFRACOST_API_KEY\n")

	_, err := ciPlaceBlock(root, ".gitlab-ci.yml", "", testJobBlock)
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

			res, err := ciPlaceBlock(root, ".gitlab-ci.yml", "", testJobBlock)
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

	_, err := ciPlaceBlock(root, "bitbucket-pipelines.yml", "pipelines", "pull-requests:\n  '**': []\n")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `already defines "infracost"`)
	assert.Equal(t, existing, readFile(t, root, "bitbucket-pipelines.yml"))
}

// A hand-removed end sentinel would append a second copy of the block; the
// result must not reach disk.
func TestCIPlaceBlock_RefusesAResultThatWouldNotParse(t *testing.T) {
	root := t.TempDir()
	body := "pull-requests:\n  '**': []\n"

	_, err := ciPlaceBlock(root, "bitbucket-pipelines.yml", "pipelines", body)
	require.NoError(t, err)

	// Drop the closing sentinel, as a merge resolution would.
	written := readFile(t, root, "bitbucket-pipelines.yml")
	mangled := strings.ReplaceAll(written, "  "+ciBlockEnd+"\n", "")
	writeFile(t, root, "bitbucket-pipelines.yml", mangled)

	res, err := ciPlaceBlock(root, "bitbucket-pipelines.yml", "pipelines", body)
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

	res, err := ciPlaceBlock(root, ".gitlab-ci.yml", "", testJobBlock)
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

	res, err := ciPlaceBlock(root, ".gitlab-ci.yml", "", testJobBlock)
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

	res, err := ciPlaceBlock(root, ".gitlab-ci.yml", "", testJobBlock)
	require.NoError(t, err)
	assert.Contains(t, res.reason, "YAML documents")
	assert.Equal(t, multi, readFile(t, root, ".gitlab-ci.yml"))
}

func TestCIPlaceBlock_CannotPlaceReturnsTheBlockUnwritten(t *testing.T) {
	root := t.TempDir()
	anchored := "\n.defaults: &defaults\n  image: alpine\n\nbuild:\n  <<: *defaults\n  script:\n    - make\n"
	writeFile(t, root, ".gitlab-ci.yml", anchored)

	res, err := ciPlaceBlock(root, ".gitlab-ci.yml", "", testJobBlock)
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
