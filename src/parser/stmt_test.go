package parser

import (
	"testing"

	"llvm_lang/src/ast"
	"llvm_lang/src/lexer"
)

// parseStmtSrc parses src as a single statement and fails the test on any
// diagnostic - for the happy-path shape tests, where a parse error means
// the test itself is broken, not the thing under test.
func parseStmtSrc(t *testing.T, src string) (*ast.Tree, ast.NodeIndex) {
	t.Helper()
	p := New(lexer.NewFile("t.ll", src))
	n := p.parseStmt()
	if p.diags.HasErrors() {
		t.Fatalf("unexpected parse errors for %q: %v", src, p.diags.All())
	}
	return p.tree, n
}

func TestStmtShape(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "var decl with type and init",
			src:  "var a int = 5",
			want: "" +
				"VarDecl \"var\"\n" +
				"  Ident \"a\"\n" +
				"  Ident \"int\"\n" +
				"  NumberLit \"5\"\n",
		},
		{
			name: "var decl type only",
			src:  "var a int",
			want: "" +
				"VarDecl \"var\"\n" +
				"  Ident \"a\"\n" +
				"  Ident \"int\"\n" +
				"  <missing>\n",
		},
		{
			name: "var decl init only",
			src:  "var a = 5",
			want: "" +
				"VarDecl \"var\"\n" +
				"  Ident \"a\"\n" +
				"  <missing>\n" +
				"  NumberLit \"5\"\n",
		},
		{
			name: "short var decl",
			src:  "c := a + b",
			want: "" +
				"ShortVarDecl \":=\"\n" +
				"  Ident \"c\"\n" +
				"  BinaryExpr \"+\"\n" +
				"    Ident \"a\"\n" +
				"    Ident \"b\"\n",
		},
		{
			name: "bare expression statement",
			src:  "print(x)",
			want: "" +
				"ExprStmt\n" +
				"  CallExpr\n" +
				"    Ident \"print\"\n" +
				"    Ident \"x\"\n",
		},
		{
			name: "plain assignment",
			src:  "x = 1",
			want: "AssignStmt \"=\"\n  Ident \"x\"\n  NumberLit \"1\"\n",
		},
		{
			name: "compound assignment",
			src:  "x += 1",
			want: "AssignStmt \"+=\"\n  Ident \"x\"\n  NumberLit \"1\"\n",
		},
		{
			name: "assign to member",
			src:  "p.x = 1",
			want: "" +
				"AssignStmt \"=\"\n" +
				"  MemberExpr \"x\"\n" +
				"    Ident \"p\"\n" +
				"  NumberLit \"1\"\n",
		},
		{
			name: "assign to index",
			src:  "arr[0] = 1",
			want: "" +
				"AssignStmt \"=\"\n" +
				"  IndexExpr\n" +
				"    Ident \"arr\"\n" +
				"    NumberLit \"0\"\n" +
				"  NumberLit \"1\"\n",
		},
		{
			name: "increment",
			src:  "x++",
			want: "IncDecStmt \"++\"\n  Ident \"x\"\n",
		},
		{
			name: "decrement",
			src:  "x--",
			want: "IncDecStmt \"--\"\n  Ident \"x\"\n",
		},
		{
			name: "return with value",
			src:  "return x + y",
			want: "" +
				"ReturnStmt \"return\"\n" +
				"  BinaryExpr \"+\"\n" +
				"    Ident \"x\"\n" +
				"    Ident \"y\"\n",
		},
		{
			name: "bare return",
			src:  "return",
			want: "ReturnStmt \"return\"\n  <missing>\n",
		},
		{
			name: "break",
			src:  "break",
			want: "BreakStmt \"break\"\n",
		},
		{
			name: "continue",
			src:  "continue",
			want: "ContinueStmt \"continue\"\n",
		},
		{
			name: "block with two statements",
			src:  "{\n\ta := 1\n\tb := 2\n}",
			want: "" +
				"Block\n" +
				"  ShortVarDecl \":=\"\n" +
				"    Ident \"a\"\n" +
				"    NumberLit \"1\"\n" +
				"  ShortVarDecl \":=\"\n" +
				"    Ident \"b\"\n" +
				"    NumberLit \"2\"\n",
		},
		{
			name: "block, same-line last statement omits semicolon",
			src:  "{ return 1 }",
			want: "Block\n  ReturnStmt \"return\"\n    NumberLit \"1\"\n",
		},
		{
			name: "block, explicit semicolons on one line",
			src:  "{ a := 1; b := 2 }",
			want: "" +
				"Block\n" +
				"  ShortVarDecl \":=\"\n" +
				"    Ident \"a\"\n" +
				"    NumberLit \"1\"\n" +
				"  ShortVarDecl \":=\"\n" +
				"    Ident \"b\"\n" +
				"    NumberLit \"2\"\n",
		},
		{
			name: "if one-liner, no else",
			src:  "if c >= 10: print(x)",
			want: "" +
				"IfStmt \"if\"\n" +
				"  BinaryExpr \">=\"\n" +
				"    Ident \"c\"\n" +
				"    NumberLit \"10\"\n" +
				"  ExprStmt\n" +
				"    CallExpr\n" +
				"      Ident \"print\"\n" +
				"      Ident \"x\"\n" +
				"  <missing>\n",
		},
		{
			name: "if/else brace form",
			src:  "if c >= 10 {\n\tprint(x)\n} else {\n\tprint(y)\n}",
			want: "" +
				"IfStmt \"if\"\n" +
				"  BinaryExpr \">=\"\n" +
				"    Ident \"c\"\n" +
				"    NumberLit \"10\"\n" +
				"  Block\n" +
				"    ExprStmt\n" +
				"      CallExpr\n" +
				"        Ident \"print\"\n" +
				"        Ident \"x\"\n" +
				"  Block\n" +
				"    ExprStmt\n" +
				"      CallExpr\n" +
				"        Ident \"print\"\n" +
				"        Ident \"y\"\n",
		},
		{
			name: "else-if chain",
			src:  "if a { x() } else if b { y() } else { z() }",
			want: "" +
				"IfStmt \"if\"\n" +
				"  Ident \"a\"\n" +
				"  Block\n" +
				"    ExprStmt\n" +
				"      CallExpr\n" +
				"        Ident \"x\"\n" +
				"  IfStmt \"if\"\n" +
				"    Ident \"b\"\n" +
				"    Block\n" +
				"      ExprStmt\n" +
				"        CallExpr\n" +
				"          Ident \"y\"\n" +
				"    Block\n" +
				"      ExprStmt\n" +
				"        CallExpr\n" +
				"          Ident \"z\"\n",
		},
		{
			name: "bare infinite for",
			src:  "for {\n\tbreak\n}",
			want: "" +
				"ForStmt \"for\"\n" +
				"  <missing>\n" +
				"  <missing>\n" +
				"  <missing>\n" +
				"  Block\n" +
				"    BreakStmt \"break\"\n",
		},
		{
			name: "cond-only for",
			src:  "for c < 10 {\n\tc = c + 1\n}",
			want: "" +
				"ForStmt \"for\"\n" +
				"  <missing>\n" +
				"  BinaryExpr \"<\"\n" +
				"    Ident \"c\"\n" +
				"    NumberLit \"10\"\n" +
				"  <missing>\n" +
				"  Block\n" +
				"    AssignStmt \"=\"\n" +
				"      Ident \"c\"\n" +
				"      BinaryExpr \"+\"\n" +
				"        Ident \"c\"\n" +
				"        NumberLit \"1\"\n",
		},
		{
			name: "full three-clause for",
			src:  "for i := 0; i < 10; i++ {\n\tprint(i)\n}",
			want: "" +
				"ForStmt \"for\"\n" +
				"  ShortVarDecl \":=\"\n" +
				"    Ident \"i\"\n" +
				"    NumberLit \"0\"\n" +
				"  BinaryExpr \"<\"\n" +
				"    Ident \"i\"\n" +
				"    NumberLit \"10\"\n" +
				"  IncDecStmt \"++\"\n" +
				"    Ident \"i\"\n" +
				"  Block\n" +
				"    ExprStmt\n" +
				"      CallExpr\n" +
				"        Ident \"print\"\n" +
				"        Ident \"i\"\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tree, n := parseStmtSrc(t, tt.src)
			got := tree.Dump(n)
			if got != tt.want {
				t.Errorf("Dump(%q):\n got:\n%s\nwant:\n%s", tt.src, got, tt.want)
			}
		})
	}
}

func TestShortVarDeclRejectsNonIdentTarget(t *testing.T) {
	p := New(lexer.NewFile("t.ll", "5 := x"))
	n := p.parseStmt()
	if p.diags.ErrorCount() != 1 {
		t.Fatalf("ErrorCount = %d, want 1: %v", p.diags.ErrorCount(), p.diags.All())
	}
	if got := p.tree.Nodes[n].Kind.String(); got != "ShortVarDecl" {
		t.Fatalf("node kind = %s, want ShortVarDecl (best-effort)", got)
	}
}

func TestAssignRejectsInvalidTarget(t *testing.T) {
	p := New(lexer.NewFile("t.ll", "5 = x"))
	n := p.parseStmt()
	if p.diags.ErrorCount() != 1 {
		t.Fatalf("ErrorCount = %d, want 1: %v", p.diags.ErrorCount(), p.diags.All())
	}
	if got := p.tree.Nodes[n].Kind.String(); got != "AssignStmt" {
		t.Fatalf("node kind = %s, want AssignStmt (best-effort)", got)
	}
}

func TestForCondOnlyRejectsNonExpressionClause(t *testing.T) {
	// `x := 1` followed directly by `{` (no semicolon) looks like the
	// cond-only form, but a short-var-decl isn't a valid loop condition.
	p := New(lexer.NewFile("t.ll", "for x := 1 { }"))
	p.parseStmt()
	if p.diags.ErrorCount() != 1 {
		t.Fatalf("ErrorCount = %d, want 1: %v", p.diags.ErrorCount(), p.diags.All())
	}
}

func TestBlockMissingSemicolonStillRecovers(t *testing.T) {
	// No semicolon and no newline between the two statements: expect()
	// reports it but doesn't consume, so the loop must still recover and
	// parse both statements rather than hanging or dropping the second one.
	p := New(lexer.NewFile("t.ll", "{ a := 1 b := 2 }"))
	n := p.parseStmt()
	if !p.diags.HasErrors() {
		t.Fatalf("expected a diagnostic for the missing semicolon")
	}
	kids := p.tree.Children(n)
	if len(kids) != 2 {
		t.Fatalf("Block has %d statements, want 2 (recovery should still find both): %s", len(kids), p.tree.Dump(n))
	}
}
