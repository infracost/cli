package parser

import (
	"github.com/hashicorp/go-hclog"
	"github.com/infracost/cli/pkg/config/process"
	"github.com/infracost/proto/gen/go/infracost/parser/api"
)

var (
	_ process.Processor = (*Config)(nil)
)

type Config struct {
	Plugin  string `env:"INFRACOST_CLI_PARSER_PLUGIN"`
	Version string `env:"INFRACOST_CLI_PARSER_PLUGIN_VERSION"`

	Load func(level hclog.Level) (api.ParserServiceClient, func(), error)

	// SupportedARMResources, when non-nil, is sent in
	// InitializeRequest.arm_supported_resources before every parseARM
	// run. The scanner populates this once per `infracost scan` (via
	// the Azurerm provider plugin's ListSupportedResources RPC) when
	// any project in the run is ARM-typed. Without it, the parser
	// falls back to "everything supported" and ARM resource types
	// the providers binary doesn't handle get silently dropped on
	// the providers translator's unhandled-fallthrough — the
	// long-standing CFN-parity gap noted in parser/TODO.md.
	SupportedARMResources *api.SupportedResources
}

func (c *Config) Process() {
	c.Load = func(level hclog.Level) (api.ParserServiceClient, func(), error) {
		return Connect(c.Plugin, level)
	}
}
