package plugins

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/hashicorp/go-plugin"
	"github.com/infracost/cli/pkg/logging"
	"github.com/infracost/cli/pkg/plugins/consts"
	"github.com/infracost/cli/pkg/plugins/pluginconn"
	"github.com/infracost/cli/pkg/plugins/pluginerr"
	pb "github.com/infracost/proto/gen/go/infracost/plugin"
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

type Manager struct {
	dir           string
	stateMu       sync.Mutex
	useMu         sync.RWMutex
	loadOnce      sync.Once
	parserPlugins []*ParserPlugin
	loadErr       error
}

// NewManager creates a new manager using the flat plugin directory at dir. If
// dir is empty, the manager defaults to the CLI plugin cache directory.
func NewManager(dir string) (*Manager, error) {
	if dir == "" {
		dir = defaultPluginCachePath()
	}
	return &Manager{dir: dir}, nil
}

func (m *Manager) LoadParserPluginForProject(ctx context.Context, projectTypeOrPluginName string) (*ParserPlugin, error) {
	plugins, err := m.LoadParserPlugins(ctx)
	if err != nil {
		return nil, err
	}

	for _, p := range plugins {
		if p.Info.Name == projectTypeOrPluginName || p.ParserConfig.GetConfigFileProjectType() == projectTypeOrPluginName {
			return p, nil
		}
	}

	return nil, fmt.Errorf("no plugin found for project type or plugin name: %q", projectTypeOrPluginName)
}

// LoadParserPlugins discovers and connects to parser plugins in the manager's
// flat plugin directory. The result is cached on first call; subsequent calls
// return the same plugins and error without re-scanning the directory.
func (m *Manager) LoadParserPlugins(ctx context.Context) ([]*ParserPlugin, error) {
	m.stateMu.Lock()
	defer m.stateMu.Unlock()

	m.loadOnce.Do(func() {
		m.parserPlugins, m.loadErr = m.loadParserPlugins(ctx)
	})
	return m.parserPlugins, m.loadErr
}

func (m *Manager) loadParserPlugins(ctx context.Context) ([]*ParserPlugin, error) {
	entries, err := os.ReadDir(m.dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}

	var plugins []*ParserPlugin
	for _, entry := range entries {
		if entry.IsDir() || isPluginSidecar(entry.Name()) || isKnownProviderPlugin(entry.Name()) {
			continue
		}

		p, err := connectParserPlugin(ctx, filepath.Join(m.dir, entry.Name()), pluginconn.ConnectOptions{})
		if err != nil {
			logging.Debugf("skipping plugin candidate %s: %v", filepath.Join(m.dir, entry.Name()), err)
			continue
		}

		p.ParserServiceClient = lockedParserServiceClient{client: p.ParserServiceClient, mu: &m.useMu}
		plugins = append(plugins, p)
	}

	return plugins, nil
}

func isPluginSidecar(name string) bool {
	return strings.HasSuffix(name, ".sha256") || strings.HasSuffix(name, ".version")
}

func isKnownProviderPlugin(name string) bool {
	for _, spec := range pluginSpecs {
		if spec.Type == pluginTypeProvider && name == pluginBinaryName(spec.Name) {
			return true
		}
	}
	return false
}

// Close terminates every parser plugin subprocess this manager has connected to.
func (m *Manager) Close() {
	m.stateMu.Lock()
	defer m.stateMu.Unlock()

	m.useMu.Lock()
	defer m.useMu.Unlock()

	for _, p := range m.parserPlugins {
		if p.client != nil {
			p.client.Kill()
		}
	}
	m.parserPlugins = nil
	m.loadErr = nil
	m.loadOnce = sync.Once{}
}

type ParserPlugin struct {
	pb.ParserServiceClient
	Info         *pb.GetPluginInfoResponse
	ParserConfig *pb.GetParserConfigResponse
	client       *plugin.Client
}

type lockedParserServiceClient struct {
	client pb.ParserServiceClient
	mu     *sync.RWMutex
}

func (c lockedParserServiceClient) GetParserConfig(ctx context.Context, in *pb.GetParserConfigRequest, opts ...grpc.CallOption) (*pb.GetParserConfigResponse, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.client.GetParserConfig(ctx, in, opts...)
}

func (c lockedParserServiceClient) IdentifyProjects(ctx context.Context, in *pb.IdentifyProjectsRequest, opts ...grpc.CallOption) (*pb.IdentifyProjectsResponse, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.client.IdentifyProjects(ctx, in, opts...)
}

func (c lockedParserServiceClient) Parse(ctx context.Context, in *pb.ParseRequest, opts ...grpc.CallOption) (*pb.ParseResponse, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.client.Parse(ctx, in, opts...)
}

type grpcPlugin struct {
	plugin.NetRPCUnsupportedPlugin
}

func (grpcPlugin) GRPCServer(*plugin.GRPCBroker, *grpc.Server) error {
	return errors.New("not implemented")
}

func (grpcPlugin) GRPCClient(_ context.Context, _ *plugin.GRPCBroker, conn *grpc.ClientConn) (any, error) {
	return conn, nil
}

func connectParserPlugin(ctx context.Context, path string, opts pluginconn.ConnectOptions) (*ParserPlugin, error) {
	stat, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if stat.IsDir() {
		return nil, fmt.Errorf("%s is a directory", path)
	}
	if runtime.GOOS != "windows" && stat.Mode()&0o111 == 0 {
		return nil, fmt.Errorf("%w: %s (try: chmod +x %s)", pluginerr.ErrPluginNotExecutable, path, path)
	}

	startTimeout := pluginconn.StartTimeout()
	client := plugin.NewClient(&plugin.ClientConfig{
		HandshakeConfig: handshakeConfig,
		Plugins: map[string]plugin.Plugin{
			dispenseName: grpcPlugin{},
		},
		Cmd:              exec.Command(path),
		StartTimeout:     startTimeout,
		AllowedProtocols: []plugin.Protocol{plugin.ProtocolGRPC},
		Logger:           opts.ResolveLogger(),
		SyncStderr:       logging.Output(),
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
		return nil, pluginerr.WindowsHint(pluginerr.ClassifyConnect(err), path, startTimeout)
	}

	raw, err := rpcClient.Dispense(dispenseName)
	if err != nil {
		client.Kill()
		return nil, fmt.Errorf("%w: %v", pluginerr.ErrPluginHandshake, err)
	}

	conn, ok := raw.(*grpc.ClientConn)
	if !ok {
		client.Kill()
		return nil, fmt.Errorf("unexpected dispensed type %T", raw)
	}

	pluginClient := pb.NewPluginServiceClient(conn)
	info, err := pluginClient.GetPluginInfo(ctx, &pb.GetPluginInfoRequest{})
	if err != nil {
		client.Kill()
		return nil, fmt.Errorf("failed to get plugin info: %w", err)
	}
	if info == nil || info.GetType() != pb.PluginType_PARSER {
		client.Kill()
		return nil, fmt.Errorf("plugin %q is not a parser", path)
	}

	parserClient := pb.NewParserServiceClient(conn)
	parserConfig, err := parserClient.GetParserConfig(ctx, &pb.GetParserConfigRequest{})
	if err != nil {
		client.Kill()
		return nil, fmt.Errorf("failed to get parser config: %w", err)
	}

	return &ParserPlugin{
		ParserServiceClient: parserClient,
		Info:                info,
		ParserConfig:        parserConfig,
		client:              client,
	}, nil
}
