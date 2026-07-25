package parser

import (
	"testing"

	"llvm_lang/src/ast"
	"llvm_lang/src/lexer"
)

// TestExternFuncDeclShape covers `extern func Name(params) RetType` (see
// LANGUAGE.md's "External functions (FFI)" section and ast.Node's own
// ExternFuncDecl doc comment for the [name, paramList, returnType] shape
// asserted here): no receiver, no body, an optional return type, and the
// exact same ParamList/Param grammar an ordinary FuncDecl's own paramList
// already uses (pointer-typed params included).
func TestExternFuncDeclShape(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "extern func with a param and return type",
			src:  "extern func Foo(x i32) bool",
			want: "" +
				"ExternFuncDecl \"extern\"\n" +
				"  Ident \"Foo\"\n" +
				"  ParamList\n" +
				"    Param\n" +
				"      Ident \"x\"\n" +
				"      Ident \"i32\"\n" +
				"  Ident \"bool\"\n",
		},
		{
			name: "extern func with no params and no return type",
			src:  "extern func Bar()",
			want: "" +
				"ExternFuncDecl \"extern\"\n" +
				"  Ident \"Bar\"\n" +
				"  ParamList\n" +
				"  <missing>\n",
		},
		{
			name: "extern func with multiple params",
			src:  "extern func Add(x i32, y i32) i32",
			want: "" +
				"ExternFuncDecl \"extern\"\n" +
				"  Ident \"Add\"\n" +
				"  ParamList\n" +
				"    Param\n" +
				"      Ident \"x\"\n" +
				"      Ident \"i32\"\n" +
				"    Param\n" +
				"      Ident \"y\"\n" +
				"      Ident \"i32\"\n" +
				"  Ident \"i32\"\n",
		},
		{
			name: "extern func with a pointer-typed param",
			src:  "extern func QueryPerformanceCounter(counter *i64) bool",
			want: "" +
				"ExternFuncDecl \"extern\"\n" +
				"  Ident \"QueryPerformanceCounter\"\n" +
				"  ParamList\n" +
				"    Param\n" +
				"      Ident \"counter\"\n" +
				"      PointerType\n" +
				"        Ident \"i64\"\n" +
				"  Ident \"bool\"\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tree, n := parseDeclSrc(t, tt.src)
			got := tree.Dump(n)
			if got != tt.want {
				t.Errorf("Dump(%q):\n got:\n%s\nwant:\n%s", tt.src, got, tt.want)
			}
		})
	}
}

// TestExternFuncDeclEndToEnd covers an extern declaration alongside an
// ordinary func at file scope, including the ASI rule that terminates one
// with no explicit semicolon (see lexer.asiEligible - `bool`/an Identifier
// with no keyword is ASI-eligible, exactly like any other type name) - the
// same ASI rule a type-less `var` already relies on for its own statement
// termination.
func TestExternFuncDeclEndToEnd(t *testing.T) {
	src := "extern func QueryPerformanceCounter(counter *i64) bool\n" +
		"func main() int {\n" +
		"\treturn 0\n" +
		"}\n"
	tree, diags := ParseFile(lexer.NewFile("t.ll", src), false)
	if diags.HasErrors() {
		t.Fatalf("unexpected parse errors: %v", diags.All())
	}
	decls := tree.Children(tree.Root)
	wantKinds := []string{"ExternFuncDecl", "FuncDecl"}
	if len(decls) != len(wantKinds) {
		t.Fatalf("File has %d top-level items, want %d:\n%s", len(decls), len(wantKinds), tree.Dump(tree.Root))
	}
	for i, want := range wantKinds {
		if got := tree.Nodes[decls[i]].Kind.String(); got != want {
			t.Errorf("decl[%d] kind = %s, want %s", i, got, want)
		}
	}
}

// TestExternFuncDeclNoReturnTypeEndToEnd covers the no-return-type case
// end to end - `)` is ASI-eligible (see lexer.asiEligible), so
// `extern func Bar(x i32)` alone on a line must terminate there, exactly
// like parseFuncDecl's own `{`-less lookahead decides "no return type" for
// an ordinary func.
func TestExternFuncDeclNoReturnTypeEndToEnd(t *testing.T) {
	src := "extern func Bar(x i32)\n" +
		"func main() int {\n" +
		"\treturn 0\n" +
		"}\n"
	tree, diags := ParseFile(lexer.NewFile("t.ll", src), false)
	if diags.HasErrors() {
		t.Fatalf("unexpected parse errors: %v", diags.All())
	}
	decls := tree.Children(tree.Root)
	if len(decls) != 2 {
		t.Fatalf("File has %d top-level items, want 2:\n%s", len(decls), tree.Dump(tree.Root))
	}
	externDecl := decls[0]
	if got := tree.Nodes[externDecl].Kind.String(); got != "ExternFuncDecl" {
		t.Fatalf("decl[0] kind = %s, want ExternFuncDecl", got)
	}
	if returnType := tree.ExternFuncReturnType(externDecl); returnType != ast.InvalidNode {
		t.Errorf("ExternFuncReturnType = %d, want ast.InvalidNode", returnType)
	}
}
