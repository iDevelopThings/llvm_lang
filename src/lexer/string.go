package lexer

import "strings"

// StringValue returns the decoded value of a String token: its source text
// with the surrounding quotes stripped and escape sequences resolved.
// Decoding is lazy - call this only for tokens whose value is actually
// needed (e.g. building an AST literal). When the literal contains no
// backslash, the raw span is returned directly with no allocation.
func (f *File) StringValue(t Token) string {
	if t.End <= t.Start+1 {
		return "" // malformed/unterminated literal
	}
	raw := f.Src[t.Start+1 : t.End-1]
	if !strings.ContainsRune(raw, '\\') {
		return raw
	}
	var b strings.Builder
	b.Grow(len(raw))
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if c != '\\' || i+1 >= len(raw) {
			b.WriteByte(c)
			continue
		}
		i++
		switch raw[i] {
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		case 'r':
			b.WriteByte('\r')
		case '\\':
			b.WriteByte('\\')
		case '"':
			b.WriteByte('"')
		case '0':
			b.WriteByte(0)
		default:
			b.WriteByte('\\')
			b.WriteByte(raw[i])
		}
	}
	return b.String()
}
