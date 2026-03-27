// Package process provides reflection-based config struct hydration. It populates
// struct fields from environment variables, CLI flags, and default values using
// struct tags, then runs post-parse validation via the Processor interface.
package process

import (
	"reflect"
)

var (
	processorType = reflect.TypeOf((*Processor)(nil)).Elem()
)

// Processor is implemented by config structs that need post-parse validation or
// derived computation. Process is called depth-first (children before parents)
// after flags have been parsed.
type Processor interface {
	Process()
}

// Process walks the target struct and calls Process on any fields (or the target
// itself) that implement Processor. The target must be a pointer to a struct.
func Process(target interface{}) {
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

	process(v)
}

func process(v reflect.Value) {
	t := v.Type()
	for i := 0; i < v.NumField(); i++ {
		if field := t.Field(i); !field.IsExported() {
			continue
		}

		fieldValue := v.Field(i)
		currentType, _ := unpackType(fieldValue.Type(), fieldValue.Addr().Type())
		if currentType.Kind() == reflect.Struct {
			current, _ := unpackValue(fieldValue)
			process(current)
		}
	}

	if v.Addr().Type().Implements(processorType) {
		v.Addr().Interface().(Processor).Process()
	}
}
