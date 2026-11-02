package api

import (
	"fmt"
	"reflect"
	"testing"
)

// nilSlicePaths lists every nil slice reachable from v; a nil slice marshals to JSON null and breaks the SPA.
func nilSlicePaths(v reflect.Value, path string) []string {
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() {
			return nil
		}
		return nilSlicePaths(v.Elem(), path)
	case reflect.Struct:
		var out []string
		for i := 0; i < v.NumField(); i++ {
			out = append(out, nilSlicePaths(v.Field(i), path+"."+v.Type().Field(i).Name)...)
		}
		return out
	case reflect.Slice:
		if v.IsNil() {
			return []string{path}
		}
		var out []string
		for i := 0; i < v.Len(); i++ {
			out = append(out, nilSlicePaths(v.Index(i), fmt.Sprintf("%s[%d]", path, i))...)
		}
		return out
	}
	return nil
}

func TestAnalyticsDTO_SlicesNeverNil(t *testing.T) {
	dtos := map[string]any{
		"delivery":    newDeliveryDTO(),
		"reliability": newReliabilityDTO(),
		"capacity":    newCapacityDTO(),
		"operations":  newOperationsDTO(),
	}
	for name, dto := range dtos {
		for _, path := range nilSlicePaths(reflect.ValueOf(dto), name) {
			t.Errorf("%s is a nil slice (serialises as null)", path)
		}
	}
}
