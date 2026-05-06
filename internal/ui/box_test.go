package ui

import "testing"

func TestPrintableWidth(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want int
	}{
		{"plain ascii", "Claude Code", 11},
		{"with CSI styling", "\x1b[1m\x1b[31mClaude Code\x1b[0m", 11},
		{
			// VS-16 (U+FE0F) bumps an ambiguous-width base to 2 cells.
			name: "emoji presentation",
			in:   "⚠️ heads up",
			want: 11,
		},
		{
			// Kitty APC declaring c=2: should count as 2 visible cells
			// regardless of the base64 payload length.
			name: "kitty APC contributes c=N cells",
			in:   "before \x1b_Gf=100,c=2,r=1,a=T;AAAABBBBCCCC\x1b\\ after",
			want: 6 + 1 + 2 + 1 + 5, // "before" + " " + icon + " " + "after"
		},
		{
			// Two icons and styled text in one line.
			name: "multiple APCs plus CSI",
			in:   "\x1b_Gc=2;abcd\x1b\\\x1b[1mhi\x1b[0m\x1b_Gc=3;efgh\x1b\\",
			want: 2 + 2 + 3,
		},
		{
			// OSC hyperlink: opening + closing OSCs strip out, label stays.
			name: "OSC hyperlink",
			in:   "\x1b]8;;https://example.com\x07link\x1b]8;;\x07",
			want: 4,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := PrintableWidth(tc.in); got != tc.want {
				t.Fatalf("PrintableWidth(%q) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}
