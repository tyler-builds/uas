package googlefonts

import (
	"fmt"
	"strconv"
	"strings"
)

// field is one field of a text-format protocol buffer message: a scalar (Value) or a message (Msg).
type field struct {
	Name  string
	Value string
	Msg   []field
}

// parseTextProto parses the protocol buffer text format that METADATA.pb files are written in, enough for
// them: fields "name: value" and "name { ... }" (or "name: { ... }"), quoted strings with C escapes,
// numbers and enum names, and # comments.
func parseTextProto(src string) ([]field, error) {
	p := &tpParser{src: src}
	fs, err := p.message(false)
	if err != nil {
		return nil, fmt.Errorf("METADATA.pb: %w", err)
	}
	return fs, nil
}

type tpParser struct {
	src string
	pos int
}

func (p *tpParser) skip() {
	for p.pos < len(p.src) {
		c := p.src[p.pos]
		switch {
		case c == '#':
			for p.pos < len(p.src) && p.src[p.pos] != '\n' {
				p.pos++
			}
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == ',' || c == ';':
			p.pos++
		default:
			return
		}
	}
}

func (p *tpParser) message(nested bool) ([]field, error) {
	var out []field
	for {
		p.skip()
		if p.pos >= len(p.src) {
			if nested {
				return nil, fmt.Errorf("unclosed message")
			}
			return out, nil
		}
		if p.src[p.pos] == '}' {
			if !nested {
				return nil, fmt.Errorf("unexpected } at %d", p.pos)
			}
			p.pos++
			return out, nil
		}
		name := p.ident()
		if name == "" {
			return nil, fmt.Errorf("expected a field name at %d", p.pos)
		}
		p.skip()
		if p.pos < len(p.src) && p.src[p.pos] == ':' {
			p.pos++
			p.skip()
		}
		if p.pos < len(p.src) && p.src[p.pos] == '{' {
			p.pos++
			msg, err := p.message(true)
			if err != nil {
				return nil, err
			}
			out = append(out, field{Name: name, Msg: msg})
			continue
		}
		v, err := p.value()
		if err != nil {
			return nil, err
		}
		out = append(out, field{Name: name, Value: v})
	}
}

func (p *tpParser) ident() string {
	start := p.pos
	for p.pos < len(p.src) {
		c := p.src[p.pos]
		if c == '_' || c == '.' || c == '-' || c == '+' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') {
			p.pos++
			continue
		}
		break
	}
	return p.src[start:p.pos]
}

func (p *tpParser) value() (string, error) {
	if p.pos >= len(p.src) {
		return "", fmt.Errorf("missing value")
	}
	if q := p.src[p.pos]; q == '"' || q == '\'' {
		// Adjacent strings join, as in C.
		var b strings.Builder
		for p.pos < len(p.src) && (p.src[p.pos] == '"' || p.src[p.pos] == '\'') {
			s, err := p.str()
			if err != nil {
				return "", err
			}
			b.WriteString(s)
			p.skip()
		}
		return b.String(), nil
	}
	v := p.ident()
	if v == "" {
		return "", fmt.Errorf("bad value at %d", p.pos)
	}
	return v, nil
}

// str reads a quoted string, decoding its escapes: \n, \t, \", \\, octal \303 and hex \xc3 bytes.
func (p *tpParser) str() (string, error) {
	q := p.src[p.pos]
	p.pos++
	var b []byte
	for p.pos < len(p.src) {
		c := p.src[p.pos]
		switch {
		case c == q:
			p.pos++
			return string(b), nil
		case c == '\n':
			return "", fmt.Errorf("unterminated string")
		case c != '\\':
			b = append(b, c)
			p.pos++
			continue
		}
		p.pos++
		if p.pos >= len(p.src) {
			break
		}
		e := p.src[p.pos]
		switch {
		case e >= '0' && e <= '7':
			end := p.pos
			for end < len(p.src) && end < p.pos+3 && p.src[end] >= '0' && p.src[end] <= '7' {
				end++
			}
			n, _ := strconv.ParseUint(p.src[p.pos:end], 8, 8)
			b = append(b, byte(n))
			p.pos = end
			continue
		case e == 'x' && p.pos+2 < len(p.src):
			n, err := strconv.ParseUint(p.src[p.pos+1:p.pos+3], 16, 8)
			if err != nil {
				return "", fmt.Errorf("bad escape")
			}
			b = append(b, byte(n))
			p.pos += 3
			continue
		case e == 'n':
			b = append(b, '\n')
		case e == 't':
			b = append(b, '\t')
		case e == 'r':
			b = append(b, '\r')
		default:
			b = append(b, e)
		}
		p.pos++
	}
	return "", fmt.Errorf("unterminated string")
}

// get returns the value of the first field called name.
func get(fs []field, name string) string {
	for _, f := range fs {
		if f.Name == name && f.Msg == nil {
			return f.Value
		}
	}
	return ""
}

// all returns the fields called name.
func all(fs []field, name string) []field {
	var out []field
	for _, f := range fs {
		if f.Name == name {
			out = append(out, f)
		}
	}
	return out
}
