package discovery_test

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/infracost/cli/internal/tui/discovery"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeFile is a tiny helper that creates a file with empty contents
// at path, ensuring parent directories exist. Test fixtures don't
// care about file content — only its presence and name.
func writeFile(t *testing.T, path string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, nil, 0o644))
}

// makeRepo turns dir into a git-managed directory by adding a .git
// subdirectory. discovery treats this as a repo root.
func makeRepo(t *testing.T, dir string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".git"), 0o755))
}

func TestIsIaCProject(t *testing.T) {
	d := t.TempDir()
	assert.False(t, discovery.IsIaCProject(d), "empty dir is not an IaC project")

	writeFile(t, filepath.Join(d, "README.md"))
	assert.False(t, discovery.IsIaCProject(d), "README alone is not IaC")

	writeFile(t, filepath.Join(d, "main.tf"))
	assert.True(t, discovery.IsIaCProject(d), "main.tf marks the dir as IaC")
}

func TestMatchedMarkers_RecognisesEachMarker(t *testing.T) {
	// One marker per fixture so we can verify each pattern lands.
	// We create them in different temp dirs so a single ReadDir of
	// each only matches one pattern at a time.
	cases := []struct {
		name string
		file string
		want string
	}{
		{"terraform tf", "main.tf", "*.tf"},
		{"terraform tfvars", "vars.tfvars", "*.tfvars"},
		{"cdk", "cdk.json", "cdk.json"},
		{"infracost yml", "infracost.yml", "infracost.yml"},
		{"infracost yaml", "infracost.yaml", "infracost.yaml"},
		{"pulumi lower", "pulumi.yaml", "pulumi.yaml"},
		{"pulumi cap", "Pulumi.yaml", "Pulumi.yaml"},
		{"terragrunt", "terragrunt.hcl", "terragrunt.hcl"},
		{"terraform json", "main.tf.json", "main.tf.json"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := t.TempDir()
			writeFile(t, filepath.Join(d, tc.file))

			got := discovery.MatchedMarkers(d)

			assert.Contains(t, got, tc.want, "expected %s to match", tc.file)
		})
	}
}

func TestMatchedMarkers_IgnoresUnmatched(t *testing.T) {
	d := t.TempDir()
	writeFile(t, filepath.Join(d, "go.mod"))
	writeFile(t, filepath.Join(d, "package.json"))
	writeFile(t, filepath.Join(d, "Cargo.toml"))

	assert.Nil(t, discovery.MatchedMarkers(d), "non-IaC files shouldn't match anything")
}

func TestWalk_YieldsRepoRootForIaCInsideRepo(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "myrepo")
	makeRepo(t, repo)
	// IaC nested two levels deep — walker should still yield the
	// repo root, not the deeper subdirectory.
	writeFile(t, filepath.Join(repo, "infra", "terraform", "main.tf"))

	var found []discovery.Project
	err := discovery.Walk(context.Background(), root, func(p discovery.Project) {
		found = append(found, p)
	})
	require.NoError(t, err)

	require.Len(t, found, 1)
	assert.Equal(t, repo, found[0].Path)
	assert.Equal(t, "myrepo", found[0].Name)
}

func TestWalk_DedupesMonorepoToSingleEntry(t *testing.T) {
	// Two unrelated subdirs each carry their own .tf files, and the
	// repo root has another. Pre-fix this would have yielded three
	// entries; we want exactly one (the repo).
	root := t.TempDir()
	repo := filepath.Join(root, "monorepo")
	makeRepo(t, repo)
	writeFile(t, filepath.Join(repo, "main.tf"))
	writeFile(t, filepath.Join(repo, "modules", "network", "main.tf"))
	writeFile(t, filepath.Join(repo, "environments", "prod", "main.tf"))

	var found []discovery.Project
	err := discovery.Walk(context.Background(), root, func(p discovery.Project) {
		found = append(found, p)
	})
	require.NoError(t, err)

	require.Len(t, found, 1, "monorepo should yield a single entry, not one per IaC subdir")
	assert.Equal(t, repo, found[0].Path)
}

func TestWalk_NonRepoDirsAreIgnored(t *testing.T) {
	// IaC files outside any git repo should not be yielded — the
	// picker is intentionally git-scoped so casual playground dirs
	// don't pollute the project list.
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "scratch", "main.tf"))

	var found []discovery.Project
	err := discovery.Walk(context.Background(), root, func(p discovery.Project) {
		found = append(found, p)
	})
	require.NoError(t, err)

	assert.Empty(t, found)
}

func TestWalk_RepoWithoutIaCIsSkipped(t *testing.T) {
	// A git repo containing only Go code shouldn't appear in the
	// picker — saves the user from a list dominated by every repo
	// they've ever cloned.
	root := t.TempDir()
	repo := filepath.Join(root, "go-app")
	makeRepo(t, repo)
	writeFile(t, filepath.Join(repo, "go.mod"))
	writeFile(t, filepath.Join(repo, "main.go"))

	var found []discovery.Project
	err := discovery.Walk(context.Background(), root, func(p discovery.Project) {
		found = append(found, p)
	})
	require.NoError(t, err)

	assert.Empty(t, found)
}

func TestWalk_HonoursContextCancellation(t *testing.T) {
	// Set up a tree large enough that an unbounded walk would visit
	// many entries, then cancel the context up front. The walker
	// should bail out quickly with ctx.Err() rather than visit
	// everything.
	root := t.TempDir()
	for i := range [50]int{} {
		dir := filepath.Join(root, "a", "b", "c", "d", "e", "f")
		writeFile(t, filepath.Join(dir, "file.txt"))
		_ = i
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before Walk runs

	var found []discovery.Project
	err := discovery.Walk(ctx, root, func(p discovery.Project) {
		found = append(found, p)
	})

	assert.ErrorIs(t, err, context.Canceled)
}

func TestWalk_FindsMultipleRepos(t *testing.T) {
	// Two siblings, each their own git repo, each with IaC. Walker
	// should yield both. Order isn't guaranteed by filepath.WalkDir
	// across platforms, so we sort for the assertion.
	root := t.TempDir()
	repoA := filepath.Join(root, "repoA")
	repoB := filepath.Join(root, "repoB")
	makeRepo(t, repoA)
	makeRepo(t, repoB)
	writeFile(t, filepath.Join(repoA, "main.tf"))
	writeFile(t, filepath.Join(repoB, "infra", "main.tf"))

	var found []discovery.Project
	err := discovery.Walk(context.Background(), root, func(p discovery.Project) {
		found = append(found, p)
	})
	require.NoError(t, err)

	paths := []string{}
	for _, p := range found {
		paths = append(paths, p.Path)
	}
	sort.Strings(paths)
	assert.Equal(t, []string{repoA, repoB}, paths)
}
