package parser

import (
	"testing"

	"llvm_lang/src/lexer"
)

// TestVariadicParamShape covers `...T` on a parameter list's own last
// parameter (see LANGUAGE.md's "Variadic parameters" section) - the `...`
// token is carried as the Param node's own Tok (ast.Node's Param doc
// comment), so Dump renders it inline exactly like an operator overload's
// own token already does.
func TestVariadicParamShape(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "sole variadic parameter",
			src:  "func Sum(nums ...int) int { return 0 }",
			want: "" +
				"FuncDecl \"func\"\n" +
				"  <missing>\n" +
				"  Ident \"Sum\"\n" +
				"  <missing>\n" +
				"  ParamList\n" +
				"    Param \"...\"\n" +
				"      Ident \"nums\"\n" +
				"      Ident \"int\"\n" +
				"  Ident \"int\"\n" +
				"  Block\n" +
				"    ReturnStmt \"return\"\n" +
				"      NumberLit \"0\"\n",
		},
		{
			name: "fixed leading parameter plus a variadic last one",
			src:  "func Join(sep string, parts ...string) string { return sep }",
			want: "" +
				"FuncDecl \"func\"\n" +
				"  <missing>\n" +
				"  Ident \"Join\"\n" +
				"  <missing>\n" +
				"  ParamList\n" +
				"    Param\n" +
				"      Ident \"sep\"\n" +
				"      Ident \"string\"\n" +
				"    Param \"...\"\n" +
				"      Ident \"parts\"\n" +
				"      Ident \"string\"\n" +
				"  Ident \"string\"\n" +
				"  Block\n" +
				"    ReturnStmt \"return\"\n" +
				"      Ident \"sep\"\n",
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

// TestVariadicParamOnlyLastIsRejected covers the structural "only the last
// parameter may be variadic" rule (LANGUAGE.md's "Variadic parameters"
// section): `...` on a non-last parameter still parses (the general shape),
// but is flagged with a clear diagnostic, not silently accepted.
func TestVariadicParamOnlyLastIsRejected(t *testing.T) {
	src := "func F(a ...int, b int) { }"
	p := New(lexer.NewFile("t.ll", src))
	p.parseTopLevelItem()
	if !p.diags.HasErrors() {
		t.Fatalf("expected a diagnostic for a non-last variadic parameter, got none")
	}
	found := false
	for _, d := range p.diags.All() {
		if d.Msg == "only the last parameter may be variadic" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected the 'only the last parameter may be variadic' diagnostic, got: %v", p.diags.All())
	}
}

// TestSpreadCallShape covers the trailing `...` spread form at a call
// expression's own last argument (`Join(",", parts...)` - see LANGUAGE.md's
// "Variadic parameters" section) - carried as the CallExpr node's own Tok,
// mirroring the variadic parameter's own Param-level marker.
func TestSpreadCallShape(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "spread as the sole argument",
			src:  "Join(parts...)",
			want: "" +
				"CallExpr \"...\"\n" +
				"  Ident \"Join\"\n" +
				"  Ident \"parts\"\n",
		},
		{
			name: "fixed leading argument plus a spread last one",
			src:  "Join(\",\", parts...)",
			want: "" +
				"CallExpr \"...\"\n" +
				"  Ident \"Join\"\n" +
				"  StringLit \"\\\",\\\"\"\n" +
				"  Ident \"parts\"\n",
		},
		{
			name: "an ordinary call with no spread has no CallExpr token",
			src:  "Join(\",\", \"a\", \"b\")",
			want: "" +
				"CallExpr\n" +
				"  Ident \"Join\"\n" +
				"  StringLit \"\\\",\\\"\"\n" +
				"  StringLit \"\\\"a\\\"\"\n" +
				"  StringLit \"\\\"b\\\"\"\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := New(lexer.NewFile("t.ll", tt.src))
			n := p.parseExpr(precLowest)
			if p.diags.HasErrors() {
				t.Fatalf("unexpected parse errors for %q: %v", tt.src, p.diags.All())
			}
			got := p.tree.Dump(n)
			if got != tt.want {
				t.Errorf("Dump(%q):\n got:\n%s\nwant:\n%s", tt.src, got, tt.want)
			}
		})
	}
}

// TestSpreadOnlyLegalOnLastArgumentIsRejected covers spread's own structural
// restriction: `...` right after a non-last argument (more arguments follow
// it) is a clean diagnostic, not silently accepted or misparsed.
func TestSpreadOnlyLegalOnLastArgumentIsRejected(t *testing.T) {
	src := "Join(parts..., \"extra\")"
	p := New(lexer.NewFile("t.ll", src))
	p.parseExpr(precLowest)
	if !p.diags.HasErrors() {
		t.Fatalf("expected a diagnostic for a non-last spread argument, got none")
	}
	found := false
	for _, d := range p.diags.All() {
		if d.Msg == "... (spread) is only legal after a call's last argument" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected the spread-position diagnostic, got: %v", p.diags.All())
	}
}
