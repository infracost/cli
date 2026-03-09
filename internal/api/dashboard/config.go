package dashboard

import (
	"net/http"

	"github.com/infracost/cli/pkg/environment"
)

var (
	defaultValues = map[environment.Environment]map[string]string{
		environment.Production: {
			"endpoint": "https://dashboard.api.infracost.io",
		},
		environment.Development: {
			"endpoint": "https://dashboard.api.dev.infracost.io",
		},
		environment.Local: {
			"endpoint": "http://localhost:5000",
		},
	}
)

type Config struct {
	Endpoint string `env:"INFRACOST_CLI_DASHBOARD_ENDPOINT" flag:"dashboard-endpoint;hidden" usage:"The endpoint for the Infracost dashboard"`
}

func (c *Config) Client(client *http.Client) *Client {
	return &Client{
		client: client,
		config: c,
	}
}

func (c *Config) ApplyDefaults(env environment.Environment) {
	if c.Endpoint == "" {
		c.Endpoint = defaultValues[env]["endpoint"]
	}
}
