package parser

import (
	"llvm_lang/src/ast"
	"llvm_lang/src/enums"
	"llvm_lang/src/lexer"
)

// assignOps are the lexemes finishSimpleStmt recognizes as turning an
// already-parsed expression into an AssignStmt - populated in init for the
// same reason expr.go's tables are (see expr.go's prefixFns/infixRules
// comment): it's simplest to keep every lookup table built the same way.
var assignOps map[enums.Lexeme]bool

func init() {
	assignOps = map[enums.Lexeme]bool{
		enums.Lexemes.Equal:         true,
		enums.Lexemes.PlusEqual:     true,
		enums.Lexemes.MinusEqual:    true,
		enums.Lexemes.AsteriskEqual: true,
		enums.Lexemes.SlashEqual:    true,
	}
}

// parseStmt dispatches on the current token to the right statement grammar
// rule. Keyword-led statements are checked first since they all lex as
// Lexeme.Identifier and are only distinguished by Token.Keyword; anything
// else falls through to parseSimpleStmt (short-var-decl, assignment, or a
// bare expression).
func (p *Parser) parseStmt() ast.NodeIndex {
	switch {
	case p.at(enums.Lexemes.LeftBrace):
		return p.parseBlock()
	case p.atKeyword(enums.Keywords.Var):
		return p.parseVarDecl()
	case p.atKeyword(enums.Keywords.If):
		return p.parseIfStmt()
	case p.atKeyword(enums.Keywords.For):
		return p.parseForStmt()
	case p.atKeyword(enums.Keywords.Return):
		return p.parseReturnStmt()
	case p.atKeyword(enums.Keywords.Break):
		return p.parseBreakStmt()
	case p.atKeyword(enums.Keywords.Continue):
		return p.parseContinueStmt()
	default:
		return p.parseSimpleStmt()
	}
}

// parseBlock parses a brace-delimited statement list. The last statement
// before `}` may omit its terminating semicolon (matching Go: ASI already
// covers the common case of a newline before `}`, this covers the same-line
// case too, e.g. `{ return 1 }`); anywhere else a semicolon is required.
func (p *Parser) parseBlock() ast.NodeIndex {
	openTok := p.expect(enums.Lexemes.LeftBrace)
	var stmts []ast.NodeIndex
	for !p.at(enums.Lexemes.RightBrace) && !p.at(enums.Lexemes.EOF) {
		stmts = append(stmts, p.parseStmt())
		if p.at(enums.Lexemes.RightBrace) || p.at(enums.Lexemes.EOF) {
			break
		}
		p.expect(enums.Lexemes.Semicolon)
	}
	closeTok := p.expect(enums.Lexemes.RightBrace)
	span := ast.Span{
		Start: openTok.Start,
		End:   closeTok.End,
	}
	return p.tree.NewNode(enums.NodeKinds.Block, lexer.Token{}, span, stmts...)
}

// parseTypeExpr parses a type reference: a bare identifier (int, string,
// bool, or a struct name), or an array type `[N]T` (fixed-size) / `[]T`
// (dynamic - parsed now, rejected later at a semantic stage once one
// exists, so the grammar doesn't need to change when dynamic arrays land).
func (p *Parser) parseTypeExpr() ast.NodeIndex {
	if openTok, ok := p.accept(enums.Lexemes.LeftBracket); ok {
		p.exprLev++
		size := ast.InvalidNode
		if !p.at(enums.Lexemes.RightBracket) {
			size = p.parseExpr(precLowest)
		}
		p.exprLev--
		p.expect(enums.Lexemes.RightBracket)
		elem := p.parseTypeExpr()
		span := ast.Span{
			Start: openTok.Start,
			End:   p.tree.SpanOf(elem).End,
		}
		return p.tree.NewNode(enums.NodeKinds.ArrayType, lexer.Token{}, span, size, elem)
	}
	nameTok := p.expectIdent()
	return p.tree.NewNode(enums.NodeKinds.Ident, nameTok, tokenSpan(nameTok))
}

// parseVarDecl parses `var name Type`, `var name = Expr`, or
// `var name Type = Expr` - type and initializer are each individually
// optional at the grammar level (that at least one must be present is a
// sema concern, not a parse error).
func (p *Parser) parseVarDecl() ast.NodeIndex {
	kwTok := p.expectKeyword(enums.Keywords.Var)
	nameTok := p.expectIdent()
	name := p.tree.NewNode(enums.NodeKinds.Ident, nameTok, tokenSpan(nameTok))
	end := nameTok.End

	typ := ast.InvalidNode
	if !p.at(enums.Lexemes.Equal) && !p.at(enums.Lexemes.Semicolon) && !p.at(enums.Lexemes.EOF) {
		typ = p.parseTypeExpr()
		end = p.tree.SpanOf(typ).End
	}

	value := ast.InvalidNode
	if _, ok := p.accept(enums.Lexemes.Equal); ok {
		value = p.parseExpr(precLowest)
		end = p.tree.SpanOf(value).End
	}

	span := ast.Span{
		Start: kwTok.Start,
		End:   end,
	}
	return p.tree.NewNode(enums.NodeKinds.VarDecl, kwTok, span, name, typ, value)
}

// parseSimpleStmt parses whatever can appear as a for-loop init/post clause
// or a standalone statement that isn't keyword-led: a short-var-decl
// (`x := expr`), an assignment (`lvalue = expr`, including compound forms
// and ++/--), or a bare expression used as a statement.
func (p *Parser) parseSimpleStmt() ast.NodeIndex {
	expr := p.parseExpr(precLowest)

	switch {
	case p.at(enums.Lexemes.ColonEqual):
		return p.finishShortVarDecl(expr)
	case assignOps[p.tok.Lexeme]:
		return p.finishAssignStmt(expr)
	case p.at(enums.Lexemes.PlusPlus) || p.at(enums.Lexemes.MinusMinus):
		return p.finishIncDecStmt(expr)
	default:
		return p.tree.NewNode(enums.NodeKinds.ExprStmt, lexer.Token{}, p.tree.SpanOf(expr), expr)
	}
}

func (p *Parser) finishShortVarDecl(name ast.NodeIndex) ast.NodeIndex {
	// Bad is excluded here: if name already failed to parse, parseExpr
	// already reported why - piling "left side of := must be an
	// identifier" on top would just be a second, redundant diagnostic for
	// the same root cause.
	if kind := p.tree.Nodes[name].Kind; kind != enums.NodeKinds.Ident && kind != enums.NodeKinds.Bad {
		p.errorAt(p.tree.SpanOf(name).Start, "left side of := must be an identifier")
	}
	opTok := p.expect(enums.Lexemes.ColonEqual)
	value := p.parseExpr(precLowest)
	span := ast.Span{
		Start: p.tree.SpanOf(name).Start,
		End:   p.tree.SpanOf(value).End,
	}
	return p.tree.NewNode(enums.NodeKinds.ShortVarDecl, opTok, span, name, value)
}

func (p *Parser) finishAssignStmt(target ast.NodeIndex) ast.NodeIndex {
	p.checkAssignTarget(target)
	opTok := p.tok
	p.advance()
	value := p.parseExpr(precLowest)
	span := ast.Span{
		Start: p.tree.SpanOf(target).Start,
		End:   p.tree.SpanOf(value).End,
	}
	return p.tree.NewNode(enums.NodeKinds.AssignStmt, opTok, span, target, value)
}

func (p *Parser) finishIncDecStmt(target ast.NodeIndex) ast.NodeIndex {
	p.checkAssignTarget(target)
	opTok := p.tok
	p.advance()
	span := ast.Span{
		Start: p.tree.SpanOf(target).Start,
		End:   opTok.End,
	}
	return p.tree.NewNode(enums.NodeKinds.IncDecStmt, opTok, span, target)
}

// checkAssignTarget reports an error if target isn't a valid lvalue. The
// node is still built either way - this is diagnostic, not a parse failure.
// Bad is accepted same as a valid lvalue, for the same already-reported-once
// reason as finishShortVarDecl's check.
func (p *Parser) checkAssignTarget(target ast.NodeIndex) {
	switch p.tree.Nodes[target].Kind {
	case enums.NodeKinds.Ident, enums.NodeKinds.MemberExpr, enums.NodeKinds.IndexExpr, enums.NodeKinds.Bad:
	default:
		p.errorAt(p.tree.SpanOf(target).Start, "cannot assign to this expression")
	}
}

// parseIfStmt parses either form: the one-line `if cond: stmt` (no else),
// or the brace form `if cond { ... } [else (if ... | { ... })]`, with
// else-if chains falling out of the recursive call for `else if`.
func (p *Parser) parseIfStmt() ast.NodeIndex {
	kwTok := p.expectKeyword(enums.Keywords.If)

	// Composite literals are disallowed at the top level of the condition,
	// same as Go: `if a { b() }` must mean "condition a, then a block
	// containing b()", never "condition (a{b()})" - see exprLev on Parser.
	savedLev := p.exprLev
	p.exprLev = -1
	cond := p.parseExpr(precLowest)
	p.exprLev = savedLev

	if _, ok := p.accept(enums.Lexemes.Colon); ok {
		then := p.parseStmt()
		span := ast.Span{
			Start: kwTok.Start,
			End:   p.tree.SpanOf(then).End,
		}
		return p.tree.NewNode(enums.NodeKinds.IfStmt, kwTok, span, cond, then, ast.InvalidNode)
	}

	then := p.parseBlock()
	elseBranch := ast.InvalidNode
	end := p.tree.SpanOf(then).End
	if _, ok := p.acceptKeyword(enums.Keywords.Else); ok {
		if p.atKeyword(enums.Keywords.If) {
			elseBranch = p.parseIfStmt()
		} else {
			elseBranch = p.parseBlock()
		}
		end = p.tree.SpanOf(elseBranch).End
	}

	span := ast.Span{
		Start: kwTok.Start,
		End:   end,
	}
	return p.tree.NewNode(enums.NodeKinds.IfStmt, kwTok, span, cond, then, elseBranch)
}

// parseForStmt parses all three Go-style forms - bare `for {}`, cond-only
// `for cond {}`, and full `for init; cond; post {}` - by tentatively
// parsing the first clause as a simple statement and then looking at what
// follows it to decide which form it turned out to be, the same
// disambiguation Go's own grammar uses.
func (p *Parser) parseForStmt() ast.NodeIndex {
	kwTok := p.expectKeyword(enums.Keywords.For)

	if p.at(enums.Lexemes.LeftBrace) {
		body := p.parseBlock()
		span := ast.Span{
			Start: kwTok.Start,
			End:   p.tree.SpanOf(body).End,
		}
		return p.tree.NewNode(enums.NodeKinds.ForStmt, kwTok, span, ast.InvalidNode, ast.InvalidNode, ast.InvalidNode, body)
	}

	// Composite literals are disallowed at the top level of the header,
	// same reasoning (and same escape hatch via parens) as parseIfStmt.
	savedLev := p.exprLev
	p.exprLev = -1

	first := p.parseSimpleStmt()

	if _, ok := p.accept(enums.Lexemes.Semicolon); ok {
		return p.finishThreeClauseFor(kwTok, first, savedLev)
	}
	p.exprLev = savedLev

	// Cond-only form: `first` must have been a bare expression, not an
	// assignment/short-var-decl - unwrap the ExprStmt wrapper parseSimpleStmt
	// gave it back to a plain expression for the condition slot.
	cond := first
	if p.tree.Nodes[first].Kind == enums.NodeKinds.ExprStmt {
		cond = p.tree.Child(first, 0)
	} else {
		p.errorAt(p.tree.SpanOf(first).Start, "for loop condition must be a boolean expression")
	}
	body := p.parseBlock()
	span := ast.Span{
		Start: kwTok.Start,
		End:   p.tree.SpanOf(body).End,
	}
	return p.tree.NewNode(enums.NodeKinds.ForStmt, kwTok, span, ast.InvalidNode, cond, ast.InvalidNode, body)
}

// finishThreeClauseFor parses the rest of `for init; cond; post { body }`
// once the leading `init;` has already been consumed. savedLev is the
// exprLev to restore before the body, where composite literals are fine
// again - see parseForStmt.
func (p *Parser) finishThreeClauseFor(kwTok lexer.Token, init ast.NodeIndex, savedLev int) ast.NodeIndex {
	cond := ast.InvalidNode
	if !p.at(enums.Lexemes.Semicolon) {
		cond = p.parseExpr(precLowest)
	}
	p.expect(enums.Lexemes.Semicolon)

	post := ast.InvalidNode
	if !p.at(enums.Lexemes.LeftBrace) {
		post = p.parseSimpleStmt()
	}
	p.exprLev = savedLev

	body := p.parseBlock()
	span := ast.Span{
		Start: kwTok.Start,
		End:   p.tree.SpanOf(body).End,
	}
	return p.tree.NewNode(enums.NodeKinds.ForStmt, kwTok, span, init, cond, post, body)
}

func (p *Parser) parseReturnStmt() ast.NodeIndex {
	kwTok := p.expectKeyword(enums.Keywords.Return)
	value := ast.InvalidNode
	end := kwTok.End
	if !p.at(enums.Lexemes.Semicolon) && !p.at(enums.Lexemes.RightBrace) && !p.at(enums.Lexemes.EOF) {
		value = p.parseExpr(precLowest)
		end = p.tree.SpanOf(value).End
	}
	span := ast.Span{
		Start: kwTok.Start,
		End:   end,
	}
	return p.tree.NewNode(enums.NodeKinds.ReturnStmt, kwTok, span, value)
}

func (p *Parser) parseBreakStmt() ast.NodeIndex {
	kwTok := p.expectKeyword(enums.Keywords.Break)
	return p.tree.NewNode(enums.NodeKinds.BreakStmt, kwTok, tokenSpan(kwTok))
}

func (p *Parser) parseContinueStmt() ast.NodeIndex {
	kwTok := p.expectKeyword(enums.Keywords.Continue)
	return p.tree.NewNode(enums.NodeKinds.ContinueStmt, kwTok, tokenSpan(kwTok))
}
