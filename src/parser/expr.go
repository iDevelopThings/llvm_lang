package parser

import (
	"llvm_lang/src/ast"
	"llvm_lang/src/enums"
	"llvm_lang/src/lexer"
)

// precedence controls both when parseExpr stops extending a left-hand
// expression and, via the +1 in a binary rule's recursive call, associativity.
// Higher binds tighter. Postfix operators (call/index/member) sit above
// unary specifically so `-a.b` parses as `-(a.b)`, not `(-a).b`.
type precedence int

const (
	precLowest  precedence = iota
	precOr                 // ||
	precAnd                // &&
	precCompare            // == != < <= > >=
	precAdd                // + - | ^
	precMul                // * / % &
	precUnary              // - !  (prefix)
	precPostfix            // ( [ .  (call, index, member)
)

// prefixFn starts a new expression from the current token (a literal,
// identifier, unary operator, or grouping paren).
type prefixFn func(p *Parser) ast.NodeIndex

// infixFn extends an already-parsed left-hand expression using the current
// token - a binary operator, or a postfix operator (call/index/member) that
// parses its own suffix grammar instead of recursing for a right operand.
type infixFn func(p *Parser, left ast.NodeIndex) ast.NodeIndex

type infixRule struct {
	parse infixFn
	prec  precedence
}

// prefixFns and infixRules are populated in init, not as var initializer
// expressions: parseParenExpr/parseBinaryExpr call back into parseExpr,
// which reads these same maps, and Go's static init-order analysis flags
// that as a cycle when it appears in a var initializer (even though it's
// fine at runtime, since every parse happens well after package init). A
// plain assignment inside init sidesteps the analysis entirely.
var prefixFns map[enums.Lexeme]prefixFn
var infixRules map[enums.Lexeme]infixRule

func init() {
	prefixFns = map[enums.Lexeme]prefixFn{
		enums.Lexemes.Identifier:  parseIdentExpr,
		enums.Lexemes.Number:      parseNumberExpr,
		enums.Lexemes.String:      parseStringExpr,
		enums.Lexemes.LeftParen:   parseParenExpr,
		enums.Lexemes.Minus:       parseUnaryExpr,
		enums.Lexemes.Not:         parseUnaryExpr,
		enums.Lexemes.LeftBracket: parseArrayTypeLit,
		// Ampersand/Asterisk each already have an infix rule below (bitwise
		// `&`, multiply `*`) - a prefix entry for the same lexeme is exactly
		// the same dual-role disambiguation unary `-` already has alongside
		// binary `-` (see LANGUAGE.md's "Pointers" section): parseExpr only
		// ever consults prefixFns for the *first* token of an expression, so
		// there's no conflict with the infix table below at all.
		enums.Lexemes.Ampersand: parseUnaryExpr,
		enums.Lexemes.Asterisk:  parseUnaryExpr,
	}

	infixRules = map[enums.Lexeme]infixRule{
		enums.Lexemes.Or: {
			parse: parseBinaryExpr,
			prec:  precOr,
		},
		enums.Lexemes.And: {
			parse: parseBinaryExpr,
			prec:  precAnd,
		},
		enums.Lexemes.EqualEqual: {
			parse: parseBinaryExpr,
			prec:  precCompare,
		},
		enums.Lexemes.NotEqual: {
			parse: parseBinaryExpr,
			prec:  precCompare,
		},
		enums.Lexemes.LessThan: {
			parse: parseBinaryExpr,
			prec:  precCompare,
		},
		enums.Lexemes.LessThanEqual: {
			parse: parseBinaryExpr,
			prec:  precCompare,
		},
		enums.Lexemes.GreaterThan: {
			parse: parseBinaryExpr,
			prec:  precCompare,
		},
		enums.Lexemes.GreaterThanEqual: {
			parse: parseBinaryExpr,
			prec:  precCompare,
		},
		enums.Lexemes.Plus: {
			parse: parseBinaryExpr,
			prec:  precAdd,
		},
		enums.Lexemes.Minus: {
			parse: parseBinaryExpr,
			prec:  precAdd,
		},
		enums.Lexemes.Pipe: {
			parse: parseBinaryExpr,
			prec:  precAdd,
		},
		enums.Lexemes.Caret: {
			parse: parseBinaryExpr,
			prec:  precAdd,
		},
		enums.Lexemes.Asterisk: {
			parse: parseBinaryExpr,
			prec:  precMul,
		},
		enums.Lexemes.Slash: {
			parse: parseBinaryExpr,
			prec:  precMul,
		},
		enums.Lexemes.Percent: {
			parse: parseBinaryExpr,
			prec:  precMul,
		},
		enums.Lexemes.Ampersand: {
			parse: parseBinaryExpr,
			prec:  precMul,
		},
		enums.Lexemes.LeftParen: {
			parse: parseCallExpr,
			prec:  precPostfix,
		},
		enums.Lexemes.LeftBracket: {
			parse: parseIndexExpr,
			prec:  precPostfix,
		},
		enums.Lexemes.Dot: {
			parse: parseMemberExpr,
			prec:  precPostfix,
		},
	}
}

// parseExpr is the Pratt loop every grammar rule needing an expression calls
// into: parse a left-hand side via its prefix rule, then keep extending it
// with infix/postfix rules whose precedence clears minPrec. Call/index/member
// chain through this same loop as ordinary infix continuations - `a.b[0].c()`
// needs no special handling beyond their table entries.
func (p *Parser) parseExpr(minPrec precedence) ast.NodeIndex {
	prefix, ok := prefixFns[p.tok.Lexeme]
	if !ok {
		tok := p.tok
		p.errorAtSpan(tok.Start, tok.End, "expected expression, found %s", p.describe(tok))
		p.advance()
		return p.badNode(tok)
	}
	return p.continueExpr(prefix(p), minPrec)
}

// continueExpr is parseExpr's own loop, resumed over an already-parsed
// left-hand side - for the rare rule that had to build its operand itself
// before ordinary infix/postfix continuation can take over.
func (p *Parser) continueExpr(left ast.NodeIndex, minPrec precedence) ast.NodeIndex {
	for {
		rule, ok := infixRules[p.tok.Lexeme]
		if !ok || rule.prec < minPrec {
			return left
		}
		left = rule.parse(p, left)
	}
}

func parseIdentExpr(p *Parser) ast.NodeIndex {
	tok := p.tok
	switch tok.Keyword {
	case enums.Keywords.True, enums.Keywords.False:
		p.advance()
		return p.tree.NewNode(enums.NodeKinds.BoolLit, tok, tokenSpan(tok))
	case enums.Keywords.This:
		p.advance()
		return p.tree.NewNode(enums.NodeKinds.ThisExpr, tok, tokenSpan(tok))
	case enums.Keywords.Func:
		return p.parseFuncLit()
	case enums.Keywords.New:
		return p.parseNewExpr()
	case enums.Keywords.Move:
		return p.parseMoveExpr()
	case enums.Keywords.Match:
		return p.parseMatchExpr()
	case enums.Keywords.Range:
		return p.parseRangeExpr()
	case "":
		p.advance()
		ident := p.tree.NewNode(enums.NodeKinds.Ident, tok, tokenSpan(tok))
		// A named-type composite literal (`Point{...}`) - only when
		// composite literals are allowed here at all (see exprLev on
		// Parser); otherwise this is just a plain identifier and the `{`
		// belongs to whatever follows (an if/for body, most commonly).
		if p.atCompositeLitBody() {
			return p.finishCompositeLit(ident)
		}
		return ident
	default:
		p.errorAtSpan(tok.Start, tok.End, "unexpected keyword %s in expression", p.describe(tok))
		p.advance()
		return p.badNode(tok)
	}
}

// parseFuncLit parses a function-literal expression: `func(params)
// [returnType] { body }` - a real, value-producing expression, not just the
// bare `func(T1, T2) R` *type* syntax parseFuncType already handles (see
// ast.Node's own FuncLit doc comment for the [paramList, returnType, body]
// shape - FuncDecl's own shape minus the receiver/name slots a literal has no
// use for). Reuses parseParamList/parseBlock verbatim - a literal's own
// params are named, exactly like a FuncDecl's, unlike a bare function type's
// unnamed ParamTypeList - so this is really just parseFuncDecl with no
// receiver clause and no name to parse. See LANGUAGE.md's "Lambdas" section
// for the language-level feature and CODEGEN.md for how capture-by-reference
// actually lowers.
func (p *Parser) parseFuncLit() ast.NodeIndex {
	kwTok := p.expectKeyword(enums.Keywords.Func)

	params := p.parseParamList()

	returnType := ast.InvalidNode
	if !p.at(enums.Lexemes.LeftBrace) {
		returnType = p.parseTypeExpr()
	}

	body := p.parseBlock()

	span := ast.Span{
		Start: kwTok.Start,
		End:   p.tree.SpanOf(body).End,
	}
	return p.tree.NewNode(enums.NodeKinds.FuncLit, kwTok, span, params, returnType, body)
}

// parseNewExpr parses `new T(args)` / `new T{...}` (see LANGUAGE.md's
// "Pointers" section) - the `new` keyword wraps an ordinary, already-legal
// constructor-call or composite-literal expression unchanged: parsing the
// wrapped expression at precPostfix means the prefix rule for T (an
// identifier) runs first, then the very same infix loop that already
// recognizes `(` as a call and `{` as a composite literal keeps extending
// it - no bespoke grammar of its own is needed for either shape, only for
// the leading `new` keyword itself. sema (not this grammar rule) is what
// actually requires the wrapped expression to be one of those two shapes.
func (p *Parser) parseNewExpr() ast.NodeIndex {
	kwTok := p.expectKeyword(enums.Keywords.New)
	inner := p.parseExpr(precPostfix)
	span := ast.Span{
		Start: kwTok.Start,
		End:   p.tree.SpanOf(inner).End,
	}
	return p.tree.NewNode(enums.NodeKinds.NewExpr, kwTok, span, inner)
}

// parseMoveExpr parses `move x` (see LANGUAGE.md's "Destructors" section) -
// x must be a bare identifier; parsing the operand at precPostfix (rather
// than just consuming a single identifier token) lets a wrong shape like
// `move this.field`/`move arr[i]`/`move (x)` still parse as one contiguous
// expression, so it can be rejected here with one clean diagnostic instead
// of leaving a trailing `.field`/`[i]`/etc. for the statement parser to
// stumble over. sema never re-reports this - see checkMoveExpr.
func (p *Parser) parseMoveExpr() ast.NodeIndex {
	kwTok := p.expectKeyword(enums.Keywords.Move)
	operand := p.parseExpr(precPostfix)
	if p.tree.Nodes[operand].Kind != enums.NodeKinds.Ident {
		span := p.tree.SpanOf(operand)
		p.errorAtSpan(span.Start, span.End, "move requires a plain variable name, not a more complex expression")
	}
	span := ast.Span{
		Start: kwTok.Start,
		End:   p.tree.SpanOf(operand).End,
	}
	return p.tree.NewNode(enums.NodeKinds.MoveExpr, kwTok, span, operand)
}

// parseMatchExpr parses `match` in EXPRESSION position (see LANGUAGE.md's
// "match" section: "match as an expression") - reachable anywhere an
// expression is legal (a `:=` right-hand side, a function call argument,
// nested inside another expression), unlike parseMatchStmt (stmt.go), which
// only ever fires at statement-start (parseStmt's own keyword-dispatch
// checks Match before ever falling through to expression parsing at all -
// see parseStmt's own doc comment - so a bare top-level `match x {...}`
// statement is completely unaffected by this, still parsed via
// parseMatchStmt, unchanged). Shares parseMatchStmt's own subject-parsing
// logic verbatim, including the exprLev = -1 composite-literal-
// disambiguation escape hatch (a bare `match shape {` would otherwise be
// ambiguous with a composite literal `shape{...}`) - the one real grammar
// difference between the two is each arm's own body shape, parsed here by
// parseMatchExprArm instead of parseMatchArm's own always-a-block shape.
// Produces the exact same MatchStmt/MatchArm node kinds the statement form
// does (see ast.Node's own MatchStmt doc comment) - sema/codegen tell the
// two apart purely by which dispatch reached the node (checkStmt/genStmt's
// vs. checkExpr/genExpr's), never by any grammar-level marker on the node
// itself.
func (p *Parser) parseMatchExpr() ast.NodeIndex {
	kwTok := p.expectKeyword(enums.Keywords.Match)

	savedLev := p.exprLev
	p.exprLev = -1
	subject := p.parseExpr(precLowest)
	p.exprLev = savedLev

	p.expect(enums.Lexemes.LeftBrace)
	arms := p.parseSemiList(enums.Lexemes.RightBrace, p.parseMatchExprArm)
	closeTok := p.expect(enums.Lexemes.RightBrace)

	span := ast.Span{
		Start: kwTok.Start,
		End:   closeTok.End,
	}
	children := append([]ast.NodeIndex{subject}, arms...)
	return p.tree.NewNode(enums.NodeKinds.MatchStmt, kwTok, span, children...)
}

// parseMatchExprArm parses one expression-mode match arm:
// `pattern0, pattern1, ... => body` - the pattern-list grammar itself is
// identical to parseMatchArm's own (see that function's own doc comment for
// what each pattern shape can be), but body may now be either of two
// surface shapes:
//
//   - a real brace-delimited block (`{ ... }`) - parsed via parseBlock,
//     completely unchanged, and may contain `yield` anywhere inside, at any
//     nesting depth, alongside ordinary if/for/whatever statements.
//   - a bare expression with no braces at all (`pattern => expr`) - desugared
//     right here into a synthetic single-statement Block wrapping a
//     synthetic YieldStmt around that expression (see ast.Node's own
//     MatchArm doc comment: this extends the exact same "sometimes a Block,
//     sometimes not" convention ForStmt's own init/post slots already use).
//
// This desugaring is purely a parser-level convenience: it means sema and
// codegen only ever have to handle ONE canonical arm-body shape ("a Block
// whose every reachable path must yield") regardless of which surface form
// the user actually wrote. The synthetic Block/YieldStmt nodes carry the
// wrapped expression's own span and no token of their own - there's no
// `{`/`}`/`yield` keyword anywhere in the source for either to point at.
func (p *Parser) parseMatchExprArm() ast.NodeIndex {
	patterns := []ast.NodeIndex{p.parseExpr(precLowest)}
	for {
		if _, ok := p.accept(enums.Lexemes.Comma); !ok {
			break
		}
		patterns = append(patterns, p.parseExpr(precLowest))
	}
	p.expect(enums.Lexemes.FatArrow)

	var body ast.NodeIndex
	if p.at(enums.Lexemes.LeftBrace) {
		body = p.parseBlock()
	} else {
		value := p.parseExpr(precLowest)
		valueSpan := p.tree.SpanOf(value)
		yieldStmt := p.tree.NewNode(enums.NodeKinds.YieldStmt, lexer.Token{}, valueSpan, value)
		body = p.tree.NewNode(enums.NodeKinds.Block, lexer.Token{}, valueSpan, yieldStmt)
	}

	span := ast.Span{
		Start: p.tree.SpanOf(patterns[0]).Start,
		End:   p.tree.SpanOf(body).End,
	}
	children := append(patterns, body)
	return p.tree.NewNode(enums.NodeKinds.MatchArm, lexer.Token{}, span, children...)
}

// parseRangeExpr parses `range subject` in EXPRESSION position (see
// LANGUAGE.md's "Range loops" section) - grammatically legal anywhere an
// expression is (mirroring match/new above), though only ever meaningful
// directly as a for-loop header's `:=` value (parser/stmt.go's
// finishRangeForStmt); anywhere else sema rejects it with a clean
// diagnostic rather than a panic (checkExpr's own RangeExpr case).
func (p *Parser) parseRangeExpr() ast.NodeIndex {
	kwTok := p.expectKeyword(enums.Keywords.Range)
	subject := p.parseExpr(precLowest)
	span := ast.Span{
		Start: kwTok.Start,
		End:   p.tree.SpanOf(subject).End,
	}
	return p.tree.NewNode(enums.NodeKinds.RangeExpr, kwTok, span, subject)
}

func parseNumberExpr(p *Parser) ast.NodeIndex {
	tok := p.tok
	p.advance()
	return p.tree.NewNode(enums.NodeKinds.NumberLit, tok, tokenSpan(tok))
}

func parseStringExpr(p *Parser) ast.NodeIndex {
	tok := p.tok
	p.advance()
	return p.tree.NewNode(enums.NodeKinds.StringLit, tok, tokenSpan(tok))
}

// parseParenExpr handles `(` only as a prefix (grouping); `(` as an infix
// (call) is parseCallExpr - the same token, disambiguated purely by which
// table a Pratt parser consults, with no extra bookkeeping needed.
func parseParenExpr(p *Parser) ast.NodeIndex {
	openTok := p.expect(enums.Lexemes.LeftParen)
	p.exprLev++
	inner := p.parseExpr(precLowest)
	p.exprLev--
	closeTok := p.expect(enums.Lexemes.RightParen)
	span := ast.Span{
		Start: openTok.Start,
		End:   closeTok.End,
	}
	return p.tree.NewNode(enums.NodeKinds.ParenExpr, lexer.Token{}, span, inner)
}

func parseUnaryExpr(p *Parser) ast.NodeIndex {
	opTok := p.tok
	p.advance()
	operand := p.parseExpr(precUnary)
	span := ast.Span{
		Start: opTok.Start,
		End:   p.tree.SpanOf(operand).End,
	}
	return p.tree.NewNode(enums.NodeKinds.UnaryExpr, opTok, span, operand)
}

// parseBinaryExpr recurses at rule.prec+1 for the right operand: every
// operator here is left-associative, so a following operator of the same
// precedence must be left for the outer loop to consume (`a-b-c` ->
// `(a-b)-c`), not swallowed by this call.
func parseBinaryExpr(p *Parser, left ast.NodeIndex) ast.NodeIndex {
	opTok := p.tok
	rule := infixRules[opTok.Lexeme]
	p.advance()
	right := p.parseExpr(rule.prec + 1)
	span := ast.Span{
		Start: p.tree.SpanOf(left).Start,
		End:   p.tree.SpanOf(right).End,
	}
	return p.tree.NewNode(enums.NodeKinds.BinaryExpr, opTok, span, left, right)
}

func parseCallExpr(p *Parser, callee ast.NodeIndex) ast.NodeIndex {
	p.expect(enums.Lexemes.LeftParen)
	p.exprLev++
	var args []ast.NodeIndex
	var spreadTok lexer.Token
	if isMakeCallee(p, callee) {
		args = append([]ast.NodeIndex{callee}, p.parseMakeArgs()...)
	} else {
		callArgs, spread := p.parseCallArgs()
		args = append([]ast.NodeIndex{callee}, callArgs...)
		spreadTok = spread
	}
	p.exprLev--
	closeTok := p.expect(enums.Lexemes.RightParen)
	span := ast.Span{
		Start: p.tree.SpanOf(callee).Start,
		End:   closeTok.End,
	}
	return p.tree.NewNode(enums.NodeKinds.CallExpr, spreadTok, span, args...)
}

// parseCallArgs parses an ordinary call's comma-separated argument list, plus
// the trailing spread form (`Join(",", parts...)` - see LANGUAGE.md's
// "Variadic parameters" section): a `...` immediately after the LAST argument
// marks a spread call, forwarding that argument's own slice value directly as
// the variadic parameter rather than collecting a fresh one (see
// ast.Node's own CallExpr doc comment and Tree.CallHasSpread). Bespoke rather
// than parseCommaList, for the same reason parseMakeArgs already has its own
// loop: `...` must only be recognized right after an argument, and only when
// no further argument follows - parseCommaList's own per-element callback
// shape has no way to express that. A `...` followed by another argument is
// reported here and then simply treated as if no spread were written, so the
// remaining arguments still parse normally.
func (p *Parser) parseCallArgs() (args []ast.NodeIndex, spreadTok lexer.Token) {
	for !p.at(enums.Lexemes.RightParen) && !p.at(enums.Lexemes.EOF) {
		args = append(args, p.parseExpr(precLowest))
		if tok, ok := p.accept(enums.Lexemes.DotDotDot); ok {
			if _, ok := p.accept(enums.Lexemes.Comma); ok && !p.at(enums.Lexemes.RightParen) {
				p.errorAtSpan(tok.Start, tok.End, "... (spread) is only legal after a call's last argument")
				continue
			}
			spreadTok = tok
			break
		}
		if _, ok := p.accept(enums.Lexemes.Comma); !ok {
			break
		}
	}
	return args, spreadTok
}

// isMakeCallee reports whether callee is a bare reference to the
// predeclared `make` builtin, syntactically - just its identifier text, the
// same name-based check Go's own parser uses for make/new/append (see
// parseMakeArgs's doc comment): this pass has no symbol resolution yet, so
// there's no way to tell a real reference to the predeclared `make` apart
// from a local/package-level name that merely happens to be spelled "make".
// A program that shadows "make" as an ordinary function or variable (legal -
// see sema/scope.go's universeScope, exactly like `print` can be shadowed)
// and then tries to call it as such is out of scope for this round, the same
// narrow edge case Go itself carries for its own predeclared identifiers.
func isMakeCallee(p *Parser, callee ast.NodeIndex) bool {
	return p.tree.Nodes[callee].Kind == enums.NodeKinds.Ident && p.tree.Text(callee) == "make"
}

// parseMakeArgs parses make's own bespoke argument grammar: `make([]T, n)` or
// `make([]T, n, cap)` - unlike every other call in this language, make's
// first "argument" position is a type expression (`[]T`), not a value
// expression (see LANGUAGE.md's "Dynamic arrays" section for why: a bare
// `[]T` with no composite-literal body isn't parseable as an ordinary
// expression at all, the same wrinkle Go's own grammar hits and solves the
// same way - bespoke grammar for make/new/append rather than making arbitrary
// types parse as expressions everywhere). The type is parsed via
// parseTypeExpr directly, sidestepping parseArrayTypeLit's expression-level
// `[` prefix rule (which requires a following `{...}` body) entirely; every
// remaining argument (n, optionally cap) parses as an ordinary expression.
func (p *Parser) parseMakeArgs() []ast.NodeIndex {
	if p.at(enums.Lexemes.RightParen) {
		return nil
	}
	typ := p.parseTypeExpr()
	args := []ast.NodeIndex{typ}
	for {
		if _, ok := p.accept(enums.Lexemes.Comma); !ok {
			break
		}
		if p.at(enums.Lexemes.RightParen) {
			break // a trailing comma is tolerated right before close
		}
		args = append(args, p.parseExpr(precLowest))
	}
	return args
}

// parseIndexExpr parses both `s[i]` (IndexExpr) and a Go-style slice
// expression `s[a:b]` / `s[:b]` / `s[a:]` / `s[:]` (SliceExpr) - see
// LANGUAGE.md's "Slicing" section and ast.Node's own SliceExpr doc comment
// for the [object, low, high] shape. The two share one `[` infix rule: after
// `[`, an optional low-bound expression is parsed (skipped entirely when the
// very next token is already `:`, i.e. the low bound was omitted), then a
// following `:` disambiguates a slice expression from a plain index - parse
// an optional high-bound expression (skipped when `]` follows immediately)
// and build a SliceExpr; with no `:`, the already-parsed expression is the
// atTypeOnlyStart reports whether the current token can only ever begin a
// type expression, never a value one (`[]T`/`[N]T`, `map[K]V`, `func(...)`,
// `cfunc(...)`) - the cue parseIndexExpr uses to parse an explicit generic
// instantiation's argument (`Foo[[]int]`) as a type rather than an
// expression. A bare identifier or a `*T` pointer type stays ambiguous with
// indexing and is still parsed as an expression, then reinterpreted as a type
// by sema (see its typeArgFromNode).
func (p *Parser) atTypeOnlyStart() bool {
	switch {
	case p.at(enums.Lexemes.LeftBracket),
		p.atKeyword(enums.Keywords.Map),
		p.atKeyword(enums.Keywords.Func),
		p.atKeyword(enums.Keywords.CFunc):
		return true
	default:
		return false
	}
}

// parseIndexExpr parses both `s[i]` (IndexExpr) and a Go-style slice
// expression `s[a:b]` / `s[:b]` / `s[a:]` / `s[:]` (SliceExpr) - see
// LANGUAGE.md's "Slicing" section and ast.Node's own SliceExpr doc comment
// for the [object, low, high] shape. The two share one `[` infix rule: after
// `[`, an optional low-bound expression is parsed (skipped entirely when the
// very next token is already `:`, i.e. the low bound was omitted), then a
// following `:` disambiguates a slice expression from a plain index - parse
// an optional high-bound expression (skipped when `]` follows immediately)
// and build a SliceExpr; with no `:`, the already-parsed expression is the
// ordinary index. An index that starts type-only (see atTypeOnlyStart) is
// parsed as a type instead, which covers both an explicit generic
// instantiation's argument and an array-literal key (`m[[3]int{1,2,3}]`).
// This deliberately doesn't support Go's less-common 3-index `s[a:b:c]` form -
// not needed, out of scope for this round (see LANGUAGE.md).
func parseIndexExpr(p *Parser, target ast.NodeIndex) ast.NodeIndex {
	p.expect(enums.Lexemes.LeftBracket)
	p.exprLev++

	low := ast.InvalidNode
	switch {
	case p.at(enums.Lexemes.Colon):
	case p.atTypeOnlyStart():
		low = p.parseTypeExpr()
		if p.atCompositeLitBody() {
			// `m[[3]int{1,2,3}]` - an array/map-literal key, not a type
			// argument; from here it's an ordinary expression again.
			low = p.continueExpr(p.finishCompositeLit(low), precLowest)
		}
	default:
		low = p.parseExpr(precLowest)
	}

	// A comma here can only mean a multi-argument explicit instantiation
	// (`Pair[int, string]`) - no indexing or slicing grammar takes one. Every
	// remaining argument parses as a plain type; the first already did, one
	// way or the other, above.
	if p.at(enums.Lexemes.Comma) {
		args := []ast.NodeIndex{low}
		for {
			if _, ok := p.accept(enums.Lexemes.Comma); !ok {
				break
			}
			if p.at(enums.Lexemes.RightBracket) {
				break
			}
			args = append(args, p.parseTypeExpr())
		}
		p.exprLev--
		closeTok := p.expect(enums.Lexemes.RightBracket)
		listSpan := ast.Span{
			Start: p.tree.SpanOf(args[0]).Start,
			End:   closeTok.End,
		}
		list := p.tree.NewNode(enums.NodeKinds.TypeArgList, lexer.Token{}, listSpan, args...)
		span := ast.Span{
			Start: p.tree.SpanOf(target).Start,
			End:   closeTok.End,
		}
		return p.finishIndexExpr(p.tree.NewNode(enums.NodeKinds.IndexExpr, lexer.Token{}, span, target, list))
	}

	if _, ok := p.accept(enums.Lexemes.Colon); ok {
		high := ast.InvalidNode
		if !p.at(enums.Lexemes.RightBracket) {
			high = p.parseExpr(precLowest)
		}
		p.exprLev--
		closeTok := p.expect(enums.Lexemes.RightBracket)
		span := ast.Span{
			Start: p.tree.SpanOf(target).Start,
			End:   closeTok.End,
		}
		return p.tree.NewNode(enums.NodeKinds.SliceExpr, lexer.Token{}, span, target, low, high)
	}

	p.exprLev--
	closeTok := p.expect(enums.Lexemes.RightBracket)
	span := ast.Span{
		Start: p.tree.SpanOf(target).Start,
		End:   closeTok.End,
	}
	return p.finishIndexExpr(p.tree.NewNode(enums.NodeKinds.IndexExpr, lexer.Token{}, span, target, low))
}

// atCompositeLitBody reports whether a `{` here starts a composite literal's
// body rather than a statement block (see exprLev on Parser).
func (p *Parser) atCompositeLitBody() bool {
	return p.exprLev >= 0 && p.at(enums.Lexemes.LeftBrace)
}

// finishIndexExpr extends a just-built IndexExpr with a composite-literal body
// when one follows (`SlotMap[Entity]{...}` - a generic struct's own
// construction syntax, see LANGUAGE.md's "Generics" section).
func (p *Parser) finishIndexExpr(idx ast.NodeIndex) ast.NodeIndex {
	if p.atCompositeLitBody() {
		return p.finishCompositeLit(idx)
	}
	return idx
}

func parseMemberExpr(p *Parser, object ast.NodeIndex) ast.NodeIndex {
	p.expect(enums.Lexemes.Dot)
	nameTok := p.expectMemberName()
	span := ast.Span{
		Start: p.tree.SpanOf(object).Start,
		End:   nameTok.End,
	}
	member := p.tree.NewNode(enums.NodeKinds.MemberExpr, nameTok, span, object)
	// A package-qualified composite literal (`shapes.Point{...}` - see
	// LANGUAGE.md's "Imports" section).
	if p.atCompositeLitBody() {
		return p.finishCompositeLit(member)
	}
	return member
}

// parseArrayTypeLit is the prefix rule for a bare `[` starting an
// expression: the only thing that can be is an array-type composite
// literal (`[3]int{1, 2, 3}`, `[]int{...}`) - a bare array type with no
// literal body isn't a value, so that case is reported as an error rather
// than silently returned as if it were one.
func parseArrayTypeLit(p *Parser) ast.NodeIndex {
	typ := p.parseTypeExpr()
	if !p.at(enums.Lexemes.LeftBrace) {
		span := p.tree.SpanOf(typ)
		p.errorAtSpan(span.Start, span.End, "expected a composite literal (Type{...}) after array type")
		return typ
	}
	return p.finishCompositeLit(typ)
}

// finishCompositeLit parses the `{ elem, elem, ... }` body of a composite
// literal, given its already-parsed type expression (an Ident for a named
// struct type, or an ArrayType). Each element is either a bare expression
// (positional) or `key: value` (keyed) - mirrors go/ast's CompositeLit,
// which unifies both array and struct literals the same way, via
// KeyValueExpr for the keyed case.
func (p *Parser) finishCompositeLit(typ ast.NodeIndex) ast.NodeIndex {
	p.expect(enums.Lexemes.LeftBrace)
	p.exprLev++
	elems := append([]ast.NodeIndex{typ}, p.parseCommaList(enums.Lexemes.RightBrace, p.parseCompositeLitElem)...)
	p.exprLev--
	closeTok := p.expect(enums.Lexemes.RightBrace)
	span := ast.Span{
		Start: p.tree.SpanOf(typ).Start,
		End:   closeTok.End,
	}
	return p.tree.NewNode(enums.NodeKinds.CompositeLit, lexer.Token{}, span, elems...)
}

func (p *Parser) parseCompositeLitElem() ast.NodeIndex {
	value := p.parseExpr(precLowest)
	if _, ok := p.accept(enums.Lexemes.Colon); ok {
		key := value
		val := p.parseExpr(precLowest)
		span := ast.Span{
			Start: p.tree.SpanOf(key).Start,
			End:   p.tree.SpanOf(val).End,
		}
		return p.tree.NewNode(enums.NodeKinds.KeyValueExpr, lexer.Token{}, span, key, val)
	}
	return value
}
