package dashboard_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/infracost/cli/internal/api/dashboard"
)

func TestCreateOrganizationSendsSourceAndTrigger(t *testing.T) {
	var request struct {
		Variables map[string]map[string]interface{} `json:"variables"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		_, _ = w.Write([]byte(`{"data":{"createOrganization":{"id":"org-1","name":"Acme","slug":"acme"}}}`))
	}))
	t.Cleanup(server.Close)
	cfg := &dashboard.Config{Endpoint: server.URL}
	cfg.Process()

	result, err := cfg.Client(server.Client()).CreateOrganization(context.Background(), "Acme", dashboard.OrgCreateTriggerCISetup)

	require.NoError(t, err)
	assert.Equal(t, "org-1", result.ID)
	assert.Equal(t, map[string]interface{}{"name": "Acme", "source": "Cli", "trigger": "CiSetup"}, request.Variables["organization"])
}
