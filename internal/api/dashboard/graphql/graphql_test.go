package graphql

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type payload struct {
	CurrentUser struct {
		ID string `json:"id"`
	} `json:"currentUser"`
}

func queryTestServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestQuery_RejectedSession(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		body       string
		wantStatus int
		wantIn     string
	}{
		{
			name:   "unauthenticated with a 200, which is what the dashboard actually sends",
			status: http.StatusOK,
			body:   `{"errors":[{"message":"Unauthorized","extensions":{"code":"UNAUTHENTICATED"}}]}`,
			wantIn: "session has expired",
		},
		{
			name:       "401 from a proxy in front of the dashboard",
			status:     http.StatusUnauthorized,
			body:       "denied by gateway",
			wantStatus: http.StatusUnauthorized,
			wantIn:     "denied by gateway",
		},
		{
			name:       "403 from a proxy",
			status:     http.StatusForbidden,
			body:       "",
			wantStatus: http.StatusForbidden,
			wantIn:     "session has expired",
		},
		{
			name:   "a named cause is kept instead of the bare rejection",
			status: http.StatusOK,
			body:   `{"errors":[{"message":"token is missing the required scope","extensions":{"code":"UNAUTHENTICATED"}}]}`,
			wantIn: "missing the required scope",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := queryTestServer(t, tt.status, tt.body)

			_, err := Query[payload](context.Background(), srv.Client(), srv.URL, "{currentUser{id}}", nil)

			require.Error(t, err)
			var authErr *AuthError
			require.ErrorAs(t, err, &authErr)
			assert.Equal(t, tt.wantStatus, authErr.Status)
			assert.Contains(t, err.Error(), tt.wantIn)
			// Every rejection names the way out, whatever the cause.
			assert.Contains(t, err.Error(), "infracost auth login")
		})
	}
}

// TestQuery_OtherErrorsAreNotAuthErrors keeps an unrelated GraphQL error
// reaching the caller's own message, rather than being rewritten as a login
// problem.
func TestQuery_OtherErrorsAreNotAuthErrors(t *testing.T) {
	srv := queryTestServer(t, http.StatusOK, `{"errors":[{"message":"no associated organization"}]}`)

	r, err := Query[payload](context.Background(), srv.Client(), srv.URL, "{currentUser{id}}", nil)

	require.NoError(t, err)
	require.Len(t, r.Errors, 1)
	assert.Equal(t, "no associated organization", r.Errors[0].Message)
}

func TestQuery_Success(t *testing.T) {
	srv := queryTestServer(t, http.StatusOK, `{"data":{"currentUser":{"id":"user-1"}}}`)

	r, err := Query[payload](context.Background(), srv.Client(), srv.URL, "{currentUser{id}}", nil)

	require.NoError(t, err)
	assert.Equal(t, "user-1", r.Data.CurrentUser.ID)
}

func TestSanitizeBody(t *testing.T) {
	got := sanitizeBody("\x1b[2J\x1b[1;1HLogin at\n\n  https://evil.example\t")

	assert.Equal(t, "[2J [1;1HLogin at https://evil.example", got)
}
