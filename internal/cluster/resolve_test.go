package cluster

import (
	"context"
	"testing"

	gptree "github.com/infracost/go-proto/pkg/tree"
	"github.com/infracost/go-proto/pkg/tree/aws/eks"
	pb "github.com/infracost/proto/gen/go/infracost/plugin"
	"google.golang.org/grpc"
)

type fakeParser struct {
	resp *pb.ParseResponse
	err  error
}

func (f fakeParser) Parse(_ context.Context, _ *pb.ParseRequest, _ ...grpc.CallOption) (*pb.ParseResponse, error) {
	return f.resp, f.err
}

func TestResolveFromDir(t *testing.T) {
	var w gptree.Tree
	w.AWS.EKS.NodeGroups = []eks.NodeGroup{
		nodeGroup("default_workers", "us-east-2", 3, true, "r7i.large"),
	}
	pt, err := w.ToProto()
	if err != nil {
		t.Fatalf("ToProto: %v", err)
	}

	cfg, err := ResolveFromDir(context.Background(), "./infra", fakeParser{resp: &pb.ParseResponse{Tree: pt}})
	if err != nil {
		t.Fatalf("ResolveFromDir: %v", err)
	}
	if cfg == nil || len(cfg.ComputePools) != 1 || len(cfg.ComputePools[0].InstanceTypes) != 1 || cfg.ComputePools[0].InstanceTypes[0] != "r7i.large" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}
