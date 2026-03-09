package providers

import (
	"encoding/hex"
	"hash/fnv"
	"strconv"
	"time"

	"github.com/infracost/cli/internal/protocache"
	protoprovider "github.com/infracost/proto/gen/go/infracost/provider"
	"google.golang.org/protobuf/proto"
)

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
