package providers

import (
	"encoding/hex"
	"hash/fnv"
	"os"
	"strconv"
	"time"

	"github.com/infracost/cli/internal/protocache"
	protoprovider "github.com/infracost/proto/gen/go/infracost/provider"
	"google.golang.org/protobuf/proto"
)

// providerCacheVersion returns a stable string keyed to the provider
// plugin binary that handles `prov`. Used in the process-cache key so
// rebuilding a provider plugin invalidates cached output. Falls back
// to "" when the path is unset or can't be statted — pre-version
// behavior — so callers that don't override the path still cache.
func (c *Config) providerCacheVersion(prov protoprovider.Provider) string {
	path, _ := c.providerOverrideFor(prov)
	if path == "" {
		return ""
	}
	info, err := os.Stat(path)
	if err != nil {
		return ""
	}
	return strconv.FormatInt(info.ModTime().UnixNano(), 10)
}

func (c *Config) providerOverrideFor(prov protoprovider.Provider) (string, string) {
	switch prov {
	case protoprovider.Provider_PROVIDER_AWS:
		return c.AWS, c.AWSVersion
	case protoprovider.Provider_PROVIDER_GOOGLE:
		return c.Google, c.GoogleVersion
	case protoprovider.Provider_PROVIDER_AZURERM:
		return c.Azure, c.AzureVersion
	default:
		return "", ""
	}
}

func createCacheKey(prov protoprovider.Provider, input *protoprovider.Input, providerVersion string) protocache.Key {
	// Clone the input so we can zero out volatile fields (like API tokens and
	// trace IDs) that change between runs but don't affect the output.
	stable := proto.Clone(input).(*protoprovider.Input)
	stable.Infracost = nil

	h := fnv.New128a()
	h.Write([]byte(providerVersion))
	h.Write([]byte{0})
	h.Write([]byte{byte(time.Now().UTC().Day())}) //nolint:gosec // G115: day-of-month (1-31) always fits in a byte
	h.Write([]byte{0})
	h.Write([]byte(strconv.Itoa(int(prov))))
	h.Write([]byte{0})
	opts := proto.MarshalOptions{Deterministic: true}
	if j, err := opts.Marshal(stable); err == nil {
		h.Write(j)
	}
	return protocache.Key(hex.EncodeToString(h.Sum(nil)))
}
