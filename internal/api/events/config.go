package events

import (
	"net/http"
)

type Config struct {
	Endpoint string `env:"INFRACOST_CLI_EVENTS_ENDPOINT" flag:"events-endpoint;hidden" usage:"The endpoint for the Infracost events service" default:"https://pricing.api.infracost.io"`
}

func (c *Config) Client(client *http.Client) *Client {
	// The events client may be used before config defaults are applied (e.g.
	// to report early errors), so ensure the endpoint is always set.
	if c.Endpoint == "" {
		c.Endpoint = "https://pricing.api.infracost.io"
	}
	return &Client{
		client: client,
		config: c,
	}
}
