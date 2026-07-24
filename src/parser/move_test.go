package parser

import (
	"testing"

	"llvm_lang/src/lexer"
)

// TestMoveExprShape covers `move x` (see LANGUAGE.md's "Destructors"
// section's "move" subsection) - a prefix keyword wrapping a bare
// identifier, the same shape `new`/`delete` already establish for a
// leading-keyword expression/statement.
func TestMoveExprShape(t *testing.T) {
	src := "move x"
	want := "MoveExpr \"move\"\n  Ident \"x\"\n"

	tree, n := parseExprSrc(t, src)
	got := tree.Dump(n)
	if got != want {
		t.Errorf("Dump(%q):\n got:\n%s\nwant:\n%s", src, got, want)
	}
}

// TestMoveExprInVarDecl covers `move x` reaching a real short-var-decl's own
// initializer position, the same thin end-to-end wiring check
// TestPointerVarDecl performs for a pointer type.
func TestMoveExprInVarDecl(t *testing.T) {
	src := "y := move x"
	want := "" +
		"ShortVarDecl \":=\"\n" +
		"  Ident \"y\"\n" +
		"  MoveExpr \"move\"\n" +
		"    Ident \"x\"\n"

	tree, n := parseStmtSrc(t, src)
	got := tree.Dump(n)
	if got != want {
		t.Errorf("Dump(%q):\n got:\n%s\nwant:\n%s", src, got, want)
	}
}

// TestMoveExprRejectsNonIdentifierOperand covers the parser's own "bare
// identifier only" restriction (see parseMoveExpr's own doc comment):
// `move this.field`/`move arr[i]`/`move (x)` (a parenthesized name too - this
// language deliberately doesn't special-case unwrapping a paren around a
// move operand) all still parse as one contiguous expression, but are
// rejected with exactly one diagnostic each rather than leaving a trailing
// token for the statement parser to stumble over.
func TestMoveExprRejectsNonIdentifierOperand(t *testing.T) {
	tests := []struct {
		name string
		src  string
	}{
		{"field access", "move this.field"},
		{"array index", "move arr[i]"},
		{"parenthesized identifier", "move (x)"},
		{"call expression", "move f()"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := New(lexer.NewFile("t.ll", tt.src))
			p.parseExpr(precLowest)
			if p.diags.ErrorCount() != 1 {
				t.Fatalf("ErrorCount = %d, want 1: %v", p.diags.ErrorCount(), p.diags.All())
			}
		})
	}
}
