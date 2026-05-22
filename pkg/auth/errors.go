package auth

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"

	"golang.org/x/oauth2"
)

// LoginDeniedError is returned when Auth0 denies a login attempt (the
// PKCE callback receives error=access_denied, or the device-flow token
// exchange returns an OAuth error response).
//
// The Auth0 post-login actions sometimes encode a structured payload in
// the error description — see infra/modules/auth0/actions/post-login-access-control.js
// for the unverified-email case. When that payload is present and decodes
// cleanly, the typed fields below are populated; otherwise Description
// holds the raw string Auth0 returned.
type LoginDeniedError struct {
	Code            string
	Description     string
	UnverifiedEmail bool
}

func (e *LoginDeniedError) Error() string {
	if e.UnverifiedEmail {
		return "your email isn't verified yet — check your inbox for a verification link from Infracost, then run 'infracost auth login' again"
	}
	if e.Description != "" {
		return fmt.Sprintf("login was denied: %s", e.Description)
	}
	return fmt.Sprintf("login was denied (%s)", e.Code)
}

// IsUnverifiedEmail reports whether err (or any error it wraps) is a
// login-denied error caused by an unverified email.
func IsUnverifiedEmail(err error) bool {
	var lde *LoginDeniedError
	if errors.As(err, &lde) {
		return lde.UnverifiedEmail
	}
	return false
}

// parseLoginDenied builds a LoginDeniedError from the OAuth error code
// and description returned by Auth0. The description may be:
//
//   - a base64-encoded JSON payload like {"error":"unverified_email",...}
//     produced by api.access.deny() in the post-login-access-control action;
//   - a plain string (e.g. the support message from the register action
//     when /auth/register fails);
//   - empty.
//
// Callers should not rely on a particular shape beyond the typed fields
// below; new payload shapes added on the Auth0 side won't break this.
func parseLoginDenied(code, description string) *LoginDeniedError {
	e := &LoginDeniedError{Code: code, Description: description}

	if description == "" {
		return e
	}

	decoded, err := base64.StdEncoding.DecodeString(description)
	if err != nil {
		return e
	}

	var payload struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(decoded, &payload); err != nil {
		return e
	}

	if payload.Error == "unverified_email" {
		e.UnverifiedEmail = true
		// Replace the opaque base64 blob with the parsed error name so any
		// fall-through logging is human-readable.
		e.Description = payload.Error
	}

	return e
}

// loginDeniedFromOAuthError inspects an error returned by the oauth2
// library (e.g. from DeviceAccessToken) and, if it carries an OAuth
// error response, converts it to a LoginDeniedError. Returns the
// original error untouched if it's not a recognized OAuth error.
func loginDeniedFromOAuthError(err error) error {
	if err == nil {
		return nil
	}
	var oe *oauth2.RetrieveError
	if errors.As(err, &oe) {
		return parseLoginDenied(oe.ErrorCode, oe.ErrorDescription)
	}
	return err
}