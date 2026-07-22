package parser

import (
	"testing"

	"llvm_lang/src/ast"
	"llvm_lang/src/lexer"
)

// parseTypeExprSrc parses src as a single type-position expression (see
// parseTypeExpr) and fails the test on any diagnostic.
func parseTypeExprSrc(t *testing.T, src string) (*ast.Tree, ast.NodeIndex) {
	t.Helper()
	p := New(lexer.NewFile("t.ll", src))
	n := p.parseTypeExpr()
	if p.diags.HasErrors() {
		t.Fatalf("unexpected parse errors for %q: %v", src, p.diags.All())
	}
	return p.tree, n
}

// TestFuncTypeShape covers parsing precedence/nesting for function-type
// expressions (`func(T1, T2) R`) - the new type-position grammar rule this
// round adds for first-class function values (see LANGUAGE.md's
// "First-class functions" section). Every case asserts the exact tree shape
// via Tree.Dump, matching this project's established style for parser
// precedence/shape tests.
func TestFuncTypeShape(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "no params, no return type",
			src:  "func()",
			want: "" +
				"FuncType \"func\"\n" +
				"  ParamTypeList\n" +
				"  <missing>\n",
		},
		{
			name: "one param, no return type",
			src:  "func(int)",
			want: "" +
				"FuncType \"func\"\n" +
				"  ParamTypeList\n" +
				"    Ident \"int\"\n" +
				"  <missing>\n",
		},
		{
			name: "params and return type",
			src:  "func(int, int) int",
			want: "" +
				"FuncType \"func\"\n" +
				"  ParamTypeList\n" +
				"    Ident \"int\"\n" +
				"    Ident \"int\"\n" +
				"  Ident \"int\"\n",
		},
		{
			name: "no params, with return type",
			src:  "func() bool",
			want: "" +
				"FuncType \"func\"\n" +
				"  ParamTypeList\n" +
				"  Ident \"bool\"\n",
		},
		{
			name: "a function type as one of its own parameter types",
			src:  "func(func(int) int, string) bool",
			want: "" +
				"FuncType \"func\"\n" +
				"  ParamTypeList\n" +
				"    FuncType \"func\"\n" +
				"      ParamTypeList\n" +
				"        Ident \"int\"\n" +
				"      Ident \"int\"\n" +
				"    Ident \"string\"\n" +
				"  Ident \"bool\"\n",
		},
		{
			name: "a function type as its own return type",
			src:  "func(int) func(int) int",
			want: "" +
				"FuncType \"func\"\n" +
				"  ParamTypeList\n" +
				"    Ident \"int\"\n" +
				"  FuncType \"func\"\n" +
				"    ParamTypeList\n" +
				"      Ident \"int\"\n" +
				"    Ident \"int\"\n",
		},
		{
			name: "an array of function types",
			src:  "[3]func(int) int",
			want: "" +
				"ArrayType\n" +
				"  NumberLit \"3\"\n" +
				"  FuncType \"func\"\n" +
				"    ParamTypeList\n" +
				"      Ident \"int\"\n" +
				"    Ident \"int\"\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tree, n := parseTypeExprSrc(t, tt.src)
			got := tree.Dump(n)
			if got != tt.want {
				t.Errorf("Dump(%q):\n got:\n%s\nwant:\n%s", tt.src, got, tt.want)
			}
		})
	}
}

// TestFuncTypeInVarDeclAndParam covers a function type used in the two
// other positions that matter besides a bare type expression: a VarDecl's
// own type annotation, and a FuncDecl parameter's type - both already go
// through the same parseTypeExpr this file's other tests exercise directly,
// so these are thin end-to-end checks that the wiring itself is correct.
func TestFuncTypeInVarDeclAndParam(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "var decl with a function-typed annotation",
			src:  "var f func(int, int) int",
			want: "" +
				"VarDecl \"var\"\n" +
				"  Ident \"f\"\n" +
				"  FuncType \"func\"\n" +
				"    ParamTypeList\n" +
				"      Ident \"int\"\n" +
				"      Ident \"int\"\n" +
				"    Ident \"int\"\n" +
				"  <missing>\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := New(lexer.NewFile("t.ll", tt.src))
			n := p.parseStmt()
			if p.diags.HasErrors() {
				t.Fatalf("unexpected parse errors for %q: %v", tt.src, p.diags.All())
			}
			got := p.tree.Dump(n)
			if got != tt.want {
				t.Errorf("Dump(%q):\n got:\n%s\nwant:\n%s", tt.src, got, tt.want)
			}
		})
	}

	// A function-typed parameter (`fn func(int) int`) - parsed via
	// parseFuncDecl/parseParam, not parseTypeExpr directly.
	src := "func apply(fn func(int) int, x int) int { return fn(x) }"
	want := "" +
		"FuncDecl \"func\"\n" +
		"  <missing>\n" +
		"  Ident \"apply\"\n" +
		"  ParamList\n" +
		"    Param\n" +
		"      Ident \"fn\"\n" +
		"      FuncType \"func\"\n" +
		"        ParamTypeList\n" +
		"          Ident \"int\"\n" +
		"        Ident \"int\"\n" +
		"    Param\n" +
		"      Ident \"x\"\n" +
		"      Ident \"int\"\n" +
		"  Ident \"int\"\n" +
		"  Block\n" +
		"    ReturnStmt \"return\"\n" +
		"      CallExpr\n" +
		"        Ident \"fn\"\n" +
		"        Ident \"x\"\n"
	p := New(lexer.NewFile("t.ll", src))
	n := p.parseTopLevelItem()
	if p.diags.HasErrors() {
		t.Fatalf("unexpected parse errors for %q: %v", src, p.diags.All())
	}
	got := p.tree.Dump(n)
	if got != want {
		t.Errorf("Dump(%q):\n got:\n%s\nwant:\n%s", src, got, want)
	}
}
