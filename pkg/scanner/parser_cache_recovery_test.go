package scanner

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/infracost/cli/internal/cache"
	"github.com/infracost/cli/pkg/plugins"
	repoconfig "github.com/infracost/config"
	"github.com/infracost/go-proto/pkg/diagnostic"
	"github.com/infracost/go-proto/pkg/rat"
	"github.com/infracost/proto/gen/go/infracost/parser"
	pluginpb "github.com/infracost/proto/gen/go/infracost/plugin"
	"github.com/infracost/proto/gen/go/infracost/provider"
	treepb "github.com/infracost/proto/gen/go/infracost/tree"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
)

func TestScanProjectRetriesTransientParserDiagnostics(t *testing.T) {
	for _, typ := range []parser.DiagnosticType{
		parser.DiagnosticType_DIAGNOSTIC_TYPE_MODULE_FETCH_ERROR,
		parser.DiagnosticType_DIAGNOSTIC_TYPE_INVALID_SOURCE_MAP,
		parser.DiagnosticType_DIAGNOSTIC_TYPE_REMOTE_VARIABLE_LOAD_ERROR,
	} {
		t.Run(typ.String(), func(t *testing.T) {
			d := diagnostic.New(typ, "temporary upstream failure").ToProto()
			failed := &pluginpb.ParseResponse{Diagnostics: []*parser.Diagnostic{d}}
			if !d.Critical {
				failed.Tree = &treepb.Tree{}
			}
			parserClient := &cacheRecoveryParser{responses: []*pluginpb.ParseResponse{failed, {Tree: &treepb.Tree{}}}}
			opts := cacheRecoveryOptions(t, parserClient)

			first, err := ScanProject(context.Background(), opts)
			require.NoError(t, err)
			require.Len(t, first.Diagnostics, 1, "the current run must still report its diagnostic")
			require.Equal(t, d.Error, first.Diagnostics[0].Error)
			if d.Critical {
				require.Empty(t, first.Resources, "critical failures must still stop before pricing")
			}

			second, err := ScanProject(context.Background(), opts)
			require.NoError(t, err)
			require.Equal(t, 2, parserClient.calls, "unchanged local source must not pin a transient failure")
			require.Empty(t, second.Diagnostics)
			require.Equal(t, "10", second.TotalMonthlyCost.StringFixed(0))

			third, err := ScanProject(context.Background(), opts)
			require.NoError(t, err)
			require.Empty(t, third.Diagnostics)
			require.Equal(t, "10", third.TotalMonthlyCost.StringFixed(0))
			require.Equal(t, 2, parserClient.calls, "the recovered parse must be cached")
		})
	}
}

func TestParserCacheRejectsExistingTransientResponses(t *testing.T) {
	for _, lateWarning := range []bool{false, true} {
		name := "critical module fetch error"
		if lateWarning {
			name = "transient warning after diagnostic display limit"
		}
		t.Run(name, func(t *testing.T) {
			parserClient := &cacheRecoveryParser{responses: []*pluginpb.ParseResponse{{Tree: &treepb.Tree{}}}}
			opts := cacheRecoveryOptions(t, parserClient)
			failed := &pluginpb.ParseResponse{}
			if lateWarning {
				for range 12 {
					failed.Diagnostics = append(failed.Diagnostics, diagnostic.New(parser.DiagnosticType_DIAGNOSTIC_TYPE_MISSING_INPUT_VARIABLE, "missing optional variable").ToProto())
				}
				failed.Diagnostics = append(failed.Diagnostics, diagnostic.New(parser.DiagnosticType_DIAGNOSTIC_TYPE_REMOTE_VARIABLE_LOAD_ERROR, "temporary upstream failure").ToProto())
				failed.Tree = &treepb.Tree{}
			} else {
				failed.Diagnostics = []*parser.Diagnostic{diagnostic.New(parser.DiagnosticType_DIAGNOSTIC_TYPE_MODULE_FETCH_ERROR, "temporary upstream failure").ToProto()}
			}
			generic, err := genericOptionsFingerprint(buildGenericOptions(opts))
			require.NoError(t, err)
			fp, err := fingerprintProject(opts.RootDir, opts.RootDir, nil, append(generic, 0))
			require.NoError(t, err)
			// Seed bytes directly to represent an entry written by an older CLI.
			data, err := proto.Marshal(failed)
			require.NoError(t, err)
			path := filepath.Join(parserCacheDir(opts.Project.Name, "1.0.0"), projectCacheFilename(opts.RootDir))
			require.NoError(t, os.WriteFile(path, append([]byte(fp), data...), 0o600))

			result, err := ScanProject(context.Background(), opts)
			require.NoError(t, err)
			require.Equal(t, 1, parserClient.calls, "an existing transient entry must not suppress retry")
			require.Empty(t, result.Diagnostics)
			require.Equal(t, "10", result.TotalMonthlyCost.StringFixed(0))
		})
	}
}

func TestParserCachePreservesStableDiagnostics(t *testing.T) {
	for _, typ := range []parser.DiagnosticType{
		parser.DiagnosticType_DIAGNOSTIC_TYPE_HCL_PARSE_ERROR,
		parser.DiagnosticType_DIAGNOSTIC_TYPE_MISSING_INPUT_VARIABLE,
	} {
		t.Run(typ.String(), func(t *testing.T) {
			response := &pluginpb.ParseResponse{
				Diagnostics: []*parser.Diagnostic{diagnostic.New(typ, "source-dependent diagnostic").ToProto()},
				Tree:        &treepb.Tree{},
			}
			parserClient := &cacheRecoveryParser{responses: []*pluginpb.ParseResponse{response}}
			opts := cacheRecoveryOptions(t, parserClient)
			for range 2 {
				result, err := ScanProject(context.Background(), opts)
				require.NoError(t, err)
				require.Len(t, result.Diagnostics, 1)
				require.Equal(t, "source-dependent diagnostic", result.Diagnostics[0].Error)
			}
			require.Equal(t, 1, parserClient.calls, "stable diagnostics retain cache hits")
		})
	}
}

// --- Test helpers: in-process plugin clients and isolated cache fixtures. ---

type cacheRecoveryParser struct {
	pluginpb.ParserServiceClient
	responses []*pluginpb.ParseResponse
	calls     int
}

func (p *cacheRecoveryParser) Parse(context.Context, *pluginpb.ParseRequest, ...grpc.CallOption) (*pluginpb.ParseResponse, error) {
	i := min(p.calls, len(p.responses)-1)
	p.calls++
	return p.responses[i], nil
}

type cacheRecoveryProvider struct{ pluginpb.ProviderServiceClient }

func (cacheRecoveryProvider) Process(context.Context, *pluginpb.ProcessRequest, ...grpc.CallOption) (*pluginpb.ProcessResponse, error) {
	return &pluginpb.ProcessResponse{Output: &provider.Output{Resources: []*provider.Resource{{
		Name: "aws_instance.web",
		Costs: &provider.ResourceCosts{Components: []*provider.CostComponent{{
			PeriodPrice: &provider.PeriodPrice{Price: rat.New(10).Proto(), Period: provider.Period_MONTH},
			Quantity:    rat.New(1).Proto(),
		}}},
	}}}}, nil
}

// cacheRecoveryOptions keeps tests independent of installed plugins and other cached projects.
func cacheRecoveryOptions(t *testing.T, parserClient *cacheRecoveryParser) *ScanProjectOptions {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "main.tf"), []byte(`module "web" { source = "example/web/aws" }`), 0o600))
	name := "cache-recovery-" + cache.Key(root)
	dir := parserCacheDir(name, "1.0.0")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(filepath.Dir(dir))) })
	return &ScanProjectOptions{
		RootDir:    root,
		Project:    &repoconfig.Project{Name: name, Path: ".", Type: "terraform"},
		RepoConfig: &repoconfig.Config{},
		Plugins: &plugins.Config{
			LoadParserPluginForProject: func(context.Context, string) (*plugins.ParserPlugin, error) {
				return &plugins.ParserPlugin{ParserServiceClient: parserClient, Info: &pluginpb.GetPluginInfoResponse{Name: name, Version: "1.0.0"}}, nil
			},
			LoadProviderPlugins: func(context.Context) ([]*plugins.ProviderPlugin, error) {
				return []*plugins.ProviderPlugin{{ProviderServiceClient: cacheRecoveryProvider{}, Info: &pluginpb.GetPluginInfoResponse{Name: "test-provider"}}}, nil
			},
		},
	}
}

// --- End test helpers. ---
