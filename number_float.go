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
	if n.exponent >= 0 {
		if n.exponent > 15 {
			return 0, false
		}
		for range n.exponent {
			if mantissa > (uint64(1)<<53)/10 {
				return 0, false
			}
			mantissa *= 10
		}
	} else {
		denominatorPower := -n.exponent
		if denominatorPower > 22 {
			return 0, false
		}
		for range denominatorPower {
			if mantissa%5 != 0 {
				return 0, false
			}
			mantissa /= 5
		}
		if mantissa > uint64(1)<<53 {
			return 0, false
		}
		value := math.Ldexp(float64(mantissa), -denominatorPower)
		if n.negative {
			value = -value
		}
		return value, true
	}
	if mantissa > uint64(1)<<53 {
		return 0, false
	}
	value := float64(mantissa)
	if n.negative {
		value = -value
	}
	return value, true
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
