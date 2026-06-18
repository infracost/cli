package cluster

import (
	"testing"

	gptree "github.com/infracost/go-proto/pkg/tree"
	"github.com/infracost/go-proto/pkg/tree/aws/eks"
	"github.com/infracost/go-proto/pkg/tree/resource"
	"github.com/infracost/go-proto/pkg/tree/value"
)

// nodeGroup builds an eks.NodeGroup mirroring the shape produced by parsing a
// terraform-aws-modules/eks managed node group.
func nodeGroup(name, region string, count int64, spot bool, instanceTypes ...string) eks.NodeGroup {
	items := make([]value.Value[string], 0, len(instanceTypes))
	for _, it := range instanceTypes {
		items = append(items, value.New(it, 0, "", nil))
	}
	purchase := eks.PurchaseOptionOnDemand
	if spot {
		purchase = eks.PurchaseOptionSpot
	}
	return eks.NodeGroup{
		Resource:       resource.Resource{Region: region},
		Name:           value.New(name, 0, "", nil),
		ClusterName:    value.New("prod", 0, "", nil),
		InstanceCount:  value.New(count, 0, "", nil),
		InstanceTypes:  *value.NewList(items, 0, "", nil),
		PurchaseOption: value.New(purchase, 0, "", nil),
	}
}

func TestFromTerraformTree_EKSNodeGroups(t *testing.T) {
	// Mirrors coast's prod cluster in ./infra: SPOT worker pools on r7i.large,
	// a t3.large data-export pool, region us-east-2.
	var w gptree.Tree
	w.AWS.EKS.NodeGroups = []eks.NodeGroup{
		nodeGroup("default_workers", "us-east-2", 3, true, "r7i.large", "r7a.large", "r6i.large"),
		nodeGroup("data_export", "us-east-2", 1, true, "t3.large", "t3a.large"),
	}

	pt, err := w.ToProto()
	if err != nil {
		t.Fatalf("ToProto: %v", err)
	}

	cfg, err := FromTerraformTree(pt)
	if err != nil {
		t.Fatalf("FromTerraformTree: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected a cluster config, got nil")
	}
	if cfg.CloudProvider != "aws" || cfg.Region != "us-east-2" {
		t.Fatalf("unexpected cloud/region: %s/%s", cfg.CloudProvider, cfg.Region)
	}
	if len(cfg.ComputePools) != 2 {
		t.Fatalf("expected 2 pools, got %d", len(cfg.ComputePools))
	}

	p0 := cfg.ComputePools[0]
	if p0.Name != "default_workers" || !p0.Spot || p0.NodeCount != 3 {
		t.Fatalf("unexpected first pool: %+v", p0)
	}
	if len(p0.InstanceTypes) != 3 || p0.InstanceTypes[0] != "r7i.large" || p0.InstanceTypes[2] != "r6i.large" {
		t.Fatalf("expected all 3 instance families carried, got %v", p0.InstanceTypes)
	}

	// The derived config must serialize to the JSON the provider consumes.
	js, err := cfg.JSON()
	if err != nil || js == "" {
		t.Fatalf("JSON(): %q err=%v", js, err)
	}
}

func TestFromTerraformTree_NoCluster(t *testing.T) {
	var w gptree.Tree
	pt, err := w.ToProto()
	if err != nil {
		t.Fatalf("ToProto: %v", err)
	}
	cfg, err := FromTerraformTree(pt)
	if err != nil {
		t.Fatalf("FromTerraformTree: %v", err)
	}
	if cfg != nil {
		t.Fatalf("expected nil config for a tree with no node groups, got %+v", cfg)
	}
}
