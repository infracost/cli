package cluster

import (
	gptree "github.com/infracost/go-proto/pkg/tree"
	"github.com/infracost/go-proto/pkg/tree/aws/eks"
	prototree "github.com/infracost/proto/gen/go/infracost/tree"
)

// FromTerraformTree derives a cluster Config from a parsed Terraform tree (the
// cluster's IaC), reading EKS node groups. Each node group becomes a compute
// pool; the workload->pool match later uses node-group labels (not yet exposed
// by the tree) or falls back to the first pool.
//
// Returns (nil, nil) when the tree defines no recognizable cluster, so callers
// can fall back to an explicitly-provided cluster spec.
func FromTerraformTree(protoTree *prototree.Tree) (*Config, error) {
	if protoTree == nil {
		return nil, nil
	}
	w, err := gptree.FromProto(protoTree)
	if err != nil {
		return nil, err
	}

	cfg := &Config{CloudProvider: "aws"}

	for i := range w.AWS.EKS.NodeGroups {
		ng := &w.AWS.EKS.NodeGroups[i]

		// A node group may list several instance families (Spot diversification);
		// price against the first as a representative.
		instanceType := ""
		if items := ng.InstanceTypes.Items(); len(items) > 0 {
			instanceType = items[0].Value()
		}
		if instanceType == "" {
			continue
		}

		if cfg.Region == "" {
			cfg.Region = ng.GetBase().Region
		}

		cfg.ComputePools = append(cfg.ComputePools, ComputePool{
			Name:         ng.Name.Value(),
			InstanceType: instanceType,
			NodeCount:    ng.InstanceCount.Value(),
			Spot:         ng.PurchaseOption.Value() == eks.PurchaseOptionSpot,
		})
	}

	if len(cfg.ComputePools) == 0 {
		return nil, nil
	}
	return cfg, nil
}
