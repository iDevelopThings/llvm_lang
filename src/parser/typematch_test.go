package parser

import (
	"strings"
	"testing"

	"llvm_lang/src/lexer"
)

// This file covers the type-pattern grammar a match arm gains for an Any
// subject (see LANGUAGE.md's "Type matching" section and
// parseMatchArmPattern) - table-driven Tree.Dump shape assertions, matching
// this package's established convention.

// dumpMatchArm parses src (one statement-position match) and dumps its i'th
// arm.
func dumpMatchArm(t *testing.T, src string, i int) string {
	t.Helper()
	p := New(lexer.NewFile("t.ll", "func f() {\n"+src+"\n}"))
	n := p.parseTopLevelItem()
	if p.diags.HasErrors() {
		t.Fatalf("unexpected parse errors for %q: %v", src, p.diags.All())
	}
	matchStmt := p.tree.Child(p.tree.FuncBody(n), 0)
	return p.tree.Dump(p.tree.MatchArms(matchStmt)[i])
}

// TestTypeMatchArmPatternShapes covers every type-pattern surface form: a
// `name Type` binding (nominal, generic, pointer, map), a bare type that
// needs no wrapper node, and the shapes that stay ordinary expressions.
func TestTypeMatchArmPatternShapes(t *testing.T) {
	tests := []struct {
		name string
		arm  string
		want string
	}{
		{
			name: "binding on a primitive",
			arm:  "v int => { }",
			want: "" +
				"MatchArm\n" +
				"  TypePattern\n" +
				"    Ident \"v\"\n" +
				"    Ident \"int\"\n" +
				"  Block\n",
		},
		{
			// `name *Type` (both bare identifiers) is genuinely ambiguous
			// with multiplication at the grammar level (see
			// parseMatchArmPattern's own doc comment) - it parses as an
			// ordinary BinaryExpr like `x * y` always has, and it's sema
			// (via Info.Refs, following Resolve's own lexical peek), not the
			// parser, that later decides this is a pointer-type binding
			// rather than a value-match multiplication.
			name: "binding on a pointer type stays an ordinary BinaryExpr",
			arm:  "v *Point => { }",
			want: "" +
				"MatchArm\n" +
				"  BinaryExpr \"*\"\n" +
				"    Ident \"v\"\n" +
				"    Ident \"Point\"\n" +
				"  Block\n",
		},
		{
			name: "binding on a generic instantiation",
			arm:  "v Pair[int, string] => { }",
			want: "" +
				"MatchArm\n" +
				"  TypePattern\n" +
				"    Ident \"v\"\n" +
				"    IndexExpr\n" +
				"      Ident \"Pair\"\n" +
				"      TypeArgList\n" +
				"        Ident \"int\"\n" +
				"        Ident \"string\"\n" +
				"  Block\n",
		},
		{
			name: "binding on a map type",
			arm:  "v map[string]int => { }",
			want: "" +
				"MatchArm\n" +
				"  TypePattern\n" +
				"    Ident \"v\"\n" +
				"    MapType\n" +
				"      Ident \"string\"\n" +
				"      Ident \"int\"\n" +
				"  Block\n",
		},
		{
			name: "binding on a package-qualified type",
			arm:  "v pkg.Point => { }",
			want: "" +
				"MatchArm\n" +
				"  TypePattern\n" +
				"    Ident \"v\"\n" +
				"    MemberExpr \"Point\"\n" +
				"      Ident \"pkg\"\n" +
				"  Block\n",
		},
		{
			name: "bare nominal type stays a plain Ident",
			arm:  "Point => { }",
			want: "" +
				"MatchArm\n" +
				"  Ident \"Point\"\n" +
				"  Block\n",
		},
		{
			name: "binding on a dynamic array type",
			arm:  "v []int => { }",
			want: "" +
				"MatchArm\n" +
				"  TypePattern\n" +
				"    Ident \"v\"\n" +
				"    ArrayType\n" +
				"      <missing>\n" +
				"      Ident \"int\"\n" +
				"  Block\n",
		},
		{
			name: "bare dynamic array type",
			arm:  "[]int => { }",
			want: "" +
				"MatchArm\n" +
				"  ArrayType\n" +
				"    <missing>\n" +
				"    Ident \"int\"\n" +
				"  Block\n",
		},
		{
			name: "bare fixed array type",
			arm:  "[3]int => { }",
			want: "" +
				"MatchArm\n" +
				"  ArrayType\n" +
				"    NumberLit \"3\"\n" +
				"    Ident \"int\"\n" +
				"  Block\n",
		},
		{
			name: "bare pointer type",
			arm:  "*Point => { }",
			want: "" +
				"MatchArm\n" +
				"  PointerType\n" +
				"    Ident \"Point\"\n" +
				"  Block\n",
		},
		{
			name: "bare map type",
			arm:  "map[string]int => { }",
			want: "" +
				"MatchArm\n" +
				"  MapType\n" +
				"    Ident \"string\"\n" +
				"    Ident \"int\"\n" +
				"  Block\n",
		},
		{
			name: "wildcard is untouched",
			arm:  "_ => { }",
			want: "" +
				"MatchArm\n" +
				"  Ident \"_\"\n" +
				"  Block\n",
		},
		{
			name: "bare generic instantiation stays an IndexExpr",
			arm:  "Pair[int, string] => { }",
			want: "" +
				"MatchArm\n" +
				"  IndexExpr\n" +
				"    Ident \"Pair\"\n" +
				"    TypeArgList\n" +
				"      Ident \"int\"\n" +
				"      Ident \"string\"\n" +
				"  Block\n",
		},
		{
			name: "an index expression is still a value pattern",
			arm:  "arr[0] => { }",
			want: "" +
				"MatchArm\n" +
				"  IndexExpr\n" +
				"    Ident \"arr\"\n" +
				"    NumberLit \"0\"\n" +
				"  Block\n",
		},
		{
			name: "an enum variant pattern is untouched",
			arm:  "Shape.Circle(r) => { }",
			want: "" +
				"MatchArm\n" +
				"  CallExpr\n" +
				"    MemberExpr \"Circle\"\n" +
				"      Ident \"Shape\"\n" +
				"    Ident \"r\"\n" +
				"  Block\n",
		},
		{
			name: "an array literal is still a value pattern",
			arm:  "[3]int{1, 2, 3} => { }",
			want: "" +
				"MatchArm\n" +
				"  CompositeLit\n" +
				"    ArrayType\n" +
				"      NumberLit \"3\"\n" +
				"      Ident \"int\"\n" +
				"    NumberLit \"1\"\n" +
				"    NumberLit \"2\"\n" +
				"    NumberLit \"3\"\n" +
				"  Block\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := dumpMatchArm(t, "match a {\n"+tt.arm+"\n}", 0)
			if got != tt.want {
				t.Errorf("Dump(%q):\n got:\n%s\nwant:\n%s", tt.arm, got, tt.want)
			}
		})
	}
}

// TestTypeMatchExprArmPatternShapes proves the expression-position arm
// grammar shares the exact same pattern list (parseMatchArmPatterns), rather
// than a second copy that could drift.
func TestTypeMatchExprArmPatternShapes(t *testing.T) {
	src := `func f() {
	x := match a {
		v int => 1
		[]int => 2
		_ => 3
	}
}`
	p := New(lexer.NewFile("t.ll", src))
	n := p.parseTopLevelItem()
	if p.diags.HasErrors() {
		t.Fatalf("unexpected parse errors: %v", p.diags.All())
	}
	matchExpr := p.tree.Child(p.tree.Child(p.tree.FuncBody(n), 0), 1)
	arms := p.tree.MatchArms(matchExpr)
	if len(arms) != 3 {
		t.Fatalf("len(arms) = %d, want 3", len(arms))
	}
	for i, want := range []string{"TypePattern", "ArrayType", "Ident"} {
		got := p.tree.Nodes[p.tree.MatchArmPattern(arms[i])].Kind.String()
		if got != want {
			t.Errorf("arm %d pattern kind = %s, want %s", i, got, want)
		}
	}
}

// TestTypeMatchMultiPatternArmParses proves the grammar itself accepts a
// comma-separated type-pattern list - rejecting it is sema's job (see
// checkTypeMatchStmt), exactly like the enum-match arm restriction.
func TestTypeMatchMultiPatternArmParses(t *testing.T) {
	got := dumpMatchArm(t, "match a {\nint, string => { }\n_ => { }\n}", 0)
	want := "" +
		"MatchArm\n" +
		"  Ident \"int\"\n" +
		"  Ident \"string\"\n" +
		"  Block\n"
	if got != want {
		t.Errorf("Dump():\n got:\n%s\nwant:\n%s", got, want)
	}
}

// TestTypeMatchBrokenPatternRecovers covers the malformed-source path: a
// binding with no type after it must report a syntax error rather than
// crash or silently swallow the arm (see malformed_test.go's own convention).
func TestTypeMatchBrokenPatternRecovers(t *testing.T) {
	src := `func f() {
	match a {
		v * => { }
		_ => { }
	}
}`
	p := New(lexer.NewFile("t.ll", src))
	p.parseTopLevelItem()
	if !p.diags.HasErrors() {
		t.Fatalf("expected a parse error for a type pattern with no type")
	}
	if got := p.diags.All()[0].Msg; !strings.Contains(got, "expected") {
		t.Errorf("diagnostic = %q, want an \"expected ...\" syntax error", got)
	}
}

// TestValueMatchMultiplicationPatternStaysOrdinary is a regression test for
// this round's own `name *Type` grammar (see parseMatchArmPattern's doc
// comment): an ordinary value-match pattern shaped like `x * y` must still
// parse as plain multiplication, not get swallowed into the new pointer-
// type-pattern reading. Every RHS shape other than a bare identifier was
// never actually ambiguous in the first place (the Pratt parser's own
// postfix chaining already resolves a call/parenthesized/other expression to
// something other than a plain Ident before returning) - covered here
// alongside the one genuinely ambiguous case (two bare identifiers) so a
// future change to this area can't silently narrow back to only handling
// that one shape.
func TestValueMatchMultiplicationPatternStaysOrdinary(t *testing.T) {
	tests := []struct {
		name string
		arm  string
		want string
	}{
		{
			name: "two bare identifiers",
			arm:  "x * y => { }",
			want: "" +
				"MatchArm\n" +
				"  BinaryExpr \"*\"\n" +
				"    Ident \"x\"\n" +
				"    Ident \"y\"\n" +
				"  Block\n",
		},
		{
			name: "identifier times a call",
			arm:  "x * g() => { }",
			want: "" +
				"MatchArm\n" +
				"  BinaryExpr \"*\"\n" +
				"    Ident \"x\"\n" +
				"    CallExpr\n" +
				"      Ident \"g\"\n" +
				"  Block\n",
		},
		{
			name: "identifier times a parenthesized expression",
			arm:  "x * (y + 1) => { }",
			want: "" +
				"MatchArm\n" +
				"  BinaryExpr \"*\"\n" +
				"    Ident \"x\"\n" +
				"    ParenExpr\n" +
				"      BinaryExpr \"+\"\n" +
				"        Ident \"y\"\n" +
				"        NumberLit \"1\"\n" +
				"  Block\n",
		},
		{
			name: "identifier times a literal",
			arm:  "x * 2 => { }",
			want: "" +
				"MatchArm\n" +
				"  BinaryExpr \"*\"\n" +
				"    Ident \"x\"\n" +
				"    NumberLit \"2\"\n" +
				"  Block\n",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := dumpMatchArm(t, "match a {\n"+tc.arm+"\n_ => { }\n}", 0)
			if got != tc.want {
				t.Errorf("Dump():\n got:\n%s\nwant:\n%s", got, tc.want)
			}
		})
	}
}
