package auth

import (
	"encoding/base64"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
)

func TestParseLoginDenied_UnverifiedEmail(t *testing.T) {
	// Mirrors the payload emitted by
	// infra/modules/auth0/actions/post-login-access-control.js when
	// api.access.deny() fires on an unverified email — base64-encoded
	// JSON with the error name plus CLI redirect metadata.
	payload := `{"error":"unverified_email","source":"cli","cliPort":"8080","cliState":"abc"}`
	encoded := base64.StdEncoding.EncodeToString([]byte(payload))

	e := parseLoginDenied("access_denied", encoded)

	assert.True(t, e.UnverifiedEmail)
	assert.Equal(t, "access_denied", e.Code)
	assert.Contains(t, e.Error(), "verified")
	assert.True(t, IsUnverifiedEmail(e))
	assert.True(t, IsUnverifiedEmail(fmt.Errorf("wrapping: %w", e)))
}

func TestParseLoginDenied_PlainString(t *testing.T) {
	// Mirrors post-login-register.js error path — api.access.deny() is
	// called with a plain support message string when /auth/register
	// fails, so we should pass it through unchanged.
	e := parseLoginDenied("access_denied", "Please email support@infracost.io")

	assert.False(t, e.UnverifiedEmail)
	assert.Equal(t, "Please email support@infracost.io", e.Description)
	assert.Contains(t, e.Error(), "Please email support@infracost.io")
	assert.False(t, IsUnverifiedEmail(e))
}

func TestParseLoginDenied_EmptyDescription(t *testing.T) {
	e := parseLoginDenied("access_denied", "")

	assert.False(t, e.UnverifiedEmail)
	assert.Empty(t, e.Description)
	assert.Contains(t, e.Error(), "access_denied")
}

func TestParseLoginDenied_Base64ButNotJSON(t *testing.T) {
	// A description that happens to be valid base64 but doesn't decode
	// to JSON should fall through to the plain-description path.
	encoded := base64.StdEncoding.EncodeToString([]byte("not json"))

	e := parseLoginDenied("access_denied", encoded)

	assert.False(t, e.UnverifiedEmail)
	assert.Equal(t, encoded, e.Description)
}

func TestLoginDeniedFromOAuthError(t *testing.T) {
	t.Run("converts RetrieveError carrying unverified_email payload", func(t *testing.T) {
		payload := `{"error":"unverified_email"}`
		encoded := base64.StdEncoding.EncodeToString([]byte(payload))

		oauthErr := &oauth2.RetrieveError{
			ErrorCode:        "access_denied",
			ErrorDescription: encoded,
		}

		converted := loginDeniedFromOAuthError(oauthErr)
		var lde *LoginDeniedError
		require.True(t, errors.As(converted, &lde))
		assert.True(t, lde.UnverifiedEmail)
	})

	t.Run("passes through non-OAuth errors unchanged", func(t *testing.T) {
		original := errors.New("boom")
		assert.Equal(t, original, loginDeniedFromOAuthError(original))
	})

	t.Run("returns nil for nil input", func(t *testing.T) {
		assert.Nil(t, loginDeniedFromOAuthError(nil))
	})
}