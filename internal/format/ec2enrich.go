package format

import (
	"regexp"
	"strings"

	"github.com/infracost/cli/internal/ec2instances"
)

// EC2InstanceTypeDetail provides semantic detail about an EC2 instance type
// so that LLMs can understand the nature of a recommended change without
// guessing based on the instance type string alone.
type EC2InstanceTypeDetail struct {
	Value       string  `json:"value"`
	Family      string  `json:"family"`
	Generation  string  `json:"generation"`
	Size        string  `json:"size"`
	Arch        string  `json:"arch"`
	IsGraviton  bool    `json:"is_graviton"`
	VCPUs       int     `json:"vcpus,omitempty"`
	MemoryGiB   float64 `json:"memory_gib,omitempty"`
	NetworkGbps string  `json:"network_gbps,omitempty"`
}

// AttributeDetail enriches a FinOps issue with structured metadata about the
// current and recommended attribute values.
type AttributeDetail struct {
	From       *EC2InstanceTypeDetail `json:"from,omitempty"`
	To         *EC2InstanceTypeDetail `json:"to,omitempty"`
	ChangeKind string                 `json:"change_kind,omitempty"`
}

// ec2InstanceTypeRe parses an EC2 instance type string into its components.
//
// EC2 instance types follow the format: <family><generation>[variants].<size>
//
//   - family:     one or more lowercase letters identifying the instance family
//                 (e.g. "t" for burstable, "m" for general purpose, "c" for compute)
//   - generation: a digit indicating the hardware generation (e.g. "5", "6", "7")
//   - variants:   optional lowercase letters after the generation digit that indicate
//                 processor or feature variants (e.g. "g" = Graviton/ARM, "i" = Intel,
//                 "a" = AMD, "n" = enhanced networking, "d" = local NVMe storage)
//   - size:       the instance size after the dot (e.g. "nano", "micro", "large",
//                 "xlarge", "2xlarge", "metal")
//
// Examples: t3.micro → (t, 3, micro), m6g.xlarge → (m, 6g, xlarge),
//
//	c7gn.large → (c, 7gn, large)
//
// Capture groups: (1) family, (2) generation+variants, (3) size.
var ec2InstanceTypeRe = regexp.MustCompile(`^([a-z]+)(\d+[a-z]*)\.(\w+)$`)

// descriptionInstanceTypeRe extracts from/to instance types from policy
// issue descriptions that follow the pattern:
//
//	Switch `field` from `<from>` to `<to>` ...
var descriptionInstanceTypeRe = regexp.MustCompile("from `([^`]+)` to `([^`]+)`")

func parseEC2InstanceType(s string) *EC2InstanceTypeDetail {
	m := ec2InstanceTypeRe.FindStringSubmatch(s)
	if m == nil {
		return nil
	}

	family := m[1]
	gen := m[2]
	size := m[3]

	isGraviton := len(gen) > 1 && strings.Contains(gen[1:], "g")
	arch := "x86_64"
	if isGraviton {
		arch = "arm64"
	}

	detail := &EC2InstanceTypeDetail{
		Value:      s,
		Family:     family,
		Generation: gen,
		Size:       size,
		Arch:       arch,
		IsGraviton: isGraviton,
	}

	if specs := ec2instances.Lookup(s); specs != nil {
		detail.VCPUs = specs.VCPUs
		detail.MemoryGiB = specs.MemoryGiB
		detail.NetworkGbps = specs.NetworkGbps
		detail.Arch = specs.Arch
		detail.IsGraviton = specs.Arch == "arm64"
	}

	return detail
}

// enrichFinopsIssues attaches AttributeDetail to each FinOps issue that
// references EC2 instance types, using a static lookup table for hardware
// specs.
func enrichFinopsIssues(issues []FinopsIssueOutput) {
	for i := range issues {
		if !strings.Contains(issues[i].Attribute, "instance_type") {
			continue
		}
		m := descriptionInstanceTypeRe.FindStringSubmatch(issues[i].Description)
		if m == nil {
			continue
		}

		from := parseEC2InstanceType(m[1])
		to := parseEC2InstanceType(m[2])
		if from == nil && to == nil {
			continue
		}

		detail := &AttributeDetail{
			From: from,
			To:   to,
		}

		if from != nil && to != nil {
			detail.ChangeKind = inferChangeKind(from, to)
		}

		issues[i].AttributeDetail = detail
	}
}

func inferChangeKind(from, to *EC2InstanceTypeDetail) string {
	switch {
	case from.Arch != to.Arch:
		return "architecture_migration"
	case from.Family != to.Family:
		return "family_change"
	case from.Generation != to.Generation:
		return "generation_upgrade"
	case from.Size != to.Size:
		return "size_change"
	default:
		return ""
	}
}