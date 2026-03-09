package providers

import (
	"context"
	"errors"

	"github.com/hashicorp/go-hclog"
	"github.com/infracost/cli/internal/logging"
	"github.com/infracost/cli/internal/protocache"
	proto "github.com/infracost/proto/gen/go/infracost/provider"
)

func (c *Config) Process(ctx context.Context, provider proto.Provider, input *proto.Input, fn func(hclog.Level) (proto.ProviderServiceClient, func(), error), level hclog.Level) ([]*proto.Resource, []*proto.FinopsPolicyResult, error) {

	var cache protocache.Cache[*proto.Output]

	// TODO: we probably want to include the provider plugin version in the cache key, but we need to decide how to get that - we could add a new method to the plugin interface that returns the version
	providerVersion := ""
	key := createCacheKey(provider, input, providerVersion)
	if loaded, err := cache.Load(key); err == nil {
		return loaded.Resources, loaded.FinopsResults, nil
	} else if !errors.Is(err, protocache.ErrCacheMiss) {
		logging.Warnf("failed to load provider output from cache: %s", err)
	}

	providerClient, stop, err := fn(level)
	if stop != nil {
		defer stop()
	}
	if err != nil {
		return nil, nil, err
	}
	response, err := providerClient.Process(ctx, &proto.ProcessRequest{
		Input: input,
	})
	if err != nil {
		return nil, nil, err
	}

	if err := cache.Save(key, response.Output); err != nil {
		logging.Warnf("failed to save provider output to cache: %s", err)
	}

	return response.Output.Resources, response.Output.FinopsResults, nil

}
