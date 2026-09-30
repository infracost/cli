package main

import (
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/infracost/cli/internal/api/events"
	"github.com/infracost/go-proto/pkg/diagnostic"
	parserpb "github.com/infracost/proto/gen/go/infracost/parser"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHasBoundedErrors guards the generic infracost-command event: the setup
// commands send a fixed label, since their error text names local paths.
func TestHasBoundedErrors(t *testing.T) {
	tests := []struct {
		name string
		path string
		want bool
	}{
		{name: "ci setup", path: "infracost ci setup", want: true},
		{name: "unified setup", path: "infracost setup", want: true},
		{name: "other command", path: "infracost breakdown", want: false},
		{name: "unrelated setup-alike", path: "infracost agent setup", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			events.RegisterMetadata("commandPath", tt.path)
			assert.Equal(t, tt.want, hasBoundedErrors())
		})
	}
}

// TestHasBoundedErrorsBeforePreRun covers the flag-parse path: commandPath is
// registered in PersistentPreRun, which a bad flag never reaches.
func TestHasBoundedErrorsBeforePreRun(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want bool
	}{
		{name: "ci setup with a bad flag", args: []string{"infracost", "ci", "setup", "--bogus"}, want: true},
		{name: "setup with a bad flag", args: []string{"infracost", "setup", "--bogus"}, want: true},
		{name: "ci setup exactly", args: []string{"infracost", "ci", "setup"}, want: true},
		{name: "another command", args: []string{"infracost", "breakdown", "--bogus"}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			events.Restore(map[string]interface{}{})
			origArgs := os.Args
			os.Args = tt.args
			t.Cleanup(func() { os.Args = origArgs })

			assert.Equal(t, tt.want, hasBoundedErrors())
		})
	}
}

// TestSetupErrorNeverReachesInfracostError pins the other telemetry exit from
// a failed command. main.go turns a command error into a FAILED_OPERATION
// diagnostic and pushes diag.String() verbatim for anything diags.Critical()
// returns, so the guarantee rests on that type never being critical.
func TestSetupErrorNeverReachesInfracostError(t *testing.T) {
	setupErrors := []error{
		fmt.Errorf("writing /Users/owen/code/acme-prod-infra/.github/workflows/infracost.yml: %w", os.ErrPermission),
		errors.New(`.github/workflows/ci.yml already defines "deploy-acme-prod-account" at line 42`),
		errors.New("could not parse remote URL \"git@github.acme.internal:acme/prod-infra.git\""),
		errors.New("ics_live_abc123 rejected"),
	}
	for _, err := range setupErrors {
		t.Run(err.Error()[:20], func(t *testing.T) {
			// Exactly what main.go does with the error cmd.Execute returns.
			var diags *diagnostic.Diagnostics
			diags = diags.Add(diagnostic.FromError(parserpb.DiagnosticType_DIAGNOSTIC_TYPE_FAILED_OPERATION, err))

			require.Equal(t, 1, diags.Len(), "the diagnostic is recorded")
			assert.Zero(t, diags.Critical().Len(), "a command error must not be critical: critical diagnostics are pushed verbatim as infracost-error")

			for _, d := range diags.Critical().Unwrap() {
				assert.NotContains(t, d.String(), "acme")
				assert.NotContains(t, d.String(), "/Users/")
				assert.NotContains(t, d.String(), "ics_live")
			}
		})
	}
}
