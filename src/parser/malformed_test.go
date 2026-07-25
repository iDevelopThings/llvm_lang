package parser

import (
	"strings"
	"testing"

	"llvm_lang/src/ast"
	"llvm_lang/src/enums"
	"llvm_lang/src/lexer"
)

// This file is the "should definitely not work" counterpart to
// expr_test.go/stmt_test.go's shape tests: malformed input that must be
// rejected outright, or that must degrade to a bounded number of
// diagnostics and a still-usable tree rather than hanging or crashing.

func TestKeywordRejectedAsVarName(t *testing.T) {
	p := New(lexer.NewFile("t.ll", "var if = 5"))
	p.parseStmt()
	if p.diags.ErrorCount() != 1 {
		t.Fatalf("ErrorCount = %d, want 1: %v", p.diags.ErrorCount(), p.diags.All())
	}
}

func TestKeywordRejectedAsTypeName(t *testing.T) {
	p := New(lexer.NewFile("t.ll", "var a var"))
	p.parseStmt()
	if p.diags.ErrorCount() != 1 {
		t.Fatalf("ErrorCount = %d, want 1: %v", p.diags.ErrorCount(), p.diags.All())
	}
}

// TestKeywordAllowedAsMemberField covers expectMemberName's own contract
// (see its doc comment): a member-access name after `.` is never confused
// with any keyword's own expression-position grammar (there's no `if x.y`
// ambiguity the way a bare `var if = 5`/`if` used as a value would create),
// so `a.if` parses cleanly as a real MemberExpr rather than being rejected
// the way TestKeywordRejectedAsVarName's own `var if = 5` still is.
func TestKeywordAllowedAsMemberField(t *testing.T) {
	p := New(lexer.NewFile("t.ll", "a.if"))
	n := p.parseExpr(precLowest)
	if p.diags.ErrorCount() != 0 {
		t.Fatalf("ErrorCount = %d, want 0: %v", p.diags.ErrorCount(), p.diags.All())
	}
	if p.tree.Nodes[n].Kind != enums.NodeKinds.MemberExpr {
		t.Fatalf("Kind = %s, want MemberExpr", p.tree.Nodes[n].Kind)
	}
	if got := p.tree.Text(n); got != "if" {
		t.Errorf("member name text = %q, want %q", got, "if")
	}
}

// TestKeywordRejectedAsMethodConstructorOrDestructor covers the one
// exclusion within expectMemberName's own relaxation (see parseFuncDecl's
// doc comment): `constructor`/`destructor` already name a completely
// different, unnamed struct-level construct, so a METHOD (unlike a plain
// field, or any other keyword-spelled method name) still can't be named
// either one - caught by review before landing; found by hands-on testing
// that `func (Point) constructor(...)` silently compiled and coexisted
// with a struct's own real constructor block with zero diagnostic.
func TestKeywordRejectedAsMethodConstructorOrDestructor(t *testing.T) {
	for _, kw := range []string{"constructor", "destructor"} {
		p := New(lexer.NewFile("t.ll", "func (Point) "+kw+"() {}"))
		p.parseFuncDecl()
		if p.diags.ErrorCount() != 1 {
			t.Errorf("%s: ErrorCount = %d, want 1: %v", kw, p.diags.ErrorCount(), p.diags.All())
		}
	}
}

// TestKeywordRejectedAsFreeFuncName covers the boundary expectMemberName's
// own doc comment draws: unlike a method (only ever reached through
// `receiver.name(...)`), a free function's own name can stand alone as a
// bare called value (`move()`) - and `move` always dispatches to its own
// MoveExpr prefix rule wherever a bare identifier could appear as a value
// (parseIdentExpr), so a free function literally named `move` would make
// `move` itself uncallable as a value ever again. parseFuncDecl must still
// reject this via expectIdent, exactly like a var name.
func TestKeywordRejectedAsFreeFuncName(t *testing.T) {
	p := New(lexer.NewFile("t.ll", "func move() {}"))
	p.parseTopLevelItem()
	if p.diags.ErrorCount() != 1 {
		t.Fatalf("ErrorCount = %d, want 1: %v", p.diags.ErrorCount(), p.diags.All())
	}
}

func TestKeywordAsNameStillRecoversRestOfDecl(t *testing.T) {
	// The bad name shouldn't derail the rest of the declaration: type and
	// initializer must still parse normally.
	p := New(lexer.NewFile("t.ll", "var if int = 5"))
	n := p.parseStmt()
	if p.diags.ErrorCount() != 1 {
		t.Fatalf("ErrorCount = %d, want 1: %v", p.diags.ErrorCount(), p.diags.All())
	}
	typ := p.tree.Child(n, 1)
	value := p.tree.Child(n, 2)
	if got := p.tree.Text(typ); got != "int" {
		t.Errorf("type = %q, want %q", got, "int")
	}
	if got := p.tree.Text(value); got != "5" {
		t.Errorf("value = %q, want %q", got, "5")
	}
}

func TestMismatchedClosingDelimiter(t *testing.T) {
	p := New(lexer.NewFile("t.ll", "(1 + 2]"))
	p.parseExpr(precLowest)
	if p.diags.ErrorCount() != 1 {
		t.Fatalf("ErrorCount = %d, want 1: %v", p.diags.ErrorCount(), p.diags.All())
	}
}

func TestUnclosedParenExpr(t *testing.T) {
	p := New(lexer.NewFile("t.ll", "(1 + 2"))
	p.parseExpr(precLowest)
	if p.diags.ErrorCount() != 1 {
		t.Fatalf("ErrorCount = %d, want 1: %v", p.diags.ErrorCount(), p.diags.All())
	}
}

func TestUnclosedIndexExpr(t *testing.T) {
	p := New(lexer.NewFile("t.ll", "a[0"))
	p.parseExpr(precLowest)
	if p.diags.ErrorCount() != 1 {
		t.Fatalf("ErrorCount = %d, want 1: %v", p.diags.ErrorCount(), p.diags.All())
	}
}

func TestUnclosedBlock(t *testing.T) {
	p := New(lexer.NewFile("t.ll", "{ a := 1"))
	p.parseStmt()
	if p.diags.ErrorCount() != 1 {
		t.Fatalf("ErrorCount = %d, want 1: %v", p.diags.ErrorCount(), p.diags.All())
	}
}

func TestBinaryOpMissingRightOperand(t *testing.T) {
	p := New(lexer.NewFile("t.ll", "1 +"))
	p.parseExpr(precLowest)
	if p.diags.ErrorCount() != 1 {
		t.Fatalf("ErrorCount = %d, want 1: %v", p.diags.ErrorCount(), p.diags.All())
	}
}

func TestLeadingPlusNotSupported(t *testing.T) {
	// Unlike `-`/`!`, `+` isn't registered as a unary prefix operator - a
	// leading `+` must be rejected, not silently accepted as a no-op.
	p := New(lexer.NewFile("t.ll", "+1"))
	p.parseExpr(precLowest)
	if p.diags.ErrorCount() != 1 {
		t.Fatalf("ErrorCount = %d, want 1: %v", p.diags.ErrorCount(), p.diags.All())
	}
}

func TestEmptyInputAsExpression(t *testing.T) {
	p := New(lexer.NewFile("t.ll", ""))
	n := p.parseExpr(precLowest)
	if !p.diags.HasErrors() {
		t.Fatalf("expected an error for empty input")
	}
	if got := p.tree.Nodes[n].Kind.String(); got != "Bad" {
		t.Fatalf("node kind = %s, want Bad", got)
	}
}

func TestLexErrorFlowsThroughCleanlyIntoStmt(t *testing.T) {
	// The lexer's own error (unterminated string) must show up exactly
	// once, and must not stop the rest of the statement from parsing
	// normally - a String token is a String token as far as the parser's
	// concerned, whatever the lexer flagged about it.
	p := New(lexer.NewFile("t.ll", `x := "unterminated`))
	n := p.parseStmt()
	if p.diags.ErrorCount() != 1 {
		t.Fatalf("ErrorCount = %d, want 1: %v", p.diags.ErrorCount(), p.diags.All())
	}
	if got := p.tree.Nodes[n].Kind.String(); got != "ShortVarDecl" {
		t.Fatalf("node kind = %s, want ShortVarDecl", got)
	}
}

func TestBadShortVarDeclTargetReportsOnce(t *testing.T) {
	// `struct` isn't dispatched as its own statement yet (that's decl.go's
	// job, not built), so it falls through to parseSimpleStmt and is
	// rejected in expression position (parseIdentExpr), producing a Bad
	// node; finishShortVarDecl must not pile a second "left side of :=
	// must be an identifier" error on top of that same root cause. (`if`
	// doesn't work for this test: it's dispatched straight to parseIfStmt
	// before ever reaching parseSimpleStmt. `func` doesn't work for this
	// test either, not anymore: it's now a legal expression prefix in its
	// own right - a function-literal expression, see parseFuncLit/
	// LANGUAGE.md's "Lambdas" section - so `func := 5` no longer exercises
	// this "unhandled keyword in expression position" path at all.)
	p := New(lexer.NewFile("t.ll", "struct := 5"))
	p.parseStmt()
	if p.diags.ErrorCount() != 1 {
		t.Fatalf("ErrorCount = %d, want 1 (no duplicate diagnostic): %v", p.diags.ErrorCount(), p.diags.All())
	}
}

func TestStrayDoubleSemicolonRecovers(t *testing.T) {
	// Not something we set out to support, but it must degrade gracefully:
	// bounded errors, and both real statements still end up in the tree.
	p := New(lexer.NewFile("t.ll", "{ a := 1;; b := 2 }"))
	n := p.parseStmt()
	if !p.diags.HasErrors() {
		t.Fatalf("expected at least one diagnostic for the stray ';'")
	}
	if p.diags.ErrorCount() >= maxErrors {
		t.Fatalf("ErrorCount = %d hit the bailout threshold on trivial input", p.diags.ErrorCount())
	}
	var names []string
	for _, kid := range p.tree.Children(n) {
		if p.tree.Nodes[kid].Kind == enums.NodeKinds.ShortVarDecl {
			names = append(names, p.tree.Text(p.tree.Child(kid, 0)))
		}
	}
	if len(names) != 2 || names[0] != "a" || names[1] != "b" {
		t.Fatalf("expected both a and b to survive recovery, got %v\n%s", names, p.tree.Dump(n))
	}
}

func TestChainedShortVarDeclRecovers(t *testing.T) {
	// `:=` is a statement-level construct, not an expression operator, so
	// `a := b := c` can't chain the way it might look like it should -
	// must not hang or crash, just accumulate bounded, recoverable errors.
	p := New(lexer.NewFile("t.ll", "{ a := b := c }"))
	n := p.parseStmt()
	if !p.diags.HasErrors() {
		t.Fatalf("expected at least one diagnostic")
	}
	if p.diags.ErrorCount() >= maxErrors {
		t.Fatalf("ErrorCount = %d hit the bailout threshold on trivial input", p.diags.ErrorCount())
	}
	if len(p.tree.Children(n)) == 0 {
		t.Fatalf("expected at least one recovered statement, got none\n%s", p.tree.Dump(n))
	}
}

func TestTrailingCommaInCallArgs(t *testing.T) {
	// A trailing comma in call arguments is now tolerated, the same as
	// finishCompositeLit already tolerates one in a composite literal's
	// element list (`Point{1, 2,}`) - previously this cost a couple of
	// stacked diagnostics instead of parsing cleanly.
	p := New(lexer.NewFile("t.ll", "f(a, b,)"))
	n := p.parseExpr(precLowest)
	if p.diags.HasErrors() {
		t.Fatalf("unexpected diagnostics for a trailing comma in call args: %v", p.diags.All())
	}
	args := p.tree.Children(n)
	if len(args) != 3 { // callee + 2 args
		t.Fatalf("expected callee + 2 args, got %d children\n%s", len(args), p.tree.Dump(n))
	}
	names := []string{p.tree.Text(args[1]), p.tree.Text(args[2])}
	if names[0] != "a" || names[1] != "b" {
		t.Fatalf("expected args a, b, got %v", names)
	}
}

func TestTrailingCommaInParamList(t *testing.T) {
	// A trailing comma in a function's parameter list is now tolerated, the
	// same as parseCallExpr/finishCompositeLit already tolerate one - both
	// now share parseCommaList with parseParamList, which previously used
	// its own hand-rolled loop with no trailing-comma tolerance at all
	// (`func f(a int, b int,) {}` used to cost a spurious "expected
	// identifier, found )" error).
	p := New(lexer.NewFile("t.ll", "func f(a int, b int,) {}"))
	n := p.parseTopLevelItem()
	if p.diags.HasErrors() {
		t.Fatalf("unexpected diagnostics for a trailing comma in a param list: %v", p.diags.All())
	}
	params := p.tree.Children(p.tree.Child(n, 3)) // FuncDecl -> ParamList
	if len(params) != 2 {
		t.Fatalf("expected 2 params, got %d\n%s", len(params), p.tree.Dump(n))
	}
	names := []string{p.tree.Text(p.tree.Child(params[0], 0)), p.tree.Text(p.tree.Child(params[1], 0))}
	if names[0] != "a" || names[1] != "b" {
		t.Fatalf("expected params a, b, got %v", names)
	}
}

func TestTrailingCommaInFuncTypeParamList(t *testing.T) {
	// Same fix, for parseFuncType's own parameter-*type* loop
	// (`var x func(int, int,)` used to fail the same way parseParamList did).
	p := New(lexer.NewFile("t.ll", "var x func(int, int,)"))
	n := p.parseStmt()
	if p.diags.HasErrors() {
		t.Fatalf("unexpected diagnostics for a trailing comma in a func-type param list: %v", p.diags.All())
	}
	funcType := p.tree.Child(n, 1)                           // VarDecl -> FuncType
	paramTypes := p.tree.Children(p.tree.Child(funcType, 0)) // FuncType -> ParamTypeList
	if len(paramTypes) != 2 {
		t.Fatalf("expected 2 param types, got %d\n%s", len(paramTypes), p.tree.Dump(n))
	}
	if got := p.tree.Text(paramTypes[0]); got != "int" {
		t.Errorf("paramTypes[0] = %q, want %q", got, "int")
	}
	if got := p.tree.Text(paramTypes[1]); got != "int" {
		t.Errorf("paramTypes[1] = %q, want %q", got, "int")
	}
}

func TestManyIllegalCharactersBailOutCleanly(t *testing.T) {
	// Each illegal '$' costs two diagnostics (one lexical, one "expected
	// expression"), so this should trip the bailout well before the block
	// closes - proving Run's recover works from deep inside real
	// statement/block parsing, not just the flat expression-level case
	// already covered in parser_test.go.
	src := "{ " + strings.Repeat("$ ", 15) + "}"
	_, diags := Run(lexer.NewFile("t.ll", src), func(p *Parser) ast.NodeIndex {
		return p.parseBlock()
	})
	if diags.ErrorCount() != maxErrors {
		t.Fatalf("ErrorCount = %d, want exactly maxErrors=%d (bailout should cap it)", diags.ErrorCount(), maxErrors)
	}
}
