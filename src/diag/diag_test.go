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

func TestSortedOrdersByPosition(t *testing.T) {
	b := NewBag()
	b.Errorf(lexer.Pos(10), "second-found-first")
	b.Errorf(lexer.Pos(2), "first-found-second")

	sorted := b.Sorted()
	if sorted[0].Pos != 2 || sorted[1].Pos != 10 {
		t.Fatalf("Sorted() did not order by position: %+v", sorted)
	}
}
