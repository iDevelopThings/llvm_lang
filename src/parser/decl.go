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
	case p.atKeyword(enums.Keywords.Extern):
		return p.parseExternFuncDecl()
	case p.atKeyword(enums.Keywords.Enum):
		return p.parseEnumDecl()
	default:
		tok := p.tok
		p.errorAtSpan(tok.Start, tok.End, "expected a top-level declaration (import, var, func, struct, enum, or extern), found %s", p.describe(tok))
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
		returnType = p.parseFuncDeclReturnType()
	}

	body := p.parseBlock()

	span := ast.Span{
		Start: kwTok.Start,
		End:   p.tree.SpanOf(body).End,
	}
	return p.tree.NewNode(enums.NodeKinds.FuncDecl, kwTok, span, receiver, name, params, returnType, body)
}

// parseFuncDeclReturnType parses a FuncDecl's own return-type position: a
// plain type (`func f() int`, completely unchanged - see parseTypeExpr), a
// parenthesized, comma-separated list of 2+ types (`func f() (int, bool)` -
// see LANGUAGE.md's "Go-style multi-return values" section), wrapped in a
// MultiReturnType node the same way parseParamList wraps FuncDecl's own
// variable-arity params (see ast.Node's own MultiReturnType doc comment), or
// a `yield T` generator return-type marker (see LANGUAGE.md's "Generator
// functions" section), wrapped in a YieldReturnType node. Deliberately its
// own function, not folded into parseTypeExpr itself - a bare type position
// never starts with `(` or the `yield` keyword anywhere else in this grammar
// (a var's type annotation, a param's type, a struct field's type, an array
// element type, a FuncType/FuncLit/ExternFuncDecl's own return type all still
// call parseTypeExpr directly, unchanged), so both new cases only ever apply
// right here, at a FuncDecl's own return-type position - the one place each
// round that added one actually needed it. A method's receiver-clause
// restriction on `yield` (a generator can't be a method) is enforced by sema,
// not here - this function has no idea whether a receiver clause preceded it.
//
// A parenthesized list of fewer than 2 types (`func f() ()`, `func f() (int)`)
// still parses fine structurally - the same "grammar accepts the general
// shape, sema enforces the feature's own narrower rule" division of labor
// this project already uses for a duplicate-arity constructor or a non-empty
// destructor param list (see sema.checkDestructorDecl) - see
// sema.multiReturnTypeFromNode for the actual "at least 2" diagnostic.
func (p *Parser) parseFuncDeclReturnType() ast.NodeIndex {
	if p.atKeyword(enums.Keywords.Yield) {
		return p.parseYieldReturnType()
	}
	if !p.at(enums.Lexemes.LeftParen) {
		return p.parseTypeExpr()
	}
	openTok := p.expect(enums.Lexemes.LeftParen)
	types := p.parseCommaList(enums.Lexemes.RightParen, p.parseTypeExpr)
	closeTok := p.expect(enums.Lexemes.RightParen)
	span := ast.Span{
		Start: openTok.Start,
		End:   closeTok.End,
	}
	return p.tree.NewNode(enums.NodeKinds.MultiReturnType, lexer.Token{}, span, types...)
}

// parseYieldReturnType parses `yield T` in a FuncDecl's own return-type
// position (see LANGUAGE.md's "Generator functions" section) - the `yield`
// keyword followed by the generator's own single yielded element type.
func (p *Parser) parseYieldReturnType() ast.NodeIndex {
	kwTok := p.expectKeyword(enums.Keywords.Yield)
	elemType := p.parseTypeExpr()
	span := ast.Span{
		Start: kwTok.Start,
		End:   p.tree.SpanOf(elemType).End,
	}
	return p.tree.NewNode(enums.NodeKinds.YieldReturnType, kwTok, span, elemType)
}

// parseExternFuncDecl parses `extern func Name(params) RetType` - a top-level
// FFI declaration binding an external C symbol, with no body at all (see
// ast.Node's own ExternFuncDecl doc comment and LANGUAGE.md's "External
// functions (FFI)" section). Reuses parseFuncDecl's own param-list parsing
// verbatim, but deliberately not its return-type parsing: the return type
// here is a plain parseTypeExpr(), not parseFuncDeclReturnType(), so an
// extern func can never declare a parenthesized multi-return list - matching
// the FFI's own ABI restriction that an external function can't return
// multiple values. Also skips the receiver-clause parsing (an extern func
// can never be a method) and skips a `{ ... }` body entirely - the
// declaration simply ends right after the optional return type, exactly like
// a type-less `var` already does for statement termination (parseFile's own
// semicolon-separator loop handles that, same as every other top-level item).
func (p *Parser) parseExternFuncDecl() ast.NodeIndex {
	kwTok := p.expectKeyword(enums.Keywords.Extern)
	p.expectKeyword(enums.Keywords.Func)

	nameTok := p.expectIdent()
	name := p.tree.NewNode(enums.NodeKinds.Ident, nameTok, tokenSpan(nameTok))

	params := p.parseParamList()

	returnType := ast.InvalidNode
	if !p.at(enums.Lexemes.Semicolon) && !p.at(enums.Lexemes.EOF) {
		returnType = p.parseTypeExpr()
	}

	end := p.tree.SpanOf(params).End
	if returnType != ast.InvalidNode {
		end = p.tree.SpanOf(returnType).End
	}
	span := ast.Span{
		Start: kwTok.Start,
		End:   end,
	}
	return p.tree.NewNode(enums.NodeKinds.ExternFuncDecl, kwTok, span, name, params, returnType)
}

// parseParamList parses a comma-separated `(name Type, ...)` list, wrapped
// in its own variable-arity node so FuncDecl itself stays fixed-arity (it
// has fixed slots both before and after the params: receiver/name, then
// returnType/body).
func (p *Parser) parseParamList() ast.NodeIndex {
	openTok := p.expect(enums.Lexemes.LeftParen)
	params := p.parseCommaList(enums.Lexemes.RightParen, p.parseParam)
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

// parseStructDecl parses `struct Name { field Type ... }` - data-only aside
// from two narrow, deliberate exceptions: a `constructor(params) { body }`
// block (see LANGUAGE.md's "Constructors" section) or a `destructor() { body
// }` block (see LANGUAGE.md's "Destructors" section) may also appear directly
// inside the braces - everything else (an ordinary method) is still declared
// separately via `func (Name) method() {...}`, to keep multi-file
// organization like Go's receiver methods, and to keep this grammar rule
// from ever needing to parse a full method's signature (name, receiver,
// return type).
func (p *Parser) parseStructDecl() ast.NodeIndex {
	kwTok := p.expectKeyword(enums.Keywords.Struct)
	nameTok := p.expectIdent()
	name := p.tree.NewNode(enums.NodeKinds.Ident, nameTok, tokenSpan(nameTok))

	p.expect(enums.Lexemes.LeftBrace)
	members := append([]ast.NodeIndex{name}, p.parseSemiList(enums.Lexemes.RightBrace, p.parseStructMember)...)
	closeTok := p.expect(enums.Lexemes.RightBrace)

	span := ast.Span{
		Start: kwTok.Start,
		End:   closeTok.End,
	}
	return p.tree.NewNode(enums.NodeKinds.StructDecl, kwTok, span, members...)
}

// parseStructMember parses one element of a struct body - either an
// ordinary field, or (the two exceptions - see parseStructDecl) a
// constructor or destructor block, disambiguated by whether the element
// starts with the `constructor`/`destructor` keyword.
func (p *Parser) parseStructMember() ast.NodeIndex {
	switch {
	case p.atKeyword(enums.Keywords.Constructor):
		return p.parseConstructorDecl()
	case p.atKeyword(enums.Keywords.Destructor):
		return p.parseDestructorDecl()
	default:
		return p.parseField()
	}
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

// parseConstructorDecl parses `constructor(params) { body }` - see
// ast.Node's own ConstructorDecl doc comment for the [paramList, body] shape:
// no name (it's overload-resolved by argument count, not called by name -
// see LANGUAGE.md's "Constructors" section), no receiver clause, and no
// return type, unlike a full `parseFuncDecl`.
func (p *Parser) parseConstructorDecl() ast.NodeIndex {
	kwTok := p.expectKeyword(enums.Keywords.Constructor)
	params := p.parseParamList()
	body := p.parseBlock()

	span := ast.Span{
		Start: kwTok.Start,
		End:   p.tree.SpanOf(body).End,
	}
	return p.tree.NewNode(enums.NodeKinds.ConstructorDecl, kwTok, span, params, body)
}

// parseDestructorDecl parses `destructor() { body }` - see ast.Node's own
// DestructorDecl doc comment for the [paramList, body] shape, identical to
// parseConstructorDecl's own: no name, no receiver clause, and no return
// type. Unlike a constructor, a destructor's own paramList is always
// semantically required to be empty - sema, not this grammar rule, rejects a
// non-empty one (see sema.checkDestructorDecl), the same division of labor
// duplicate-arity constructor rejection already uses (grammar accepts the
// general shape, sema enforces the feature's own narrower rule on top of
// it).
func (p *Parser) parseDestructorDecl() ast.NodeIndex {
	kwTok := p.expectKeyword(enums.Keywords.Destructor)
	params := p.parseParamList()
	body := p.parseBlock()

	span := ast.Span{
		Start: kwTok.Start,
		End:   p.tree.SpanOf(body).End,
	}
	return p.tree.NewNode(enums.NodeKinds.DestructorDecl, kwTok, span, params, body)
}

// parseEnumDecl parses `enum Name { Variant1, Variant2(T1, T2), Variant3 {
// f Type }, destructor() {...} }` - see LANGUAGE.md's "Enums" section.
func (p *Parser) parseEnumDecl() ast.NodeIndex {
	kwTok := p.expectKeyword(enums.Keywords.Enum)
	nameTok := p.expectIdent()
	name := p.tree.NewNode(enums.NodeKinds.Ident, nameTok, tokenSpan(nameTok))

	p.expect(enums.Lexemes.LeftBrace)
	members := append([]ast.NodeIndex{name}, p.parseEnumMemberList()...)
	closeTok := p.expect(enums.Lexemes.RightBrace)

	span := ast.Span{
		Start: kwTok.Start,
		End:   closeTok.End,
	}
	return p.tree.NewNode(enums.NodeKinds.EnumDecl, kwTok, span, members...)
}

// parseEnumMemberList parses every member of an enum body (see
// parseEnumMember) up to the closing `}` - members are comma-separated
// (matching the worked syntax, `Variant1, Variant2(T1, T2), ...`), but
// deliberately NOT via the shared parseCommaList helper every other
// comma-separated grammar rule in this package uses: a bare unit variant
// name, or a tuple/struct variant's own trailing `)`/`}`, is exactly the
// kind of token the lexer's own ASI rule (asiEligible) already treats as
// "a newline here ends the statement" - writing each variant on its own
// line with no trailing comma at all (the natural Rust-style layout this
// example's own worked syntax doesn't literally show, but obviously implies
// is fine too) would otherwise insert a stray semicolon between one member
// and the next that a strict comma-only list has no way to tolerate. This
// loop instead consumes any run of commas and/or semicolons (explicit or
// ASI-inserted) between members - covering a trailing Rust-style comma, a
// bare ASI-only line break, or any mix of the two - uniformly.
func (p *Parser) parseEnumMemberList() []ast.NodeIndex {
	var members []ast.NodeIndex
	for !p.at(enums.Lexemes.RightBrace) && !p.at(enums.Lexemes.EOF) {
		members = append(members, p.parseEnumMember())
		for {
			if _, ok := p.accept(enums.Lexemes.Comma); ok {
				continue
			}
			if _, ok := p.accept(enums.Lexemes.Semicolon); ok {
				continue
			}
			break
		}
	}
	return members
}

// parseEnumMember parses one element of an enum body - either an ordinary
// variant (see parseEnumVariant) or (the same narrow exception a struct
// already carries - see parseStructMember) a destructor block, disambiguated
// by whether the element starts with the `destructor` keyword.
func (p *Parser) parseEnumMember() ast.NodeIndex {
	if p.atKeyword(enums.Keywords.Destructor) {
		return p.parseDestructorDecl()
	}
	return p.parseEnumVariant()
}

// parseEnumVariant parses one variant - a bare name (a unit variant,
// `Point`), a name followed by a parenthesized comma-separated type list (a
// tuple variant, `Circle(f64)`), or a name followed by a braced
// comma-separated field list (a struct variant, `Triangle { base f64, height
// f64 }`, reusing parseField - this project's existing `name Type`
// field-declaration shape - verbatim, just comma- rather than
// semicolon-separated here). See ast.Node's own EnumVariant doc comment for
// the three resulting shapes.
func (p *Parser) parseEnumVariant() ast.NodeIndex {
	nameTok := p.expectIdent()
	end := nameTok.End

	var children []ast.NodeIndex
	switch {
	case p.at(enums.Lexemes.LeftParen):
		p.advance()
		children = p.parseCommaList(enums.Lexemes.RightParen, p.parseTypeExpr)
		closeTok := p.expect(enums.Lexemes.RightParen)
		end = closeTok.End
	case p.at(enums.Lexemes.LeftBrace):
		p.advance()
		children = p.parseEnumFieldList()
		closeTok := p.expect(enums.Lexemes.RightBrace)
		end = closeTok.End
	}

	span := ast.Span{
		Start: nameTok.Start,
		End:   end,
	}
	return p.tree.NewNode(enums.NodeKinds.EnumVariant, nameTok, span, children...)
}

// parseEnumFieldList parses a struct-variant's own braced field list
// (`{ base f64, height f64 }`) - the same tolerant-separator shape
// parseEnumMemberList uses one level up, for the identical reason: a
// field's own type is frequently a bare identifier (ASI-eligible), so a
// multi-line, one-field-per-line layout with no trailing comma needs the
// same comma-and/or-semicolon tolerance a strict parseCommaList can't give.
func (p *Parser) parseEnumFieldList() []ast.NodeIndex {
	var fields []ast.NodeIndex
	for !p.at(enums.Lexemes.RightBrace) && !p.at(enums.Lexemes.EOF) {
		fields = append(fields, p.parseField())
		for {
			if _, ok := p.accept(enums.Lexemes.Comma); ok {
				continue
			}
			if _, ok := p.accept(enums.Lexemes.Semicolon); ok {
				continue
			}
			break
		}
	}
	return fields
}
