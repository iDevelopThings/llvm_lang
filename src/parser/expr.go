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
	left := prefix(p)

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
	case "":
		p.advance()
		ident := p.tree.NewNode(enums.NodeKinds.Ident, tok, tokenSpan(tok))
		// A named-type composite literal (`Point{...}`) - only when
		// composite literals are allowed here at all (see exprLev on
		// Parser); otherwise this is just a plain identifier and the `{`
		// belongs to whatever follows (an if/for body, most commonly).
		if p.exprLev >= 0 && p.at(enums.Lexemes.LeftBrace) {
			return p.finishCompositeLit(ident)
		}
		return ident
	default:
		p.errorAtSpan(tok.Start, tok.End, "unexpected keyword %s in expression", p.describe(tok))
		p.advance()
		return p.badNode(tok)
	}
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
	if isMakeCallee(p, callee) {
		args = append([]ast.NodeIndex{callee}, p.parseMakeArgs()...)
	} else {
		args = append([]ast.NodeIndex{callee}, p.parseCommaList(enums.Lexemes.RightParen, func() ast.NodeIndex {
			return p.parseExpr(precLowest)
		})...)
	}
	p.exprLev--
	closeTok := p.expect(enums.Lexemes.RightParen)
	span := ast.Span{
		Start: p.tree.SpanOf(callee).Start,
		End:   closeTok.End,
	}
	return p.tree.NewNode(enums.NodeKinds.CallExpr, lexer.Token{}, span, args...)
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

func parseIndexExpr(p *Parser, target ast.NodeIndex) ast.NodeIndex {
	p.expect(enums.Lexemes.LeftBracket)
	p.exprLev++
	index := p.parseExpr(precLowest)
	p.exprLev--
	closeTok := p.expect(enums.Lexemes.RightBracket)
	span := ast.Span{
		Start: p.tree.SpanOf(target).Start,
		End:   closeTok.End,
	}
	return p.tree.NewNode(enums.NodeKinds.IndexExpr, lexer.Token{}, span, target, index)
}

func parseMemberExpr(p *Parser, object ast.NodeIndex) ast.NodeIndex {
	p.expect(enums.Lexemes.Dot)
	nameTok := p.expectIdent()
	span := ast.Span{
		Start: p.tree.SpanOf(object).Start,
		End:   nameTok.End,
	}
	member := p.tree.NewNode(enums.NodeKinds.MemberExpr, nameTok, span, object)
	// A package-qualified composite literal (`shapes.Point{...}` - see
	// LANGUAGE.md's "Imports" section) - same brace-ambiguity guard
	// parseIdentExpr's own plain-Ident composite-literal check uses (see
	// exprLev's own doc comment on Parser).
	if p.exprLev >= 0 && p.at(enums.Lexemes.LeftBrace) {
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
