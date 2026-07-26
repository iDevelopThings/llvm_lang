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
	case p.atKeyword(enums.Keywords.Func), p.atKeyword(enums.Keywords.Async):
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

// parseTopLevelDeclSequence parses a sequence of top-level declarations (see
// parseTopLevelItem) up to term (EOF for a whole file, `}` for a tests{}
// block's own body) - the shared "imports first, semicolon required between
// declarations but optional right before the terminator" rule parseFile and
// parseTestsBlock both need, so it isn't hand-duplicated in both places (see
// AGENTS.md's "look for a shared home" convention). A tests{} block is
// special-cased here (rather than routed through parseTopLevelItem's normal
// switch) since it can splice MULTIPLE decls into the sequence in test mode,
// unlike every other single-NodeIndex top-level item.
func (p *Parser) parseTopLevelDeclSequence(term enums.Lexeme) []ast.NodeIndex {
	var decls []ast.NodeIndex
	sawNonImport := false
	for !p.at(term) && !p.at(enums.Lexemes.EOF) {
		if p.atKeyword(enums.Keywords.Import) && sawNonImport {
			p.errorAtSpan(p.tok.Start, p.tok.End, "import declarations must come before all other top-level declarations")
		}
		if !p.atKeyword(enums.Keywords.Import) {
			sawNonImport = true
		}
		if p.atKeyword(enums.Keywords.Tests) {
			decls = append(decls, p.parseTestsBlock()...)
		} else {
			decls = append(decls, p.parseTopLevelItem())
		}
		if p.at(term) || p.at(enums.Lexemes.EOF) {
			break
		}
		p.expect(enums.Lexemes.Semicolon)
	}
	return decls
}

// parseFile parses a whole source file: parseTopLevelDeclSequence through
// EOF, wrapped in the File root node.
func (p *Parser) parseFile() ast.NodeIndex {
	start := p.tok.Start
	decls := p.parseTopLevelDeclSequence(enums.Lexemes.EOF)
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
// tree and every diagnostic collected, rather than panicking. testMode
// controls how any tests{} block in the file is attached - see
// parseTestsBlock.
func ParseFile(file *lexer.File, testMode bool) (*ast.Tree, *diag.Bag) {
	return Run(file, func(p *Parser) *ast.Tree {
		p.testMode = testMode
		p.parseFile()
		return p.tree
	})
}

// parseTestsBlock parses `tests { <decl>* }` (see LANGUAGE.md's "tests{}"
// section) - legal anywhere a top-level declaration is, in any file. Its
// body is always fully parsed regardless of p.testMode, so a syntax error
// inside is always caught; only how the result attaches to the enclosing
// decl sequence differs:
//   - testMode: the block's own parsed decls are returned unwrapped, to be
//     spliced directly into the caller's sequence as ordinary top-level
//     declarations.
//   - otherwise: the decls are wrapped in one TestBlockDecl node, invisible
//     to every downstream pass since nothing queries
//     TopLevelDeclsOfKind(TestBlockDecl) - see DECISIONS.md.
//
// Nesting a tests{} block inside another is rejected with a diagnostic, but
// still parsed/recovered rather than bailing the whole file.
func (p *Parser) parseTestsBlock() []ast.NodeIndex {
	kwTok := p.expectKeyword(enums.Keywords.Tests)

	if p.inTestBlock {
		p.errorAtSpan(kwTok.Start, kwTok.End, "tests blocks cannot be nested")
	}
	wasInTestBlock := p.inTestBlock
	p.inTestBlock = true

	p.expect(enums.Lexemes.LeftBrace)
	inner := p.parseTopLevelDeclSequence(enums.Lexemes.RightBrace)
	closeTok := p.expect(enums.Lexemes.RightBrace)

	p.inTestBlock = wasInTestBlock

	if p.testMode {
		return inner
	}

	span := ast.Span{
		Start: kwTok.Start,
		End:   closeTok.End,
	}
	block := p.tree.NewNode(enums.NodeKinds.TestBlockDecl, kwTok, span, inner...)
	return []ast.NodeIndex{block}
}

// parseFuncDecl parses `func name(params) ReturnType { body }`, or, with a
// receiver clause, `func (Name) name(params) ReturnType { body }` - a
// method. The receiver is unambiguous: a plain function's name always comes
// directly after `func`, so a `(` there can only mean a receiver clause.
//
// An optional leading `async` (`async func name(params) ReturnType { body }`
// - see LANGUAGE.md's "Coroutines" section) is captured as the node's own
// Tok instead of the `func` keyword that still must follow it - see
// ast.Node's own FuncDecl doc comment and Tree.FuncIsAsync. A receiver
// clause or a `FuncLit` combined with async is a clean sema-level rejection
// (checkFuncDecl), not a grammar one - this function has no idea whether a
// receiver clause follows, and a FuncLit's own parse function never checks
// for a leading async at all, so the combination is structurally
// unreachable there.
func (p *Parser) parseFuncDecl() ast.NodeIndex {
	var kwTok lexer.Token
	if p.atKeyword(enums.Keywords.Async) {
		kwTok = p.expectKeyword(enums.Keywords.Async)
		p.expectKeyword(enums.Keywords.Func)
	} else {
		kwTok = p.expectKeyword(enums.Keywords.Func)
	}

	receiver := ast.InvalidNode
	if _, ok := p.accept(enums.Lexemes.LeftParen); ok {
		receiver = p.parseReceiver()
		p.expect(enums.Lexemes.RightParen)
	}

	// A free function's own name can stand alone as a bare called value
	// (`add(1, 2)`), so it goes through expectIdent same as a var/type name
	// - see that function's own doc comment for why a keyword can't be
	// reused there. A method's name is only ever reached through
	// `receiver.name(...)`, exactly like a struct field - see
	// expectMemberName's own doc comment for why that has no such
	// restriction - except for constructor/destructor specifically: those
	// two already name a completely different, unnamed struct-level
	// construct (see LANGUAGE.md's "Constructors"/"Destructors" sections),
	// so a method ALSO answering to one of those names would silently
	// coexist with, and be trivially confused with, that real mechanism -
	// reported the same way expectIdent rejects any other reserved name.
	var nameTok lexer.Token
	switch {
	case receiver == ast.InvalidNode:
		nameTok = p.expectIdent()
	case p.atKeyword(enums.Keywords.Constructor), p.atKeyword(enums.Keywords.Destructor):
		nameTok = p.tok
		p.errorAtSpan(nameTok.Start, nameTok.End, "%s is reserved for a struct's own constructor/destructor block, not a method name", p.describe(nameTok))
		p.advance()
	default:
		nameTok = p.expectMemberName()
	}
	name := p.tree.NewNode(enums.NodeKinds.Ident, nameTok, tokenSpan(nameTok))

	typeParams := ast.InvalidNode
	if p.at(enums.Lexemes.LeftBracket) {
		typeParams = p.parseTypeParamList(lexer.Token{})
	}

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
	return p.tree.NewNode(enums.NodeKinds.FuncDecl, kwTok, span, receiver, name, typeParams, params, returnType, body)
}

// parseReceiver parses a method's receiver clause contents - a bare type name
// (`Point`), or a generic one naming the receiver's own type parameters
// (`SlotMap[T]` - see LANGUAGE.md's "Generics" section). Deliberately its own
// tiny rule rather than parseTypeExpr: a receiver clause is a declaration
// position, always exactly one identifier optionally followed by a
// type-parameter list, never an array/pointer/map/func type - and sema
// resolves it by name text (see sema's addMethod), which only an Ident-shaped
// node ever supplied anyway.
func (p *Parser) parseReceiver() ast.NodeIndex {
	nameTok := p.expectIdent()
	if !p.at(enums.Lexemes.LeftBracket) {
		return p.tree.NewNode(enums.NodeKinds.Ident, nameTok, tokenSpan(nameTok))
	}
	return p.parseTypeParamList(nameTok)
}

// parseTypeParamList parses `[T]` / `[A, B]` - one Ident child per declared
// type parameter. tok becomes the resulting node's own Tok: the zero Token for
// a declaration's own type-parameter list, or the receiver type's name token
// when this is a receiver clause (see ast.Node's TypeParamList doc comment).
func (p *Parser) parseTypeParamList(tok lexer.Token) ast.NodeIndex {
	openTok := p.expect(enums.Lexemes.LeftBracket)
	names := p.parseCommaList(enums.Lexemes.RightBracket, func() ast.NodeIndex {
		nameTok := p.expectIdent()
		return p.tree.NewNode(enums.NodeKinds.Ident, nameTok, tokenSpan(nameTok))
	})
	closeTok := p.expect(enums.Lexemes.RightBracket)
	start := openTok.Start
	if tok.Start != tok.End {
		start = tok.Start
	}
	span := ast.Span{
		Start: start,
		End:   closeTok.End,
	}
	return p.tree.NewNode(enums.NodeKinds.TypeParamList, tok, span, names...)
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
// from three narrow, deliberate exceptions: a `constructor(params) { body }`
// block (see LANGUAGE.md's "Constructors" section), a `destructor() { body
// }` block (see LANGUAGE.md's "Destructors" section), or an
// `operator OP(param) RetType { body }` block (see LANGUAGE.md's "Operator
// overloading" section) may also appear directly inside the braces -
// everything else (an ordinary method) is still declared separately via
// `func (Name) method() {...}`, to keep multi-file organization like Go's
// receiver methods, and to keep this grammar rule from ever needing to parse
// a full method's signature (name, receiver, return type).
func (p *Parser) parseStructDecl() ast.NodeIndex {
	kwTok := p.expectKeyword(enums.Keywords.Struct)
	nameTok := p.expectIdent()
	name := p.tree.NewNode(enums.NodeKinds.Ident, nameTok, tokenSpan(nameTok))

	typeParams := ast.InvalidNode
	if p.at(enums.Lexemes.LeftBracket) {
		typeParams = p.parseTypeParamList(lexer.Token{})
	}

	p.expect(enums.Lexemes.LeftBrace)
	members := append([]ast.NodeIndex{name, typeParams}, p.parseSemiList(enums.Lexemes.RightBrace, p.parseStructMember)...)
	closeTok := p.expect(enums.Lexemes.RightBrace)

	span := ast.Span{
		Start: kwTok.Start,
		End:   closeTok.End,
	}
	return p.tree.NewNode(enums.NodeKinds.StructDecl, kwTok, span, members...)
}

// parseStructMember parses one element of a struct body - either an
// ordinary field, or (the three exceptions - see parseStructDecl) a
// constructor, destructor, or operator-overload block, disambiguated by
// whether the element starts with the `constructor`/`destructor`/`operator`
// keyword.
func (p *Parser) parseStructMember() ast.NodeIndex {
	switch {
	case p.atKeyword(enums.Keywords.Constructor):
		return p.parseConstructorDecl()
	case p.atKeyword(enums.Keywords.Destructor):
		return p.parseDestructorDecl()
	case p.atKeyword(enums.Keywords.Operator):
		return p.parseOperatorDecl()
	default:
		return p.parseField()
	}
}

func (p *Parser) parseField() ast.NodeIndex {
	nameTok := p.expectMemberName()
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

// parseOperatorDecl parses `operator OP(param) RetType { body }` - see
// ast.Node's own OperatorDecl doc comment for the [paramList, returnType,
// body] shape: no name (like a constructor, it's never called by name - see
// LANGUAGE.md's "Operator overloading" section), no receiver clause, but -
// unlike a constructor/destructor - a real explicit return type, parsed the
// same way parseField already parses a field's type.
//
// expectOperatorToken only checks that the token is *shaped* like one of
// this grammar's own single-token binary operators - a purely structural
// check. Which of those are actually legal to overload (binary `+ - * /`,
// unary `-` only) and whether the declared parameter
// count (0 or 1) matches is sema's own job (declareOperator, resolve.go),
// the same "grammar accepts the general shape, sema narrows it" split
// parseDestructorDecl's always-empty-paramList rule already uses.
func (p *Parser) parseOperatorDecl() ast.NodeIndex {
	kwTok := p.expectKeyword(enums.Keywords.Operator)
	opTok := p.expectOperatorToken()

	params := p.parseParamList()
	returnType := p.parseTypeExpr()
	body := p.parseBlock()

	span := ast.Span{
		Start: kwTok.Start,
		End:   p.tree.SpanOf(body).End,
	}
	return p.tree.NewNode(enums.NodeKinds.OperatorDecl, opTok, span, params, returnType, body)
}

// expectOperatorToken consumes and returns the current token if it's one of
// this grammar's own recognized single-token *binary* operators - every
// infixRules entry (expr.go) below precPostfix, i.e. every key there
// EXCEPT `(`/`[`/`.` (call/index/member): those three are also infixRules
// entries, at precPostfix, but are postfix operators, not binary ones, and
// so must never be accepted as an operator-overload token here (`operator
// (v T) ...` must not silently treat `(` itself as the operator). A bare
// structural check, not itself a bare-token dispatch: an OperatorDecl's own
// Tok becomes this exact token (see ast.Node's doc comment), same as
// expect's own "consume and return" shape, just keyed against a set of
// lexemes instead of one.
func (p *Parser) expectOperatorToken() lexer.Token {
	tok := p.tok
	if rule, ok := infixRules[tok.Lexeme]; !ok || rule.prec >= precPostfix {
		p.errorAtSpan(tok.Start, tok.End, "expected an operator (+, -, *, /) after 'operator', found %s", p.describe(tok))
		return tok
	}
	p.advance()
	return tok
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
