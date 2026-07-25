package parser

import (
	"testing"

	"llvm_lang/src/lexer"
)

// This file covers this round's new grammar for coroutines (see LANGUAGE.md's
// "Coroutines" section): an optional leading `async` on a FuncDecl (carried
// as its own Tok, per Node's own FuncDecl doc comment - see
// TestAsyncFuncDeclShape) and a bare `await` statement (AwaitStmt, no
// children - see TestAwaitStmtShape), matching generator_test.go's own
// Tree.Dump shape-assertion convention.

func TestAsyncFuncDeclShape(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "async func - Tok is the async keyword, not func",
			src:  "async func Coro() { }",
			want: "" +
				"FuncDecl \"async\"\n" +
				"  <missing>\n" +
				"  Ident \"Coro\"\n" +
				"  <missing>\n" +
				"  ParamList\n" +
				"  <missing>\n" +
				"  Block\n",
		},
		{
			name: "plain func is completely unchanged - Tok is func",
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

// TestAsyncFuncIsAsyncAccessor proves Tree.FuncIsAsync reads exactly what
// TestAsyncFuncDeclShape's Dump output shows structurally.
func TestAsyncFuncIsAsyncAccessor(t *testing.T) {
	p := New(lexer.NewFile("t.ll", "async func Coro() { }"))
	n := p.parseTopLevelItem()
	if p.diags.HasErrors() {
		t.Fatalf("unexpected parse errors: %v", p.diags.All())
	}
	if !p.tree.FuncIsAsync(n) {
		t.Error("FuncIsAsync(async func) = false, want true")
	}

	p2 := New(lexer.NewFile("t.ll", "func f() { }"))
	n2 := p2.parseTopLevelItem()
	if p2.diags.HasErrors() {
		t.Fatalf("unexpected parse errors: %v", p2.diags.All())
	}
	if p2.tree.FuncIsAsync(n2) {
		t.Error("FuncIsAsync(plain func) = true, want false")
	}
}

// TestAsyncFuncDeclWithReceiverParsesFine proves the grammar itself doesn't
// know yet whether async combines with a receiver clause - sema is what
// rejects a method being async (see sema.checkFuncDecl's "a method cannot be
// an async function" diagnostic), the same "grammar accepts the general
// shape, sema enforces the narrower rule" division of labor
// TestFuncDeclYieldReturnTypeOnMethodParsesFine already uses one construct
// over.
func TestAsyncFuncDeclWithReceiverParsesFine(t *testing.T) {
	src := "async func (Point) Move() { }"
	p := New(lexer.NewFile("t.ll", src))
	p.parseTopLevelItem()
	if p.diags.HasErrors() {
		t.Fatalf("unexpected parse errors for %q: %v", src, p.diags.All())
	}
}

// TestAsyncOnFuncLitIsParseError proves `async` is structurally impossible
// before a FuncLit - unlike a top-level FuncDecl, parseFuncLit's own parse
// function never checks for a leading async keyword at all (see
// parseFuncDecl's own doc comment), so `async` in expression position is
// just an ordinary parse error, not a silently-accepted async lambda.
func TestAsyncOnFuncLitIsParseError(t *testing.T) {
	src := "var x = async func() { }"
	p := New(lexer.NewFile("t.ll", src))
	p.parseTopLevelItem()
	if !p.diags.HasErrors() {
		t.Fatalf("expected a parse error for `async` before a FuncLit, got none")
	}
}

func TestAwaitStmtShape(t *testing.T) {
	src := "async func Coro() { await }"
	p := New(lexer.NewFile("t.ll", src))
	n := p.parseTopLevelItem()
	if p.diags.HasErrors() {
		t.Fatalf("unexpected parse errors for %q: %v", src, p.diags.All())
	}
	want := "" +
		"FuncDecl \"async\"\n" +
		"  <missing>\n" +
		"  Ident \"Coro\"\n" +
		"  <missing>\n" +
		"  ParamList\n" +
		"  <missing>\n" +
		"  Block\n" +
		"    AwaitStmt \"await\"\n"
	got := p.tree.Dump(n)
	if got != want {
		t.Errorf("Dump(%q):\n got:\n%s\nwant:\n%s", src, got, want)
	}
}

// TestAwaitStmtNestedAnyDepthParsesFine proves `await` is grammatically
// legal at any nesting depth inside a function body (sema, not this
// grammar, restricts it to an async function's own body - see
// sema.checkAwaitStmt) - mirroring break/continue's own "legal anywhere,
// sema-restricted" grammar shape.
func TestAwaitStmtNestedAnyDepthParsesFine(t *testing.T) {
	src := "func f() { if true { for { await } } }"
	p := New(lexer.NewFile("t.ll", src))
	p.parseTopLevelItem()
	if p.diags.HasErrors() {
		t.Fatalf("unexpected parse errors for %q: %v", src, p.diags.All())
	}
}
