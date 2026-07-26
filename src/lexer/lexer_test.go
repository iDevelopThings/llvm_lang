package lexer

import (
	"testing"

	"llvm_lang/src/enums"
)

type wantTok struct {
	lex enums.Lexeme
	kw  enums.Keyword
	txt string
}

func collect(t *testing.T, src string) ([]Token, *File) {
	t.Helper()
	file := NewFile("test.ll", src)
	lx := New(file)
	var toks []Token
	for tok := range lx.All() {
		toks = append(toks, tok)
		if tok.Lexeme == enums.Lexemes.Illegal {
			t.Fatalf("illegal token at %v: %q", file.Position(tok.Start), file.Lexeme(tok))
		}
	}
	if errs := lx.Errors(); len(errs) > 0 {
		t.Fatalf("lexer errors: %v", errs)
	}
	return toks, file
}

func check(t *testing.T, toks []Token, file *File, want []wantTok) {
	t.Helper()
	if len(toks) != len(want) {
		var got []string
		for _, tok := range toks {
			got = append(got, tok.Lexeme.String()+"("+file.Lexeme(tok)+")")
		}
		t.Fatalf("token count = %d, want %d\ngot: %v", len(toks), len(want), got)
	}
	for i, w := range want {
		tok := toks[i]
		if tok.Lexeme != w.lex {
			t.Errorf("token[%d] lexeme = %s, want %s (text %q)", i, tok.Lexeme, w.lex, file.Lexeme(tok))
		}
		if tok.Keyword != w.kw {
			t.Errorf("token[%d] keyword = %q, want %q", i, tok.Keyword, w.kw)
		}
		if w.txt != "" && file.Lexeme(tok) != w.txt {
			t.Errorf("token[%d] text = %q, want %q", i, file.Lexeme(tok), w.txt)
		}
	}
}

func TestVarDeclAndASI(t *testing.T) {
	toks, file := collect(t, "var a int = 5\nvar b int = 10\n")
	L := enums.Lexemes
	K := enums.Keywords
	check(t, toks, file, []wantTok{
		{L.Identifier, K.Var, "var"},
		{L.Identifier, "", "a"},
		{L.Identifier, "", "int"},
		{L.Equal, "", "="},
		{L.Number, "", "5"},
		{L.Semicolon, "", ""}, // inserted
		{L.Identifier, K.Var, "var"},
		{L.Identifier, "", "b"},
		{L.Identifier, "", "int"},
		{L.Equal, "", "="},
		{L.Number, "", "10"},
		{L.Semicolon, "", ""}, // inserted
		{L.EOF, "", ""},
	})
}

// TestThisTriggersASI covers asiEligible's `this` keyword case (added
// alongside sema's checkThisExpr change letting a bare `this` be used as an
// ordinary value - see AGENTS.md/LANGUAGE.md): a newline right after a bare
// `this` must insert a virtual semicolon exactly like it already does after
// `break`/`continue`/`return`/`true`/`false` - otherwise a statement like
// `p := this` followed by another statement on the next line would never
// get a separator between them at all.
func TestThisTriggersASI(t *testing.T) {
	toks, file := collect(t, "p := this\nq := 1\n")
	L := enums.Lexemes
	K := enums.Keywords
	check(t, toks, file, []wantTok{
		{L.Identifier, "", "p"},
		{L.ColonEqual, "", ":="},
		{L.Identifier, K.This, "this"},
		{L.Semicolon, "", ""}, // inserted
		{L.Identifier, "", "q"},
		{L.ColonEqual, "", ":="},
		{L.Number, "", "1"},
		{L.Semicolon, "", ""}, // inserted
		{L.EOF, "", ""},
	})
}

func TestWalrusAndBinaryOp(t *testing.T) {
	toks, file := collect(t, "c := a + b\n")
	L := enums.Lexemes
	check(t, toks, file, []wantTok{
		{L.Identifier, "", "c"},
		{L.ColonEqual, "", ":="},
		{L.Identifier, "", "a"},
		{L.Plus, "", "+"},
		{L.Identifier, "", "b"},
		{L.Semicolon, "", ""},
		{L.EOF, "", ""},
	})
}

func TestFuncDeclBraces(t *testing.T) {
	src := "func add(x int, y int) int {\n\treturn x + y\n}\n"
	toks, file := collect(t, src)
	L := enums.Lexemes
	K := enums.Keywords
	check(t, toks, file, []wantTok{
		{L.Identifier, K.Func, "func"},
		{L.Identifier, "", "add"},
		{L.LeftParen, "", "("},
		{L.Identifier, "", "x"},
		{L.Identifier, "", "int"},
		{L.Comma, "", ","},
		{L.Identifier, "", "y"},
		{L.Identifier, "", "int"},
		{L.RightParen, "", ")"},
		{L.Identifier, "", "int"},
		{L.LeftBrace, "", "{"},
		{L.Identifier, K.Return, "return"},
		{L.Identifier, "", "x"},
		{L.Plus, "", "+"},
		{L.Identifier, "", "y"},
		{L.Semicolon, "", ""}, // inserted before }
		{L.RightBrace, "", "}"},
		{L.Semicolon, "", ""}, // inserted after }
		{L.EOF, "", ""},
	})
}

func TestOneLineIf(t *testing.T) {
	toks, file := collect(t, `if c >= 10: print("....")`+"\n")
	L := enums.Lexemes
	K := enums.Keywords
	check(t, toks, file, []wantTok{
		{L.Identifier, K.If, "if"},
		{L.Identifier, "", "c"},
		{L.GreaterThanEqual, "", ">="},
		{L.Number, "", "10"},
		{L.Colon, "", ":"},
		{L.Identifier, "", "print"},
		{L.LeftParen, "", "("},
		{L.String, "", `"...."`},
		{L.RightParen, "", ")"},
		{L.Semicolon, "", ""},
		{L.EOF, "", ""},
	})
}

func TestBoolLiteralsAndFullExample(t *testing.T) {
	src := "var n bool = true\n" +
		"if c >= 10 {\n" +
		"    print(\"....\")\n" +
		"} else {\n" +
		"    print(\"....\")\n" +
		"}\n"
	toks, _ := collect(t, src)
	L := enums.Lexemes
	K := enums.Keywords
	// spot-check the interesting bits rather than the whole stream
	var boolTok Token
	for _, tok := range toks {
		if tok.Keyword == K.True {
			boolTok = tok
		}
	}
	if boolTok.Lexeme != L.Identifier {
		t.Fatalf("expected `true` to lex as Identifier+Keyword(True), got %v", boolTok)
	}
}

func TestStringEscapes(t *testing.T) {
	file := NewFile("test.ll", `"a\nb\"c\\d"`)
	lx := New(file)
	tok := lx.Next()
	if tok.Lexeme != enums.Lexemes.String {
		t.Fatalf("expected String, got %s", tok.Lexeme)
	}
	got := file.StringValue(tok)
	want := "a\nb\"c\\d"
	if got != want {
		t.Fatalf("StringValue = %q, want %q", got, want)
	}
}

func TestUnterminatedString(t *testing.T) {
	file := NewFile("test.ll", `"unterminated`)
	lx := New(file)
	tok := lx.Next()
	if tok.Lexeme != enums.Lexemes.String {
		t.Fatalf("expected String (best-effort), got %s", tok.Lexeme)
	}
	if len(lx.Errors()) == 0 {
		t.Fatalf("expected an unterminated string error")
	}
}

func TestIllegalCharacter(t *testing.T) {
	file := NewFile("test.ll", "a $ b")
	lx := New(file)
	lx.Next() // a
	tok := lx.Next()
	if tok.Lexeme != enums.Lexemes.Illegal {
		t.Fatalf("expected Illegal, got %s", tok.Lexeme)
	}
	if len(lx.Errors()) == 0 {
		t.Fatalf("expected an illegal-character error")
	}
}

func TestPeekDoesNotConsume(t *testing.T) {
	file := NewFile("test.ll", "a b")
	lx := New(file)
	p1 := lx.Peek()
	p2 := lx.Peek()
	if p1 != p2 {
		t.Fatalf("Peek not idempotent: %v != %v", p1, p2)
	}
	n := lx.Next()
	if n != p1 {
		t.Fatalf("Next after Peek = %v, want %v", n, p1)
	}
	if file.Lexeme(lx.Next()) != "b" {
		t.Fatalf("expected second token b")
	}
}

func TestPositionResolution(t *testing.T) {
	file := NewFile("test.ll", "var a int = 5\nvar b int = 10\n")
	lx := New(file)
	var bTok Token
	for tok := range lx.All() {
		if file.Lexeme(tok) == "b" {
			bTok = tok
			break
		}
	}
	pos := file.Position(bTok.Start)
	if pos.Line != 2 || pos.Column != 5 {
		t.Fatalf("Position(b) = %+v, want line 2 col 5", pos)
	}
}

func TestNumberForms(t *testing.T) {
	toks, file := collect(t, "5 10 3.14 2.5e10 1e-3\n")
	L := enums.Lexemes
	want := []string{"5", "10", "3.14", "2.5e10", "1e-3"}
	for i, w := range want {
		if toks[i].Lexeme != L.Number || file.Lexeme(toks[i]) != w {
			t.Errorf("token[%d] = %s %q, want Number %q", i, toks[i].Lexeme, file.Lexeme(toks[i]), w)
		}
	}
}

// TestEllipsisToken covers the new `...` token (see LANGUAGE.md's "Variadic
// parameters" section) - a plain `.` must stay unaffected, and `...` must
// scan as one single DotDotDot token, not three separate Dots.
func TestEllipsisToken(t *testing.T) {
	toks, file := collect(t, "a.b\nc...\n")
	L := enums.Lexemes
	check(t, toks, file, []wantTok{
		{L.Identifier, "", "a"},
		{L.Dot, "", "."},
		{L.Identifier, "", "b"},
		{L.Semicolon, "", ""}, // inserted
		{L.Identifier, "", "c"},
		{L.DotDotDot, "", "..."},
		// no ASI after `...` - it's punctuation, like Dot/Comma/LeftParen
		{L.EOF, "", ""},
	})
}
