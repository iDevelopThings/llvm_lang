package codewriter

import (
	"bytes"
	"fmt"
	"io"
)

// Writer accumulates source text with a current indent depth.
//
// Zero value is ready to use with tab indentation and "//" comments.
// Prefer New when setting options or pre-sizing the buffer.
type Writer struct {
	buf        bytes.Buffer
	indentUnit string
	comment    string
	depth      int
	midLine    bool // false (zero value) means next write starts a line
}

// Option configures a Writer at construction.
type Option func(*Writer)

// IndentUnit sets the per-depth indent string. Default is "\t".
func IndentUnit(unit string) Option {
	return func(w *Writer) {
		if unit != "" {
			w.indentUnit = unit
		}
	}
}

// CommentPrefix sets the line-comment marker. Default is "//".
func CommentPrefix(prefix string) Option {
	return func(w *Writer) {
		if prefix != "" {
			w.comment = prefix
		}
	}
}

// Grow pre-sizes the internal buffer to reduce reallocations.
func Grow(n int) Option {
	return func(w *Writer) {
		if n > 0 {
			w.buf.Grow(n)
		}
	}
}

// New returns a Writer with tab indents and "//" comments.
func New(opts ...Option) *Writer {
	w := &Writer{
		indentUnit: "\t",
		comment:    "//",
	}
	for _, opt := range opts {
		opt(w)
	}
	return w
}

func (w *Writer) initDefaults() {
	if w.indentUnit == "" {
		w.indentUnit = "\t"
	}
	if w.comment == "" {
		w.comment = "//"
	}
}

// Line writes an indented line followed by a newline.
func (w *Writer) Line(s string) {
	w.ensureIndent()
	w.buf.WriteString(s)
	w.newline()
}

// Linef writes an indented formatted line followed by a newline.
func (w *Writer) Linef(format string, args ...any) {
	w.ensureIndent()
	if len(args) == 0 {
		w.buf.WriteString(format)
	} else {
		fmt.Fprintf(&w.buf, format, args...)
	}
	w.newline()
}

// Lines writes each string as its own indented line.
func (w *Writer) Lines(lines ...string) {
	for _, line := range lines {
		w.Line(line)
	}
}

// Print writes indented text with no trailing newline.
// Indent is applied only at the start of a line.
func (w *Writer) Print(s string) {
	w.ensureIndent()
	w.buf.WriteString(s)
}

// Printf writes indented formatted text with no trailing newline.
func (w *Writer) Printf(format string, args ...any) {
	w.ensureIndent()
	if len(args) == 0 {
		w.buf.WriteString(format)
	} else {
		fmt.Fprintf(&w.buf, format, args...)
	}
}

// Raw appends text with no indentation.
func (w *Writer) Raw(s string) {
	if s == "" {
		return
	}
	w.buf.WriteString(s)
	w.midLine = s[len(s)-1] != '\n'
}

// Rawf appends formatted text with no indentation.
func (w *Writer) Rawf(format string, args ...any) {
	if len(args) == 0 {
		w.Raw(format)
		return
	}
	before := w.buf.Len()
	fmt.Fprintf(&w.buf, format, args...)
	if w.buf.Len() > before {
		w.midLine = w.buf.Bytes()[w.buf.Len()-1] != '\n'
	}
}

// NL ends the current line.
func (w *Writer) NL() {
	w.buf.WriteByte('\n')
	w.midLine = false
}

// Blank writes a blank line.
func (w *Writer) Blank() {
	if w.midLine {
		w.newline()
	}
	w.buf.WriteByte('\n')
	w.midLine = false
}

// Indent runs body with depth increased by one.
func (w *Writer) Indent(body func()) {
	_ = w.IndentErr(func() error {
		body()
		return nil
	})
}

// IndentErr is Indent for a body that can fail. The indent depth is restored
// before the error is returned.
func (w *Writer) IndentErr(body func() error) error {
	w.depth++
	defer func() {
		w.depth--
	}()
	return body()
}

// Group writes header, open delimiter, indented body, then close delimiter.
//
//	w.Group("import", "(", ")", func() { w.Line(`"fmt"`) })
//
// emits:
//
//	import (
//		"fmt"
//	)
//
// An empty header writes only the delimiters.
func (w *Writer) Group(header, open, close string, body func()) {
	_ = w.GroupErr(header, open, close, func() error {
		body()
		return nil
	})
}

// GroupErr is Group for a body that can fail. It restores the indent depth but
// does not write the closing delimiter when body returns an error.
func (w *Writer) GroupErr(
	header string,
	open string,
	close string,
	body func() error,
) error {
	w.ensureIndent()
	if header != "" {
		w.buf.WriteString(header)
		w.buf.WriteByte(' ')
	}
	w.buf.WriteString(open)
	w.newline()
	if err := w.IndentErr(body); err != nil {
		return err
	}
	w.ensureIndent()
	w.buf.WriteString(close)
	w.newline()
	return nil
}

// Groupf is Group with a formatted header. The last argument must be the
// body func(); preceding arguments are fmt args for format.
//
//	w.Groupf("var %s = []%s", "{", "}", name, typ, func() { ... })
func (w *Writer) Groupf(format, open, close string, args ...any) {
	body, headerArgs := splitBody(args)
	header := format
	if len(headerArgs) > 0 {
		header = fmt.Sprintf(format, headerArgs...)
	}
	w.Group(header, open, close, body)
}

// Brace writes a { ... } group.
func (w *Writer) Brace(header string, body func()) {
	w.Group(header, "{", "}", body)
}

// BraceErr is Brace for a body that can fail.
func (w *Writer) BraceErr(header string, body func() error) error {
	return w.GroupErr(header, "{", "}", body)
}

// Bracef is Brace with a formatted header. Last arg must be func().
//
//	w.Bracef("type %s struct", name, func() { w.Line("X int") })
func (w *Writer) Bracef(format string, args ...any) {
	body, headerArgs := splitBody(args)
	header := format
	if len(headerArgs) > 0 {
		header = fmt.Sprintf(format, headerArgs...)
	}
	w.Brace(header, body)
}

// Paren writes a ( ... ) group.
func (w *Writer) Paren(header string, body func()) {
	w.Group(header, "(", ")", body)
}

// ParenErr is Paren for a body that can fail.
func (w *Writer) ParenErr(header string, body func() error) error {
	return w.GroupErr(header, "(", ")", body)
}

// Parenf is Paren with a formatted header. Last arg must be func().
func (w *Writer) Parenf(format string, args ...any) {
	body, headerArgs := splitBody(args)
	header := format
	if len(headerArgs) > 0 {
		header = fmt.Sprintf(format, headerArgs...)
	}
	w.Paren(header, body)
}

// Open writes header and open delimiter, then increases depth.
// Pair with Close. Prefer Brace/Paren/Group when the body is a clean
// callback; use Open/Close when control flow needs an early exit.
func (w *Writer) Open(header, open string) {
	w.ensureIndent()
	if header != "" {
		w.buf.WriteString(header)
		w.buf.WriteByte(' ')
	}
	w.buf.WriteString(open)
	w.newline()
	w.depth++
}

// Close decreases depth and writes the close delimiter.
func (w *Writer) Close(close string) {
	if w.depth > 0 {
		w.depth--
	}
	w.ensureIndent()
	w.buf.WriteString(close)
	w.newline()
}

// Comment writes a single line comment at the current indent.
func (w *Writer) Comment(s string) {
	w.initDefaults()
	w.ensureIndent()
	w.buf.WriteString(w.comment)
	if s != "" {
		w.buf.WriteByte(' ')
		w.buf.WriteString(s)
	}
	w.newline()
}

// Commentf writes a formatted line comment.
func (w *Writer) Commentf(format string, args ...any) {
	if len(args) == 0 {
		w.Comment(format)
		return
	}
	w.Comment(fmt.Sprintf(format, args...))
}

// Comments writes each string as its own line comment.
// An empty string emits the comment marker alone.
func (w *Writer) Comments(lines ...string) {
	for _, line := range lines {
		w.Comment(line)
	}
}

// Bytes returns the accumulated source.
func (w *Writer) Bytes() []byte {
	return w.buf.Bytes()
}

// String returns the accumulated source.
func (w *Writer) String() string {
	return w.buf.String()
}

// Len returns the number of bytes written.
func (w *Writer) Len() int {
	return w.buf.Len()
}

// Reset clears the buffer and indent depth.
func (w *Writer) Reset() {
	w.buf.Reset()
	w.depth = 0
	w.midLine = false
}

// WriteTo writes the accumulated source to dst.
func (w *Writer) WriteTo(dst io.Writer) (int64, error) {
	return w.buf.WriteTo(dst)
}

func (w *Writer) ensureIndent() {
	w.initDefaults()
	if w.midLine {
		return
	}
	for range w.depth {
		w.buf.WriteString(w.indentUnit)
	}
	w.midLine = true
}

func (w *Writer) newline() {
	w.buf.WriteByte('\n')
	w.midLine = false
}

func splitBody(args []any) (func(), []any) {
	if len(args) == 0 {
		panic("codewriter: missing body func()")
	}
	body, ok := args[len(args)-1].(func())
	if !ok {
		panic("codewriter: last argument must be func()")
	}
	return body, args[:len(args)-1]
}
