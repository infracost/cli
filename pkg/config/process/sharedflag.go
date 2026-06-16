package process

import (
	"strconv"

	"github.com/spf13/pflag"
)

// SharedFlag implementations can share the value they're give with other structs in a single operation.
type SharedFlag interface {
	pflag.Value
	AddTarget(target *string)
}

// SharedBoolFlag is the bool counterpart to SharedFlag.
type SharedBoolFlag interface {
	pflag.Value
	AddBoolTarget(target *bool)
}

// BoolFlag is a SharedBoolFlag that mirrors the value parsed from a flag or
// environment variable to any number of registered bool targets.
type BoolFlag struct {
	Value   bool
	targets []*bool
}

func (b *BoolFlag) String() string {
	return strconv.FormatBool(b.Value)
}

func (b *BoolFlag) Set(s string) error {
	v, err := strconv.ParseBool(s)
	if err != nil {
		return err
	}
	b.Value = v
	for _, t := range b.targets {
		*t = v
	}
	return nil
}

func (b *BoolFlag) Type() string { return "bool" }

// IsBoolFlag tells pflag to treat this as a switch (--name implies true).
func (b *BoolFlag) IsBoolFlag() bool { return true }

func (b *BoolFlag) AddBoolTarget(target *bool) {
	*target = b.Value
	b.targets = append(b.targets, target)
}
