package inspect

import (
	"io"

	"github.com/infracost/cli/internal/format"
)

type Options struct {
	Summary   bool
	GroupBy   []string // type, provider, project, policy
	Policy    string   // filter to a specific policy name/slug
	Resource  string   // filter to a specific resource address
	Provider  string
	Project   string
	CostsOnly bool
	Failing   bool
	Top       int
	JSON      bool
}

func Run(w io.Writer, data *format.Output, opts Options) error {
	filtered := Filter(data, opts)

	if opts.Policy != "" {
		return WritePolicyDetail(w, filtered, opts)
	}

	if len(opts.GroupBy) == 0 {
		switch {
		case opts.Resource != "":
			opts.GroupBy = []string{"policy"}
		case opts.Provider != "" || opts.Top > 0 || opts.CostsOnly:
			opts.GroupBy = []string{"type"}
		}
	}

	if opts.Summary && opts.Resource == "" {
		return WriteSummary(w, filtered, opts.JSON)
	}

	if len(opts.GroupBy) > 0 {
		return WriteGroupBy(w, filtered, opts)
	}

	return WriteSummary(w, filtered, opts.JSON)
}
