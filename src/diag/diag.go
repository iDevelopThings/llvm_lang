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

// Diagnostic is one recorded problem at a source position, optionally
// spanning a range rather than a single point.
type Diagnostic struct {
	Pos      lexer.Pos
	End      lexer.Pos // == Pos (or unset/zero) means "no span" - a single-point caret
	Severity Severity
	Msg      string
	Label    string // short trailing hint appended after the caret(s); "" means none
}

// hasSpan reports whether d carries a real range rather than a single point -
// End is only ever set via ErrorfSpan/WarnfSpan/ErrorfLabel/WarnfLabel, so a
// plain Errorf/Warnf diagnostic (End left at its zero value, or explicitly
// equal to Pos) always renders as a single caret.
func (d Diagnostic) hasSpan() bool {
	return d.End > d.Pos
}

// Bag accumulates diagnostics in encounter order. The zero value is not
// usable; construct one with NewBag.
type Bag struct {
	diags []Diagnostic
	errN  int // count with Severity == SeverityError, tracked incrementally
}

// NewBag returns an empty Bag.
func NewBag() *Bag { return &Bag{} }

func (b *Bag) add(start, end lexer.Pos, label string, sev Severity, msg string) {
	b.diags = append(b.diags, Diagnostic{
		Pos:      start,
		End:      end,
		Severity: sev,
		Msg:      msg,
		Label:    label,
	})
	if sev == SeverityError {
		b.errN++
	}
}

// Errorf records an error-severity diagnostic at the single point pos.
func (b *Bag) Errorf(pos lexer.Pos, format string, a ...any) {
	b.add(pos, pos, "", SeverityError, fmt.Sprintf(format, a...))
}

// Warnf records a warning-severity diagnostic at the single point pos.
func (b *Bag) Warnf(pos lexer.Pos, format string, a ...any) {
	b.add(pos, pos, "", SeverityWarning, fmt.Sprintf(format, a...))
}

// ErrorfSpan records an error-severity diagnostic spanning [start, end) -
// FormatSnippet underlines the whole range instead of a single caret.
func (b *Bag) ErrorfSpan(start, end lexer.Pos, format string, a ...any) {
	b.add(start, end, "", SeverityError, fmt.Sprintf(format, a...))
}

// WarnfSpan records a warning-severity diagnostic spanning [start, end).
func (b *Bag) WarnfSpan(start, end lexer.Pos, format string, a ...any) {
	b.add(start, end, "", SeverityWarning, fmt.Sprintf(format, a...))
}

// ErrorfLabel records an error-severity diagnostic spanning [start, end) with
// a short trailing label rendered after the caret underline (see
// FormatSnippet) - e.g. pointing at a specific sub-token with a hint like
// "unexported symbol" rather than relying on the message text alone.
func (b *Bag) ErrorfLabel(start, end lexer.Pos, label, format string, a ...any) {
	b.add(start, end, label, SeverityError, fmt.Sprintf(format, a...))
}

// WarnfLabel records a warning-severity diagnostic spanning [start, end) with
// a short trailing label - the warning counterpart to ErrorfLabel.
func (b *Bag) WarnfLabel(start, end lexer.Pos, label, format string, a ...any) {
	b.add(start, end, label, SeverityWarning, fmt.Sprintf(format, a...))
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
// line and a caret (or, for a diagnostic with a real span, a run of carets
// underlining the whole range) pointing at the offending column, plus a
// trailing " <- label" when the diagnostic carries one.
//
// A literal tab character in the echoed line is replaced with a single space
// before printing: File.Position already counts a tab as exactly one column
// (same as any other rune), but echoing it verbatim would render wider than
// one column in any real terminal, visually drifting the caret line away
// from the column the math actually computed. Swapping it for one space
// keeps the 1-rune-per-column assumption the caret math already relies on
// true for the printed line too.
func FormatSnippet(file *lexer.File, d Diagnostic) string {
	pos := file.Position(d.Pos)
	line := sanitizeLine(file.Line(pos.Line))

	col := pos.Column - 1
	if col < 0 {
		col = 0
	}

	width := 1
	if d.hasSpan() {
		endPos := file.Position(d.End)
		if endPos.Line == pos.Line {
			width = endPos.Column - pos.Column
		} else {
			// A span crossing physical lines isn't rendered as a real
			// multi-line underline (diagnostics here are overwhelmingly
			// single-line) - fall back to underlining from the start
			// column to the end of the one reported line.
			width = len(line) - col
		}
		if width < 1 {
			width = 1
		}
	}

	caretLine := strings.Repeat(" ", col) + strings.Repeat("^", width)
	if d.Label != "" {
		caretLine += " <- " + d.Label
	}

	return fmt.Sprintf("%s\n%s\n%s", Format(file, d), line, caretLine)
}

// sanitizeLine replaces every literal tab in line with a single space - see
// FormatSnippet's doc comment for why.
func sanitizeLine(line string) string {
	return strings.ReplaceAll(line, "\t", " ")
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
