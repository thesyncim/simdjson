package simdjson

import (
	"bytes"
	"encoding/binary"
	"math/bits"
	"reflect"
	"strconv"
	"unsafe"
)

// Signed is the set of integer types accepted by DecoderCursor.Int.
type Signed interface {
	~int | ~int8 | ~int16 | ~int32 | ~int64
}

// Unsigned is the set of integer types accepted by DecoderCursor.Uint.
type Unsigned interface {
	~uint | ~uint8 | ~uint16 | ~uint32 | ~uint64 | ~uintptr
}

// Float is the set of floating-point types accepted by DecoderCursor.Float.
type Float interface {
	~float32 | ~float64
}

// StringLike is the set of string types accepted by DecoderCursor.String.
type StringLike interface {
	~string
}

// BoolLike is the set of boolean types accepted by DecoderCursor.Bool.
type BoolLike interface {
	~bool
}

type decoderFlags uint8

const (
	decoderZeroCopy decoderFlags = 1 << iota
	decoderDisallowUnknown
	decoderCaseSensitive
	decoderSourceOwned
)

// DecoderCursor is the concrete, interface-free parser surface used by generated
// decoders. Its generic scalar methods are specialized at each generated field.
type DecoderCursor struct {
	src      []byte
	i        int
	maxDepth int
	depth    int
	flags    decoderFlags
}

// NewDecoderCursor starts decoding src with opts.
func NewDecoderCursor(src []byte, opts TypedOptions) DecoderCursor {
	maxDepth := opts.MaxDepth
	if maxDepth <= 0 {
		maxDepth = defaultMaxDepth
	}
	var flags decoderFlags
	if opts.ZeroCopy {
		flags |= decoderZeroCopy
	}
	if opts.DisallowUnknownFields {
		flags |= decoderDisallowUnknown
	}
	if opts.CaseSensitive {
		flags |= decoderCaseSensitive
	}
	return DecoderCursor{
		src:      src,
		maxDepth: maxDepth,
		flags:    flags,
	}
}

// Finish verifies that exactly one complete JSON value was consumed.
func (c *DecoderCursor) Finish() error {
	if c.i == len(c.src) {
		return nil
	}
	return c.finishSlow()
}

//go:noinline
func (c *DecoderCursor) finishSlow() error {
	c.skipSpace()
	if c.i != len(c.src) {
		return c.err(c.i, "unexpected data after top-level value")
	}
	return nil
}

// TryNull consumes null and reports true, or leaves a non-null value untouched.
func (c *DecoderCursor) TryNull() (bool, error) {
	i := c.i
	if i < len(c.src) && c.src[i] > ' ' && c.src[i] != 'n' {
		return false, nil
	}
	return c.tryNullSlow()
}

//go:noinline
func (c *DecoderCursor) tryNullSlow() (bool, error) {
	c.skipSpace()
	if c.i >= len(c.src) || c.src[c.i] != 'n' {
		return false, nil
	}
	if !matchStringAt(c.src, c.i, "null") {
		return false, c.err(c.i, "invalid literal")
	}
	c.i += 4
	return true, nil
}

// BeginObject consumes an opening object delimiter.
func (c *DecoderCursor) BeginObject(typeName string) error {
	i := c.i
	if i < len(c.src) && c.src[i] == '{' && c.depth < c.maxDepth {
		c.depth++
		c.i = i + 1
		return nil
	}
	return c.beginObjectSlow(typeName)
}

//go:noinline
func (c *DecoderCursor) beginObjectSlow(typeName string) error {
	c.skipSpace()
	if c.i >= len(c.src) || c.src[c.i] != '{' {
		return c.expected(typeName, "object")
	}
	if c.depth >= c.maxDepth {
		return c.err(c.i, "maximum nesting depth exceeded")
	}
	c.depth++
	c.i++
	return nil
}

// NextObjectField returns the next decoded key. first must be true only for
// the first call after BeginObject.
func (c *DecoderCursor) NextObjectField(first bool) (key string, ok bool, err error) {
	i := c.i
	if i >= len(c.src) {
		return c.nextObjectFieldSlow(first)
	}
	if first {
		if c.src[i] == '}' {
			c.i = i + 1
			c.depth--
			return "", false, nil
		}
		if c.src[i] != '"' {
			return c.nextObjectFieldSlow(first)
		}
	} else {
		switch c.src[i] {
		case '}':
			c.i = i + 1
			c.depth--
			return "", false, nil
		case ',':
			i++
			if i >= len(c.src) || c.src[i] != '"' {
				return c.nextObjectFieldSlow(first)
			}
		default:
			return c.nextObjectFieldSlow(first)
		}
	}

	keyStart := i + 1
	keyEnd := keyStart
	if keyStart+8 <= len(c.src) {
		mask := stringSpecialMask(binary.LittleEndian.Uint64(c.src[keyStart:]))
		if mask == 0 {
			return c.nextObjectFieldSlow(first)
		}
		keyEnd += bits.TrailingZeros64(mask) / 8
	} else {
		keyEnd = scanStringSpecial(c.src, keyStart)
	}
	if keyEnd >= len(c.src) || c.src[keyEnd] != '"' || keyEnd+1 >= len(c.src) || c.src[keyEnd+1] != ':' ||
		(keyEnd+2 < len(c.src) && c.src[keyEnd+2] <= ' ') {
		return c.nextObjectFieldSlow(first)
	}
	key = unsafe.String(unsafe.SliceData(c.src[keyStart:keyEnd]), keyEnd-keyStart)
	c.i = keyEnd + 2
	return key, true, nil
}

//go:noinline
func (c *DecoderCursor) nextObjectFieldSlow(first bool) (key string, ok bool, err error) {
	c.skipSpace()
	if c.i >= len(c.src) {
		return "", false, c.err(c.i, "unterminated object")
	}
	if !first {
		switch c.src[c.i] {
		case '}':
			c.i++
			c.depth--
			return "", false, nil
		case ',':
			c.i++
		default:
			return "", false, c.err(c.i, "expected comma or closing brace in object")
		}
	} else if c.src[c.i] == '}' {
		c.i++
		c.depth--
		return "", false, nil
	}

	c.skipSpace()
	if c.i >= len(c.src) || c.src[c.i] != '"' {
		return "", false, c.err(c.i, "expected object key string")
	}
	keyStart := c.i + 1
	if keyStart+8 <= len(c.src) {
		mask := stringSpecialMask(binary.LittleEndian.Uint64(c.src[keyStart:]))
		keyEnd := keyStart + bits.TrailingZeros64(mask)/8
		if mask != 0 && c.src[keyEnd] == '"' {
			key = unsafe.String(unsafe.SliceData(c.src[keyStart:keyEnd]), keyEnd-keyStart)
			c.i = keyEnd + 1
		} else {
			key, err = c.typedKey()
		}
	} else {
		key, err = c.typedKey()
	}
	if err != nil {
		return "", false, err
	}
	c.skipSpace()
	if c.i >= len(c.src) || c.src[c.i] != ':' {
		return "", false, c.err(c.i, "expected colon after object key")
	}
	c.i++
	c.skipSpace()
	return key, true, nil
}

// BeginArray consumes an opening array delimiter.
func (c *DecoderCursor) BeginArray(typeName string) error {
	i := c.i
	if i < len(c.src) && c.src[i] == '[' && c.depth < c.maxDepth {
		c.depth++
		c.i = i + 1
		return nil
	}
	return c.beginArraySlow(typeName)
}

//go:noinline
func (c *DecoderCursor) beginArraySlow(typeName string) error {
	c.skipSpace()
	if c.i >= len(c.src) || c.src[c.i] != '[' {
		return c.expected(typeName, "array")
	}
	if c.depth >= c.maxDepth {
		return c.err(c.i, "maximum nesting depth exceeded")
	}
	c.depth++
	c.i++
	return nil
}

// NextArrayElement reports whether another value is available. first must be
// true only for the first call after BeginArray.
func (c *DecoderCursor) NextArrayElement(first bool) (bool, error) {
	i := c.i
	if i >= len(c.src) {
		return c.nextArrayElementSlow(first)
	}
	if first {
		if c.src[i] == ']' {
			c.i = i + 1
			c.depth--
			return false, nil
		}
		if c.src[i] > ' ' {
			return true, nil
		}
		return c.nextArrayElementSlow(first)
	}
	switch c.src[i] {
	case ']':
		c.i = i + 1
		c.depth--
		return false, nil
	case ',':
		if i+1 < len(c.src) && c.src[i+1] > ' ' {
			c.i = i + 1
			return true, nil
		}
	}
	return c.nextArrayElementSlow(first)
}

//go:noinline
func (c *DecoderCursor) nextArrayElementSlow(first bool) (bool, error) {
	c.skipSpace()
	if c.i >= len(c.src) {
		return false, c.err(c.i, "unterminated array")
	}
	if !first {
		switch c.src[c.i] {
		case ']':
			c.i++
			c.depth--
			return false, nil
		case ',':
			c.i++
		default:
			return false, c.err(c.i, "expected comma or closing bracket in array")
		}
	} else if c.src[c.i] == ']' {
		c.i++
		c.depth--
		return false, nil
	}
	c.skipSpace()
	return true, nil
}

// Skip validates and consumes the next value without materializing it.
func (c *DecoderCursor) Skip() error {
	p := c.slowParser()
	err := p.skipTypedValue(c.depth)
	c.i = p.i
	return err
}

// Unknown skips key unless unknown fields are disallowed.
func (c *DecoderCursor) Unknown(typeName, key string) error {
	if c.flags&decoderDisallowUnknown != 0 {
		return &TypedDecodeError{Offset: c.i, TypeName: typeName, Reason: "unknown field " + strconv.Quote(key)}
	}
	return c.Skip()
}

// CaseSensitive reports the generated field-matching mode.
func (c *DecoderCursor) CaseSensitive() bool {
	return c.flags&decoderCaseSensitive != 0
}

// ReserveSlice gives a generated slice a source-density capacity hint without
// reflection or interface conversion. Existing capacity is always retained.
func (c *DecoderCursor) ReserveSlice[T any](dst *[]T, bytesPerElement int) {
	if cap(*dst) != 0 {
		return
	}
	if bytesPerElement <= 0 {
		bytesPerElement = 16
	}
	capacity := (len(c.src)-c.i)/bytesPerElement + 1
	if capacity < 4 {
		capacity = 4
	} else if capacity > 1024 {
		capacity = 1024
	}
	*dst = make([]T, 0, capacity)
}

// Bool decodes directly into any defined boolean type.
func (c *DecoderCursor) Bool[T BoolLike](dst *T) error {
	i := c.i
	if i < len(c.src) {
		switch c.src[i] {
		case 't':
			if matchStringAt(c.src, i, "true") {
				*dst = true
				c.i = i + 4
				return nil
			}
		case 'f':
			if matchStringAt(c.src, i, "false") {
				*dst = false
				c.i = i + 5
				return nil
			}
		}
	}
	return c.boolSlow(dst)
}

//go:noinline
func (c *DecoderCursor) boolSlow[T BoolLike](dst *T) error {
	i := c.i
	if i >= len(c.src) {
		return c.genericExpected[T]("boolean")
	}
	if c.src[i] == 'n' {
		if !matchStringAt(c.src, i, "null") {
			return c.err(i, "invalid literal")
		}
		*dst = false
		c.i = i + 4
		return nil
	}
	switch c.src[i] {
	case 't':
		if !matchStringAt(c.src, i, "true") {
			return c.err(i, "invalid literal")
		}
		*dst = true
		c.i = i + 4
		return nil
	case 'f':
		if !matchStringAt(c.src, i, "false") {
			return c.err(i, "invalid literal")
		}
		*dst = false
		c.i = i + 5
		return nil
	default:
		return c.genericExpected[T]("boolean")
	}
}

// String decodes directly into any defined string type.
func (c *DecoderCursor) String[T StringLike](dst *T) error {
	i := c.i
	if i < len(c.src) && c.src[i] == '"' {
		start := i + 1
		end := scanStringSpecial(c.src, start)
		if end < len(c.src) && c.src[end] == '"' && c.flags&(decoderZeroCopy|decoderSourceOwned) != 0 {
			*dst = T(unsafe.String(unsafe.SliceData(c.src[start:end]), end-start))
			c.i = end + 1
			return nil
		}
	}
	return c.stringSlow(dst)
}

//go:noinline
func (c *DecoderCursor) stringSlow[T StringLike](dst *T) error {
	if c.i < len(c.src) && c.src[c.i] == 'n' {
		if !matchStringAt(c.src, c.i, "null") {
			return c.err(c.i, "invalid literal")
		}
		*dst = ""
		c.i += 4
		return nil
	}
	if c.i >= len(c.src) || c.src[c.i] != '"' {
		return c.genericExpected[T]("string")
	}
	start := c.i + 1
	end := scanStringSpecial(c.src, start)
	if end < len(c.src) && c.src[end] == '"' {
		c.ownSource()
		text := unsafe.String(unsafe.SliceData(c.src[start:end]), end-start)
		*dst = T(text)
		c.i = end + 1
		return nil
	}
	c.ownSource()
	p := c.slowParser()
	text, err := p.parseString()
	c.i = p.i
	if err != nil {
		return err
	}
	*dst = T(text)
	return nil
}

// Number decodes a JSON number's original spelling into a defined string type.
func (c *DecoderCursor) Number[T StringLike](dst *T) error {
	if c.i < len(c.src) && c.src[c.i] == 'n' {
		if !matchStringAt(c.src, c.i, "null") {
			return c.err(c.i, "invalid literal")
		}
		*dst = ""
		c.i += 4
		return nil
	}
	start, end, err := c.numberToken[T]()
	if err != nil {
		return err
	}
	c.ownSource()
	base := unsafe.Pointer(unsafe.SliceData(c.src))
	text := unsafe.String((*byte)(unsafe.Add(base, start)), end-start)
	*dst = T(text)
	c.i = end
	return nil
}

// Int decodes directly into any defined signed integer type.
func (c *DecoderCursor) Int[T Signed](dst *T) error {
	if c.i < len(c.src) && c.src[c.i] == 'n' {
		if !matchStringAt(c.src, c.i, "null") {
			return c.err(c.i, "invalid literal")
		}
		*dst = 0
		c.i += 4
		return nil
	}
	start := c.i
	if start >= len(c.src) {
		return c.genericExpected[T]("number")
	}
	i := start
	negative := false
	if c.src[i] == '-' {
		negative = true
		i++
		if i == len(c.src) {
			return c.err(start, "invalid number")
		}
	}
	if !isDigit(c.src[i]) {
		return c.genericExpected[T]("number")
	}
	bits := int(unsafe.Sizeof(*dst)) * 8
	limit := uint64(1) << (bits - 1)
	if !negative {
		limit--
	}
	value := uint64(0)
	if c.src[i] == '0' {
		i++
		if i < len(c.src) && isDigit(c.src[i]) {
			return c.err(start, "invalid leading zero in number")
		}
	} else {
		for i < len(c.src) && isDigit(c.src[i]) {
			digit := uint64(c.src[i] - '0')
			if value > (limit-digit)/10 {
				return c.genericError[T](start, "integer overflow")
			}
			value = value*10 + digit
			i++
		}
	}
	if i < len(c.src) && (c.src[i] == '.' || c.src[i] == 'e' || c.src[i] == 'E') {
		base := unsafe.Pointer(unsafe.SliceData(c.src))
		if _, ok := scanNumberFast(base, len(c.src), start); !ok {
			_, message := scanNumber(c.src, start)
			return c.err(start, message)
		}
		return c.genericError[T](start, "fractional number cannot be stored in an integer")
	}
	if negative {
		*dst = T(-int64(value))
	} else {
		*dst = T(value)
	}
	c.i = i
	return nil
}

// Uint decodes directly into any defined unsigned integer type.
func (c *DecoderCursor) Uint[T Unsigned](dst *T) error {
	if c.i < len(c.src) && c.src[c.i] == 'n' {
		if !matchStringAt(c.src, c.i, "null") {
			return c.err(c.i, "invalid literal")
		}
		*dst = 0
		c.i += 4
		return nil
	}
	start := c.i
	if start >= len(c.src) || !isDigit(c.src[start]) {
		return c.genericExpected[T]("number")
	}
	bits := int(unsafe.Sizeof(*dst)) * 8
	limit := ^uint64(0)
	if bits < 64 {
		limit = uint64(1)<<bits - 1
	}
	i := start
	value := uint64(0)
	if c.src[i] == '0' {
		i++
		if i < len(c.src) && isDigit(c.src[i]) {
			return c.err(start, "invalid leading zero in number")
		}
	} else {
		for i < len(c.src) && isDigit(c.src[i]) {
			digit := uint64(c.src[i] - '0')
			if value > (limit-digit)/10 {
				return c.genericError[T](start, "unsigned integer overflow")
			}
			value = value*10 + digit
			i++
		}
	}
	if i < len(c.src) && (c.src[i] == '.' || c.src[i] == 'e' || c.src[i] == 'E') {
		base := unsafe.Pointer(unsafe.SliceData(c.src))
		if _, ok := scanNumberFast(base, len(c.src), start); !ok {
			_, message := scanNumber(c.src, start)
			return c.err(start, message)
		}
		return c.genericError[T](start, "fractional number cannot be stored in an unsigned integer")
	}
	*dst = T(value)
	c.i = i
	return nil
}

// Float decodes directly into any defined float32 or float64 type.
func (c *DecoderCursor) Float[T Float](dst *T) error {
	i := c.i
	if i < len(c.src) && (c.src[i] == '-' || isDigit(c.src[i])) {
		if value, end, ok := shortTypedFloatAt(c.src, i); ok {
			*dst = T(value)
			c.i = end
			return nil
		}
	}
	return c.floatSlow(dst)
}

//go:noinline
func (c *DecoderCursor) floatSlow[T Float](dst *T) error {
	if c.i < len(c.src) && c.src[c.i] == 'n' {
		if !matchStringAt(c.src, c.i, "null") {
			return c.err(c.i, "invalid literal")
		}
		*dst = 0
		c.i += 4
		return nil
	}
	if c.i >= len(c.src) || (c.src[c.i] != '-' && !isDigit(c.src[c.i])) {
		return c.genericExpected[T]("number")
	}
	start := c.i
	base := unsafe.Pointer(unsafe.SliceData(c.src))
	end, integer, negative, isInteger, ok := scanAnyNumberFast(base, len(c.src), start)
	if !ok {
		_, message := scanNumber(c.src, start)
		return c.err(start, message)
	}
	text := unsafe.String((*byte)(unsafe.Add(base, start)), end-start)
	bits := int(unsafe.Sizeof(*dst)) * 8
	var value float64
	var err error
	if isInteger && integer <= uint64(1)<<53 {
		value = float64(integer)
		if negative {
			value = -value
		}
	} else if bits == 64 {
		if exact, ok := exactJSONFloat64(base, start, end); ok {
			value = exact
		} else {
			value, err = strconv.ParseFloat(text, 64)
		}
	} else {
		value, err = strconv.ParseFloat(text, 32)
	}
	if err != nil {
		return c.genericError[T](start, "number out of range")
	}
	*dst = T(value)
	c.i = end
	return nil
}

func shortTypedFloatAt(src []byte, start int) (value float64, end int, ok bool) {
	negative := false
	i := start
	if src[i] == '-' {
		negative = true
		i++
	}
	if i >= len(src) || !isDigit(src[i]) {
		return 0, start, false
	}
	value = float64(src[i] - '0')
	i++
	if typedNumberEnd(src, i) {
		if negative {
			value = -value
		}
		return value, i, true
	}
	if i >= len(src) {
		return 0, start, false
	}
	switch src[i] {
	case '.':
		i++
		if i >= len(src) || !isDigit(src[i]) {
			return 0, start, false
		}
		value += float64(src[i]-'0') / 10
		i++
	case 'e', 'E':
		i++
		exponentNegative := false
		if i < len(src) && (src[i] == '+' || src[i] == '-') {
			exponentNegative = src[i] == '-'
			i++
		}
		if i >= len(src) || !isDigit(src[i]) {
			return 0, start, false
		}
		exponent := int(src[i] - '0')
		if exponentNegative {
			value /= anyPow10[exponent]
		} else {
			value *= anyPow10[exponent]
		}
		i++
	default:
		return 0, start, false
	}
	if !typedNumberEnd(src, i) {
		return 0, start, false
	}
	if negative {
		value = -value
	}
	return value, i, true
}

func typedNumberEnd(src []byte, i int) bool {
	if i == len(src) {
		return true
	}
	switch src[i] {
	case ',', ']', '}', ' ', '\n', '\r', '\t':
		return true
	default:
		return false
	}
}

func (c *DecoderCursor) numberToken[T any]() (start, end int, err error) {
	if c.i >= len(c.src) || (c.src[c.i] != '-' && !isDigit(c.src[c.i])) {
		return c.i, c.i, c.genericExpected[T]("number")
	}
	start = c.i
	base := unsafe.Pointer(unsafe.SliceData(c.src))
	end, ok := scanNumberFast(base, len(c.src), start)
	if !ok {
		_, msg := scanNumber(c.src, start)
		return start, end, c.err(start, msg)
	}
	return start, end, nil
}

// ownSource lazily takes ownership of source-backed strings with one copy.
// Result strings keep the cloned backing array alive after the cursor returns.
func (c *DecoderCursor) ownSource() {
	if c.flags&(decoderZeroCopy|decoderSourceOwned) != 0 {
		return
	}
	c.src = bytes.Clone(c.src)
	c.flags |= decoderSourceOwned
}

func (c *DecoderCursor) skipSpace() {
	c.i = skipSpace(c.src, c.i)
}

func (c *DecoderCursor) err(offset int, reason string) error {
	return syntaxError(c.src, offset, reason)
}

func (c *DecoderCursor) slowParser() parser {
	return parser{src: c.src, i: c.i, maxDepth: c.maxDepth, zeroCopy: true}
}

func (c *DecoderCursor) typedKey() (string, error) {
	p := c.slowParser()
	key, err := p.typedKey()
	c.i = p.i
	return key, err
}

func (c *DecoderCursor) genericExpected[T any](jsonType string) error {
	return c.genericError[T](c.i, "expected "+jsonType)
}

func (c *DecoderCursor) genericError[T any](offset int, reason string) error {
	return &TypedDecodeError{Offset: offset, Type: reflect.TypeFor[T](), Reason: reason}
}

func (c *DecoderCursor) expected(typeName, jsonType string) error {
	return &TypedDecodeError{Offset: c.i, TypeName: typeName, Reason: "expected " + jsonType}
}
