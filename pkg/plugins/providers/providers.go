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
	proto "github.com/infracost/proto/gen/go/infracost/provider"
	"google.golang.org/grpc"
)

var (
	_ plugin.Plugin     = (*provider)(nil)
	_ plugin.GRPCPlugin = (*provider)(nil)
)

func Connect(path string, level hclog.Level) (proto.ProviderServiceClient, func(), error) {

	if path == "" {
		return nil, nil, fmt.Errorf("no plugin path provided (set INFRACOST_CLI_PLUGIN_AUTO_UPDATE=true to download plugins automatically)")
	}

	if stat, err := os.Stat(path); err != nil {
		return nil, nil, fmt.Errorf("error accessing plugin at %s: %w", path, err)
	} else if stat.IsDir() {
		return nil, nil, fmt.Errorf("plugin path %s is a directory, not a binary", path)
	} else if runtime.GOOS != "windows" && stat.Mode()&0111 == 0 {
		return nil, nil, fmt.Errorf("plugin at %s is not executable (try: chmod +x %s)", path, path)
	}

	client := plugin.NewClient(&plugin.ClientConfig{
		HandshakeConfig: plugin.HandshakeConfig{
			ProtocolVersion:  1,
			MagicCookieKey:   "INFRACOST_PROVIDER_PLUGIN_MAGIC_COOKIE",
			MagicCookieValue: "04d179d767fc",
		},
		Plugins: map[string]plugin.Plugin{
			"provider": new(provider),
		},
		Cmd:              exec.Command(path),
		AllowedProtocols: []plugin.Protocol{plugin.ProtocolGRPC},
		Logger: hclog.New(&hclog.LoggerOptions{
			Level:  level,
			Output: os.Stderr,
		}),
		GRPCDialOptions: []grpc.DialOption{
			grpc.WithDefaultCallOptions(
				grpc.MaxCallRecvMsgSize(consts.MaxGRPCMessageSize),
				grpc.MaxCallSendMsgSize(consts.MaxGRPCMessageSize),
			),
		},
	})

	rpcClient, err := client.Client()
	if err != nil {
		return nil, nil, err
	}

	raw, err := rpcClient.Dispense("provider")
	if err != nil {
		return nil, nil, err
	}

	return raw.(proto.ProviderServiceClient), client.Kill, nil
}

type provider struct {
	plugin.NetRPCUnsupportedPlugin
}

func (p *provider) GRPCServer(*plugin.GRPCBroker, *grpc.Server) error {
	return fmt.Errorf("not implemented")
}

func (p *provider) GRPCClient(_ context.Context, _ *plugin.GRPCBroker, conn *grpc.ClientConn) (interface{}, error) {
	return proto.NewProviderServiceClient(conn), nil
}
