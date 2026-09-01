package cmds

import (
	"errors"
	"fmt"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/infracost/cli/internal/ui"
	"github.com/infracost/cli/pkg/plugins/registry"
)

// unofficialTrustMode selects how the trust gate treats a non-interactive run
// (no usable TTY) that has not passed --allow-unofficial.
type unofficialTrustMode int

const (
	// trustFail — `plugin install` and explicit `plugin update <name>`: a
	// non-interactive run without --allow-unofficial is a hard error. The user
	// named a single plugin to (re)install, so there is nothing else to fall
	// back to; refusing loudly is better than silently doing nothing.
	trustFail unofficialTrustMode = iota
	// trustSkip — `plugin update` with no name (update-all): a non-interactive
	// run without the flag skips this one entry with a warning so the other
	// plugins still update. The skip must not affect the command's exit code.
	trustSkip
)

// Test seams. The real implementations talk to the controlling terminal, which
// unit tests can't drive, so they are indirected through package vars that the
// tests in plugin_trust_test.go override. Production code never reassigns them.
var (
	unofficialIsInteractive = ui.IsInteractive
	unofficialConfirm       = promptUnofficialConfirm
)

// confirmUnofficialInstall is the trust gate that must pass before any
// component artifact of an unofficial (`official: false`) registry entry is
// downloaded. It is called once per entry by every download-producing plugin
// operation (install, update); no trust decision is ever persisted, so each
// new download of unreviewed native code re-prompts.
//
// It returns (proceed, err):
//
//   - Official entries return (true, nil) with no prompt — Infracost vouches
//     for them.
//   - --allow-unofficial returns (true, nil), skipping both the prompt and the
//     non-interactive gate for scripted use. A generic --yes-style flag cannot
//     reach this function; only the dedicated flag or an interactive Yes does.
//   - On a usable TTY the loud warning is shown and an explicit Yes is required.
//     Yes ⇒ (true, nil); No or Ctrl-C/Esc ⇒ (false, nil) — a decline is not an
//     error, callers abort cleanly with exit 0.
//   - Without a usable TTY and without the flag the mode decides: trustFail
//     returns (false, err) naming --allow-unofficial; trustSkip warns and
//     returns (false, nil) so update-all continues with the other entries.
func confirmUnofficialInstall(e *registry.Entry, allowUnofficial bool, mode unofficialTrustMode) (proceed bool, err error) {
	// Official plugins are reviewed by Infracost — never gated. Handled before
	// the flag so a flipped-to-unofficial manifest simply starts prompting.
	if e.Official {
		return true, nil
	}

	// The explicit opt-in bypasses the prompt and the non-interactive gate.
	if allowUnofficial {
		return true, nil
	}

	if !unofficialIsInteractive() {
		if mode == trustSkip {
			ui.Warnf("Skipping unofficial plugin %s — it can't be confirmed without a terminal. Pass %s to install it non-interactively.",
				ui.Accent(e.Name), ui.Code("--allow-unofficial"))
			return false, nil
		}
		return false, fmt.Errorf("%s is an unofficial plugin and cannot be installed without confirmation in a non-interactive terminal — pass --allow-unofficial to proceed", e.Name)
	}

	// Interactive: show the loud warning, then require an explicit Yes.
	renderUnofficialWarning(e)
	confirmed, err := unofficialConfirm(e)
	if err != nil {
		// Ctrl-C / Esc at the prompt is a decline, not a failure.
		if errors.Is(err, huh.ErrUserAborted) {
			return false, nil
		}
		return false, err
	}
	return confirmed, nil
}

// renderUnofficialWarning prints the loud, boxed warning that precedes the
// confirmation prompt for an unofficial entry. It names the author and homepage
// from the manifest so the user can vet the plugin, states the native-code
// risk, and lists the component types that would be installed.
func renderUnofficialWarning(e *registry.Entry) {
	author := e.Author
	if author == "" {
		author = "unknown"
	}
	homepage := e.Homepage
	if homepage == "" {
		homepage = "unknown"
	}

	var b strings.Builder
	b.WriteString(ui.Bold(ui.Caution("Unofficial plugin — not reviewed by Infracost")))
	b.WriteString("\n\n")
	fmt.Fprintf(&b, "%s is not authored or reviewed by Infracost.\n", ui.Accent(e.Name))
	fmt.Fprintf(&b, "%s %s\n", ui.Muted("Author:  "), author)
	fmt.Fprintf(&b, "%s %s\n", ui.Muted("Homepage:"), ui.Code(homepage))
	b.WriteByte('\n')
	b.WriteString(ui.Caution("Plugins run as native code on your machine with your permissions."))
	b.WriteByte('\n')
	b.WriteString("Infracost cannot vouch for its safety or behavior.")
	b.WriteByte('\n')
	b.WriteByte('\n')
	fmt.Fprintf(&b, "%s %s", ui.Muted("Will install:"), e.Capabilities())

	fmt.Println()
	fmt.Print(ui.Box(b.String()))
}

// promptUnofficialConfirm shows the TTY confirmation for an unofficial entry,
// defaulting to No. It follows the huh.NewConfirm + ui.BrandTheme convention
// used elsewhere (see runSetupStep in setup.go).
func promptUnofficialConfirm(e *registry.Entry) (bool, error) {
	// confirm starts false, so the No button is the default selection.
	var confirm bool
	err := huh.NewConfirm().
		Title(fmt.Sprintf("Install %s anyway?", e.Name)).
		Description("This downloads and runs third-party code with your permissions.").
		Affirmative("Yes, install").
		Negative("No, cancel").
		Value(&confirm).
		WithTheme(ui.BrandTheme()).
		Run()
	if err != nil {
		return false, err
	}
	return confirm, nil
}
