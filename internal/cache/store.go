package cache

import (
	"github.com/infracost/cli/internal/format"
)

// Store is the backing storage for cached scan results. Implementations
// decide where the data lives (disk, memory, …). [Config] embeds a Store
// and delegates its data operations to it; the default is the disk-backed
// store but the MCP server swaps in [MemoryStore] so MCP sessions don't
// leak results onto the user's filesystem.
type Store interface {
	Write(absPath string, data *format.Output) error
	ForPath(absPath string) (*format.Output, error)
	ForPathAllowStale(absPath string) (*format.Output, error)
	Latest(allowStale bool) (*format.Output, error)
}