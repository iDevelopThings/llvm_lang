// Package diag collects compiler diagnostics (lexical and syntactic errors,
// eventually warnings) as they're found, rather than surfacing each one as a
// Go error the caller must check at every call site. A Bag is passed down
// through lexing/parsing; producers just append to it and keep going, and
// whoever's driving the pipeline inspects it once, at a point of their
// choosing.
package diag

import (
	"fmt"
	"sort"
	"strings"

	"llvm_lang/src/lexer"
)

// Severity classifies a Diagnostic.
type Severity int

const (
	SeverityError Severity = iota
	SeverityWarning
)

func (s Severity) String() string {
	if s == SeverityWarning {
		return "warning"
	}
	return "error"
}

// Diagnostic is one recorded problem at a source position.
type Diagnostic struct {
	Pos      lexer.Pos
	Severity Severity
	Msg      string
}

// Bag accumulates diagnostics in encounter order. The zero value is not
// usable; construct one with NewBag.
type Bag struct {
	diags []Diagnostic
	errN  int // count with Severity == SeverityError, tracked incrementally
}

// NewBag returns an empty Bag.
func NewBag() *Bag { return &Bag{} }

func (b *Bag) add(pos lexer.Pos, sev Severity, msg string) {
	b.diags = append(b.diags, Diagnostic{
		Pos:      pos,
		Severity: sev,
		Msg:      msg,
	})
	if sev == SeverityError {
		b.errN++
	}
}

// Errorf records an error-severity diagnostic at pos.
func (b *Bag) Errorf(pos lexer.Pos, format string, a ...any) {
	b.add(pos, SeverityError, fmt.Sprintf(format, a...))
}

// Warnf records a warning-severity diagnostic at pos.
func (b *Bag) Warnf(pos lexer.Pos, format string, a ...any) {
	b.add(pos, SeverityWarning, fmt.Sprintf(format, a...))
}

// ErrorCount returns the number of error-severity diagnostics recorded.
func (b *Bag) ErrorCount() int { return b.errN }

// HasErrors reports whether any error-severity diagnostic was recorded.
func (b *Bag) HasErrors() bool { return b.errN > 0 }

// Len returns the total number of diagnostics, errors and warnings alike.
func (b *Bag) Len() int { return len(b.diags) }

// All returns every diagnostic in encounter order.
func (b *Bag) All() []Diagnostic {
	out := make([]Diagnostic, len(b.diags))
	copy(out, b.diags)
	return out
}

// Sorted returns diagnostics ordered by source position. Encounter order can
// differ from source order once statement-level error recovery jumps
// around, so this is what a CLI should print.
func (b *Bag) Sorted() []Diagnostic {
	out := b.All()
	sort.SliceStable(out, func(i, j int) bool { return out[i].Pos < out[j].Pos })
	return out
}

// Format renders one diagnostic as "file:line:col: severity: message".
func Format(file *lexer.File, d Diagnostic) string {
	return fmt.Sprintf("%s: %s: %s", file.Position(d.Pos), d.Severity, d.Msg)
}

// FormatSnippet renders a diagnostic like Format, followed by its source
// line and a caret pointing at the offending column.
func FormatSnippet(file *lexer.File, d Diagnostic) string {
	pos := file.Position(d.Pos)
	col := pos.Column - 1
	if col < 0 {
		col = 0
	}
	return fmt.Sprintf("%s\n%s\n%s^", Format(file, d), file.Line(pos.Line), strings.Repeat(" ", col))
}

// FormatAll renders every diagnostic in the bag, sorted by position, one per
// line.
func FormatAll(file *lexer.File, b *Bag) string {
	var sb strings.Builder
	for _, d := range b.Sorted() {
		sb.WriteString(Format(file, d))
		sb.WriteByte('\n')
	}
	return sb.String()
}
