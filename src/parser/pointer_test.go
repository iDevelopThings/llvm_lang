package parser

import (
	"testing"

	"llvm_lang/src/lexer"
)

// TestPointerTypeShape covers parsing `*T` in type position (see
// LANGUAGE.md's "Pointers" section) - the pointer counterpart to
// functype_test.go's TestFuncTypeShape, same Tree.Dump-assertion style.
func TestPointerTypeShape(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "pointer to a builtin type",
			src:  "*int",
			want: "" +
				"PointerType\n" +
				"  Ident \"int\"\n",
		},
		{
			name: "pointer to a struct type",
			src:  "*Point",
			want: "" +
				"PointerType\n" +
				"  Ident \"Point\"\n",
		},
		{
			name: "pointer to a pointer",
			src:  "**int",
			want: "" +
				"PointerType\n" +
				"  PointerType\n" +
				"    Ident \"int\"\n",
		},
		{
			name: "pointer to a fixed-size array",
			src:  "*[3]int",
			want: "" +
				"PointerType\n" +
				"  ArrayType\n" +
				"    NumberLit \"3\"\n" +
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

// TestPointerVarDecl covers a `*T` type annotation in a real VarDecl, the
// same thin end-to-end wiring check TestFuncTypeInVarDeclAndParam performs
// for function types.
func TestPointerVarDecl(t *testing.T) {
	src := "var p *Point"
	want := "" +
		"VarDecl \"var\"\n" +
		"  Ident \"p\"\n" +
		"  PointerType\n" +
		"    Ident \"Point\"\n" +
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
}

// TestAddressOfAndDerefShape covers `&`/`*` as unary expression-level
// operators (see LANGUAGE.md's "Pointers" section) - both share their
// lexeme with an existing infix operator (bitwise `&`, multiply `*`), the
// same dual-role disambiguation unary `-` already has alongside binary `-`.
func TestAddressOfAndDerefShape(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "address-of a plain identifier",
			src:  "&x",
			want: "UnaryExpr \"&\"\n  Ident \"x\"\n",
		},
		{
			name: "dereference a plain identifier",
			src:  "*p",
			want: "UnaryExpr \"*\"\n  Ident \"p\"\n",
		},
		{
			name: "address-of binds tighter than binary +",
			src:  "&x + 1",
			want: "" +
				"BinaryExpr \"+\"\n" +
				"  UnaryExpr \"&\"\n" +
				"    Ident \"x\"\n" +
				"  NumberLit \"1\"\n",
		},
		{
			name: "dereference then member access chains through postfix",
			src:  "*p.x",
			want: "" +
				"UnaryExpr \"*\"\n" +
				"  MemberExpr \"x\"\n" +
				"    Ident \"p\"\n",
		},
		{
			name: "address-of a member expression",
			src:  "&p.x",
			want: "" +
				"UnaryExpr \"&\"\n" +
				"  MemberExpr \"x\"\n" +
				"    Ident \"p\"\n",
		},
		{
			name: "double dereference",
			src:  "**pp",
			want: "" +
				"UnaryExpr \"*\"\n" +
				"  UnaryExpr \"*\"\n" +
				"    Ident \"pp\"\n",
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

// TestNewExprShape covers `new T(args)` and `new T{...}` (see LANGUAGE.md's
// "Pointers" section) - `new` wraps an ordinary, already-legal constructor-
// call or composite-literal expression unchanged.
func TestNewExprShape(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "new wrapping a constructor call",
			src:  "new Point(1, 2)",
			want: "" +
				"NewExpr \"new\"\n" +
				"  CallExpr\n" +
				"    Ident \"Point\"\n" +
				"    NumberLit \"1\"\n" +
				"    NumberLit \"2\"\n",
		},
		{
			name: "new wrapping a struct composite literal",
			src:  "new Point{1, 2}",
			want: "" +
				"NewExpr \"new\"\n" +
				"  CompositeLit\n" +
				"    Ident \"Point\"\n" +
				"    NumberLit \"1\"\n" +
				"    NumberLit \"2\"\n",
		},
		{
			name: "new wrapping an array composite literal",
			src:  "new [3]int{1, 2, 3}",
			want: "" +
				"NewExpr \"new\"\n" +
				"  CompositeLit\n" +
				"    ArrayType\n" +
				"      NumberLit \"3\"\n" +
				"      Ident \"int\"\n" +
				"    NumberLit \"1\"\n" +
				"    NumberLit \"2\"\n" +
				"    NumberLit \"3\"\n",
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

// TestDeleteStmtShape covers `delete p` (see LANGUAGE.md's "Pointers"
// section) - its own dedicated statement form, same as break/continue.
func TestDeleteStmtShape(t *testing.T) {
	src := "delete p"
	want := "DeleteStmt \"delete\"\n  Ident \"p\"\n"

	tree, n := parseStmtSrc(t, src)
	got := tree.Dump(n)
	if got != want {
		t.Errorf("Dump(%q):\n got:\n%s\nwant:\n%s", src, got, want)
	}
}

// TestDerefAssignTarget covers `*p = v` parsing as a legal assignment
// target (see checkAssignTarget's UnaryExpr case).
func TestDerefAssignTarget(t *testing.T) {
	src := "*p = 5"
	want := "" +
		"AssignStmt \"=\"\n" +
		"  UnaryExpr \"*\"\n" +
		"    Ident \"p\"\n" +
		"  NumberLit \"5\"\n"

	tree, n := parseStmtSrc(t, src)
	got := tree.Dump(n)
	if got != want {
		t.Errorf("Dump(%q):\n got:\n%s\nwant:\n%s", src, got, want)
	}
}
