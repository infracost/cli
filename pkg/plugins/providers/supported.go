package providers

import (
	"context"

	"github.com/hashicorp/go-hclog"
	parserapi "github.com/infracost/proto/gen/go/infracost/parser/api"
	proto "github.com/infracost/proto/gen/go/infracost/provider"
)

// ListSupportedResources asks a provider plugin which resource types
// it can produce cost components for. The returned response carries
// separate sets for Terraform / CloudFormation / ARM; callers pick
// whichever IaC type matches the project they're about to parse and
// pass it into the parser's InitializeRequest.
//
// All three provider plugins (AWS / Azurerm / Google) return
// identical content — the underlying tree-walk lookups don't scope
// by the TargetProvider build flag. Semantically the Azurerm plugin
// is the natural one to ask for ARM, AWS for CFN, any of the three
// for Terraform; technically any choice works.
func (c *Config) ListSupportedResources(ctx context.Context, fn func(hclog.Level) (proto.ProviderServiceClient, func(), error)) (*proto.ListSupportedResourcesResponse, error) {
	providerClient, stop, err := fn(hclog.DefaultLevel)
	if stop != nil {
		defer stop()
	}
	if err != nil {
		return nil, err
	}
	return providerClient.ListSupportedResources(ctx, &proto.ListSupportedResourcesRequest{})
}

// ARMSupportedResources is a convenience that returns just the ARM
// slice from the broader response, since the only current caller
// (parser InitializeRequest plumbing for ARM scans) doesn't need
// the Terraform or CloudFormation slices.
func (c *Config) ARMSupportedResources(ctx context.Context, fn func(hclog.Level) (proto.ProviderServiceClient, func(), error)) (*parserapi.SupportedResources, error) {
	resp, err := c.ListSupportedResources(ctx, fn)
	if err != nil {
		return nil, err
	}
	return resp.GetArm(), nil
}
