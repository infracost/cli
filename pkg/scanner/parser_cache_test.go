package scanner

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
}

// touch bumps a file's mtime so the change is visible even on coarse-mtime filesystems.
func touch(t *testing.T, path string) {
	t.Helper()
	future := time.Now().Add(time.Hour)
	require.NoError(t, os.Chtimes(path, future, future))
}

func setupRepo(t *testing.T) (root, project string) {
	t.Helper()
	root = t.TempDir()
	project = filepath.Join(root, "envs", "prod")
	writeFile(t, filepath.Join(project, "main.tf"), `module "x" { source = "../../modules/x" }`)
	writeFile(t, filepath.Join(root, "modules", "x", "main.tf"), `resource "aws_instance" "a" {}`)
	writeFile(t, filepath.Join(root, "vars", "prod.tfvars"), `a = 1`)
	return root, project
}

func fingerprint(t *testing.T, root, project string, deps ...string) string {
	t.Helper()
	fp, err := fingerprintProject(root, project, deps, nil)
	require.NoError(t, err)
	return fp
}

func TestFingerprintProject_DependencyDirChange(t *testing.T) {
	root, project := setupRepo(t)
	before := fingerprint(t, root, project, "modules/x")

	writeFile(t, filepath.Join(root, "modules", "x", "main.tf"), `resource "aws_instance" "b" { instance_type = "t3.micro" }`)
	touch(t, filepath.Join(root, "modules", "x", "main.tf"))

	require.NotEqual(t, before, fingerprint(t, root, project, "modules/x"))
}

func TestFingerprintProject_DependencyFileChange(t *testing.T) {
	root, project := setupRepo(t)
	before := fingerprint(t, root, project, "vars/prod.tfvars")

	writeFile(t, filepath.Join(root, "vars", "prod.tfvars"), `a = 2222`)
	touch(t, filepath.Join(root, "vars", "prod.tfvars"))

	require.NotEqual(t, before, fingerprint(t, root, project, "vars/prod.tfvars"))
}

func TestFingerprintProject_UnlistedChangeIgnored(t *testing.T) {
	root, project := setupRepo(t)
	before := fingerprint(t, root, project, "modules/x")

	writeFile(t, filepath.Join(root, "vars", "prod.tfvars"), `a = 2222`)
	touch(t, filepath.Join(root, "vars", "prod.tfvars"))

	require.Equal(t, before, fingerprint(t, root, project, "modules/x"))
}

func TestFingerprintProject_MissingDependency(t *testing.T) {
	root, project := setupRepo(t)
	before := fingerprint(t, root, project, "modules/y")

	writeFile(t, filepath.Join(root, "modules", "y", "main.tf"), `resource "aws_instance" "c" {}`)

	require.NotEqual(t, before, fingerprint(t, root, project, "modules/y"))
}

func TestFingerprintProject_DependencyInsideProject(t *testing.T) {
	root, project := setupRepo(t)
	writeFile(t, filepath.Join(project, "sub", "main.tf"), `locals {}`)

	require.Equal(t, fingerprint(t, root, project), fingerprint(t, root, project, "envs/prod/sub"))
}

func TestFingerprintProject_DependencyOrder(t *testing.T) {
	root, project := setupRepo(t)

	require.Equal(t,
		fingerprint(t, root, project, "modules/x", "vars/prod.tfvars"),
		fingerprint(t, root, project, "vars/prod.tfvars", "modules/x", "modules/x"),
	)
}

func TestFingerprintProject_DependencyGlob(t *testing.T) {
	root, project := setupRepo(t)
	before := fingerprint(t, root, project, "vars/*.tfvars")

	writeFile(t, filepath.Join(root, "vars", "prod.tfvars"), `a = 2222`)
	touch(t, filepath.Join(root, "vars", "prod.tfvars"))

	require.NotEqual(t, before, fingerprint(t, root, project, "vars/*.tfvars"))
}
