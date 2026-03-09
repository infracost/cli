package inspect

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
)

func writeTable(w io.Writer, headers []string, fill func(add func(row []string))) error {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)

	_, _ = fmt.Fprintln(tw, strings.Join(headers, "\t"))
	_, _ = fmt.Fprintln(tw, strings.Repeat("─\t", len(headers)))

	fill(func(row []string) {
		_, _ = fmt.Fprintln(tw, strings.Join(row, "\t"))
	})

	return tw.Flush()
}
