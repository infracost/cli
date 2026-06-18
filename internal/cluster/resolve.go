package cluster

import (
	"context"
	"os"
	"path/filepath"

	"github.com/infracost/proto/gen/go/infracost/parser/options"
	pb "github.com/infracost/proto/gen/go/infracost/plugin"
	"google.golang.org/grpc"
)

// TerraformParser is the subset of a parser plugin the resolver needs.
// *plugins.ParserPlugin satisfies it (it embeds the ParserServiceClient).
type TerraformParser interface {
	Parse(ctx context.Context, in *pb.ParseRequest, opts ...grpc.CallOption) (*pb.ParseResponse, error)
}

// ResolveFromDir parses the cluster IaC at dir with the given Terraform parser
// plugin and derives the cluster topology. Returns (nil, nil) when dir has no
// recognizable cluster.
//
// The repo root is set to dir's enclosing git repository (not dir itself) so
// that local modules referenced as siblings — e.g. a root at infra/prod that
// uses ../modules/base — are loaded; the parser rejects modules outside the
// repo root.
func ResolveFromDir(ctx context.Context, dir string, parser TerraformParser) (*Config, error) {
	resp, err := parser.Parse(ctx, &pb.ParseRequest{
		Path: dir,
		GenericOptions: &options.GenericOptions{
			RepoDirectory:    repoRoot(dir),
			WorkingDirectory: dir,
		},
	})
	if err != nil {
		return nil, err
	}
	return FromTerraformTree(resp.GetTree())
}

// repoRoot walks up from dir to the nearest ancestor containing a .git entry,
// returning that directory. Falls back to dir when no repository is found.
func repoRoot(dir string) string {
	d := dir
	for {
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			return d
		}
		parent := filepath.Dir(d)
		if parent == d {
			return dir
		}
		d = parent
	}
}
