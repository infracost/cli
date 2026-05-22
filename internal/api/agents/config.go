package agents

import (
	"net/http"

	"github.com/infracost/cli/pkg/config/process"
	"github.com/infracost/cli/pkg/environment"
)

var (
	_ process.Processor = (*Config)(nil)

	defaultValues = map[string]map[string]string{
		environment.Production: {
			"endpoint": "https://app.coast.infracost.io/agents",
		},
		environment.Development: {
			"endpoint": "https://app.coast.dev.infracost.io/agents",
		},
		environment.Local: {
			"endpoint": "http://localhost:8787/agents",
		},
	}
)

type Config struct {
	Environment string `flagvalue:"environment"`
	Endpoint    string `env:"INFRACOST_CLI_AGENTS_ENDPOINT" flag:"agents-endpoint;hidden" usage:"The endpoint for the Infracost Agents API"`

	// Can override this in tests.
	Client func(httpClient *http.Client) Client
}

func (c *Config) Process() {
	if c.Endpoint == "" {
		c.Endpoint = defaultValues[c.Environment]["endpoint"]
	}

	c.Client = func(httpClient *http.Client) Client {
		return &client{
			client: httpClient,
			config: c,
		}
	}
}
