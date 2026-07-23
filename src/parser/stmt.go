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
	case p.atKeyword(enums.Keywords.Delete):
		return p.parseDeleteStmt()
	case p.atKeyword(enums.Keywords.Match):
		return p.parseMatchStmt()
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
	stmts := p.parseSemiList(enums.Lexemes.RightBrace, p.parseStmt)
	closeTok := p.expect(enums.Lexemes.RightBrace)
	span := ast.Span{
		Start: openTok.Start,
		End:   closeTok.End,
	}
	return p.tree.NewNode(enums.NodeKinds.Block, lexer.Token{}, span, stmts...)
}

// parseTypeExpr parses a type reference: a bare identifier (int, string,
// bool, or a struct name), a package-qualified name (`pkg.Name` - an
// imported package's exported struct type, see LANGUAGE.md's "Imports"
// section; parsed as a MemberExpr node, the exact same shape a value-level
// `a.b` already uses - sema.typeFromNode/resolveType tell the two apart by
// context, not by grammar), an array type `[N]T` (fixed-size) / `[]T`
// (dynamic - parsed now, rejected later at a semantic stage once one
// exists, so the grammar doesn't need to change when dynamic arrays land),
// a pointer type `*T` (see LANGUAGE.md's "Pointers" section - a leading `*`
// prefix modifier, same shape as `[N]T`'s own leading `[`), a function
// type `func(T1, T2) R` (see parseFuncType) - first-class function values
// (LANGUAGE.md's "First-class functions" section) - or a map type
// `map[K]V` (see LANGUAGE.md's "Maps" section) - the same recursive-into-
// element-type shape `[N]T`/`[]T` already use just above, keyed on the
// `map` keyword instead of a leading `[`.
func (p *Parser) parseTypeExpr() ast.NodeIndex {
	if starTok, ok := p.accept(enums.Lexemes.Asterisk); ok {
		elem := p.parseTypeExpr()
		span := ast.Span{
			Start: starTok.Start,
			End:   p.tree.SpanOf(elem).End,
		}
		return p.tree.NewNode(enums.NodeKinds.PointerType, lexer.Token{}, span, elem)
	}
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
	if p.atKeyword(enums.Keywords.Func) {
		return p.parseFuncType()
	}
	if kwTok, ok := p.acceptKeyword(enums.Keywords.Map); ok {
		p.expect(enums.Lexemes.LeftBracket)
		key := p.parseTypeExpr()
		p.expect(enums.Lexemes.RightBracket)
		elem := p.parseTypeExpr()
		span := ast.Span{
			Start: kwTok.Start,
			End:   p.tree.SpanOf(elem).End,
		}
		return p.tree.NewNode(enums.NodeKinds.MapType, lexer.Token{}, span, key, elem)
	}
	nameTok := p.expectIdent()
	ident := p.tree.NewNode(enums.NodeKinds.Ident, nameTok, tokenSpan(nameTok))
	if _, ok := p.accept(enums.Lexemes.Dot); ok {
		fieldTok := p.expectIdent()
		span := ast.Span{
			Start: nameTok.Start,
			End:   fieldTok.End,
		}
		return p.tree.NewNode(enums.NodeKinds.MemberExpr, fieldTok, span, ident)
	}
	return ident
}

// parseFuncType parses a function-type expression: `func(T1, T2) R` (a
// comma-separated parameter *type* list - no parameter names, unlike
// FuncDecl's own param list - plus a return type), or `func(T1, T2)` with no
// return type at all (an implicitly void function type, mirroring
// FuncDecl's own optional return type). The parameter types are wrapped in
// their own ParamTypeList node for the same reason FuncDecl wraps its params
// in a ParamList: FuncType has a variable-arity part (the parameter types)
// followed by a further fixed slot (the return type), so the variable part
// needs its own node to keep FuncType itself fixed-arity.
func (p *Parser) parseFuncType() ast.NodeIndex {
	kwTok := p.expectKeyword(enums.Keywords.Func)

	openTok := p.expect(enums.Lexemes.LeftParen)
	paramTypes := p.parseCommaList(enums.Lexemes.RightParen, p.parseTypeExpr)
	closeTok := p.expect(enums.Lexemes.RightParen)
	listSpan := ast.Span{
		Start: openTok.Start,
		End:   closeTok.End,
	}
	paramList := p.tree.NewNode(enums.NodeKinds.ParamTypeList, lexer.Token{}, listSpan, paramTypes...)

	returnType := ast.InvalidNode
	end := closeTok.End
	if p.atTypeStart() {
		returnType = p.parseTypeExpr()
		end = p.tree.SpanOf(returnType).End
	}

	span := ast.Span{
		Start: kwTok.Start,
		End:   end,
	}
	return p.tree.NewNode(enums.NodeKinds.FuncType, kwTok, span, paramList, returnType)
}

// atTypeStart reports whether the current token could begin a type
// expression (parseTypeExpr): a `[` (array type), the `func` keyword
// (function type), or a plain identifier naming a builtin/struct type.
// Unlike parseFuncDecl's own return-type check (unambiguous there - a
// FuncDecl's return type is always followed by `{`), a FuncType's optional
// return type can be followed by all sorts of things depending on context
// (`,` inside an outer param list, `)`, `=`, `;`, EOF, ...), so this decides
// positively whether a type could start here, rather than negatively
// checking for one specific terminator. A keyword other than `func` (`if`,
// `true`, `this`, ...) also lexes as Lexeme.Identifier (see Token.Keyword)
// but can never start a type, hence the explicit Keyword == "" check.
func (p *Parser) atTypeStart() bool {
	if p.at(enums.Lexemes.LeftBracket) || p.at(enums.Lexemes.Asterisk) || p.atKeyword(enums.Keywords.Func) {
		return true
	}
	return p.at(enums.Lexemes.Identifier) && p.tok.Keyword == ""
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
// and ++/--), a multi-target destructuring form (`a, b := f()` / `a, b =
// f()` - see LANGUAGE.md's "Go-style multi-return values" section), or a
// bare expression used as a statement. The multi-target forms are
// disambiguated with exactly one token of lookahead after the first name/
// target, the same way Go's own parser does it: a following `,` means "keep
// collecting names/targets" before `:=`/`=` decides which of the two this
// turns out to be; anything else falls straight through to the existing
// single-name/target forms, completely unchanged.
func (p *Parser) parseSimpleStmt() ast.NodeIndex {
	expr := p.parseExpr(precLowest)

	if p.at(enums.Lexemes.Comma) {
		return p.finishMultiTargetStmt(expr)
	}

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

// finishMultiTargetStmt parses the rest of a multi-target destructuring
// statement, given its first already-parsed name/target and the `,`
// following it (see parseSimpleStmt) - collects every remaining name/target,
// then dispatches on whichever of `:=`/`=` actually follows the full list to
// decide which of MultiShortVarDecl/MultiAssignStmt this is (see ast.Node's
// own doc comments for both shapes).
func (p *Parser) finishMultiTargetStmt(first ast.NodeIndex) ast.NodeIndex {
	targets := []ast.NodeIndex{first}
	for {
		if _, ok := p.accept(enums.Lexemes.Comma); !ok {
			break
		}
		targets = append(targets, p.parseExpr(precLowest))
	}

	if p.at(enums.Lexemes.ColonEqual) {
		return p.finishMultiShortVarDecl(targets)
	}
	if p.at(enums.Lexemes.Equal) {
		return p.finishMultiAssignStmt(targets)
	}

	tok := p.tok
	p.errorAtSpan(tok.Start, tok.End, "expected := or = after a comma-separated list, found %s", p.describe(tok))
	return p.badNode(tok)
}

// finishMultiShortVarDecl parses `:= call` given names already collected by
// finishMultiTargetStmt - the multi-name counterpart to finishShortVarDecl.
// Every name must be a plain identifier, same check finishShortVarDecl's own
// single-name form already makes (Bad is accepted the same already-reported-
// once way).
func (p *Parser) finishMultiShortVarDecl(names []ast.NodeIndex) ast.NodeIndex {
	for _, name := range names {
		if kind := p.tree.Nodes[name].Kind; kind != enums.NodeKinds.Ident && kind != enums.NodeKinds.Bad {
			span := p.tree.SpanOf(name)
			p.errorAtSpan(span.Start, span.End, "left side of := must be an identifier")
		}
	}
	opTok := p.expect(enums.Lexemes.ColonEqual)
	value := p.parseExpr(precLowest)
	span := ast.Span{
		Start: p.tree.SpanOf(names[0]).Start,
		End:   p.tree.SpanOf(value).End,
	}
	children := append(append([]ast.NodeIndex{}, names...), value)
	return p.tree.NewNode(enums.NodeKinds.MultiShortVarDecl, opTok, span, children...)
}

// finishMultiAssignStmt parses `= call` given targets already collected by
// finishMultiTargetStmt - the multi-target counterpart to finishAssignStmt.
// Only plain `=` is supported here (no compound `+=`-style form makes sense
// against more than one target at once), so finishMultiTargetStmt only ever
// reaches this via its own explicit `p.at(enums.Lexemes.Equal)` check.
func (p *Parser) finishMultiAssignStmt(targets []ast.NodeIndex) ast.NodeIndex {
	for _, target := range targets {
		p.checkAssignTarget(target)
	}
	opTok := p.expect(enums.Lexemes.Equal)
	value := p.parseExpr(precLowest)
	span := ast.Span{
		Start: p.tree.SpanOf(targets[0]).Start,
		End:   p.tree.SpanOf(value).End,
	}
	children := append(append([]ast.NodeIndex{}, targets...), value)
	return p.tree.NewNode(enums.NodeKinds.MultiAssignStmt, opTok, span, children...)
}

func (p *Parser) finishShortVarDecl(name ast.NodeIndex) ast.NodeIndex {
	// Bad is excluded here: if name already failed to parse, parseExpr
	// already reported why - piling "left side of := must be an
	// identifier" on top would just be a second, redundant diagnostic for
	// the same root cause.
	if kind := p.tree.Nodes[name].Kind; kind != enums.NodeKinds.Ident && kind != enums.NodeKinds.Bad {
		span := p.tree.SpanOf(name)
		p.errorAtSpan(span.Start, span.End, "left side of := must be an identifier")
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
	case enums.NodeKinds.Ident,
		enums.NodeKinds.MemberExpr,
		enums.NodeKinds.IndexExpr,
		// UnaryExpr covers `*p` as an lvalue (see LANGUAGE.md's "Pointers"
		// section) - this is purely a parser-level shape check (unchanged
		// from before this feature: any lvalue-shaped syntax is accepted
		// here), same as every other case; sema's checkLValue is what
		// actually rejects a UnaryExpr whose operator isn't "*" (e.g. `&x = 5`
		// never parses as anything but this same shape, and is rejected
		// there instead).
		enums.NodeKinds.UnaryExpr,
		enums.NodeKinds.Bad:
	default:
		span := p.tree.SpanOf(target)
		p.errorAtSpan(span.Start, span.End, "cannot assign to this expression")
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
		span := p.tree.SpanOf(first)
		p.errorAtSpan(span.Start, span.End, "for loop condition must be a boolean expression")
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

// parseReturnStmt parses a bare `return`, a plain single-value `return expr`
// (completely unchanged), or a multi-value `return a, b, ...` (see
// LANGUAGE.md's "Go-style multi-return values" section) - the latter wraps
// its comma-separated value list in a MultiValueExpr node occupying
// ReturnStmt's own single expr slot, the same "wrap the variable part in its
// own node" convention parseFuncDeclReturnType's MultiReturnType already
// uses one level up (see ast.Node's own MultiValueExpr doc comment).
func (p *Parser) parseReturnStmt() ast.NodeIndex {
	kwTok := p.expectKeyword(enums.Keywords.Return)
	value := ast.InvalidNode
	end := kwTok.End
	if !p.at(enums.Lexemes.Semicolon) && !p.at(enums.Lexemes.RightBrace) && !p.at(enums.Lexemes.EOF) {
		first := p.parseExpr(precLowest)
		value = first
		if p.at(enums.Lexemes.Comma) {
			values := []ast.NodeIndex{first}
			for {
				if _, ok := p.accept(enums.Lexemes.Comma); !ok {
					break
				}
				values = append(values, p.parseExpr(precLowest))
			}
			listSpan := ast.Span{
				Start: p.tree.SpanOf(first).Start,
				End:   p.tree.SpanOf(values[len(values)-1]).End,
			}
			value = p.tree.NewNode(enums.NodeKinds.MultiValueExpr, lexer.Token{}, listSpan, values...)
		}
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

// parseDeleteStmt parses `delete p` (see LANGUAGE.md's "Pointers" section) -
// its own dedicated statement form, the same way break/continue/return are,
// rather than a call-expression-shaped builtin: delete returns nothing and is
// purely side-effecting, unlike make/append/len.
func (p *Parser) parseDeleteStmt() ast.NodeIndex {
	kwTok := p.expectKeyword(enums.Keywords.Delete)
	expr := p.parseExpr(precLowest)
	span := ast.Span{
		Start: kwTok.Start,
		End:   p.tree.SpanOf(expr).End,
	}
	return p.tree.NewNode(enums.NodeKinds.DeleteStmt, kwTok, span, expr)
}

// parseMatchStmt parses `match subject { pattern => { ... } ... }` - see
// LANGUAGE.md's "match" section. subject is parsed with composite literals
// disabled at its top level (exprLev = -1), exactly the same escape-hatch
// if/for's own header already uses - a bare `match shape {` would otherwise
// be ambiguous with a composite literal `shape{...}`. Arms are separated the
// same way a Block's own statement list is (ASI already covers the common
// case, since every arm's body ends in `}`).
func (p *Parser) parseMatchStmt() ast.NodeIndex {
	kwTok := p.expectKeyword(enums.Keywords.Match)

	savedLev := p.exprLev
	p.exprLev = -1
	subject := p.parseExpr(precLowest)
	p.exprLev = savedLev

	p.expect(enums.Lexemes.LeftBrace)
	arms := p.parseSemiList(enums.Lexemes.RightBrace, p.parseMatchArm)
	closeTok := p.expect(enums.Lexemes.RightBrace)

	span := ast.Span{
		Start: kwTok.Start,
		End:   closeTok.End,
	}
	children := append([]ast.NodeIndex{subject}, arms...)
	return p.tree.NewNode(enums.NodeKinds.MatchStmt, kwTok, span, children...)
}

// parseMatchArm parses one `pattern0, pattern1, ... => { body }` arm - a
// comma-separated list of one or more patterns (Go's own `case a, b, c:`
// multi-value-per-arm shape), each parsed with the ordinary expression
// grammar (parseExpr): a wildcard `_` (a bare Ident), a unit-variant pattern
// (a MemberExpr), a tuple-variant pattern (a CallExpr whose "arguments" are
// fresh binding names), a struct-variant pattern (a CompositeLit whose keyed
// elements' values are fresh binding names), or - new this round - an
// ordinary value expression (a literal, a variable reference, ...) - see
// ast.Node's own MatchArm doc comment for why none of this needs any new
// expression-parsing grammar at all: every one of these shapes already
// parses via CallExpr/CompositeLit/MemberExpr/any-other-expression's own
// existing grammar, since a pattern's own shape is deliberately identical to
// either a variant's own construction expression or a plain value
// expression. This grammar itself makes no attempt to restrict how many
// patterns an arm may have (an enum-match arm's "exactly one pattern" rule
// is sema's job - checkEnumMatchStmt - since the grammar has no notion yet
// of whether the enclosing match's subject is an enum or a plain value).
func (p *Parser) parseMatchArm() ast.NodeIndex {
	patterns := []ast.NodeIndex{p.parseExpr(precLowest)}
	for {
		if _, ok := p.accept(enums.Lexemes.Comma); !ok {
			break
		}
		patterns = append(patterns, p.parseExpr(precLowest))
	}
	p.expect(enums.Lexemes.FatArrow)
	body := p.parseBlock()

	span := ast.Span{
		Start: p.tree.SpanOf(patterns[0]).Start,
		End:   p.tree.SpanOf(body).End,
	}
	children := append(patterns, body)
	return p.tree.NewNode(enums.NodeKinds.MatchArm, lexer.Token{}, span, children...)
}
