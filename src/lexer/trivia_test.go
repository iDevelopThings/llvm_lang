package lexer

import (
	"strings"
	"testing"

	"llvm_lang/src/enums"
)

// reconstruct rebuilds the exact source from a token stream's leading trivia
// plus each token's own text - the property that actually matters (full
// round-trip fidelity), rather than any particular internal trivia shape.
func reconstruct(file *File, toks []Token) string {
	var b strings.Builder
	for _, tok := range toks {
		for _, tr := range file.Trivia(tok.LeadingTrivia) {
			b.WriteString(file.TriviaText(tr))
		}
		b.WriteString(file.Lexeme(tok))
	}
	return b.String()
}

func TestTriviaRoundTrip(t *testing.T) {
	srcs := []string{
		"var a int = 5\nvar b int = 10\n",
		"  \t// leading comment\nfunc add(x int, y int) int {\n\treturn x + y // trailing\n}\n",
		"/* block\n   comment */\nc := a + b\n",
		"a\n\n\nb\n", // blank lines between statements
		"",
		"   \n\n", // trailing whitespace only, no real tokens
	}
	for _, src := range srcs {
		file := NewFile("t.ll", src)
		lx := New(file)
		var toks []Token
		for tok := range lx.All() {
			toks = append(toks, tok)
		}
		got := reconstruct(file, toks)
		if got != src {
			t.Errorf("round trip mismatch:\n got: %q\nwant: %q", got, src)
		}
	}
}

func TestCommentsRecordedAsTrivia(t *testing.T) {
	file := NewFile("t.ll", "// hello\na")
	lx := New(file)
	tok := lx.Next()
	if tok.Lexeme != enums.Lexemes.Identifier {
		t.Fatalf("expected Identifier, got %s", tok.Lexeme)
	}
	trivia := file.Trivia(tok.LeadingTrivia)
	var sawComment bool
	for _, tr := range trivia {
		if tr.Kind == TriviaLineComment {
			sawComment = true
			if file.TriviaText(tr) != "// hello" {
				t.Errorf("comment text = %q, want %q", file.TriviaText(tr), "// hello")
			}
		}
	}
	if !sawComment {
		t.Fatalf("expected a LineComment among leading trivia, got %+v", trivia)
	}
}

func TestBlockCommentASI(t *testing.T) {
	// A newline-containing block comment after an ASI-eligible token still
	// triggers automatic semicolon insertion, same as a bare newline.
	file := NewFile("t.ll", "a /* spans\na newline */ b")
	lx := New(file)
	var lexemes []enums.Lexeme
	for tok := range lx.All() {
		lexemes = append(lexemes, tok.Lexeme)
	}
	want := []enums.Lexeme{
		enums.Lexemes.Identifier,
		enums.Lexemes.Semicolon, // inserted right after the block comment
		enums.Lexemes.Identifier,
		enums.Lexemes.Semicolon, // inserted before EOF: `b` is ASI-eligible too, even with no trailing newline
		enums.Lexemes.EOF,
	}
	if len(lexemes) != len(want) {
		t.Fatalf("lexemes = %v, want %v", lexemes, want)
	}
	for i := range want {
		if lexemes[i] != want[i] {
			t.Errorf("lexeme[%d] = %s, want %s", i, lexemes[i], want[i])
		}
	}
}

func TestEOFCarriesTrailingTrivia(t *testing.T) {
	// `a` is ASI-eligible with no trailing newline in the source, so Go's
	// end-of-file rule fires: the *first* token emitted once l.pos reaches
	// len(src) is the ASI-inserted Semicolon, not EOF - so the trailing
	// comment's trivia lands there, and a genuine EOF (queried afterward)
	// is empty. This is what TestTriviaRoundTrip really guarantees end to
	// end; this test just documents which token owns it.
	file := NewFile("t.ll", "a // trailing comment, no newline")
	lx := New(file)
	lx.Next() // a
	semi := lx.Next()
	if semi.Lexeme != enums.Lexemes.Semicolon {
		t.Fatalf("expected the ASI Semicolon, got %s", semi.Lexeme)
	}
	trivia := file.Trivia(semi.LeadingTrivia)
	var found bool
	for _, tr := range trivia {
		if tr.Kind == TriviaLineComment {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected the trailing comment among leading trivia, got %+v", trivia)
	}

	eof := lx.Next()
	if eof.Lexeme != enums.Lexemes.EOF {
		t.Fatalf("expected EOF, got %s", eof.Lexeme)
	}
	if eof.LeadingTrivia.Count != 0 {
		t.Fatalf("expected EOF to carry no further trivia, got %+v", file.Trivia(eof.LeadingTrivia))
	}
}
