package browser

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"time"
)

// Open opens the specified URL in the user's default browser.
func Open(url string) error {
	cmd, err := openCommand(runtime.GOOS, url)
	if err != nil {
		return err
	}

	return cmd.Start()
}

func openCommand(goos, url string) (*exec.Cmd, error) {
	switch goos {
	case "windows":
		// Avoid cmd.exe parsing of URL query separators such as '&'.
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url), nil // #nosec G204
	case "darwin":
		return exec.Command("open", url), nil // #nosec G204
	case "linux":
		return exec.Command("xdg-open", url), nil // #nosec G204
	default:
		return nil, fmt.Errorf("unsupported platform: %s", goos)
	}
}

// WaitAndOpen prints a message and waits for the user to press Enter before opening the specified URL.
// It stops waiting and does not open the browser if the context is canceled.
func WaitAndOpen(ctx context.Context, url string, automatic bool) {
	if automatic {
		err := Open(url)
		if err != nil {
			fmt.Printf("Failed to open browser: %v\n", err)
			fmt.Println("Please open the above URL manually.")
		}
		return
	}

	fmt.Printf("\nPress Enter to open the browser automatically...\n")

	go func() {
		// Poll stdin with a short deadline so this goroutine can exit when ctx
		// is cancelled (e.g. the OAuth callback fires before Enter is pressed).
		// A blocked stdin read cannot be interrupted, and the orphan would
		// race subsequent TUI prompts for input bytes.
		defer func() { _ = os.Stdin.SetReadDeadline(time.Time{}) }()

		buf := make([]byte, 1)
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}

			_ = os.Stdin.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
			n, err := os.Stdin.Read(buf)
			if err != nil {
				if errors.Is(err, os.ErrDeadlineExceeded) {
					continue
				}
				return
			}
			if n > 0 && buf[0] == '\n' {
				if err := Open(url); err != nil {
					fmt.Printf("Failed to open browser: %v\n", err)
					fmt.Println("Please open the above URL manually.")
				}
				return
			}
		}
	}()
}
