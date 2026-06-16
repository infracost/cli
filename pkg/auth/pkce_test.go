package auth

import (
	"net"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCallbackListeners_PortInUse(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() {
		_ = listener.Close()
	}()

	port := listener.Addr().(*net.TCPAddr).Port
	cfg := &Config{InternalConfig: InternalConfig{CallbackPort: port}}

	listeners, err := cfg.callbackListeners()
	closeCallbackListeners(listeners)
	require.Error(t, err)
	require.True(t, isCallbackPortInUse(err))
}
