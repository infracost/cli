package events

import (
	"context"
	"encoding/json"
	"maps"
	"slices"

	analytics "github.com/infracost/analytics-go"

	"github.com/infracost/cli/pkg/logging"
)

type tracker struct {
	client Client
}

func NewTracker(client Client) analytics.AnalyticsClient {
	return tracker{client: client}
}

func (t tracker) Track(ctx context.Context, event analytics.EventName, properties any) error {
	fields, err := flatten(properties)
	if err != nil {
		logging.WithError(err).Msgf("events: failed to marshal %s properties", event)
		return nil
	}

	extra := make([]interface{}, 0, len(fields)*2)
	for _, key := range slices.Sorted(maps.Keys(fields)) {
		extra = append(extra, key, fields[key])
	}
	t.client.Push(ctx, string(event), extra...)
	return nil
}

func flatten(properties any) (map[string]interface{}, error) {
	buf, err := json.Marshal(properties)
	if err != nil {
		return nil, err
	}
	var fields map[string]interface{}
	if err := json.Unmarshal(buf, &fields); err != nil {
		return nil, err
	}
	return fields, nil
}
