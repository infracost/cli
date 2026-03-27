package process

import "reflect"

// unpackType will unwrap the type, iterating through pointers and interfaces until the real type has been discovered.
// It returns the source type and the parent type to it.
func unpackType(t reflect.Type, parent reflect.Type) (reflect.Type, reflect.Type) {
	for t.Kind() == reflect.Pointer {
		parent = t
		t = t.Elem() // then unpack all the pointers to get the real core type
	}
	return t, parent
}

// unpackValue will unwrap the provided value, iterating through pointers until the real value has been discovered.
// It returns the source value and the parent value to it.
//
// We'll initialize pointers as we go, but not the inner most pointer. Callers must check the returned parent value
// for nil, before they try and set any values on the returned value.
func unpackValue(value reflect.Value) (reflect.Value, reflect.Value) {
	parent := value.Addr()
	for value.Kind() == reflect.Pointer {
		parent = value
		if value.IsNil() {
			value.Set(reflect.New(value.Type().Elem()))
		}
		value = value.Elem()
	}
	return value, parent
}
