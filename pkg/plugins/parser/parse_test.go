package parser

import (
	"context"
	"testing"

	"github.com/hashicorp/go-hclog"
	parsermocks "github.com/infracost/cli/pkg/plugins/parser/mocks"
	repoconfig "github.com/infracost/config"
	"github.com/infracost/proto/gen/go/infracost/parser/api"
	"github.com/infracost/proto/gen/go/infracost/parser/options"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

// runParse calls parseWithoutCache against a mocked client and returns
// the captured *api.ParseRequest. parseWithoutCache bypasses the disk
// cache that Parse layers on top, keeping the test hermetic across
// machines.
func runParse(t *testing.T, project *repoconfig.Project, path string) *api.ParseRequest {
	t.Helper()

	client := parsermocks.NewMockParserServiceClient(t)
	client.EXPECT().Initialize(mock.Anything, mock.Anything).Return(&api.InitializeResponse{}, nil)

	var captured *api.ParseRequest
	client.EXPECT().Parse(mock.Anything, mock.Anything).
		Run(func(_ context.Context, in *api.ParseRequest, _ ...grpc.CallOption) {
			captured = in
		}).
		Return(&api.ParseResponse{}, nil)

	cfg := &Config{
		Load: func(hclog.Level) (api.ParserServiceClient, func(), error) {
			return client, func() {}, nil
		},
	}

	_, err := cfg.parseWithoutCache(
		context.Background(),
		path,
		&repoconfig.Config{},
		project,
		hclog.Off,
		&options.GenericOptions{},
	)
	require.NoError(t, err)
	require.NotNil(t, captured, "expected Parse() to have been invoked")
	return captured
}

func TestParseWithoutCache_RoutesByProjectType(t *testing.T) {
	t.Run("terraform type routes to terraform target", func(t *testing.T) {
		req := runParse(t,
			&repoconfig.Project{Type: repoconfig.ProjectTypeTerraform, Path: "."},
			t.TempDir(),
		)
		_, ok := req.Target.Value.(*api.ParseRequestTarget_Terraform)
		require.True(t, ok, "expected Terraform target, got %T", req.Target.Value)
	})

	t.Run("terragrunt type routes to terraform target", func(t *testing.T) {
		// Terragrunt projects go through the same terraform parser
		// entrypoint; the parser itself decides what to do.
		req := runParse(t,
			&repoconfig.Project{Type: repoconfig.ProjectTypeTerragrunt, Path: "."},
			t.TempDir(),
		)
		_, ok := req.Target.Value.(*api.ParseRequestTarget_Terraform)
		require.True(t, ok, "expected Terraform target, got %T", req.Target.Value)
	})

	t.Run("cloudformation type routes to cloudformation target", func(t *testing.T) {
		req := runParse(t,
			&repoconfig.Project{Type: repoconfig.ProjectTypeCloudFormation, Path: "template.json"},
			"template.json",
		)
		got, ok := req.Target.Value.(*api.ParseRequestTarget_Cloudformation)
		require.True(t, ok, "expected CloudFormation target, got %T", req.Target.Value)
		require.Equal(t, "template.json", got.Cloudformation.TemplatePath)
	})

	t.Run("arm type routes to arm target", func(t *testing.T) {
		req := runParse(t,
			&repoconfig.Project{Type: repoconfig.ProjectTypeARM, Path: "template.json"},
			"template.json",
		)
		got, ok := req.Target.Value.(*api.ParseRequestTarget_Arm)
		require.True(t, ok, "expected ARM target, got %T", req.Target.Value)
		require.Equal(t, "template.json", got.Arm.TemplatePath)
	})
}

func TestParseARM_AzureContextPopulation(t *testing.T) {
	t.Run("azure context flows through when fields are set", func(t *testing.T) {
		req := runParse(t,
			&repoconfig.Project{
				Type: repoconfig.ProjectTypeARM,
				Path: "template.json",
				Azure: repoconfig.ProjectAzureConfig{
					SubscriptionID:    "sub-123",
					TenantID:          "tenant-456",
					ResourceGroupName: "rg-test",
					Location:          "eastus",
					ManagementGroupID: "mg-789",
				},
			},
			"template.json",
		)
		got, ok := req.Target.Value.(*api.ParseRequestTarget_Arm)
		require.True(t, ok)
		require.NotNil(t, got.Arm.Options.AzureContext)
		require.Equal(t, "sub-123", got.Arm.Options.AzureContext.SubscriptionId)
		require.Equal(t, "tenant-456", got.Arm.Options.AzureContext.TenantId)
		require.Equal(t, "rg-test", got.Arm.Options.AzureContext.ResourceGroupName)
		require.Equal(t, "eastus", got.Arm.Options.AzureContext.Location)
		require.Equal(t, "mg-789", got.Arm.Options.AzureContext.ManagementGroupId)
	})

	t.Run("azure context is nil when no fields are set", func(t *testing.T) {
		// Mirrors how parseCloudFormation omits AwsContext when empty —
		// avoids handing the parser an all-zero context that it would
		// have to treat differently from "unset".
		req := runParse(t,
			&repoconfig.Project{Type: repoconfig.ProjectTypeARM, Path: "template.json"},
			"template.json",
		)
		got, ok := req.Target.Value.(*api.ParseRequestTarget_Arm)
		require.True(t, ok)
		require.Nil(t, got.Arm.Options.AzureContext)
	})
}

func TestParseWithoutCache_ExtensionFallback(t *testing.T) {
	// When project.Type is unset (legacy configs, single-file LSP
	// invocations) the parser falls back to extension-based dispatch.
	// Pin that behaviour so the autodetect-based path staying
	// load-bearing doesn't accidentally remove this safety net.

	t.Run("untyped .tf file routes to terraform", func(t *testing.T) {
		req := runParse(t,
			&repoconfig.Project{Path: "main.tf"},
			"main.tf", // non-existent path -> Stat fails -> extension switch fires
		)
		_, ok := req.Target.Value.(*api.ParseRequestTarget_Terraform)
		require.True(t, ok, "expected Terraform target, got %T", req.Target.Value)
	})

	t.Run("untyped .json file routes to cloudformation", func(t *testing.T) {
		// Without a project type or content sniff, the legacy default
		// is CloudFormation — ARM relies on autodetect or explicit
		// project.Type to be picked correctly.
		req := runParse(t,
			&repoconfig.Project{Path: "template.json"},
			"template.json",
		)
		_, ok := req.Target.Value.(*api.ParseRequestTarget_Cloudformation)
		require.True(t, ok, "expected CloudFormation target, got %T", req.Target.Value)
	})

	t.Run("untyped directory routes to terraform", func(t *testing.T) {
		req := runParse(t,
			&repoconfig.Project{Path: "."},
			t.TempDir(),
		)
		_, ok := req.Target.Value.(*api.ParseRequestTarget_Terraform)
		require.True(t, ok, "expected Terraform target, got %T", req.Target.Value)
	})
}
