package plugins

import (
	"context"
	"errors"
	"fmt"
	"io"
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

// pluginCommand builds the exec.Cmd for launching a plugin, propagating the
// CLI's effective log level to the plugin via LOG_LEVEL so plugin logs stream
// back at the same verbosity as the CLI (warn by default, debug under --debug).
func pluginCommand(path string) *exec.Cmd {
	cmd := exec.Command(path)
	cmd.Env = append(os.Environ(), "LOG_LEVEL="+logging.WriteLevel())
	return cmd
}

var handshakeConfig = plugin.HandshakeConfig{
	ProtocolVersion:  handshakeProtocolVersion,
	MagicCookieKey:   handshakeMagicCookieKey,
	MagicCookieValue: handshakeMagicCookieValue,
}

// ManagerOptions configures a Manager.
type ManagerOptions struct {
	// Dir is the directory the manager loads plugins from. If empty, the
	// default plugin cache directory is used.
	Dir string

	// Cache is the directory the manager downloads required plugins into.
	// If empty, defaults to Dir. Ignored when Dir is set to a developer
	// override directory.
	Cache string

	// BaseURL is the root URL where plugin archives are hosted.
	BaseURL string

	// AutoUpdate controls whether required plugins are auto-installed or
	// auto-updated when missing/outdated. When false, only existing flat
	// binaries are used.
	AutoUpdate bool

	// SkipInstall, when true, suppresses the install step entirely. Used
	// when a developer points Dir at a local plugin build directory.
	SkipInstall bool
}

// Manager owns the lifecycle of every plugin used by the CLI: installing
// required plugins, discovering whatever is in the plugin directory,
// connecting to each, and tearing them all down on Close.
type Manager struct {
	opts ManagerOptions

	installMu   sync.Mutex
	installOnce sync.Once
	installErr  error

	loadMu          sync.Mutex
	loadOnce        sync.Once
	parserPlugins   []*ParserPlugin
	providerPlugins []*ProviderPlugin
	loadErr         error

	useMu sync.RWMutex

	// queryInfo reads an installed plugin's reported version. Defaults to
	// queryPluginInfo; tests stub this to avoid spawning real binaries.
	queryInfo func(context.Context, string) (*pb.GetPluginInfoResponse, error)
}

// NewManager creates a new manager.
func NewManager(opts ManagerOptions) *Manager {
	if opts.Dir == "" {
		opts.Dir = defaultPluginCachePath()
	}
	if opts.Cache == "" {
		opts.Cache = opts.Dir
	}
	return &Manager{opts: opts, queryInfo: queryPluginInfo}
}

// Dir returns the directory the manager loads plugins from.
func (m *Manager) Dir() string { return m.opts.Dir }

// EnsureInstalled downloads and installs any required plugins that aren't
// already present in the cache directory. It is a no-op when SkipInstall is
// set.
func (m *Manager) EnsureInstalled() error {
	m.installMu.Lock()
	defer m.installMu.Unlock()

	m.installOnce.Do(func() {
		m.installErr = m.ensureInstalled()
	})
	return m.installErr
}

func (m *Manager) ensureInstalled() error {
	if m.opts.SkipInstall {
		return nil
	}
	for _, required := range requiredPlugins {
		if _, err := m.Install(required.Name, requiredPluginVersion(required.Key)); err != nil {
			return err
		}
	}
	return nil
}

// LoadParserPlugins returns every parser plugin discovered in the plugin
// directory. The first call connects to every plugin; subsequent calls
// return the cached result.
func (m *Manager) LoadParserPlugins(ctx context.Context) ([]*ParserPlugin, error) {
	if err := m.loadAll(ctx); err != nil {
		return nil, err
	}
	return m.parserPlugins, nil
}

// LoadProviderPlugins returns every provider plugin discovered in the plugin
// directory.
func (m *Manager) LoadProviderPlugins(ctx context.Context) ([]*ProviderPlugin, error) {
	if err := m.loadAll(ctx); err != nil {
		return nil, err
	}
	return m.providerPlugins, nil
}

// LoadParserPluginForProject returns the parser plugin that handles the given
// project type. Match priority: plugin name first, then the plugin's reported
// ConfigFileProjectType.
func (m *Manager) LoadParserPluginForProject(ctx context.Context, projectTypeOrPluginName string) (*ParserPlugin, error) {
	plugins, err := m.LoadParserPlugins(ctx)
	if err != nil {
		return nil, err
	}

	for _, p := range plugins {
		if p.Info.GetName() == projectTypeOrPluginName || p.ParserConfig.GetConfigFileProjectType() == projectTypeOrPluginName {
			return p, nil
		}
	}

	return nil, fmt.Errorf("no plugin found for project type or plugin name: %q", projectTypeOrPluginName)
}

func (m *Manager) loadAll(ctx context.Context) error {
	m.loadMu.Lock()
	defer m.loadMu.Unlock()

	m.loadOnce.Do(func() {
		m.parserPlugins, m.providerPlugins, m.loadErr = m.discoverAndConnect(ctx)
	})
	return m.loadErr
}

func (m *Manager) discoverAndConnect(ctx context.Context) ([]*ParserPlugin, []*ProviderPlugin, error) {
	entries, err := os.ReadDir(m.opts.Dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil, nil
		}
		return nil, nil, err
	}

	var parsers []*ParserPlugin
	var providers []*ProviderPlugin
	seenPaths := make(map[string]string)
	for _, entry := range entries {
		if entry.IsDir() || isPluginSidecar(entry.Name()) {
			continue
		}
		path := filepath.Join(m.opts.Dir, entry.Name())
		parser, provider, err := m.connect(ctx, path)
		if err != nil {
			logging.Debugf("skipping plugin candidate %s: %v", path, err)
			continue
		}

		var name string
		switch {
		case parser != nil:
			name = parser.Info.GetName()
		case provider != nil:
			name = provider.Info.GetName()
		}

		if existing, ok := seenPaths[name]; ok {
			if parser != nil {
				parser.client.Kill()
			}
			if provider != nil {
				provider.client.Kill()
			}
			for _, p := range parsers {
				p.client.Kill()
			}
			for _, p := range providers {
				p.client.Kill()
			}
			return nil, nil, fmt.Errorf("found two plugins reporting the same name %q:\n  %s\n  %s\ndelete one of these files and try again", name, existing, path)
		}
		seenPaths[name] = path

		if parser != nil {
			parsers = append(parsers, parser)
		}
		if provider != nil {
			providers = append(providers, provider)
		}
	}

	return parsers, providers, nil
}

// queryPluginInfo spawns the plugin binary at path, calls GetPluginInfo, and
// returns the response. The subprocess is terminated before this function
// returns. Used by Install to read a plugin's reported version without
// holding a long-lived connection.
func queryPluginInfo(ctx context.Context, path string) (*pb.GetPluginInfoResponse, error) {
	stat, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if stat.IsDir() {
		return nil, fmt.Errorf("%s is a directory", path)
	}
	if runtime.GOOS != "windows" && stat.Mode()&0o111 == 0 {
		return nil, fmt.Errorf("%w: %s", pluginerr.ErrPluginNotExecutable, path)
	}

	startTimeout := pluginconn.StartTimeout()
	client := plugin.NewClient(&plugin.ClientConfig{
		HandshakeConfig: handshakeConfig,
		Plugins: map[string]plugin.Plugin{
			dispenseName: grpcPlugin{},
		},
		Cmd:              pluginCommand(path),
		StartTimeout:     startTimeout,
		AllowedProtocols: []plugin.Protocol{plugin.ProtocolGRPC},
		Logger:           logging.PluginHCLogger(),
		// go-plugin re-emits parsed plugin stderr through Logger; discard the
		// raw copy so lines aren't also printed unformatted.
		SyncStderr: io.Discard,
		GRPCDialOptions: []grpc.DialOption{
			grpc.WithDefaultCallOptions(
				grpc.MaxCallRecvMsgSize(consts.MaxGRPCMessageSize),
				grpc.MaxCallSendMsgSize(consts.MaxGRPCMessageSize),
			),
		},
	})
	defer client.Kill()

	rpcClient, err := client.Client()
	if err != nil {
		return nil, pluginerr.WindowsHint(pluginerr.ClassifyConnect(err), path, startTimeout)
	}
	raw, err := rpcClient.Dispense(dispenseName)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", pluginerr.ErrPluginHandshake, err)
	}
	conn, ok := raw.(*grpc.ClientConn)
	if !ok {
		return nil, fmt.Errorf("unexpected dispensed type %T", raw)
	}

	info, err := pb.NewPluginServiceClient(conn).GetPluginInfo(ctx, &pb.GetPluginInfoRequest{})
	if err != nil {
		return nil, fmt.Errorf("failed to get plugin info: %w", err)
	}
	if info == nil {
		return nil, fmt.Errorf("plugin %q returned no info", path)
	}
	return info, nil
}

// connect starts the plugin subprocess at path, reads GetPluginInfo, and
// returns either a parser or provider client depending on the reported type.
func (m *Manager) connect(ctx context.Context, path string) (*ParserPlugin, *ProviderPlugin, error) {
	stat, err := os.Stat(path)
	if err != nil {
		return nil, nil, err
	}
	if stat.IsDir() {
		return nil, nil, fmt.Errorf("%s is a directory", path)
	}
	if runtime.GOOS != "windows" && stat.Mode()&0o111 == 0 {
		return nil, nil, fmt.Errorf("%w: %s (try: chmod +x %s)", pluginerr.ErrPluginNotExecutable, path, path)
	}

	startTimeout := pluginconn.StartTimeout()
	client := plugin.NewClient(&plugin.ClientConfig{
		HandshakeConfig: handshakeConfig,
		Plugins: map[string]plugin.Plugin{
			dispenseName: grpcPlugin{},
		},
		Cmd:              pluginCommand(path),
		StartTimeout:     startTimeout,
		AllowedProtocols: []plugin.Protocol{plugin.ProtocolGRPC},
		Logger:           logging.PluginHCLogger(),
		// go-plugin re-emits parsed plugin stderr through Logger; discard the
		// raw copy so lines aren't also printed unformatted.
		SyncStderr: io.Discard,
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

	pluginClient := pb.NewPluginServiceClient(conn)
	info, err := pluginClient.GetPluginInfo(ctx, &pb.GetPluginInfoRequest{})
	if err != nil {
		client.Kill()
		return nil, nil, fmt.Errorf("failed to get plugin info: %w", err)
	}
	if info == nil {
		client.Kill()
		return nil, nil, fmt.Errorf("plugin %q returned no info", path)
	}

	switch info.GetType() {
	case pb.PluginType_PARSER:
		parserClient := pb.NewParserServiceClient(conn)
		parserConfig, err := parserClient.GetParserConfig(ctx, &pb.GetParserConfigRequest{})
		if err != nil {
			client.Kill()
			return nil, nil, fmt.Errorf("failed to get parser config: %w", err)
		}
		return &ParserPlugin{
			ParserServiceClient: lockedParserServiceClient{client: parserClient, mu: &m.useMu},
			Info:                info,
			ParserConfig:        parserConfig,
			Path:                path,
			client:              client,
		}, nil, nil

	case pb.PluginType_PROVIDER:
		providerClient := pb.NewProviderServiceClient(conn)
		return nil, &ProviderPlugin{
			ProviderServiceClient: lockedProviderServiceClient{client: providerClient, mu: &m.useMu},
			Info:                  info,
			Path:                  path,
			client:                client,
		}, nil

	default:
		client.Kill()
		return nil, nil, fmt.Errorf("plugin %q reports unknown type %v", path, info.GetType())
	}
}

// Close kills every plugin subprocess this manager has connected to.
func (m *Manager) Close() {
	m.loadMu.Lock()
	defer m.loadMu.Unlock()

	m.useMu.Lock()
	defer m.useMu.Unlock()

	for _, p := range m.parserPlugins {
		if p.client != nil {
			p.client.Kill()
		}
	}
	for _, p := range m.providerPlugins {
		if p.client != nil {
			p.client.Kill()
		}
	}
	m.parserPlugins = nil
	m.providerPlugins = nil
	m.loadErr = nil
	m.loadOnce = sync.Once{}
}

func isPluginSidecar(name string) bool {
	return strings.HasSuffix(name, ".sha256") || strings.HasSuffix(name, ".version")
}

// ParserPlugin is a connected parser plugin subprocess.
type ParserPlugin struct {
	pb.ParserServiceClient
	Info         *pb.GetPluginInfoResponse
	ParserConfig *pb.GetParserConfigResponse
	// Path is the on-disk location of the plugin binary, or empty when
	// the plugin was constructed in tests without a real binary.
	Path   string
	client *plugin.Client
}

// ProviderPlugin is a connected provider plugin subprocess.
type ProviderPlugin struct {
	pb.ProviderServiceClient
	Info *pb.GetPluginInfoResponse
	// Path is the on-disk location of the plugin binary, or empty when
	// the plugin was constructed in tests without a real binary.
	Path   string
	client *plugin.Client
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

type lockedProviderServiceClient struct {
	client pb.ProviderServiceClient
	mu     *sync.RWMutex
}

func (c lockedProviderServiceClient) Process(ctx context.Context, in *pb.ProcessRequest, opts ...grpc.CallOption) (*pb.ProcessResponse, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.client.Process(ctx, in, opts...)
}

func (c lockedProviderServiceClient) ListFinopsPolicies(ctx context.Context, in *pb.ListFinopsPoliciesRequest, opts ...grpc.CallOption) (*pb.ListFinopsPoliciesResponse, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.client.ListFinopsPolicies(ctx, in, opts...)
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
