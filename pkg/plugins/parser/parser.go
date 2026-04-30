package parser

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
	proto "github.com/infracost/proto/gen/go/infracost/parser/api"
	"google.golang.org/grpc"
)

var (
	_ plugin.Plugin     = (*parser)(nil)
	_ plugin.GRPCPlugin = (*parser)(nil)
)

func Connect(path string, level hclog.Level) (proto.ParserServiceClient, func(), error) {
	return ConnectWithOptions(path, pluginconn.ConnectOptions{Level: level})
}

func ConnectWithOptions(path string, opts pluginconn.ConnectOptions) (proto.ParserServiceClient, func(), error) {
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
		HandshakeConfig: plugin.HandshakeConfig{
			ProtocolVersion:  1,
			MagicCookieKey:   "INFRACOST_PARSER_PLUGIN_MAGIC_COOKIE",
			MagicCookieValue: "ac92b06c592f",
		},
		Plugins: map[string]plugin.Plugin{
			"parser": new(parser),
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
		return nil, nil, pluginerr.WindowsHint(pluginerr.ClassifyConnect(err), path, startTimeout)
	}

	raw, err := rpcClient.Dispense("parser")
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %v", pluginerr.ErrPluginHandshake, err)
	}

	return raw.(proto.ParserServiceClient), client.Kill, nil
}

type parser struct {
	plugin.NetRPCUnsupportedPlugin
}

func (p *parser) GRPCServer(*plugin.GRPCBroker, *grpc.Server) error {
	return fmt.Errorf("not implemented")
}

func (p *parser) GRPCClient(_ context.Context, _ *plugin.GRPCBroker, conn *grpc.ClientConn) (interface{}, error) {
	return proto.NewParserServiceClient(conn), nil
}
