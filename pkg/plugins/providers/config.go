package providers

import (
	"os"
	"strings"
	"sync"

	"github.com/hashicorp/go-hclog"
	pluginpb "github.com/infracost/proto/gen/go/infracost/plugin"
	proto "github.com/infracost/proto/gen/go/infracost/provider"
)

type cachedProviderClient struct {
	client pluginpb.ProviderServiceClient
	stop   func()
	id     uint64
}

type Config struct {
	AWS    string `env:"INFRACOST_CLI_PROVIDER_PLUGIN_AWS"`
	Google string `env:"INFRACOST_CLI_PROVIDER_PLUGIN_GOOGLE"`
	Azure  string `env:"INFRACOST_CLI_PROVIDER_PLUGIN_AZURERM"`

	AWSVersion    string `env:"INFRACOST_CLI_PROVIDER_PLUGIN_AWS_VERSION"`
	AzureVersion  string `env:"INFRACOST_CLI_PROVIDER_PLUGIN_AZURE_VERSION"`
	GoogleVersion string `env:"INFRACOST_CLI_PROVIDER_PLUGIN_GOOGLE_VERSION"`

	LoadAWS     func(level hclog.Level) (pluginpb.ProviderServiceClient, func(), error)
	LoadGoogle  func(level hclog.Level) (pluginpb.ProviderServiceClient, func(), error)
	LoadAzurerm func(level hclog.Level) (pluginpb.ProviderServiceClient, func(), error)

	mu           sync.Mutex
	clients      map[proto.Provider]cachedProviderClient
	nextClientID uint64
}

func (c *Config) providerClient(provider proto.Provider, level hclog.Level, loader func(hclog.Level) (pluginpb.ProviderServiceClient, func(), error)) (pluginpb.ProviderServiceClient, uint64, error) {
	c.mu.Lock()
	if c.clients != nil {
		if cached, ok := c.clients[provider]; ok {
			c.mu.Unlock()
			return cached.client, cached.id, nil
		}
	}
	c.mu.Unlock()

	client, stop, err := loader(level)
	if err != nil {
		if stop != nil {
			stop()
		}
		return nil, 0, err
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.clients == nil {
		c.clients = make(map[proto.Provider]cachedProviderClient)
	}
	if cached, ok := c.clients[provider]; ok {
		if stop != nil {
			stop()
		}
		return cached.client, cached.id, nil
	}
	c.nextClientID++
	id := c.nextClientID
	c.clients[provider] = cachedProviderClient{client: client, stop: stop, id: id}
	return client, id, nil
}

func (c *Config) evictProviderClient(provider proto.Provider, id uint64) {
	c.mu.Lock()
	cached, ok := c.clients[provider]
	if ok && cached.id == id {
		delete(c.clients, provider)
	} else {
		ok = false
	}
	c.mu.Unlock()

	if ok && cached.stop != nil {
		cached.stop()
	}
}

func (c *Config) providerVersion(provider proto.Provider) string {
	var path, configured string
	switch provider {
	case proto.Provider_PROVIDER_AWS:
		path, configured = c.AWS, c.AWSVersion
	case proto.Provider_PROVIDER_GOOGLE:
		path, configured = c.Google, c.GoogleVersion
	case proto.Provider_PROVIDER_AZURERM:
		path, configured = c.Azure, c.AzureVersion
	}
	if path == "" {
		return configured
	}
	data, err := os.ReadFile(path + ".version") //nolint:gosec // G304: sidecar path is derived from configured plugin path
	if err != nil {
		return configured
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return configured
	}
	return fields[0]
}

func (c *Config) Close() {
	c.mu.Lock()
	clients := c.clients
	c.clients = nil
	c.mu.Unlock()

	for _, cached := range clients {
		if cached.stop != nil {
			cached.stop()
		}
	}
}
