package cmds

import (
	"context"
	"errors"
	"testing"

	"github.com/infracost/cli/internal/config"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/oauth2"
)

// stubTokenSource is a canned oauth2.TokenSource for exercising the auth
// gate without a real login flow.
type stubTokenSource struct {
	token *oauth2.Token
	err   error
}

func (s stubTokenSource) Token() (*oauth2.Token, error) {
	return s.token, s.err
}

func toolCallRequest(name string) mcp.Request {
	return &mcp.ServerRequest[*mcp.CallToolParamsRaw]{
		Params: &mcp.CallToolParamsRaw{Name: name},
	}
}

// runGate invokes requireSessionReadyMiddleware around a next handler that
// records whether it ran, and returns the (result, called) pair.
func runGate(t *testing.T, cfg *config.Config, source oauth2.TokenSource, method, tool string) (mcp.Result, bool) {
	t.Helper()

	called := false
	next := func(context.Context, string, mcp.Request) (mcp.Result, error) {
		called = true
		return &mcp.CallToolResult{}, nil
	}

	handler := requireSessionReadyMiddleware(cfg, source)(next)
	res, err := handler(context.Background(), method, toolCallRequest(tool))
	if err != nil {
		t.Fatalf("gate returned protocol error, want a tool-error result: %v", err)
	}
	return res, called
}

func isErrorResult(res mcp.Result) bool {
	ctr, ok := res.(*mcp.CallToolResult)
	return ok && ctr.IsError
}

func TestRequireSessionReady_AuthFailureBlocksEveryTool(t *testing.T) {
	cfg := &config.Config{OrgID: "org-1"}
	source := stubTokenSource{err: errors.New("not logged in — run 'infracost auth login'")}

	// Even a recovery tool needs a token, so auth failure blocks all tools.
	for _, tool := range []string{"scan", "whoami", "fetch_orgs"} {
		res, called := runGate(t, cfg, source, "tools/call", tool)
		if called {
			t.Errorf("tool %q: next handler ran despite auth failure", tool)
		}
		if !isErrorResult(res) {
			t.Errorf("tool %q: want IsError tool result, got %#v", tool, res)
		}
	}
}

func TestRequireSessionReady_PassesThroughWhenAuthAndOrgResolved(t *testing.T) {
	cfg := &config.Config{OrgID: "org-1"}
	source := stubTokenSource{token: &oauth2.Token{AccessToken: "tok"}}

	res, called := runGate(t, cfg, source, "tools/call", "scan")
	if !called {
		t.Fatal("next handler did not run for an authenticated, org-resolved call")
	}
	if isErrorResult(res) {
		t.Fatalf("unexpected error result: %#v", res)
	}
}

func TestRequireSessionReady_RecoveryToolsSkipOrgGate(t *testing.T) {
	// No org resolved (OrgID empty). Recovery tools must still pass through
	// so an agent can call fetch_orgs / set_org (or the user can switch
	// orgs) to get unstuck. A non-recovery tool would instead fall into
	// resolveOrg here.
	cfg := &config.Config{}
	source := stubTokenSource{token: &oauth2.Token{AccessToken: "tok"}}

	for _, tool := range []string{"whoami", "fetch_orgs", "set_org", "doctor"} {
		res, called := runGate(t, cfg, source, "tools/call", tool)
		if !called {
			t.Errorf("recovery tool %q: next handler did not run with no org selected", tool)
		}
		if isErrorResult(res) {
			t.Errorf("recovery tool %q: unexpected error result: %#v", tool, res)
		}
	}
}

func TestRequireSessionReady_IgnoresNonToolCalls(t *testing.T) {
	// A failing token source would block a tools/call, but other methods
	// (e.g. the initialize handshake) must pass through untouched so the
	// connection can be established before any auth is required.
	cfg := &config.Config{}
	source := stubTokenSource{err: errors.New("not logged in")}

	_, called := runGate(t, cfg, source, "initialize", "")
	if !called {
		t.Fatal("non tools/call request was blocked by the session gate")
	}
}
