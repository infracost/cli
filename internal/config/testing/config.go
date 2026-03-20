package testing

import (
	"net/http"
	"path/filepath"
	"testing"

	"github.com/hashicorp/go-hclog"
	"github.com/infracost/cli/internal/api/dashboard"
	dashboardMock "github.com/infracost/cli/internal/api/dashboard/mocks"
	"github.com/infracost/cli/internal/api/events"
	eventsMock "github.com/infracost/cli/internal/api/events/mocks"
	"github.com/infracost/cli/internal/cache"
	"github.com/infracost/cli/internal/config"
	"github.com/infracost/cli/internal/logging"
	"github.com/infracost/cli/pkg/auth"
	"github.com/infracost/cli/pkg/environment"
	"github.com/infracost/cli/pkg/plugins"
	"github.com/infracost/cli/pkg/plugins/parser"
	parserMock "github.com/infracost/cli/pkg/plugins/parser/mocks"
	"github.com/infracost/cli/pkg/plugins/providers"
	providerMock "github.com/infracost/cli/pkg/plugins/providers/mocks"
	"github.com/infracost/proto/gen/go/infracost/parser/api"
	"github.com/infracost/proto/gen/go/infracost/provider"
	"github.com/rs/zerolog"
)

func Config(t *testing.T) config.Config {
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
			Providers: providers.Config{
				// by setting the AWS, Google and Azure fields, we should prevent the system from hitting the manifest
				// url when these are required.
				AWS:    "aws",
				Google: "google",
				Azure:  "azure",
				LoadAWS: func(hclog.Level) (provider.ProviderServiceClient, func(), error) {
					return new(providerMock.MockProviderServiceClient), func() {}, nil
				},
				LoadGoogle: func(hclog.Level) (provider.ProviderServiceClient, func(), error) {
					return new(providerMock.MockProviderServiceClient), func() {}, nil
				},
				LoadAzurerm: func(hclog.Level) (provider.ProviderServiceClient, func(), error) {
					return new(providerMock.MockProviderServiceClient), func() {}, nil
				},
			},
			Parser: parser.Config{
				Plugin: "parser", // set this so it doesn't attempt to load the manifest url
				Load: func(hclog.Level) (api.ParserServiceClient, func(), error) {
					return new(parserMock.MockParserServiceClient), func() {}, nil
				},
			},
			Cache: filepath.Join(temp, "plugins"), // hopefully shouldn't use the cache
		},
		Cache: cache.Config{
			Cache:     filepath.Join(temp, "cache"),
		},
	}
	cfg.Logging.ForTest(t) // we'll make sure the logger uses the test output
	return cfg
}
