package scanner

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"

	"github.com/infracost/cli/pkg/logging"
	"github.com/infracost/proto/gen/go/infracost/parser/options"
)

// SSHFetchAuthFromValue resolves the FetchAuth from a comma-separated list of
// SSH private key file paths (the --ssh-key-file / INFRACOST_CLI_SSH_KEY_FILE
// value). An empty value scans the standard ~/.ssh default key files. Shared by
// every fetch-capable command so they authenticate private module fetches the
// same way.
func SSHFetchAuthFromValue(value string) *options.FetchAuth {
	var files []string
	for _, p := range strings.Split(value, ",") {
		if p = strings.TrimSpace(p); p != "" {
			files = append(files, p)
		}
	}
	return ResolveSSHFetchAuth(files)
}

// defaultSSHKeyBasenames are the private key files ssh itself falls back to when
// no IdentityFile is configured, kept in step with OpenSSH's default set.
var defaultSSHKeyBasenames = []string{
	"id_rsa", "id_ecdsa", "id_ecdsa_sk", "id_ed25519", "id_ed25519_sk", "id_dsa",
}

// ResolveSSHFetchAuth builds the FetchAuth carrying the developer's on-disk SSH
// keys for the parser's in-process getter. grabber's SSH system fallback only
// consults the ssh-agent, so a key sitting in ~/.ssh that isn't loaded into an
// agent would otherwise be unusable; passing it explicitly closes that gap. The
// keys travel with an empty host (defaults offered for any host), and grabber
// still falls back to the agent, so this never suppresses agent identities.
//
// When keyFiles is non-empty (the --ssh-key-file flag / INFRACOST_SSH_KEY_FILE
// env) those exact paths are used; otherwise the standard ~/.ssh default key
// files are scanned. Passphrase-protected and unreadable keys are skipped - they
// remain resolvable via the agent. Returns nil when no usable key is found,
// leaving the getter to rely on the agent / system fallback.
func ResolveSSHFetchAuth(keyFiles []string) *options.FetchAuth {
	explicit := len(keyFiles) > 0
	paths := keyFiles
	if !explicit {
		paths = defaultSSHKeyFiles()
	}

	var keys []*options.SSHKey
	for _, path := range paths {
		key, err := readUsableSSHKey(path)
		if err != nil {
			// A named override that fails is worth a warning; a missing default
			// key file is normal (most machines have only one or two).
			if explicit {
				logging.Warnf("skipping SSH key %q: %s", path, err)
			} else {
				logging.Debugf("skipping SSH key %q: %s", path, err)
			}
			continue
		}
		keys = append(keys, &options.SSHKey{PrivateKey: key})
	}

	if len(keys) == 0 {
		return nil
	}
	return &options.FetchAuth{SshKeys: keys}
}

// defaultSSHKeyFiles returns the standard ~/.ssh private key paths.
func defaultSSHKeyFiles() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	paths := make([]string, 0, len(defaultSSHKeyBasenames))
	for _, name := range defaultSSHKeyBasenames {
		paths = append(paths, filepath.Join(home, ".ssh", name))
	}
	return paths
}

// readUsableSSHKey reads and validates a private key file, returning its PEM
// bytes. It errors for missing/unreadable files, non-keys, and
// passphrase-protected keys (which cannot be used headless - the agent covers
// those).
func readUsableSSHKey(path string) ([]byte, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- path is the developer's own key file
	if err != nil {
		return nil, err
	}
	if _, err := ssh.ParsePrivateKey(data); err != nil {
		var missing *ssh.PassphraseMissingError
		if errors.As(err, &missing) {
			return nil, fmt.Errorf("passphrase-protected (load it into ssh-agent instead)")
		}
		return nil, fmt.Errorf("not a usable private key: %w", err)
	}
	return data, nil
}
