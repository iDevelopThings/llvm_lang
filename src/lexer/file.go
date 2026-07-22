package lexer

import (
	"fmt"
	"sort"
	"strings"
)

// File is a lexed source unit: its text plus a line-start table built
// incrementally during lexing. A token only ever carries a byte offset (Pos);
// resolving that offset to a human line/column is deferred to here and paid
// only when a diagnostic actually needs one, via binary search over the line
// table rather than tracking line/col on every token as it's scanned.
type File struct {
	Name string
	Src  string

	// lineStarts[i] is the byte offset of the first byte of line i+1.
	// lineStarts[0] is always 0.
	lineStarts []int32

	// trivia is the shared arena every token's LeadingTrivia range indexes
	// into, appended to as the lexer skips whitespace and comments.
	trivia []Trivia
}

// NewFile wraps source text for lexing. name is used only for diagnostics.
func NewFile(name, src string) *File {
	return &File{
		Name:       name,
		Src:        src,
		lineStarts: []int32{0},
	}
}

// markLine records that a new line begins at offset. Called by the lexer
// each time it consumes a newline, so the table costs one append per source
// line, not per token.
func (f *File) markLine(offset int32) {
	f.lineStarts = append(f.lineStarts, offset)
}

// Position is a resolved, 1-based human-readable source location.
type Position struct {
	Filename string
	Line     int
	Column   int
	Offset   int
}

func (p Position) String() string {
	if p.Filename == "" {
		return fmt.Sprintf("%d:%d", p.Line, p.Column)
	}
	return fmt.Sprintf("%s:%d:%d", p.Filename, p.Line, p.Column)
}

// Position resolves a byte offset to a line/column.
func (f *File) Position(p Pos) Position {
	off := int(p)
	i := sort.Search(len(f.lineStarts), func(i int) bool { return int(f.lineStarts[i]) > off }) - 1
	if i < 0 {
		i = 0
	}
	return Position{
		Filename: f.Name,
		Line:     i + 1,
		Column:   off - int(f.lineStarts[i]) + 1,
		Offset:   off,
	}
}

// Lexeme returns the exact, zero-copy source text a token spans.
func (f *File) Lexeme(t Token) string {
	return f.Src[t.Start:t.End]
}

// addTrivia records one skipped run and returns its index in the arena.
func (f *File) addTrivia(kind TriviaKind, start, end int32) {
	f.trivia = append(f.trivia, Trivia{
		Kind:  kind,
		Start: Pos(start),
		End:   Pos(end),
	})
}

// Trivia returns the trivia entries in r - typically a token's LeadingTrivia.
func (f *File) Trivia(r Range) []Trivia {
	return f.trivia[r.Start:r.End()]
}

// TriviaText returns a trivia entry's exact, zero-copy source text.
func (f *File) TriviaText(t Trivia) string {
	return f.Src[t.Start:t.End]
}

// Line returns the source text of the given 1-based line, without its
// terminator. Used to render a caret under an error position.
func (f *File) Line(line int) string {
	if line < 1 || line > len(f.lineStarts) {
		return ""
	}
	start := int(f.lineStarts[line-1])
	end := len(f.Src)
	if line < len(f.lineStarts) {
		end = int(f.lineStarts[line])
	}
	return strings.TrimRight(f.Src[start:end], "\r\n")
}
