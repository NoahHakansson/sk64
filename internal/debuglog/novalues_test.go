package debuglog

import (
	"reflect"
	"testing"
)

func TestDebugLogAPINeverAcceptsValueBytes(t *testing.T) {
	allowed := map[reflect.Type]bool{
		reflect.TypeFor[string]():     true,
		reflect.TypeFor[int]():        true,
		reflect.TypeFor[ErrorClass](): true,
	}
	loggerTypes := []reflect.Type{reflect.TypeFor[Logger](), reflect.TypeFor[*Logger]()}
	for _, loggerType := range loggerTypes {
		for methodIndex := 0; methodIndex < loggerType.NumMethod(); methodIndex++ {
			method := loggerType.Method(methodIndex)
			for parameterIndex := 1; parameterIndex < method.Type.NumIn(); parameterIndex++ {
				parameter := method.Type.In(parameterIndex)
				if !allowed[parameter] {
					t.Errorf("%s parameter %d has disallowed type %v", method.Name, parameterIndex, parameter)
				}
			}
		}
	}
	if allowed[reflect.TypeFor[[]byte]()] {
		t.Fatal("parameter whitelist accepted []byte sanity-check type")
	}
	if allowed[reflect.TypeFor[error]()] {
		t.Fatal("parameter whitelist accepted error")
	}
	errorClassType := reflect.TypeFor[ErrorClass]()
	for fieldIndex := 0; fieldIndex < errorClassType.NumField(); fieldIndex++ {
		if errorClassType.Field(fieldIndex).IsExported() {
			t.Fatalf("ErrorClass field %s is exported", errorClassType.Field(fieldIndex).Name)
		}
	}
}
