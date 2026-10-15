package headless

// This small application-only JSON representation preserves JavaScript object
// iteration order, binary64 numbers and UTF-16 strings (including lone
// surrogates). It never rewrites the core Session's raw arguments.
import (
	"encoding/json"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

type jsString []uint16
type jsonField struct {
	key   jsString
	value any
}
type jsonObject []jsonField

func jsText(s string) jsString  { return utf16.Encode([]rune(s)) }
func (s jsString) text() string { return string(utf16.Decode(s)) }
func (o jsonObject) get(key string) any {
	for _, f := range o {
		if f.key.text() == key {
			return f.value
		}
	}
	return nil
}
func (o *jsonObject) set(key jsString, value any) {
	for i := range *o {
		if equalString((*o)[i].key, key) {
			(*o)[i].value = value
			return
		}
	}
	*o = append(*o, jsonField{key, value})
}
func equalString(a, b jsString) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
func object(fields ...any) jsonObject {
	o := jsonObject{}
	for i := 0; i < len(fields); i += 2 {
		o.set(jsText(fields[i].(string)), fields[i+1])
	}
	return o
}
func arrayIndex(s jsString) (uint64, bool) {
	v := s.text()
	n, e := strconv.ParseUint(v, 10, 32)
	return n, e == nil && n < math.MaxUint32 && strconv.FormatUint(n, 10) == v
}
func (o jsonObject) ordered() jsonObject {
	copy := append(jsonObject{}, o...)
	sort.SliceStable(copy, func(i, j int) bool {
		a, ai := arrayIndex(copy[i].key)
		b, bi := arrayIndex(copy[j].key)
		if ai != bi {
			return ai
		}
		return ai && a < b
	})
	return copy
}

// Syntax is checked once by encoding/json; the parser below only builds values.
func parseJSON(raw string) (any, bool) {
	if !json.Valid([]byte(raw)) {
		return nil, false
	}
	p := valueParser{raw: raw}
	return p.value(), true
}

type valueParser struct {
	raw string
	at  int
}

func (p *valueParser) space() {
	for p.at < len(p.raw) && strings.ContainsRune(" \t\r\n", rune(p.raw[p.at])) {
		p.at++
	}
}
func (p *valueParser) str() jsString {
	p.at++
	var out jsString
	for p.raw[p.at] != '"' {
		if p.raw[p.at] == '\\' {
			p.at++
			c := p.raw[p.at]
			p.at++
			switch c {
			case 'u':
				n, _ := strconv.ParseUint(p.raw[p.at:p.at+4], 16, 16)
				out = append(out, uint16(n))
				p.at += 4
			case 'b':
				out = append(out, 8)
			case 'f':
				out = append(out, 12)
			case 'n':
				out = append(out, 10)
			case 'r':
				out = append(out, 13)
			case 't':
				out = append(out, 9)
			default:
				out = append(out, uint16(c))
			}
		} else {
			r, n := utf8.DecodeRuneInString(p.raw[p.at:])
			out = append(out, utf16.Encode([]rune{r})...)
			p.at += n
		}
	}
	p.at++
	return out
}
func (p *valueParser) value() any {
	p.space()
	switch p.raw[p.at] {
	case '"':
		return p.str()
	case '{':
		p.at++
		o := jsonObject{}
		p.space()
		for p.raw[p.at] != '}' {
			k := p.str()
			p.space()
			p.at++
			o.set(k, p.value())
			p.space()
			if p.raw[p.at] != ',' {
				break
			}
			p.at++
			p.space()
		}
		p.at++
		return o
	case '[':
		p.at++
		a := []any{}
		p.space()
		for p.raw[p.at] != ']' {
			a = append(a, p.value())
			p.space()
			if p.raw[p.at] != ',' {
				break
			}
			p.at++
		}
		p.at++
		return a
	case 't':
		p.at += 4
		return true
	case 'f':
		p.at += 5
		return false
	case 'n':
		p.at += 4
		return nil
	default:
		start := p.at
		for p.at < len(p.raw) && strings.ContainsRune("-+0123456789.eE", rune(p.raw[p.at])) {
			p.at++
		}
		n, _ := strconv.ParseFloat(p.raw[start:p.at], 64)
		return n
	}
}
func finiteJSON(v any) bool {
	switch x := v.(type) {
	case float64:
		return !math.IsInf(x, 0) && !math.IsNaN(x)
	case jsonObject:
		for _, f := range x {
			if !finiteJSON(f.value) {
				return false
			}
		}
	case []any:
		for _, v := range x {
			if !finiteJSON(v) {
				return false
			}
		}
	}
	return true
}
func parseArguments(raw string) any {
	if raw == "" {
		return jsonObject{}
	}
	v, ok := parseJSON(raw)
	if !ok || !finiteJSON(v) {
		return jsText(raw)
	}
	return v
}

func jsonString(s jsString) string {
	var b strings.Builder
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '"', '\\':
			b.WriteByte('\\')
			b.WriteByte(byte(c))
		case 8:
			b.WriteString(`\b`)
		case 9:
			b.WriteString(`\t`)
		case 10:
			b.WriteString(`\n`)
		case 12:
			b.WriteString(`\f`)
		case 13:
			b.WriteString(`\r`)
		default:
			if c < 32 || c >= 0xd800 && c <= 0xdfff {
				if c >= 0xd800 && c <= 0xdbff && i+1 < len(s) && s[i+1] >= 0xdc00 && s[i+1] <= 0xdfff {
					b.WriteRune(utf16.DecodeRune(rune(c), rune(s[i+1])))
					i++
				} else {
					b.WriteString(`\u`)
					b.WriteString(strconv.FormatUint(uint64(c)|0x10000, 16)[1:])
				}
			} else {
				b.WriteRune(rune(c))
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}
func stringify(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case bool:
		return strconv.FormatBool(x)
	case string:
		return jsonString(jsText(x))
	case jsString:
		return jsonString(x)
	case float64:
		if math.IsInf(x, 0) || math.IsNaN(x) {
			return "null"
		}
		if x == 0 {
			return "0"
		}
		b, _ := json.Marshal(x)
		return string(b)
	case jsonObject:
		parts := make([]string, 0, len(x))
		for _, f := range x.ordered() {
			parts = append(parts, jsonString(f.key)+":"+stringify(f.value))
		}
		return "{" + strings.Join(parts, ",") + "}"
	case []any:
		parts := make([]string, len(x))
		for i, v := range x {
			parts[i] = stringify(v)
		}
		return "[" + strings.Join(parts, ",") + "]"
	default:
		panic("unexpected application JSON value")
	}
}

const maxStringBytes = 8 * 1024
const maxEventBytes = 32 * 1024

func boundString(s jsString, limit int, truncated *bool) jsString {
	text := s.text()
	if len(text) <= limit {
		return s
	}
	*truncated = true
	// Node's Buffer decoding replaces a split character, then removes a trailing
	// U+FFFD, including a genuine replacement character at the cut boundary.
	cut := text[:limit]
	cut = strings.ToValidUTF8(cut, "\uFFFD")
	cut = strings.TrimSuffix(cut, "\uFFFD")
	return jsText(cut)
}
func boundValue(v any, limit, depth int, truncated *bool) any {
	switch x := v.(type) {
	case string:
		return boundString(jsText(x), limit, truncated)
	case jsString:
		return boundString(x, limit, truncated)
	case jsonObject:
		if depth >= 64 {
			*truncated = true
			return jsText("[truncated: depth]")
		}
		out := jsonObject{}
		for _, f := range x.ordered() {
			out.set(boundString(f.key, limit, truncated), boundValue(f.value, limit, depth+1, truncated))
		}
		return out
	case []any:
		if depth >= 64 {
			*truncated = true
			return jsText("[truncated: depth]")
		}
		out := make([]any, len(x))
		for i, v := range x {
			out[i] = boundValue(v, limit, depth+1, truncated)
		}
		return out
	default:
		return v
	}
}
func boundJSONLine(event jsonObject, stringLimit, eventLimit int) string {
	truncated := false
	bounded := boundValue(event, stringLimit, 0, &truncated).(jsonObject)
	if truncated {
		bounded.set(jsText("truncated"), true)
	}
	line := stringify(bounded)
	if len(line)+1 <= eventLimit {
		return line
	}
	scalars := jsonObject{}
	for _, f := range bounded.ordered() {
		switch f.value.(type) {
		case jsonObject, []any:
		default:
			scalars.set(f.key, f.value)
		}
	}
	scalars.set(jsText("truncated"), true)
	line = stringify(scalars)
	if len(line)+1 <= eventLimit {
		return line
	}
	return stringify(object("type", bounded.get("type"), "truncated", true))
}
