package providers

import (
	"context"
	"errors"
	"testing"

	"github.com/hashicorp/go-hclog"
	pluginpb "github.com/infracost/proto/gen/go/infracost/plugin"
	proto "github.com/infracost/proto/gen/go/infracost/provider"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

type nilOutputProviderClient struct{}

func (nilOutputProviderClient) Process(context.Context, *pluginpb.ProcessRequest, ...grpc.CallOption) (*pluginpb.ProcessResponse, error) {
	return &pluginpb.ProcessResponse{}, nil
}

func (nilOutputProviderClient) ListFinopsPolicies(context.Context, *pluginpb.ListFinopsPoliciesRequest, ...grpc.CallOption) (*pluginpb.ListFinopsPoliciesResponse, error) {
	return &pluginpb.ListFinopsPoliciesResponse{}, nil
}

type errProviderClient struct{}

func (errProviderClient) Process(context.Context, *pluginpb.ProcessRequest, ...grpc.CallOption) (*pluginpb.ProcessResponse, error) {
	return nil, errors.New("plugin connection failed")
}

func (errProviderClient) ListFinopsPolicies(context.Context, *pluginpb.ListFinopsPoliciesRequest, ...grpc.CallOption) (*pluginpb.ListFinopsPoliciesResponse, error) {
	return &pluginpb.ListFinopsPoliciesResponse{}, nil
}

func TestProcessTreeInputTreatsNilOutputAsEmpty(t *testing.T) {
	cfg := &Config{}
	input := &proto.TreeInput{AbsolutePath: t.TempDir()}
	resources, finops, err := cfg.ProcessTreeInput(context.Background(), proto.Provider_PROVIDER_AWS, input, func(hclog.Level) (pluginpb.ProviderServiceClient, func(), error) {
		return nilOutputProviderClient{}, func() {}, nil
	}, hclog.Off)
	t.Cleanup(cfg.Close)

	require.NoError(t, err)
	require.Empty(t, resources)
	require.Empty(t, finops)
}

func TestProcessTreeInputEvictsCachedClientOnError(t *testing.T) {
	cfg := &Config{}
	t.Cleanup(cfg.Close)

	loads := 0
	stops := 0
	loader := func(hclog.Level) (pluginpb.ProviderServiceClient, func(), error) {
		loads++
		if loads == 1 {
			return errProviderClient{}, func() { stops++ }, nil
		}
		return nilOutputProviderClient{}, func() { stops++ }, nil
	}

	input := &proto.TreeInput{AbsolutePath: t.TempDir()}
	_, _, err := cfg.ProcessTreeInput(context.Background(), proto.Provider_PROVIDER_AWS, input, loader, hclog.Off)
	require.Error(t, err)
	require.Equal(t, 1, stops)

	_, _, err = cfg.ProcessTreeInput(context.Background(), proto.Provider_PROVIDER_AWS, input, loader, hclog.Off)
	require.NoError(t, err)
	require.Equal(t, 2, loads)
}
