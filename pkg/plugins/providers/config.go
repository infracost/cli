package providers

import (
	"github.com/hashicorp/go-hclog"
	proto "github.com/infracost/proto/gen/go/infracost/provider"
)

type Config struct {
	AWS    string `env:"INFRACOST_CLI_PROVIDER_PLUGIN_AWS"`
	Google string `env:"INFRACOST_CLI_PROVIDER_PLUGIN_GOOGLE"`
	Azure  string `env:"INFRACOST_CLI_PROVIDER_PLUGIN_AZURERM"`

	AWSVersion    string `env:"INFRACOST_CLI_PROVIDER_PLUGIN_AWS_VERSION"`
	AzureVersion  string `env:"INFRACOST_CLI_PROVIDER_PLUGIN_AZURE_VERSION"`
	GoogleVersion string `env:"INFRACOST_CLI_PROVIDER_PLUGIN_GOOGLE_VERSION"`
}

func (c *Config) LoadAWS(level hclog.Level) (proto.ProviderServiceClient, func(), error) {
	return Connect(c.AWS, level)
}

func (c *Config) LoadGCP(level hclog.Level) (proto.ProviderServiceClient, func(), error) {
	return Connect(c.Google, level)
}

func (c *Config) LoadAzure(level hclog.Level) (proto.ProviderServiceClient, func(), error) {
	return Connect(c.Azure, level)
}
