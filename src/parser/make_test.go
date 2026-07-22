package parser

import "testing"

// TestMakeCallArgumentGrammar covers make's own bespoke first-argument
// grammar (see expr.go's parseMakeArgs and LANGUAGE.md's "Dynamic arrays"
// section): unlike every other call in this language, make's first argument
// is a type expression (an ArrayType node), not an ordinary value
// expression - asserted here via Tree.Dump, the same table-driven precedence/
// shape style expr_test.go already uses.
func TestMakeCallArgumentGrammar(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "two-arg make",
			src:  "make([]int, 3)",
			want: "" +
				"CallExpr\n" +
				"  Ident \"make\"\n" +
				"  ArrayType\n" +
				"    <missing>\n" +
				"    Ident \"int\"\n" +
				"  NumberLit \"3\"\n",
		},
		{
			name: "three-arg make",
			src:  "make([]int, 3, 5)",
			want: "" +
				"CallExpr\n" +
				"  Ident \"make\"\n" +
				"  ArrayType\n" +
				"    <missing>\n" +
				"    Ident \"int\"\n" +
				"  NumberLit \"3\"\n" +
				"  NumberLit \"5\"\n",
		},
		{
			name: "make of a named struct type",
			src:  "make([]Point, n)",
			want: "" +
				"CallExpr\n" +
				"  Ident \"make\"\n" +
				"  ArrayType\n" +
				"    <missing>\n" +
				"    Ident \"Point\"\n" +
				"  Ident \"n\"\n",
		},
		{
			name: "make of a nested slice type",
			src:  "make([][]int, 2)",
			want: "" +
				"CallExpr\n" +
				"  Ident \"make\"\n" +
				"  ArrayType\n" +
				"    <missing>\n" +
				"    ArrayType\n" +
				"      <missing>\n" +
				"      Ident \"int\"\n" +
				"  NumberLit \"2\"\n",
		},
		{
			name: "make with an expression n argument",
			src:  "make([]int, n+1)",
			want: "" +
				"CallExpr\n" +
				"  Ident \"make\"\n" +
				"  ArrayType\n" +
				"    <missing>\n" +
				"    Ident \"int\"\n" +
				"  BinaryExpr \"+\"\n" +
				"    Ident \"n\"\n" +
				"    NumberLit \"1\"\n",
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

// TestMakeShadowedAsOrdinaryCallStillParses asserts a call whose callee isn't
// literally spelled "make" never takes make's special argument grammar -
// ordinary call parsing (arguments as plain value expressions) applies as
// always, so a same-shaped call to a differently-named function still
// parses as an ordinary CallExpr.
func TestMakeShadowedAsOrdinaryCallStillParses(t *testing.T) {
	tree, n := parseExprSrc(t, "notMake(a, b)")
	want := "" +
		"CallExpr\n" +
		"  Ident \"notMake\"\n" +
		"  Ident \"a\"\n" +
		"  Ident \"b\"\n"
	if got := tree.Dump(n); got != want {
		t.Errorf("Dump(%q):\n got:\n%s\nwant:\n%s", "notMake(a, b)", got, want)
	}
}

// TestMakeWithNoArguments covers the degenerate `make()` case - a syntax
// error further down the pipeline (sema rejects the wrong argument count),
// but the parser itself must still produce a well-formed tree rather than
// panicking on an empty argument list.
func TestMakeWithNoArguments(t *testing.T) {
	tree, n := parseExprSrc(t, "make()")
	want := "CallExpr\n  Ident \"make\"\n"
	if got := tree.Dump(n); got != want {
		t.Errorf("Dump(%q):\n got:\n%s\nwant:\n%s", "make()", got, want)
	}
}
