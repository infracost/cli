package process

import (
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/infracost/go-proto/pkg/diagnostic"
	parserpb "github.com/infracost/proto/gen/go/infracost/parser"
	"github.com/spf13/pflag"
)

var (
	valueType = reflect.TypeOf((*pflag.Value)(nil)).Elem()
)

// PreProcess walks the target struct and hydrates fields based on struct tags.
// Fields tagged with `env:"VAR"` are populated from environment variables,
// `default:"val"` sets a fallback when no env var is found and the field is zero,
// and `flag:"name"` registers a pflag on the provided FlagSet. Nested structs
// are recursed into unless they implement pflag.Value, in which case they are
// treated as leaf values. The target must be a pointer to a struct.
func PreProcess(target interface{}, flags *pflag.FlagSet) *diagnostic.Diagnostics {
	v := reflect.ValueOf(target)

	if v.Kind() == reflect.Interface {
		v = v.Elem()
	}

	if v.Kind() != reflect.Pointer {
		panic("target must be a pointer to a struct")
	}
	v = v.Elem()

	if v.Kind() != reflect.Struct {
		panic("target must be a pointer to a struct")
	}

	return preprocess(v, flags)
}

func preprocess(v reflect.Value, flags *pflag.FlagSet) *diagnostic.Diagnostics {
	var diags *diagnostic.Diagnostics

	t := v.Type()
	for i := 0; i < v.NumField(); i++ {
		field := t.Field(i)
		fieldValue := v.Field(i)

		if !field.IsExported() {
			continue
		}

		envName, hasEnvName := field.Tag.Lookup("env")
		flagValue, hasFlagValue := field.Tag.Lookup("flag")
		flagTargetName, hasFlagTarget := field.Tag.Lookup("flagvalue")
		defaultValue, hasDefaultValue := field.Tag.Lookup("default")

		hasEnvName = hasEnvName && envName != ""
		hasFlagValue = hasFlagValue && flagValue != ""
		hasFlagTarget = hasFlagTarget && flagTargetName != ""
		hasDefaultValue = hasDefaultValue && defaultValue != ""

		currentType, parentType := unpackType(fieldValue.Type(), fieldValue.Addr().Type())
		isPflagValue := parentType.Implements(valueType)

		if currentType.Kind() == reflect.Struct && !isPflagValue {
			if hasEnvName || hasFlagValue || hasDefaultValue || hasFlagTarget {
				// programmer error, so we panic
				panic("nested structs cannot be tagged with env, flag, flagvalue or default, or they must implement pflag.Value")
			}

			current, _ := unpackValue(fieldValue)

			// Then we have a struct that needs to be processed recursively.
			if err := preprocess(current, flags); err != nil {
				return err
			}
		}

		if hasFlagTarget && (hasFlagValue || hasEnvName || hasDefaultValue) {
			panic("flagvalue cannot be combined with flag, env, or default tags")
		}

		if !hasEnvName && !hasFlagValue && !hasFlagTarget && !hasDefaultValue {
			// if we have no env, flag, flagvalue or default value then we're not going to touch this field
			continue
		}

		hasEnvValue := false
		if hasEnvName {
			if value := os.Getenv(envName); len(value) > 0 {

				current, _ := unpackValue(fieldValue)
				hasEnvValue = true
				if err := setFieldValue(current, value, isPflagValue); err != nil {
					// this means the user put a string into something that expected a boolean or a number or something
					// this is a user error, so we return an error for them.
					diags = diags.Add(diagnostic.New(parserpb.DiagnosticType_DIAGNOSTIC_TYPE_UNSPECIFIED, "invalid value for environment variable %s: %w", envName, err))
					continue
				}
			}
		}

		if !hasEnvValue && hasDefaultValue {
			current, _ := unpackValue(fieldValue)
			if current.IsZero() {
				if err := setFieldValue(current, defaultValue, isPflagValue); err != nil {
					// this is a programmer error, so we panic
					// if the default value can't be set, then the programmer is probably doing something wrong
					panic(err)
				}
			}
		}

		if hasFlagValue {
			var hidden bool
			parts := strings.Split(flagValue, ";")
			flagName := parts[0]
			for _, part := range parts[1:] {
				if part == "hidden" {
					hidden = true
				}
			}

			_, parent := unpackValue(fieldValue)
			registerFlag(parent, flags, flagName, field.Tag.Get("usage"), hidden, isPflagValue)
		}

		if hasFlagTarget {
			existing := flags.Lookup(flagTargetName)
			if existing == nil {
				panic(fmt.Sprintf("flagvalue %q references flag that has not been registered", flagTargetName))
			}

			sf, ok := existing.Value.(SharedFlag)
			if !ok {
				panic(fmt.Sprintf("flagvalue %q references flag that does not implement SharedFlag", flagTargetName))
			}

			if fieldValue.Kind() != reflect.String {
				panic(fmt.Sprintf("flagvalue %q can only be used on string fields", flagTargetName))
			}

			target := fieldValue.Addr().Interface().(*string)
			if existing.DefValue != "" {
				*target = existing.DefValue
			}
			sf.AddTarget(target)
		}

	}

	return diags
}

// setFieldValue sets a reflected field's value from a string. It handles
// pflag.Value types, time.Duration, and primitive kinds (string, int, bool).
func setFieldValue(v reflect.Value, s string, isPflagValue bool) error {
	if isPflagValue {
		pv := v.Addr().Interface().(pflag.Value)
		return pv.Set(s)
	}

	if v.Type() == reflect.TypeOf(time.Duration(0)) {
		d, err := time.ParseDuration(s)
		if err != nil {
			return fmt.Errorf("expected duration, got %q", s)
		}
		v.SetInt(int64(d))
		return nil
	}

	switch v.Kind() {
	case reflect.String:
		v.SetString(s)
	case reflect.Int:
		i, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return fmt.Errorf("expected integer, got string")
		}
		v.SetInt(i)
	case reflect.Bool:
		// boolean has a couple of special cases:
		if len(s) == 0 {
			// an empty value is actually true for a boolean, it means someone set the environment variable so set it
			// to true
			v.SetBool(true)
			break
		}

		// similarly, 1 or 0 are special cases that are common to be assigned to boolean environment variables

		if s == "1" {
			v.SetBool(true)
			break
		}

		if s == "0" {
			v.SetBool(false)
			break
		}

		b, err := strconv.ParseBool(s)
		if err != nil {
			return fmt.Errorf("expected boolean, got string")
		}
		v.SetBool(b)
	default:
		// this means the programmer tried to assign a flag to an unsupported value, so we panic
		// not something a user should ever see, and we want them to tell us if they do
		panic(fmt.Errorf("unsupported type for value: %s", v.Kind()))
	}
	return nil
}

// registerFlag will register a flag with the provided flags set. The provided value must be a pointer to a value.
func registerFlag(v reflect.Value, flags *pflag.FlagSet, name string, usage string, hidden bool, isPflagValue bool) {
	if isPflagValue {
		pv := v.Interface().(pflag.Value)
		flags.Var(pv, name, usage)
	} else {
		switch v.Type().Elem().Kind() {
		case reflect.String:
			var defaultValue string
			if !v.IsNil() {
				defaultValue = v.Elem().String()
			}
			flags.StringVar(v.Interface().(*string), name, defaultValue, usage)
		case reflect.Int:
			var defaultValue int64
			if !v.IsNil() {
				defaultValue = v.Elem().Int()
			}
			flags.IntVar(v.Interface().(*int), name, int(defaultValue), usage)
		case reflect.Bool:
			var defaultValue bool
			if !v.IsNil() {
				defaultValue = v.Elem().Bool()
			}
			flags.BoolVar(v.Interface().(*bool), name, defaultValue, usage)
		default:
			// this means the programmer tried to assign a flag to an unsupported value, so we panic
			// not something a user should ever see, and we want them to tell us if they do
			panic(fmt.Errorf("unsupported type for flag: %s", v.Kind()))
		}
	}

	if hidden {
		if err := flags.MarkHidden(name); err != nil {
			panic(err) // panic as this should never happen
		}
	}
}
