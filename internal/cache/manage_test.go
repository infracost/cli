package cache

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseAge(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
	}{
		{"30d", 30 * 24 * time.Hour},
		{"2w", 2 * 7 * 24 * time.Hour},
		{"24h", 24 * time.Hour},
		{"12h30m", 12*time.Hour + 30*time.Minute},
		{"45m", 45 * time.Minute},
		// d/w only fire on a trailing single-char suffix — anything else
		// must remain valid Go duration syntax so we don't accidentally
		// shadow "24h" type values.
		{"1h", time.Hour},
	}
	for _, c := range cases {
		got, err := ParseAge(c.in)
		require.NoError(t, err, c.in)
		assert.Equal(t, c.want, got, c.in)
	}

	errCases := []string{"", "  ", "garbage", "5x", "-1d", "-30m", "1.5d"}
	for _, in := range errCases {
		_, err := ParseAge(in)
		assert.Error(t, err, in)
	}
}
