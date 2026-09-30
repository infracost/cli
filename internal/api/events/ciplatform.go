package events

import (
	"strconv"
	"strings"
	"sync"
)

// maxPlatformLabelLen caps a label from the environment. Real platform names
// are short; anything longer is not one.
const maxPlatformLabelLen = 48

var normalizedCIPlatform = sync.OnceValue(func() string {
	return normalizeCIPlatform(getCIPlatform())
})

// NormalizedCIPlatform returns the CI platform reported in event metadata.
func NormalizedCIPlatform() string { return normalizedCIPlatform() }

// normalizeCIPlatform maps a raw platform to a reportable label, trusting the
// value as far as its source earns. INFRACOST_CI_PLATFORM is set by our own
// tooling to name a CI detection cannot see, so it is bounded by shape; the
// bare CI variable is whatever the user's environment holds, so it must name a
// platform we already know.
func normalizeCIPlatform(raw string, source ciPlatformSource) string {
	platform := strings.ToLower(raw)
	if platform == "" {
		return ""
	}
	if b, err := strconv.ParseBool(platform); err == nil {
		return unknownOrEmpty(b)
	}
	switch platform {
	case "yes", "on":
		return "unknown"
	case "no", "off":
		return ""
	}
	// Azure with no BUILD_REPOSITORY_PROVIDER set: shaped like a label, but it
	// names no provider.
	if platform == "azure_devops_" {
		return "unknown"
	}

	switch source {
	case ciPlatformProducer, ciPlatformDetected:
		if !isPlatformLabel(platform) {
			return "unknown"
		}
		return platform
	case ciPlatformCIVar:
		if !knownCIPlatforms()[platform] {
			return "unknown"
		}
		return platform
	}
	return "unknown"
}

// knownCIPlatforms is every label our own detection can produce. It bounds the
// bare CI variable, which no producer of ours controls.
var knownCIPlatforms = sync.OnceValue(func() map[string]bool {
	known := make(map[string]bool, len(ciPlatformEnvs)+len(ciPlatformEnvPrefixes))
	for _, platform := range ciPlatformEnvs {
		known[platform] = true
	}
	for _, platform := range ciPlatformEnvPrefixes {
		known[platform] = true
	}
	return known
})

// isPlatformLabel reports whether s is shaped like a platform name. Paths,
// URLs, whitespace and anything overlong are rejected: they are not labels,
// and they are the shapes that carry user data.
func isPlatformLabel(s string) bool {
	if s == "" || len(s) > maxPlatformLabelLen {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_', r == '-', r == '.':
		default:
			return false
		}
	}
	return true
}

func unknownOrEmpty(inCI bool) string {
	if inCI {
		return "unknown"
	}
	return ""
}
