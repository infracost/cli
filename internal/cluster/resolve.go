package cluster

import (
	"context"

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
func ResolveFromDir(ctx context.Context, dir string, parser TerraformParser) (*Config, error) {
	resp, err := parser.Parse(ctx, &pb.ParseRequest{
		Path: dir,
		GenericOptions: &options.GenericOptions{
			RepoDirectory:    dir,
			WorkingDirectory: dir,
		},
	})
	if err != nil {
		return nil, err
	}
	return FromTerraformTree(resp.GetTree())
}
