package providers

import (
	"testing"

	proto "github.com/infracost/proto/gen/go/infracost/provider"
	"github.com/stretchr/testify/require"
)

func TestCreateTreeCacheKeyIncludesProviderVersion(t *testing.T) {
	input := &proto.TreeInput{AbsolutePath: t.TempDir()}

	v1 := createTreeCacheKey(proto.Provider_PROVIDER_AWS, input, "1.0.0")
	v2 := createTreeCacheKey(proto.Provider_PROVIDER_AWS, input, "2.0.0")

	require.NotEqual(t, v1, v2)
}
