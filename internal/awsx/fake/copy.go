package fake

import "reflect"

// deepCopy returns a copy of v that shares no memory with it, so values
// handed to callers cannot alias (or race with) later in-place estate edits.
func deepCopy[T any](v T) T {
	rv := reflect.ValueOf(&v).Elem()
	return copyValue(rv).Interface().(T)
}

func ptrTo[T any](v T) *T { return &v }

func copyValue(v reflect.Value) reflect.Value {
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() {
			return v
		}
		n := reflect.New(v.Type().Elem())
		n.Elem().Set(copyValue(v.Elem()))
		return n
	case reflect.Slice:
		if v.IsNil() {
			return v
		}
		n := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
		for i := 0; i < v.Len(); i++ {
			n.Index(i).Set(copyValue(v.Index(i)))
		}
		return n
	case reflect.Map:
		if v.IsNil() {
			return v
		}
		n := reflect.MakeMapWithSize(v.Type(), v.Len())
		for it := v.MapRange(); it.Next(); {
			n.SetMapIndex(it.Key(), copyValue(it.Value()))
		}
		return n
	case reflect.Interface:
		if v.IsNil() {
			return v
		}
		n := reflect.New(v.Type()).Elem()
		n.Set(copyValue(v.Elem()))
		return n
	case reflect.Struct:
		n := reflect.New(v.Type()).Elem()
		n.Set(v) // unexported fields (time.Time internals) are immutable values
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).IsExported() {
				n.Field(i).Set(copyValue(v.Field(i)))
			}
		}
		return n
	default:
		return v
	}
}
