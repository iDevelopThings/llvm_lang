package parser

import (
	"testing"

	"llvm_lang/src/lexer"
)

// This file covers this round's new `map[K]V` type-position grammar (see
// LANGUAGE.md's "Maps" section and ast.Node's own MapType doc comment) -
// table-driven Tree.Dump shape assertions, matching this package's
// established convention (see functype_test.go/pointer_test.go).

// TestMapTypeShape covers parsing precedence/nesting for map-type
// expressions: a plain map[string]int, a struct-keyed/valued map, a nested
// map value (map[K]map[K2]V2 - see LANGUAGE.md's own explicit note that this
// should just fall out for free from the general type-position grammar), and
// a map nested inside an array element type/vice versa.
func TestMapTypeShape(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "plain map[string]int",
			src:  "map[string]int",
			want: "" +
				"MapType\n" +
				"  Ident \"string\"\n" +
				"  Ident \"int\"\n",
		},
		{
			name: "struct key and value",
			src:  "map[Point]Point",
			want: "" +
				"MapType\n" +
				"  Ident \"Point\"\n" +
				"  Ident \"Point\"\n",
		},
		{
			name: "nested map value",
			src:  "map[string]map[string]int",
			want: "" +
				"MapType\n" +
				"  Ident \"string\"\n" +
				"  MapType\n" +
				"    Ident \"string\"\n" +
				"    Ident \"int\"\n",
		},
		{
			name: "map of slices",
			src:  "map[string][]int",
			want: "" +
				"MapType\n" +
				"  Ident \"string\"\n" +
				"  ArrayType\n" +
				"    <missing>\n" +
				"    Ident \"int\"\n",
		},
		{
			name: "array of maps",
			src:  "[3]map[string]int",
			want: "" +
				"ArrayType\n" +
				"  NumberLit \"3\"\n" +
				"  MapType\n" +
				"    Ident \"string\"\n" +
				"    Ident \"int\"\n",
		},
		{
			name: "pointer-keyed map",
			src:  "map[*int]bool",
			want: "" +
				"MapType\n" +
				"  PointerType\n" +
				"    Ident \"int\"\n" +
				"  Ident \"bool\"\n",
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

// TestMakeMapCallArgumentGrammar covers make's own bespoke argument grammar
// applied to a map type - `make(map[K]V)` - reusing the exact same
// parseMakeArgs path make_test.go's array-focused tests already cover, just
// asserting a MapType node in the first argument slot instead of an
// ArrayType one.
func TestMakeMapCallArgumentGrammar(t *testing.T) {
	tree, n := parseExprSrc(t, "make(map[string]int)")
	want := "" +
		"CallExpr\n" +
		"  Ident \"make\"\n" +
		"  MapType\n" +
		"    Ident \"string\"\n" +
		"    Ident \"int\"\n"
	if got := tree.Dump(n); got != want {
		t.Errorf("Dump(%q):\n got:\n%s\nwant:\n%s", "make(map[string]int)", got, want)
	}
}

// TestMapKeywordUnexpectedInExpressionPosition covers this round's explicit
// out-of-scope decision - there is no map composite-literal syntax
// (`map[string]int{...}`) - see LANGUAGE.md's "Maps" section: `map` used to
// *start* an expression (not a type position reached via make/a var
// annotation/etc.) has nowhere legal to go, since parseIdentExpr's own
// keyword switch has no case for it, so it falls to that switch's default
// "unexpected keyword" branch - a clean parse diagnostic, not a panic.
func TestMapKeywordUnexpectedInExpressionPosition(t *testing.T) {
	p := New(lexer.NewFile("t.ll", `x := map[string]int{"a": 1}`))
	p.parseStmt()
	if !p.diags.HasErrors() {
		t.Fatalf("expected a parse error for a bare map-literal expression, got none")
	}
}
