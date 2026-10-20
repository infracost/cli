package cmds

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/infracost/cli/internal/format"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadBaselineFile(t *testing.T) {
	write := func(t *testing.T, body string) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "baseline.json")
		require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
		return path
	}

	t.Run("rejects diff output", func(t *testing.T) {
		// Shares the `projects` key with a scan, but names costs
		// differently, so it would decode as a zero-cost baseline.
		var b bytes.Buffer
		require.NoError(t, format.Diff(
			&format.Output{Currency: "USD", Projects: []format.ProjectOutput{{ProjectName: "main"}}},
			&format.Output{Currency: "USD", Projects: []format.ProjectOutput{{ProjectName: "main"}}},
		).ToJSON(&b))

		_, err := loadBaselineFile(write(t, b.String()))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "is `infracost diff` output")
	})

	t.Run("accepts scan output", func(t *testing.T) {
		got, err := loadBaselineFile(write(t,
			`{"currency":"USD","projects":[{"project_name":"main","resources":[]}]}`))
		require.NoError(t, err)
		assert.Equal(t, "USD", got.Currency)
	})

	t.Run("rejects an unrelated json file", func(t *testing.T) {
		_, err := loadBaselineFile(write(t, `{"hello":"world"}`))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "has no projects")
	})
}

func TestDiffDiagnosticsErr(t *testing.T) {
	withDiagnostic := func(severity string) *format.Output {
		return &format.Output{Projects: []format.ProjectOutput{{
			ProjectName: "main",
			Diagnostics: []format.DiagnosticOutput{{
				Prefix:   "HCL parse error",
				Message:  "invalid block",
				Severity: severity,
			}},
		}}}
	}

	t.Run("critical stops the diff and prints the message", func(t *testing.T) {
		var stderr bytes.Buffer
		err := diffDiagnosticsErr(&stderr, "baseline", withDiagnostic("critical"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "the baseline has 1 critical diagnostic")
		assert.Contains(t, stderr.String(), "invalid block")
	})

	t.Run("warnings do not stop the diff", func(t *testing.T) {
		var stderr bytes.Buffer
		assert.NoError(t, diffDiagnosticsErr(&stderr, "current scan", withDiagnostic("warning")))
		assert.Empty(t, stderr.String())
	})

	t.Run("no diagnostics", func(t *testing.T) {
		var stderr bytes.Buffer
		assert.NoError(t, diffDiagnosticsErr(&stderr, "baseline", &format.Output{}))
	})
}
