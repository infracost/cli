package cmds

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/charmbracelet/huh"
	"github.com/infracost/cli/internal/api/events"
	"github.com/infracost/cli/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type ciSetupEvent struct {
	name  string
	extra []interface{}
}

type ciSetupEventRecorder struct {
	events []ciSetupEvent
}

func (r *ciSetupEventRecorder) Push(_ context.Context, name string, extra ...interface{}) {
	r.events = append(r.events, ciSetupEvent{name: name, extra: extra})
}

// field reads a key's value out of the recorded key-value pairs.
func (e ciSetupEvent) field(key string) (interface{}, bool) {
	for i := 0; i+1 < len(e.extra); i += 2 {
		if e.extra[i] == key {
			return e.extra[i+1], true
		}
	}
	return nil, false
}

func ciSetupEventsConfig(recorder *ciSetupEventRecorder) *config.Config {
	cfg := new(config.Config)
	cfg.Events.ClientFn = func(*http.Client) events.Client { return recorder }
	return cfg
}

func TestPushCISetupOutcome(t *testing.T) {
	recorder := new(ciSetupEventRecorder)
	result := CISetupResult{
		Outcome:    "configured",
		Configured: true,
		Platform:   "github",
		startedAt:  time.Now().Add(-time.Second),
	}

	pushCISetupOutcome(context.Background(), ciSetupEventsConfig(recorder), &result, nil)

	require.Len(t, recorder.events, 1)
	assert.Equal(t, "infracost-ci-setup-outcome", recorder.events[0].name)

	for key, want := range map[string]interface{}{
		"outcome": "configured", "configured": true, "targetCIPlatform": "github",
		"stage": ciStagePreflight,
	} {
		got, ok := recorder.events[0].field(key)
		if assert.True(t, ok, key) {
			assert.Equal(t, want, got, key)
		}
	}

	_, overlaid := recorder.events[0].field("ciPlatform")
	assert.False(t, overlaid, "ciPlatform is the runner we are in, not the setup target")

	_, sentMessage := recorder.events[0].field("errorMessage")
	assert.False(t, sentMessage, "error text must never be sent")

	duration, ok := recorder.events[0].field("durationSeconds")
	require.True(t, ok)
	seconds, ok := duration.(float64)
	require.True(t, ok, "durationSeconds must be a float64")
	assert.Greater(t, seconds, 0.5, "duration is measured from startedAt")
	assert.Less(t, seconds, 60.0, "duration must not be measured from the zero time")
}

// TestPushCISetupOutcome_UnsetStartedAt guards the case where a result reaches
// the event without startedAt set, which would report ~1.7e9 seconds.
func TestPushCISetupOutcome_UnsetStartedAt(t *testing.T) {
	recorder := new(ciSetupEventRecorder)
	result := CISetupResult{Outcome: "configured"}

	pushCISetupOutcome(context.Background(), ciSetupEventsConfig(recorder), &result, nil)

	require.Len(t, recorder.events, 1)
	duration, _ := recorder.events[0].field("durationSeconds")
	assert.Equal(t, 0.0, duration, "no duration when startedAt is unset")
}

// TestPushCISetupEventBounded pins the inline sends: the start event runs
// ahead of interactive work, so a slow endpoint must not stall the command.
func TestPushCISetupEventBounded(t *testing.T) {
	cfg := new(config.Config)
	cfg.Events.ClientFn = func(*http.Client) events.Client { return blockingEventClient{} }

	start := time.Now()
	pushCISetupEvent(context.Background(), cfg, "infracost-ci-setup-start")

	assert.Less(t, time.Since(start), ciSetupEventTimeout+250*time.Millisecond)
}

type blockingEventClient struct{}

func (blockingEventClient) Push(ctx context.Context, _ string, _ ...interface{}) {
	<-ctx.Done()
}

func TestCISetupErrorKind(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "nil", err: nil, want: ""},
		{name: "platform not selected", err: errCIPlatformNotSelected, want: "platform-not-selected"},
		{name: "platform not selected wrapped", err: fmt.Errorf("resolving: %w", errCIPlatformNotSelected), want: "platform-not-selected"},
		{name: "unknown platform", err: fmt.Errorf("%w %q", errCIPlatformUnknown, "nope"), want: "platform-unknown"},
		{name: "not a git repo", err: fmt.Errorf("%w — run this from a repo", errCINotGitRepo), want: "not-git-repo"},
		{name: "no git remote", err: fmt.Errorf("%w — add an origin", errCINoGitRemote), want: "no-git-remote"},
		{name: "unparsed remote", err: fmt.Errorf("%w %q", errCIRemoteURLUnparsed, "nope"), want: "remote-url-unparsed"},
		{name: "no orgs", err: fmt.Errorf("%w for this account", errCINoOrganizations), want: "no-organizations"},
		{name: "org not found", err: fmt.Errorf("%w: %q", errCIOrgNotFound, "acme"), want: "org-not-found"},
		{name: "api key missing", err: fmt.Errorf("KEY: %w", errCIAPIKeyMissing), want: "api-key-missing"},
		{name: "not interactive", err: fmt.Errorf("cannot confirm: %w", errCINotInteractive), want: "not-interactive"},
		{name: "user aborted", err: fmt.Errorf("prompt: %w", huh.ErrUserAborted), want: "aborted"},
		{name: "canceled", err: context.Canceled, want: "canceled"},
		{name: "deadline", err: fmt.Errorf("calling api: %w", context.DeadlineExceeded), want: "canceled"},
		{name: "permission denied", err: fmt.Errorf("writing /home/me/acme/ci.yml: %w", os.ErrPermission), want: "permission-denied"},
		{name: "not exist", err: fmt.Errorf("reading /home/me/acme/ci.yml: %w", os.ErrNotExist), want: "file-not-found"},
		{name: "exists", err: fmt.Errorf("creating /home/me/acme/ci.yml: %w", os.ErrExist), want: "file-exists"},
		{name: "network", err: fmt.Errorf("posting: %w", &net.DNSError{IsTimeout: true}), want: "network"},
		{name: "unclassified", err: errors.New(`.github/workflows/ci.yml already defines "deploy-acme-prod"`), want: "other"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, CISetupErrorKind(tt.err))
		})
	}
}

// TestCISetupErrorKindLeaksNothing is the guard for the whole scheme: whatever
// the error says, only a bounded label reaches the event.
func TestCISetupErrorKindLeaksNothing(t *testing.T) {
	allowed := map[string]bool{
		"": true, "platform-not-selected": true, "platform-unknown": true,
		"not-git-repo": true, "no-git-remote": true, "remote-url-unparsed": true,
		"no-organizations": true, "org-not-found": true, "api-key-missing": true,
		"not-interactive": true, "aborted": true, "canceled": true,
		"permission-denied": true, "file-not-found": true, "file-exists": true,
		"network": true, "other": true,
	}
	secrets := []error{
		fmt.Errorf("writing /Users/owen/code/acme-prod-infra/.github/workflows/infracost.yml: %w", os.ErrPermission),
		errors.New(`.github/workflows/ci.yml already defines "deploy-acme-prod-account" at line 42`),
		errors.New("ics_live_abc123 rejected"),
	}
	for _, err := range secrets {
		kind := CISetupErrorKind(err)
		assert.True(t, allowed[kind], "unexpected label %q", kind)
	}
}

// secretFragments are the shapes a leak would take: a local path, a job name
// out of the user's YAML, a credential.
var secretFragments = []string{
	"/Users/", "/home/", "acme-prod-infra", "deploy-acme-prod-account", "ics_live_abc123",
	".github/workflows", "infracost.yml", "line 42",
}

// assertNoLeak fails if any value in an event payload carries a fragment of
// the error that produced it.
func assertNoLeak(t *testing.T, event ciSetupEvent) {
	t.Helper()
	for _, value := range event.extra {
		s, ok := value.(string)
		if !ok {
			continue
		}
		for _, fragment := range secretFragments {
			assert.NotContains(t, s, fragment, "event field %q carries user data", s)
		}
	}
}

// TestPushCISetupOutcomeNeverSendsErrorText drives the real payload builder
// with errors that carry paths, job names and a credential, then checks the
// event for fragments of each. Appending err.Error() to the extras fails it.
func TestPushCISetupOutcomeNeverSendsErrorText(t *testing.T) {
	errs := []error{
		fmt.Errorf("writing /Users/owen/code/acme-prod-infra/.github/workflows/infracost.yml: %w", os.ErrPermission),
		errors.New(`.github/workflows/ci.yml already defines "deploy-acme-prod-account" at line 42`),
		errors.New("ics_live_abc123 rejected"),
		fmt.Errorf("reading /home/ci/acme/.gitlab-ci.yml: %w", os.ErrNotExist),
	}
	for _, err := range errs {
		t.Run(CISetupErrorKind(err), func(t *testing.T) {
			recorder := new(ciSetupEventRecorder)
			result := CISetupResult{startedAt: time.Now()}

			pushCISetupOutcome(context.Background(), ciSetupEventsConfig(recorder), &result, err)

			require.Len(t, recorder.events, 1)
			assertNoLeak(t, recorder.events[0])
		})
	}
}

// TestPushCISetupOutcomeError pins the outcome event's shape on the error path.
func TestPushCISetupOutcomeError(t *testing.T) {
	recorder := new(ciSetupEventRecorder)
	result := CISetupResult{startedAt: time.Now()}
	err := fmt.Errorf("writing /Users/owen/code/acme/.github/workflows/infracost.yml: %w", os.ErrPermission)

	pushCISetupOutcome(context.Background(), ciSetupEventsConfig(recorder), &result, err)

	require.Len(t, recorder.events, 1)
	outcome, _ := recorder.events[0].field("outcome")
	assert.Equal(t, "error", outcome, "an unset outcome defaults to error")
	kind, ok := recorder.events[0].field("errorKind")
	require.True(t, ok)
	assert.Equal(t, "permission-denied", kind)
	stage, ok := recorder.events[0].field("stage")
	require.True(t, ok)
	assert.Equal(t, ciStagePreflight, stage, "an unset stage defaults to preflight")

	assertNoLeak(t, recorder.events[0])
}

// TestPushCISetupOutcomeStage checks the stage a run reached rides along, so a
// failure names a phase rather than only "error".
func TestPushCISetupOutcomeStage(t *testing.T) {
	recorder := new(ciSetupEventRecorder)
	result := CISetupResult{stage: ciStageWrite, startedAt: time.Now()}

	pushCISetupOutcome(context.Background(), ciSetupEventsConfig(recorder), &result, os.ErrPermission)

	require.Len(t, recorder.events, 1)
	stage, _ := recorder.events[0].field("stage")
	assert.Equal(t, ciStageWrite, stage)
}

// TestSetStage checks a stage change reaches the outcome event, so a failure
// names the phase it happened in.
func TestSetStage(t *testing.T) {
	recorder := new(ciSetupEventRecorder)
	result := CISetupResult{startedAt: time.Now()}

	result.stage = ciStageConfirm
	pushCISetupOutcome(context.Background(), ciSetupEventsConfig(recorder), &result, nil)

	require.Len(t, recorder.events, 1)
	stage, _ := recorder.events[0].field("stage")
	assert.Equal(t, ciStageConfirm, stage)
}
