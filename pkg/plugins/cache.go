package plugins

import (
	"encoding/hex"
	"hash/fnv"
	"time"

	"github.com/infracost/cli/internal/protocache"
	protoprovider "github.com/infracost/proto/gen/go/infracost/provider"
	"google.golang.org/protobuf/proto"
)

func createTreeCacheKey(pluginName, pluginVersion string, input *protoprovider.TreeInput) protocache.Key {
	stable := proto.Clone(input).(*protoprovider.TreeInput)
	stable.Infracost = nil

	h := fnv.New128a()
	h.Write([]byte(pluginName))
	h.Write([]byte{0})
	h.Write([]byte(pluginVersion))
	h.Write([]byte{0})
	h.Write([]byte{byte(time.Now().UTC().Day())}) //nolint:gosec // G115: day-of-month (1-31) always fits in a byte
	h.Write([]byte{0})
	opts := proto.MarshalOptions{Deterministic: true}
	if j, err := opts.Marshal(stable); err == nil {
		h.Write(j)
	}
	return protocache.Key(hex.EncodeToString(h.Sum(nil)))
}
