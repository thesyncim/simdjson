package gentest

import "encoding/json"

//go:generate go run ../../cmd/simdjsongen -type Document,Numeric -output model_simdjson.go

type Score float64

type Record struct {
	ID      int         `json:"id"`
	Active  bool        `json:"active"`
	Name    string      `json:"name"`
	Message string      `json:"message"`
	Scores  [3]Score    `json:"scores"`
	Number  json.Number `json:"number"`
	Ignored string      `json:"-"`
}

type Meta struct {
	Count  uint   `json:"count"`
	Source string `json:"source"`
}

type Document struct {
	Items    []Record `json:"items"`
	Meta     Meta     `json:"meta"`
	Optional *Meta    `json:"optional"`
	Fixed    [2]int   `json:"fixed"`
}

type Numeric struct {
	I8     int8        `json:"i8"`
	I16    int16       `json:"i16"`
	I32    int32       `json:"i32"`
	I64    int64       `json:"i64"`
	Int    int         `json:"int"`
	U8     uint8       `json:"u8"`
	U16    uint16      `json:"u16"`
	U32    uint32      `json:"u32"`
	U64    uint64      `json:"u64"`
	Uint   uint        `json:"uint"`
	F32    float32     `json:"f32"`
	F64    float64     `json:"f64"`
	Bool   bool        `json:"bool"`
	Text   string      `json:"text"`
	Number json.Number `json:"number"`
}
