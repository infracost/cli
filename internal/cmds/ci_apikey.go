package cmds

import (
	"fmt"
	"strings"
)

// v01APIKeyPrefix is the prefix every v0.1 dashboard API key carries. CI v2
// rejects these, so storing one as the repository secret produces a pipeline
// that fails on its first run with an authentication error.
const v01APIKeyPrefix = "ico-"

// maxAPIKeyAttempts bounds the re-prompt after a key the CLI knows is wrong.
const maxAPIKeyAttempts = 3

// validateCIAPIKey rejects the key shapes that cannot work, before setup
// writes one into a repository secret where the failure only surfaces on the
// next pull request. The check is negative only: the token is opaque, so
// anything not known to be wrong is accepted.
func validateCIAPIKey(key string) error {
	switch {
	case key != strings.TrimSpace(key):
		return fmt.Errorf("the %s value has leading or trailing whitespace, which the Authorization header rejects", ciAPIKeySecret)

	case strings.HasPrefix(key, "$(") || strings.HasPrefix(key, "${"):
		return fmt.Errorf("the %s value is an unexpanded variable reference (%q): set the key itself, not a reference to it", ciAPIKeySecret, key)

	case strings.HasPrefix(key, v01APIKeyPrefix):
		return fmt.Errorf("the %s value is a v0.1 API key (%s…), which Infracost CI does not accept. Copy a CLI token from your organization's CLI tokens page instead", ciAPIKeySecret, v01APIKeyPrefix)
	}

	return nil
}
