package events

import (
	"net/http"
)

var (
	_ Config
)

type Config struct {
	Endpoint string `env:"INFRACOST_CLI_EVENTS_ENDPOINT" flag:"events-endpoint;hidden" usage:"The endpoint for the Infracost events service" default:"https://pricing.api.infracost.io"`

	Client func(httpClient *http.Client) Client
}

func (c *Config) Process() {
	c.Client = func(httpClient *http.Client) Client {
		// The events client may be used before config defaults are applied (e.g.
		// to report early errors), so ensure the endpoint is always set.
		if c.Endpoint == "" {
			c.Endpoint = "https://pricing.api.infracost.io"
		}
		return &client{
			client: httpClient,
			config: c,
		}
	}
}
