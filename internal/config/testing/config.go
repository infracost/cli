package testing

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/infracost/cli/internal/api/dashboard"
	dashboardMock "github.com/infracost/cli/internal/api/dashboard/mocks"
	"github.com/infracost/cli/internal/api/events"
	eventsMock "github.com/infracost/cli/internal/api/events/mocks"
	"github.com/infracost/cli/internal/cache"
	"github.com/infracost/cli/internal/config"
	"github.com/infracost/cli/pkg/auth"
	"github.com/infracost/cli/pkg/environment"
	"github.com/infracost/cli/pkg/logging"
	"github.com/infracost/cli/pkg/plugins"
	"github.com/rs/zerolog"
)

func Config(t *testing.T) *config.Config {
	t.Helper()
	temp := t.TempDir()
	cfg := config.Config{
		Environment: environment.Environment{
			Value: environment.Local,
		},
		Currency: "USD",
		OrgID:    "testing-organization",
		Dashboard: dashboard.Config{
			Environment: environment.Local,
			Client: func(*http.Client) dashboard.Client {
				return new(dashboardMock.MockClient)
			},
		},
		Events: events.Config{
			ClientFn: func(*http.Client) events.Client {
				return new(eventsMock.MockClient)
			},
		},
		Auth: auth.Config{
			ExternalConfig: auth.ExternalConfig{
				AuthenticationToken: "testing-authentication-token", // shouldn't attempt to log in with this set
			},
			Environment: environment.Local,
		},
		Logging: logging.Config{
			WriteLevel: zerolog.TraceLevel.String(),
		},
		Plugins: plugins.Config{
			Cache: filepath.Join(temp, "plugins"),
			// Block real subprocess launches by default; component tests
			// override these injectors with mock plugins.
			LoadProviderPlugins: func(context.Context) ([]*plugins.ProviderPlugin, error) {
				return nil, nil
			},
		},
		Cache: cache.Config{
			Cache: filepath.Join(temp, "cache"),
		},
	}
	cfg.Logging.ForTest(t) // we'll make sure the logger uses the test output
	return &cfg
}
