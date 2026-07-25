package parser

import (
	"testing"

	"llvm_lang/src/lexer"
)

// This file covers this round's new grammar for generator functions (see
// LANGUAGE.md's "Generator functions" section): a FuncDecl's own `yield T`
// return-type marker (YieldReturnType), parsed only at parseFuncDeclReturnType's
// own position - table-driven Tree.Dump shape assertions, matching this
// package's established convention (see multireturn_test.go).

func TestFuncDeclYieldReturnTypeShape(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "yield return type wraps the element type",
			src:  "func Range(a int, b int) yield int { }",
			want: "" +
				"FuncDecl \"func\"\n" +
				"  <missing>\n" +
				"  Ident \"Range\"\n" +
				"  <missing>\n" +
				"  ParamList\n" +
				"    Param\n" +
				"      Ident \"a\"\n" +
				"      Ident \"int\"\n" +
				"    Param\n" +
				"      Ident \"b\"\n" +
				"      Ident \"int\"\n" +
				"  YieldReturnType \"yield\"\n" +
				"    Ident \"int\"\n" +
				"  Block\n",
		},
		{
			name: "plain single return type is completely unchanged",
			src:  "func f() int { return 0 }",
			want: "" +
				"FuncDecl \"func\"\n" +
				"  <missing>\n" +
				"  Ident \"f\"\n" +
				"  <missing>\n" +
				"  ParamList\n" +
				"  Ident \"int\"\n" +
				"  Block\n" +
				"    ReturnStmt \"return\"\n" +
				"      NumberLit \"0\"\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := New(lexer.NewFile("t.ll", tt.src))
			n := p.parseTopLevelItem()
			if p.diags.HasErrors() {
				t.Fatalf("unexpected parse errors for %q: %v", tt.src, p.diags.All())
			}
			got := p.tree.Dump(n)
			if got != tt.want {
				t.Errorf("Dump(%q):\n got:\n%s\nwant:\n%s", tt.src, got, tt.want)
			}
		})
	}
}

// TestFuncDeclYieldReturnTypeOnMethodParsesFine proves the grammar itself
// doesn't know yet whether a receiver clause preceded the return type - a
// method declaring `yield T` parses structurally fine; sema is what rejects
// it (see sema.checkFuncDecl's own "a method cannot be a generator function"
// diagnostic).
func TestFuncDeclYieldReturnTypeOnMethodParsesFine(t *testing.T) {
	src := "func (Point) Values() yield int { }"
	p := New(lexer.NewFile("t.ll", src))
	p.parseTopLevelItem()
	if p.diags.HasErrors() {
		t.Fatalf("unexpected parse errors for %q: %v", src, p.diags.All())
	}
}

// TestYieldReturnTypeOnFuncLitIsParseError proves `yield T` is structurally
// impossible on a FuncLit's own return type - parseFuncLitReturnType-
// equivalent parsing calls parseTypeExpr directly, never
// parseFuncDeclReturnType (see that function's own doc comment), so `yield`
// there is just an ordinary parse error, not a silently-accepted generator
// literal.
func TestYieldReturnTypeOnFuncLitIsParseError(t *testing.T) {
	src := "var x = func() yield int { }"
	p := New(lexer.NewFile("t.ll", src))
	p.parseTopLevelItem()
	if !p.diags.HasErrors() {
		t.Fatalf("expected a parse error for a FuncLit's own `yield` return type, got none")
	}
}

// TestYieldReturnTypeOnExternFuncDeclIsParseError mirrors the FuncLit case
// one declaration kind over (see parseExternFuncDecl's own doc comment: its
// return type is also a plain parseTypeExpr(), never parseFuncDeclReturnType()).
func TestYieldReturnTypeOnExternFuncDeclIsParseError(t *testing.T) {
	src := "extern func f() yield int"
	p := New(lexer.NewFile("t.ll", src))
	p.parseTopLevelItem()
	if !p.diags.HasErrors() {
		t.Fatalf("expected a parse error for an ExternFuncDecl's own `yield` return type, got none")
	}
}
