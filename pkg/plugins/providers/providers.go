package providers

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/go-plugin"
	"github.com/infracost/cli/pkg/plugins/consts"
	"github.com/infracost/cli/pkg/plugins/pluginconn"
	"github.com/infracost/cli/pkg/plugins/pluginerr"
	pluginpb "github.com/infracost/proto/gen/go/infracost/plugin"
	"google.golang.org/grpc"
)

const (
	handshakeMagicCookieKey   = "INFRACOST_PLUGIN"
	handshakeMagicCookieValue = "de8c7e96-497c-4168-80c4-fc875c8ce764"
	handshakeProtocolVersion  = 1
	dispenseName              = "plugin"
)

var handshakeConfig = plugin.HandshakeConfig{
	ProtocolVersion:  handshakeProtocolVersion,
	MagicCookieKey:   handshakeMagicCookieKey,
	MagicCookieValue: handshakeMagicCookieValue,
}

var (
	_ plugin.Plugin     = (*provider)(nil)
	_ plugin.GRPCPlugin = (*provider)(nil)
)

func Connect(path string, level hclog.Level) (pluginpb.ProviderServiceClient, func(), error) {
	return ConnectWithOptions(path, pluginconn.ConnectOptions{Level: level})
}

func ConnectWithOptions(path string, opts pluginconn.ConnectOptions) (pluginpb.ProviderServiceClient, func(), error) {
	if path == "" {
		return nil, nil, fmt.Errorf("%w: no plugin path provided (set INFRACOST_CLI_PLUGIN_AUTO_UPDATE=true to download plugins automatically)", pluginerr.ErrPluginNotFound)
	}

	if stat, err := os.Stat(path); err != nil {
		return nil, nil, fmt.Errorf("%w: %s: %v (try setting INFRACOST_CLI_PLUGIN_AUTO_UPDATE=true to re-download)", pluginerr.ErrPluginNotFound, path, err)
	} else if stat.IsDir() {
		return nil, nil, fmt.Errorf("%w: %s is a directory, not a binary (try deleting it and running again)", pluginerr.ErrPluginNotFound, path)
	} else if runtime.GOOS != "windows" && stat.Mode()&0111 == 0 {
		return nil, nil, fmt.Errorf("%w: %s (try: chmod +x %s)", pluginerr.ErrPluginNotExecutable, path, path)
	}

	startTimeout := pluginconn.StartTimeout()
	client := plugin.NewClient(&plugin.ClientConfig{
		HandshakeConfig: handshakeConfig,
		Plugins: map[string]plugin.Plugin{
			dispenseName: new(provider),
		},
		Cmd:              exec.Command(path),
		StartTimeout:     startTimeout,
		AllowedProtocols: []plugin.Protocol{plugin.ProtocolGRPC},
		Logger:           opts.ResolveLogger(),
		GRPCDialOptions: []grpc.DialOption{
			grpc.WithDefaultCallOptions(
				grpc.MaxCallRecvMsgSize(consts.MaxGRPCMessageSize),
				grpc.MaxCallSendMsgSize(consts.MaxGRPCMessageSize),
			),
		},
	})

	rpcClient, err := client.Client()
	if err != nil {
		client.Kill()
		return nil, nil, pluginerr.WindowsHint(pluginerr.ClassifyConnect(err), path, startTimeout)
	}

	raw, err := rpcClient.Dispense(dispenseName)
	if err != nil {
		client.Kill()
		return nil, nil, fmt.Errorf("%w: %v", pluginerr.ErrPluginHandshake, err)
	}

	conn, ok := raw.(*grpc.ClientConn)
	if !ok {
		client.Kill()
		return nil, nil, fmt.Errorf("unexpected dispensed type %T", raw)
	}

	pluginClient := pluginpb.NewPluginServiceClient(conn)
	info, err := pluginClient.GetPluginInfo(context.Background(), &pluginpb.GetPluginInfoRequest{})
	if err != nil {
		client.Kill()
		return nil, nil, fmt.Errorf("failed to get plugin info: %w", err)
	}
	if info == nil || info.GetType() != pluginpb.PluginType_PROVIDER {
		client.Kill()
		return nil, nil, fmt.Errorf("plugin %q is not a provider", path)
	}

	return pluginpb.NewProviderServiceClient(conn), client.Kill, nil
}

type provider struct {
	plugin.NetRPCUnsupportedPlugin
}

func (p *provider) GRPCServer(*plugin.GRPCBroker, *grpc.Server) error {
	return fmt.Errorf("not implemented")
}

func (p *provider) GRPCClient(_ context.Context, _ *plugin.GRPCBroker, conn *grpc.ClientConn) (interface{}, error) {
	return conn, nil
}
