package cmds

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// maxNearbyEntries bounds the directory read behind a suggestion. A node_modules
// or a monorepo root is not worth walking to improve an error message.
const maxNearbyEntries = 500

// targetNotExistError reports a target that is not there, naming the path that
// was looked for and the directory it was resolved against.
//
// The bare "target directory does not exist" it replaces named neither. The
// path a person or an agent passes is usually relative, so the one that was
// actually checked is the one piece of information the message was missing.
func targetNotExistError(absolutePath string) error {
	msg := fmt.Sprintf("target directory does not exist: %s", absolutePath)

	if wd, err := os.Getwd(); err == nil {
		msg += fmt.Sprintf("\n  looked for it from: %s", wd)
	}
	if suggestion := nearbyPath(absolutePath); suggestion != "" {
		msg += fmt.Sprintf("\n  did you mean: %s", suggestion)
	}

	return errors.New(msg)
}

// nearbyPath returns a sibling of absolutePath whose name is a plausible
// correction, or "" when nothing obvious is next to it. It covers the two
// cases that actually occur: a case difference, and a name one is a prefix of.
func nearbyPath(absolutePath string) string {
	parent, base := filepath.Split(absolutePath)
	if base == "" {
		return ""
	}

	entries, err := os.ReadDir(parent)
	if err != nil || len(entries) > maxNearbyEntries {
		return ""
	}

	var prefixMatch string
	for _, entry := range entries {
		name := entry.Name()
		if strings.EqualFold(name, base) {
			return filepath.Join(parent, name)
		}
		// Only an unambiguous prefix match is worth offering: two candidates
		// mean the guess is as likely to mislead as to help.
		if sharedPrefix(name, base) {
			if prefixMatch != "" {
				return ""
			}
			prefixMatch = filepath.Join(parent, name)
		}
	}

	return prefixMatch
}

// minPrefixLen is the shortest shared prefix worth calling a typo. Below it a
// one or two letter sibling matches almost anything.
const minPrefixLen = 3

func sharedPrefix(name, base string) bool {
	shorter, longer := name, base
	if len(longer) < len(shorter) {
		shorter, longer = longer, shorter
	}
	return len(shorter) >= minPrefixLen && strings.HasPrefix(longer, shorter)
}
