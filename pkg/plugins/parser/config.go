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
}

func (c *Config) Process() {
	c.Load = func(level hclog.Level) (api.ParserServiceClient, func(), error) {
		return Connect(c.Plugin, level)
	}
}
