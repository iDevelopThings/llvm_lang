package parser

import (
	"testing"

	"llvm_lang/src/ast"
	"llvm_lang/src/enums"
	"llvm_lang/src/lexer"
)

// TestMatchExprBareArmDesugarsToYield covers parseMatchExprArm's own
// bare-expression-arm desugaring (see ast.Node's own MatchArm/YieldStmt doc
// comments and LANGUAGE.md's "match" section's "match as an expression"
// subsection): `pattern => expr` (no braces) must produce a synthetic
// single-statement Block wrapping a synthetic YieldStmt around expr - the
// one canonical arm-body shape sema/codegen ever see, regardless of which
// surface form was actually written.
func TestMatchExprBareArmDesugarsToYield(t *testing.T) {
	src := `func f() {
	x := match s {
		"a" => 1
		_ => 2
	}
}`
	p := New(lexer.NewFile("t.ll", src))
	n := p.parseTopLevelItem()
	if p.diags.HasErrors() {
		t.Fatalf("unexpected parse errors: %v", p.diags.All())
	}
	body := p.tree.FuncBody(n)
	shortVarDecl := p.tree.Child(body, 0)
	matchExpr := p.tree.Child(shortVarDecl, 1)
	if kind := p.tree.Nodes[matchExpr].Kind.String(); kind != "MatchStmt" {
		t.Fatalf("`:=` init kind = %s, want MatchStmt", kind)
	}

	arms := p.tree.MatchArms(matchExpr)
	if len(arms) != 2 {
		t.Fatalf("len(arms) = %d, want 2", len(arms))
	}

	for i, want := range []string{"1", "2"} {
		armBody := p.tree.MatchArmBody(arms[i])
		if kind := p.tree.Nodes[armBody].Kind.String(); kind != "Block" {
			t.Fatalf("arm %d body kind = %s, want Block", i, kind)
		}
		stmts := p.tree.Children(armBody)
		if len(stmts) != 1 {
			t.Fatalf("arm %d body has %d statements, want 1 (the synthetic yield)", i, len(stmts))
		}
		yieldStmt := stmts[0]
		if kind := p.tree.Nodes[yieldStmt].Kind.String(); kind != "YieldStmt" {
			t.Fatalf("arm %d body's sole statement kind = %s, want YieldStmt", i, kind)
		}
		value := p.tree.Child(yieldStmt, 0)
		if got := p.tree.Text(value); got != want {
			t.Errorf("arm %d yielded value = %q, want %q", i, got, want)
		}
	}
}

// TestMatchExprBlockArmStaysBlock covers the other arm-body shape: a real
// brace-delimited block, containing an explicit `yield` (possibly nested
// inside an if, with multiple yields) - parsed completely unchanged via
// parseBlock, no synthetic wrapping at all.
func TestMatchExprBlockArmStaysBlock(t *testing.T) {
	src := `func f() {
	x := match s {
		"s" => {
			if special {
				yield "small-but-special"
			}
			yield "small"
		}
		_ => "other"
	}
}`
	p := New(lexer.NewFile("t.ll", src))
	n := p.parseTopLevelItem()
	if p.diags.HasErrors() {
		t.Fatalf("unexpected parse errors: %v", p.diags.All())
	}
	body := p.tree.FuncBody(n)
	shortVarDecl := p.tree.Child(body, 0)
	matchExpr := p.tree.Child(shortVarDecl, 1)
	arms := p.tree.MatchArms(matchExpr)
	if len(arms) != 2 {
		t.Fatalf("len(arms) = %d, want 2", len(arms))
	}

	blockArmBody := p.tree.MatchArmBody(arms[0])
	if kind := p.tree.Nodes[blockArmBody].Kind.String(); kind != "Block" {
		t.Fatalf("block arm body kind = %s, want Block", kind)
	}
	stmts := p.tree.Children(blockArmBody)
	if len(stmts) != 2 {
		t.Fatalf("block arm body has %d statements, want 2 (if, yield)", len(stmts))
	}
	if kind := p.tree.Nodes[stmts[0]].Kind.String(); kind != "IfStmt" {
		t.Errorf("block arm's first statement kind = %s, want IfStmt", kind)
	}
	if kind := p.tree.Nodes[stmts[1]].Kind.String(); kind != "YieldStmt" {
		t.Errorf("block arm's second statement kind = %s, want YieldStmt", kind)
	}

	// The nested yield inside the if's own then-branch.
	thenBranch := p.tree.Child(stmts[0], 1)
	nestedStmts := p.tree.Children(thenBranch)
	if len(nestedStmts) != 1 || p.tree.Nodes[nestedStmts[0]].Kind.String() != "YieldStmt" {
		t.Errorf("if's then-branch = %v, want a single nested YieldStmt", nestedStmts)
	}

	// The wildcard arm is still a bare-expression arm, desugared exactly
	// like TestMatchExprBareArmDesugarsToYield covers - both arm shapes
	// coexist freely in the very same match.
	wildcardBody := p.tree.MatchArmBody(arms[1])
	if kind := p.tree.Nodes[wildcardBody].Kind.String(); kind != "Block" {
		t.Fatalf("wildcard arm body kind = %s, want Block", kind)
	}
	if len(p.tree.Children(wildcardBody)) != 1 {
		t.Fatalf("wildcard arm body has %d statements, want 1", len(p.tree.Children(wildcardBody)))
	}
}

// TestMatchExprAsCallArgument covers `match` parsed anywhere an expression
// is legal, not just a `:=` right-hand side - a function call argument -
// proving parseMatchExpr is wired into the Pratt prefix table generally
// (parseIdentExpr's own Keywords.Match case), not special-cased to one
// grammar position.
func TestMatchExprAsCallArgument(t *testing.T) {
	src := `func f() {
	print(match x {
		1 => "one"
		_ => "other"
	})
}`
	p := New(lexer.NewFile("t.ll", src))
	n := p.parseTopLevelItem()
	if p.diags.HasErrors() {
		t.Fatalf("unexpected parse errors: %v", p.diags.All())
	}
	body := p.tree.FuncBody(n)
	exprStmt := p.tree.Child(body, 0)
	call := p.tree.Child(exprStmt, 0)
	if kind := p.tree.Nodes[call].Kind.String(); kind != "CallExpr" {
		t.Fatalf("statement kind = %s, want CallExpr", kind)
	}
	arg := p.tree.Child(call, 1)
	if kind := p.tree.Nodes[arg].Kind.String(); kind != "MatchStmt" {
		t.Fatalf("call argument kind = %s, want MatchStmt", kind)
	}
}

// TestBareMatchStmtStillStatementMode is the regression test explicitly
// called for by this round's brief: a bare top-level `match x {...}`
// statement (no assignment, no wrapping expression) must still parse via
// the existing, unchanged parseMatchStmt path - never reaching
// parseMatchExpr at all - because parseStmt's own keyword-first dispatch
// checks Match before ever falling through to expression parsing (see
// parseStmt's own doc comment). The clearest proof: a bare-expression arm
// (`pattern => expr`, no braces) is only legal grammar for an
// EXPRESSION-position match (parseMatchExprArm) - parseMatchArm (the
// statement-mode arm parser) always requires a block, so writing one at
// statement position must still be a parse error, exactly as it always was
// before this round ever existed.
func TestBareMatchStmtStillStatementMode(t *testing.T) {
	src := `func f() {
	match x {
		1 => {
			print(1)
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
	if kind := p.tree.Nodes[matchStmt].Kind.String(); kind != "MatchStmt" {
		t.Fatalf("bare match statement kind = %s, want MatchStmt (not wrapped in ExprStmt)", kind)
	}
	arms := p.tree.MatchArms(matchStmt)
	if len(arms) != 2 {
		t.Fatalf("len(arms) = %d, want 2", len(arms))
	}
	for i, arm := range arms {
		if kind := p.tree.Nodes[p.tree.MatchArmBody(arm)].Kind.String(); kind != "Block" {
			t.Errorf("arm %d body kind = %s, want Block (statement-mode arms are always real blocks)", i, kind)
		}
	}

	// Now the negative half: the identical bare-expression-arm shape that's
	// legal in expression mode must still be REJECTED at statement position.
	badSrc := `func f() {
	match x {
		1 => "one"
		_ => "other"
	}
}`
	p2 := New(lexer.NewFile("t2.ll", badSrc))
	p2.parseTopLevelItem()
	if !p2.diags.HasErrors() {
		t.Error("expected a parse error for a bare-expression arm at statement position, got none")
	}
}

// TestYieldStmtShape covers YieldStmt's own bare grammar shape - the
// grammar itself places no restriction on where `yield` may appear (that's
// sema's job, checkYieldStmt - see LANGUAGE.md's "match" section's "match
// as an expression" subsection); this only checks the node it parses into.
func TestYieldStmtShape(t *testing.T) {
	src := `func f() {
	yield 5
}`
	p := New(lexer.NewFile("t.ll", src))
	n := p.parseTopLevelItem()
	if p.diags.HasErrors() {
		t.Fatalf("unexpected parse errors: %v", p.diags.All())
	}
	body := p.tree.FuncBody(n)
	yieldStmt := p.tree.Child(body, 0)
	if kind := p.tree.Nodes[yieldStmt].Kind.String(); kind != "YieldStmt" {
		t.Fatalf("statement kind = %s, want YieldStmt", kind)
	}
	if p.tree.Nodes[yieldStmt].Tok.Keyword != enums.Keywords.Yield {
		t.Errorf("YieldStmt.Tok.Keyword = %q, want Yield", p.tree.Nodes[yieldStmt].Tok.Keyword)
	}
	value := p.tree.Child(yieldStmt, 0)
	if value == ast.InvalidNode {
		t.Fatal("YieldStmt's value child = InvalidNode, want a real expression")
	}
	if got := p.tree.Text(value); got != "5" {
		t.Errorf("yielded value = %q, want \"5\"", got)
	}
}
