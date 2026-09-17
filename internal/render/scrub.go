package render

import (
	"fmt"
	"reflect"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// A report is data read from a cluster, a state file or a model, and any of it can carry
// bytes a terminal would obey: an escape sequence in an annotation, in a field manager name
// or in a narrator's answer would colour, move or clear the screen of whoever reads the
// output, and the promise that Color false writes no escape sequence would be broken by the
// data rather than by the renderer. Every renderer therefore works on a copy of its report
// in which each control character has been made visible, and each timestamp carries UTC so
// the copy formats the same on every machine. The report handed in is never modified.

var timeType = reflect.TypeOf(time.Time{})

// scrub deep copies v, pinning every time to UTC and, when text is set, rewriting every
// string with visible. JSON keeps the strings as they are, since the encoder escapes them.
func scrub(v any, text bool) any {
	if v == nil {
		return nil
	}
	return scrubValue(reflect.ValueOf(v), text).Interface()
}

func scrubValue(v reflect.Value, text bool) reflect.Value {
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() {
			return v
		}
		out := reflect.New(v.Type().Elem())
		out.Elem().Set(scrubValue(v.Elem(), text))
		return out
	case reflect.Interface:
		if v.IsNil() {
			return v
		}
		out := reflect.New(v.Type()).Elem()
		out.Set(scrubValue(v.Elem(), text))
		return out
	case reflect.Struct:
		if v.Type() == timeType {
			if !v.CanInterface() {
				return v
			}
			return reflect.ValueOf(v.Interface().(time.Time).UTC())
		}
		out := reflect.New(v.Type()).Elem()
		out.Set(v)
		for i := 0; i < v.NumField(); i++ {
			if f := out.Field(i); f.CanSet() {
				f.Set(scrubValue(v.Field(i), text))
			}
		}
		return out
	case reflect.Slice:
		if v.IsNil() {
			return v
		}
		out := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
		for i := 0; i < v.Len(); i++ {
			out.Index(i).Set(scrubValue(v.Index(i), text))
		}
		return out
	case reflect.Array:
		out := reflect.New(v.Type()).Elem()
		for i := 0; i < v.Len(); i++ {
			out.Index(i).Set(scrubValue(v.Index(i), text))
		}
		return out
	case reflect.Map:
		if v.IsNil() {
			return v
		}
		out := reflect.MakeMapWithSize(v.Type(), v.Len())
		for it := v.MapRange(); it.Next(); {
			out.SetMapIndex(it.Key(), scrubValue(it.Value(), text))
		}
		return out
	case reflect.String:
		if !text {
			return v
		}
		out := reflect.New(v.Type()).Elem()
		out.SetString(visible(v.String()))
		return out
	default:
		return v
	}
}

// visible spells every control character out as its escaped form, keeps newlines, turns a
// tab into a space so columns stay aligned, and repairs invalid UTF-8. What was in the data
// is shown, never obeyed, and never dropped without a trace.
func visible(s string) string {
	if utf8.ValidString(s) && !strings.ContainsFunc(s, isControl) {
		return s
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\t':
			b.WriteByte(' ')
		case isControl(r):
			if r < 0x100 {
				fmt.Fprintf(&b, `\x%02x`, r)
			} else {
				fmt.Fprintf(&b, `\u%04x`, r)
			}
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func isControl(r rune) bool {
	return r != '\n' && (unicode.IsControl(r) || r == utf8.RuneError)
}
