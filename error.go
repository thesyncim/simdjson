package simdjson

import "fmt"

// Error describes a JSON syntax error with byte, line, and column positions.
type Error struct {
	Offset  int
	Line    int
	Column  int
	Message string
}

func (e *Error) Error() string {
	return fmt.Sprintf("json syntax error at byte %d, line %d, column %d: %s", e.Offset, e.Line, e.Column, e.Message)
}

func syntaxError(src []byte, off int, msg string) *Error {
	if off < 0 {
		off = 0
	}
	if off > len(src) {
		off = len(src)
	}
	line, col := 1, 1
	for i := 0; i < off; i++ {
		if src[i] == '\n' {
			line++
			col = 1
			continue
		}
		col++
	}
	return &Error{Offset: off, Line: line, Column: col, Message: msg}
}
