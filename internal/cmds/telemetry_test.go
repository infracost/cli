package cmds

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/infracost/cli/internal/api/events/eventstest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPushRunError(t *testing.T) {
	recorder := new(eventstest.Recorder)

	pushRunError(eventsConfig(recorder), "json", time.Now(), errors.New("scan failed"))

	require.Len(t, recorder.Events, 1)
	assert.Equal(t, "infracost-error", recorder.Events[0].Name)
	message, _ := recorder.Events[0].Field("error")
	assert.Equal(t, "scan failed", message)
	outputFormat, _ := recorder.Events[0].Field("outputFormat")
	assert.Equal(t, "json", outputFormat)
	_, sentRunSeconds := recorder.Events[0].Field("runSeconds")
	assert.True(t, sentRunSeconds)
}

func TestPushRunErrorWithoutError(t *testing.T) {
	recorder := new(eventstest.Recorder)

	pushRunError(eventsConfig(recorder), "json", time.Now(), nil)

	assert.Empty(t, recorder.Events)
}

func TestPushLoginOutcomeWithFetchedUser(t *testing.T) {
	recorder := new(eventstest.Recorder)
	outcome := &loginOutcome{orgCount: 2, userFetched: true, createdOrg: true}

	pushLoginOutcome(context.Background(), eventsConfig(recorder), outcome, time.Now(), nil)

	require.Len(t, recorder.Events, 1)
	assert.Equal(t, "infracost-auth-login", recorder.Events[0].Name)
	for key, want := range map[string]interface{}{"outcome": "success", "createdOrg": true, "orgCount": 2.0} {
		got, _ := recorder.Events[0].Field(key)
		assert.Equal(t, want, got, key)
	}
}

func TestPushLoginOutcomeWithoutFetchedUser(t *testing.T) {
	recorder := new(eventstest.Recorder)

	pushLoginOutcome(context.Background(), eventsConfig(recorder), &loginOutcome{}, time.Now(), errors.New("abandoned"))

	require.Len(t, recorder.Events, 1)
	outcome, _ := recorder.Events[0].Field("outcome")
	assert.Equal(t, "error", outcome)
	_, sentOrgCount := recorder.Events[0].Field("orgCount")
	assert.False(t, sentOrgCount, "an unread user must not look like a user in no org")
}
