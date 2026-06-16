package auth

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
)

// fakeTokenSource returns the next entry in `results` on each Token()
// call. If calls outrun the slice the test fails — callers should
// program exactly as many results as they expect calls.
type fakeTokenSource struct {
	mu      sync.Mutex
	results []tokenResult
	calls   int
}

type tokenResult struct {
	tok *oauth2.Token
	err error
}

func (f *fakeTokenSource) Token() (*oauth2.Token, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.calls >= len(f.results) {
		return nil, fmt.Errorf("fakeTokenSource: unexpected call %d", f.calls+1)
	}
	r := f.results[f.calls]
	f.calls++
	return r.tok, r.err
}

func (f *fakeTokenSource) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func invalidGrant() error {
	return &oauth2.RetrieveError{ErrorCode: "invalid_grant"}
}

func TestIsInvalidGrant(t *testing.T) {
	assert.True(t, isInvalidGrant(invalidGrant()))
	assert.True(t, isInvalidGrant(fmt.Errorf("wrapped: %w", invalidGrant())))
	assert.False(t, isInvalidGrant(&oauth2.RetrieveError{ErrorCode: "invalid_client"}))
	assert.False(t, isInvalidGrant(errors.New("network down")))
	assert.False(t, isInvalidGrant(nil))
}

func TestRereadingTokenSource_PassesThroughSuccess(t *testing.T) {
	inner := &fakeTokenSource{results: []tokenResult{
		{tok: &oauth2.Token{AccessToken: "ok"}},
	}}
	reloaded := false
	r := &rereadingTokenSource{
		inner: inner,
		reload: func() (oauth2.TokenSource, error) {
			reloaded = true
			return nil, nil
		},
	}

	tok, err := r.Token()
	require.NoError(t, err)
	assert.Equal(t, "ok", tok.AccessToken)
	assert.False(t, reloaded, "successful call should not trigger reload")
}

func TestRereadingTokenSource_NonInvalidGrantSurfaces(t *testing.T) {
	netErr := errors.New("network down")
	inner := &fakeTokenSource{results: []tokenResult{{err: netErr}}}
	reloaded := false
	r := &rereadingTokenSource{
		inner: inner,
		reload: func() (oauth2.TokenSource, error) {
			reloaded = true
			return nil, nil
		},
	}

	_, err := r.Token()
	assert.ErrorIs(t, err, netErr)
	assert.False(t, reloaded, "non-invalid_grant errors should not trigger reload")
}

func TestRereadingTokenSource_InvalidGrantTriggersReloadAndRetry(t *testing.T) {
	stale := &fakeTokenSource{results: []tokenResult{{err: invalidGrant()}}}
	fresh := &fakeTokenSource{results: []tokenResult{
		{tok: &oauth2.Token{AccessToken: "fresh"}},
	}}
	r := &rereadingTokenSource{
		inner: stale,
		reload: func() (oauth2.TokenSource, error) {
			return fresh, nil
		},
	}

	tok, err := r.Token()
	require.NoError(t, err)
	assert.Equal(t, "fresh", tok.AccessToken)
	assert.Equal(t, 1, stale.callCount())
	assert.Equal(t, 1, fresh.callCount())
	assert.Same(t, fresh, r.inner, "inner should be swapped to the reloaded source")
}

func TestRereadingTokenSource_ReloadErrorSurfacesOriginal(t *testing.T) {
	orig := invalidGrant()
	stale := &fakeTokenSource{results: []tokenResult{{err: orig}}}
	r := &rereadingTokenSource{
		inner: stale,
		reload: func() (oauth2.TokenSource, error) {
			return nil, errors.New("cache read failed")
		},
	}

	_, err := r.Token()
	// Surface the original invalid_grant — the user needs the auth
	// error, not the cache plumbing failure.
	assert.Same(t, orig, err)
}

func TestRereadingTokenSource_ReloadNilSurfacesOriginal(t *testing.T) {
	orig := invalidGrant()
	stale := &fakeTokenSource{results: []tokenResult{{err: orig}}}
	r := &rereadingTokenSource{
		inner: stale,
		reload: func() (oauth2.TokenSource, error) {
			return nil, nil
		},
	}

	_, err := r.Token()
	assert.Same(t, orig, err)
}

func TestRereadingTokenSource_ConcurrentInvalidGrantReloadsOnce(t *testing.T) {
	const callers = 10
	staleResults := make([]tokenResult, callers)
	for i := range staleResults {
		staleResults[i] = tokenResult{err: invalidGrant()}
	}
	stale := &fakeTokenSource{results: staleResults}

	freshResults := make([]tokenResult, callers)
	for i := range freshResults {
		freshResults[i] = tokenResult{tok: &oauth2.Token{AccessToken: "fresh"}}
	}
	fresh := &fakeTokenSource{results: freshResults}

	var reloads int32
	r := &rereadingTokenSource{
		inner: stale,
		reload: func() (oauth2.TokenSource, error) {
			atomic.AddInt32(&reloads, 1)
			return fresh, nil
		},
	}

	var wg sync.WaitGroup
	wg.Add(callers)
	for range callers {
		go func() {
			defer wg.Done()
			tok, err := r.Token()
			assert.NoError(t, err)
			if tok != nil {
				assert.Equal(t, "fresh", tok.AccessToken)
			}
		}()
	}
	wg.Wait()

	assert.Equal(t, int32(1), atomic.LoadInt32(&reloads),
		"concurrent invalid_grants should reload at most once")
}