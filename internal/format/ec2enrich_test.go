package format

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestParseEC2InstanceType(t *testing.T) {
	tests := []struct {
		input string
		want  *EC2InstanceTypeDetail
	}{
		{"t3.micro", &EC2InstanceTypeDetail{
			Value: "t3.micro", Family: "t", Generation: "3", Size: "micro",
			Arch: "x86_64", IsGraviton: false, VCPUs: 2, MemoryGiB: 1, NetworkGbps: "up to 5",
		}},
		{"t4g.medium", &EC2InstanceTypeDetail{
			Value: "t4g.medium", Family: "t", Generation: "4g", Size: "medium",
			Arch: "arm64", IsGraviton: true, VCPUs: 2, MemoryGiB: 4, NetworkGbps: "up to 5",
		}},
		{"m5.large", &EC2InstanceTypeDetail{
			Value: "m5.large", Family: "m", Generation: "5", Size: "large",
			Arch: "x86_64", IsGraviton: false, VCPUs: 2, MemoryGiB: 8, NetworkGbps: "up to 10",
		}},
		{"m6g.xlarge", &EC2InstanceTypeDetail{
			Value: "m6g.xlarge", Family: "m", Generation: "6g", Size: "xlarge",
			Arch: "arm64", IsGraviton: true, VCPUs: 4, MemoryGiB: 16, NetworkGbps: "up to 10",
		}},
		{"m6i.2xlarge", &EC2InstanceTypeDetail{
			Value: "m6i.2xlarge", Family: "m", Generation: "6i", Size: "2xlarge",
			Arch: "x86_64", IsGraviton: false, VCPUs: 8, MemoryGiB: 32, NetworkGbps: "up to 12.5",
		}},
		{"c7gn.large", &EC2InstanceTypeDetail{
			Value: "c7gn.large", Family: "c", Generation: "7gn", Size: "large",
			Arch: "arm64", IsGraviton: true, VCPUs: 2, MemoryGiB: 4, NetworkGbps: "up to 30",
		}},
		{"r5a.4xlarge", &EC2InstanceTypeDetail{
			Value: "r5a.4xlarge", Family: "r", Generation: "5a", Size: "4xlarge",
			Arch: "x86_64", IsGraviton: false, VCPUs: 16, MemoryGiB: 128, NetworkGbps: "up to 10",
		}},
		// unknown instance type — parsed but no specs
		{"x1.large", &EC2InstanceTypeDetail{
			Value: "x1.large", Family: "x", Generation: "1", Size: "large",
			Arch: "x86_64", IsGraviton: false,
		}},
		{"invalid", nil},
		{"", nil},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := parseEC2InstanceType(tt.input)
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("parseEC2InstanceType(%q) mismatch (-want +got):\n%s", tt.input, diff)
			}
		})
	}
}

func TestEnrichFinopsIssues(t *testing.T) {
	tests := []struct {
		name       string
		issues     []FinopsIssueOutput
		checkIndex int
		want       *AttributeDetail
	}{
		{
			name: "graviton migration",
			issues: []FinopsIssueOutput{{
				Description: "Switch `instance_type` from `t3.micro` to `t4g.micro` (saves ~20%)",
				Attribute:   "instance_type",
			}},
			checkIndex: 0,
			want: &AttributeDetail{
				From:       &EC2InstanceTypeDetail{Value: "t3.micro", Family: "t", Generation: "3", Size: "micro", Arch: "x86_64", VCPUs: 2, MemoryGiB: 1, NetworkGbps: "up to 5"},
				To:         &EC2InstanceTypeDetail{Value: "t4g.micro", Family: "t", Generation: "4g", Size: "micro", Arch: "arm64", IsGraviton: true, VCPUs: 2, MemoryGiB: 1, NetworkGbps: "up to 5"},
				ChangeKind: "architecture_migration",
			},
		},
		{
			name: "generation upgrade same arch",
			issues: []FinopsIssueOutput{{
				Description: "Switch `instance_type` from `m5.large` to `m6i.large`",
				Attribute:   "instance_type",
			}},
			checkIndex: 0,
			want: &AttributeDetail{
				From:       &EC2InstanceTypeDetail{Value: "m5.large", Family: "m", Generation: "5", Size: "large", Arch: "x86_64", VCPUs: 2, MemoryGiB: 8, NetworkGbps: "up to 10"},
				To:         &EC2InstanceTypeDetail{Value: "m6i.large", Family: "m", Generation: "6i", Size: "large", Arch: "x86_64", VCPUs: 2, MemoryGiB: 8, NetworkGbps: "up to 12.5"},
				ChangeKind: "generation_upgrade",
			},
		},
		{
			name: "non instance_type attribute ignored",
			issues: []FinopsIssueOutput{{
				Description: "Switch `volume_type` from `gp2` to `gp3`",
				Attribute:   "volume_type",
			}},
			checkIndex: 0,
			want:       nil,
		},
		{
			name: "no from/to in description",
			issues: []FinopsIssueOutput{{
				Description: "Change instance_type to a preferred machine type.",
				Attribute:   "instance_type",
			}},
			checkIndex: 0,
			want:       nil,
		},
		{
			name: "launch_template instance_type",
			issues: []FinopsIssueOutput{{
				Description: "Switch `instance_type` from `c5.xlarge` to `c6g.xlarge` (saves ~15%)",
				Attribute:   "instance_type",
			}},
			checkIndex: 0,
			want: &AttributeDetail{
				From:       &EC2InstanceTypeDetail{Value: "c5.xlarge", Family: "c", Generation: "5", Size: "xlarge", Arch: "x86_64", VCPUs: 4, MemoryGiB: 8, NetworkGbps: "up to 10"},
				To:         &EC2InstanceTypeDetail{Value: "c6g.xlarge", Family: "c", Generation: "6g", Size: "xlarge", Arch: "arm64", IsGraviton: true, VCPUs: 4, MemoryGiB: 8, NetworkGbps: "up to 10"},
				ChangeKind: "architecture_migration",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			enrichFinopsIssues(tt.issues)
			got := tt.issues[tt.checkIndex].AttributeDetail
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("enrichFinopsIssues() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestInferChangeKind(t *testing.T) {
	tests := []struct {
		name string
		from *EC2InstanceTypeDetail
		to   *EC2InstanceTypeDetail
		want string
	}{
		{
			"arch migration",
			&EC2InstanceTypeDetail{Arch: "x86_64", Family: "t", Generation: "3", Size: "micro"},
			&EC2InstanceTypeDetail{Arch: "arm64", Family: "t", Generation: "4g", Size: "micro"},
			"architecture_migration",
		},
		{
			"family change",
			&EC2InstanceTypeDetail{Arch: "x86_64", Family: "m", Generation: "5", Size: "large"},
			&EC2InstanceTypeDetail{Arch: "x86_64", Family: "c", Generation: "5", Size: "large"},
			"family_change",
		},
		{
			"generation upgrade",
			&EC2InstanceTypeDetail{Arch: "x86_64", Family: "m", Generation: "5", Size: "large"},
			&EC2InstanceTypeDetail{Arch: "x86_64", Family: "m", Generation: "6i", Size: "large"},
			"generation_upgrade",
		},
		{
			"size change",
			&EC2InstanceTypeDetail{Arch: "x86_64", Family: "m", Generation: "5", Size: "large"},
			&EC2InstanceTypeDetail{Arch: "x86_64", Family: "m", Generation: "5", Size: "xlarge"},
			"size_change",
		},
		{
			"identical",
			&EC2InstanceTypeDetail{Arch: "x86_64", Family: "m", Generation: "5", Size: "large"},
			&EC2InstanceTypeDetail{Arch: "x86_64", Family: "m", Generation: "5", Size: "large"},
			"",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := inferChangeKind(tt.from, tt.to)
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}