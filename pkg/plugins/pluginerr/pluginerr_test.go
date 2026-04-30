package pluginerr

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestClassifyConnect(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want error
	}{
		{name: "nil", err: nil, want: nil},
		{name: "start timeout", err: errors.New("timeout while waiting for plugin to start"), want: ErrPluginHandshakeTimeout},
		{name: "exited before connect", err: errors.New("plugin exited before we could connect"), want: ErrPluginExecFailed},
		{name: "exec format error", err: errors.New("fork/exec /tmp/p: exec format error"), want: ErrPluginExecFailed},
		{name: "no such file", err: errors.New("fork/exec /tmp/p: no such file or directory"), want: ErrPluginExecFailed},
		{name: "permission denied", err: errors.New("fork/exec /tmp/p: permission denied"), want: ErrPluginExecFailed},
		{name: "incompatible version", err: errors.New("incompatible core API version with plugin. Plugin version: 2"), want: ErrPluginHandshake},
		{name: "unknown handshake", err: errors.New("Unrecognized remote plugin message: garbage"), want: ErrPluginHandshake},
		{name: "already classified", err: fmt.Errorf("%w: original", ErrPluginExecFailed), want: ErrPluginExecFailed},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyConnect(tc.err)
			if tc.want == nil {
				assert.Nil(t, got)
				return
			}
			assert.True(t, errors.Is(got, tc.want), "expected wrap of %v, got: %v", tc.want, got)
		})
	}
}
