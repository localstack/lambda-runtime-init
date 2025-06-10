package sandbox

import (
	"fmt"
	"reflect"
	"unsafe"

	"go.amzn.com/lambda/interop"
	"go.amzn.com/lambda/rapidcore"
)

// CreateSandboxContext creates a SandboxContext using reflection
// This is a dirty hack since the SandboxBuilder sets quite a few elements by default that cause issues
func CreateSandboxContext(rapidCtx interop.RapidContext, handler string, runtimeAPIAddress string) (interop.SandboxContext, error) {
	sandboxCtx := &rapidcore.SandboxContext{}
	v := reflect.ValueOf(sandboxCtx).Elem()

	setField := func(fieldName string, value interface{}) {
		field := v.FieldByName(fieldName)
		if !field.IsValid() {
			return
		}

		var settableField reflect.Value
		if field.CanSet() {
			settableField = field
		} else {

			settableField = reflect.NewAt(field.Type(), unsafe.Pointer(field.UnsafeAddr())).Elem()
		}

		settableField.Set(reflect.ValueOf(value))
	}

	setField("rapidCtx", rapidCtx)
	setField("handler", handler)
	setField("runtimeAPIAddress", runtimeAPIAddress)

	if sandboxCtx == nil || reflect.ValueOf(*sandboxCtx).IsZero() {
		return nil, fmt.Errorf("failed to dynamically create SandboxContext.")
	}

	return sandboxCtx, nil
}
