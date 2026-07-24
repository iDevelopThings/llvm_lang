package parser

import (
	"testing"

	"llvm_lang/src/lexer"
)

// This file covers this round's new grammar for Go-style multi-return values
// (see LANGUAGE.md's "Go-style multi-return values" section): a FuncDecl's
// own parenthesized `(T1, T2, ...)` return-type list (MultiReturnType), a
// multi-value `return a, b, ...` (MultiValueExpr), and the two multi-target
// destructuring statement forms (MultiShortVarDecl/MultiAssignStmt) - table-
// driven Tree.Dump shape assertions, matching this package's established
// convention (see functype_test.go/pointer_test.go).

// TestFuncDeclMultiReturnTypeShape covers a FuncDecl's own new return-type
// grammar: a plain single type (unchanged), and a parenthesized 2+ type list
// (MultiReturnType, wrapped into FuncDecl's existing single return-type
// slot).
func TestFuncDeclMultiReturnTypeShape(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "single unparenthesized return type is completely unchanged",
			src:  "func f() int { return 0 }",
			want: "" +
				"FuncDecl \"func\"\n" +
				"  <missing>\n" +
				"  Ident \"f\"\n" +
				"  ParamList\n" +
				"  Ident \"int\"\n" +
				"  Block\n" +
				"    ReturnStmt \"return\"\n" +
				"      NumberLit \"0\"\n",
		},
		{
			name: "no return type at all is completely unchanged",
			src:  "func f() { }",
			want: "" +
				"FuncDecl \"func\"\n" +
				"  <missing>\n" +
				"  Ident \"f\"\n" +
				"  ParamList\n" +
				"  <missing>\n" +
				"  Block\n",
		},
		{
			name: "two-type multi-return",
			src:  "func f() (int, bool) { return 0, true }",
			want: "" +
				"FuncDecl \"func\"\n" +
				"  <missing>\n" +
				"  Ident \"f\"\n" +
				"  ParamList\n" +
				"  MultiReturnType\n" +
				"    Ident \"int\"\n" +
				"    Ident \"bool\"\n" +
				"  Block\n" +
				"    ReturnStmt \"return\"\n" +
				"      MultiValueExpr\n" +
				"        NumberLit \"0\"\n" +
				"        BoolLit \"true\"\n",
		},
		{
			name: "three-type multi-return, mixed widths",
			src:  "func f() (i64, bool, string) { return 0, true, \"x\" }",
			want: "" +
				"FuncDecl \"func\"\n" +
				"  <missing>\n" +
				"  Ident \"f\"\n" +
				"  ParamList\n" +
				"  MultiReturnType\n" +
				"    Ident \"i64\"\n" +
				"    Ident \"bool\"\n" +
				"    Ident \"string\"\n" +
				"  Block\n" +
				"    ReturnStmt \"return\"\n" +
				"      MultiValueExpr\n" +
				"        NumberLit \"0\"\n" +
				"        BoolLit \"true\"\n" +
				"        StringLit \"\\\"x\\\"\"\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := New(lexer.NewFile("t.ll", tt.src))
			n := p.parseTopLevelItem()
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

// TestMultiShortVarDeclShape covers `a, b := f(...)` - a fresh
// MultiShortVarDecl node, distinct from a single-name `x := f()` (completely
// unchanged, still a plain ShortVarDecl).
func TestMultiShortVarDeclShape(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "single-name := is completely unchanged",
			src:  "x := f()",
			want: "" +
				"ShortVarDecl \":=\"\n" +
				"  Ident \"x\"\n" +
				"  CallExpr\n" +
				"    Ident \"f\"\n",
		},
		{
			name: "two-name destructuring :=",
			src:  "a, b := f()",
			want: "" +
				"MultiShortVarDecl \":=\"\n" +
				"  Ident \"a\"\n" +
				"  Ident \"b\"\n" +
				"  CallExpr\n" +
				"    Ident \"f\"\n",
		},
		{
			name: "three-name destructuring :=",
			src:  "a, b, c := f(1, 2)",
			want: "" +
				"MultiShortVarDecl \":=\"\n" +
				"  Ident \"a\"\n" +
				"  Ident \"b\"\n" +
				"  Ident \"c\"\n" +
				"  CallExpr\n" +
				"    Ident \"f\"\n" +
				"    NumberLit \"1\"\n" +
				"    NumberLit \"2\"\n",
		},
		{
			// Map two-result indexing (`v, ok := m[k]` - see LANGUAGE.md's
			// "Maps" section) parses into the exact same MultiShortVarDecl
			// shape as the ordinary multi-return call case above, just with
			// an IndexExpr (not a CallExpr) as the trailing value child -
			// see ast.Node's own MultiShortVarDecl doc comment and
			// sema.checkDestructureSource, which branches on this same
			// IndexExpr-vs-CallExpr distinction one layer down.
			name: "two-name map-index destructuring :=",
			src:  "v, ok := m[k]",
			want: "" +
				"MultiShortVarDecl \":=\"\n" +
				"  Ident \"v\"\n" +
				"  Ident \"ok\"\n" +
				"  IndexExpr\n" +
				"    Ident \"m\"\n" +
				"    Ident \"k\"\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := New(lexer.NewFile("t.ll", tt.src))
			n := p.parseSimpleStmt()
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

// TestMultiAssignStmtShape covers `a, b = f(...)` - the assignment-form
// counterpart to MultiShortVarDecl, including non-Ident lvalue targets
// (a MemberExpr and an IndexExpr) to prove the grammar isn't accidentally
// scoped to plain identifiers only.
func TestMultiAssignStmtShape(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "single-target = is completely unchanged",
			src:  "x = f()",
			want: "" +
				"AssignStmt \"=\"\n" +
				"  Ident \"x\"\n" +
				"  CallExpr\n" +
				"    Ident \"f\"\n",
		},
		{
			name: "two-target destructuring =, plain idents",
			src:  "a, b = f()",
			want: "" +
				"MultiAssignStmt \"=\"\n" +
				"  Ident \"a\"\n" +
				"  Ident \"b\"\n" +
				"  CallExpr\n" +
				"    Ident \"f\"\n",
		},
		{
			name: "member-expr and index-expr targets, not just idents",
			src:  "p.x, arr[0] = f()",
			want: "" +
				"MultiAssignStmt \"=\"\n" +
				"  MemberExpr \"x\"\n" +
				"    Ident \"p\"\n" +
				"  IndexExpr\n" +
				"    Ident \"arr\"\n" +
				"    NumberLit \"0\"\n" +
				"  CallExpr\n" +
				"    Ident \"f\"\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := New(lexer.NewFile("t.ll", tt.src))
			n := p.parseSimpleStmt()
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

// TestReturnStmtSingleValueUnchanged proves a plain single-value `return`
// (including a bare `return`) never produces a MultiValueExpr wrapper - only
// a genuine comma-separated list does (see TestFuncDeclMultiReturnTypeShape
// above for the multi-value case, exercised via a full FuncDecl since a bare
// ReturnStmt needs an enclosing function to parse in context-free isolation
// only as far as parseStmt cares, which parseReturnStmt already handles).
func TestReturnStmtSingleValueUnchanged(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "bare return",
			src:  "return",
			want: "ReturnStmt \"return\"\n  <missing>\n",
		},
		{
			name: "single-value return",
			src:  "return 5",
			want: "ReturnStmt \"return\"\n  NumberLit \"5\"\n",
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

// TestParallelMultiAssignShape covers this round's own general Go-style
// parallel multi-assignment (`a, b := 1, 2`/`a, b = 1, 2` - each side
// individually evaluated and paired positionally, nothing to do with a
// multi-return call at all - see LANGUAGE.md's "Go-style multi-return
// values" section): finishMultiShortVarDecl/finishMultiAssignStmt build a
// MultiValueExpr wrapping every comma-separated value, the identical
// wrap-the-variable-arity-part convention parseReturnStmt's own multi-value
// `return a, b, ...` already uses (see TestFuncDeclMultiReturnTypeShape
// above) - occupying the exact same trailing "value" slot the existing
// CallExpr/IndexExpr shapes (TestMultiShortVarDeclShape/
// TestMultiAssignStmtShape above) already sit in.
func TestParallelMultiAssignShape(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "two-value parallel := ",
			src:  "a, b := 1, 2",
			want: "" +
				"MultiShortVarDecl \":=\"\n" +
				"  Ident \"a\"\n" +
				"  Ident \"b\"\n" +
				"  MultiValueExpr\n" +
				"    NumberLit \"1\"\n" +
				"    NumberLit \"2\"\n",
		},
		{
			name: "the swap idiom",
			src:  "a, b = b, a",
			want: "" +
				"MultiAssignStmt \"=\"\n" +
				"  Ident \"a\"\n" +
				"  Ident \"b\"\n" +
				"  MultiValueExpr\n" +
				"    Ident \"b\"\n" +
				"    Ident \"a\"\n",
		},
		{
			name: "mixed-type positions",
			src:  "x, s := 5, \"hi\"",
			want: "" +
				"MultiShortVarDecl \":=\"\n" +
				"  Ident \"x\"\n" +
				"  Ident \"s\"\n" +
				"  MultiValueExpr\n" +
				"    NumberLit \"5\"\n" +
				"    StringLit \"\\\"hi\\\"\"\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := New(lexer.NewFile("t.ll", tt.src))
			n := p.parseSimpleStmt()
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

// TestSingleTargetValueCountMismatchRejectedCleanly covers this round's own
// nice-to-have: a single-target `a := 1, 2`/`a = 1, 2` never reaches
// finishMultiShortVarDecl/finishMultiAssignStmt at all (those need a comma
// *before* `:=`/`=` too - see finishMultiTargetStmt) - it goes through the
// separate single-name finishShortVarDecl/finishAssignStmt instead, which
// would otherwise leave the trailing `, 2` unconsumed for the enclosing
// statement-list's own separator check to choke on (a confusing raw
// "expected ; found ','"). reportSingleTargetValueCountMismatch upgrades this
// to a real, clean "assignment mismatch" diagnostic instead.
func TestSingleTargetValueCountMismatchRejectedCleanly(t *testing.T) {
	tests := []struct {
		name string
		src  string
	}{
		{name: "short var decl", src: "a := 1, 2"},
		{name: "plain assignment", src: "a = 1, 2"},
		{name: "three values", src: "a := 1, 2, 3"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := New(lexer.NewFile("t.ll", tt.src))
			p.parseSimpleStmt()
			if !p.diags.HasErrors() {
				t.Fatalf("expected a parse error for %q, got none", tt.src)
			}
		})
	}
}

// TestMultiTargetStmtMissingAssignOpRejectedCleanly proves a comma-separated
// list followed by neither `:=` nor `=` (e.g. a compound-assignment operator,
// which makes no sense against more than one target at once) is a clean
// diagnostic, not a panic.
func TestMultiTargetStmtMissingAssignOpRejectedCleanly(t *testing.T) {
	p := New(lexer.NewFile("t.ll", "a, b += f()"))
	p.parseSimpleStmt()
	if !p.diags.HasErrors() {
		t.Fatalf("expected a parse error for a multi-target += statement, got none")
	}
}
