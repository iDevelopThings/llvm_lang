package parser

import (
	"testing"

	"llvm_lang/src/ast"
	"llvm_lang/src/lexer"
)

func parseDeclSrc(t *testing.T, src string) (*ast.Tree, ast.NodeIndex) {
	t.Helper()
	p := New(lexer.NewFile("t.ll", src))
	n := p.parseTopLevelItem()
	if p.diags.HasErrors() {
		t.Fatalf("unexpected parse errors for %q: %v", src, p.diags.All())
	}
	return p.tree, n
}

func TestDeclShape(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "func with params and return type",
			src:  "func add(x int, y int) int { return x + y }",
			want: "" +
				"FuncDecl \"func\"\n" +
				"  <missing>\n" +
				"  Ident \"add\"\n" +
				"  <missing>\n" +
				"  ParamList\n" +
				"    Param\n" +
				"      Ident \"x\"\n" +
				"      Ident \"int\"\n" +
				"    Param\n" +
				"      Ident \"y\"\n" +
				"      Ident \"int\"\n" +
				"  Ident \"int\"\n" +
				"  Block\n" +
				"    ReturnStmt \"return\"\n" +
				"      BinaryExpr \"+\"\n" +
				"        Ident \"x\"\n" +
				"        Ident \"y\"\n",
		},
		{
			name: "func with no params and no return type",
			src:  "func f() { }",
			want: "" +
				"FuncDecl \"func\"\n" +
				"  <missing>\n" +
				"  Ident \"f\"\n" +
				"  <missing>\n" +
				"  ParamList\n" +
				"  <missing>\n" +
				"  Block\n",
		},
		{
			name: "method with receiver, using this",
			src:  "func (Point) translate(dx int, dy int) { this.x = this.x + dx }",
			want: "" +
				"FuncDecl \"func\"\n" +
				"  Ident \"Point\"\n" +
				"  Ident \"translate\"\n" +
				"  <missing>\n" +
				"  ParamList\n" +
				"    Param\n" +
				"      Ident \"dx\"\n" +
				"      Ident \"int\"\n" +
				"    Param\n" +
				"      Ident \"dy\"\n" +
				"      Ident \"int\"\n" +
				"  <missing>\n" +
				"  Block\n" +
				"    AssignStmt \"=\"\n" +
				"      MemberExpr \"x\"\n" +
				"        ThisExpr \"this\"\n" +
				"      BinaryExpr \"+\"\n" +
				"        MemberExpr \"x\"\n" +
				"          ThisExpr \"this\"\n" +
				"        Ident \"dx\"\n",
		},
		{
			name: "struct decl",
			src:  "struct Point {\n\tx int\n\ty int\n}",
			want: "" +
				"StructDecl \"struct\"\n" +
				"  Ident \"Point\"\n" +
				"  <missing>\n" +
				"  Field\n" +
				"    Ident \"x\"\n" +
				"    Ident \"int\"\n" +
				"  Field\n" +
				"    Ident \"y\"\n" +
				"    Ident \"int\"\n",
		},
		{
			// See expectMemberName's own doc comment: a field/method name is
			// never confused with any keyword's own expression grammar the
			// way a var/free-function name would be, so `move` - otherwise
			// a reserved word (see LANGUAGE.md's "Destructors" section) - is
			// legal here.
			name: "method named with a keyword",
			src:  "func (Point) move(dx int, dy int) { this.x = this.x + dx }",
			want: "" +
				"FuncDecl \"func\"\n" +
				"  Ident \"Point\"\n" +
				"  Ident \"move\"\n" +
				"  <missing>\n" +
				"  ParamList\n" +
				"    Param\n" +
				"      Ident \"dx\"\n" +
				"      Ident \"int\"\n" +
				"    Param\n" +
				"      Ident \"dy\"\n" +
				"      Ident \"int\"\n" +
				"  <missing>\n" +
				"  Block\n" +
				"    AssignStmt \"=\"\n" +
				"      MemberExpr \"x\"\n" +
				"        ThisExpr \"this\"\n" +
				"      BinaryExpr \"+\"\n" +
				"        MemberExpr \"x\"\n" +
				"          ThisExpr \"this\"\n" +
				"        Ident \"dx\"\n",
		},
		{
			name: "struct field named with a keyword",
			src:  "struct Entry {\n\tmove int\n}",
			want: "" +
				"StructDecl \"struct\"\n" +
				"  Ident \"Entry\"\n" +
				"  <missing>\n" +
				"  Field\n" +
				"    Ident \"move\"\n" +
				"    Ident \"int\"\n",
		},
		{
			name: "empty struct",
			src:  "struct Empty { }",
			want: "StructDecl \"struct\"\n  Ident \"Empty\"\n  <missing>\n",
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

// TestConstructorDeclShape covers a struct with zero, one, and multiple
// constructors - see LANGUAGE.md's "Constructors" section for the language
// feature and ast.Node's own StructDecl/ConstructorDecl doc comments for the
// shapes asserted here: a ConstructorDecl is [paramList, body], with no
// name/receiver/return-type children, interspersed among a StructDecl's
// ordinary Field children in declaration order.
func TestConstructorDeclShape(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "struct with zero constructors is unaffected",
			src:  "struct Point {\n\tx int\n}",
			want: "" +
				"StructDecl \"struct\"\n" +
				"  Ident \"Point\"\n" +
				"  <missing>\n" +
				"  Field\n" +
				"    Ident \"x\"\n" +
				"    Ident \"int\"\n",
		},
		{
			name: "struct with one zero-arg constructor",
			src:  "struct Point {\n\tx int\n\n\tconstructor() {\n\t\tthis.x = 99\n\t}\n}",
			want: "" +
				"StructDecl \"struct\"\n" +
				"  Ident \"Point\"\n" +
				"  <missing>\n" +
				"  Field\n" +
				"    Ident \"x\"\n" +
				"    Ident \"int\"\n" +
				"  ConstructorDecl \"constructor\"\n" +
				"    ParamList\n" +
				"    Block\n" +
				"      AssignStmt \"=\"\n" +
				"        MemberExpr \"x\"\n" +
				"          ThisExpr \"this\"\n" +
				"        NumberLit \"99\"\n",
		},
		{
			name: "struct with multiple constructors overloaded by arg count",
			src: "struct Point {\n" +
				"\tx int\n\n" +
				"\tconstructor() {\n\t\tthis.x = 99\n\t}\n" +
				"\tconstructor(v int) {\n\t\tthis.x = v\n\t}\n" +
				"}",
			want: "" +
				"StructDecl \"struct\"\n" +
				"  Ident \"Point\"\n" +
				"  <missing>\n" +
				"  Field\n" +
				"    Ident \"x\"\n" +
				"    Ident \"int\"\n" +
				"  ConstructorDecl \"constructor\"\n" +
				"    ParamList\n" +
				"    Block\n" +
				"      AssignStmt \"=\"\n" +
				"        MemberExpr \"x\"\n" +
				"          ThisExpr \"this\"\n" +
				"        NumberLit \"99\"\n" +
				"  ConstructorDecl \"constructor\"\n" +
				"    ParamList\n" +
				"      Param\n" +
				"        Ident \"v\"\n" +
				"        Ident \"int\"\n" +
				"    Block\n" +
				"      AssignStmt \"=\"\n" +
				"        MemberExpr \"x\"\n" +
				"          ThisExpr \"this\"\n" +
				"        Ident \"v\"\n",
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

// TestDestructorDeclShape covers a struct with zero and one destructor - see
// LANGUAGE.md's "Destructors" section for the language feature and
// ast.Node's own StructDecl/DestructorDecl doc comments for the shapes
// asserted here: a DestructorDecl is [paramList, body], with an always-empty
// ParamList (sema, not this grammar, enforces "zero parameters" - see
// TestDestructorMustTakeNoParametersIsError, typecheck_test.go), no name/
// receiver/return-type children, interspersed among a StructDecl's ordinary
// Field/ConstructorDecl children in declaration order.
func TestDestructorDeclShape(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "struct with zero destructors is unaffected",
			src:  "struct Point {\n\tx int\n}",
			want: "" +
				"StructDecl \"struct\"\n" +
				"  Ident \"Point\"\n" +
				"  <missing>\n" +
				"  Field\n" +
				"    Ident \"x\"\n" +
				"    Ident \"int\"\n",
		},
		{
			name: "struct with one destructor",
			src:  "struct Point {\n\tx int\n\n\tdestructor() {\n\t\tthis.x = 0\n\t}\n}",
			want: "" +
				"StructDecl \"struct\"\n" +
				"  Ident \"Point\"\n" +
				"  <missing>\n" +
				"  Field\n" +
				"    Ident \"x\"\n" +
				"    Ident \"int\"\n" +
				"  DestructorDecl \"destructor\"\n" +
				"    ParamList\n" +
				"    Block\n" +
				"      AssignStmt \"=\"\n" +
				"        MemberExpr \"x\"\n" +
				"          ThisExpr \"this\"\n" +
				"        NumberLit \"0\"\n",
		},
		{
			name: "constructor and destructor coexisting on one struct",
			src: "struct Point {\n" +
				"\tx int\n\n" +
				"\tconstructor(v int) {\n\t\tthis.x = v\n\t}\n" +
				"\tdestructor() {\n\t\tthis.x = 0\n\t}\n" +
				"}",
			want: "" +
				"StructDecl \"struct\"\n" +
				"  Ident \"Point\"\n" +
				"  <missing>\n" +
				"  Field\n" +
				"    Ident \"x\"\n" +
				"    Ident \"int\"\n" +
				"  ConstructorDecl \"constructor\"\n" +
				"    ParamList\n" +
				"      Param\n" +
				"        Ident \"v\"\n" +
				"        Ident \"int\"\n" +
				"    Block\n" +
				"      AssignStmt \"=\"\n" +
				"        MemberExpr \"x\"\n" +
				"          ThisExpr \"this\"\n" +
				"        Ident \"v\"\n" +
				"  DestructorDecl \"destructor\"\n" +
				"    ParamList\n" +
				"    Block\n" +
				"      AssignStmt \"=\"\n" +
				"        MemberExpr \"x\"\n" +
				"          ThisExpr \"this\"\n" +
				"        NumberLit \"0\"\n",
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

func TestArrayTypeAndCompositeLitShape(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "fixed-size array type",
			src:  "var a [5]int",
			want: "" +
				"VarDecl \"var\"\n" +
				"  Ident \"a\"\n" +
				"  ArrayType\n" +
				"    NumberLit \"5\"\n" +
				"    Ident \"int\"\n" +
				"  <missing>\n",
		},
		{
			name: "dynamic array type parses (semantically rejected later, not a parse error)",
			src:  "var a []int",
			want: "" +
				"VarDecl \"var\"\n" +
				"  Ident \"a\"\n" +
				"  ArrayType\n" +
				"    <missing>\n" +
				"    Ident \"int\"\n" +
				"  <missing>\n",
		},
		{
			name: "nested array type",
			src:  "var a [5][3]int",
			want: "" +
				"VarDecl \"var\"\n" +
				"  Ident \"a\"\n" +
				"  ArrayType\n" +
				"    NumberLit \"5\"\n" +
				"    ArrayType\n" +
				"      NumberLit \"3\"\n" +
				"      Ident \"int\"\n" +
				"  <missing>\n",
		},
		{
			name: "array composite literal",
			src:  "b := [3]int{1, 2, 3}",
			want: "" +
				"ShortVarDecl \":=\"\n" +
				"  Ident \"b\"\n" +
				"  CompositeLit\n" +
				"    ArrayType\n" +
				"      NumberLit \"3\"\n" +
				"      Ident \"int\"\n" +
				"    NumberLit \"1\"\n" +
				"    NumberLit \"2\"\n" +
				"    NumberLit \"3\"\n",
		},
		{
			name: "struct composite literal, positional",
			src:  "p := Point{1, 2}",
			want: "" +
				"ShortVarDecl \":=\"\n" +
				"  Ident \"p\"\n" +
				"  CompositeLit\n" +
				"    Ident \"Point\"\n" +
				"    NumberLit \"1\"\n" +
				"    NumberLit \"2\"\n",
		},
		{
			name: "struct composite literal, keyed",
			src:  "p := Point{x: 1, y: 2}",
			want: "" +
				"ShortVarDecl \":=\"\n" +
				"  Ident \"p\"\n" +
				"  CompositeLit\n" +
				"    Ident \"Point\"\n" +
				"    KeyValueExpr\n" +
				"      Ident \"x\"\n" +
				"      NumberLit \"1\"\n" +
				"    KeyValueExpr\n" +
				"      Ident \"y\"\n" +
				"      NumberLit \"2\"\n",
		},
		{
			name: "trailing comma in composite literal is tolerated",
			src:  "p := Point{1, 2,}",
			want: "" +
				"ShortVarDecl \":=\"\n" +
				"  Ident \"p\"\n" +
				"  CompositeLit\n" +
				"    Ident \"Point\"\n" +
				"    NumberLit \"1\"\n" +
				"    NumberLit \"2\"\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := New(lexer.NewFile("t.ll", tt.src))
			n := p.parseStmt()
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

// TestCompositeLitAmbiguityGuard is the regression test for the classic
// Go parsing wrinkle this round had to solve: a bare `Foo{` is ambiguous
// between "start of a composite literal" and "identifier, followed
// unrelatedly by a block". If exprLev weren't wired correctly, `if a { b()
// }` would misparse as `if (a{b()})` - a single ExprStmt wrapping a
// CompositeLit - instead of an if-statement with a real block body.
func TestCompositeLitAmbiguityGuard(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "if condition followed by block is never a composite literal",
			src:  "if a { b() }",
			want: "" +
				"IfStmt \"if\"\n" +
				"  Ident \"a\"\n" +
				"  Block\n" +
				"    ExprStmt\n" +
				"      CallExpr\n" +
				"        Ident \"b\"\n" +
				"  <missing>\n",
		},
		{
			name: "for cond-only followed by block is never a composite literal",
			src:  "for a { b() }",
			want: "" +
				"ForStmt \"for\"\n" +
				"  <missing>\n" +
				"  Ident \"a\"\n" +
				"  <missing>\n" +
				"  Block\n" +
				"    ExprStmt\n" +
				"      CallExpr\n" +
				"        Ident \"b\"\n",
		},
		{
			name: "parenthesizing the condition re-enables composite literals",
			src:  "if (Foo{}) { }",
			want: "" +
				"IfStmt \"if\"\n" +
				"  ParenExpr\n" +
				"    CompositeLit\n" +
				"      Ident \"Foo\"\n" +
				"  Block\n" +
				"  <missing>\n",
		},
		{
			name: "a composite literal inside call args within a condition is fine too",
			src:  "if f(Foo{}) { }",
			want: "" +
				"IfStmt \"if\"\n" +
				"  CallExpr\n" +
				"    Ident \"f\"\n" +
				"    CompositeLit\n" +
				"      Ident \"Foo\"\n" +
				"  Block\n" +
				"  <missing>\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := New(lexer.NewFile("t.ll", tt.src))
			n := p.parseStmt()
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

func TestParseFileEndToEnd(t *testing.T) {
	src := `struct Point {
	x int
	y int
}

func (Point) translate(dx int, dy int) {
	this.x = this.x + dx
	this.y = this.y + dy
}

func add(x int, y int) int {
	return x + y
}

func main() {
	var a int = 5
	p := Point{x: 1, y: 2}
	if a >= 10 {
		print("big")
	} else {
		print("small")
	}
	for i := 0; i < 10; i++ {
		print(i)
	}
}
`
	tree, diags := ParseFile(lexer.NewFile("t.ll", src))
	if diags.HasErrors() {
		t.Fatalf("unexpected parse errors: %v", diags.All())
	}
	root := tree.Root
	if got := tree.Nodes[root].Kind.String(); got != "File" {
		t.Fatalf("root kind = %s, want File", got)
	}
	decls := tree.Children(root)
	if len(decls) != 4 {
		t.Fatalf("File has %d top-level decls, want 4:\n%s", len(decls), tree.Dump(root))
	}
	wantKinds := []string{"StructDecl", "FuncDecl", "FuncDecl", "FuncDecl"}
	for i, want := range wantKinds {
		if got := tree.Nodes[decls[i]].Kind.String(); got != want {
			t.Errorf("decl[%d] kind = %s, want %s", i, got, want)
		}
	}
	lastFunc := decls[3]
	nameNode := tree.Child(lastFunc, 1)
	if got := tree.Text(nameNode); got != "main" {
		t.Errorf("last func name = %q, want %q", got, "main")
	}
}

func TestTopLevelVarFuncStruct(t *testing.T) {
	// Real global vars sit directly at file scope (no main() needed for
	// these - they're static data, not executable code); func/struct
	// declarations can be interleaved among them freely.
	src := "var a int = 5\n" +
		"var b int = 10\n" +
		"func add(x int, y int) int {\n" +
		"\treturn x + y\n" +
		"}\n" +
		"struct Point {\n" +
		"\tx int\n" +
		"}\n"
	tree, diags := ParseFile(lexer.NewFile("t.ll", src))
	if diags.HasErrors() {
		t.Fatalf("unexpected parse errors: %v", diags.All())
	}
	decls := tree.Children(tree.Root)
	wantKinds := []string{"VarDecl", "VarDecl", "FuncDecl", "StructDecl"}
	if len(decls) != len(wantKinds) {
		t.Fatalf("File has %d top-level items, want %d:\n%s", len(decls), len(wantKinds), tree.Dump(tree.Root))
	}
	for i, want := range wantKinds {
		if got := tree.Nodes[decls[i]].Kind.String(); got != want {
			t.Errorf("item[%d] kind = %s, want %s", i, got, want)
		}
	}
}

func TestBareStatementsRejectedAtTopLevel(t *testing.T) {
	// Unlike inside a function body, `:=`, `if`, and bare expressions
	// aren't valid directly at file scope - only var/func/struct
	// declarations are (see AGENTS.md's "Top level" section: LLVM has no
	// notion of "just run a statement at global scope"). Each bad line
	// must still degrade to a diagnostic and recover, not derail the rest
	// of the file.
	src := "c := 1\n" +
		"if c >= 10: print(c)\n" +
		"func add(x int, y int) int {\n" +
		"\treturn x + y\n" +
		"}\n"
	tree, diags := ParseFile(lexer.NewFile("t.ll", src))
	if diags.ErrorCount() != 2 {
		t.Fatalf("ErrorCount = %d, want 2 (one per bad top-level statement): %v", diags.ErrorCount(), diags.All())
	}
	decls := tree.Children(tree.Root)
	if len(decls) != 3 {
		t.Fatalf("File has %d top-level items, want 3 (2 bad + FuncDecl): %s", len(decls), tree.Dump(tree.Root))
	}
	if got := tree.Nodes[decls[2]].Kind.String(); got != "FuncDecl" {
		t.Errorf("decl[2] kind = %s, want FuncDecl (recovery must still reach it)", got)
	}
}

func TestTopLevelGarbageRecovers(t *testing.T) {
	// `)` alone isn't a valid declaration - still needs to degrade to a
	// bounded diagnostic and a still-usable tree, same as any other
	// malformed input.
	src := ")\n\nfunc f() { }"
	tree, diags := ParseFile(lexer.NewFile("t.ll", src))
	if !diags.HasErrors() {
		t.Fatalf("expected a diagnostic for the stray ')'")
	}
	if diags.ErrorCount() >= maxErrors {
		t.Fatalf("ErrorCount = %d hit the bailout threshold on trivial input", diags.ErrorCount())
	}
	root := tree.Root
	decls := tree.Children(root)
	if len(decls) != 2 {
		t.Fatalf("File has %d top-level items, want 2 (bad statement + FuncDecl): %s", len(decls), tree.Dump(root))
	}
	if got := tree.Nodes[decls[1]].Kind.String(); got != "FuncDecl" {
		t.Errorf("decl[1] kind = %s, want FuncDecl", got)
	}
}

func TestArrayTypeWithoutLiteralBodyIsRejected(t *testing.T) {
	// `[3]int` alone isn't a value - it's a type used where an expression
	// was expected, so it must be reported, not silently accepted.
	p := New(lexer.NewFile("t.ll", "x := [3]int"))
	p.parseStmt()
	if !p.diags.HasErrors() {
		t.Fatalf("expected an error for a bare array type used as a value")
	}
}

func TestThisOutsideMethodStillParses(t *testing.T) {
	// Whether `this` is valid outside a method body is a sema concern, not
	// a parse error - the grammar accepts it anywhere an expression can.
	p := New(lexer.NewFile("t.ll", "this.x"))
	n := p.parseExpr(precLowest)
	if p.diags.HasErrors() {
		t.Fatalf("unexpected parse errors: %v", p.diags.All())
	}
	if got := p.tree.Nodes[n].Kind.String(); got != "MemberExpr" {
		t.Fatalf("node kind = %s, want MemberExpr", got)
	}
}
