package browser

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
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
		input := make(chan struct{})
		go func() {
			_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
			close(input)
		}()

		select {
		case <-input:
			err := Open(url)
			if err != nil {
				fmt.Printf("Failed to open browser: %v\n", err)
				fmt.Println("Please open the above URL manually.")
			}
		case <-ctx.Done():
		}
	}()
}
