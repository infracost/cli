package trace

import (
	"os"
	"path/filepath"

	"github.com/google/uuid"
	"github.com/infracost/cli/internal/logging"
)

var InstallID string

func init() {
	bytes, err := os.ReadFile(defaultInstallCachePath())
	if err != nil {
		if os.IsNotExist(err) {
			installID := uuid.New().String()
			if err := os.WriteFile(defaultInstallCachePath(), []byte(installID), 0600); err != nil {
				logging.WithError(err).Msg("failed to save install cache file, using default value")
				InstallID = uuid.Nil.String()
				return
			}
			InstallID = installID
			return
		}
		logging.WithError(err).Msg("failed to read install cache file, using default value")
		InstallID = uuid.Nil.String()
		return
	}
	InstallID = string(bytes)
}

func defaultInstallCachePath() string {
	dir, err := os.UserConfigDir()
	if err == nil {
		return filepath.Join(dir, "infracost", "installId")
	}
	logging.WithError(err).Msg("installCachePath: failed to load user config dir, falling back to home directory")

	dir, err = os.UserHomeDir()
	if err == nil {
		return filepath.Join(dir, ".infracost", "installId")
	}

	logging.WithError(err).Msg("installCachePath: failed to load user home dir, falling back to current directory")
	return filepath.Join(".infracost", "installId")
}
