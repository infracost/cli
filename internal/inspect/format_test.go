package inspect

import (
	"testing"
)

func TestTruncateCell(t *testing.T) {
	tests := []struct {
		name          string
		cell          string
		width         int
		truncateRight bool
		want          string
	}{
		{
			name:  "short cell fits as-is",
			cell:  "data",
			width: 10,
			want:  "data",
		},
		{
			name:  "middle truncation preserves both ends",
			cell:  "aws_appautoscaling_target.dynamodb_read",
			width: 20,
			want:  "aws_appaut…modb_read",
		},
		{
			// At width 20 the suffix-distinguishing chars survive, so two
			// otherwise-identical resource names stay visually distinct.
			name:  "middle truncation distinguishes similar resource names",
			cell:  "aws_appautoscaling_target.dynamodb_write",
			width: 20,
			want:  "aws_appaut…odb_write",
		},
		{
			name:          "right truncation drops suffix",
			cell:          "Consider using GP3 volumes for better price-performance",
			width:         20,
			truncateRight: true,
			want:          "Consider using GP3 …",
		},
		{
			name:  "width 1 returns just ellipsis",
			cell:  "anything",
			width: 1,
			want:  "…",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := truncateCell(tc.cell, tc.width, tc.truncateRight)
			if got != tc.want {
				t.Errorf("truncateCell(%q, %d, %v):\n  got:  %q\n  want: %q", tc.cell, tc.width, tc.truncateRight, got, tc.want)
			}
		})
	}
}
