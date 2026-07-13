package simdjson

type compactParser struct {
	src      []byte
	dst      []byte
	i        int
	maxDepth int
}

func appendCompact(dst, src []byte, maxDepth int) ([]byte, error) {
	switch len(src) {
	case 4:
		if src[0] == 'n' && src[1] == 'u' && src[2] == 'l' && src[3] == 'l' ||
			src[0] == 't' && src[1] == 'r' && src[2] == 'u' && src[3] == 'e' {
			return append(dst, src...), nil
		}
	case 5:
		if src[0] == 'f' && src[1] == 'a' && src[2] == 'l' && src[3] == 's' && src[4] == 'e' {
			return append(dst, src...), nil
		}
	}
	if maxDepth <= 0 {
		maxDepth = defaultMaxDepth
	}
	startLen := len(dst)
	c := compactParser{src: src, dst: dst, maxDepth: maxDepth}
	c.skipSpace()
	if err := c.value(0); err != nil {
		return dst[:startLen], err
	}
	c.skipSpace()
	if c.i != len(src) {
		return dst[:startLen], syntaxError(src, c.i, "unexpected data after top-level value")
	}
	return c.dst, nil
}

func (c *compactParser) skipSpace() {
	c.i = skipSpace(c.src, c.i)
}

func (c *compactParser) value(depth int) error {
	if depth > c.maxDepth {
		return syntaxError(c.src, c.i, "maximum nesting depth exceeded")
	}
	if c.i >= len(c.src) {
		return syntaxError(c.src, c.i, "expected value")
	}
	switch c.src[c.i] {
	case 'n':
		return c.literal("null")
	case 't':
		return c.literal("true")
	case 'f':
		return c.literal("false")
	case '"':
		return c.string()
	case '[':
		return c.array(depth + 1)
	case '{':
		return c.object(depth + 1)
	default:
		if c.src[c.i] == '-' || isDigit(c.src[c.i]) {
			return c.number()
		}
		return syntaxError(c.src, c.i, "unexpected byte while parsing value")
	}
}

func (c *compactParser) literal(lit string) error {
	if !matchStringAt(c.src, c.i, lit) {
		return syntaxError(c.src, c.i, "invalid literal")
	}
	c.i += len(lit)
	c.dst = append(c.dst, lit...)
	return nil
}

func (c *compactParser) array(depth int) error {
	if depth > c.maxDepth {
		return syntaxError(c.src, c.i, "maximum nesting depth exceeded")
	}
	c.i++
	c.dst = append(c.dst, '[')
	c.skipSpace()
	if c.i < len(c.src) && c.src[c.i] == ']' {
		c.i++
		c.dst = append(c.dst, ']')
		return nil
	}

	for {
		c.skipSpace()
		if err := c.value(depth); err != nil {
			return err
		}
		c.skipSpace()
		if c.i >= len(c.src) {
			return syntaxError(c.src, c.i, "unterminated array")
		}
		switch c.src[c.i] {
		case ',':
			c.i++
			c.dst = append(c.dst, ',')
		case ']':
			c.i++
			c.dst = append(c.dst, ']')
			return nil
		default:
			return syntaxError(c.src, c.i, "expected comma or closing bracket in array")
		}
	}
}

func (c *compactParser) object(depth int) error {
	if depth > c.maxDepth {
		return syntaxError(c.src, c.i, "maximum nesting depth exceeded")
	}
	c.i++
	c.dst = append(c.dst, '{')
	c.skipSpace()
	if c.i < len(c.src) && c.src[c.i] == '}' {
		c.i++
		c.dst = append(c.dst, '}')
		return nil
	}

	for {
		c.skipSpace()
		if c.i >= len(c.src) || c.src[c.i] != '"' {
			return syntaxError(c.src, c.i, "expected object key string")
		}
		if err := c.string(); err != nil {
			return err
		}
		c.skipSpace()
		if c.i >= len(c.src) || c.src[c.i] != ':' {
			return syntaxError(c.src, c.i, "expected colon after object key")
		}
		c.i++
		c.dst = append(c.dst, ':')
		c.skipSpace()
		if err := c.value(depth); err != nil {
			return err
		}
		c.skipSpace()
		if c.i >= len(c.src) {
			return syntaxError(c.src, c.i, "unterminated object")
		}
		switch c.src[c.i] {
		case ',':
			c.i++
			c.dst = append(c.dst, ',')
		case '}':
			c.i++
			c.dst = append(c.dst, '}')
			return nil
		default:
			return syntaxError(c.src, c.i, "expected comma or closing brace in object")
		}
	}
}

func (c *compactParser) number() error {
	start := c.i
	v := validator{src: c.src, i: c.i, maxDepth: c.maxDepth}
	if err := v.parseNumber(); err != nil {
		return err
	}
	c.i = v.i
	c.dst = append(c.dst, c.src[start:c.i]...)
	return nil
}

func (c *compactParser) string() error {
	start := c.i
	c.i++
	for {
		j := scanStringSpecial(c.src, c.i)
		if j >= len(c.src) {
			return syntaxError(c.src, len(c.src), "unterminated string")
		}
		c.i = j
		ch := c.src[c.i]
		switch {
		case ch == '"':
			c.i++
			c.dst = append(c.dst, c.src[start:c.i]...)
			return nil
		case ch == '\\':
			c.i++
			if c.i >= len(c.src) {
				return syntaxError(c.src, c.i, "unterminated escape sequence")
			}
			v := validator{src: c.src, i: c.i, maxDepth: c.maxDepth}
			if err := v.validateEscape(); err != nil {
				return err
			}
			c.i = v.i
		case ch < 0x20:
			return syntaxError(c.src, c.i, "unescaped control byte in string")
		default:
			next, bad := scanStringUnicodeRun(c.src, c.i)
			if bad >= 0 {
				return syntaxError(c.src, bad, "invalid UTF-8 in string")
			}
			c.i = next
		}
	}
}
