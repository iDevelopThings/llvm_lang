package parser

import (
	"testing"

	"llvm_lang/src/lexer"
)

// TestOperatorDeclShape covers a struct declaring operator overloads in both
// forms - zero params (unary) and one param (binary) - see LANGUAGE.md's
// "Operator overloading" section and ast.Node's own StructDecl/OperatorDecl
// doc comments for the shapes asserted here: an OperatorDecl is [paramList,
// returnType, body], its own Tok the operator symbol itself (not the leading
// `operator` keyword), interspersed among a StructDecl's ordinary Field/
// ConstructorDecl/DestructorDecl children in declaration order.
func TestOperatorDeclShape(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "struct with zero operator overloads is unaffected",
			src:  "struct Vector2 {\n\tx f64\n}",
			want: "" +
				"StructDecl \"struct\"\n" +
				"  Ident \"Vector2\"\n" +
				"  <missing>\n" +
				"  Field\n" +
				"    Ident \"x\"\n" +
				"    Ident \"f64\"\n",
		},
		{
			name: "binary operator * overload",
			src: "struct Vector2 {\n" +
				"\tx f64\n\n" +
				"\toperator *(scalar f64) Vector2 {\n\t\treturn Vector2{this.x * scalar}\n\t}\n" +
				"}",
			want: "" +
				"StructDecl \"struct\"\n" +
				"  Ident \"Vector2\"\n" +
				"  <missing>\n" +
				"  Field\n" +
				"    Ident \"x\"\n" +
				"    Ident \"f64\"\n" +
				"  OperatorDecl \"*\"\n" +
				"    ParamList\n" +
				"      Param\n" +
				"        Ident \"scalar\"\n" +
				"        Ident \"f64\"\n" +
				"    Ident \"Vector2\"\n" +
				"    Block\n" +
				"      ReturnStmt \"return\"\n" +
				"        CompositeLit\n" +
				"          Ident \"Vector2\"\n" +
				"          BinaryExpr \"*\"\n" +
				"            MemberExpr \"x\"\n" +
				"              ThisExpr \"this\"\n" +
				"            Ident \"scalar\"\n",
		},
		{
			name: "unary operator - overload",
			src: "struct Vector2 {\n" +
				"\tx f64\n\n" +
				"\toperator -() Vector2 {\n\t\treturn Vector2{-this.x}\n\t}\n" +
				"}",
			want: "" +
				"StructDecl \"struct\"\n" +
				"  Ident \"Vector2\"\n" +
				"  <missing>\n" +
				"  Field\n" +
				"    Ident \"x\"\n" +
				"    Ident \"f64\"\n" +
				"  OperatorDecl \"-\"\n" +
				"    ParamList\n" +
				"    Ident \"Vector2\"\n" +
				"    Block\n" +
				"      ReturnStmt \"return\"\n" +
				"        CompositeLit\n" +
				"          Ident \"Vector2\"\n" +
				"          UnaryExpr \"-\"\n" +
				"            MemberExpr \"x\"\n" +
				"              ThisExpr \"this\"\n",
		},
		{
			name: "every binary token plus unary - coexisting on one struct",
			src: "struct Vector2 {\n" +
				"\tx f64\n\n" +
				"\toperator +(other Vector2) Vector2 {\n\t\treturn this\n\t}\n" +
				"\toperator -(other Vector2) Vector2 {\n\t\treturn this\n\t}\n" +
				"\toperator *(scalar f64) Vector2 {\n\t\treturn this\n\t}\n" +
				"\toperator /(scalar f64) Vector2 {\n\t\treturn this\n\t}\n" +
				"\toperator -() Vector2 {\n\t\treturn this\n\t}\n" +
				"}",
			want: "" +
				"StructDecl \"struct\"\n" +
				"  Ident \"Vector2\"\n" +
				"  <missing>\n" +
				"  Field\n" +
				"    Ident \"x\"\n" +
				"    Ident \"f64\"\n" +
				"  OperatorDecl \"+\"\n" +
				"    ParamList\n" +
				"      Param\n" +
				"        Ident \"other\"\n" +
				"        Ident \"Vector2\"\n" +
				"    Ident \"Vector2\"\n" +
				"    Block\n" +
				"      ReturnStmt \"return\"\n" +
				"        ThisExpr \"this\"\n" +
				"  OperatorDecl \"-\"\n" +
				"    ParamList\n" +
				"      Param\n" +
				"        Ident \"other\"\n" +
				"        Ident \"Vector2\"\n" +
				"    Ident \"Vector2\"\n" +
				"    Block\n" +
				"      ReturnStmt \"return\"\n" +
				"        ThisExpr \"this\"\n" +
				"  OperatorDecl \"*\"\n" +
				"    ParamList\n" +
				"      Param\n" +
				"        Ident \"scalar\"\n" +
				"        Ident \"f64\"\n" +
				"    Ident \"Vector2\"\n" +
				"    Block\n" +
				"      ReturnStmt \"return\"\n" +
				"        ThisExpr \"this\"\n" +
				"  OperatorDecl \"/\"\n" +
				"    ParamList\n" +
				"      Param\n" +
				"        Ident \"scalar\"\n" +
				"        Ident \"f64\"\n" +
				"    Ident \"Vector2\"\n" +
				"    Block\n" +
				"      ReturnStmt \"return\"\n" +
				"        ThisExpr \"this\"\n" +
				"  OperatorDecl \"-\"\n" +
				"    ParamList\n" +
				"    Ident \"Vector2\"\n" +
				"    Block\n" +
				"      ReturnStmt \"return\"\n" +
				"        ThisExpr \"this\"\n",
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

// TestOperatorDeclMissingOperatorSymbolIsError covers a genuinely malformed
// `operator` block - no operator symbol at all between the keyword and the
// parameter list - rejected with a clean, single diagnostic naming the
// problem (expectOperatorToken, decl.go), unlike a token that merely isn't
// one of this round's *supported* operators (e.g. `%` - see
// TestOperatorDeclUnsupportedTokenParsesCleanly below and
// sema.TestOperatorDeclUnsupportedTokenIsError, resolve_test.go): that
// narrower "only + - * / (binary) and - (unary) are overloadable" rule is
// sema's job, not the grammar's (see LANGUAGE.md's "Operator overloading"
// section).
func TestOperatorDeclMissingOperatorSymbolIsError(t *testing.T) {
	src := "struct Vector2 {\n" +
		"\tx f64\n\n" +
		"\toperator (v f64) Vector2 {\n\t\treturn this\n\t}\n" +
		"}\n" +
		"func main() {\n\tprint(1)\n}\n"

	file := lexer.NewFile("t.ll", src)
	tree, diags := ParseFile(file, false)
	if diags.ErrorCount() != 1 {
		t.Fatalf("ErrorCount = %d, want 1: %v", diags.ErrorCount(), diags.All())
	}
	if got := diags.All()[0].Msg; got != `expected an operator (+, -, *, /) after 'operator', found "("` {
		t.Errorf("diagnostic = %q, want a message naming the missing operator", got)
	}

	// The rest of the file still parses despite the one malformed block -
	// this feature must not take down the whole file's parse.
	decls := tree.Children(tree.Root)
	if len(decls) != 2 {
		t.Fatalf("top-level decls = %d, want 2 (StructDecl, FuncDecl): dump:\n%s", len(decls), tree.Dump(tree.Root))
	}
}

// TestOperatorDeclRecoversFromMalformedParam covers a malformed operator
// param (a missing parameter type) recovering reasonably rather than
// derailing the rest of the file's parse - the same "don't take down the
// whole file" bar every other malformed-construct test in this package
// already holds itself to (see malformed_test.go).
func TestOperatorDeclRecoversFromMalformedParam(t *testing.T) {
	src := "struct Vector2 {\n" +
		"\tx f64\n\n" +
		"\toperator *(scalar) Vector2 {\n\t\treturn this\n\t}\n" +
		"}\n" +
		"func main() {\n\tprint(1)\n}\n"

	tree, diags := ParseFile(lexer.NewFile("t.ll", src), false)
	if !diags.HasErrors() {
		t.Fatal("expected parse errors for a param with no declared type")
	}
	decls := tree.Children(tree.Root)
	if len(decls) != 2 {
		t.Fatalf("top-level decls = %d, want 2 (StructDecl, FuncDecl): dump:\n%s", len(decls), tree.Dump(tree.Root))
	}
}

// TestOperatorDeclUnsupportedTokenParsesCleanly covers the grammar's own
// deliberately broader acceptance: any single-token binary operator this
// language recognizes at expression position (here `%`, not part of this
// round's overloadable set - see LANGUAGE.md's "Operator overloading"
// section) parses as an ordinary OperatorDecl with zero parse errors -
// "is this legal to overload" is sema's own narrower question
// (declareOperator, resolve.go; see sema.TestOperatorDeclUnsupportedTokenIsError),
// not this grammar rule's, the same "grammar accepts the general shape,
// sema narrows it" split parseDestructorDecl's own empty-paramList rule
// already uses.
func TestOperatorDeclUnsupportedTokenParsesCleanly(t *testing.T) {
	src := "struct Vector2 {\n" +
		"\tx f64\n\n" +
		"\toperator %(v f64) f64 {\n\t\treturn v\n\t}\n" +
		"}"
	tree, n := parseDeclSrc(t, src)
	want := "" +
		"StructDecl \"struct\"\n" +
		"  Ident \"Vector2\"\n" +
		"  <missing>\n" +
		"  Field\n" +
		"    Ident \"x\"\n" +
		"    Ident \"f64\"\n" +
		"  OperatorDecl \"%\"\n" +
		"    ParamList\n" +
		"      Param\n" +
		"        Ident \"v\"\n" +
		"        Ident \"f64\"\n" +
		"    Ident \"f64\"\n" +
		"    Block\n" +
		"      ReturnStmt \"return\"\n" +
		"        Ident \"v\"\n"
	if got := tree.Dump(n); got != want {
		t.Errorf("Dump(%q):\n got:\n%s\nwant:\n%s", src, got, want)
	}
}
