package scanner

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/infracost/proto/gen/go/infracost/parser/options"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
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

func TestFingerprintProject_DanglingSymlinkIsMissing(t *testing.T) {
	root, project := setupRepo(t)
	link := filepath.Join(root, "modules", "shared")
	require.NoError(t, os.Symlink(filepath.Join(root, "modules", "gone"), link))

	require.Equal(t, []dependencyPath{{rel: "modules/shared"}}, resolveDependencyPaths(root, project, []string{"modules/shared"}))

	before := fingerprint(t, root, project, "modules/shared")
	writeFile(t, filepath.Join(root, "modules", "gone", "main.tf"), `resource "aws_instance" "d" {}`)

	require.NotEqual(t, before, fingerprint(t, root, project, "modules/shared"))
}

func TestFingerprintProject_SymlinkedDependencyDir(t *testing.T) {
	root, project := setupRepo(t)
	require.NoError(t, os.Symlink(filepath.Join(root, "modules", "x"), filepath.Join(root, "modules", "link")))
	before := fingerprint(t, root, project, "modules/link")

	writeFile(t, filepath.Join(root, "modules", "x", "main.tf"), `resource "aws_instance" "e" { instance_type = "t3.micro" }`)
	touch(t, filepath.Join(root, "modules", "x", "main.tf"))

	require.NotEqual(t, before, fingerprint(t, root, project, "modules/link"))
}

func TestGenericOptionsFingerprint(t *testing.T) {
	base := &options.GenericOptions{ProjectName: "p", Env: map[string]string{"A": "1"}, TemporaryDirectory: "/tmp/run-1"}
	a, err := genericOptionsFingerprint(base)
	require.NoError(t, err)

	sameButNewTmp := proto.Clone(base).(*options.GenericOptions)
	sameButNewTmp.TemporaryDirectory = "/tmp/run-2"
	b, err := genericOptionsFingerprint(sameButNewTmp)
	require.NoError(t, err)
	require.Equal(t, a, b)

	newEnv := proto.Clone(base).(*options.GenericOptions)
	newEnv.Env["A"] = "2"
	c, err := genericOptionsFingerprint(newEnv)
	require.NoError(t, err)
	require.NotEqual(t, a, c)
}
