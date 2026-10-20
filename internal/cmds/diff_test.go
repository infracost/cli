package cmds

import (
	"bytes"
	"testing"

	"github.com/infracost/cli/internal/format"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
