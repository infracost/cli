package auth

import "golang.org/x/oauth2"

var (
	_ oauth2.TokenSource = AuthenticationToken("")
)

// AuthenticationToken is a token source that returns a constant token with the given value. This should be used
// for Service Account Tokens and Personal Access Tokens, so the rest of the CLI doesn't know the difference between
// what style of auth is being used.
type AuthenticationToken string

func (a AuthenticationToken) Token() (*oauth2.Token, error) {
	return &oauth2.Token{AccessToken: string(a)}, nil
}
