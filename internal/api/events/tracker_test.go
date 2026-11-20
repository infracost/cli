package events

import (
	"context"
	"testing"

	analytics "github.com/infracost/analytics-go"
	"github.com/infracost/cli/internal/api/events/eventstest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTrackerFlattensPropertiesIntoSortedPairs(t *testing.T) {
	recorder := new(eventstest.Recorder)
	commandPath := "infracost auth login"

	err := analytics.TrackInfracostCommand(context.Background(), NewTracker(recorder), analytics.InfracostCommandProperties{
		Command:         "login",
		CommandPath:     &commandPath,
		Success:         false,
		DurationSeconds: 1.5,
	})

	require.NoError(t, err)
	require.Len(t, recorder.Events, 1)
	assert.Equal(t, "infracost-command", recorder.Events[0].Name)
	assert.Equal(t, []interface{}{
		"command", "login",
		"commandPath", "infracost auth login",
		"durationSeconds", 1.5,
		"success", false,
	}, recorder.Events[0].Extra)
}

func TestTrackerSendsEnumsAsStrings(t *testing.T) {
	recorder := new(eventstest.Recorder)
	kind := analytics.InfracostCommandErrorKind_Network

	err := analytics.TrackInfracostCommand(context.Background(), NewTracker(recorder), analytics.InfracostCommandProperties{
		Command:   "setup",
		ErrorKind: &kind,
	})

	require.NoError(t, err)
	require.Len(t, recorder.Events, 1)
	assert.Contains(t, recorder.Events[0].Extra, "network")
}

func TestTrackerDropsUnmarshalableProperties(t *testing.T) {
	recorder := new(eventstest.Recorder)

	err := NewTracker(recorder).Track(context.Background(), analytics.EventInfracostError, map[string]any{"error": func() {}})

	require.NoError(t, err)
	assert.Empty(t, recorder.Events)
}
