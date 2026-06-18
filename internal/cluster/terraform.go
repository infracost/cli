package cluster

import (
	"encoding/json"
	"strings"

	gptree "github.com/infracost/go-proto/pkg/tree"
	"github.com/infracost/go-proto/pkg/tree/aws/eks"
	prototree "github.com/infracost/proto/gen/go/infracost/tree"
)

// FromTerraformTree derives a cluster Config from a parsed Terraform tree (the
// cluster's IaC), reading EKS node groups. Each node group becomes a compute
// pool, including its Kubernetes labels + taints (surfaced by the terraform
// parser in raw attributes) so the provider can match a pod to the pool it
// would actually schedule onto.
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

		// A node group may list several interchangeable instance families
		// (Spot diversification); carry them all so the provider can price the
		// cheapest.
		var instanceTypes []string
		for _, it := range ng.InstanceTypes.Items() {
			if v := it.Value(); v != "" {
				instanceTypes = append(instanceTypes, v)
			}
		}
		if len(instanceTypes) == 0 {
			continue
		}

		if cfg.Region == "" {
			cfg.Region = ng.GetBase().Region
		}

		ras := ng.GetBase().Definition.RawStringAttributes
		cfg.ComputePools = append(cfg.ComputePools, ComputePool{
			Name:          ng.Name.Value(),
			InstanceTypes: instanceTypes,
			NodeCount:     ng.InstanceCount.Value(),
			Spot:          ng.PurchaseOption.Value() == eks.PurchaseOptionSpot,
			Labels:        parseLabels(ras["k8s_labels"]),
			Taints:        parseTaints(ras["k8s_taints"]),
		})
	}

	if len(cfg.ComputePools) == 0 {
		return nil, nil
	}
	return cfg, nil
}

func parseLabels(raw string) map[string]string {
	if raw == "" {
		return nil
	}
	var m map[string]string
	if json.Unmarshal([]byte(raw), &m) != nil {
		return nil
	}
	return m
}

func parseTaints(raw string) []Taint {
	if raw == "" {
		return nil
	}
	var entries []map[string]string
	if json.Unmarshal([]byte(raw), &entries) != nil {
		return nil
	}
	out := make([]Taint, 0, len(entries))
	for _, e := range entries {
		out = append(out, Taint{
			Key:    e["key"],
			Value:  e["value"],
			Effect: normalizeTaintEffect(e["effect"]),
		})
	}
	return out
}

// normalizeTaintEffect maps the Terraform/AWS taint effect spelling
// (NO_SCHEDULE) to the Kubernetes spelling (NoSchedule) the provider matches
// pod tolerations against.
func normalizeTaintEffect(effect string) string {
	switch strings.ToUpper(strings.ReplaceAll(effect, "_", "")) {
	case "NOSCHEDULE":
		return "NoSchedule"
	case "NOEXECUTE":
		return "NoExecute"
	case "PREFERNOSCHEDULE":
		return "PreferNoSchedule"
	default:
		return effect
	}
}
