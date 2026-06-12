package providers

import (
	"context"

	"github.com/hashicorp/go-hclog"
	pluginpb "github.com/infracost/proto/gen/go/infracost/plugin"
	proto "github.com/infracost/proto/gen/go/infracost/provider"
)

func (c *Config) ListFinopsPolicies(ctx context.Context, provider proto.Provider, fn func(hclog.Level) (pluginpb.ProviderServiceClient, func(), error)) ([]*proto.FinopsPolicy, error) {
	providerClient, clientID, err := c.providerClient(provider, hclog.DefaultLevel, fn)
	if err != nil {
		return nil, err
	}
	response, err := providerClient.ListFinopsPolicies(ctx, &pluginpb.ListFinopsPoliciesRequest{})
	if err != nil {
		c.evictProviderClient(provider, clientID)
		return nil, err
	}
	return pluginFinopsPoliciesToProvider(response.GetPolicies()), nil
}

func pluginFinopsPoliciesToProvider(policies []*pluginpb.FinopsPolicy) []*proto.FinopsPolicy {
	out := make([]*proto.FinopsPolicy, 0, len(policies))
	for _, policy := range policies {
		out = append(out, &proto.FinopsPolicy{
			Slug:             policy.GetSlug(),
			Name:             policy.GetName(),
			Group:            policy.GetGroup(),
			Description:      policy.GetDescription(),
			OnlyNewResources: policy.GetOnlyNewResources(),
		})
	}
	return out
}
