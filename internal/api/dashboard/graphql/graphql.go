package graphql

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"unicode"
)

type Response[T any] struct {
	Data   T       `json:"data"`
	Errors []Error `json:"errors,omitempty"`
}

type Error struct {
	Message    string     `json:"message"`
	Extensions Extensions `json:"extensions,omitempty"`
}

// Extensions carries the GraphQL error code. UNAUTHENTICATED is the one the
// CLI acts on: the dashboard answers a rejected token with 200 and this code,
// so the HTTP status alone never reveals it.
type Extensions struct {
	Code string `json:"code"`
}

// AuthError reports a rejected session. Typed rather than a plain error
// because the only useful response is to log in again, and the bare
// "Unauthorized" this replaces said nothing about that — 28% of the people who
// saw it never scanned again, and the ones who recovered took 6.4 hours.
type AuthError struct {
	// Status is 0 when the dashboard answered 200 with an UNAUTHENTICATED
	// error, which is what an expired session actually produces.
	Status int
	// Message is the dashboard's own text, when there was one.
	Message string
}

type Request struct {
	Query     string                 `json:"query"`
	Variables map[string]interface{} `json:"variables,omitempty"`
}

const (
	unauthenticatedCode = "UNAUTHENTICATED"

	// maxErrorBody caps what is quoted back from a 401/403 body, which comes
	// from whatever is in front of the dashboard and is not bounded.
	maxErrorBody = 1024

	// LoginHint is what to do about a rejected session. Both halves are named
	// because the fix differs: a person logs in again, a pipeline or an agent
	// sets the token.
	LoginHint = "run 'infracost auth login' to log in again " +
		"(non-interactive: set INFRACOST_CLI_AUTHENTICATION_TOKEN)"
)

func (e *AuthError) Error() string {
	cause := "your Infracost session has expired or was revoked"
	if e.Status != 0 {
		cause = strings.TrimSpace(fmt.Sprintf("%s (%d %s)", cause, e.Status, http.StatusText(e.Status)))
	}

	// Only a bare "Unauthorized" leaves the cause open. Anything else — a
	// missing scope, a suspended org, a proxy's own refusal — names it better
	// than the hint does.
	if e.Message != "" && !bareRejection(e.Message) {
		return fmt.Sprintf("%s: %s. %s", cause, e.Message, LoginHint)
	}

	return cause + ". " + LoginHint
}

// bareRejection reports a message that says only that the token was rejected.
func bareRejection(msg string) bool {
	m := strings.ToLower(strings.TrimSpace(msg))
	m = strings.TrimSuffix(m, ".")
	return m == "" || m == "unauthorized" || m == "forbidden"
}

func Query[T any](ctx context.Context, client *http.Client, endpoint string, query string, variables map[string]interface{}) (Response[T], error) {
	request := Request{
		Query:     query,
		Variables: variables,
	}

	bytes := new(bytes.Buffer)
	if err := json.NewEncoder(bytes).Encode(request); err != nil {
		return Response[T]{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes)
	if err != nil {
		return Response[T]{}, err
	}

	req.Header.Set("Content-Type", "application/json")
	r, err := client.Do(req) // #nosec G704 -- request target originates from config file
	if err != nil {
		return Response[T]{}, err
	}
	defer func() {
		_ = r.Body.Close()
	}()

	// The dashboard answers a rejected token with 200, so a status here comes
	// from a proxy in front of it. Its body is the only thing naming the
	// cause, so it is carried.
	if r.StatusCode == http.StatusUnauthorized || r.StatusCode == http.StatusForbidden {
		return Response[T]{}, &AuthError{Status: r.StatusCode, Message: bodyMessage(r.Body)}
	}

	var response Response[T]
	if err := json.NewDecoder(r.Body).Decode(&response); err != nil {
		return Response[T]{}, err
	}

	if authErr := authError(response.Errors); authErr != nil {
		return Response[T]{}, authErr
	}

	return response, nil
}

// authError reports the UNAUTHENTICATED entry in a GraphQL error list, which
// is what an expired or revoked session produces. The first entry that names a
// cause wins over an earlier bare "Unauthorized".
func authError(errs []Error) *AuthError {
	var first *AuthError
	for _, e := range errs {
		if e.Extensions.Code != unauthenticatedCode {
			continue
		}
		if !bareRejection(e.Message) {
			return &AuthError{Message: e.Message}
		}
		if first == nil {
			first = &AuthError{Message: e.Message}
		}
	}
	return first
}

// bodyMessage is the responder's own text for a status rejection: a proxy or
// gateway, which names a cause the dashboard's own error codes cannot.
func bodyMessage(body io.Reader) string {
	b, err := io.ReadAll(io.LimitReader(body, maxErrorBody))
	if err != nil {
		return ""
	}
	return sanitizeBody(string(b))
}

// sanitizeBody drops the control bytes in a response body, which is written to
// a terminal that would otherwise execute the ANSI escapes in it, and collapses
// the whitespace an HTML error page is mostly made of.
func sanitizeBody(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	space := false
	for _, r := range s {
		switch {
		case r < 0x20 || r == 0x7f || unicode.IsSpace(r):
			space = b.Len() > 0
		default:
			if space {
				b.WriteRune(' ')
				space = false
			}
			b.WriteRune(r)
		}
	}
	return b.String()
}
