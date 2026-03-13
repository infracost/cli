package cache

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/infracost/cli/internal/format"
	"github.com/infracost/go-proto/pkg/rat"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKeyDeterminism(t *testing.T) {
	k1 := Key("/home/user/project")
	k2 := Key("/home/user/project")
	k3 := Key("/home/user/other")

	assert.Equal(t, k1, k2, "same input should produce same key")
	assert.NotEqual(t, k1, k3, "different input should produce different key")
	assert.Len(t, k1, 16)
}

func testOutput() format.Output {
	return format.Output{
		Currency: "USD",
		Projects: []format.ProjectOutput{
			{
				ProjectName: "test-project",
				Path:        "/test/path",
				Resources: []format.ResourceOutput{
					{
						Name: "aws_instance.web",
						Type: "aws_instance",
						CostComponents: []format.CostComponentOutput{
							{
								Name:             "Instance usage",
								Unit:             "hours",
								TotalMonthlyCost: rat.New(10),
							},
						},
					},
				},
			},
		},
	}
}

func testConfig(t *testing.T) *Config {
	t.Helper()
	c := &Config{Cache: filepath.Join(t.TempDir(), "cache")}
	c.Process()
	return c
}

// ForPath tests

func TestForPath(t *testing.T) {
	c := testConfig(t)
	data := testOutput()
	absPath := t.TempDir()

	err := c.Write(absPath, &data)
	require.NoError(t, err)

	output, err := c.ForPath(absPath)
	require.NoError(t, err)

	assert.Equal(t, "USD", output.Currency)
	assert.Len(t, output.Projects, 1)
	assert.Equal(t, "test-project", output.Projects[0].ProjectName)
}

func TestForPathExpired(t *testing.T) {
	c := testConfig(t)
	c.TTL = time.Nanosecond
	data := testOutput()
	absPath := "/test/project"

	err := c.Write(absPath, &data)
	require.NoError(t, err)

	time.Sleep(2 * time.Millisecond)
	_, err = c.ForPath(absPath)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no cached results found")
}

func TestForPathMissing(t *testing.T) {
	c := testConfig(t)

	_, err := c.ForPath("/nonexistent/path")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no cached results found")
}

func TestForPathStaleSourceFiles(t *testing.T) {
	c := testConfig(t)
	sourceDir := t.TempDir()
	data := testOutput()

	err := c.Write(sourceDir, &data)
	require.NoError(t, err)

	_, err = c.ForPath(sourceDir)
	require.NoError(t, err)

	// Create a file in the source dir newer than the cache.
	time.Sleep(10 * time.Millisecond)
	err = os.WriteFile(filepath.Join(sourceDir, "main.tf"), []byte("resource {}"), 0600)
	require.NoError(t, err)

	_, err = c.ForPath(sourceDir)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "stale")
}

func TestForPathSkipsHeavyDirs(t *testing.T) {
	c := testConfig(t)
	sourceDir := t.TempDir()
	data := testOutput()

	err := c.Write(sourceDir, &data)
	require.NoError(t, err)

	// Create a newer file inside .terraform — should be skipped.
	time.Sleep(10 * time.Millisecond)
	tfDir := filepath.Join(sourceDir, ".terraform")
	require.NoError(t, os.MkdirAll(tfDir, 0700))
	err = os.WriteFile(filepath.Join(tfDir, "plugin.bin"), []byte("binary"), 0600)
	require.NoError(t, err)

	output, err := c.ForPath(sourceDir)
	require.NoError(t, err)
	assert.Equal(t, "USD", output.Currency)
}

func TestForPathWrongPath(t *testing.T) {
	c := testConfig(t)
	data := testOutput()
	absPath := t.TempDir()

	err := c.Write(absPath, &data)
	require.NoError(t, err)

	_, err = c.ForPath("/completely/different/path")
	assert.Error(t, err)
}

// Latest tests

func TestLatest(t *testing.T) {
	c := testConfig(t)
	data := testOutput()

	err := c.Write("/first/project", &data)
	require.NoError(t, err)
	time.Sleep(10 * time.Millisecond)

	data.Projects[0].ProjectName = "second-project"
	err = c.Write("/second/project", &data)
	require.NoError(t, err)

	output, err := c.Latest(false)
	require.NoError(t, err)
	assert.Equal(t, "second-project", output.Projects[0].ProjectName)
}

func TestLatestExpired(t *testing.T) {
	c := testConfig(t)
	c.TTL = time.Nanosecond
	data := testOutput()

	err := c.Write("/test/project", &data)
	require.NoError(t, err)

	time.Sleep(2 * time.Millisecond)

	_, err = c.Latest(false)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no cached results found")
}

func TestLatestEmpty(t *testing.T) {
	c := testConfig(t)

	_, err := c.Latest(false)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no cached results found")
}

func TestLatestStaleSourceFiles(t *testing.T) {
	c := testConfig(t)
	sourceDir := t.TempDir()
	data := testOutput()

	err := c.Write(sourceDir, &data)
	require.NoError(t, err)

	// Create a file in the source dir newer than the cache.
	time.Sleep(10 * time.Millisecond)
	err = os.WriteFile(filepath.Join(sourceDir, "main.tf"), []byte("resource {}"), 0600)
	require.NoError(t, err)

	// Without allowStale, should be rejected.
	_, err = c.Latest(false)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "stale")

	// With allowStale, should still return the data.
	output, err := c.Latest(true)
	require.NoError(t, err)
	assert.Equal(t, "USD", output.Currency)
}

func TestLatestDeletedSourceDir(t *testing.T) {
	c := testConfig(t)
	sourceDir := t.TempDir()
	data := testOutput()

	err := c.Write(sourceDir, &data)
	require.NoError(t, err)

	// Remove the source directory (simulates price command's temp dir cleanup).
	require.NoError(t, os.RemoveAll(sourceDir))

	// Should still return the cached data since a missing directory is not
	// considered stale.
	output, err := c.Latest(false)
	require.NoError(t, err)
	assert.Equal(t, "USD", output.Currency)
}

// ReadFile tests

func TestReadFile(t *testing.T) {
	data := testOutput()
	b, err := json.Marshal(data)
	require.NoError(t, err)

	path := filepath.Join(t.TempDir(), "output.json")
	err = os.WriteFile(path, b, 0600)
	require.NoError(t, err)

	output, err := ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "USD", output.Currency)
	assert.Len(t, output.Projects, 1)
}