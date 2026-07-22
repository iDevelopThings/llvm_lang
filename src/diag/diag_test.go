package diag

import (
	"strings"
	"testing"

	"llvm_lang/src/lexer"
)

func TestFormatAndSnippet(t *testing.T) {
	file := lexer.NewFile("t.ll", "var a int = 5\nvar b int = ???\n")
	// File's line table is built incrementally as the lexer scans, so drive
	// it once to populate it before resolving a position - matching real
	// usage, where diagnostics are read after a full lex/parse pass.
	for tok := range lexer.New(file).All() {
		_ = tok
	}
	b := NewBag()
	// position the error at the '?' run on line 2.
	b.Errorf(lexer.Pos(len("var a int = 5\nvar b int = ")), "unexpected character %q", '?')

	if !b.HasErrors() || b.ErrorCount() != 1 {
		t.Fatalf("ErrorCount = %d, want 1", b.ErrorCount())
	}

	line := Format(file, b.All()[0])
	if !strings.HasPrefix(line, "t.ll:2:13:") {
		t.Fatalf("Format = %q, want prefix t.ll:2:13:", line)
	}

	snippet := FormatSnippet(file, b.All()[0])
	wantLines := []string{
		"t.ll:2:13: error: unexpected character '?'",
		"var b int = ???",
		strings.Repeat(" ", 12) + "^",
	}
	got := strings.Split(snippet, "\n")
	if len(got) != len(wantLines) {
		t.Fatalf("FormatSnippet lines = %d, want %d\n%s", len(got), len(wantLines), snippet)
	}
	for i, w := range wantLines {
		if got[i] != w {
			t.Errorf("line[%d] = %q, want %q", i, got[i], w)
		}
	}
}

func TestSeqYieldsEveryDiagnosticInEncounterOrder(t *testing.T) {
	b := NewBag()
	b.Errorf(lexer.Pos(10), "first")
	b.Warnf(lexer.Pos(2), "second")

	var got []string
	for d := range b.Seq() {
		got = append(got, d.Msg)
	}
	want := []string{"first", "second"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("Seq() yielded %v, want %v", got, want)
	}
}

func TestSortedOrdersByPosition(t *testing.T) {
	b := NewBag()
	b.Errorf(lexer.Pos(10), "second-found-first")
	b.Errorf(lexer.Pos(2), "first-found-second")

	sorted := b.Sorted()
	if sorted[0].Pos != 2 || sorted[1].Pos != 10 {
		t.Fatalf("Sorted() did not order by position: %+v", sorted)
	}
}

// lexedFile builds a *lexer.File and drives the lexer over it once, so its
// line table is populated before any diag.Bag/FormatSnippet call resolves a
// position against it - matching real usage (see TestFormatAndSnippet).
func lexedFile(t *testing.T, name, src string) *lexer.File {
	t.Helper()
	file := lexer.NewFile(name, src)
	for tok := range lexer.New(file).All() {
		_ = tok
	}
	return file
}

// TestFormatSnippetSpanRendersMultipleCarets covers ErrorfSpan: a
// multi-character span underlines every column it covers with its own '^',
// not just a single-point caret at the start.
func TestFormatSnippetSpanRendersMultipleCarets(t *testing.T) {
	src := "var abcde int = 1\n"
	file := lexedFile(t, "t.ll", src)

	b := NewBag()
	start := lexer.Pos(len("var "))
	end := lexer.Pos(len("var abcde"))
	b.ErrorfSpan(start, end, "bad name")

	snippet := FormatSnippet(file, b.All()[0])
	wantCaretLine := strings.Repeat(" ", 4) + strings.Repeat("^", 5) // "abcde" is 5 columns
	got := strings.Split(snippet, "\n")
	if len(got) != 3 {
		t.Fatalf("FormatSnippet lines = %d, want 3\n%s", len(got), snippet)
	}
	if got[2] != wantCaretLine {
		t.Errorf("caret line = %q, want %q", got[2], wantCaretLine)
	}
}

// TestFormatSnippetCrossLineSpanFallsBackToLineEnd covers a span whose Pos
// and End resolve to different physical lines (not the common case, but
// FormatSnippet must still produce something sane rather than a nonsensical
// or negative caret run): it falls back to underlining from the start
// column to the end of the one reported (start) line.
func TestFormatSnippetCrossLineSpanFallsBackToLineEnd(t *testing.T) {
	src := "var ab\ncd int = 1\n"
	file := lexedFile(t, "t.ll", src)

	b := NewBag()
	start := lexer.Pos(len("var "))     // "ab", line 1
	end := lexer.Pos(len("var ab\ncd")) // crosses onto line 2
	b.ErrorfSpan(start, end, "bad span")

	snippet := FormatSnippet(file, b.All()[0])
	got := strings.Split(snippet, "\n")
	if len(got) != 3 {
		t.Fatalf("FormatSnippet lines = %d, want 3\n%s", len(got), snippet)
	}
	// line 1 is "var ab" (6 runes); start column is 5 (1-based), so the
	// fallback underlines from column 5 to the end of that line - 2 carets.
	wantCaretLine := strings.Repeat(" ", 4) + strings.Repeat("^", 2)
	if got[2] != wantCaretLine {
		t.Errorf("caret line = %q, want %q", got[2], wantCaretLine)
	}
}

// TestFormatSnippetLabelAppendsArrowHint covers ErrorfLabel/WarnfLabel: a
// diagnostic with a non-empty Label renders a trailing " <- label" after the
// caret underline.
func TestFormatSnippetLabelAppendsArrowHint(t *testing.T) {
	src := "var a int = unknown\n"
	file := lexedFile(t, "t.ll", src)

	b := NewBag()
	start := lexer.Pos(len("var a int = "))
	end := lexer.Pos(len("var a int = unknown"))
	b.ErrorfLabel(start, end, "not found", "undefined: %s", "unknown")

	snippet := FormatSnippet(file, b.All()[0])
	if !strings.HasSuffix(snippet, "<- not found") {
		t.Errorf("FormatSnippet = %q, want a trailing \"<- not found\"", snippet)
	}

	got := strings.Split(snippet, "\n")
	if len(got) != 3 {
		t.Fatalf("FormatSnippet lines = %d, want 3\n%s", len(got), snippet)
	}
	wantCaretLine := strings.Repeat(" ", 12) + strings.Repeat("^", 7) + " <- not found"
	if got[2] != wantCaretLine {
		t.Errorf("caret line = %q, want %q", got[2], wantCaretLine)
	}
}

// TestFormatSnippetWarnfLabelIsWarningSeverity covers WarnfLabel specifically
// (as opposed to ErrorfLabel): the rendered header says "warning", and the
// diagnostic doesn't count toward ErrorCount/HasErrors.
func TestFormatSnippetWarnfLabelIsWarningSeverity(t *testing.T) {
	src := "var a int = 1\n"
	file := lexedFile(t, "t.ll", src)

	b := NewBag()
	pos := lexer.Pos(len("var "))
	b.WarnfLabel(pos, pos+1, "hint", "some warning")

	if b.HasErrors() || b.ErrorCount() != 0 {
		t.Fatalf("WarnfLabel must not count as an error: ErrorCount = %d", b.ErrorCount())
	}

	snippet := FormatSnippet(file, b.All()[0])
	if !strings.Contains(snippet, ": warning: some warning") {
		t.Errorf("FormatSnippet = %q, want a \"warning\" severity header", snippet)
	}
	if !strings.HasSuffix(snippet, "<- hint") {
		t.Errorf("FormatSnippet = %q, want a trailing \"<- hint\"", snippet)
	}
}

// TestFormatSnippetSanitizesTabsInEchoedLine covers the tab-to-space fix: a
// literal tab in the source line being echoed is replaced with a single
// space, so the caret math (which treats every rune, tabs included, as
// exactly one column - see File.Position) stays aligned with what's actually
// printed, rather than a tab rendering wider than one column in a real
// terminal and visually drifting the caret away from its target column.
func TestFormatSnippetSanitizesTabsInEchoedLine(t *testing.T) {
	src := "\tvar a int = ?\n"
	file := lexedFile(t, "t.ll", src)

	b := NewBag()
	pos := lexer.Pos(len("\tvar a int = "))
	b.Errorf(pos, "unexpected character %q", '?')

	snippet := FormatSnippet(file, b.All()[0])
	got := strings.Split(snippet, "\n")
	if len(got) != 3 {
		t.Fatalf("FormatSnippet lines = %d, want 3\n%s", len(got), snippet)
	}
	if strings.Contains(got[1], "\t") {
		t.Errorf("echoed line = %q, still contains a literal tab", got[1])
	}
	wantLine := " var a int = ?"
	if got[1] != wantLine {
		t.Errorf("echoed line = %q, want %q", got[1], wantLine)
	}
	// The tab (column 1) counts as exactly one column, same as any other
	// rune - the caret for column 14 (the '?') still lines up at index 13.
	wantCaretLine := strings.Repeat(" ", 13) + "^"
	if got[2] != wantCaretLine {
		t.Errorf("caret line = %q, want %q", got[2], wantCaretLine)
	}
}
