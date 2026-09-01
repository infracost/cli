package plugins

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/infracost/cli/pkg/logging"
)

const (
	// stateFileName is the provenance state file kept alongside managed plugin
	// binaries in the plugin cache directory. It records how each registry
	// entry got onto the machine so list/update/uninstall can tell registry
	// installs apart from the compiled-in required set and hand-copied
	// binaries. The name is a dotfile so it is easy to filter out of plugin
	// discovery (see isPluginSidecar).
	stateFileName = ".state.json"

	// stateSchemaVersion is the on-disk format version. It exists so a future
	// CLI can evolve the record shape without misreading an old file.
	stateSchemaVersion = 1
)

// warnCorruptStateOnce ensures the "state file was corrupt" warning is printed
// at most once per CLI process even if several commands load the state.
var warnCorruptStateOnce sync.Once

// StateComponent records one installed component of a registry entry.
type StateComponent struct {
	// Type is "parser" or "provider".
	Type string `json:"type"`
	// BinaryName is the on-disk binary filename (without any .exe suffix),
	// e.g. "infracost-parser-kubernetes".
	BinaryName string `json:"binaryName"`
}

// StateRecord is the provenance record for one registry-installed repository
// entry. It stores display metadata only — the official/author fields never
// gate the unofficial-plugin trust prompt, which re-runs on every download.
type StateRecord struct {
	// Name is the registry entry name (<github-owner>/<github-repository>).
	Name string `json:"name"`
	// Version is the shared release version every component was installed at.
	Version string `json:"version"`
	// Components is every installed component of the entry.
	Components []StateComponent `json:"components"`
	// Pinned is true when the entry was installed with an explicit @<version>.
	Pinned bool `json:"pinned"`
	// Official records the entry's official flag as of install time.
	Official bool `json:"official"`
	// Author records the entry's author as of install time.
	Author string `json:"author"`
	// InstalledAt is when the entry was installed.
	InstalledAt time.Time `json:"installedAt"`
}

// State is the on-disk provenance state for registry-installed plugins.
type State struct {
	SchemaVersion int           `json:"schemaVersion"`
	Records       []StateRecord `json:"records"`
}

// emptyState returns a valid, record-less state.
func emptyState() *State {
	return &State{SchemaVersion: stateSchemaVersion, Records: []StateRecord{}}
}

// stateFilePath returns the provenance state file path for a plugin cache
// directory.
func stateFilePath(cacheDir string) string {
	return filepath.Join(cacheDir, stateFileName)
}

// loadState reads the provenance state from the given plugin cache directory.
// Reads are tolerant: a missing file yields an empty state; a corrupt file is
// renamed aside (with a one-time warning) and treated as empty. It never
// returns an error — the state is advisory metadata, so a read failure must
// never break a plugin command.
func loadState(cacheDir string) *State {
	path := stateFilePath(cacheDir)

	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			logging.Debugf("could not read plugin state file %s: %v — treating as empty", path, err)
		}
		return emptyState()
	}

	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		setStateAside(path, err)
		return emptyState()
	}

	if s.SchemaVersion == 0 {
		s.SchemaVersion = stateSchemaVersion
	}
	if s.Records == nil {
		s.Records = []StateRecord{}
	}
	return &s
}

// setStateAside renames a corrupt state file to a ".corrupt" sibling so the
// next write starts clean, and warns the user once. If the rename fails the
// file is removed outright — either way the caller proceeds with an empty
// state.
func setStateAside(path string, cause error) {
	aside := path + ".corrupt"
	_ = os.Remove(aside)
	if err := os.Rename(path, aside); err != nil {
		logging.Debugf("could not set corrupt plugin state file %s aside: %v", path, err)
		_ = os.Remove(path)
		aside = ""
	}

	warnCorruptStateOnce.Do(func() {
		if aside != "" {
			logging.Warnf("plugin state file %s was unreadable (%v); it has been moved to %s and reset. Registry-installed plugins may show as unmanaged until reinstalled.", path, cause, aside)
			return
		}
		logging.Warnf("plugin state file %s was unreadable (%v) and has been reset. Registry-installed plugins may show as unmanaged until reinstalled.", path, cause)
	})
}

// save writes the state atomically to the given plugin cache directory: encode
// to a temp file in the same directory, then rename it into place so a
// concurrent reader never sees a half-written file. Records whose name has
// since joined the compiled-in required set are dropped first — required-set
// semantics own those plugins.
func (s *State) save(cacheDir string) error {
	s.SchemaVersion = stateSchemaVersion
	s.dropRequired()
	if s.Records == nil {
		s.Records = []StateRecord{}
	}

	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to encode plugin state: %w", err)
	}
	data = append(data, '\n')

	if err := os.MkdirAll(cacheDir, 0o750); err != nil {
		return fmt.Errorf("failed to create plugin cache directory: %w", err)
	}

	tmp, err := os.CreateTemp(cacheDir, ".state-*.tmp")
	if err != nil {
		return fmt.Errorf("failed to create temp plugin state file: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("failed to write plugin state: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("failed to close temp plugin state file: %w", err)
	}

	path := stateFilePath(cacheDir)
	// os.Rename atomically replaces an existing file on Unix; on Windows it
	// fails if the destination exists, so remove it first (mirrors the binary
	// install flow).
	if runtime.GOOS == "windows" {
		_ = os.Remove(path)
	}
	if err := renameWithRetry(tmpPath, path); err != nil {
		return fmt.Errorf("failed to write plugin state file: %w", err)
	}
	return nil
}

// find returns a pointer to the record for name, or nil if absent. The pointer
// aliases the slice backing array, so callers may mutate it in place before a
// save.
func (s *State) find(name string) *StateRecord {
	for i := range s.Records {
		if s.Records[i].Name == name {
			return &s.Records[i]
		}
	}
	return nil
}

// upsert replaces the record with the same name, or appends it when absent.
func (s *State) upsert(rec StateRecord) {
	for i := range s.Records {
		if s.Records[i].Name == rec.Name {
			s.Records[i] = rec
			return
		}
	}
	s.Records = append(s.Records, rec)
}

// remove drops the record for name. It reports whether a record was removed.
func (s *State) remove(name string) bool {
	for i := range s.Records {
		if s.Records[i].Name == name {
			s.Records = append(s.Records[:i], s.Records[i+1:]...)
			return true
		}
	}
	return false
}

// dropRequired removes records for names that are now in the compiled-in
// required set. Such plugins are managed by the required-set flow, so a stale
// provenance record for them is discarded on the next write.
func (s *State) dropRequired() {
	if len(s.Records) == 0 {
		return
	}
	kept := s.Records[:0]
	for _, rec := range s.Records {
		if isRequiredName(rec.Name) {
			continue
		}
		kept = append(kept, rec)
	}
	s.Records = kept
}

// isRequiredName reports whether name matches a compiled-in required plugin.
// Required plugins report their name as <owner>/<repo> (the DisplayName),
// matching the registry entry name form.
func isRequiredName(name string) bool {
	for _, r := range requiredPlugins {
		if r.DisplayName == name {
			return true
		}
	}
	return false
}
