package cmds

import (
	"fmt"

	"github.com/infracost/cli/internal/config"
)

// requireUserLogin returns an error if the user is authenticated via a service
// account token rather than an interactive login. Setup commands need a real
// user identity (for org resolution, etc.) and cannot operate with tokens.
func requireUserLogin(cfg *config.Config) error {
	if len(cfg.Auth.AuthenticationToken) > 0 {
		return fmt.Errorf("setup requires interactive login, it cannot be used with INFRACOST_CLI_AUTHENTICATION_TOKEN\nRun `infracost login` first, then retry")
	}
	return nil
}