package parser

import (
	"testing"

	"llvm_lang/src/ast"
	"llvm_lang/src/lexer"
)

// parseExprSrc parses src as a single expression and fails the test on any
// diagnostic - for the happy-path precedence/shape tests, where a parse
// error means the test itself is broken, not the thing under test.
func parseExprSrc(t *testing.T, src string) (*ast.Tree, ast.NodeIndex) {
	t.Helper()
	p := New(lexer.NewFile("t.ll", src))
	n := p.parseExpr(precLowest)
	if p.diags.HasErrors() {
		t.Fatalf("unexpected parse errors for %q: %v", src, p.diags.All())
	}
	return p.tree, n
}

// TestExprShape is the main precedence/associativity/postfix-chaining net:
// every case asserts the exact tree shape via Tree.Dump, so a precedence
// regression shows up as a diff in an obviously wrong place rather than a
// vague "some test somewhere failed."
func TestExprShape(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "literal number",
			src:  "5",
			want: "NumberLit \"5\"\n",
		},
		{
			name: "literal string",
			src:  `"hi"`,
			want: `StringLit "\"hi\""` + "\n",
		},
		{
			name: "bool literal true",
			src:  "true",
			want: "BoolLit \"true\"\n",
		},
		{
			name: "bool literal false",
			src:  "false",
			want: "BoolLit \"false\"\n",
		},
		{
			name: "identifier",
			src:  "x",
			want: "Ident \"x\"\n",
		},
		{
			name: "mul binds tighter than add",
			src:  "1 + 2 * 3",
			want: "" +
				"BinaryExpr \"+\"\n" +
				"  NumberLit \"1\"\n" +
				"  BinaryExpr \"*\"\n" +
				"    NumberLit \"2\"\n" +
				"    NumberLit \"3\"\n",
		},
		{
			name: "add is left-associative",
			src:  "a - b - c",
			want: "" +
				"BinaryExpr \"-\"\n" +
				"  BinaryExpr \"-\"\n" +
				"    Ident \"a\"\n" +
				"    Ident \"b\"\n" +
				"  Ident \"c\"\n",
		},
		{
			name: "mul is left-associative",
			src:  "a / b / c",
			want: "" +
				"BinaryExpr \"/\"\n" +
				"  BinaryExpr \"/\"\n" +
				"    Ident \"a\"\n" +
				"    Ident \"b\"\n" +
				"  Ident \"c\"\n",
		},
		{
			name: "and binds tighter than or",
			src:  "a || b && c",
			want: "" +
				"BinaryExpr \"||\"\n" +
				"  Ident \"a\"\n" +
				"  BinaryExpr \"&&\"\n" +
				"    Ident \"b\"\n" +
				"    Ident \"c\"\n",
		},
		{
			name: "compare binds tighter than and",
			src:  "a < b && c > d",
			want: "" +
				"BinaryExpr \"&&\"\n" +
				"  BinaryExpr \"<\"\n" +
				"    Ident \"a\"\n" +
				"    Ident \"b\"\n" +
				"  BinaryExpr \">\"\n" +
				"    Ident \"c\"\n" +
				"    Ident \"d\"\n",
		},
		{
			name: "add binds tighter than compare",
			src:  "a == b + c",
			want: "" +
				"BinaryExpr \"==\"\n" +
				"  Ident \"a\"\n" +
				"  BinaryExpr \"+\"\n" +
				"    Ident \"b\"\n" +
				"    Ident \"c\"\n",
		},
		{
			name: "mixed mul/add on both sides",
			src:  "a * b + c * d",
			want: "" +
				"BinaryExpr \"+\"\n" +
				"  BinaryExpr \"*\"\n" +
				"    Ident \"a\"\n" +
				"    Ident \"b\"\n" +
				"  BinaryExpr \"*\"\n" +
				"    Ident \"c\"\n" +
				"    Ident \"d\"\n",
		},
		{
			name: "parens override precedence",
			src:  "(1 + 2) * 3",
			want: "" +
				"BinaryExpr \"*\"\n" +
				"  ParenExpr\n" +
				"    BinaryExpr \"+\"\n" +
				"      NumberLit \"1\"\n" +
				"      NumberLit \"2\"\n" +
				"  NumberLit \"3\"\n",
		},
		{
			name: "unary binds tighter than binary",
			src:  "-a + b",
			want: "" +
				"BinaryExpr \"+\"\n" +
				"  UnaryExpr \"-\"\n" +
				"    Ident \"a\"\n" +
				"  Ident \"b\"\n",
		},
		{
			name: "not operator",
			src:  "!a",
			want: "UnaryExpr \"!\"\n  Ident \"a\"\n",
		},
		{
			name: "postfix binds tighter than unary: member",
			src:  "-a.b",
			want: "" +
				"UnaryExpr \"-\"\n" +
				"  MemberExpr \"b\"\n" +
				"    Ident \"a\"\n",
		},
		{
			name: "postfix binds tighter than unary: call",
			src:  "-a()",
			want: "" +
				"UnaryExpr \"-\"\n" +
				"  CallExpr\n" +
				"    Ident \"a\"\n",
		},
		{
			name: "call, no args",
			src:  "f()",
			want: "CallExpr\n  Ident \"f\"\n",
		},
		{
			name: "call, multiple args",
			src:  "add(x, y)",
			want: "" +
				"CallExpr\n" +
				"  Ident \"add\"\n" +
				"  Ident \"x\"\n" +
				"  Ident \"y\"\n",
		},
		{
			name: "nested member access",
			src:  "a.b.c",
			want: "" +
				"MemberExpr \"c\"\n" +
				"  MemberExpr \"b\"\n" +
				"    Ident \"a\"\n",
		},
		{
			name: "nested index access",
			src:  "a[0][1]",
			want: "" +
				"IndexExpr\n" +
				"  IndexExpr\n" +
				"    Ident \"a\"\n" +
				"    NumberLit \"0\"\n" +
				"  NumberLit \"1\"\n",
		},
		{
			name: "mixed member/index/call chain",
			src:  "a.b[0].c()",
			want: "" +
				"CallExpr\n" +
				"  MemberExpr \"c\"\n" +
				"    IndexExpr\n" +
				"      MemberExpr \"b\"\n" +
				"        Ident \"a\"\n" +
				"      NumberLit \"0\"\n",
		},
		{
			name: "call then member",
			src:  "a().b",
			want: "" +
				"MemberExpr \"b\"\n" +
				"  CallExpr\n" +
				"    Ident \"a\"\n",
		},
		{
			name: "plain index still parses as IndexExpr, unchanged",
			src:  "s[1]",
			want: "" +
				"IndexExpr\n" +
				"  Ident \"s\"\n" +
				"  NumberLit \"1\"\n",
		},
		{
			name: "slice with both bounds",
			src:  "s[1:4]",
			want: "" +
				"SliceExpr\n" +
				"  Ident \"s\"\n" +
				"  NumberLit \"1\"\n" +
				"  NumberLit \"4\"\n",
		},
		{
			name: "slice with low bound omitted",
			src:  "s[:4]",
			want: "" +
				"SliceExpr\n" +
				"  Ident \"s\"\n" +
				"  <missing>\n" +
				"  NumberLit \"4\"\n",
		},
		{
			name: "slice with high bound omitted",
			src:  "s[1:]",
			want: "" +
				"SliceExpr\n" +
				"  Ident \"s\"\n" +
				"  NumberLit \"1\"\n" +
				"  <missing>\n",
		},
		{
			name: "slice with both bounds omitted",
			src:  "s[:]",
			want: "" +
				"SliceExpr\n" +
				"  Ident \"s\"\n" +
				"  <missing>\n" +
				"  <missing>\n",
		},
		{
			name: "slice of an index expression, chained",
			src:  "a[0][1:2]",
			want: "" +
				"SliceExpr\n" +
				"  IndexExpr\n" +
				"    Ident \"a\"\n" +
				"    NumberLit \"0\"\n" +
				"  NumberLit \"1\"\n" +
				"  NumberLit \"2\"\n",
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

func TestExprSpanCoversWholeExpression(t *testing.T) {
	tree, n := parseExprSrc(t, "a.b[0].c()")
	span := tree.SpanOf(n)
	if got := tree.File.Src[span.Start:span.End]; got != "a.b[0].c()" {
		t.Errorf("span text = %q, want %q", got, "a.b[0].c()")
	}
}

func TestExprErrorRecoveryProducesBadNode(t *testing.T) {
	p := New(lexer.NewFile("t.ll", "+"))
	n := p.parseExpr(precLowest)
	if !p.diags.HasErrors() {
		t.Fatalf("expected a diagnostic for a token with no prefix rule")
	}
	if p.diags.ErrorCount() != 1 {
		t.Fatalf("ErrorCount = %d, want 1", p.diags.ErrorCount())
	}
	if got := p.tree.Nodes[n].Kind; got.String() != "Bad" {
		t.Fatalf("node kind = %s, want Bad", got)
	}
}

func TestUnclosedCallStillProducesATree(t *testing.T) {
	// A missing `)` must not panic or hang - expect() reports it and leaves
	// the token unconsumed, so parsing still terminates cleanly.
	p := New(lexer.NewFile("t.ll", "f(a, b"))
	n := p.parseExpr(precLowest)
	if !p.diags.HasErrors() {
		t.Fatalf("expected a diagnostic for the missing ')'")
	}
	if got := p.tree.Nodes[n].Kind.String(); got != "CallExpr" {
		t.Fatalf("node kind = %s, want CallExpr", got)
	}
}
