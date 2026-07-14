package simd

import (
	"errors"
	"slices"
	"time"
)

var (
	errTimeYear = errors.New("Time.AppendText: year outside of range [0,9999]")
	errTimeZone = errors.New("Time.AppendText: timezone hour outside of range [0,23]")
)

// AppendTime appends value in the RFC 3339 form used by time.Time.AppendText.
// Its fixed-width date and clock digits use the selected SIMD digit kernel.
func AppendTime(dst []byte, value time.Time) ([]byte, error) {
	// One zone lookup feeds every calendar field. Calling Date, Clock, and
	// Zone instead repeats the location lookup and epoch shift three times.
	_, offset := value.Zone()
	abs := uint64(value.Unix() + int64(offset) + unixToAbsolute)
	year, month, day := absDaysToDate(abs / secondsPerDay)
	if uint(year) >= 10_000 {
		return dst, errTimeYear
	}
	clock := uint32(abs % secondsPerDay)
	hour := clock / 3600
	minute := clock / 60 % 60
	second := clock % 60
	zoneMinutes := offset / 60
	if zoneMinutes <= -24*60 || zoneMinutes >= 24*60 {
		return dst, errTimeZone
	}

	nanosecond := value.Nanosecond()
	fractionDigits := 0
	if nanosecond != 0 {
		fractionDigits = 9
		for nanosecond%10 == 0 {
			nanosecond /= 10
			fractionDigits--
		}
	}
	zoneBytes := 1
	if offset != 0 {
		zoneBytes = 6
	}
	fractionBytes := fractionDigits
	if fractionBytes != 0 {
		fractionBytes++
	}

	const dateTimeBytes = len(`"2006-01-02T15:04:05`)
	total := dateTimeBytes + fractionBytes + zoneBytes + 1
	start := len(dst)
	const maxTimeBytes = len(time.RFC3339Nano) + 2
	if cap(dst)-start < maxTimeBytes {
		dst = growTime(dst)
	}
	dst = dst[:start+maxTimeBytes]
	out := dst[start:]

	storeDateTimeParts((*[20]byte)(out), uint32(year), month, day, hour, minute, second)

	i := dateTimeBytes
	if fractionDigits != 0 {
		out[i] = '.'
		i++
		var fraction [8]byte
		if fractionDigits == 9 {
			out[i] = byte(nanosecond/100_000_000) + '0'
			Store8Digits(&fraction, uint64(nanosecond%100_000_000))
			copy(out[i+1:], fraction[:])
		} else {
			Store8Digits(&fraction, uint64(nanosecond))
			copy(out[i:], fraction[8-fractionDigits:])
		}
		i += fractionDigits
	}
	if offset == 0 {
		out[i] = 'Z'
		i++
	} else {
		if zoneMinutes < 0 {
			out[i] = '-'
			zoneMinutes = -zoneMinutes
		} else {
			out[i] = '+'
		}
		out[i+1] = byte(zoneMinutes/600) + '0'
		out[i+2] = byte(zoneMinutes/60%10) + '0'
		out[i+3] = ':'
		out[i+4] = byte(zoneMinutes/10%6) + '0'
		out[i+5] = byte(zoneMinutes%10) + '0'
		i += 6
	}
	out[i] = '"'
	return dst[:start+total], nil
}

//go:noinline
func growTime(dst []byte) []byte {
	return slices.Grow(dst, len(time.RFC3339Nano)+2)
}
