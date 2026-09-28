// Package rpc implements the restricted EasyGo JSON-RPC profile over mutual TLS.
package rpc

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"unicode/utf8"
)

// ValidateJSON rejects duplicate keys (including inside opaque JSON), invalid
// UTF-8, excessive nesting and trailing values before any request can execute.
func ValidateJSON(raw []byte) error {
	if !utf8.Valid(raw) {
		return errors.New("invalid UTF-8")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var walk func(int) error
	walk = func(depth int) error {
		if depth > 64 {
			return errors.New("JSON nesting exceeds limit")
		}
		t, e := d.Token()
		if e != nil {
			return e
		}
		delim, ok := t.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				k, e := d.Token()
				if e != nil {
					return e
				}
				key, ok := k.(string)
				if !ok || seen[key] {
					return errors.New("duplicate JSON key")
				}
				seen[key] = true
				if e = walk(depth + 1); e != nil {
					return e
				}
			}
		case '[':
			for d.More() {
				if e := walk(depth + 1); e != nil {
					return e
				}
			}
		default:
			return errors.New("invalid JSON delimiter")
		}
		_, e = d.Token()
		return e
	}
	if e := walk(0); e != nil {
		return e
	}
	if _, e := d.Token(); e != io.EOF {
		return errors.New("trailing JSON")
	}
	return nil
}

var rawType = reflect.TypeOf(json.RawMessage{})

// Decode also rejects unknown or incorrectly cased fields and null scalars.
// RawMessage and map keys remain extensible, for provider state and JSON schemas.
func Decode(raw []byte, out any) error {
	if err := ValidateJSON(raw); err != nil {
		return err
	}
	typ := reflect.TypeOf(out)
	if typ == nil || typ.Kind() != reflect.Pointer {
		return errors.New("decode target must be a pointer")
	}
	if err := shape(raw, typ.Elem()); err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	return d.Decode(out)
}
func fields(t reflect.Type, out map[string]reflect.Type) {
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := strings.Split(f.Tag.Get("json"), ",")[0]
		if tag == "-" {
			continue
		}
		if f.Anonymous && tag == "" {
			ft := f.Type
			if ft.Kind() == reflect.Pointer {
				ft = ft.Elem()
			}
			if ft.Kind() == reflect.Struct {
				fields(ft, out)
				continue
			}
		}
		if f.PkgPath != "" {
			continue
		}
		if tag == "" {
			tag = f.Name
		}
		out[tag] = f.Type
	}
}
func shape(raw []byte, t reflect.Type) error {
	if t == rawType {
		return nil
	}
	if t.Kind() == reflect.Pointer {
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return nil
		}
		return shape(raw, t.Elem())
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		switch t.Kind() {
		case reflect.Slice, reflect.Map, reflect.Interface:
			return nil
		}
		return errors.New("null scalar or object")
	}
	switch t.Kind() {
	case reflect.Struct:
		var m map[string]json.RawMessage
		if json.Unmarshal(raw, &m) != nil {
			return errors.New("expected object")
		}
		fs := map[string]reflect.Type{}
		fields(t, fs)
		for k, v := range m {
			ft, ok := fs[k]
			if !ok {
				return errors.New("unknown field")
			}
			if err := shape(v, ft); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		var a []json.RawMessage
		if json.Unmarshal(raw, &a) != nil {
			return errors.New("expected array")
		}
		for _, v := range a {
			if err := shape(v, t.Elem()); err != nil {
				return err
			}
		}
	case reflect.Map:
		var m map[string]json.RawMessage
		if json.Unmarshal(raw, &m) != nil {
			return errors.New("expected object")
		}
		for _, v := range m {
			if err := shape(v, t.Elem()); err != nil {
				return err
			}
		}
	}
	return nil
}

func ReadConfig(r io.Reader, out any) error {
	raw, e := io.ReadAll(io.LimitReader(r, 8*1024*1024+1))
	if e != nil || len(raw) > 8*1024*1024 {
		return errors.New("configuration exceeds limit or cannot be read")
	}
	if e = Decode(raw, out); e != nil {
		return errors.New("invalid configuration JSON")
	}
	return nil
}
