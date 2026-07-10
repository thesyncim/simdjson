package simdjson

import (
	"encoding/json"
	"strconv"
)

// Kind is the JSON type stored in a Value.
type Kind uint8

const (
	Invalid Kind = iota
	Null
	Bool
	Number
	String
	Array
	Object
)

func (k Kind) String() string {
	switch k {
	case Null:
		return "null"
	case Bool:
		return "bool"
	case Number:
		return "number"
	case String:
		return "string"
	case Array:
		return "array"
	case Object:
		return "object"
	default:
		return "invalid"
	}
}

// Member is one ordered object entry.
type Member struct {
	Key   string
	Value Value
}

// Value is an immutable JSON AST node.
type Value struct {
	kind Kind
	b    bool
	s    string
	n    string
	a    []Value
	o    []Member
}

// Kind returns the JSON kind of v.
func (v Value) Kind() Kind {
	return v.kind
}

// Bool returns v as a bool.
func (v Value) Bool() (bool, bool) {
	if v.kind != Bool {
		return false, false
	}
	return v.b, true
}

// Text returns v as a string.
func (v Value) Text() (string, bool) {
	if v.kind != String {
		return "", false
	}
	return v.s, true
}

// NumberText returns the original JSON number spelling.
func (v Value) NumberText() (string, bool) {
	if v.kind != Number {
		return "", false
	}
	return v.n, true
}

// Float64 parses a number value as float64.
func (v Value) Float64() (float64, bool) {
	if v.kind != Number {
		return 0, false
	}
	f, err := strconv.ParseFloat(v.n, 64)
	return f, err == nil
}

// Int64 parses an integer number value as int64.
func (v Value) Int64() (int64, bool) {
	if v.kind != Number {
		return 0, false
	}
	i, err := strconv.ParseInt(v.n, 10, 64)
	return i, err == nil
}

// Array returns v as an array.
func (v Value) Array() ([]Value, bool) {
	if v.kind != Array {
		return nil, false
	}
	return v.a, true
}

// Object returns v as ordered object members.
func (v Value) Object() ([]Member, bool) {
	if v.kind != Object {
		return nil, false
	}
	return v.o, true
}

// Get returns the last object member with key.
func (v Value) Get(key string) (Value, bool) {
	if v.kind != Object {
		return Value{}, false
	}
	for i := len(v.o) - 1; i >= 0; i-- {
		if v.o[i].Key == key {
			return v.o[i].Value, true
		}
	}
	return Value{}, false
}

// Index returns the ith array element.
func (v Value) Index(i int) (Value, bool) {
	if v.kind != Array || i < 0 || i >= len(v.a) {
		return Value{}, false
	}
	return v.a[i], true
}

// Any converts v to standard Go JSON shapes. Numbers are json.Number.
func (v Value) Any() any {
	switch v.kind {
	case Null:
		return nil
	case Bool:
		return v.b
	case Number:
		return json.Number(v.n)
	case String:
		return v.s
	case Array:
		out := make([]any, len(v.a))
		for i := range v.a {
			out[i] = v.a[i].Any()
		}
		return out
	case Object:
		out := make(map[string]any, len(v.o))
		for _, m := range v.o {
			out[m.Key] = m.Value.Any()
		}
		return out
	default:
		return nil
	}
}

// String returns compact JSON for v.
func (v Value) String() string {
	b, _ := v.MarshalJSON()
	return string(b)
}
