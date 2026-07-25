// Package ast_test (an external test package - see foldrange_test.go's own
// doc comment for why: src/ast can't import src/parser, and a hand-built
// tree risks silently sidestepping a real grammar detail).
package ast_test

import (
	"testing"

	"llvm_lang/src/lexer"
	"llvm_lang/src/parser"
)

func TestDeclSymbols_FuncDetail(t *testing.T) {
	src := `func Insert(v int, n int) int {
	return v + n
}
`
	file := lexer.NewFile("t.llx", src)
	tree, diags := parser.ParseFile(file)
	if diags.HasErrors() {
		t.Fatalf("unexpected parse errors: %v", diags.Sorted())
	}

	syms := tree.DeclSymbols()
	if len(syms) != 1 {
		t.Fatalf("len(syms) = %d, want 1", len(syms))
	}
	want := "(v int, n int) int"
	if syms[0].Detail != want {
		t.Errorf("Detail = %q, want %q", syms[0].Detail, want)
	}
}

func TestDeclSymbols_StructDetail(t *testing.T) {
	src := `struct Point {
	x int
	y int
}
`
	file := lexer.NewFile("t.llx", src)
	tree, diags := parser.ParseFile(file)
	if diags.HasErrors() {
		t.Fatalf("unexpected parse errors: %v", diags.Sorted())
	}

	syms := tree.DeclSymbols()
	if len(syms) != 1 {
		t.Fatalf("len(syms) = %d, want 1", len(syms))
	}
	want := "{ x int, y int }"
	if syms[0].Detail != want {
		t.Errorf("Detail = %q, want %q", syms[0].Detail, want)
	}
}

// TestDeclSymbols_ExternFuncDetail is the regression test for a real drift:
// an ExternFuncDecl got no Detail at all here, so unimported-package
// completion showed a signature for a `func` candidate and nothing for an
// `extern func` one - even after sema's own copy of this rendering was
// fixed for the same node-layout difference.
func TestDeclSymbols_ExternFuncDetail(t *testing.T) {
	src := `extern func abs(x i32) i32
`
	file := lexer.NewFile("t.llx", src)
	tree, diags := parser.ParseFile(file)
	if diags.HasErrors() {
		t.Fatalf("unexpected parse errors: %v", diags.Sorted())
	}

	syms := tree.DeclSymbols()
	if len(syms) != 1 {
		t.Fatalf("len(syms) = %d, want 1", len(syms))
	}
	want := "(x i32) i32"
	if syms[0].Detail != want {
		t.Errorf("Detail = %q, want %q", syms[0].Detail, want)
	}
}

func TestDeclSymbols_FuncNoParamsNoReturn(t *testing.T) {
	src := `func Noop() {
}
`
	file := lexer.NewFile("t.llx", src)
	tree, diags := parser.ParseFile(file)
	if diags.HasErrors() {
		t.Fatalf("unexpected parse errors: %v", diags.Sorted())
	}

	syms := tree.DeclSymbols()
	if len(syms) != 1 {
		t.Fatalf("len(syms) = %d, want 1", len(syms))
	}
	want := "()"
	if syms[0].Detail != want {
		t.Errorf("Detail = %q, want %q", syms[0].Detail, want)
	}
}
