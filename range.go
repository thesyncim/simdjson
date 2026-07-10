package simdjson

// EachArray validates src as a JSON array and calls fn for each raw element.
//
// Element RawValue slices alias src. If fn returns an error, iteration stops and
// that error is returned.
func EachArray(src []byte, fn func(index int, value RawValue) error) error {
	return EachArrayOptions(src, Options{}, fn)
}

// EachArrayOptions is EachArray with parser options.
func EachArrayOptions(src []byte, opts Options, fn func(index int, value RawValue) error) error {
	s := rawSeeker{src: src, maxDepth: opts.MaxDepth}
	if s.maxDepth <= 0 {
		s.maxDepth = defaultMaxDepth
	}
	s.skipSpace()
	if err := s.eachArray(1, fn); err != nil {
		return err
	}
	s.skipSpace()
	if s.i != len(src) {
		return syntaxError(src, s.i, "unexpected data after top-level array")
	}
	return nil
}

// EachArray validates r as a JSON array and calls fn for each raw element.
func (r RawValue) EachArray(fn func(index int, value RawValue) error) error {
	return EachArray(r.src, fn)
}

// EachObject validates src as a JSON object and calls fn for each raw member.
//
// Key strings alias src when the key is unescaped. Escaped keys allocate only
// for the unescaped key string. Value RawValue slices alias src.
func EachObject(src []byte, fn func(key string, value RawValue) error) error {
	return EachObjectOptions(src, Options{}, fn)
}

// EachObjectOptions is EachObject with parser options.
func EachObjectOptions(src []byte, opts Options, fn func(key string, value RawValue) error) error {
	s := rawSeeker{src: src, maxDepth: opts.MaxDepth}
	if s.maxDepth <= 0 {
		s.maxDepth = defaultMaxDepth
	}
	s.skipSpace()
	if err := s.eachObject(1, fn); err != nil {
		return err
	}
	s.skipSpace()
	if s.i != len(src) {
		return syntaxError(src, s.i, "unexpected data after top-level object")
	}
	return nil
}

// EachObject validates r as a JSON object and calls fn for each raw member.
func (r RawValue) EachObject(fn func(key string, value RawValue) error) error {
	return EachObject(r.src, fn)
}

func (s *rawSeeker) eachArray(depth int, fn func(index int, value RawValue) error) error {
	if depth > s.maxDepth {
		return syntaxError(s.src, s.i, "maximum nesting depth exceeded")
	}
	if s.i >= len(s.src) || s.src[s.i] != '[' {
		return syntaxError(s.src, s.i, "expected array")
	}
	s.i++
	s.skipSpace()
	if s.i < len(s.src) && s.src[s.i] == ']' {
		s.i++
		return nil
	}

	for index := 0; ; index++ {
		s.skipSpace()
		start := s.i
		if err := s.skipValue(depth); err != nil {
			return err
		}
		if fn != nil {
			if err := fn(index, RawValue{src: s.src[start:s.i]}); err != nil {
				return err
			}
		}
		s.skipSpace()
		if s.i >= len(s.src) {
			return syntaxError(s.src, s.i, "unterminated array")
		}
		switch s.src[s.i] {
		case ',':
			s.i++
		case ']':
			s.i++
			return nil
		default:
			return syntaxError(s.src, s.i, "expected comma or closing bracket in array")
		}
	}
}

func (s *rawSeeker) eachObject(depth int, fn func(key string, value RawValue) error) error {
	if depth > s.maxDepth {
		return syntaxError(s.src, s.i, "maximum nesting depth exceeded")
	}
	if s.i >= len(s.src) || s.src[s.i] != '{' {
		return syntaxError(s.src, s.i, "expected object")
	}
	s.i++
	s.skipSpace()
	if s.i < len(s.src) && s.src[s.i] == '}' {
		s.i++
		return nil
	}

	for {
		s.skipSpace()
		if s.i >= len(s.src) || s.src[s.i] != '"' {
			return syntaxError(s.src, s.i, "expected object key string")
		}
		keyStart, keyEnd, escaped, err := s.parseStringRaw()
		if err != nil {
			return err
		}
		key, err := s.keyString(keyStart, keyEnd, escaped)
		if err != nil {
			return err
		}
		s.skipSpace()
		if s.i >= len(s.src) || s.src[s.i] != ':' {
			return syntaxError(s.src, s.i, "expected colon after object key")
		}
		s.i++
		s.skipSpace()
		valueStart := s.i
		if err := s.skipValue(depth); err != nil {
			return err
		}
		if fn != nil {
			if err := fn(key, RawValue{src: s.src[valueStart:s.i]}); err != nil {
				return err
			}
		}
		s.skipSpace()
		if s.i >= len(s.src) {
			return syntaxError(s.src, s.i, "unterminated object")
		}
		switch s.src[s.i] {
		case ',':
			s.i++
		case '}':
			s.i++
			return nil
		default:
			return syntaxError(s.src, s.i, "expected comma or closing brace in object")
		}
	}
}

func (s *rawSeeker) keyString(start, end int, escaped bool) (string, error) {
	if !escaped {
		return ownedBytesString(s.src[start:end]), nil
	}
	p := parser{src: s.src, i: start - 1, maxDepth: s.maxDepth, zeroCopy: true}
	return p.parseString()
}
