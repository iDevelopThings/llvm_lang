package parser

import (
	"testing"

	"llvm_lang/src/ast"
	"llvm_lang/src/lexer"
)

func TestEnumDeclShape(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "unit, tuple, and struct variants",
			src:  "enum Shape { Point, Circle(f64), Triangle { base f64, height f64 } }",
			want: "" +
				"EnumDecl \"enum\"\n" +
				"  Ident \"Shape\"\n" +
				"  EnumVariant \"Point\"\n" +
				"  EnumVariant \"Circle\"\n" +
				"    Ident \"f64\"\n" +
				"  EnumVariant \"Triangle\"\n" +
				"    Field\n" +
				"      Ident \"base\"\n" +
				"      Ident \"f64\"\n" +
				"    Field\n" +
				"      Ident \"height\"\n" +
				"      Ident \"f64\"\n",
		},
		{
			name: "tuple variant with multiple associated types",
			src:  "enum Result { Ok(int), Err(string, int) }",
			want: "" +
				"EnumDecl \"enum\"\n" +
				"  Ident \"Result\"\n" +
				"  EnumVariant \"Ok\"\n" +
				"    Ident \"int\"\n" +
				"  EnumVariant \"Err\"\n" +
				"    Ident \"string\"\n" +
				"    Ident \"int\"\n",
		},
		{
			name: "recursive variant via pointer type",
			src:  "enum List { Cons(int, *List), Nil }",
			want: "" +
				"EnumDecl \"enum\"\n" +
				"  Ident \"List\"\n" +
				"  EnumVariant \"Cons\"\n" +
				"    Ident \"int\"\n" +
				"    PointerType\n" +
				"      Ident \"List\"\n" +
				"  EnumVariant \"Nil\"\n",
		},
		{
			name: "with a destructor",
			src:  "enum Resource { Owned(int), destructor() { } }",
			want: "" +
				"EnumDecl \"enum\"\n" +
				"  Ident \"Resource\"\n" +
				"  EnumVariant \"Owned\"\n" +
				"    Ident \"int\"\n" +
				"  DestructorDecl \"destructor\"\n" +
				"    ParamList\n" +
				"    Block\n",
		},
		{
			name: "one variant per line, no trailing commas (ASI tolerance)",
			src: "enum Shape {\n" +
				"\tPoint\n" +
				"\tCircle(f64)\n" +
				"}",
			want: "" +
				"EnumDecl \"enum\"\n" +
				"  Ident \"Shape\"\n" +
				"  EnumVariant \"Point\"\n" +
				"  EnumVariant \"Circle\"\n" +
				"    Ident \"f64\"\n",
		},
		{
			name: "trailing comma after every variant, one per line",
			src: "enum Shape {\n" +
				"\tPoint,\n" +
				"\tCircle(f64),\n" +
				"}",
			want: "" +
				"EnumDecl \"enum\"\n" +
				"  Ident \"Shape\"\n" +
				"  EnumVariant \"Point\"\n" +
				"  EnumVariant \"Circle\"\n" +
				"    Ident \"f64\"\n",
		},
		{
			name: "struct variant fields one per line, no trailing commas",
			src: "enum Shape {\n" +
				"\tTriangle {\n" +
				"\t\tbase f64\n" +
				"\t\theight f64\n" +
				"\t}\n" +
				"}",
			want: "" +
				"EnumDecl \"enum\"\n" +
				"  Ident \"Shape\"\n" +
				"  EnumVariant \"Triangle\"\n" +
				"    Field\n" +
				"      Ident \"base\"\n" +
				"      Ident \"f64\"\n" +
				"    Field\n" +
				"      Ident \"height\"\n" +
				"      Ident \"f64\"\n",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := New(lexer.NewFile("t.ll", tc.src))
			n := p.parseTopLevelItem()
			if p.diags.HasErrors() {
				t.Fatalf("unexpected parse errors for %q: %v", tc.src, p.diags.All())
			}
			if got := p.tree.Dump(n); got != tc.want {
				t.Errorf("Dump() =\n%s\nwant:\n%s", got, tc.want)
			}
		})
	}
}

func TestEnumVariantClassification(t *testing.T) {
	src := "enum Shape { Point, Circle(f64), Triangle { base f64 } }"
	p := New(lexer.NewFile("t.ll", src))
	n := p.parseTopLevelItem()
	if p.diags.HasErrors() {
		t.Fatalf("unexpected parse errors: %v", p.diags.All())
	}
	variants := p.tree.EnumVariants(n)
	if len(variants) != 3 {
		t.Fatalf("len(EnumVariants) = %d, want 3", len(variants))
	}
	want := []ast.EnumVariantKind{ast.EnumVariantUnit, ast.EnumVariantTuple, ast.EnumVariantStruct}
	for i, v := range variants {
		if got := p.tree.ClassifyEnumVariant(v); got != want[i] {
			t.Errorf("variant %d: ClassifyEnumVariant = %v, want %v", i, got, want[i])
		}
	}
}

func TestMatchStmtShape(t *testing.T) {
	src := `func f() {
	match shape {
		Shape.Circle(r) => {
			print(r)
		}
		Shape.Triangle{base: b, height: h} => {
			print(b)
		}
		Shape.Point => {
		}
		_ => {
		}
	}
}`
	p := New(lexer.NewFile("t.ll", src))
	n := p.parseTopLevelItem()
	if p.diags.HasErrors() {
		t.Fatalf("unexpected parse errors: %v", p.diags.All())
	}
	body := p.tree.FuncBody(n)
	matchStmt := p.tree.Child(body, 0)
	if p.tree.Nodes[matchStmt].Kind.String() != "MatchStmt" {
		t.Fatalf("first statement kind = %s, want MatchStmt", p.tree.Nodes[matchStmt].Kind)
	}

	subject := p.tree.MatchSubject(matchStmt)
	if p.tree.Text(subject) != "shape" {
		t.Errorf("subject = %q, want shape", p.tree.Text(subject))
	}

	arms := p.tree.MatchArms(matchStmt)
	if len(arms) != 4 {
		t.Fatalf("len(arms) = %d, want 4", len(arms))
	}

	// Arm 0: Shape.Circle(r) - a CallExpr pattern.
	pat0 := p.tree.MatchArmPattern(arms[0])
	if kind := p.tree.Nodes[pat0].Kind.String(); kind != "CallExpr" {
		t.Errorf("arm 0 pattern kind = %s, want CallExpr", kind)
	}

	// Arm 1: Shape.Triangle{base: b, height: h} - a CompositeLit pattern.
	pat1 := p.tree.MatchArmPattern(arms[1])
	if kind := p.tree.Nodes[pat1].Kind.String(); kind != "CompositeLit" {
		t.Errorf("arm 1 pattern kind = %s, want CompositeLit", kind)
	}

	// Arm 2: Shape.Point - a bare MemberExpr pattern.
	pat2 := p.tree.MatchArmPattern(arms[2])
	if kind := p.tree.Nodes[pat2].Kind.String(); kind != "MemberExpr" {
		t.Errorf("arm 2 pattern kind = %s, want MemberExpr", kind)
	}

	// Arm 3: _ - a wildcard Ident pattern.
	pat3 := p.tree.MatchArmPattern(arms[3])
	if kind := p.tree.Nodes[pat3].Kind.String(); kind != "Ident" || p.tree.Text(pat3) != "_" {
		t.Errorf("arm 3 pattern = %s %q, want Ident \"_\"", kind, p.tree.Text(pat3))
	}
}

func TestMatchStmtSubjectCompositeLitAmbiguityGuard(t *testing.T) {
	// The subject expression must not swallow the arm list's own opening
	// brace as if it were a composite literal - same escape hatch if/for's
	// own header already needs (see parser.parseMatchStmt).
	src := `func f() {
	match shape {
		Shape.Point => {
		}
	}
}`
	p := New(lexer.NewFile("t.ll", src))
	n := p.parseTopLevelItem()
	if p.diags.HasErrors() {
		t.Fatalf("unexpected parse errors: %v", p.diags.All())
	}
	body := p.tree.FuncBody(n)
	matchStmt := p.tree.Child(body, 0)
	subject := p.tree.MatchSubject(matchStmt)
	if kind := p.tree.Nodes[subject].Kind.String(); kind != "Ident" {
		t.Errorf("subject kind = %s, want Ident (not a composite literal)", kind)
	}
}
