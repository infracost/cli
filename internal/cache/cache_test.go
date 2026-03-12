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

func TestWriteThenRead(t *testing.T) {
	c := testConfig(t)
	data := testOutput()
	absPath := t.TempDir()

	err := c.Write(absPath, &data)
	require.NoError(t, err)

	output, err := c.Read(absPath, false)
	require.NoError(t, err)

	assert.Equal(t, "USD", output.Currency)
	assert.Len(t, output.Projects, 1)
	assert.Equal(t, "test-project", output.Projects[0].ProjectName)
}

func TestReadExpired(t *testing.T) {
	c := testConfig(t)
	c.TTL = time.Nanosecond
	data := testOutput()
	absPath := "/test/project"

	err := c.Write(absPath, &data)
	require.NoError(t, err)

	// Give it a moment to expire
	time.Sleep(2 * time.Millisecond)
	_, err = c.Read(absPath, false)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no cached results found")
}

func TestReadMissing(t *testing.T) {
	c := testConfig(t)

	_, err := c.Read("/nonexistent/path", false)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no cached results found")
}

func TestReadLatest(t *testing.T) {
	c := testConfig(t)
	data := testOutput()

	err := c.Write("/first/project", &data)
	require.NoError(t, err)
	time.Sleep(10 * time.Millisecond)

	data.Projects[0].ProjectName = "second-project"
	err = c.Write("/second/project", &data)
	require.NoError(t, err)

	output, err := c.ReadLatest()
	require.NoError(t, err)
	assert.Equal(t, "second-project", output.Projects[0].ProjectName)
}

func TestReadStaleSourceFiles(t *testing.T) {
	c := testConfig(t)
	sourceDir := t.TempDir()
	data := testOutput()

	err := c.Write(sourceDir, &data)
	require.NoError(t, err)

	// Verify cache is valid before modification
	_, err = c.Read(sourceDir, false)
	require.NoError(t, err)

	// Create a file in the source dir newer than the cache
	time.Sleep(10 * time.Millisecond)
	err = os.WriteFile(filepath.Join(sourceDir, "main.tf"), []byte("resource {}"), 0600)
	require.NoError(t, err)

	_, err = c.Read(sourceDir, false)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "stale")
}

func TestReadAllowStale(t *testing.T) {
	c := testConfig(t)
	sourceDir := t.TempDir()
	data := testOutput()

	err := c.Write(sourceDir, &data)
	require.NoError(t, err)

	// Create a file in the source dir newer than the cache
	time.Sleep(10 * time.Millisecond)
	err = os.WriteFile(filepath.Join(sourceDir, "main.tf"), []byte("resource {}"), 0600)
	require.NoError(t, err)

	// allowStale=true should still return the data
	output, err := c.Read(sourceDir, true)
	require.NoError(t, err)
	assert.Equal(t, "USD", output.Currency)
}

func TestReadSkipsHeavyDirs(t *testing.T) {
	c := testConfig(t)
	sourceDir := t.TempDir()
	data := testOutput()

	err := c.Write(sourceDir, &data)
	require.NoError(t, err)

	// Create a newer file inside .terraform — should be skipped
	time.Sleep(10 * time.Millisecond)
	tfDir := filepath.Join(sourceDir, ".terraform")
	require.NoError(t, os.MkdirAll(tfDir, 0700))
	err = os.WriteFile(filepath.Join(tfDir, "plugin.bin"), []byte("binary"), 0600)
	require.NoError(t, err)

	output, err := c.Read(sourceDir, false)
	require.NoError(t, err)
	assert.Equal(t, "USD", output.Currency)
}

func TestNoSessionFallsBackToPath(t *testing.T) {
	c := testConfig(t)
	data := testOutput()
	absPath := t.TempDir()

	err := c.Write(absPath, &data)
	require.NoError(t, err)

	output, err := c.Read(absPath, false)
	require.NoError(t, err)

	assert.Equal(t, "USD", output.Currency)
}

func TestReadBySessionID(t *testing.T) {
	c := testConfig(t)
	c.SessionID = "test-session-123"
	data := testOutput()
	absPath := t.TempDir()

	err := c.Write(absPath, &data)
	require.NoError(t, err)

	// Session overrides path — find it even with a different path.
	output, err := c.Read("/completely/different/path", false)
	require.NoError(t, err)

	assert.Equal(t, "USD", output.Currency)
}

func TestDifferentSessionFallsBackToPath(t *testing.T) {
	c := testConfig(t)
	c.SessionID = "session-A"
	data := testOutput()
	absPath := t.TempDir()

	err := c.Write(absPath, &data)
	require.NoError(t, err)

	// Reading with a different session ID still finds the entry via path+TTL fallback.
	c.SessionID = "session-B"
	output, err := c.Read(absPath, false)
	require.NoError(t, err)
	assert.Equal(t, "USD", output.Currency)
}

func TestReadLatestWithSession(t *testing.T) {
	c := testConfig(t)
	c.SessionID = "my-session"
	data := testOutput()

	err := c.Write("/first/project", &data)
	require.NoError(t, err)
	time.Sleep(10 * time.Millisecond)

	// Write a newer entry without the session.
	c.SessionID = ""
	data.Projects[0].ProjectName = "second-project"
	err = c.Write("/second/project", &data)
	require.NoError(t, err)

	// ReadLatest with the session should return the session's entry, not the newer one.
	c.SessionID = "my-session"
	output, err := c.ReadLatest()
	require.NoError(t, err)
	assert.Equal(t, "test-project", output.Projects[0].ProjectName)
}

func TestTermSessionReadBySessionID(t *testing.T) {
	c := testConfig(t)
	c.TermSessionID = "term-123"
	data := testOutput()
	absPath := t.TempDir()

	err := c.Write(absPath, &data)
	require.NoError(t, err)

	// Terminal session matches, so Read should find it even with a different path.
	output, err := c.Read("/completely/different/path", false)
	require.NoError(t, err)
	assert.Equal(t, "USD", output.Currency)
}

func TestTermSessionRespectsExpiry(t *testing.T) {
	c := testConfig(t)
	c.TermSessionID = "term-123"
	c.TTL = time.Nanosecond
	data := testOutput()

	err := c.Write("/test/project", &data)
	require.NoError(t, err)

	time.Sleep(2 * time.Millisecond)

	// Terminal session should still respect TTL, unlike explicit sessions.
	_, err = c.Read("/other/path", false)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no cached results found")
}

func TestTermSessionReadLatestRespectsExpiry(t *testing.T) {
	c := testConfig(t)
	c.TermSessionID = "term-123"
	c.TTL = time.Nanosecond
	data := testOutput()

	err := c.Write("/test/project", &data)
	require.NoError(t, err)

	time.Sleep(2 * time.Millisecond)

	// ReadLatest with terminal session should still respect TTL.
	_, err = c.ReadLatest()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no cached results found")
}

func TestExplicitSessionIgnoresExpiry(t *testing.T) {
	c := testConfig(t)
	c.SessionID = "explicit-123"
	c.TTL = time.Nanosecond
	data := testOutput()

	err := c.Write("/test/project", &data)
	require.NoError(t, err)

	time.Sleep(2 * time.Millisecond)

	// Explicit session should skip TTL.
	output, err := c.Read("/other/path", false)
	require.NoError(t, err)
	assert.Equal(t, "USD", output.Currency)
}

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