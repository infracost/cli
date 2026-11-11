package cmds

import (
	"context"
	"errors"
	"fmt"

	"github.com/charmbracelet/huh"
	"github.com/infracost/cli/internal/config"
	"github.com/infracost/cli/internal/ui"
	pkgscanner "github.com/infracost/cli/pkg/scanner"
)

// errBaselineSaved ends a `diff` that had no baseline and made one. It is not
// a failure, so the command prints what to do next and exits zero.
var errBaselineSaved = errors.New("baseline saved")

// offerBaselineScan handles a `diff` with nothing cached to compare against.
//
// Unlike inspect, scanning now cannot produce the output the user asked for:
// the scan would be of the same code, so the diff would be empty. What it can
// do is leave them with the baseline they were missing, which is the step the
// old error only described. Non-interactive runs keep that error.
func offerBaselineScan(ctx context.Context, cfg *config.Config, absPath, displayPath string, pluginOpts pkgscanner.PluginOpts) (bool, error) {
	if !ui.IsInteractive() {
		return false, nil
	}

	var confirm bool
	if err := huh.NewConfirm().
		Title(fmt.Sprintf("Nothing has been scanned yet, so there is no baseline. Scan %s now and save it as the baseline?", displayPath)).
		Affirmative("Yes").
		Negative("No").
		Value(&confirm).
		WithTheme(ui.BrandTheme()).
		Run(); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return false, nil
		}
		return false, err
	}
	if !confirm {
		return false, nil
	}

	// Labeled apart from a real diff: this run produces a baseline, and
	// counting it as a diff would inflate diff adoption.
	if _, err := scanNow(ctx, cfg, absPath, "diff-baseline", pluginOpts); err != nil {
		return false, err
	}

	fmt.Println()
	ui.Successf("Baseline saved for %s", displayPath)
	ui.Stepf("Change your code, then run `infracost diff %s` to see what it costs", displayPath)
	return true, nil
}
