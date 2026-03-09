package parser

import (
	"github.com/hashicorp/go-hclog"
	"github.com/infracost/proto/gen/go/infracost/parser/api"
)

type Config struct {
	Plugin  string `env:"INFRACOST_CLI_PARSER_PLUGIN"`
	Version string `env:"INFRACOST_CLI_PARSER_PLUGIN_VERSION"`
}

func (c *Config) Load(level hclog.Level) (api.ParserServiceClient, func(), error) {
	return Connect(c.Plugin, level)
}
