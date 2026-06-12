package providers

import (
	"context"
	"errors"

	"github.com/hashicorp/go-hclog"
	"github.com/infracost/cli/internal/protocache"
	"github.com/infracost/cli/pkg/logging"
	pluginpb "github.com/infracost/proto/gen/go/infracost/plugin"
	proto "github.com/infracost/proto/gen/go/infracost/provider"
)

func (c *Config) ProcessTreeInput(ctx context.Context, provider proto.Provider, input *proto.TreeInput, fn func(hclog.Level) (pluginpb.ProviderServiceClient, func(), error), level hclog.Level) ([]*proto.Resource, []*proto.FinopsPolicyResult, error) {
	return c.processWithCache(provider, createTreeCacheKey(provider, input, c.providerVersion(provider)), fn, level, func(client pluginpb.ProviderServiceClient) (*proto.Output, error) {
		response, err := client.Process(ctx, &pluginpb.ProcessRequest{Input: input})
		if err != nil {
			return nil, err
		}
		if response == nil || response.Output == nil {
			return &proto.Output{}, nil
		}
		return response.Output, nil
	})
}

func (c *Config) processWithCache(provider proto.Provider, key protocache.Key, fn func(hclog.Level) (pluginpb.ProviderServiceClient, func(), error), level hclog.Level, process func(pluginpb.ProviderServiceClient) (*proto.Output, error)) ([]*proto.Resource, []*proto.FinopsPolicyResult, error) {
	var cache protocache.Cache[*proto.Output]

	if loaded, err := cache.Load(key); err == nil {
		return loaded.Resources, loaded.FinopsResults, nil
	} else if !errors.Is(err, protocache.ErrCacheMiss) {
		logging.Warnf("failed to load provider output from cache: %s", err)
	}

	providerClient, clientID, err := c.providerClient(provider, level, fn)
	if err != nil {
		return nil, nil, err
	}

	output, err := process(providerClient)
	if err != nil {
		c.evictProviderClient(provider, clientID)
		return nil, nil, err
	}
	if output == nil {
		output = &proto.Output{}
	}

	if err := cache.Save(key, output); err != nil {
		logging.Warnf("failed to save provider output: %s", err)
	}

	return output.Resources, output.FinopsResults, nil
}
