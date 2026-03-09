package trace

import (
	"fmt"

	"github.com/infracost/cli/version"
)

var (
	UserAgent = fmt.Sprintf("infracost-cliv2-%s", version.Version)
)
