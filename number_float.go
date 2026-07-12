package simdjson

import (
	"math"
	"strconv"
	"unsafe"
)

const maxJSONMantissaDigits = 19

type jsonNumber struct {
	mantissa uint64
	exponent int
	negative bool

	truncated bool
	nd        int
	ndMant    int
	dp        int
}

// ParseFloat64 parses one strict JSON number with optional surrounding JSON
// whitespace. Successful calls do not allocate.
func ParseFloat64(src []byte) (float64, error) {
	start := skipSpace(src, 0)
	if start == len(src) {
		return 0, (&parser{src: src}).err(start, "expected number")
	}
	base := unsafe.Pointer(unsafe.SliceData(src))
	end, number, ok := scanJSONNumber(base, len(src), start)
	if !ok {
		_, msg := scanNumber(src, start)
		return 0, (&parser{src: src}).err(start, msg)
	}
	if trailing := skipSpace(src, end); trailing != len(src) {
		return 0, (&parser{src: src}).err(trailing, "unexpected trailing data")
	}
	if value, exact := number.exactFloat64(); exact {
		return value, nil
	}
	text := unsafe.String((*byte)(unsafe.Add(base, start)), end-start)
	value, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return 0, (&parser{src: src}).err(start, "number out of range")
	}
	return value, nil
}

func scanJSONNumber(base unsafe.Pointer, n, start int) (int, jsonNumber, bool) {
	i := start
	var number jsonNumber
	if fastByteAt(base, i) == '-' {
		number.negative = true
		i++
		if i >= n {
			return i, number, false
		}
	}

	switch c := fastByteAt(base, i); {
	case c == '0':
		number.addDigit(c)
		i++
	case isOneNine(c):
		if i <= n-16 && all16Digits(unsafe.Add(base, i)) {
			number.add16Digits(parse16Digits(unsafe.Add(base, i)))
			i += 16
		}
		for i < n && isDigit(fastByteAt(base, i)) {
			if number.nd != 0 && i <= n-16 && all16Digits(unsafe.Add(base, i)) {
				number.add16Digits(parse16Digits(unsafe.Add(base, i)))
				i += 16
				continue
			}
			number.addDigit(fastByteAt(base, i))
			i++
		}
	default:
		return i, number, false
	}

	if i < n && fastByteAt(base, i) == '.' {
		number.dp = number.nd
		i++
		if i >= n || !isDigit(fastByteAt(base, i)) {
			return i, number, false
		}
		for i < n && isDigit(fastByteAt(base, i)) {
			if number.nd != 0 && i <= n-16 && all16Digits(unsafe.Add(base, i)) {
				number.add16Digits(parse16Digits(unsafe.Add(base, i)))
				i += 16
				continue
			}
			number.addDigit(fastByteAt(base, i))
			i++
		}
	} else {
		number.dp = number.nd
	}

	if i < n && (fastByteAt(base, i) == 'e' || fastByteAt(base, i) == 'E') {
		i++
		exponentSign := 1
		if i < n && (fastByteAt(base, i) == '+' || fastByteAt(base, i) == '-') {
			if fastByteAt(base, i) == '-' {
				exponentSign = -1
			}
			i++
		}
		if i >= n || !isDigit(fastByteAt(base, i)) {
			return i, number, false
		}
		exponent := 0
		for i < n && isDigit(fastByteAt(base, i)) {
			if exponent < 10000 {
				exponent = exponent*10 + int(fastByteAt(base, i)-'0')
			}
			i++
		}
		number.dp += exponent * exponentSign
	}
	if number.mantissa != 0 {
		number.exponent = number.dp - number.ndMant
	}
	return i, number, true
}

func (n *jsonNumber) addDigit(c byte) {
	if c == '0' && n.nd == 0 {
		n.dp--
		return
	}
	n.nd++
	if n.ndMant < maxJSONMantissaDigits {
		n.mantissa = n.mantissa*10 + uint64(c-'0')
		n.ndMant++
	} else if c != '0' {
		n.truncated = true
	}
}

func (n *jsonNumber) add16Digits(digits uint64) {
	n.nd += 16
	remaining := maxJSONMantissaDigits - n.ndMant
	if remaining >= 16 {
		n.mantissa = n.mantissa*1e16 + digits
		n.ndMant += 16
		return
	}
	if remaining <= 0 {
		n.truncated = n.truncated || digits != 0
		return
	}
	divisor := pow10Uint64[16-remaining]
	n.mantissa = n.mantissa*pow10Uint64[remaining] + digits/divisor
	n.ndMant += remaining
	n.truncated = n.truncated || digits%divisor != 0
}

func (n jsonNumber) exactFloat64() (float64, bool) {
	if n.truncated {
		return 0, false
	}
	mantissa := n.mantissa
	if mantissa == 0 {
		if n.negative {
			return math.Copysign(0, -1), true
		}
		return 0, true
	}
	// This is the same exact-rounding envelope used by strconv: a mantissa
	// below 2^52 combined with a power of ten no larger than 1e22 can be
	// converted with one floating-point multiply or divide. Moving up to 15
	// decimal zeros into the mantissa extends the positive exponent range.
	if mantissa >= uint64(1)<<52 {
		if n.exponent < 0 {
			return scaleJSONFloat64(mantissa, n.exponent, n.negative)
		}
		return 0, false
	}
	value := float64(mantissa)
	if n.negative {
		value = -value
	}
	switch {
	case n.exponent == 0:
		return value, true
	case n.exponent > 0 && n.exponent <= 37:
		exponent := n.exponent
		if exponent > 22 {
			value *= anyPow10[exponent-22]
			exponent = 22
		}
		if value > 1e15 || value < -1e15 {
			return 0, false
		}
		return value * anyPow10[exponent], true
	case n.exponent < 0 && n.exponent >= -22:
		return value / anyPow10[-n.exponent], true
	default:
		return 0, false
	}
}

func exactJSONFloat64(base unsafe.Pointer, start, end int) (float64, bool) {
	i := start
	negative := false
	if fastByteAt(base, i) == '-' {
		negative = true
		i++
	}

	var mantissa uint64
	fractionDigits := 0
	if end-i == 16 && all16Digits(unsafe.Add(base, i)) {
		mantissa = parse16Digits(unsafe.Add(base, i))
		i = end
	}
	for i < end {
		c := fastByteAt(base, i)
		if c == '.' || c == 'e' || c == 'E' {
			break
		}
		if mantissa > (math.MaxUint64-uint64(c-'0'))/10 {
			return 0, false
		}
		mantissa = mantissa*10 + uint64(c-'0')
		i++
	}
	if i < end && fastByteAt(base, i) == '.' {
		i++
		for i < end {
			c := fastByteAt(base, i)
			if c == 'e' || c == 'E' {
				break
			}
			if mantissa > (math.MaxUint64-uint64(c-'0'))/10 {
				return 0, false
			}
			mantissa = mantissa*10 + uint64(c-'0')
			fractionDigits++
			i++
		}
	}

	exponent := 0
	if i < end {
		i++
		exponentNegative := false
		if i < end && (fastByteAt(base, i) == '+' || fastByteAt(base, i) == '-') {
			exponentNegative = fastByteAt(base, i) == '-'
			i++
		}
		for i < end {
			if exponent > 1000 {
				return 0, false
			}
			exponent = exponent*10 + int(fastByteAt(base, i)-'0')
			i++
		}
		if exponentNegative {
			exponent = -exponent
		}
	}
	return (jsonNumber{
		mantissa: mantissa,
		exponent: exponent - fractionDigits,
		negative: negative,
	}).exactFloat64()
}

func scanTypedFloat64(base unsafe.Pointer, n, start int) (end int, value float64, exact, ok bool) {
	i := start
	if fastByteAt(base, i) == '-' {
		i++
	}
	if i <= n-18 && fastByteAt(base, i) == '0' && fastByteAt(base, i+1) == '.' &&
		all16Digits(unsafe.Add(base, i+2)) {
		return scanTypedLeadingZeroFloat64(base, n, start)
	}
	if start <= n-8 {
		word := loadUint64LE(unsafe.Add(base, start))
		if byteEqMask(word, ',')|byteEqMask(word, ']')|byteEqMask(word, '}') != 0 {
			end, number, ok := scanJSONNumber(base, n, start)
			if !ok {
				return end, 0, false, false
			}
			value, exact = number.exactFloat64()
			return end, value, exact, true
		}
	} else {
		end, number, ok := scanJSONNumber(base, n, start)
		if !ok {
			return end, 0, false, false
		}
		value, exact = number.exactFloat64()
		return end, value, exact, true
	}
	return scanTypedSimpleFloat64(base, n, start)
}

func scanTypedSimpleFloat64(base unsafe.Pointer, n, start int) (end int, value float64, exact, ok bool) {
	i := start
	negative := false
	if fastByteAt(base, i) == '-' {
		negative = true
		i++
		if i >= n {
			return i, 0, false, false
		}
	}

	var mantissa uint64
	digits := 0
	if fastByteAt(base, i) == '0' {
		digits = 1
		i++
		if i < n && isDigit(fastByteAt(base, i)) {
			return i, 0, false, false
		}
	} else if isOneNine(fastByteAt(base, i)) {
		for i < n && isDigit(fastByteAt(base, i)) {
			digits++
			if digits <= 18 {
				mantissa = mantissa*10 + uint64(fastByteAt(base, i)-'0')
			}
			i++
		}
	} else {
		return i, 0, false, false
	}

	fractionDigits := 0
	if i < n && fastByteAt(base, i) == '.' {
		i++
		if i >= n || !isDigit(fastByteAt(base, i)) {
			return i, 0, false, false
		}
		for i < n && isDigit(fastByteAt(base, i)) {
			digits++
			fractionDigits++
			if digits <= 18 {
				mantissa = mantissa*10 + uint64(fastByteAt(base, i)-'0')
			}
			i++
		}
	}

	exponent := 0
	if i < n && (fastByteAt(base, i) == 'e' || fastByteAt(base, i) == 'E') {
		i++
		exponentNegative := false
		if i < n && (fastByteAt(base, i) == '+' || fastByteAt(base, i) == '-') {
			exponentNegative = fastByteAt(base, i) == '-'
			i++
		}
		if i >= n || !isDigit(fastByteAt(base, i)) {
			return i, 0, false, false
		}
		for i < n && isDigit(fastByteAt(base, i)) {
			if exponent <= 1000 {
				exponent = exponent*10 + int(fastByteAt(base, i)-'0')
			}
			i++
		}
		if exponentNegative {
			exponent = -exponent
		}
	}

	if digits <= 18 {
		decimalExponent := exponent - fractionDigits
		if mantissa >= uint64(1)<<52 && decimalExponent >= -22 && decimalExponent < 0 {
			value, exact = scaleJSONFloat64(mantissa, decimalExponent, negative)
		} else {
			value, exact = (jsonNumber{
				mantissa: mantissa,
				exponent: decimalExponent,
				negative: negative,
			}).exactFloat64()
		}
	}
	return i, value, exact, true
}

func scanTypedLeadingZeroFloat64(base unsafe.Pointer, n, start int) (end int, value float64, exact, ok bool) {
	i := start
	negative := false
	if fastByteAt(base, i) == '-' {
		negative = true
		i++
	}
	// The dispatcher proved that this is 0. followed by at least 16 digits.
	i += 2
	dp := 0
	for i < n && fastByteAt(base, i) == '0' {
		dp--
		i++
	}
	var mantissa uint64
	ndMant := 0
	truncated := false
	if i <= n-16 && isOneNine(fastByteAt(base, i)) && all16Digits(unsafe.Add(base, i)) {
		mantissa = parse16Digits(unsafe.Add(base, i))
		ndMant = 16
		i += 16
	}
	for i < n && isDigit(fastByteAt(base, i)) {
		c := fastByteAt(base, i)
		if ndMant < maxJSONMantissaDigits {
			mantissa = mantissa*10 + uint64(c-'0')
			ndMant++
		} else if c != '0' {
			truncated = true
		}
		i++
	}

	if i < n && (fastByteAt(base, i) == 'e' || fastByteAt(base, i) == 'E') {
		i++
		exponentSign := 1
		if i < n && (fastByteAt(base, i) == '+' || fastByteAt(base, i) == '-') {
			if fastByteAt(base, i) == '-' {
				exponentSign = -1
			}
			i++
		}
		if i >= n || !isDigit(fastByteAt(base, i)) {
			return i, 0, false, false
		}
		exponent := 0
		for i < n && isDigit(fastByteAt(base, i)) {
			if exponent < 10000 {
				exponent = exponent*10 + int(fastByteAt(base, i)-'0')
			}
			i++
		}
		dp += exponent * exponentSign
	}

	number := jsonNumber{
		mantissa:  mantissa,
		exponent:  dp - ndMant,
		negative:  negative,
		truncated: truncated,
	}
	value, exact = number.exactFloat64()
	return i, value, exact, true
}

var pow10Uint64 = [...]uint64{
	1,
	10,
	100,
	1000,
	10000,
	100000,
	1000000,
	10000000,
	100000000,
	1000000000,
	10000000000,
	100000000000,
	1000000000000,
	10000000000000,
	100000000000000,
	1000000000000000,
	10000000000000000,
	100000000000000000,
	1000000000000000000,
	10000000000000000000,
}
