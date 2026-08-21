package store

import (
	"reflect"
	"testing"
)

func TestStoreAPINeverAcceptsValueBytes(t *testing.T) {
	storeTypes := []reflect.Type{reflect.TypeFor[Store](), reflect.TypeFor[*Store]()}
	for _, storeType := range storeTypes {
		for methodIndex := 0; methodIndex < storeType.NumMethod(); methodIndex++ {
			method := storeType.Method(methodIndex)
			for parameterIndex := 1; parameterIndex < method.Type.NumIn(); parameterIndex++ {
				parameter := method.Type.In(parameterIndex)
				if containsByteSequence(parameter, make(map[reflect.Type]bool)) {
					t.Errorf("%s parameter %d reaches %v", method.Name, parameterIndex, parameter)
				}
			}
		}
	}
	type bad struct{ V []byte }
	if !containsByteSequence(reflect.TypeFor[bad](), make(map[reflect.Type]bool)) {
		t.Fatal("byte-sequence walker did not reject sanity-check type")
	}
}

func containsByteSequence(value reflect.Type, visited map[reflect.Type]bool) bool {
	if value == nil || visited[value] {
		return false
	}
	visited[value] = true
	switch value.Kind() {
	case reflect.Interface:
		return false
	case reflect.Pointer:
		return containsByteSequence(value.Elem(), visited)
	case reflect.Slice, reflect.Array:
		return value.Elem().Kind() == reflect.Uint8 || containsByteSequence(value.Elem(), visited)
	case reflect.Map:
		return containsByteSequence(value.Key(), visited) || containsByteSequence(value.Elem(), visited)
	case reflect.Struct:
		for i := 0; i < value.NumField(); i++ {
			field := value.Field(i)
			if field.IsExported() && containsByteSequence(field.Type, visited) {
				return true
			}
		}
	}
	return false
}
