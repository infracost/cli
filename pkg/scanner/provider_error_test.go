package scanner

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestWrapProviderError(t *testing.T) {
	quotaMsg := "Infracost free monthly run limit exceeded. Please contact hello@infracost.io to purchase usage pack."

	taggedQuota := func(t *testing.T) error {
		t.Helper()
		st, err := status.New(codes.ResourceExhausted, quotaMsg).WithDetails(&errdetails.ErrorInfo{
			Reason: runQuotaReason,
			Domain: "pricing.api.infracost.io",
		})
		require.NoError(t, err)
		return st.Err()
	}

	t.Run("tagged quota status becomes a typed error", func(t *testing.T) {
		got := wrapProviderError("infracost/aws", taggedQuota(t))

		var quotaErr *RunQuotaExceededError
		require.True(t, errors.As(got, &quotaErr), "expected a RunQuotaExceededError, got %v", got)
		assert.Equal(t, quotaMsg, quotaErr.Message)
	})

	// gRPC returns ResourceExhausted when a plugin's output exceeds the max
	// message size. Without the ErrorInfo tag that would be reported to the user
	// as a billing problem.
	t.Run("untagged resource-exhausted is not a quota error", func(t *testing.T) {
		err := status.Error(codes.ResourceExhausted, "grpc: received message larger than max")

		got := wrapProviderError("infracost/aws", err)

		var quotaErr *RunQuotaExceededError
		assert.False(t, errors.As(got, &quotaErr))
		assert.Contains(t, got.Error(), "failed to execute provider infracost/aws")
	})

	// Only the pricing API's own tag may put a billing message in front of the
	// user, so a plugin reusing the reason with another domain is not trusted.
	t.Run("quota reason from another domain is not a quota error", func(t *testing.T) {
		st, err := status.New(codes.ResourceExhausted, "something else").WithDetails(&errdetails.ErrorInfo{
			Reason: runQuotaReason,
			Domain: "plugin.example.com",
		})
		require.NoError(t, err)

		got := wrapProviderError("infracost/aws", st.Err())

		var quotaErr *RunQuotaExceededError
		assert.False(t, errors.As(got, &quotaErr))
		assert.Contains(t, got.Error(), "failed to execute provider infracost/aws")
	})

	t.Run("other errors are wrapped with the provider name", func(t *testing.T) {
		err := errors.New("boom")

		got := wrapProviderError("infracost/aws", err)

		assert.Contains(t, got.Error(), "failed to execute provider infracost/aws")
		assert.ErrorIs(t, got, err)
	})
}
