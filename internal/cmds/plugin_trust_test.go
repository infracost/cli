package cmds

import (
	"errors"
	"strings"
	"testing"

	"github.com/charmbracelet/huh"
	"github.com/infracost/cli/pkg/plugins/registry"
)

// stubUnofficialSeams overrides the trust-gate test seams for the duration of
// the test and restores them afterwards. confirm is what promptUnofficialConfirm
// would return on a real TTY.
func stubUnofficialSeams(t *testing.T, interactive bool, confirm func(*registry.Entry) (bool, error)) {
	t.Helper()
	origInteractive, origConfirm := unofficialIsInteractive, unofficialConfirm
	unofficialIsInteractive = func() bool { return interactive }
	unofficialConfirm = confirm
	t.Cleanup(func() {
		unofficialIsInteractive = origInteractive
		unofficialConfirm = origConfirm
	})
}

func officialEntry() *registry.Entry {
	return &registry.Entry{
		Name:       "infracost/kubernetes",
		Official:   true,
		Components: []registry.Component{{Type: registry.ComponentTypeParser}},
	}
}

func unofficialEntry() *registry.Entry {
	return &registry.Entry{
		Name:       "someone/cool-plugin",
		Official:   false,
		Author:     "Someone",
		Homepage:   "https://example.com/cool-plugin",
		Components: []registry.Component{{Type: registry.ComponentTypeParser}},
	}
}

// notCalled fails the test if the confirmation prompt is reached.
func notCalled(t *testing.T) func(*registry.Entry) (bool, error) {
	return func(*registry.Entry) (bool, error) {
		t.Helper()
		t.Fatal("confirmation prompt should not have been reached")
		return false, nil
	}
}

func TestConfirmUnofficialInstall_OfficialNeverPrompts(t *testing.T) {
	// Even non-interactive with no flag, an official entry proceeds silently.
	stubUnofficialSeams(t, false, notCalled(t))

	for _, mode := range []unofficialTrustMode{trustFail, trustSkip} {
		proceed, err := confirmUnofficialInstall(officialEntry(), false, mode)
		if err != nil {
			t.Fatalf("official entry returned error: %v", err)
		}
		if !proceed {
			t.Fatal("official entry should proceed without a prompt")
		}
	}
}

func TestConfirmUnofficialInstall_FlagBypassesPrompt(t *testing.T) {
	// --allow-unofficial proceeds without reaching the prompt, even on a TTY.
	stubUnofficialSeams(t, true, notCalled(t))

	proceed, err := confirmUnofficialInstall(unofficialEntry(), true, trustFail)
	if err != nil {
		t.Fatalf("flag bypass returned error: %v", err)
	}
	if !proceed {
		t.Fatal("--allow-unofficial should proceed")
	}
}

func TestConfirmUnofficialInstall_NonInteractiveFail(t *testing.T) {
	// install / explicit update: non-TTY without the flag is a hard error that
	// names the flag, and never reaches the prompt.
	stubUnofficialSeams(t, false, notCalled(t))

	proceed, err := confirmUnofficialInstall(unofficialEntry(), false, trustFail)
	if proceed {
		t.Fatal("non-interactive install without flag should not proceed")
	}
	if err == nil {
		t.Fatal("expected an error naming --allow-unofficial")
	}
	if !strings.Contains(err.Error(), "--allow-unofficial") {
		t.Fatalf("error should name --allow-unofficial, got: %v", err)
	}
}

func TestConfirmUnofficialInstall_NonInteractiveSkip(t *testing.T) {
	// update-all: non-TTY without the flag skips the entry cleanly (no error)
	// so the rest of the update run continues.
	stubUnofficialSeams(t, false, notCalled(t))

	proceed, err := confirmUnofficialInstall(unofficialEntry(), false, trustSkip)
	if err != nil {
		t.Fatalf("non-interactive skip should not error: %v", err)
	}
	if proceed {
		t.Fatal("non-interactive update-all without flag should skip, not proceed")
	}
}

func TestConfirmUnofficialInstall_InteractiveAccept(t *testing.T) {
	called := false
	stubUnofficialSeams(t, true, func(*registry.Entry) (bool, error) {
		called = true
		return true, nil
	})

	proceed, err := confirmUnofficialInstall(unofficialEntry(), false, trustFail)
	if err != nil {
		t.Fatalf("interactive accept returned error: %v", err)
	}
	if !called {
		t.Fatal("confirmation prompt should have been reached")
	}
	if !proceed {
		t.Fatal("an interactive Yes should proceed")
	}
}

func TestConfirmUnofficialInstall_InteractiveDecline(t *testing.T) {
	stubUnofficialSeams(t, true, func(*registry.Entry) (bool, error) {
		return false, nil
	})

	proceed, err := confirmUnofficialInstall(unofficialEntry(), false, trustFail)
	if err != nil {
		t.Fatalf("a decline is not an error, got: %v", err)
	}
	if proceed {
		t.Fatal("an interactive No should not proceed")
	}
}

func TestConfirmUnofficialInstall_InteractiveAbort(t *testing.T) {
	// Ctrl-C / Esc surfaces as huh.ErrUserAborted and is treated as a decline.
	stubUnofficialSeams(t, true, func(*registry.Entry) (bool, error) {
		return false, huh.ErrUserAborted
	})

	proceed, err := confirmUnofficialInstall(unofficialEntry(), false, trustFail)
	if err != nil {
		t.Fatalf("user abort should be a clean decline, got: %v", err)
	}
	if proceed {
		t.Fatal("aborting the prompt should not proceed")
	}
}

func TestConfirmUnofficialInstall_InteractivePromptError(t *testing.T) {
	// A genuine prompt failure (not an abort) propagates.
	boom := errors.New("tty exploded")
	stubUnofficialSeams(t, true, func(*registry.Entry) (bool, error) {
		return false, boom
	})

	proceed, err := confirmUnofficialInstall(unofficialEntry(), false, trustFail)
	if !errors.Is(err, boom) {
		t.Fatalf("expected the prompt error to propagate, got: %v", err)
	}
	if proceed {
		t.Fatal("a prompt error should not proceed")
	}
}
