package events

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDisabledClientDropsEvents(t *testing.T) {
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests++
	}))
	defer server.Close()

	cfg := &Config{Endpoint: server.URL, Disabled: true}
	client := cfg.Client(server.Client())
	require.IsType(t, noopClient{}, client)

	client.Push(context.Background(), "infracost-run", "key", "value")
	require.Zero(t, requests, "disabled events client must not send anything")
}
