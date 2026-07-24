package parser

import (
	"testing"

	"llvm_lang/src/lexer"
)

// TestCFuncTypeShape covers parsing precedence/nesting for the bare-C-
// function-pointer type expression `cfunc(T1, T2) R` - see LANGUAGE.md's
// "External functions (FFI)" section. Mirrors functype_test.go's
// TestFuncTypeShape exactly, just for the `cfunc` keyword/CFuncType node
// instead of `func`/FuncType.
func TestCFuncTypeShape(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "no params, no return type",
			src:  "cfunc()",
			want: "" +
				"CFuncType \"cfunc\"\n" +
				"  ParamTypeList\n" +
				"  <missing>\n",
		},
		{
			name: "one param, no return type",
			src:  "cfunc(int)",
			want: "" +
				"CFuncType \"cfunc\"\n" +
				"  ParamTypeList\n" +
				"    Ident \"int\"\n" +
				"  <missing>\n",
		},
		{
			name: "params and return type",
			src:  "cfunc(int, int) int",
			want: "" +
				"CFuncType \"cfunc\"\n" +
				"  ParamTypeList\n" +
				"    Ident \"int\"\n" +
				"    Ident \"int\"\n" +
				"  Ident \"int\"\n",
		},
		{
			name: "no params, with return type",
			src:  "cfunc() bool",
			want: "" +
				"CFuncType \"cfunc\"\n" +
				"  ParamTypeList\n" +
				"  Ident \"bool\"\n",
		},
		{
			name: "a cfunc type as one of its own parameter types",
			src:  "cfunc(cfunc(int) int, cstring) bool",
			want: "" +
				"CFuncType \"cfunc\"\n" +
				"  ParamTypeList\n" +
				"    CFuncType \"cfunc\"\n" +
				"      ParamTypeList\n" +
				"        Ident \"int\"\n" +
				"      Ident \"int\"\n" +
				"    Ident \"cstring\"\n" +
				"  Ident \"bool\"\n",
		},
		{
			name: "a cfunc type as its own return type",
			src:  "cfunc(int) cfunc(int) int",
			want: "" +
				"CFuncType \"cfunc\"\n" +
				"  ParamTypeList\n" +
				"    Ident \"int\"\n" +
				"  CFuncType \"cfunc\"\n" +
				"    ParamTypeList\n" +
				"      Ident \"int\"\n" +
				"    Ident \"int\"\n",
		},
		{
			name: "an array of cfunc types",
			src:  "[3]cfunc(int) int",
			want: "" +
				"ArrayType\n" +
				"  NumberLit \"3\"\n" +
				"  CFuncType \"cfunc\"\n" +
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

// TestCFuncTypeInVarDeclAndExternParam covers a cfunc type used in the two
// positions that matter for FFI: a VarDecl's own type annotation, and an
// extern func parameter's type.
func TestCFuncTypeInVarDeclAndExternParam(t *testing.T) {
	src := "var f cfunc(int, int) int"
	want := "" +
		"VarDecl \"var\"\n" +
		"  Ident \"f\"\n" +
		"  CFuncType \"cfunc\"\n" +
		"    ParamTypeList\n" +
		"      Ident \"int\"\n" +
		"      Ident \"int\"\n" +
		"    Ident \"int\"\n" +
		"  <missing>\n"
	p := New(lexer.NewFile("t.ll", src))
	n := p.parseStmt()
	if p.diags.HasErrors() {
		t.Fatalf("unexpected parse errors for %q: %v", src, p.diags.All())
	}
	got := p.tree.Dump(n)
	if got != want {
		t.Errorf("Dump(%q):\n got:\n%s\nwant:\n%s", src, got, want)
	}

	// A cfunc-typed extern func parameter (`cb cfunc(int) int`).
	externSrc := "extern func register(cb cfunc(int) int) int"
	externWant := "" +
		"ExternFuncDecl \"extern\"\n" +
		"  Ident \"register\"\n" +
		"  ParamList\n" +
		"    Param\n" +
		"      Ident \"cb\"\n" +
		"      CFuncType \"cfunc\"\n" +
		"        ParamTypeList\n" +
		"          Ident \"int\"\n" +
		"        Ident \"int\"\n" +
		"  Ident \"int\"\n"
	p2 := New(lexer.NewFile("t.ll", externSrc))
	n2 := p2.parseTopLevelItem()
	if p2.diags.HasErrors() {
		t.Fatalf("unexpected parse errors for %q: %v", externSrc, p2.diags.All())
	}
	got2 := p2.tree.Dump(n2)
	if got2 != externWant {
		t.Errorf("Dump(%q):\n got:\n%s\nwant:\n%s", externSrc, got2, externWant)
	}
}
