// SPDX-Licence-Identifier: EUPL-1.2

package ws

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func testEqual(want, got any) bool {
	return reflect.DeepEqual(want, got)
}

func testErrorIs(err, target error) bool {
	return errors.Is(err, target)
}

func testIsNil(value any) bool {
	if value == nil {
		return true
	}

	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}

func testIsEmpty(value any) bool {
	if value == nil {
		return true
	}

	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Array, reflect.Chan, reflect.Map, reflect.Slice, reflect.String:
		return v.Len() == 0
	case reflect.Bool:
		return !v.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return v.Int() == 0
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return v.Uint() == 0
	case reflect.Float32, reflect.Float64:
		return v.Float() == 0
	case reflect.Complex64, reflect.Complex128:
		return v.Complex() == 0
	case reflect.Interface, reflect.Pointer:
		return v.IsNil()
	default:
		return reflect.DeepEqual(value, reflect.Zero(v.Type()).Interface())
	}
}

func testContains(container, element any) bool {
	if s, ok := container.(string); ok {
		needle, ok := element.(string)
		return ok && strings.Contains(s, needle)
	}

	v := reflect.ValueOf(container)
	if !v.IsValid() {
		return false
	}

	switch v.Kind() {
	case reflect.Array, reflect.Slice:
		for i := range v.Len() {
			if reflect.DeepEqual(v.Index(i).Interface(), element) {
				return true
			}
		}
	case reflect.Map:
		key := reflect.ValueOf(element)
		if !key.IsValid() {
			return false
		}
		if key.Type().AssignableTo(v.Type().Key()) {
			return v.MapIndex(key).IsValid()
		}
		if key.Type().ConvertibleTo(v.Type().Key()) {
			return v.MapIndex(key.Convert(v.Type().Key())).IsValid()
		}
	}

	return false
}

func testSame(want, got any) bool {
	if testIsNil(want) || testIsNil(got) {
		return testIsNil(want) && testIsNil(got)
	}

	wantValue := reflect.ValueOf(want)
	gotValue := reflect.ValueOf(got)
	if wantValue.Type() != gotValue.Type() {
		return false
	}
	if wantValue.Kind() != reflect.Pointer {
		return false
	}
	return wantValue.Pointer() == gotValue.Pointer()
}

func testIsZero(value any) bool {
	if value == nil {
		return true
	}
	return reflect.ValueOf(value).IsZero()
}

func testEventually(condition func() bool, waitFor, tick time.Duration) bool {
	deadline := time.Now().Add(waitFor)
	for {
		if condition() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}

		sleepFor := tick
		if remaining := time.Until(deadline); remaining < sleepFor {
			sleepFor = remaining
		}
		if sleepFor > 0 {
			time.Sleep(sleepFor)
		}
	}
}

func testClose(t testing.TB, closeFn func() error) {
	t.Helper()
	_ = closeFn()
}

func testNotPanics(t *testing.T, f func()) {
	t.Helper()
	defer func() {
		if recovered := recover(); recovered != nil {
			t.Errorf("expected no panic, got %v", recovered)
		}
	}()
	f()
}
