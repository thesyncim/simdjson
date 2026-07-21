package query

import (
	"strconv"

	"github.com/thesyncim/simdjson/internal/byteview"
)

// A Result is the column-oriented output of a query: one ResultColumn per
// selected column, in Select order, each holding one Cell per result row.
// RowCount is the number of rows (the length every column shares). A pure
// projection yields one row per surviving document; a query with aggregates
// and no GROUP BY yields exactly one row; a GROUP BY yields one row per group.
//
// Cells that project a document value borrow the DocSet's storage, exactly
// like the RawValue they came from. RunInto may additionally place decoded
// escaped text and computed aggregate JSON in its Workspace. Copy Cell.JSON
// and, when needed, Cell.TextBytes before the DocSet, Result, or Workspace is
// reused if a cell must outlive that borrowing boundary.
type Result struct {
	Columns  []ResultColumn
	RowCount int
}

// A ResultColumn is one output column: its Header (the projection path or the
// aggregate spelling, e.g. "sum(price)") and its Cells, one per row.
type ResultColumn struct {
	Header string
	Cells  []Cell
}

// Column returns the result column with the given header, and whether one
// exists. Headers are unique within a Result.
func (r Result) Column(header string) (ResultColumn, bool) {
	for _, c := range r.Columns {
		if c.Header == header {
			return c, true
		}
	}
	return ResultColumn{}, false
}

// A CellKind is the JSON kind of a result Cell.
type CellKind uint8

const (
	// KindNull is a null or absent value.
	KindNull CellKind = iota
	KindBool
	KindNumber
	KindString
	// KindJSON is a container (array or object) held as raw JSON bytes.
	KindJSON
)

// A Cell is one value in a Result: a projected document value or a computed
// aggregate. Its typed accessors report false for the wrong kind, matching the
// core RawValue/Node accessors. JSON returns the exact bytes for a projected
// value and a formatted encoding for a computed one.
type Cell struct {
	kind  CellKind
	bval  bool
	fval  float64
	ival  int64
	isInt bool
	text  string
	raw   []byte
}

var (
	nullBytes  = []byte("null")
	trueBytes  = []byte("true")
	falseBytes = []byte("false")
)

// cellFromScalar builds a projection cell from a classified document value,
// preserving the value's exact source bytes.
func cellFromScalar(s scalar) Cell {
	switch s.kind {
	case kindNull:
		return Cell{kind: KindNull, raw: nullBytes}
	case kindBool:
		raw := falseBytes
		if s.bval {
			raw = trueBytes
		}
		return Cell{kind: KindBool, bval: s.bval, raw: raw}
	case kindNumber:
		f, _ := s.float64OfNumber()
		return Cell{kind: KindNumber, fval: f, isInt: s.isInt, ival: s.ival, raw: s.num}
	case kindString:
		return Cell{kind: KindString, text: s.sval, raw: s.raw}
	default:
		return Cell{kind: KindJSON, raw: s.raw}
	}
}

// floatCell builds a computed numeric cell (a SUM, AVG, MIN, or MAX result).
func floatCell(f float64) Cell {
	return Cell{kind: KindNumber, fval: f, raw: strconv.AppendFloat(nil, f, 'g', -1, 64)}
}

// countCell builds a COUNT result, an exact non-negative integer.
func countCell(n int) Cell {
	return Cell{
		kind:  KindNumber,
		fval:  float64(n),
		ival:  int64(n),
		isInt: true,
		raw:   strconv.AppendInt(nil, int64(n), 10),
	}
}

// nullCell builds a null result, the value of an aggregate over no rows and of
// an absent projection.
func nullCell() Cell {
	return Cell{kind: KindNull, raw: nullBytes}
}

// Kind returns the cell's JSON kind.
func (c Cell) Kind() CellKind { return c.kind }

// IsNull reports whether the cell is null or absent.
func (c Cell) IsNull() bool { return c.kind == KindNull }

// Bool returns the cell's boolean value, and false for a non-boolean cell.
func (c Cell) Bool() (bool, bool) {
	if c.kind != KindBool {
		return false, false
	}
	return c.bval, true
}

// Float64 returns the cell's numeric value as a float64, and false for a
// non-numeric cell.
func (c Cell) Float64() (float64, bool) {
	if c.kind != KindNumber {
		return 0, false
	}
	return c.fval, true
}

// Int64 returns the cell's numeric value as an int64 when it is an integer
// within range, and false otherwise.
func (c Cell) Int64() (int64, bool) {
	if c.kind == KindNumber && c.isInt {
		return c.ival, true
	}
	return 0, false
}

// Text returns the cell's decoded string content, and false for a non-string
// cell.
func (c Cell) Text() (string, bool) {
	if c.kind != KindString {
		return "", false
	}
	return c.text, true
}

// TextBytes returns decoded string content without allocating. The slice is a
// read-only borrowed view with the same lifetime as the Cell and must not be
// modified. For a non-string it returns nil and false.
func (c Cell) TextBytes() ([]byte, bool) {
	if c.kind != KindString {
		return nil, false
	}
	return byteview.Bytes(c.text), true
}

// JSON returns the cell as JSON bytes: the exact source bytes for a projected
// value, a formatted encoding for a computed aggregate. The slice must not be
// modified and, for a projected value, borrows the DocSet.
func (c Cell) JSON() []byte { return c.raw }

// String returns a compact debugging spelling of the cell.
func (c Cell) String() string { return string(c.raw) }
