package httpapi

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"
)

// BadNumber is a numeric JSON value that parsed to NaN or +/-Inf, or did
// not parse as a finite number at all. The standard encoding/json
// decoder rejects these before field-level validation can name the
// offending field; this tolerant scanner exists so errors such as
// "measurement value is not a finite number" can point at
// readings[3].value precisely.
type BadNumber struct {
	Path string
	Raw  string
}

type scanner struct {
	data []byte
	pos  int

	stack []string // current container path segments
	bad   []BadNumber
}

// scanLooseJSON parses a JSON document with a recursive-descent scanner
// that, unlike encoding/json, accepts the tokens NaN, Infinity and
// -Infinity and exponents that overflow float64. It returns every
// non-finite numeric token with its field path (e.g.
// readings[3].value). Any genuinely malformed JSON is reported.
func scanLooseJSON(data []byte) ([]BadNumber, error) {
	s := &scanner{data: data}
	s.skipWS()
	if err := s.parseValue(); err != nil {
		return nil, err
	}
	s.skipWS()
	if s.pos != len(s.data) {
		return nil, fmt.Errorf("invalid character %q after top-level value", string(s.data[s.pos]))
	}
	return s.bad, nil
}

func (s *scanner) parseValue() error {
	s.skipWS()
	if s.pos >= len(s.data) {
		return fmt.Errorf("unexpected end of JSON input")
	}
	switch s.data[s.pos] {
	case '{':
		return s.parseObject()
	case '[':
		return s.parseArray()
	case '"':
		_, err := s.parseString()
		return err
	case 't':
		return s.literal("true")
	case 'f':
		return s.literal("false")
	case 'n':
		return s.literal("null")
	default:
		return s.parseNumber()
	}
}

func (s *scanner) parseObject() error {
	s.pos++ // {
	s.skipWS()
	if s.pos < len(s.data) && s.data[s.pos] == '}' {
		s.pos++
		return nil
	}
	for {
		s.skipWS()
		if s.pos >= len(s.data) || s.data[s.pos] != '"' {
			return fmt.Errorf("expected object key at offset %d", s.pos)
		}
		key, err := s.parseString()
		if err != nil {
			return err
		}
		s.skipWS()
		if s.pos >= len(s.data) || s.data[s.pos] != ':' {
			return fmt.Errorf("expected ':' after key %q", key)
		}
		s.pos++
		s.stack = append(s.stack, key)
		if err := s.parseValue(); err != nil {
			return err
		}
		s.stack = s.stack[:len(s.stack)-1]
		s.skipWS()
		if s.pos >= len(s.data) {
			return fmt.Errorf("unexpected end of JSON inside object")
		}
		switch s.data[s.pos] {
		case ',':
			s.pos++
			continue
		case '}':
			s.pos++
			return nil
		default:
			return fmt.Errorf("expected ',' or '}}' at offset %d", s.pos)
		}
	}
}

func (s *scanner) parseArray() error {
	s.pos++ // [
	s.skipWS()
	if s.pos < len(s.data) && s.data[s.pos] == ']' {
		s.pos++
		return nil
	}
	idx := 0
	for {
		s.stack = append(s.stack, fmt.Sprintf("[%d]", idx))
		if err := s.parseValue(); err != nil {
			return err
		}
		s.stack = s.stack[:len(s.stack)-1]
		idx++
		s.skipWS()
		if s.pos >= len(s.data) {
			return fmt.Errorf("unexpected end of JSON inside array")
		}
		switch s.data[s.pos] {
		case ',':
			s.pos++
			continue
		case ']':
			s.pos++
			return nil
		default:
			return fmt.Errorf("expected ',' or ']' at offset %d", s.pos)
		}
	}
}

func (s *scanner) parseString() (string, error) {
	start := s.pos
	s.pos++ // opening quote
	for s.pos < len(s.data) {
		c := s.data[s.pos]
		if c == '"' {
			s.pos++
			var str string
			if err := directDecodeString(s.data[start:s.pos], &str); err != nil {
				return "", err
			}
			return str, nil
		}
		if c == '\\' {
			s.pos++
			if s.pos >= len(s.data) {
				return "", fmt.Errorf("unterminated escape in string")
			}
		} else if c < 0x20 {
			return "", fmt.Errorf("unescaped control character in string at offset %d", s.pos)
		}
		s.pos++
	}
	return "", fmt.Errorf("unterminated string")
}

func directDecodeString(raw []byte, out *string) error {
	if len(raw) < 2 || raw[0] != '"' || raw[len(raw)-1] != '"' {
		return fmt.Errorf("invalid string token")
	}
	var b strings.Builder
	for i := 1; i < len(raw)-1; {
		c := raw[i]
		if c != '\\' {
			r, size := utf8.DecodeRune(raw[i : len(raw)-1])
			b.WriteRune(r)
			i += size
			continue
		}
		i++
		if i >= len(raw)-1 {
			return fmt.Errorf("bad escape")
		}
		switch raw[i] {
		case '"', '\\', '/':
			b.WriteByte(raw[i])
		case 'b':
			b.WriteByte('\b')
		case 'f':
			b.WriteByte('\f')
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case 't':
			b.WriteByte('\t')
		case 'u':
			if i+4 >= len(raw)-1 {
				return fmt.Errorf("bad unicode escape")
			}
			n, err := strconv.ParseUint(string(raw[i+1:i+5]), 16, 32)
			if err != nil {
				return err
			}
			r := rune(n)
			if utf16Surrogate(r) {
				if i+10 < len(raw)-1 && raw[i+5] == '\\' && raw[i+6] == 'u' {
					lo, err := strconv.ParseUint(string(raw[i+7:i+11]), 16, 32)
					if err == nil {
						r = utf16Decode(r, rune(lo))
						i += 6
					}
				}
			}
			b.WriteRune(r)
			i += 4
		default:
			return fmt.Errorf("invalid escape \\%c", raw[i])
		}
		i++
	}
	*out = b.String()
	return nil
}

func utf16Surrogate(r rune) bool { return r >= 0xD800 && r <= 0xDBFF }

func utf16Decode(hi, lo rune) rune {
	if lo < 0xDC00 || lo > 0xDFFF {
		return hi
	}
	return 0x10000 + (hi-0xD800)<<10 + (lo - 0xDC00)
}

func (s *scanner) literal(want string) error {
	if s.pos+len(want) > len(s.data) || string(s.data[s.pos:s.pos+len(want)]) != want {
		return fmt.Errorf("invalid literal at offset %d", s.pos)
	}
	s.pos += len(want)
	return nil
}

func (s *scanner) parseNumber() error {
	start := s.pos
	// Tolerated non-standard numeric tokens.
	for _, tok := range []string{"-Infinity", "Infinity", "NaN"} {
		if s.pos+len(tok) <= len(s.data) && string(s.data[s.pos:s.pos+len(tok)]) == tok {
			s.pos += len(tok)
			s.recordBad(tok)
			return nil
		}
	}
	// Standard JSON number grammar.
	if s.data[s.pos] == '-' {
		s.pos++
	}
	if s.pos >= len(s.data) {
		return fmt.Errorf("invalid number at offset %d", start)
	}
	switch {
	case s.data[s.pos] == '0':
		s.pos++
	case s.data[s.pos] >= '1' && s.data[s.pos] <= '9':
		for s.pos < len(s.data) && s.data[s.pos] >= '0' && s.data[s.pos] <= '9' {
			s.pos++
		}
	default:
		return fmt.Errorf("invalid character %q looking for number at offset %d", string(s.data[s.pos]), s.pos)
	}
	if s.pos < len(s.data) && s.data[s.pos] == '.' {
		s.pos++
		if s.pos >= len(s.data) || s.data[s.pos] < '0' || s.data[s.pos] > '9' {
			return fmt.Errorf("invalid decimal at offset %d", s.pos)
		}
		for s.pos < len(s.data) && s.data[s.pos] >= '0' && s.data[s.pos] <= '9' {
			s.pos++
		}
	}
	if s.pos < len(s.data) && (s.data[s.pos] == 'e' || s.data[s.pos] == 'E') {
		s.pos++
		if s.pos < len(s.data) && (s.data[s.pos] == '+' || s.data[s.pos] == '-') {
			s.pos++
		}
		if s.pos >= len(s.data) || s.data[s.pos] < '0' || s.data[s.pos] > '9' {
			return fmt.Errorf("invalid exponent at offset %d", s.pos)
		}
		for s.pos < len(s.data) && s.data[s.pos] >= '0' && s.data[s.pos] <= '9' {
			s.pos++
		}
	}
	raw := string(s.data[start:s.pos])
	f, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		s.recordBad(raw)
	}
	return nil
}

func (s *scanner) recordBad(raw string) {
	s.bad = append(s.bad, BadNumber{Path: s.currentPath(), Raw: raw})
}

func (s *scanner) currentPath() string {
	var b strings.Builder
	for i, seg := range s.stack {
		switch {
		case strings.HasPrefix(seg, "["):
			b.WriteString(seg)
		case i == 0:
			b.WriteString(seg)
		default:
			b.WriteByte('.')
			b.WriteString(seg)
		}
	}
	return b.String()
}

func (s *scanner) skipWS() {
	for s.pos < len(s.data) {
		switch s.data[s.pos] {
		case ' ', '\t', '\n', '\r':
			s.pos++
		default:
			return
		}
	}
}
