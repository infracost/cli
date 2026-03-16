package parser

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/go-plugin"
	"github.com/infracost/cli/pkg/plugins/consts"
	proto "github.com/infracost/proto/gen/go/infracost/parser/api"
	"google.golang.org/grpc"
)

var (
	_ plugin.Plugin     = (*parser)(nil)
	_ plugin.GRPCPlugin = (*parser)(nil)
)

func Connect(path string, level hclog.Level) (proto.ParserServiceClient, func(), error) {
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

	raw, err := rpcClient.Dispense("parser")
	if err != nil {
		return nil, nil, err
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
