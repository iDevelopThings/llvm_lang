package parser

import (
	"testing"

	"llvm_lang/src/lexer"
)

// TestFuncLitShape covers parsing a function-literal expression
// (`func(params) [returnType] { body }`) - the new expression-position
// grammar rule LANGUAGE.md's "Lambdas" section adds, mirroring
// TestFuncTypeShape's own coverage of the sibling type-position grammar
// (parseFuncType).
func TestFuncLitShape(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "no params, no return type, empty body",
			src:  "func() {}",
			want: "" +
				"FuncLit \"func\"\n" +
				"  ParamList\n" +
				"  <missing>\n" +
				"  Block\n",
		},
		{
			name: "one param, return type, a body statement",
			src:  "func(x int) int { return x }",
			want: "" +
				"FuncLit \"func\"\n" +
				"  ParamList\n" +
				"    Param\n" +
				"      Ident \"x\"\n" +
				"      Ident \"int\"\n" +
				"  Ident \"int\"\n" +
				"  Block\n" +
				"    ReturnStmt \"return\"\n" +
				"      Ident \"x\"\n",
		},
		{
			name: "two params, no return type",
			src:  "func(a int, b int) { a = b }",
			want: "" +
				"FuncLit \"func\"\n" +
				"  ParamList\n" +
				"    Param\n" +
				"      Ident \"a\"\n" +
				"      Ident \"int\"\n" +
				"    Param\n" +
				"      Ident \"b\"\n" +
				"      Ident \"int\"\n" +
				"  <missing>\n" +
				"  Block\n" +
				"    AssignStmt \"=\"\n" +
				"      Ident \"a\"\n" +
				"      Ident \"b\"\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tree, n := parseExprSrc(t, tt.src)
			got := tree.Dump(n)
			if got != tt.want {
				t.Errorf("Dump(%q):\n got:\n%s\nwant:\n%s", tt.src, got, tt.want)
			}
		})
	}
}

// TestImmediatelyInvokedFuncLit covers calling a function literal directly,
// with no intermediate variable - the same postfix-call infix rule
// (parseCallExpr) chains straight off a FuncLit prefix result exactly like it
// does off any other expression, needing no grammar change of its own.
func TestImmediatelyInvokedFuncLit(t *testing.T) {
	src := "(func() int { return 42 })()"
	want := "" +
		"CallExpr\n" +
		"  ParenExpr\n" +
		"    FuncLit \"func\"\n" +
		"      ParamList\n" +
		"      Ident \"int\"\n" +
		"      Block\n" +
		"        ReturnStmt \"return\"\n" +
		"          NumberLit \"42\"\n"
	tree, n := parseExprSrc(t, src)
	got := tree.Dump(n)
	if got != want {
		t.Errorf("Dump(%q):\n got:\n%s\nwant:\n%s", src, got, want)
	}
}

// TestFuncLitAsShortVarDeclValue covers the realistic, most common use of a
// FuncLit - assigning it to a name via `:=` - the shape a closure-capturing
// program (LANGUAGE.md's "Lambdas" section) actually uses.
func TestFuncLitAsShortVarDeclValue(t *testing.T) {
	src := "increment := func() int {\n\tcount = count + 1\n\treturn count\n}"
	want := "" +
		"ShortVarDecl \":=\"\n" +
		"  Ident \"increment\"\n" +
		"  FuncLit \"func\"\n" +
		"    ParamList\n" +
		"    Ident \"int\"\n" +
		"    Block\n" +
		"      AssignStmt \"=\"\n" +
		"        Ident \"count\"\n" +
		"        BinaryExpr \"+\"\n" +
		"          Ident \"count\"\n" +
		"          NumberLit \"1\"\n" +
		"      ReturnStmt \"return\"\n" +
		"        Ident \"count\"\n"
	p := New(lexer.NewFile("t.ll", src))
	n := p.parseSimpleStmt()
	if p.diags.HasErrors() {
		t.Fatalf("unexpected parse errors for %q: %v", src, p.diags.All())
	}
	got := p.tree.Dump(n)
	if got != want {
		t.Errorf("Dump(%q):\n got:\n%s\nwant:\n%s", src, got, want)
	}
}
