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
	switch runtime.GOOS {
	case "windows":
		return exec.Command("cmd", "/c", "start", url).Start() // #nosec G204
	case "darwin":
		return exec.Command("open", url).Start() // #nosec G204
	case "linux":
		return exec.Command("xdg-open", url).Start() // #nosec G204
	default:
		return fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}
}

// WaitAndOpen prints a message and waits for the user to press Enter before opening the specified URL.
// It stops waiting and does not open the browser if the context is cancelled.
func WaitAndOpen(ctx context.Context, url string) {
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
