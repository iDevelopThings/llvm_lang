package parser

import (
	"testing"
)

// TestGenericDeclShape covers the two new declaration slots (see ast.Node's
// own FuncDecl/StructDecl/TypeParamList doc comments): a FuncDecl's own
// TypeParamList between its name and its params, a StructDecl's between its
// name and its members, and a generic receiver clause - which is itself a
// TypeParamList whose Tok is the receiver type's name, so Tree.Text still
// reads "SlotMap" exactly as it does for the plain `(SlotMap)` form.
func TestGenericDeclShape(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "generic func with one type parameter",
			src:  "func Id[T](a T) T { return a }",
			want: "" +
				"FuncDecl \"func\"\n" +
				"  <missing>\n" +
				"  Ident \"Id\"\n" +
				"  TypeParamList\n" +
				"    Ident \"T\"\n" +
				"  ParamList\n" +
				"    Param\n" +
				"      Ident \"a\"\n" +
				"      Ident \"T\"\n" +
				"  Ident \"T\"\n" +
				"  Block\n" +
				"    ReturnStmt \"return\"\n" +
				"      Ident \"a\"\n",
		},
		{
			name: "generic func with two type parameters",
			src:  "func Pair[A, B](a A, b B) A { return a }",
			want: "" +
				"FuncDecl \"func\"\n" +
				"  <missing>\n" +
				"  Ident \"Pair\"\n" +
				"  TypeParamList\n" +
				"    Ident \"A\"\n" +
				"    Ident \"B\"\n" +
				"  ParamList\n" +
				"    Param\n" +
				"      Ident \"a\"\n" +
				"      Ident \"A\"\n" +
				"    Param\n" +
				"      Ident \"b\"\n" +
				"      Ident \"B\"\n" +
				"  Ident \"A\"\n" +
				"  Block\n" +
				"    ReturnStmt \"return\"\n" +
				"      Ident \"a\"\n",
		},
		{
			name: "generic struct",
			src:  "struct Box[T] { value T }",
			want: "" +
				"StructDecl \"struct\"\n" +
				"  Ident \"Box\"\n" +
				"  TypeParamList\n" +
				"    Ident \"T\"\n" +
				"  Field\n" +
				"    Ident \"value\"\n" +
				"    Ident \"T\"\n",
		},
		{
			name: "generic receiver clause carries the type name in its own Tok",
			src:  "func (Box[T]) Get() T { return this.value }",
			want: "" +
				"FuncDecl \"func\"\n" +
				"  TypeParamList \"Box\"\n" +
				"    Ident \"T\"\n" +
				"  Ident \"Get\"\n" +
				"  <missing>\n" +
				"  ParamList\n" +
				"  Ident \"T\"\n" +
				"  Block\n" +
				"    ReturnStmt \"return\"\n" +
				"      MemberExpr \"value\"\n" +
				"        ThisExpr \"this\"\n",
		},
		{
			name: "method with both a receiver and its own type parameter",
			src:  "func (Box[T]) Map[U](f U) T { return this.value }",
			want: "" +
				"FuncDecl \"func\"\n" +
				"  TypeParamList \"Box\"\n" +
				"    Ident \"T\"\n" +
				"  Ident \"Map\"\n" +
				"  TypeParamList\n" +
				"    Ident \"U\"\n" +
				"  ParamList\n" +
				"    Param\n" +
				"      Ident \"f\"\n" +
				"      Ident \"U\"\n" +
				"  Ident \"T\"\n" +
				"  Block\n" +
				"    ReturnStmt \"return\"\n" +
				"      MemberExpr \"value\"\n" +
				"        ThisExpr \"this\"\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tree, n := parseDeclSrc(t, tt.src)
			if got := tree.Dump(n); got != tt.want {
				t.Errorf("Dump(%q):\n got:\n%s\nwant:\n%s", tt.src, got, tt.want)
			}
		})
	}
}

// TestGenericInstantiationExprShape covers the instantiation side, which
// deliberately reuses IndexExpr rather than a node kind of its own: a single
// type argument keeps IndexExpr's plain [target, index] shape, a
// comma-separated list wraps its arguments in a TypeArgList. Telling either
// apart from real indexing is sema's job, not this grammar's.
func TestGenericInstantiationExprShape(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "explicit single type argument on a call",
			src:  "func f() { Id[int](1) }",
			want: "" +
				"CallExpr\n" +
				"  IndexExpr\n" +
				"    Ident \"Id\"\n" +
				"    Ident \"int\"\n" +
				"  NumberLit \"1\"\n",
		},
		{
			name: "two type arguments wrap in a TypeArgList",
			src:  "func f() { Pair[int, string](1, \"a\") }",
			want: "" +
				"CallExpr\n" +
				"  IndexExpr\n" +
				"    Ident \"Pair\"\n" +
				"    TypeArgList\n" +
				"      Ident \"int\"\n" +
				"      Ident \"string\"\n" +
				"  NumberLit \"1\"\n" +
				"  StringLit \"\\\"a\\\"\"\n",
		},
		{
			name: "generic composite literal",
			src:  "func f() { b := Box[int]{1} }",
			want: "" +
				"CompositeLit\n" +
				"  IndexExpr\n" +
				"    Ident \"Box\"\n" +
				"    Ident \"int\"\n" +
				"  NumberLit \"1\"\n",
		},
		{
			name: "ordinary indexing is completely unchanged",
			src:  "func f() { a[0] }",
			want: "" +
				"IndexExpr\n" +
				"  Ident \"a\"\n" +
				"  NumberLit \"0\"\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tree, decl := parseDeclSrc(t, tt.src)
			stmt := tree.Child(tree.FuncBody(decl), 0)
			expr := tree.Child(stmt, 0)
			if tree.Nodes[stmt].Kind.String() == "ShortVarDecl" {
				expr = tree.Child(stmt, 1)
			}
			if got := tree.Dump(expr); got != tt.want {
				t.Errorf("Dump(%q):\n got:\n%s\nwant:\n%s", tt.src, got, tt.want)
			}
		})
	}
}

// TestGenericTypePositionShape covers `Name[args]` reached through
// parseTypeExpr (a var's annotation, a field's type, an element type) rather
// than through the Pratt loop - both produce the identical IndexExpr shape.
func TestGenericTypePositionShape(t *testing.T) {
	tree, decl := parseDeclSrc(t, "func f() { var b Box[int] }")
	varDecl := tree.Child(tree.FuncBody(decl), 0)
	want := "" +
		"IndexExpr\n" +
		"  Ident \"Box\"\n" +
		"  Ident \"int\"\n"
	if got := tree.Dump(tree.Child(varDecl, 1)); got != want {
		t.Errorf("Dump(type):\n got:\n%s\nwant:\n%s", got, want)
	}
}
