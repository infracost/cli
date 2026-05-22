package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"html"
	"net/http"
	"runtime"
	"sync"
	"syscall"
	"time"

	"github.com/infracost/cli/internal/api/events"
	"github.com/infracost/cli/internal/ui"
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
		oauth2.SetAuthURLParam("icSource", "cli"))

	var wg sync.WaitGroup
	var code string
	var callbackErr error
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
				callbackErr = errors.New("state mismatch")
				writeCallbackPlain(w, "Authentication failed: state mismatch. Please run 'infracost auth login' again.")
			case len(errorCode) > 0:
				denied := parseLoginDenied(errorCode, r.URL.Query().Get("error_description"))
				callbackErr = denied
				writeCallbackDenied(w, denied)
			case len(code) == 0:
				callbackErr = errors.New("no code returned")
				writeCallbackPlain(w, "Authentication failed: no code returned. Please run 'infracost auth login' again.")
			default:
				writeCallbackPlain(w, "Authentication successful. You can close this window and return to your terminal.")
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

	fmt.Printf("Please go to the following URL to log in:\n%s\n", ui.Code(authURL))
	browserCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	browser.WaitAndOpen(browserCtx, authURL, len(caller) > 0)

	wg.Wait() // wait for the server to finish

	switch {
	case errors.Is(serverErr, syscall.EADDRINUSE):
		return nil, nil, fmt.Errorf("callback server error: %w (use --oauth-callback-port or INFRACOST_CLI_OAUTH_CALLBACK_PORT to change the port)", serverErr)
	case serverErr != nil:
		return nil, nil, fmt.Errorf("callback server error: %w", serverErr)
	case callbackErr != nil:
		return nil, nil, callbackErr
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

// writeCallbackPlain renders a minimal HTML page with msg as the body.
// Used for the success and generic-error cases where there's no
// structured payload to translate.
func writeCallbackPlain(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = fmt.Fprintf(w, callbackHTML, html.EscapeString(msg), "")
}

// writeCallbackDenied renders the friendly page for a denied login. The
// unverified-email case gets a tailored message and a hint pointing the
// user at their inbox; everything else falls back to the description
// Auth0 returned.
func writeCallbackDenied(w http.ResponseWriter, e *LoginDeniedError) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if e.UnverifiedEmail {
		_, _ = fmt.Fprintf(w, callbackHTML,
			"Please verify your email",
			"Check your inbox for a verification link from Infracost, then run <code>infracost auth login</code> again.",
		)
		return
	}
	body := "Authentication failed. Please run <code>infracost auth login</code> again."
	if e.Description != "" {
		body = html.EscapeString(e.Description)
	}
	_, _ = fmt.Fprintf(w, callbackHTML, "Authentication failed", body)
}

// callbackHTML is the response template for the localhost OAuth
// callback page. Inlined so the binary doesn't need an embed.FS for one
// small page.
const callbackHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>Infracost</title>
<style>
body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
       background: #F5F5F9; color: #0E0E26; display: flex; align-items: center;
       justify-content: center; height: 100vh; margin: 0; }
.card { background: #fff; border: 1px solid #DCD8E1; border-radius: 8px;
        padding: 32px 40px; max-width: 480px; text-align: center; }
h1 { color: #2A2A5B; margin: 0 0 12px; font-size: 22px; }
p  { color: #55567D; margin: 0; line-height: 1.5; }
code { background: #F5F5F9; padding: 2px 6px; border-radius: 4px;
       font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 14px; }
</style>
</head>
<body>
<div class="card">
<h1>%s</h1>
<p>%s</p>
</div>
</body>
</html>`
