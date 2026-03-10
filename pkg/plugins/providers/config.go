package providers

import (
	"github.com/hashicorp/go-hclog"
	"github.com/infracost/cli/internal/config/process"
	proto "github.com/infracost/proto/gen/go/infracost/provider"
)

var (
	_ process.Processor = (*Config)(nil)
)

type Config struct {
	AWS    string `env:"INFRACOST_CLI_PROVIDER_PLUGIN_AWS"`
	Google string `env:"INFRACOST_CLI_PROVIDER_PLUGIN_GOOGLE"`
	Azure  string `env:"INFRACOST_CLI_PROVIDER_PLUGIN_AZURERM"`

	AWSVersion    string `env:"INFRACOST_CLI_PROVIDER_PLUGIN_AWS_VERSION"`
	AzureVersion  string `env:"INFRACOST_CLI_PROVIDER_PLUGIN_AZURE_VERSION"`
	GoogleVersion string `env:"INFRACOST_CLI_PROVIDER_PLUGIN_GOOGLE_VERSION"`

	LoadAWS     func(level hclog.Level) (proto.ProviderServiceClient, func(), error)
	LoadGoogle  func(level hclog.Level) (proto.ProviderServiceClient, func(), error)
	LoadAzurerm func(level hclog.Level) (proto.ProviderServiceClient, func(), error)
}

func (c *Config) Process() {
	c.LoadAWS = func(level hclog.Level) (proto.ProviderServiceClient, func(), error) {
		return Connect(c.AWS, level)
	}
	c.LoadGoogle = func(level hclog.Level) (proto.ProviderServiceClient, func(), error) {
		return Connect(c.Google, level)
	}
	c.LoadAzurerm = func(level hclog.Level) (proto.ProviderServiceClient, func(), error) {
		return Connect(c.Azure, level)
	}
}
