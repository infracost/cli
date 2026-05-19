package providers

import (
	"context"
	"errors"

	"github.com/hashicorp/go-hclog"
	"github.com/infracost/cli/internal/protocache"
	"github.com/infracost/cli/pkg/logging"
	proto "github.com/infracost/proto/gen/go/infracost/provider"
)

func (c *Config) ProcessInput(ctx context.Context, provider proto.Provider, input *proto.Input, fn func(hclog.Level) (proto.ProviderServiceClient, func(), error), level hclog.Level) ([]*proto.Resource, []*proto.FinopsPolicyResult, error) {

	var cache protocache.Cache[*proto.Output]

	// Bind the cache key to the provider plugin binary's mtime as a
	// stand-in for a real plugin version (mirrors the same pattern in
	// pkg/plugins/parser). Without this, rebuilding a provider plugin
	// during local iteration silently returns the prior cached output.
	key := createCacheKey(provider, input, c.providerCacheVersion(provider))
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
