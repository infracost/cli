package cache

import (
	"fmt"
	"sync"

	"github.com/infracost/cli/internal/format"
)

// MemoryStore is a process-local cache implementation. Each entry is kept
// in a map keyed by the absolute scan path; the store has no TTL and is
// discarded when the process exits. Used by `infracost mcp` so a session's
// scan results stay available to subsequent inspect tool calls without
// touching the user's disk cache.
type MemoryStore struct {
	mu      sync.RWMutex
	entries map[string]*format.Output
	// latestKey points at the most recently [Write]-en entry so [Latest]
	// is O(1) instead of walking every key. Empty string until the first
	// Write — both Latest's "no cached results" path and an unset
	// latestKey return the same error.
	latestKey string
}

// NewMemoryStore returns an empty in-memory [Store].
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{entries: make(map[string]*format.Output)}
}

// Write stores data against absPath, overwriting any previous entry, and
// promotes absPath to be the latest entry returned by [Latest].
func (m *MemoryStore) Write(absPath string, data *format.Output) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries[absPath] = data
	m.latestKey = absPath
	return nil
}

// ForPath returns the cached result for absPath. The freshness checks the
// disk store performs (TTL, source file mtime) are skipped here — entries
// live only for the lifetime of the MCP process, and the agent caller is
// expected to re-scan when it needs fresh data.
func (m *MemoryStore) ForPath(absPath string) (*format.Output, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	data, ok := m.entries[absPath]
	if !ok {
		return nil, fmt.Errorf("no cached results found")
	}
	return data, nil
}

// ForPathAllowStale is identical to ForPath here — MemoryStore has no
// notion of staleness.
func (m *MemoryStore) ForPathAllowStale(absPath string) (*format.Output, error) {
	return m.ForPath(absPath)
}

// Latest returns the most-recently-written entry in O(1) via the
// latestKey pointer maintained by [Write]. allowStale is ignored.
func (m *MemoryStore) Latest(_ bool) (*format.Output, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.latestKey == "" {
		return nil, fmt.Errorf("no cached results found")
	}
	return m.entries[m.latestKey], nil
}
