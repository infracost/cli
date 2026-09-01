package registry

import (
	"fmt"
	"strings"

	"github.com/infracost/cli/internal/textutil"
)

// Resolve finds the registry entry matching a user-supplied plugin identifier.
// It matches, in order:
//
//  1. an exact registry name (`owner/repo`);
//  2. a component binaryName (e.g. `infracost-parser-kubernetes`), with a
//     trailing `.exe` tolerated so a Windows on-disk filename resolves;
//  3. an alias — a required-plugin key or legacy binary name mapped to a
//     registry name by the caller. This package cannot import pkg/plugins (that
//     would cycle once the installer consumes the registry), so callers pass
//     the required-set aliases in.
//
// On no match it returns an error naming the nearest registry name when one is
// close enough, giving callers a "did you mean" suggestion.
func (r *Registry) Resolve(input string, aliases map[string]string) (*Entry, error) {
	if strings.TrimSpace(input) == "" {
		return nil, fmt.Errorf("no plugin name provided")
	}

	if e := r.byName(input); e != nil {
		return e, nil
	}

	binaryQuery := strings.TrimSuffix(input, ".exe")
	for i := range r.Plugins {
		for _, c := range r.Plugins[i].Components {
			if c.BinaryName == input || c.BinaryName == binaryQuery {
				return &r.Plugins[i], nil
			}
		}
	}

	if name, ok := aliases[input]; ok {
		if e := r.byName(name); e != nil {
			return e, nil
		}
	}

	return nil, r.notFoundError(input)
}

// ByName returns the entry with the exact registry name, or nil.
func (r *Registry) ByName(name string) *Entry { return r.byName(name) }

func (r *Registry) byName(name string) *Entry {
	for i := range r.Plugins {
		if r.Plugins[i].Name == name {
			return &r.Plugins[i]
		}
	}
	return nil
}

// notFoundError builds a "not found in registry" error, appending a
// "did you mean" suggestion when the closest registry name is within a
// reasonable edit distance of the input.
func (r *Registry) notFoundError(input string) error {
	best := ""
	bestDist := -1
	lowerInput := strings.ToLower(input)
	for i := range r.Plugins {
		d := textutil.LevenshteinDistance(lowerInput, strings.ToLower(r.Plugins[i].Name))
		if bestDist < 0 || d < bestDist {
			bestDist = d
			best = r.Plugins[i].Name
		}
	}

	if best != "" && bestDist <= max(len(input)/2, 3) {
		return fmt.Errorf("plugin %q not found in registry — did you mean %q?", input, best)
	}
	return fmt.Errorf("plugin %q not found in registry", input)
}
