package cmds_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/infracost/cli/internal/api/dashboard/mocks"
	"github.com/infracost/cli/internal/cmds"
)

func TestSetup_RejectsAuthToken(t *testing.T) {
	cfg := ciTestConfigWithAuthToken(t)
	cmd := cmds.Setup(cfg)
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetContext(context.Background())

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "INFRACOST_CLI_AUTHENTICATION_TOKEN")
}

func TestSetup_AlreadyLoggedIn(t *testing.T) {
	cfg := ciTestConfig(t, mocks.NewMockClient(t))
	cmd := cmds.Setup(cfg)
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetContext(context.Background())

	output := captureOutput(t, func() {
		_ = cmd.Execute()
	})

	assert.Contains(t, output, "✔  Already logged in")
}