package parser

import (
	"llvm_lang/src/ast"
	"llvm_lang/src/diag"
	"llvm_lang/src/enums"
	"llvm_lang/src/lexer"
)

// parseTopLevelItem parses one file-scope declaration: import (must come
// first in the file - see parseFile), var (a real global, not a script
// statement), func, or struct. Nothing else is legal here - LLVM has no
// notion of "just run a statement at global scope," only static data
// initializers, so executable logic needs an actual entry point (func
// main()) same as Go/C/Rust; see AGENTS.md's "Top level" section.
func (p *Parser) parseTopLevelItem() ast.NodeIndex {
	switch {
	case p.atKeyword(enums.Keywords.Import):
		return p.parseImportDecl()
	case p.atKeyword(enums.Keywords.Var):
		return p.parseVarDecl()
	case p.atKeyword(enums.Keywords.Func):
		return p.parseFuncDecl()
	case p.atKeyword(enums.Keywords.Struct):
		return p.parseStructDecl()
	default:
		tok := p.tok
		p.errorAtSpan(tok.Start, tok.End, "expected a top-level declaration (import, var, func, or struct), found %s", p.describe(tok))
		p.sync(enums.Lexemes.Semicolon)
		return p.badNode(tok)
	}
}

// parseImportDecl parses `import "path"` - see LANGUAGE.md's "Imports"
// section for the language-level rule (path resolution relative to the
// importing file, last-path-segment local naming, no aliasing yet). The
// path itself is kept as the node's own Tok (a String token, decoded via
// File.StringValue exactly like a StringLit expression - see ast.Node's own
// doc comment) rather than a child StringLit node, since there's nothing
// else for an ImportDecl to hold.
func (p *Parser) parseImportDecl() ast.NodeIndex {
	kwTok := p.expectKeyword(enums.Keywords.Import)
	pathTok := p.expect(enums.Lexemes.String)
	span := ast.Span{
		Start: kwTok.Start,
		End:   pathTok.End,
	}
	return p.tree.NewNode(enums.NodeKinds.ImportDecl, pathTok, span)
}

// parseFile parses a whole source file: a sequence of top-level
// declarations (see parseTopLevelItem) through EOF. Same separator rule as
// parseBlock (required between declarations, optional right before the
// terminator - here EOF instead of `}`).
//
// Every ImportDecl must come before any other top-level declaration -
// matching Go's own ordering rule (see LANGUAGE.md's "Imports" section):
// simplest to parse/read, and there's no real downside to requiring it. A
// later import is still parsed (so the rest of the file still gets checked
// normally) but reported as an error at its own position.
func (p *Parser) parseFile() ast.NodeIndex {
	start := p.tok.Start
	var decls []ast.NodeIndex
	sawNonImport := false
	for !p.at(enums.Lexemes.EOF) {
		if p.atKeyword(enums.Keywords.Import) && sawNonImport {
			p.errorAtSpan(p.tok.Start, p.tok.End, "import declarations must come before all other top-level declarations")
		}
		if !p.atKeyword(enums.Keywords.Import) {
			sawNonImport = true
		}
		decls = append(decls, p.parseTopLevelItem())
		if p.at(enums.Lexemes.EOF) {
			break
		}
		p.expect(enums.Lexemes.Semicolon)
	}
	span := ast.Span{
		Start: start,
		End:   p.tok.End,
	}
	root := p.tree.NewNode(enums.NodeKinds.File, lexer.Token{}, span, decls...)
	p.tree.Root = root
	return root
}

// ParseFile parses an entire source file into a Tree, using Run's bailout
// recovery so a hopelessly broken file still returns a (possibly partial)
// tree and every diagnostic collected, rather than panicking.
func ParseFile(file *lexer.File) (*ast.Tree, *diag.Bag) {
	return Run(file, func(p *Parser) *ast.Tree {
		p.parseFile()
		return p.tree
	})
}

// parseFuncDecl parses `func name(params) ReturnType { body }`, or, with a
// receiver clause, `func (Name) name(params) ReturnType { body }` - a
// method. The receiver is unambiguous: a plain function's name always comes
// directly after `func`, so a `(` there can only mean a receiver clause.
func (p *Parser) parseFuncDecl() ast.NodeIndex {
	kwTok := p.expectKeyword(enums.Keywords.Func)

	receiver := ast.InvalidNode
	if _, ok := p.accept(enums.Lexemes.LeftParen); ok {
		receiver = p.parseTypeExpr()
		p.expect(enums.Lexemes.RightParen)
	}

	nameTok := p.expectIdent()
	name := p.tree.NewNode(enums.NodeKinds.Ident, nameTok, tokenSpan(nameTok))

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
	return p.tree.NewNode(enums.NodeKinds.FuncDecl, kwTok, span, receiver, name, params, returnType, body)
}

// parseParamList parses a comma-separated `(name Type, ...)` list, wrapped
// in its own variable-arity node so FuncDecl itself stays fixed-arity (it
// has fixed slots both before and after the params: receiver/name, then
// returnType/body).
func (p *Parser) parseParamList() ast.NodeIndex {
	openTok := p.expect(enums.Lexemes.LeftParen)
	var params []ast.NodeIndex
	if !p.at(enums.Lexemes.RightParen) {
		params = append(params, p.parseParam())
		for {
			if _, ok := p.accept(enums.Lexemes.Comma); !ok {
				break
			}
			params = append(params, p.parseParam())
		}
	}
	closeTok := p.expect(enums.Lexemes.RightParen)
	span := ast.Span{
		Start: openTok.Start,
		End:   closeTok.End,
	}
	return p.tree.NewNode(enums.NodeKinds.ParamList, lexer.Token{}, span, params...)
}

func (p *Parser) parseParam() ast.NodeIndex {
	nameTok := p.expectIdent()
	name := p.tree.NewNode(enums.NodeKinds.Ident, nameTok, tokenSpan(nameTok))
	typ := p.parseTypeExpr()
	span := ast.Span{
		Start: nameTok.Start,
		End:   p.tree.SpanOf(typ).End,
	}
	return p.tree.NewNode(enums.NodeKinds.Param, lexer.Token{}, span, name, typ)
}

// parseStructDecl parses `struct Name { field Type ... }` - data-only, no
// nested methods (see AGENTS.md: methods are declared separately via
// `func (Name) method() {...}`, to keep multi-file organization like Go's
// receiver methods, and to keep this grammar rule from ever needing to
// parse a function body).
func (p *Parser) parseStructDecl() ast.NodeIndex {
	kwTok := p.expectKeyword(enums.Keywords.Struct)
	nameTok := p.expectIdent()
	name := p.tree.NewNode(enums.NodeKinds.Ident, nameTok, tokenSpan(nameTok))

	p.expect(enums.Lexemes.LeftBrace)
	fields := []ast.NodeIndex{name}
	for !p.at(enums.Lexemes.RightBrace) && !p.at(enums.Lexemes.EOF) {
		fields = append(fields, p.parseField())
		if p.at(enums.Lexemes.RightBrace) || p.at(enums.Lexemes.EOF) {
			break
		}
		p.expect(enums.Lexemes.Semicolon)
	}
	closeTok := p.expect(enums.Lexemes.RightBrace)

	span := ast.Span{
		Start: kwTok.Start,
		End:   closeTok.End,
	}
	return p.tree.NewNode(enums.NodeKinds.StructDecl, kwTok, span, fields...)
}

func (p *Parser) parseField() ast.NodeIndex {
	nameTok := p.expectIdent()
	name := p.tree.NewNode(enums.NodeKinds.Ident, nameTok, tokenSpan(nameTok))
	typ := p.parseTypeExpr()
	span := ast.Span{
		Start: nameTok.Start,
		End:   p.tree.SpanOf(typ).End,
	}
	return p.tree.NewNode(enums.NodeKinds.Field, lexer.Token{}, span, name, typ)
}
