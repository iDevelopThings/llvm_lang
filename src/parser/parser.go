// Package parser will hold the recursive-descent parser for llvm_lang. This
// file has no grammar yet - it's the scaffolding grammar rules get built on:
// a token cursor over the lexer, and one shared way to report a problem
// (lexical or syntactic) that keeps the parse moving instead of forcing an
// `if err != nil` at every call site.
package parser

import (
	"fmt"
	"slices"

	"llvm_lang/src/ast"
	"llvm_lang/src/diag"
	"llvm_lang/src/enums"
	"llvm_lang/src/lexer"
)

// maxErrors bounds how many diagnostics a single parse collects before
// giving up outright via bailout - past this point the input is broken
// enough that continuing just produces noise, not signal.
const maxErrors = 10

// bailout is an internal control-flow signal: panicking with it unwinds a
// hopelessly broken parse straight back to the recover in Run, without
// threading a "give up now" return value through every parsing method. It
// never escapes this package - Run always recovers it and returns normally.
type bailout struct{}

// Parser drives a single Lexer through a grammar (added as methods in later
// files) using one token of lookahead. It has no per-call error return:
// lexical and syntactic problems alike land in Diagnostics, and grammar code
// reacts to them via expect/sync rather than checking an error each call.
type Parser struct {
	file  *lexer.File
	lex   *lexer.Lexer
	diags *diag.Bag
	tree  *ast.Tree

	tok     lexer.Token // current lookahead
	lexSeen int         // len(lex.Errors()) already folded into diags

	// exprLev tracks whether a composite literal is allowed at the current
	// point (see expr.go's finishCompositeLit): non-negative means yes (the
	// default); if/for condition parsing sets it to -1 for their header,
	// since a bare `Foo{` there would be ambiguous with the following
	// block's `{` - the same fix go/parser uses for the same problem.
	// Entering any (...)/[...] bumps it back non-negative, so a
	// parenthesized composite literal in a condition still works.
	exprLev int
}

// New prepares a Parser over file with its first token loaded.
func New(file *lexer.File) *Parser {
	p := &Parser{
		file:  file,
		lex:   lexer.New(file),
		diags: diag.NewBag(),
		tree:  ast.NewTree(file),
	}
	p.advance()
	return p
}

// Diagnostics returns every diagnostic collected so far, lexical and
// syntactic alike.
func (p *Parser) Diagnostics() *diag.Bag { return p.diags }

// Tree returns the syntax tree built so far.
func (p *Parser) Tree() *ast.Tree { return p.tree }

// tokenSpan is the ast.Span covering tok alone - the common case for a leaf
// node built from a single token.
func tokenSpan(tok lexer.Token) ast.Span {
	return ast.Span{
		Start: tok.Start,
		End:   tok.End,
	}
}

// badNode records a NodeKinds.Bad placeholder at tok's position - returned
// by grammar rules that hit a syntax error but must still produce some
// NodeIndex to keep the tree (and the caller) well-formed.
func (p *Parser) badNode(tok lexer.Token) ast.NodeIndex {
	return p.tree.NewNode(enums.NodeKinds.Bad, tok, tokenSpan(tok))
}

// advance consumes the current token and pulls the next one from the
// lexer, folding in any lexical errors raised while scanning it. This is
// the only place lexer output touches the parser, so grammar code never
// has to special-case a lex failure.
func (p *Parser) advance() {
	p.tok = p.lex.Next()
	p.syncLexErrors()
}

func (p *Parser) syncLexErrors() {
	errs := p.lex.Errors()
	for _, e := range errs[p.lexSeen:] {
		p.errorAt(e.Pos, "%s", e.Msg)
	}
	p.lexSeen = len(errs)
}

// errorAt records a diagnostic at the single point pos and bails out of the
// parse once too many have piled up.
func (p *Parser) errorAt(pos lexer.Pos, format string, a ...any) {
	p.diags.Errorf(pos, format, a...)
	p.bailoutIfTooMany()
}

// errorAtSpan is errorAt, but underlining [start, end) instead of a single
// point - the parser's counterpart to the other packages' errorAt(n
// ast.NodeIndex, ...) helpers, except the parser frequently has only a raw
// token or an already-computed ast.Span in hand rather than a NodeIndex (a
// syntax error is often detected before any node for it exists at all).
func (p *Parser) errorAtSpan(start, end lexer.Pos, format string, a ...any) {
	p.diags.ErrorfSpan(start, end, format, a...)
	p.bailoutIfTooMany()
}

func (p *Parser) bailoutIfTooMany() {
	if p.diags.ErrorCount() >= maxErrors {
		panic(bailout{})
	}
}

// describe renders a token for an error message, e.g. `"foo"` or `EOF`.
func (p *Parser) describe(tok lexer.Token) string {
	if tok.Lexeme == enums.Lexemes.EOF {
		return "EOF"
	}
	return fmt.Sprintf("%q", p.file.Lexeme(tok))
}

// at reports whether the current token is lex.
func (p *Parser) at(lex enums.Lexeme) bool { return p.tok.Lexeme == lex }

// accept consumes and returns the current token if it matches lex, without
// reporting an error when it doesn't - for optional/either-way grammar spots.
func (p *Parser) accept(lex enums.Lexeme) (lexer.Token, bool) {
	if p.tok.Lexeme != lex {
		return lexer.Token{}, false
	}
	tok := p.tok
	p.advance()
	return tok, true
}

// expect consumes and returns the current token, or - if it doesn't match
// lex - reports "expected X, found Y" and returns it unconsumed. Callers
// that need to keep the parse moving after a miss should follow up with
// sync to a recovery point.
func (p *Parser) expect(lex enums.Lexeme) lexer.Token {
	if p.tok.Lexeme != lex {
		p.errorAtSpan(p.tok.Start, p.tok.End, "expected %s, found %s", lex, p.describe(p.tok))
		return p.tok
	}
	tok := p.tok
	p.advance()
	return tok
}

// expectIdent consumes and returns the current token if it's a plain
// identifier - Lexeme.Identifier with no Keyword set - reporting an error
// otherwise. Plain expect(Identifier) alone isn't enough for a
// name-binding position (a var/type/field name): every keyword also lexes
// as Lexeme.Identifier (see Token.Keyword), so it would silently accept
// `var if = 5`. Unlike expect, this always advances even on a mismatch:
// there's no alternative grammar path a caller branches on by leaving a
// clearly-wrong name-position token in place (unlike, say, expect(Colon) in
// parseIfStmt, where leaving it unconsumed lets the caller try the brace
// form instead) - advancing here avoids the same bad token cascading
// through several unrelated "expected X" errors before anything makes
// progress.
func (p *Parser) expectIdent() lexer.Token {
	tok := p.tok
	if tok.Lexeme != enums.Lexemes.Identifier || tok.Keyword != "" {
		p.errorAtSpan(tok.Start, tok.End, "expected identifier, found %s", p.describe(tok))
	}
	p.advance()
	return tok
}

// atKeyword reports whether the current token is the keyword kw. Keywords
// all lex as Lexeme.Identifier with Token.Keyword set, so this - not at -
// is how grammar code checks for a specific one.
func (p *Parser) atKeyword(kw enums.Keyword) bool { return p.tok.Keyword == kw }

// acceptKeyword consumes and returns the current token if it is the
// keyword kw, without reporting an error when it doesn't - the
// keyword-aware counterpart to accept.
func (p *Parser) acceptKeyword(kw enums.Keyword) (lexer.Token, bool) {
	if !p.atKeyword(kw) {
		return lexer.Token{}, false
	}
	tok := p.tok
	p.advance()
	return tok, true
}

// expectKeyword consumes and returns the current token if it is the keyword
// kw, or - if not - reports "expected 'kw', found Y" and returns it
// unconsumed, the keyword-aware counterpart to expect.
func (p *Parser) expectKeyword(kw enums.Keyword) lexer.Token {
	if !p.atKeyword(kw) {
		p.errorAtSpan(p.tok.Start, p.tok.End, "expected %q, found %s", kw.Info().String, p.describe(p.tok))
		return p.tok
	}
	tok := p.tok
	p.advance()
	return tok
}

// sync advances, discarding tokens, until the current one is in stop (or
// EOF) - statement-level error recovery so one syntax mistake doesn't
// cascade into a wall of spurious follow-on errors.
func (p *Parser) sync(stop ...enums.Lexeme) {
	for !p.at(enums.Lexemes.EOF) {
		if slices.ContainsFunc(stop, p.at) {
			return
		}
		p.advance()
	}
}

// Run drives fn over a fresh Parser for file and returns fn's result
// alongside every diagnostic collected, recovering the internal bailout
// panic so a hopelessly broken parse returns normally instead of crashing
// the caller. Any other panic is a real bug, not ours to swallow, and
// propagates as usual.
func Run[T any](file *lexer.File, fn func(p *Parser) T) (result T, diags *diag.Bag) {
	p := New(file)
	defer func() {
		if r := recover(); r != nil {
			if _, ok := r.(bailout); !ok {
				panic(r)
			}
		}
		diags = p.diags
	}()
	result = fn(p)
	return
}
