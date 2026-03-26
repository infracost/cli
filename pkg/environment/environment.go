package environment

import (
	"fmt"

	"github.com/infracost/cli/pkg/config/process"
)

const (
	Production  = "prod"
	Development = "dev"
	Local       = "local"
)

var _ process.SharedFlag = (*Environment)(nil)

type Environment struct {
	Value   string
	targets []*string
}

func (e *Environment) String() string {
	return e.Value
}

func (e *Environment) Set(s string) error {
	switch env := s; env {
	case Production, Development, Local:
		e.Value = s
		for _, target := range e.targets {
			*target = s
		}
		return nil
	default:
		return fmt.Errorf("invalid environment: %s", s)
	}
}

func (e *Environment) Type() string {
	return "environment"
}

func (e *Environment) AddTarget(target *string) {
	e.targets = append(e.targets, target)
}
