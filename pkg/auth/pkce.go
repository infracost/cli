package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"runtime"
	"sync"
	"syscall"
	"time"

	"github.com/infracost/cli/internal/api/events"
	"github.com/infracost/cli/pkg/auth/browser"
	"golang.org/x/oauth2"
)

func (c *Config) PKCE(ctx context.Context) (oauth2.TokenSource, *oauth2.Token, error) {
	caller, _ := events.GetMetadata[string]("caller")

	config := c.OAuth2Config()

	verifier := oauth2.GenerateVerifier()
	state := state()
	authURL := config.AuthCodeURL(state,
		oauth2.S256ChallengeOption(verifier),
		oauth2.SetAuthURLParam("audience", c.Audience),
		oauth2.SetAuthURLParam("icSource", "cliv2"))

	var wg sync.WaitGroup
	var code string
	var errorString string
	var serverErr error

	addr := fmt.Sprintf(":%d", c.CallbackPort)
	if runtime.GOOS == "windows" {
		addr = fmt.Sprintf("localhost:%d", c.CallbackPort)
	}

	wg.Go(func() {
		server := &http.Server{
			Addr: addr,

			// timeouts not strictly necessary for one-time callback service
			// but we need to keep golangci-lint happy
			ReadTimeout:  5 * time.Second,
			WriteTimeout: 10 * time.Second,
			IdleTimeout:  15 * time.Second,
		}

		mux := http.NewServeMux()
		mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
			code = r.URL.Query().Get("code")
			errorCode := r.URL.Query().Get("error")
			returnedState := r.URL.Query().Get("state")

			switch {
			case returnedState != state:
				errorString = "error: state mismatch"
				_, _ = fmt.Fprint(w, errorString)
			case len(errorCode) > 0:
				errorDescription := r.URL.Query().Get("error_description")
				if len(errorDescription) > 0 {
					errorString = fmt.Sprintf("error(%s): %s", errorCode, errorDescription)
				} else {
					errorString = fmt.Sprintf("error(%s)", errorCode)
				}
				_, _ = fmt.Fprint(w, errorString)
			case len(code) == 0:
				_, _ = fmt.Fprintf(w, "Error: no code returned")
			default:
				_, _ = fmt.Fprintf(w, "Success: You can close this window now")
			}

			// We got the code (or at least a request), so we can stop the server.
			// Using a goroutine to avoid deadlocking the handler.
			go func() {
				_ = server.Shutdown(context.Background())
			}()
		})
		server.Handler = mux

		if err := server.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			serverErr = err
		}
	})

	fmt.Printf("Please go to the following URL to log in:\n%s\n", authURL)
	browserCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	browser.WaitAndOpen(browserCtx, authURL, len(caller) > 0)

	wg.Wait() // wait for the server to finish

	switch {
	case errors.Is(serverErr, syscall.EADDRINUSE):
		return nil, nil, fmt.Errorf("callback server error: %w (use --oauth-callback-port or INFRACOST_CLI_OAUTH_CALLBACK_PORT to change the port)", serverErr)
	case serverErr != nil:
		return nil, nil, fmt.Errorf("callback server error: %w", serverErr)
	case len(errorString) > 0:
		return nil, nil, fmt.Errorf("callback server returned %s", errorString)
	case len(code) == 0:
		return nil, nil, errors.New("callback server did not return a code")
	}

	token, err := config.Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to exchange code for token: %w", err)
	}

	return config.TokenSource(ctx, token), token, nil
}

func state() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return base64.URLEncoding.EncodeToString(b)
}
