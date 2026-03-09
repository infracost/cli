package environment

import (
	"fmt"

	"github.com/spf13/pflag"
)

const (
	Production  Environment = "prod"
	Development Environment = "dev"
	Local       Environment = "local"
)

var _ pflag.Value = (*Environment)(nil)

type Environment string

func (e *Environment) String() string {
	return fmt.Sprintf("environment(%s)", *e)
}

func (e *Environment) Set(s string) error {
	switch env := Environment(s); env {
	case Production, Development, Local:
		*e = env
		return nil
	default:
		return fmt.Errorf("invalid environment: %s", s)
	}
}

func (e *Environment) Type() string {
	return "environment"
}
