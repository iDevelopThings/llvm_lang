package parser

import (
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
