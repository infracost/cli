package providers

import (
	"context"

	"github.com/hashicorp/go-hclog"
	proto "github.com/infracost/proto/gen/go/infracost/provider"
)

func (c *Config) ListFinopsPolicies(ctx context.Context, fn func(hclog.Level) (proto.ProviderServiceClient, func(), error)) ([]*proto.FinopsPolicy, error) {
	providerClient, stop, err := fn(hclog.DefaultLevel)
	if stop != nil {
		defer stop()
	}
	if err != nil {
		return nil, err
	}
	response, err := providerClient.ListFinopsPolicies(ctx, &proto.ListFinopsPoliciesRequest{})
	if err != nil {
		return nil, err
	}
	return response.GetPolicies(), nil
}
