package parser

import (
	"strings"
	"testing"

	"llvm_lang/src/enums"
	"llvm_lang/src/lexer"
)

func TestExpectAcceptHappyPath(t *testing.T) {
	file := lexer.NewFile("t.ll", "a + b")
	ok, diags := Run(file, func(p *Parser) bool {
		p.expect(enums.Lexemes.Identifier)
		p.expect(enums.Lexemes.Plus)
		p.expect(enums.Lexemes.Identifier)
		return true
	})
	if !ok {
		t.Fatal("expected true result")
	}
	if diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %v", diags.All())
	}
}

func TestExpectReportsMismatchWithoutConsuming(t *testing.T) {
	file := lexer.NewFile("t.ll", "a + b")
	_, diags := Run(file, func(p *Parser) any {
		p.expect(enums.Lexemes.Number) // mismatch: it's actually an Identifier
		if !p.at(enums.Lexemes.Identifier) {
			t.Fatal("expect must not consume on mismatch")
		}
		return nil
	})
	if diags.ErrorCount() != 1 {
		t.Fatalf("ErrorCount = %d, want 1", diags.ErrorCount())
	}
}

func TestLexErrorsFlowInAutomatically(t *testing.T) {
	file := lexer.NewFile("t.ll", "a $ b")
	_, diags := Run(file, func(p *Parser) any {
		for !p.at(enums.Lexemes.EOF) {
			p.advance()
		}
		return nil
	})
	if diags.ErrorCount() != 1 {
		t.Fatalf("ErrorCount = %d, want 1 (the illegal '$')", diags.ErrorCount())
	}
}

func TestSyncRecoversToStatementBoundary(t *testing.T) {
	file := lexer.NewFile("t.ll", "a + ; b")
	_, diags := Run(file, func(p *Parser) any {
		p.expect(enums.Lexemes.Identifier) // a
		p.expect(enums.Lexemes.Plus)       // +
		p.expect(enums.Lexemes.Identifier) // mismatch: sees ';'
		p.sync(enums.Lexemes.Semicolon)
		if !p.at(enums.Lexemes.Semicolon) {
			t.Fatal("sync should land on the semicolon")
		}
		return nil
	})
	if diags.ErrorCount() != 1 {
		t.Fatalf("ErrorCount = %d, want 1", diags.ErrorCount())
	}
}

func TestBailoutAfterTooManyErrors(t *testing.T) {
	file := lexer.NewFile("t.ll", "+ + + + + + + + + + + + + +")
	_, diags := Run(file, func(p *Parser) any {
		for !p.at(enums.Lexemes.EOF) {
			p.expect(enums.Lexemes.Identifier) // every '+' is a mismatch
			p.advance()
		}
		return nil
	})
	if diags.ErrorCount() != maxErrors {
		t.Fatalf("ErrorCount = %d, want exactly maxErrors=%d", diags.ErrorCount(), maxErrors)
	}
}

func TestRunRecoversBailoutWithoutCrashing(t *testing.T) {
	file := lexer.NewFile("t.ll", "+ + + + + + + + + + + + + +")
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Run leaked a panic: %v", r)
		}
	}()
	Run(file, func(p *Parser) any {
		for {
			p.expect(enums.Lexemes.Identifier)
			p.advance()
		}
	})
}

// TestParseFile_BailoutReturnsUsableTreeNotNil is the regression test for a
// real crash: ParseFile's own *ast.Tree return type instantiates Run's
// generic T, and before Run recovered into p.tree (rather than leaving
// result at T's zero value), a bailout meant ParseFile returned a nil
// *ast.Tree - any caller that didn't know to check for this (src/loader's
// own directory scan among them) crashed with a nil-pointer dereference the
// moment it read tree.Root/tree.Children, on wholly legitimate, if broken,
// source (a real file with 10+ genuine parse errors, no different from one
// with 9).
func TestParseFile_BailoutReturnsUsableTreeNotNil(t *testing.T) {
	// Mirrors a real-world repro (a generated FFI-binding file with a
	// malformed type token repeated once per declaration) - each line is
	// its own top-level declaration error via the real ParseFile grammar,
	// not a synthetic always-fail loop, so this actually exercises the
	// same maxErrors path a genuinely broken source file hits.
	src := strings.Repeat("func f() !bad\n", maxErrors+2)
	file := lexer.NewFile("t.ll", src)
	tree, diags := ParseFile(file, false)

	if tree == nil {
		t.Fatal("ParseFile returned a nil *ast.Tree on bailout - unsafe for any caller to touch")
	}
	if diags.ErrorCount() != maxErrors {
		t.Fatalf("ErrorCount = %d, want exactly maxErrors=%d", diags.ErrorCount(), maxErrors)
	}
	if got := tree.Children(tree.Root); len(got) != 0 {
		t.Errorf("tree.Children(tree.Root) = %v, want empty (Root never got set past the bailout)", got)
	}
}
