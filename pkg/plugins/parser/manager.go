package parser

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/hashicorp/go-hclog"
	"github.com/infracost/cli/pkg/logging"
	proto "github.com/infracost/proto/gen/go/infracost/parser/api"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// PerIaCPlugin represents a discovered per-IaC parser plugin.
type PerIaCPlugin struct {
	Name     string
	Path     string
	Metadata *proto.DescribeResponse
	client   proto.ParserServiceClient
	stop     func()
}

// PluginManager discovers per-IaC parser plugins and provides dual-mode
// routing: per-IaC plugins first, then fallback to the mono-parser.
type PluginManager struct {
	perIaCPlugins []*PerIaCPlugin
	level         hclog.Level
}

// NewPluginManager creates a manager and discovers per-IaC plugins
// in the given directory. Only binaries matching infracost-parser-plugin-*
// are loaded. Plugins that fail to load or don't support Describe are skipped.
func NewPluginManager(pluginDir string, level hclog.Level) *PluginManager {
	m := &PluginManager{level: level}
	m.discoverPlugins(pluginDir)
	return m
}

func (m *PluginManager) discoverPlugins(pluginDir string) {
	if pluginDir == "" {
		return
	}

	entries, err := os.ReadDir(pluginDir)
	if err != nil {
		logging.Debugf("plugin manager: cannot read plugin directory %s: %v", pluginDir, err)
		return
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !isPerIaCPluginBinary(name) {
			continue
		}

		pluginPath := filepath.Join(pluginDir, name)
		p, err := m.loadPerIaCPlugin(pluginPath)
		if err != nil {
			logging.Debugf("plugin manager: skipping %s: %v", name, err)
			continue
		}
		m.perIaCPlugins = append(m.perIaCPlugins, p)
	}

	sort.Slice(m.perIaCPlugins, func(i, j int) bool {
		return m.perIaCPlugins[i].Metadata.Priority < m.perIaCPlugins[j].Metadata.Priority
	})

	if len(m.perIaCPlugins) > 0 {
		names := make([]string, len(m.perIaCPlugins))
		for i, p := range m.perIaCPlugins {
			names[i] = p.Name
		}
		logging.Debugf("plugin manager: discovered %d per-IaC plugins: %s", len(m.perIaCPlugins), strings.Join(names, ", "))
	}
}

func isPerIaCPluginBinary(name string) bool {
	base := strings.TrimSuffix(name, ".exe")
	return strings.HasPrefix(base, "infracost-parser-plugin-") &&
		!strings.HasSuffix(base, "-debug")
}

func (m *PluginManager) loadPerIaCPlugin(path string) (*PerIaCPlugin, error) {
	client, stop, err := Connect(path, m.level)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	desc, err := client.Describe(ctx, &proto.DescribeRequest{})
	if err != nil {
		stop()
		if st, ok := status.FromError(err); ok && st.Code() == codes.Unimplemented {
			return nil, fmt.Errorf("plugin does not implement Describe RPC")
		}
		return nil, fmt.Errorf("describe: %w", err)
	}

	return &PerIaCPlugin{
		Name:     desc.Name,
		Path:     path,
		Metadata: desc,
		client:   client,
		stop:     stop,
	}, nil
}

// Detect asks all per-IaC plugins whether they can handle the given path,
// in priority order. Returns the claiming plugin and project type, or nil
// if no per-IaC plugin claims the path.
func (m *PluginManager) Detect(ctx context.Context, path string) (*PerIaCPlugin, string, error) {
	for _, p := range m.perIaCPlugins {
		resp, err := p.client.Detect(ctx, &proto.DetectRequest{Path: path})
		if err != nil {
			if st, ok := status.FromError(err); ok && st.Code() == codes.Unimplemented {
				continue
			}
			logging.Debugf("plugin manager: detect error from %s: %v", p.Name, err)
			continue
		}
		if resp.Detected {
			return p, resp.ProjectType, nil
		}
	}
	return nil, "", nil
}

// Client returns the gRPC client for a per-IaC plugin.
func (p *PerIaCPlugin) Client() proto.ParserServiceClient {
	return p.client
}

// HasPlugins returns true if any per-IaC plugins were discovered.
func (m *PluginManager) HasPlugins() bool {
	return len(m.perIaCPlugins) > 0
}

// Plugins returns the list of discovered per-IaC plugins.
func (m *PluginManager) Plugins() []*PerIaCPlugin {
	return m.perIaCPlugins
}

// Close shuts down all per-IaC plugin connections.
func (m *PluginManager) Close() {
	for _, p := range m.perIaCPlugins {
		if p.stop != nil {
			p.stop()
		}
	}
	m.perIaCPlugins = nil
}
