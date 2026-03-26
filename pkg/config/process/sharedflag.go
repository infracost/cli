package process

import "github.com/spf13/pflag"

// SharedFlag implementations can share the value they're give with other structs in a single operation.
type SharedFlag interface {
	pflag.Value
	AddTarget(target *string)
}
