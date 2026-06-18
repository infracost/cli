// Package cluster resolves the underlying-cluster topology that Kubernetes
// workloads deploy onto, so the kubernetes provider plugin can price them.
//
// Kubernetes manifests carry no cluster info; the cluster's node pools live in
// separate infrastructure-as-code (e.g. an EKS module in another repo). This
// package derives a cluster Config from that IaC and hands it to the kubernetes
// provider via the INFRACOST_KUBERNETES_CLUSTER environment variable (which the
// plugin subprocess inherits). The same resolver is intended for reuse by
// Infracost Cloud, which has access to every repo and can join app repos to
// their cluster repos.
package cluster

import "encoding/json"

// EnvVar is the environment variable the kubernetes provider plugin reads its
// cluster topology from. Must match the provider's contract.
const EnvVar = "INFRACOST_KUBERNETES_CLUSTER"

// Config is the cluster topology. Its JSON shape is the contract with the
// kubernetes provider plugin (mirrors providers/pkg/kubernetes.ClusterConfig);
// keep the two in sync, or promote to a shared schema module.
type Config struct {
	CloudProvider        string                `json:"cloud_provider"`
	Region               string                `json:"region"`
	ComputePools         []ComputePool         `json:"compute_pools"`
	StorageBackends      []StorageBackend      `json:"storage_backends,omitempty"`
	LoadBalancerDefaults *LoadBalancerDefaults `json:"load_balancer_defaults,omitempty"`
}

type ComputePool struct {
	Name string `json:"name"`
	// InstanceTypes are the interchangeable instance families a (usually Spot)
	// node group may launch. The provider prices all of them and uses the
	// cheapest, since a capacity/price-optimized autoscaler rarely lands on the
	// first/newest one.
	InstanceTypes []string          `json:"instance_types"`
	Spot          bool              `json:"spot"`
	NodeCount     int64             `json:"node_count,omitempty"`
	Labels        map[string]string `json:"labels,omitempty"`
}

type StorageBackend struct {
	Name        string `json:"name"`
	StorageType string `json:"storage_type"`
}

type LoadBalancerDefaults struct {
	Type string `json:"type"`
}

// JSON renders the config as the JSON the provider expects.
func (c *Config) JSON() (string, error) {
	b, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
