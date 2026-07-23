// Package sema's second phase: type checking, built on top of Resolve's
// output (Info.Refs, Info.Scopes, Info.Structs). See Check's doc comment.
package sema

import (
	"fmt"
	"strconv"
	"strings"

	"llvm_lang/src/ast"
	"llvm_lang/src/diag"
	"llvm_lang/src/enums"
)

// funcSignature is a callable's parameter/return types - a declared free
// function or method's (derived from its FuncDecl node), or an indirect
// call's (derived from a function-typed value's own TypeFunc - see
// funcType/funcSigForCall). It exists as its own small struct, distinct
// from Type, purely so checkCallExpr's argument-count/type checking has one
// shape to check against regardless of which of those two a given call's
// callee turns out to be - a funcSignature never becomes an expression's
// own Type; only a real Type (TypeFunc, for a bare function reference or a
// function-typed variable) does that. See funcSigForCall.
type funcSignature struct {
	Params []Type
	Return Type
}

// funcType builds the Type (Kind: TypeFunc) describing sig - used when a
// bare function name is referenced as a value rather than called (see
// checkIdentExpr). ret is boxed into a fresh *Type since Type.Return must be
// a pointer (see Type's own doc comment - a function type may itself return
// another function type).
func funcType(sig funcSignature) Type {
	ret := sig.Return
	return Type{
		Kind:   TypeFunc,
		Params: sig.Params,
		Return: &ret,
	}
}

// enclosingFunc is the return-type context checkReturnStmt checks against -
// pushed once per FuncDecl, or, now that a function literal (FuncLit - see
// LANGUAGE.md's "Lambdas" section) can nest arbitrarily deep inside another
// function or another literal, once per FuncLit too (checkFuncLit). curFunc
// itself is still just a single checker field, not an explicit stack - each
// of checkFuncDecl/checkFuncLit saves the previous value in a local, sets its
// own, recurses into checkBlock, and restores the saved value afterward, so
// Go's own call stack does the actual nesting for free (checkFuncLit calling
// into a doubly-nested FuncLit's own checkFuncLit recurses the identical way,
// one level deeper) - there's nothing here that needs a real stack slice.
type enclosingFunc struct {
	hasReturn bool // whether the function declared a return type at all
	ret       Type // meaningful only when hasReturn is true
}

// nodeRef pairs a NodeIndex with the Tree it's relative to. Every per-node
// memoization cache below used to be keyed by a bare ast.NodeIndex, which
// was fine when there was only ever one Tree in play; now that Check can
// follow a Symbol's Decl into a different file's Tree entirely (see
// checker.pushTree), a bare NodeIndex is ambiguous - file A's decl #5 and
// file B's decl #5 are unrelated nodes that would otherwise collide in the
// same map. See LANGUAGE.md's "Multi-file packages" section.
type nodeRef struct {
	tree *ast.Tree
	idx  ast.NodeIndex
}

// checker's tree/info/diags fields always describe "whichever file is
// currently active" - almost always the file whose own top-level
// declarations checkFile is walking, but not always: computing another
// file's declared function's signature (funcSigForDecl) or a variable's
// type (declType) needs to read *that* file's own nodes (paramList,
// return-type annotation, initializer...), so a cross-file Symbol
// dereference (a call to a function declared elsewhere, a struct used
// outside its own file, `this` referring to a struct declared in another
// file) temporarily switches these three fields to the Symbol's own Tree via
// pushTree, then restores them once that one lookup is done. Every
// memoization cache (declTypes/computingDecl/typeNodeCache/funcSigs) is
// keyed by nodeRef{tree, node} for exactly this reason - "tree" here is
// always whatever c.tree happens to be at the moment of the call, which
// pushTree guarantees is the node's own owning file.
type checker struct {
	infos    map[*ast.Tree]*Info
	allDiags map[*ast.Tree]*diag.Bag

	// treePackage names which package's own top-level Scope each tree
	// belongs to - nil for a plain single-package Check/CheckPackage call
	// (which has no cross-package concept at all, and so never restricts
	// anything - see checkExportedAccess). Set by CheckProgram for a real
	// multi-package program (see ResolveProgram, which builds exactly this
	// map for exactly this reason).
	treePackage map[*ast.Tree]*Scope

	tree  *ast.Tree
	diags *diag.Bag
	info  *Info

	// curPkgScope is treePackage[c.tree] - refreshed every enter (never by
	// pushTree, which only ever borrows another tree's *declaration* nodes
	// for the duration of one lookup; the "who is doing the accessing"
	// package identity checkExportedAccess needs always stays whatever
	// enter last set it to). nil in the same single-package case
	// treePackage itself is nil for.
	curPkgScope *Scope

	// declTypes memoizes the type of each var-introducing declaration
	// (VarDecl, ShortVarDecl, Param). computingDecl is a cycle guard: a
	// top-level var can forward-reference another (see resolve.go's pass
	// ordering, now across files too), so declType is re-entrant and needs
	// to detect `var a = b; var b = a` even when a and b live in different
	// files.
	declTypes     map[nodeRef]Type
	computingDecl map[nodeRef]bool

	// typeNodeCache memoizes typeFromNode by the type-position node
	// itself. Without it, the same struct field's type node - reachable
	// from both checkStructDecl's upfront pass and every composite literal
	// that names that field, possibly from another file - would re-run
	// (and, for a dynamic-array field, re-report) the same conversion once
	// per reference.
	typeNodeCache map[nodeRef]Type

	// funcSigs memoizes each FuncDecl's signature, keyed by the FuncDecl
	// node. Computing it also type-checks its params/return type exactly
	// once, however many call sites (or zero, or from another file) end up
	// asking for it.
	funcSigs map[nodeRef]funcSignature

	curFunc *enclosingFunc

	// loopDepth counts how many enclosing ForStmt bodies checkStmt is
	// currently inside - incremented/decremented around checkForStmt's own
	// checkBlock call, mirroring how curFunc tracks "am I inside a
	// function" for checkReturnStmt. A BreakStmt/ContinueStmt is only
	// valid while this is > 0 - see checkBreakOrContinue. It doesn't need
	// to distinguish *which* enclosing loop (that's codegen's loopStack's
	// job, once this pass has already guaranteed one exists at all).
	loopDepth int

	// computingCopyable is structCopyable's own cycle guard - a struct's
	// Copyable can depend (transitively, through a field) on another
	// struct's, possibly not computed yet (see structCopyable) - mirroring
	// computingDecl's identical role for declType, just keyed by *StructInfo
	// instead of nodeRef, since copyability is a fact about the struct type
	// itself, not about any one declaration node.
	computingCopyable map[*StructInfo]bool
}

// enter switches the checker's current-file bookkeeping to tree
// unconditionally - used by checkPackage's own per-file pass loops, as
// opposed to pushTree's save-and-restore swap for a one-off cross-file
// Symbol dereference in the middle of checking some other file's body.
func (c *checker) enter(tree *ast.Tree) {
	c.tree = tree
	c.info = c.infos[tree]
	c.diags = c.allDiags[tree]
	c.curPkgScope = c.treePackage[tree]
}

// pushTree switches the checker's current-file bookkeeping to owner - the
// Tree that actually declares whatever Symbol is about to be dereferenced
// via its Decl node (see Symbol.Tree's doc comment) - for the duration of
// that one lookup, returning a restore func (call it once done, typically
// via defer) that puts the caller's own tree/info/diags back. A same-file
// dereference (owner == c.tree) or a predeclared symbol with no owning file
// at all (owner == nil) is a no-op.
func (c *checker) pushTree(owner *ast.Tree) (restore func()) {
	if owner == nil || owner == c.tree {
		return func() {}
	}
	prevTree, prevInfo, prevDiags := c.tree, c.info, c.diags
	c.tree = owner
	c.info = c.infos[owner]
	c.diags = c.allDiags[owner]
	return func() {
		c.tree = prevTree
		c.info = prevInfo
		c.diags = prevDiags
	}
}

// Check type-checks tree, using the name/scope resolution info already
// computed by Resolve (Info.Refs, Info.Scopes, Info.Structs). It fills in
// info.Types with every expression's inferred Type, and resolves what
// Resolve deliberately deferred - a MemberExpr's field/method name and a
// CompositeLit's keyed element names - now that an object's type can
// actually be inferred (see resolve.go's doc comments for exactly what was
// left unresolved and why).
//
// Check is the single-file case of CheckPackage - see its doc comment for
// the multi-file package entry point this wraps.
func Check(tree *ast.Tree, info *Info) *diag.Bag {
	return CheckPackage([]*ast.Tree{tree}, map[*ast.Tree]*Info{tree: info})[tree]
}

// CheckPackage type-checks every file in trees as one package: a call/
// reference in one file to a func/var/struct declared in another resolves
// and type-checks exactly like a same-file one would (see
// LANGUAGE.md's "Multi-file packages" section) - funcSigForDecl/declType/
// typeFromNode transparently follow a Symbol into its own declaring file via
// pushTree whenever that differs from whichever file is currently being
// checked.
//
// infos must already hold one *Info per tree (the output of ResolvePackage,
// or - for the single-file case - Resolve) - Check fills in each one's Types
// map in place. Diagnostics land in one fresh Bag per file, same convention
// as ResolvePackage's own per-file Bags (a diagnostic's Pos is only
// meaningful relative to the one file it's reported against) - Check never
// panics on a type error: every violation is recorded and checking
// continues, so one mistake doesn't hide the rest (see AGENTS.md; unlike the
// parser's token-stream bailout, walking an already-finite tree has no
// unbounded-input risk to guard against, so there's no error-count cap
// here).
func CheckPackage(trees []*ast.Tree, infos map[*ast.Tree]*Info) map[*ast.Tree]*diag.Bag {
	return CheckProgram(trees, infos, nil)
}

// CheckProgram is CheckPackage extended for a whole multi-package program:
// trees/infos span every package in the program at once (see
// sema.ResolveProgram, which produces exactly that flat, merged shape - a
// declaration only ever lives in one tree regardless of which package it
// belongs to, so type-checking every tree in one pass needs no
// package-boundary awareness for anything except export enforcement).
// treePackage names which package's own top-level Scope each tree belongs
// to (also produced by ResolveProgram) - used only by checkExportedAccess to
// decide whether a field/method/package-member access crosses a package
// boundary; nil disables that check entirely (the plain single-package
// case - see CheckPackage - where nothing should ever be restricted).
//
// This works at all because every codegen/checker-internal lookup a cross-
// file reference needs (funcSigForDecl, declType, typeFromNode, pushTree)
// is already keyed by *Symbol/nodeRef pointer identity, not by which
// package a tree happens to belong to - see CODEGEN.md's "Multi-file
// packages" section for the identical reasoning one layer down.
func CheckProgram(trees []*ast.Tree, infos map[*ast.Tree]*Info, treePackage map[*ast.Tree]*Scope) map[*ast.Tree]*diag.Bag {
	c := &checker{
		infos:             infos,
		allDiags:          make(map[*ast.Tree]*diag.Bag, len(trees)),
		treePackage:       treePackage,
		declTypes:         make(map[nodeRef]Type),
		computingDecl:     make(map[nodeRef]bool),
		typeNodeCache:     make(map[nodeRef]Type),
		funcSigs:          make(map[nodeRef]funcSignature),
		computingCopyable: make(map[*StructInfo]bool),
	}
	for _, tree := range trees {
		c.allDiags[tree] = diag.NewBag()
		infos[tree].Types = make(map[ast.NodeIndex]Type)
	}

	c.checkPackage(trees)

	out := make(map[*ast.Tree]*diag.Bag, len(trees))
	for _, tree := range trees {
		out[tree] = c.allDiags[tree]
	}
	return out
}

func (c *checker) errorAt(n ast.NodeIndex, format string, a ...any) {
	span := c.tree.SpanOf(n)
	c.diags.ErrorfSpan(span.Start, span.End, format, a...)
}

// errorAtLabel is errorAt, plus a short trailing hint (see
// diag.Bag.ErrorfLabel) pointing at n's own "main" token (Node.Tok) rather
// than n's whole span - e.g. just the field/method name in a struct-value
// access, not the whole `p.field` expression.
func (c *checker) errorAtLabel(n ast.NodeIndex, label, format string, a ...any) {
	tok := c.tree.Nodes[n].Tok
	c.diags.ErrorfLabel(tok.Start, tok.End, label, format, a...)
}

// errorAtNodes reports an error spanning from the start of nodes' first
// element to the end of its last - used to underline a specific contiguous
// sub-range (an argument list, a composite literal's elements) narrower than
// some enclosing node's own full span. Falls back to fallback (typically the
// enclosing node itself) when nodes is empty, since there's nothing to span.
func (c *checker) errorAtNodes(nodes []ast.NodeIndex, fallback ast.NodeIndex, format string, a ...any) {
	if len(nodes) == 0 {
		c.errorAt(fallback, format, a...)
		return
	}
	start := c.tree.SpanOf(nodes[0]).Start
	end := c.tree.SpanOf(nodes[len(nodes)-1]).End
	c.diags.ErrorfSpan(start, end, format, a...)
}

// checkPackage mirrors resolvePackage's shape one level up (struct field
// types across every file first, so a dynamic-array-field or bad-size
// diagnostic in a struct nobody ever instantiates still surfaces regardless
// of which file declares it, then every file's top-level var types and
// function bodies):
func (c *checker) checkPackage(trees []*ast.Tree) {
	for _, tree := range trees {
		c.enter(tree)
		for decl := range tree.TopLevelDeclsOfKind(enums.NodeKinds.StructDecl) {
			c.checkStructDecl(decl)
		}
	}
	for _, tree := range trees {
		c.enter(tree)
		for _, decl := range tree.Children(tree.Root) {
			switch tree.Nodes[decl].Kind {
			case enums.NodeKinds.VarDecl:
				c.declType(decl)
			case enums.NodeKinds.FuncDecl:
				c.checkFuncDecl(decl)
			case enums.NodeKinds.ExternFuncDecl:
				c.checkExternFuncDecl(decl)
			}
		}
	}
}

func (c *checker) checkStructDecl(decl ast.NodeIndex) {
	for _, field := range c.tree.StructFields(decl) {
		c.typeFromNode(c.tree.Child(field, 1))
	}
	for ctor := range c.tree.StructConstructors(decl) {
		c.checkConstructorDecl(ctor)
	}
	for dtor := range c.tree.StructDestructors(decl) {
		c.checkDestructorDecl(dtor)
	}
}

// checkConstructorDecl type-checks one constructor's params and body -
// mirroring checkFuncDecl's own shape, but simpler: a constructor has no
// name, no receiver clause, and (see LANGUAGE.md's "Constructors" section)
// no declared return type at all - it "returns" the struct being
// constructed implicitly, by populating `this` - so it's checked exactly
// like an ordinary void function/method body: c.curFunc.hasReturn stays
// false the whole time, so a bare `return` is fine (an early exit) but
// `return expr` is rejected by checkReturnStmt's existing rule, and there's
// no "missing return" check to run at all (that only ever applies when
// hasReturn is true).
func (c *checker) checkConstructorDecl(ctor ast.NodeIndex) {
	paramList := c.tree.ConstructorParamList(ctor)
	body := c.tree.ConstructorBody(ctor)

	for _, param := range c.tree.Children(paramList) {
		c.declType(param)
	}

	prevFunc := c.curFunc
	c.curFunc = &enclosingFunc{hasReturn: false}
	c.checkBlock(body)
	c.curFunc = prevFunc
}

// constructorSigForDecl returns ctor's (a ConstructorDecl's) signature -
// its declared parameter types, plus the struct type it constructs as its
// "return" type (never actually returned via a return statement - see
// checkConstructorDecl - but the shape checkConstructorCall's argument
// type-checking needs is identical to an ordinary call's) - computed and
// cached on first use, same as funcSigForDecl. Reuses c.funcSigs: a
// ConstructorDecl node's own NodeIndex never collides with a FuncDecl's (see
// nodeRef), so one memoization map safely serves both.
func (c *checker) constructorSigForDecl(ctor ast.NodeIndex) funcSignature {
	key := nodeRef{c.tree, ctor}
	if sig, ok := c.funcSigs[key]; ok {
		return sig
	}
	sig := c.computeConstructorSig(ctor)
	c.funcSigs[key] = sig
	return sig
}

func (c *checker) computeConstructorSig(ctor ast.NodeIndex) funcSignature {
	paramNodes := c.tree.Children(c.tree.ConstructorParamList(ctor))
	params := make([]Type, len(paramNodes))
	for i, param := range paramNodes {
		params[i] = c.declType(param)
	}
	structInfo := c.info.Refs[ctor].StructInfo
	return funcSignature{
		Params: params,
		Return: Type{Kind: TypeStruct, Struct: structInfo},
	}
}

// checkDestructorDecl type-checks one `destructor() {...}` block - mirroring
// checkConstructorDecl almost exactly (no declared return type at all, so
// it's checked like an ordinary void method body: a bare `return` is an
// early exit, `return expr` is rejected by checkReturnStmt's existing rule,
// and there's no "missing return" check to run). The one thing genuinely
// specific to a destructor: its own paramList must actually be empty (see
// LANGUAGE.md's "Destructors" section - the grammar rule, parseDestructorDecl,
// accepts the same general `(...)` shape a constructor's own paramList would,
// so this is what actually enforces "always zero parameters").
func (c *checker) checkDestructorDecl(dtor ast.NodeIndex) {
	paramList := c.tree.DestructorParamList(dtor)
	body := c.tree.DestructorBody(dtor)

	paramNodes := c.tree.Children(paramList)
	for _, param := range paramNodes {
		c.declType(param)
	}
	if len(paramNodes) > 0 {
		c.errorAt(paramList, "destructor must take no parameters, got %d", len(paramNodes))
	}

	prevFunc := c.curFunc
	c.curFunc = &enclosingFunc{hasReturn: false}
	c.checkBlock(body)
	c.curFunc = prevFunc
}

func (c *checker) checkFuncDecl(decl ast.NodeIndex) {
	sig := c.funcSigForDecl(decl) // also checks params/return type exactly once
	c.checkMainReturnType(decl, sig)
	body := c.tree.FuncBody(decl)

	prevFunc := c.curFunc
	c.curFunc = &enclosingFunc{
		hasReturn: c.tree.FuncReturnType(decl) != ast.InvalidNode,
		ret:       sig.Return,
	}
	c.checkBlock(body)
	if c.curFunc.hasReturn && !isTerminatingStmt(c.tree, body) {
		c.errorAt(decl, "missing return")
	}
	c.curFunc = prevFunc
}

// checkMainReturnType enforces LANGUAGE.md's rule for the function literally
// named `main` (no receiver): it may declare no return type at all, or
// exactly `int` - any other declared return type is a compile error. This
// used to live in codegen (declareFuncSignature, src/codegen/func.go) as a
// g.errorAt call, which was real type-checking logic living at the wrong
// layer (see AGENTS.md's Architecture section) - codegen now trusts main's
// return type is already valid by the time it lowers it, the same way it
// trusts every other already-checked construct.
func (c *checker) checkMainReturnType(decl ast.NodeIndex, sig funcSignature) {
	if c.tree.FuncReceiver(decl) != ast.InvalidNode {
		return
	}
	if c.tree.Text(c.tree.FuncName(decl)) != "main" {
		return
	}
	if sig.Return.IsInvalid() || sig.Return.Kind == TypeVoid || sig.Return.Kind == TypeInt {
		return
	}
	c.errorAt(c.tree.FuncReturnType(decl), "main must return either nothing or int, got %s", sig.Return)
}

// funcSigForDecl returns decl's signature, computing and caching it on first
// use - decl is either a FuncDecl (computeFuncSig) or an ExternFuncDecl
// (computeExternFuncSig), dispatched by node kind right here rather than
// having every call site (funcSigForCall's Ident case, typeOfSymbolValue's
// SymFunc case) tell the two apart itself: both node kinds declare a SymFunc
// symbol (see resolve.go's declareFunc/declareExternFunc), so a caller
// dereferencing sym.Decl has no way to know in advance which shape it's
// about to read - this is the one place that distinction actually matters,
// since the two node kinds have genuinely different child layouts
// (FuncDecl's [receiver, name, paramList, returnType, body] vs
// ExternFuncDecl's own [name, paramList, returnType] - see ast.Node's doc
// comment for both).
func (c *checker) funcSigForDecl(decl ast.NodeIndex) funcSignature {
	key := nodeRef{c.tree, decl}
	if sig, ok := c.funcSigs[key]; ok {
		return sig
	}
	var sig funcSignature
	if c.tree.Nodes[decl].Kind == enums.NodeKinds.ExternFuncDecl {
		sig = c.computeExternFuncSig(decl)
	} else {
		sig = c.computeFuncSig(decl)
	}
	c.funcSigs[key] = sig
	return sig
}

func (c *checker) computeFuncSig(decl ast.NodeIndex) funcSignature {
	paramList := c.tree.FuncParamList(decl)
	returnTypeNode := c.tree.FuncReturnType(decl)

	paramNodes := c.tree.Children(paramList)
	params := make([]Type, len(paramNodes))
	for i, param := range paramNodes {
		params[i] = c.declType(param)
	}

	ret := voidType
	if returnTypeNode != ast.InvalidNode {
		ret = c.typeFromNode(returnTypeNode)
	}
	return funcSignature{
		Params: params,
		Return: ret,
	}
}

// checkExternFuncDecl type-checks decl's (an ExternFuncDecl's) own signature
// exactly once (see LANGUAGE.md's "External functions (FFI)" section) - an
// extern func has no body to check (see ast.Node's own ExternFuncDecl doc
// comment: there is nothing else to check about one beyond its params/return
// type), so this just forces funcSigForDecl's own memoized computation
// (computeExternFuncSig) to run eagerly from checkPackage's top-level pass,
// mirroring checkFuncDecl's identical funcSigForDecl call one node-kind over -
// without this, a declared-but-never-called extern func (very plausibly one
// half of a matched pair, like this feature's own QueryPerformanceCounter/
// QueryPerformanceFrequency worked example, where only one of the two might
// ever actually be called from a given program) would only get its own
// type-restriction diagnostics the first time some call site happened to
// reference it - or never, if no call site ever does.
func (c *checker) checkExternFuncDecl(decl ast.NodeIndex) {
	c.funcSigForDecl(decl)
}

// computeExternFuncSig builds decl's (an ExternFuncDecl's) signature from its
// own [name, paramList, returnType] shape (see ast.Node's doc comment) - the
// ExternFuncDecl counterpart to computeFuncSig, reusing the same declType/
// typeFromNode helpers (a Param node's shape is identical regardless of
// whether its parent is a FuncDecl or an ExternFuncDecl, so declType(param)
// needs no changes of its own to serve both). The one thing genuinely new
// here: every parameter type and the return type must be a type this round's
// FFI mechanism can actually pass across a real C ABI boundary (see
// checkExternType) - a restriction an ordinary FuncDecl's own signature never
// needed, since an ordinary call/return never has to cross out of this
// compiler's own representation into a foreign calling convention.
func (c *checker) computeExternFuncSig(decl ast.NodeIndex) funcSignature {
	paramList := c.tree.ExternFuncParamList(decl)
	returnTypeNode := c.tree.ExternFuncReturnType(decl)

	paramNodes := c.tree.Children(paramList)
	params := make([]Type, len(paramNodes))
	for i, param := range paramNodes {
		t := c.declType(param)
		params[i] = t
		c.checkExternType(param, t, "parameter")
	}

	ret := voidType
	if returnTypeNode != ast.InvalidNode {
		ret = c.typeFromNode(returnTypeNode)
		c.checkExternType(returnTypeNode, ret, "return")
	}
	return funcSignature{
		Params: params,
		Return: ret,
	}
}

// checkExternType reports a diagnostic at n when t isn't one of the types
// this round's FFI mechanism allows crossing an extern func's signature (see
// isValidExternType) - what names the position for the diagnostic's own
// wording ("parameter" or "return"). A TypeInvalid t is skipped silently -
// already reported by whatever produced it (an undefined type name, e.g.),
// so this would only add a redundant follow-on error, not a new root cause.
func (c *checker) checkExternType(n ast.NodeIndex, t Type, what string) {
	if t.IsInvalid() {
		return
	}
	if !isValidExternType(t) {
		c.errorAt(n, "extern func %s type %s is not supported - only numeric types, bool, and pointer types can cross an extern function signature", what, t)
	}
}

// isValidExternType reports whether t is a type this round's FFI mechanism
// can pass across a real C ABI boundary (see LANGUAGE.md's "External
// functions (FFI)" section): a numeric type of any width (i8/i16/i32/i64/f32/
// f64), bool, or a pointer type. A pointer is valid unconditionally, whatever
// its own pointee type is (even one otherwise disallowed here, like *string
// or a pointer to a struct) - a pointer is always just a raw address at the
// ABI level regardless of what it points to, so there's nothing to recurse
// into; TypePointer alone already covers `**T` and deeper exactly the same
// way, since each nesting level is still just TypePointer at its own Kind.
// Explicitly excluded: string (a {ptr,i32} fat struct, not a real C ABI
// shape), a struct type by value, a dynamic array (`[]T`, also a fat
// struct), and a function type (a fat closure pointer) - none of these have
// a well-defined "just pass this to a real C function" representation in
// this compiler yet, and solving that is explicitly out of scope this round.
func isValidExternType(t Type) bool {
	switch t.Kind {
	case TypeI8, TypeI16, TypeI32, TypeI64, TypeF32, TypeF64, TypeBool, TypePointer:
		return true
	default:
		return false
	}
}

// checkFuncLit type-checks a function-literal expression (`func(params)
// [returnType] { body }` - see LANGUAGE.md's "Lambdas" section) and returns
// its Type - a TypeFunc built from its own signature, exactly like a bare
// free-function reference's own Type (checkIdentExpr/typeOfSymbolValue's
// SymFunc case) - a lambda's exposed type is indistinguishable from an
// ordinary function value's at the type level; only codegen's own
// representation differs (see CODEGEN.md), never anything sema exposes.
//
// Unlike checkFuncDecl (funcSigForDecl memoizes a FuncDecl's signature by its
// own node, since the same declaration can be referenced from many call
// sites), a literal's signature is computed once, directly - it's evaluated
// at exactly the one expression position it appears in, never looked up by
// some other node pointing back at it, so there's nothing to memoize.
// Reuses c.declType(param) for each param exactly like computeFuncSig does -
// declType has no FuncDecl-specific assumption baked in, it just reads a
// Param node's own type-annotation child, which a FuncLit's ParamList
// supplies in the identical shape a FuncDecl's does.
func (c *checker) checkFuncLit(n ast.NodeIndex) Type {
	paramListNode := c.tree.FuncLitParamList(n)
	returnTypeNode := c.tree.FuncLitReturnType(n)
	body := c.tree.FuncLitBody(n)

	paramNodes := c.tree.Children(paramListNode)
	params := make([]Type, len(paramNodes))
	for i, param := range paramNodes {
		params[i] = c.declType(param)
	}

	ret := voidType
	if returnTypeNode != ast.InvalidNode {
		ret = c.typeFromNode(returnTypeNode)
	}

	prevFunc := c.curFunc
	c.curFunc = &enclosingFunc{
		hasReturn: returnTypeNode != ast.InvalidNode,
		ret:       ret,
	}
	c.checkBlock(body)
	if c.curFunc.hasReturn && !isTerminatingStmt(c.tree, body) {
		c.errorAt(n, "missing return")
	}
	c.curFunc = prevFunc

	return funcType(funcSignature{Params: params, Return: ret})
}

// declType returns the type of a VarDecl, ShortVarDecl, or Param
// declaration node, computing and memoizing it on first use so a later
// reference (an Ident resolving to this decl) never redoes the work or
// re-reports a diagnostic already raised while computing it.
func (c *checker) declType(decl ast.NodeIndex) Type {
	key := nodeRef{c.tree, decl}
	if t, ok := c.declTypes[key]; ok {
		return t
	}
	if c.computingDecl[key] {
		c.errorAt(decl, "type-checking cycle while inferring this declaration's type")
		return invalidType
	}
	c.computingDecl[key] = true
	t := c.computeDeclType(decl)
	delete(c.computingDecl, key)
	c.declTypes[key] = t
	// A VarDecl/ShortVarDecl/Param declaration node is itself a type-position
	// node as far as codegen is concerned - codegen.varDeclType used to
	// re-derive exactly this value on its own (see AGENTS.md's codegen
	// section for why that duplicate logic is gone now); storing it here
	// means every codegen call site can just read g.info.Types[declNode]
	// directly, same as any other type-position node.
	c.info.Types[decl] = t
	return t
}

func (c *checker) computeDeclType(decl ast.NodeIndex) Type {
	switch c.tree.Nodes[decl].Kind {
	case enums.NodeKinds.VarDecl:
		return c.checkVarDeclNode(decl)
	case enums.NodeKinds.ShortVarDecl:
		return c.checkShortVarDeclNode(decl)
	case enums.NodeKinds.MultiShortVarDecl:
		return c.checkMultiShortVarDeclNode(decl)
	case enums.NodeKinds.Param:
		return c.typeFromNode(c.tree.Child(decl, 1))
	default:
		return invalidType
	}
}

// checkVarDeclNode checks `var name Type`, `var name = expr`, or
// `var name Type = expr` and returns the variable's type. At least one of
// the type annotation or the initializer must be present - legal at the
// grammar level (see ast.Node's VarDecl doc comment), so it's this pass's
// job to reject the case where both are missing.
func (c *checker) checkVarDeclNode(decl ast.NodeIndex) Type {
	typeNode := c.tree.Child(decl, 1)
	initNode := c.tree.Child(decl, 2)

	if typeNode == ast.InvalidNode && initNode == ast.InvalidNode {
		c.errorAt(decl, "variable declaration needs a type, an initializer, or both")
		return invalidType
	}
	if typeNode == ast.InvalidNode {
		// No declared type: an untyped constant initializer (a bare numeric
		// literal, or an expression built entirely from them) takes Go's own
		// untyped-constant default instead of staying untyped forever - see
		// AGENTS.md's Types section.
		return c.defaultIfUntyped(initNode, c.checkValueExpr(initNode))
	}

	declared := c.typeFromNode(typeNode)
	if initNode == ast.InvalidNode {
		return declared
	}

	initType := c.checkValueExpr(initNode)
	if c.checkAssignable(initNode, declared, initType, "variable declaration") {
		c.checkNoIllegalCopy(initNode, declared, true, "variable declaration")
	}
	return declared
}

func (c *checker) checkShortVarDeclNode(decl ast.NodeIndex) Type {
	// `:=` never has a declared type (see the grammar) - same untyped
	// defaulting rule as a type-less `var`, above.
	initNode := c.tree.Child(decl, 1)
	t := c.defaultIfUntyped(initNode, c.checkValueExpr(initNode))
	c.checkNoIllegalCopy(initNode, t, true, "short variable declaration")
	return t
}

// checkMultiShortVarDeclNode type-checks `a, b := f(...)` (see LANGUAGE.md's
// "Go-style multi-return values" section) and returns the destructured
// TypeMultiReturn Type as a whole (mirroring checkShortVarDeclNode's own
// single-Type result one level up) - each individual name's own component
// Type is eagerly cached directly against that name's own Ident node here,
// not left to be computed lazily on first reference the way an ordinary
// declType entry normally would be: unlike a plain ShortVarDecl (whose sole
// declared name IS the decl node checkStmt already forces through declType,
// guaranteeing info.Types gets populated regardless of whether it's ever
// referenced again), a name declared here has no such guarantee - if it's
// never referenced again, declType(nameNode) would otherwise never run at
// all, leaving codegen's own g.info.Types[nameNode] lookup empty. Seeding
// both declType's memoization cache and info.Types directly, right here,
// closes that gap unconditionally.
func (c *checker) checkMultiShortVarDeclNode(decl ast.NodeIndex) Type {
	names := c.tree.MultiShortVarDeclNames(decl)
	value := c.tree.MultiShortVarDeclValue(decl)
	types := c.checkDestructureSource(value, len(names), "short variable declaration")

	for i, nameNode := range names {
		t := types[i]
		c.declTypes[nodeRef{c.tree, nameNode}] = t
		c.info.Types[nameNode] = t
		c.checkNoIllegalCopy(value, t, true, "short variable declaration")
	}
	return Type{
		Kind:   TypeMultiReturn,
		Params: types,
	}
}

// checkMultiAssignStmt type-checks `a, b = f(...)` - the assignment-form
// counterpart to checkMultiShortVarDeclNode, checking every target (already
// existing lvalues - Ident/MemberExpr/IndexExpr/`*p`, exactly like
// AssignStmt's own single target - see checkLValue) against its own matching
// component type from the destructured call.
func (c *checker) checkMultiAssignStmt(n ast.NodeIndex) {
	targets := c.tree.MultiAssignStmtTargets(n)
	value := c.tree.MultiAssignStmtValue(n)

	targetTypes := make([]Type, len(targets))
	allOk := true
	for i, target := range targets {
		t, ok := c.checkLValue(target)
		targetTypes[i] = t
		allOk = allOk && ok
	}

	types := c.checkDestructureSource(value, len(targets), "multi-value assignment")
	if !allOk {
		return
	}
	for i, target := range targets {
		if c.checkAssignable(target, targetTypes[i], types[i], fmt.Sprintf("assignment target %d", i+1)) {
			c.checkNoIllegalCopy(target, targetTypes[i], true, fmt.Sprintf("assignment target %d", i+1))
		}
	}
}

// checkDestructureSource type-checks value - a multi-target destructuring
// statement's (MultiShortVarDecl/MultiAssignStmt) sole right-hand side - and
// returns the wantCount component types to match against each name/target in
// order. Per this feature's own deliberate restriction (see LANGUAGE.md's
// "Go-style multi-return values" section: "destructuring only", no first-
// class tuple type, no argument-spreading, no Go-style parallel
// `a, b := 1, 2`), value must be exactly one call expression whose own
// signature returns exactly wantCount values - never any other expression
// shape, and never a mismatched count. Every rejection path still returns a
// same-length, invalidType-filled slice (rather than nil or a short one) so
// every caller can always safely index it once by position, the same
// "already reported once, don't cascade" recovery invalidType itself always
// provides elsewhere in this pass.
func (c *checker) checkDestructureSource(value ast.NodeIndex, wantCount int, context string) []Type {
	invalid := make([]Type, wantCount)
	for i := range invalid {
		invalid[i] = invalidType
	}

	if c.tree.Nodes[value].Kind != enums.NodeKinds.CallExpr {
		c.errorAt(value, "right-hand side of a %s must be exactly one function call", context)
		c.checkValueExpr(value)
		return invalid
	}

	// checkExpr, not checkValueExpr - a TypeMultiReturn result is expected
	// and legal in this one position; checkValueExpr's own rejection of it
	// is exactly what every *other* position still needs.
	t := c.checkExpr(value)
	if t.IsInvalid() {
		return invalid
	}
	if t.Kind != TypeMultiReturn {
		c.errorAt(value, "cannot destructure a single-value call result (%s) into %d targets", t, wantCount)
		return invalid
	}
	if len(t.Params) != wantCount {
		c.errorAt(value, "wrong number of values in %s: call returns %d, got %d target(s)", context, len(t.Params), wantCount)
		return invalid
	}
	return t.Params
}

// defaultIfUntyped applies Go's own untyped-constant defaulting rule (see
// AGENTS.md's Types section) whenever exprNode's checked type t is still
// untyped by the time it reaches a position with no explicit target type to
// resolve against: untyped int defaults to i32, untyped float to f64.
// Retypes exprNode's whole subtree (retypeUntyped) to match, since nothing
// else will ever revisit it. A non-untyped t passes through unchanged.
func (c *checker) defaultIfUntyped(exprNode ast.NodeIndex, t Type) Type {
	if t.Kind == TypeUntypedNil {
		// Unlike an untyped numeric constant, nil (see LANGUAGE.md's
		// "Pointers" section) has no default type to fall back to - there's
		// no general zero value in this language, only a pointer-typed one -
		// so a context that never pins down a concrete *T (a type-less
		// `:= nil`, or `print(nil)`) is a real error, not a silent default.
		c.errorAt(exprNode, "cannot use nil without a pointer type context")
		return invalidType
	}
	if !t.IsUntyped() {
		return t
	}
	def := c.defaultUntyped(t)
	c.retypeUntyped(exprNode, def)
	return def
}

// defaultUntyped maps an untyped Type to its Go-style default: untyped int
// -> i32, untyped float -> f64.
func (c *checker) defaultUntyped(t Type) Type {
	if t.Kind == TypeUntypedFloat {
		return f64Type
	}
	return i32Type
}

// retypeUntyped recursively overwrites info.Types for n and any of its
// still-untyped descendants (a NumberLit, or a ParenExpr/UnaryExpr("-")/
// BinaryExpr built entirely from untyped operands) with the now-resolved
// concrete target - the other half of Go's own untyped-constant model this
// language adapts (see AGENTS.md's Types section): a numeric literal's Type
// starts life untyped (checkNumberLit) and stays that way, possibly nested
// several levels deep in an arithmetic expression, until some context
// (checkAssignable, resolveNumericOperands, defaultIfUntyped) finally pins a
// concrete type - at which point every untyped node in that subtree needs to
// actually become that type, not just the outermost one, since codegen reads
// each node's own info.Types entry independently (a NumberLit needs to know
// its own concrete width/kind to lower a correctly-typed LLVM constant).
//
// Only descends into node kinds that can actually carry an untyped Type in
// the first place (see checkNumberLit/checkUnaryExpr/checkBinaryExpr) - a
// BinaryExpr only recurses into whichever operand was itself untyped,
// leaving an already-concrete operand (which resolveNumericOperands would
// have already retyped, or which was never untyped to begin with) alone.
func (c *checker) retypeUntyped(n ast.NodeIndex, target Type) {
	switch c.tree.Nodes[n].Kind {
	case enums.NodeKinds.ParenExpr, enums.NodeKinds.UnaryExpr:
		c.retypeUntyped(c.tree.Child(n, 0), target)
	case enums.NodeKinds.BinaryExpr:
		l := c.tree.Child(n, 0)
		r := c.tree.Child(n, 1)
		if c.info.Types[l].IsUntyped() {
			c.retypeUntyped(l, target)
		}
		if c.info.Types[r].IsUntyped() {
			c.retypeUntyped(r, target)
		}
	}
	c.info.Types[n] = target
}

// checkAssignable reports an error if a value of type got cannot be used
// where a value of type want is required, unless either side is
// TypeInvalid - already reported once at its own source, so raising a
// second diagnostic here would just be noise about the same root cause.
// context names the position for the message, e.g. "assignment",
// "argument 2", "field x".
//
// An untyped got (see AGENTS.md's Types section) adapts to want instead of
// requiring an exact Kind match, provided want is itself numeric and the
// adaptation wouldn't silently lose information: untyped-int -> any int
// width or float width is fine, but untyped-float -> an integer want is
// rejected (same as Go rejects `var a int = 5.5`) - at is retyped in place
// (retypeUntyped) to want on success, since nothing else will revisit it.
func (c *checker) checkAssignable(at ast.NodeIndex, want, got Type, context string) bool {
	if want.IsInvalid() || got.IsInvalid() {
		return true
	}
	if got.Kind == TypeUntypedNil {
		// nil (see LANGUAGE.md's "Pointers" section) adapts to any pointer
		// context, exactly like an untyped numeric constant adapts to any
		// numeric one - deliberately narrower, since this language's nil
		// isn't a general zero value: anything else (want not itself a
		// pointer) is rejected outright, not "would lose information" the
		// way an untyped-float-into-int mismatch is worded below.
		if want.Kind != TypePointer {
			c.errorAt(at, "cannot use nil as %s in %s", want, context)
			return false
		}
		c.retypeUntyped(at, want)
		return true
	}
	if got.IsUntyped() {
		if !want.IsNumeric() {
			c.errorAt(at, "cannot use %s as %s in %s", got, want, context)
			return false
		}
		if got.Kind == TypeUntypedFloat && want.IsIntegerKind() {
			c.errorAt(at, "cannot use %s as %s in %s (would truncate)", got, want, context)
			return false
		}
		c.retypeUntyped(at, want)
		return true
	}
	if !want.Equal(got) {
		c.errorAt(at, "cannot use %s as %s in %s", got, want, context)
		return false
	}
	return true
}

// structCopyable computes (and memoizes onto info.Copyable) whether info's
// struct type may be freely copied - see StructInfo.Copyable's own doc
// comment for the exact rule. Lazy and memoized, exactly like declType: a
// struct's own field types may name another struct declared later in the
// package (any file, this one included, or - since a struct's fields are
// checked regardless of export - another package entirely once this
// dependency is itself exported), so this can't simply be computed once,
// eagerly, in source declaration order the way checkStructDecl's own
// per-file loop visits structs.
//
// computingCopyable guards against a genuinely cyclic struct definition (a
// struct that, through some chain of by-value fields, contains itself) -
// not a case this pass otherwise detects or needs to solve; treating it as
// copyable simply breaks the recursion without a false diagnostic here, on
// the assumption a cyclic-by-value struct is either impossible to construct
// in the first place or will surface as a different, unrelated problem
// elsewhere.
func (c *checker) structCopyable(info *StructInfo) bool {
	if info.copyableComputed {
		return info.Copyable
	}
	if c.computingCopyable[info] {
		return true
	}
	c.computingCopyable[info] = true
	defer delete(c.computingCopyable, info)

	copyable := info.Destructor == nil
	if copyable {
		restore := c.pushTree(info.Symbol.Tree)
		for _, field := range c.tree.StructFields(info.Symbol.Decl) {
			fieldType := c.typeFromNode(c.tree.Child(field, 1))
			if c.typeIsNonCopyable(fieldType) {
				copyable = false
				break
			}
		}
		restore()
	}

	info.Copyable = copyable
	info.copyableComputed = true
	return copyable
}

// typeIsNonCopyable reports whether a value of type t can never be freely
// duplicated (see LANGUAGE.md's "Destructors" section) - a struct that isn't
// StructInfo.Copyable, or a fixed-size array of a non-copyable element type
// (propagating the exact same "one field is enough to taint the whole
// aggregate" rule StructInfo.Copyable itself already applies one level up).
// A dynamic array element's own copyability is deliberately not consulted
// here at all - see arrayTypeFromNode's own dedicated diagnostic instead,
// which rejects a non-copyable dynamic-array element type outright rather
// than needing this helper to reason about slice growth/aliasing.
func (c *checker) typeIsNonCopyable(t Type) bool {
	switch t.Kind {
	case TypeStruct:
		if t.Struct == nil {
			return false
		}
		return !c.structCopyable(t.Struct)
	case TypeArray:
		if t.Dynamic || t.Elem == nil {
			return false
		}
		return c.typeIsNonCopyable(*t.Elem)
	default:
		return false
	}
}

// isFreshConstruction reports whether n is an expression that builds a
// brand-new value in place - a composite literal or a constructor call -
// rather than referencing an already-existing one. This is exactly what
// LANGUAGE.md's "Destructors" section calls out as the one thing that's
// never "a copy" even for a non-copyable type: `f := FileHandle(...)` (or
// `f := FileHandle{...}`) constructs the one instance `f` now owns, while
// `g := f` would duplicate an existing live value - checkNoIllegalCopy is
// what actually tells those two apart at each of this rule's call sites.
// Unwraps any enclosing ParenExpr first, same as every other "what shape is
// this expression, structurally" check in this pass (e.g. checkLValue's
// UnaryExpr("*") case).
func (c *checker) isFreshConstruction(n ast.NodeIndex) bool {
	for c.tree.Nodes[n].Kind == enums.NodeKinds.ParenExpr {
		n = c.tree.Child(n, 0)
	}
	switch c.tree.Nodes[n].Kind {
	case enums.NodeKinds.CompositeLit:
		return true
	case enums.NodeKinds.CallExpr:
		callee := c.tree.Child(n, 0)
		sym, ok := c.info.Refs[callee]
		return ok && sym.Kind == SymConstructor
	default:
		return false
	}
}

// checkNoIllegalCopy enforces LANGUAGE.md's "Destructors" non-copyable rule
// at one of its four call sites (a var/short-var-decl initializer or a plain
// assignment's value, a struct/array composite-literal element, a function
// argument, or a return statement's value) - at is the source expression,
// want the context's already-matched target type (only meaningful once
// checkAssignable has already confirmed at's own type actually matches want;
// callers only call this after that succeeds, so a plain type mismatch
// never also raises a second, unrelated "illegal copy" diagnostic about the
// same expression).
//
// allowFresh distinguishes the two shapes this rule takes:
//   - a var decl, assignment, composite-literal element, or function
//     argument all allow the one deliberate exception - constructing a
//     fresh value in place (isFreshConstruction) is never "a copy". A fresh
//     argument is just as sound as a fresh var-decl initializer: the
//     callee's own parameter (an ordinary local, as far as this feature's
//     firing rule is concerned - see pushDestructorEntry, codegen/func.go)
//     becomes that value's one and only owner, destructing it at its own
//     scope exit, with no other reference to it anywhere else.
//   - a return statement's value allows no exception at all, fresh
//     construction included (see LANGUAGE.md: "this is the one that forces
//     resource-owning types only exist behind a pointer"). This is
//     genuinely different from argument-passing, not an arbitrary
//     asymmetry: soundly allowing a fresh *return* would require knowing,
//     at every call site consuming the result, that "whatever this call
//     returns is always freshly owned" - a real (if narrow) escape-analysis
//     question this round deliberately doesn't take on (see DECISIONS.md) -
//     whereas a fresh *argument*'s soundness is entirely local to the one
//     call expression itself, nothing further to infer.
func (c *checker) checkNoIllegalCopy(at ast.NodeIndex, want Type, allowFresh bool, context string) bool {
	if !c.typeIsNonCopyable(want) {
		return true
	}
	if allowFresh && c.isFreshConstruction(at) {
		return true
	}
	c.errorAt(at, "cannot copy %s in %s: it (or a field of it) has a destructor, so it cannot be copied - only constructed fresh or referenced through a pointer", want, context)
	return false
}

// typeFromNode converts a resolved type-position node (an Ident naming a
// builtin or struct type, or an ArrayType) into a Type, memoized per node.
// It relies entirely on Resolve having already populated info.Refs for
// Ident nodes in type position - this pass never does its own lexical
// lookup.
//
// Every result is also stored into info.Types[n] - not just the local cache -
// so every type-position node in the language (a VarDecl's type annotation, a
// Param's type, a Field's type, a FuncDecl's return type, an ArrayType node
// and its element - every caller of typeFromNode, directly or via
// arrayTypeFromNode's own recursion) ends up covered by Info.Types, not just
// value-expression nodes. This is exactly the gap codegen's own now-deleted
// resolveTypeNode/varDeclType duplicate logic used to work around - see
// AGENTS.md's codegen section.
func (c *checker) typeFromNode(n ast.NodeIndex) Type {
	if n == ast.InvalidNode {
		return invalidType
	}
	key := nodeRef{c.tree, n}
	if t, ok := c.typeNodeCache[key]; ok {
		return t
	}
	t := c.computeTypeFromNode(n)
	c.typeNodeCache[key] = t
	c.info.Types[n] = t
	return t
}

func (c *checker) computeTypeFromNode(n ast.NodeIndex) Type {
	switch c.tree.Nodes[n].Kind {
	case enums.NodeKinds.Ident, enums.NodeKinds.MemberExpr:
		// A MemberExpr only ever reaches type-position here for a package-
		// qualified type name (`pkg.Point`) - already fully resolved by
		// Resolve (resolveTypeMemberExpr), export-checked included, so this
		// needs no package-awareness of its own: whatever Info.Refs[n]
		// already points to is treated exactly like a same-package Ident.
		sym, ok := c.info.Refs[n]
		if !ok {
			return invalidType // undefined name; already reported by Resolve
		}
		return c.typeFromSymbol(sym)
	case enums.NodeKinds.ArrayType:
		return c.arrayTypeFromNode(n)
	case enums.NodeKinds.PointerType:
		return c.pointerTypeFromNode(n)
	case enums.NodeKinds.FuncType:
		return c.funcTypeFromNode(n)
	case enums.NodeKinds.MultiReturnType:
		return c.multiReturnTypeFromNode(n)
	default:
		return invalidType
	}
}

// multiReturnTypeFromNode converts a MultiReturnType type-position node
// (a FuncDecl's own `(T1, T2, ...)` return-type list - see LANGUAGE.md's
// "Go-style multi-return values" section and ast.Node's own MultiReturnType
// doc comment) into a Type - the multi-return counterpart to
// funcTypeFromNode/arrayTypeFromNode. The grammar accepts any count
// (parser.parseFuncDeclReturnType has no arity opinion of its own); this is
// where the feature's own narrower "2 or more" rule is actually enforced -
// the same "grammar accepts the general shape, sema enforces the feature's
// own rule" division of labor a duplicate-arity constructor or a non-empty
// destructor param list already use.
func (c *checker) multiReturnTypeFromNode(n ast.NodeIndex) Type {
	typeNodes := c.tree.Children(n)
	if len(typeNodes) < 2 {
		c.errorAt(n, "a multi-return type must declare at least 2 types, got %d", len(typeNodes))
	}
	types := make([]Type, len(typeNodes))
	for i, tn := range typeNodes {
		types[i] = c.typeFromNode(tn)
	}
	return Type{
		Kind:   TypeMultiReturn,
		Params: types,
	}
}

// pointerTypeFromNode converts a PointerType type-position node (`*T` - see
// LANGUAGE.md's "Pointers" section) into a Type - the pointer counterpart to
// arrayTypeFromNode, minus the size handling a pointer type has no use for.
func (c *checker) pointerTypeFromNode(n ast.NodeIndex) Type {
	elem := c.typeFromNode(c.tree.Child(n, 0))
	return Type{
		Kind: TypePointer,
		Elem: &elem,
	}
}

// typeFromSymbol converts a resolved type-position Symbol (SymBuiltinType or
// SymStruct - anything else means "not a type", already reported by
// Resolve) into a Type - shared by computeTypeFromNode's Ident and
// MemberExpr (package-qualified) cases, since both ultimately just name a
// builtin or struct type symbol. Uses sym.Name (not the node's own text) so
// this works identically regardless of which node kind resolved to sym.
func (c *checker) typeFromSymbol(sym *Symbol) Type {
	switch sym.Kind {
	case SymBuiltinType:
		switch sym.Name {
		case "int", "i32":
			return i32Type
		case "i8":
			return i8Type
		case "i16":
			return i16Type
		case "i64":
			return i64Type
		case "f32":
			return f32Type
		case "f64":
			return f64Type
		case "string":
			return stringType
		case "bool":
			return boolType
		default:
			return invalidType
		}
	case SymStruct:
		if sym.StructInfo == nil {
			return invalidType
		}
		return Type{
			Kind:   TypeStruct,
			Struct: sym.StructInfo,
		}
	default:
		return invalidType // "not a type"; already reported by Resolve
	}
}

// funcTypeFromNode converts a FuncType type-position node (`func(T1, T2) R`,
// or `func(T1, T2)` with an implicit void return - see ast.Node's own
// FuncType doc comment) into a Type - the function-type counterpart to
// arrayTypeFromNode. Each parameter type and the return type (when present)
// are themselves type-position nodes, so this recurses through typeFromNode
// exactly like every other nested type position in this pass, keeping every
// FuncType-reachable node covered by info.Types (see typeFromNode's own doc
// comment).
func (c *checker) funcTypeFromNode(n ast.NodeIndex) Type {
	paramListNode := c.tree.Child(n, 0)
	returnNode := c.tree.Child(n, 1)

	paramNodes := c.tree.Children(paramListNode)
	params := make([]Type, len(paramNodes))
	for i, p := range paramNodes {
		params[i] = c.typeFromNode(p)
	}

	ret := voidType
	if returnNode != ast.InvalidNode {
		ret = c.typeFromNode(returnNode)
	}
	return Type{
		Kind:   TypeFunc,
		Params: params,
		Return: &ret,
	}
}

// arrayTypeFromNode converts an ArrayType node - a fixed-size `[N]T` or a
// dynamic `[]T` (size == InvalidNode) - into a Type. This is the one place
// every array type, wherever it appears (a var's type, a param, a field, a
// composite literal's target type, a return type, make's own first
// argument), passes through - see LANGUAGE.md's "Dynamic arrays" section for
// make/append/len, the real, working feature built on top of this Type
// shape.
func (c *checker) arrayTypeFromNode(n ast.NodeIndex) Type {
	sizeNode := c.tree.Child(n, 0)
	elemNode := c.tree.Child(n, 1)
	elem := c.typeFromNode(elemNode)

	if sizeNode == ast.InvalidNode {
		// A dynamic array whose element type is non-copyable is explicitly
		// out of scope for this round (see LANGUAGE.md's "Destructors"
		// section): `make`/`append`/growth all copy element bytes around
		// (memcpy on reallocation) with no destructor-cascading concept at
		// all, so allowing this here would silently mishandle it rather than
		// give it a real diagnostic. Non-copyable is only ever non-copyable
		// because some struct in the chain declares its own destructor (see
		// StructInfo.Copyable), so this one check already covers both "the
		// element type itself has a destructor" and "the element type embeds
		// one" uniformly.
		if c.typeIsNonCopyable(elem) {
			c.errorAt(elemNode, "dynamic array element type %s is non-copyable (has a destructor); dynamic arrays of a destructor-owning type are not supported", elem)
		}
		return Type{
			Kind:    TypeArray,
			Elem:    &elem,
			Dynamic: true,
		}
	}

	size, ok := c.constArraySize(sizeNode)
	if !ok {
		return invalidType
	}
	return Type{
		Kind: TypeArray,
		Elem: &elem,
		Size: size,
	}
}

// constArraySize requires sizeNode to be a positive integer literal - this
// language has no constant-expression evaluator yet, so `[n]int` for some
// other variable/constant n isn't supported (see BLOCKERS.md).
func (c *checker) constArraySize(sizeNode ast.NodeIndex) (int64, bool) {
	t := c.checkValueExpr(sizeNode)
	if t.IsInvalid() {
		return 0, false
	}
	if c.tree.Nodes[sizeNode].Kind != enums.NodeKinds.NumberLit {
		c.errorAt(sizeNode, "array size must be a constant integer literal")
		return 0, false
	}
	if t.Kind != TypeUntypedInt {
		c.errorAt(sizeNode, "array size must be an integer, not a float literal")
		return 0, false
	}
	// A bare integer NumberLit is always untyped-int at this point (see
	// checkNumberLit) - pin it to i32 (matching every other bare "int"
	// context this language has - see AGENTS.md's Types section) so codegen
	// sees a concrete Type for it like any other expression node.
	c.retypeUntyped(sizeNode, i32Type)
	text := c.tree.Text(sizeNode)
	n, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		c.errorAt(sizeNode, "invalid array size %q", text)
		return 0, false
	}
	if n <= 0 {
		c.errorAt(sizeNode, "array size must be positive, got %d", n)
		return 0, false
	}
	return n, true
}

// checkBlock type-checks every statement of block in order. Unlike
// resolveBlock, this needs no Scope of its own - name resolution already
// happened; every Ident this pass looks at is already keyed into info.Refs.
func (c *checker) checkBlock(block ast.NodeIndex) {
	for _, stmt := range c.tree.Children(block) {
		c.checkStmt(stmt)
	}
}

func (c *checker) checkStmt(n ast.NodeIndex) {
	switch c.tree.Nodes[n].Kind {
	case enums.NodeKinds.VarDecl,
		enums.NodeKinds.ShortVarDecl,
		enums.NodeKinds.MultiShortVarDecl:
		c.declType(n)
	case enums.NodeKinds.AssignStmt:
		c.checkAssignStmt(n)
	case enums.NodeKinds.MultiAssignStmt:
		c.checkMultiAssignStmt(n)
	case enums.NodeKinds.IncDecStmt:
		c.checkIncDecStmt(n)
	case enums.NodeKinds.ExprStmt:
		// A call used as a statement is the one place a void result is
		// fine - nothing is discarding a value that was actually needed. An
		// untyped result (e.g. the admittedly-pointless statement `5`, which
		// this grammar doesn't reject) still needs to default like any other
		// value context nothing else will ever revisit - see defaultIfUntyped.
		expr := c.tree.Child(n, 0)
		c.defaultIfUntyped(expr, c.checkExpr(expr))
	case enums.NodeKinds.ReturnStmt:
		c.checkReturnStmt(n)
	case enums.NodeKinds.BreakStmt:
		c.checkBreakOrContinue(n, "break")
	case enums.NodeKinds.ContinueStmt:
		c.checkBreakOrContinue(n, "continue")
	case enums.NodeKinds.DeleteStmt:
		c.checkDeleteStmt(n)
	case enums.NodeKinds.Block:
		c.checkBlock(n)
	case enums.NodeKinds.IfStmt:
		c.checkIfStmt(n)
	case enums.NodeKinds.ForStmt:
		c.checkForStmt(n)
	}
}

// checkBreakOrContinue verifies n (a BreakStmt or ContinueStmt) actually
// appears somewhere inside a ForStmt's body - previously unvalidated by
// either Resolve or Check (see BLOCKERS.md's codegen-phase entry #6: a bare
// top-level `break` used to pass both passes with zero diagnostics, leaving
// codegen to discover the problem on its own, far later than a semantic
// error belongs). word names the keyword for the diagnostic message
// ("break"/"continue").
func (c *checker) checkBreakOrContinue(n ast.NodeIndex, word string) {
	if c.loopDepth == 0 {
		c.errorAt(n, "%s outside a loop", word)
	}
}

// checkDeleteStmt type-checks `delete p` (see LANGUAGE.md's "Pointers"
// section) - p must have a pointer type; delete itself produces no value.
func (c *checker) checkDeleteStmt(n ast.NodeIndex) {
	expr := c.tree.Child(n, 0)
	t := c.defaultIfUntyped(expr, c.checkValueExpr(expr))
	if t.IsInvalid() {
		return
	}
	if t.Kind != TypePointer {
		c.errorAt(n, "delete requires a pointer, got %s", t)
	}
}

func (c *checker) checkAssignStmt(n ast.NodeIndex) {
	target := c.tree.Child(n, 0)
	value := c.tree.Child(n, 1)

	tt, ok := c.checkLValue(target)
	vt := c.checkValueExpr(value)
	if !ok {
		return
	}

	op := c.tree.Text(n)
	if op == "=" {
		if c.checkAssignable(value, tt, vt, "assignment") {
			c.checkNoIllegalCopy(value, tt, true, "assignment")
		}
		return
	}
	c.checkCompoundOp(value, op, tt, vt)
}

// checkCompoundOp checks a compound-assignment operator (+= -= *= /=)
// against its target/value types - the same rule as the corresponding
// binary operator (see checkBinaryExpr), since `x += y` means exactly
// `x = x + y` type-wise, just without re-deriving a result type: the target's
// own type (always already concrete - it's a declared variable) is the
// result, and value adapts to it exactly like an assignment's right-hand
// side would (checkAssignable) - including an untyped value literal
// (`x += 1`) adapting to whatever numeric type x already is.
func (c *checker) checkCompoundOp(value ast.NodeIndex, op string, tt, vt Type) {
	if tt.IsInvalid() || vt.IsInvalid() {
		return
	}
	if op == "+=" && tt.Kind == TypeString {
		c.checkAssignable(value, stringType, vt, "assignment")
		return
	}
	if !tt.IsNumeric() {
		c.errorAt(value, "operator %s not defined for %s", op, tt)
		return
	}
	c.checkAssignable(value, tt, vt, "assignment")
}

// checkLValue type-checks target as an assignment/inc-dec destination. The
// parser already rejected anything whose *shape* can't be an lvalue
// (checkAssignTarget in parser/stmt.go accepts only Ident, MemberExpr, and
// IndexExpr) - what's left for this type-aware pass is whether the name
// behind that shape is actually assignable: an Ident might name a type, not
// a variable; a MemberExpr might name a method, not a field.
// checkMemberExpr already reports and returns TypeInvalid for its own
// not-a-field case, so IndexExpr/MemberExpr can still just reuse checkExpr's
// dispatch directly. An Ident needs its own explicit guard now that a bare
// function name type-checks successfully as a value (checkIdentExpr's
// SymFunc case, for first-class functions - see LANGUAGE.md): a function has
// nowhere to be assigned *to* (there's no storage location backing it, only
// a fixed declaration), so checkExpr succeeding is no longer sufficient
// proof of assignability the way it still is for every other kind.
func (c *checker) checkLValue(n ast.NodeIndex) (Type, bool) {
	switch c.tree.Nodes[n].Kind {
	case enums.NodeKinds.Ident:
		sym, ok := c.info.Refs[n]
		if ok && sym.Kind != SymVar && sym.Kind != SymParam {
			c.errorAt(n, "cannot assign to %s (%s is not a variable)", c.tree.Text(n), sym.Kind)
			return invalidType, false
		}
		t := c.checkExpr(n)
		return t, !t.IsInvalid()
	case enums.NodeKinds.MemberExpr,
		enums.NodeKinds.IndexExpr:
		t := c.checkExpr(n)
		return t, !t.IsInvalid()
	case enums.NodeKinds.UnaryExpr:
		// `*p = v` (see LANGUAGE.md's "Pointers" section) - a dereference is
		// the one UnaryExpr shape that's ever a valid lvalue; `&x` (the only
		// other prefix operator sharing this node kind) never is - the
		// parser's own checkAssignTarget accepts the shape broadly (same as
		// every other case here), so this is what actually narrows it down,
		// same division of labor as Ident's own sym.Kind check just above.
		if c.tree.Text(n) != "*" {
			c.errorAt(n, "cannot assign to this expression")
			return invalidType, false
		}
		t := c.checkExpr(n)
		return t, !t.IsInvalid()
	case enums.NodeKinds.Bad:
		return invalidType, false
	default:
		c.errorAt(n, "cannot assign to this expression")
		return invalidType, false
	}
}

// checkIncDecStmt types `++`/`--` - any numeric type (every int width, every
// float width - same as Go, which defines IncDecStmt directly in terms of
// `x += 1`/`x -= 1`, and an untyped `1` adapts to any numeric type). The
// target is always an already-declared variable, so t itself is never
// untyped here.
func (c *checker) checkIncDecStmt(n ast.NodeIndex) {
	target := c.tree.Child(n, 0)
	t, ok := c.checkLValue(target)
	if !ok {
		return
	}
	if !t.IsNumeric() {
		c.errorAt(n, "operator %s requires a numeric operand, got %s", c.tree.Text(n), t)
	}
}

func (c *checker) checkReturnStmt(n ast.NodeIndex) {
	value := c.tree.Child(n, 0)
	fn := c.curFunc
	if fn == nil {
		return // unreachable given the grammar (return only inside a func body)
	}

	if value == ast.InvalidNode {
		if fn.hasReturn {
			c.errorAt(n, "missing return value (function returns %s)", fn.ret)
		}
		return
	}

	if c.tree.Nodes[value].Kind == enums.NodeKinds.MultiValueExpr {
		c.checkMultiValueReturn(value, fn)
		return
	}

	vt := c.checkValueExpr(value)
	if !fn.hasReturn {
		c.errorAt(value, "function does not return a value")
		return
	}
	if fn.ret.Kind == TypeMultiReturn {
		c.errorAt(value, "function returns %s; return must supply %d values", fn.ret, len(fn.ret.Params))
		return
	}
	if c.checkAssignable(value, fn.ret, vt, "return statement") {
		// allowFresh=false: returning a non-copyable type by value is always
		// rejected, even when the returned expression is itself a fresh
		// construction - see checkNoIllegalCopy's own doc comment for why
		// this one context allows no exception at all.
		c.checkNoIllegalCopy(value, fn.ret, false, "return statement")
	}
}

// checkMultiValueReturn type-checks `return a, b, ...` (listNode is the
// MultiValueExpr wrapping the value list - see ast.Node's own doc comment)
// against fn, the enclosing function's own return context - the multi-value
// counterpart to checkReturnStmt's own plain single-value tail. Every
// individual value is an ordinary single-value expression (checkValueExpr),
// exactly like a single-value `return expr` would check its own one value -
// there's no argument-spreading here (a further multi-return call among the
// listed values is rejected the same way any other position rejects one).
func (c *checker) checkMultiValueReturn(listNode ast.NodeIndex, fn *enclosingFunc) {
	values := c.tree.Children(listNode)

	if !fn.hasReturn {
		c.errorAt(listNode, "function does not return a value")
		for _, v := range values {
			c.checkValueExpr(v)
		}
		return
	}
	if fn.ret.Kind != TypeMultiReturn {
		c.errorAt(listNode, "function returns %s, not multiple values", fn.ret)
		for _, v := range values {
			c.checkValueExpr(v)
		}
		return
	}

	want := fn.ret.Params
	if len(values) != len(want) {
		c.errorAtNodes(values, listNode, "wrong number of return values: got %d, want %d", len(values), len(want))
		for _, v := range values {
			c.checkValueExpr(v)
		}
		return
	}

	for i, v := range values {
		vt := c.checkValueExpr(v)
		context := fmt.Sprintf("return value %d", i+1)
		if c.checkAssignable(v, want[i], vt, context) {
			// allowFresh=false, same reasoning as checkReturnStmt's own
			// single-value tail: a multi-value return allows no fresh-
			// construction exception either.
			c.checkNoIllegalCopy(v, want[i], false, context)
		}
	}
}

func (c *checker) checkIfStmt(n ast.NodeIndex) {
	c.checkCondition(c.tree.Child(n, 0))
	c.checkStmt(c.tree.Child(n, 1))
	if elseBranch := c.tree.Child(n, 2); elseBranch != ast.InvalidNode {
		c.checkStmt(elseBranch)
	}
}

func (c *checker) checkForStmt(n ast.NodeIndex) {
	if init := c.tree.Child(n, 0); init != ast.InvalidNode {
		c.checkStmt(init)
	}
	if cond := c.tree.Child(n, 1); cond != ast.InvalidNode {
		c.checkCondition(cond)
	}
	if post := c.tree.Child(n, 2); post != ast.InvalidNode {
		c.checkStmt(post)
	}
	c.loopDepth++
	c.checkBlock(c.tree.Child(n, 3))
	c.loopDepth--
}

func (c *checker) checkCondition(n ast.NodeIndex) {
	t := c.checkValueExpr(n)
	if !t.IsInvalid() && t.Kind != TypeBool {
		c.errorAt(n, "condition must be bool, got %s", t)
	}
}

// checkExpr type-checks n and memoizes its Type into info.Types, so every
// expression node this pass visits has an entry - no nil/zero-value
// checking needed downstream (codegen). The one deliberate exception is a
// *direct* call's callee position (a plain function name, or a method's
// MemberExpr) - see funcSigForCall: an *indirect* call's callee (a
// function-typed variable/parameter, or any other function-valued
// expression) does get a real entry, same as any other value expression.
func (c *checker) checkExpr(n ast.NodeIndex) Type {
	if n == ast.InvalidNode {
		return invalidType
	}
	t := c.inferExpr(n)
	c.info.Types[n] = t
	return t
}

// checkValueExpr is checkExpr for a position that requires an actual value -
// everywhere except a bare call used as a statement (see checkStmt's
// ExprStmt case). A void result (a call to a function with no declared
// return type) is valid there and nowhere else.
func (c *checker) checkValueExpr(n ast.NodeIndex) Type {
	t := c.checkExpr(n)
	if t.Kind == TypeVoid {
		c.errorAt(n, "call does not return a value, cannot be used here")
		return invalidType
	}
	if t.Kind == TypeMultiReturn {
		// A multi-return call's result (see LANGUAGE.md's "Go-style
		// multi-return values" section) has deliberately no first-class
		// tuple type - it can only ever be consumed by immediate
		// destructuring at the one matching call site (a return statement
		// matching the enclosing function's own multi-return type -
		// checkMultiValueReturn - or the sole right-hand side of a matching
		// multi-target `:=`/`=` - checkDestructureSource), never stored,
		// passed on, or used as an ordinary single value. Both of those two
		// consuming positions call checkExpr directly instead of this
		// function specifically to bypass this rejection - every other
		// position (a single-name `:=`, a call argument, print, an operator
		// operand, ...) still goes through checkValueExpr and lands here.
		c.errorAt(n, "multi-value result %s cannot be used as a single value; it can only be destructured immediately (a, b := ... / a, b = ...) or returned matching a function's own multi-return type", t)
		return invalidType
	}
	return t
}

func (c *checker) inferExpr(n ast.NodeIndex) Type {
	switch c.tree.Nodes[n].Kind {
	case enums.NodeKinds.Ident:
		return c.checkIdentExpr(n)
	case enums.NodeKinds.NumberLit:
		return c.checkNumberLit(n)
	case enums.NodeKinds.StringLit:
		return stringType
	case enums.NodeKinds.BoolLit:
		return boolType
	case enums.NodeKinds.ThisExpr:
		return c.checkThisExpr(n)
	case enums.NodeKinds.ParenExpr:
		return c.checkExpr(c.tree.Child(n, 0))
	case enums.NodeKinds.UnaryExpr:
		return c.checkUnaryExpr(n)
	case enums.NodeKinds.BinaryExpr:
		return c.checkBinaryExpr(n)
	case enums.NodeKinds.CallExpr:
		return c.checkCallExpr(n)
	case enums.NodeKinds.IndexExpr:
		return c.checkIndexExpr(n)
	case enums.NodeKinds.SliceExpr:
		return c.checkSliceExpr(n)
	case enums.NodeKinds.MemberExpr:
		return c.checkMemberExpr(n)
	case enums.NodeKinds.CompositeLit:
		return c.checkCompositeLit(n)
	case enums.NodeKinds.FuncLit:
		return c.checkFuncLit(n)
	case enums.NodeKinds.NewExpr:
		return c.checkNewExpr(n)
	case enums.NodeKinds.ArrayType:
		// Reachable two ways (see resolve.go's resolveExpr, which documents
		// the same two paths for its own ArrayType case): a bare array type
		// used where an expression was expected (the parser's own
		// parse-error recovery path, parser/expr.go's parseArrayTypeLit -
		// already flagged by the parser itself), or - a second, genuinely
		// diagnostic-free path - a user function named `make` (shadowing the
		// predeclared builtin, legal per scope.go's universeScope) called
		// with make's own bespoke argument grammar: isMakeCallee (parser/
		// expr.go) dispatches purely on the callee's lexical spelling, with
		// no awareness that `make` might resolve to an ordinary function, so
		// it unconditionally forces the first "argument" through
		// parseTypeExpr into an ArrayType node. isBuiltinCall correctly sees
		// through the shadowing and falls through to the ordinary-call path
		// below, but that path expects every argument to be an ordinary
		// value expression - without this case, this ArrayType node would
		// silently type as invalidType with zero diagnostic (checkAssignable
		// treats an already-invalid operand as "already reported elsewhere"),
		// letting a syntactically-valid-looking call reach codegen's genExpr,
		// which has no ArrayType case and panics. Reported here instead, so
		// either path ends in a real diagnostic rather than an internal
		// panic - matching this codebase's "lower already-correct code, not
		// re-derive semantics" contract for codegen (see CODEGEN.md).
		c.errorAt(n, "array type used as a value")
		return invalidType
	default:
		// Bad has no sensible value type; already diagnosed upstream.
		return invalidType
	}
}

// checkNumberLit types a numeric literal as one of Go's own "untyped
// constant" kinds (see AGENTS.md's Types section) rather than immediately
// picking a concrete type: `.`/`e`/`E` in the literal's text (the same test
// that used to reject a float literal outright, back when this language had
// no float type at all - see BLOCKERS.md) now just decides which untyped
// bucket it starts in. Some later context (a declared type, a concretely-
// typed operand, a parameter/return type, a composite-literal element type,
// or - absent any of those - Go's own untyped-constant default) resolves it
// to a concrete type; see resolveNumericOperands/checkAssignable/
// defaultIfUntyped.
func (c *checker) checkNumberLit(n ast.NodeIndex) Type {
	text := c.tree.Text(n)
	if strings.ContainsAny(text, ".eE") {
		return untypedFloatType
	}
	return untypedIntType
}

// checkIdentExpr types a bare identifier reference. A free function name
// (SymFunc with a real declaration) is a first-class value now - its Type is
// a TypeFunc built from its own signature (funcType/funcSigForDecl) - see
// LANGUAGE.md's "First-class functions" section; this is deliberately scoped
// to free functions only, never a method (a method is never reachable
// through a bare Ident at all - only through a MemberExpr, which
// checkMemberExpr still rejects as a value uncalled, exactly as before -
// see LANGUAGE.md for why method values remain out of scope this round). The
// predeclared `print` builtin is a SymFunc with no real declaration
// (Decl == InvalidNode - see universeScope) and so has no signature to
// build a Type from; referencing it bare remains an error.
func (c *checker) checkIdentExpr(n ast.NodeIndex) Type {
	sym, ok := c.info.Refs[n]
	if !ok {
		return invalidType // undefined name; already reported by Resolve
	}
	return c.typeOfSymbolValue(n, sym)
}

// typeOfSymbolValue types a reference to sym used as a value - shared
// between a bare identifier (checkIdentExpr) and a package-qualified
// reference (`pkg.Name`, checkMemberExpr's package branch/funcSigForCall):
// both name exactly the same kinds of top-level declaration, so the same
// var/func/type/package dispatch applies either way. n is only used for its
// source position and (via c.tree.Text) the name text in a diagnostic - for
// a MemberExpr this is still correct, since a MemberExpr's own Tok is the
// field-name token (see ast.Node's doc comment).
func (c *checker) typeOfSymbolValue(n ast.NodeIndex, sym *Symbol) Type {
	switch sym.Kind {
	case SymVar, SymParam:
		// sym may be declared in a different file (or package) than the one
		// currently being checked - see LANGUAGE.md's "Multi-file packages"
		// and "Imports" sections - pushTree switches to its own owning file
		// for the duration of declType, which reads that file's own
		// declaration/initializer nodes.
		restore := c.pushTree(sym.Tree)
		t := c.declType(sym.Decl)
		restore()
		return t
	case SymFunc:
		if sym.Decl == ast.InvalidNode {
			c.errorAt(n, "%s is a builtin, not a value", c.tree.Text(n))
			return invalidType
		}
		restore := c.pushTree(sym.Tree)
		sig := c.funcSigForDecl(sym.Decl)
		restore()
		return funcType(sig)
	case SymStruct, SymBuiltinType:
		c.errorAt(n, "%s is a type, not a value", c.tree.Text(n))
		return invalidType
	case SymPackage:
		c.errorAt(n, "%s is a package, not a value", c.tree.Text(n))
		return invalidType
	case SymBuiltinValue:
		// Currently only `nil` (see scope.go's universeScope) - starts life
		// untyped, exactly like a numeric literal (checkNumberLit), deferring
		// to whatever pointer-typed context it's used in (checkAssignable/
		// checkEqualityOperands) to pin down the concrete *T.
		return untypedNilType
	default:
		return invalidType
	}
}

// checkThisExpr types `this` as a pointer to the enclosing method's receiver
// struct (`*T`, never the bare struct type) - matching what `this` already,
// literally is at the codegen level: the receiver parameter itself, a real
// pointer, no alloca of its own (see CODEGEN.md's "Method receivers"
// section). This is what makes `return this`/`x := this`/passing `this` as
// a `*T`-typed argument type-check - a bare `this` used as an ordinary value
// couldn't be expressed at all before this, only `.field`/`.method()` access
// through it. It does NOT change the receiver-declaration syntax itself
// (`func (T) Method()` stays receiver-less, no `*` - see LANGUAGE.md's
// "Structs" section); it's purely about the type of a bare `this` value.
//
// resolveMember's own generic `TypePointer` auto-deref (used for all
// `.field`/`.method()` access, same mechanism an ordinary `*T`-typed local
// already goes through) means `this.field`/`this.method(...)` keep working
// completely unchanged - see checkThisExprRegression-style tests.
//
// sym.Decl is that struct's own StructDecl node (see resolve.go's
// fnScope.Receiver construction), not a variable declaration, so this
// doesn't go through declType.
func (c *checker) checkThisExpr(n ast.NodeIndex) Type {
	sym, ok := c.info.Refs[n]
	if !ok {
		return invalidType // "this outside a method"; already reported by Resolve
	}
	// sym.Decl is the receiver struct's own StructDecl node, which may live
	// in a different file than the method itself (see Symbol.Tree's doc
	// comment) - read it via sym.Tree, never c.tree, which is this method's
	// own file and would misinterpret a foreign NodeIndex.
	name := sym.Tree.Text(sym.Tree.Child(sym.Decl, 0))
	info, ok := c.info.Structs[name]
	if !ok {
		return invalidType
	}
	return Type{
		Kind: TypePointer,
		Elem: &Type{
			Kind:   TypeStruct,
			Struct: info,
		},
	}
}

// checkUnaryExpr types `-`/`!`/`&`/`*`. Unary `-` works on any numeric type
// (every int width, every float width, or an untyped constant - see
// AGENTS.md's Types section) and always yields the exact same Type/Kind it
// was given, untyped included: an untyped operand simply stays untyped,
// deferring resolution further up the tree exactly like a NumberLit would on
// its own (see retypeUntyped's ParenExpr/UnaryExpr case, which knows to
// recurse into a unary-minus operand the same way). `&`/`*` (see
// LANGUAGE.md's "Pointers" section) are handled by their own dedicated
// helpers (checkAddressOf/checkDeref) before ever reaching
// checkValueExpr - unlike -/!/&&, `&`'s operand must be checked as an
// addressable location, not an ordinary value expression (an addressable
// expression is still a perfectly good value too, but checkAddressOf needs
// to know *which* expression shapes even qualify, the same reason
// checkLValue exists as its own function rather than reusing checkExpr's
// generic dispatch).
func (c *checker) checkUnaryExpr(n ast.NodeIndex) Type {
	operand := c.tree.Child(n, 0)
	op := c.tree.Text(n)
	switch op {
	case "&":
		return c.checkAddressOf(n, operand)
	case "*":
		return c.checkDeref(n, operand)
	}

	t := c.checkValueExpr(operand)
	if t.IsInvalid() {
		return invalidType
	}
	switch op {
	case "-":
		if !t.IsNumeric() {
			c.errorAt(n, "operator - not defined for %s", t)
			return invalidType
		}
		return t
	case "!":
		if t.Kind != TypeBool {
			c.errorAt(n, "operator ! not defined for %s", t)
			return invalidType
		}
		return boolType
	default:
		return invalidType
	}
}

// checkAddressOf types `&x` (see LANGUAGE.md's "Pointers" section): operand
// must be an addressable location (checkAddressable) - not a plain value -
// producing a real pointer to it, rather than a pointer to some anonymous
// spilled copy (see codegen's genAddr/genUnaryExpr, which lowers this
// exactly that way: the address itself, no intermediate load).
func (c *checker) checkAddressOf(n, operand ast.NodeIndex) Type {
	t, ok := c.checkAddressable(operand)
	if !ok || t.IsInvalid() {
		return invalidType
	}
	return Type{
		Kind: TypePointer,
		Elem: &t,
	}
}

// checkAddressable reports whether operand is a valid `&` operand - the same
// set of expression shapes checkLValue accepts as a valid assignment target
// (a variable, a struct field, an array element, or another pointer's
// dereference), since "has a real address `&` can take" and "can appear on
// the left of `=`" are the same property here. Kept as its own function
// (rather than reusing checkLValue directly) purely so the diagnostic
// wording matches what `&` misuse actually looks like ("cannot take the
// address of X", not checkLValue's own "cannot assign to X").
//
// A MemberExpr/IndexExpr shape isn't addressable on its own, though - a
// `.field`/`[index]` needs its own *object* to itself be addressable (or
// already be a pointer being auto-dereferenced/a dynamic array indexed
// straight into, both always fine - see isAddressableChain), all the way
// down to some real base storage location, exactly like Go's own
// "addressable all the way down" rule - so isAddressableChain is what
// actually decides those two cases, once checkExpr itself has confirmed the
// operand type-checks at all.
func (c *checker) checkAddressable(n ast.NodeIndex) (Type, bool) {
	switch c.tree.Nodes[n].Kind {
	case enums.NodeKinds.Ident:
		sym, ok := c.info.Refs[n]
		if ok && sym.Kind != SymVar && sym.Kind != SymParam {
			c.errorAt(n, "cannot take the address of %s (%s is not a variable)", c.tree.Text(n), sym.Kind)
			return invalidType, false
		}
		t := c.checkExpr(n)
		return t, !t.IsInvalid()
	case enums.NodeKinds.MemberExpr,
		enums.NodeKinds.IndexExpr:
		t := c.checkExpr(n)
		if t.IsInvalid() {
			return invalidType, false
		}
		if !c.isAddressableChain(n) {
			c.errorAt(n, "cannot take the address of this expression")
			return invalidType, false
		}
		return t, true
	case enums.NodeKinds.UnaryExpr:
		if c.tree.Text(n) != "*" {
			c.errorAt(n, "cannot take the address of this expression")
			return invalidType, false
		}
		t := c.checkExpr(n)
		return t, !t.IsInvalid()
	case enums.NodeKinds.Bad:
		return invalidType, false
	default:
		c.errorAt(n, "cannot take the address of this expression")
		return invalidType, false
	}
}

// isAddressableChain reports whether n - a MemberExpr or IndexExpr, already
// fully type-checked by the caller (so c.info.Types/c.info.Refs already have
// every entry this recurses into) - denotes a real, stable storage location
// rather than a throwaway value, recursively all the way down to some base
// case, matching Go's own "addressable all the way down" rule:
//
//   - a MemberExpr (`p.field`) is addressable iff p itself is addressable -
//     *unless* p is itself pointer-typed, in which case `.field` auto-derefs
//     it (see resolveMember), and dereferencing a pointer is always a real
//     address regardless of whether the pointer expression itself had one.
//   - an IndexExpr (`arr[i]`) is addressable iff arr itself is addressable -
//     *unless* arr is a dynamic array (`[]T`), whose backing storage always
//     lives on the arena heap already (see codegen's genAddr IndexExpr case),
//     never in the slice header's own storage, so no recursion into the
//     header's own addressability is needed either.
//   - an Ident is addressable iff it names a variable/parameter (the same
//     base case checkAddressable's own Ident branch already applies).
//   - a "*" UnaryExpr (a dereference) is always addressable, same as
//     checkAddressable's own UnaryExpr branch.
//   - anything else (a call, a literal, an arbitrary expression) is not.
//
// Shared by checkAddressable (for `&`) and checkArraySliceAddressable (for
// slicing a fixed array) - both need exactly this same recursive rule, just
// applied at a different starting node shape.
func (c *checker) isAddressableChain(n ast.NodeIndex) bool {
	switch c.tree.Nodes[n].Kind {
	case enums.NodeKinds.Ident:
		sym, ok := c.info.Refs[n]
		return ok && (sym.Kind == SymVar || sym.Kind == SymParam)
	case enums.NodeKinds.MemberExpr:
		object := c.tree.Child(n, 0)
		if c.info.Types[object].Kind == TypePointer {
			return true
		}
		return c.isAddressableChain(object)
	case enums.NodeKinds.IndexExpr:
		target := c.tree.Child(n, 0)
		targetType := c.info.Types[target]
		if targetType.Kind == TypeArray && targetType.Dynamic {
			return true
		}
		return c.isAddressableChain(target)
	case enums.NodeKinds.UnaryExpr:
		return c.tree.Text(n) == "*"
	default:
		return false
	}
}

// checkDeref types `*p` (see LANGUAGE.md's "Pointers" section): p must be a
// pointer type; the result is its pointee type. Valid both as a value
// (`x := *p`) and, via checkLValue's own UnaryExpr("*") case, as an
// assignment target (`*p = v`).
func (c *checker) checkDeref(n, operand ast.NodeIndex) Type {
	t := c.checkValueExpr(operand)
	if t.IsInvalid() {
		return invalidType
	}
	if t.Kind != TypePointer {
		c.errorAt(n, "cannot dereference %s (not a pointer)", t)
		return invalidType
	}
	return *t.Elem
}

// checkBinaryExpr types a binary operator against its (already
// value-checked) operands. See AGENTS.md's Operators section for the exact
// rules this encodes:
//   - `+` is numeric+numeric->numeric or string+string->string (the one
//     operator overloaded onto a second type - string concatenation)
//   - `- * /` are numeric+numeric->numeric (int widths and floats alike)
//   - `%` and the bitwise `& | ^` are integer+integer->integer only (any
//     width, but never float - same restriction Go itself has)
//   - `== !=` require both operands the same comparable type: numeric
//     (any combination of widths/untyped-ness that resolves to a common
//     type - see resolveNumericOperands), string, bool, or the exact same
//     struct/array type (Type.Equal). Two structs/arrays of different types
//     remain a compile error, same as everywhere else (no implicit
//     conversion) - this only accepts the case where both sides already
//     agree.
//   - `< <= > >=` are numeric+numeric->bool or string+string->bool
//     (lexicographic ordering, same as Go)
//   - `&& ||` are bool+bool->bool only
//
// A numeric operand may be untyped (a bare literal, or an expression built
// entirely from them) - see resolveNumericOperands for exactly how an
// untyped operand adapts to a concretely-typed one, or how two untyped
// operands combine while staying deferred.
func (c *checker) checkBinaryExpr(n ast.NodeIndex) Type {
	lNode := c.tree.Child(n, 0)
	rNode := c.tree.Child(n, 1)
	lt := c.checkValueExpr(lNode)
	rt := c.checkValueExpr(rNode)
	op := c.tree.Text(n)

	if lt.IsInvalid() || rt.IsInvalid() {
		return invalidType
	}

	switch op {
	case "+":
		if lt.Kind == TypeString && rt.Kind == TypeString {
			return stringType
		}
		if result, ok := c.checkNumericBinary(lNode, rNode, lt, rt, false); ok {
			return result
		}
		c.errorAt(n, "operator + not defined for %s and %s", lt, rt)
		return invalidType
	case "-", "*", "/":
		if result, ok := c.checkNumericBinary(lNode, rNode, lt, rt, false); ok {
			return result
		}
		c.errorAt(n, "operator %s requires numeric operands, got %s and %s", op, lt, rt)
		return invalidType
	case "%", "&", "|", "^":
		if result, ok := c.checkNumericBinary(lNode, rNode, lt, rt, true); ok {
			return result
		}
		c.errorAt(n, "operator %s requires integer operands, got %s and %s", op, lt, rt)
		return invalidType
	case "==", "!=":
		return c.checkEqualityOperands(n, lNode, rNode, lt, rt, op)
	case "<", "<=", ">", ">=":
		return c.checkOrderingOperands(n, lNode, rNode, lt, rt, op)
	case "&&", "||":
		if lt.Kind == TypeBool && rt.Kind == TypeBool {
			return boolType
		}
		c.errorAt(n, "operator %s requires bool operands, got %s and %s", op, lt, rt)
		return invalidType
	default:
		return invalidType
	}
}

// checkNumericBinary type-checks a numeric binary operator (arithmetic or
// bitwise) given its already-checked operand types, resolving any untyped
// operand along the way (see resolveNumericOperands). intOnly rejects a
// float-kind result - the bitwise `&|^` and `%` operators, which Go also
// restricts to integers.
func (c *checker) checkNumericBinary(lNode, rNode ast.NodeIndex, lt, rt Type, intOnly bool) (Type, bool) {
	resolved, ok := c.resolveNumericOperands(lNode, rNode, lt, rt)
	if !ok {
		return invalidType, false
	}
	if intOnly && resolved.IsFloatKind() {
		return invalidType, false
	}
	return resolved, true
}

// resolveNumericOperands computes the common numeric type lNode/rNode (whose
// already-checked types are lt/rt) share for a binary numeric operator,
// adapting whichever operand is untyped to the other's concrete type (see
// AGENTS.md's Types section for the exact untyped-constant rule this
// implements) and immediately retyping that operand's whole subtree
// (retypeUntyped), since nothing else will ever revisit it. When BOTH
// operands are untyped, resolution is deferred instead - the combined "kind"
// (untyped-float if either operand looks like a float, else untyped-int) is
// returned without retyping either operand yet, so an enclosing context
// further up the tree (checkAssignable, or a further binary operator) still
// gets the chance to pin a concrete type later.
//
// Reports ok=false for two already-concrete numeric types of different
// widths/kinds, an untyped-float operand meeting an already-concrete integer
// type (would silently truncate - rejected, same as everywhere else untyped-
// float meets an int context), or a non-numeric operand.
func (c *checker) resolveNumericOperands(lNode, rNode ast.NodeIndex, lt, rt Type) (Type, bool) {
	if !lt.IsNumeric() || !rt.IsNumeric() {
		return invalidType, false
	}
	switch {
	case lt.IsUntyped() && rt.IsUntyped():
		if lt.Kind == TypeUntypedFloat || rt.Kind == TypeUntypedFloat {
			return untypedFloatType, true
		}
		return untypedIntType, true
	case lt.IsUntyped():
		if lt.Kind == TypeUntypedFloat && rt.IsIntegerKind() {
			return invalidType, false
		}
		c.retypeUntyped(lNode, rt)
		return rt, true
	case rt.IsUntyped():
		if rt.Kind == TypeUntypedFloat && lt.IsIntegerKind() {
			return invalidType, false
		}
		c.retypeUntyped(rNode, lt)
		return lt, true
	default:
		if lt.Equal(rt) {
			return lt, true
		}
		return invalidType, false
	}
}

// checkEqualityOperands types `==`/`!=`. A dynamic array (slice) is never
// comparable with either operator - mirroring Go's own restriction exactly
// (Go only allows a slice to be compared against `nil`, a concept this
// language doesn't have yet - see LANGUAGE.md's "Dynamic arrays" section) -
// checked before the general struct/array case below, which would otherwise
// happily accept two identically-typed slices via Type.Equal.
func (c *checker) checkEqualityOperands(n, lNode, rNode ast.NodeIndex, lt, rt Type, op string) Type {
	if (lt.Kind == TypeArray && lt.Dynamic) || (rt.Kind == TypeArray && rt.Dynamic) {
		c.errorAt(n, "slices are not comparable with %s", op)
		return invalidType
	}
	if lt.Kind == TypeUntypedNil || rt.Kind == TypeUntypedNil {
		return c.checkNilEquality(n, lNode, rNode, lt, rt, op)
	}
	switch {
	case lt.Kind == TypeStruct, lt.Kind == TypeArray:
		if lt.Equal(rt) {
			return boolType
		}
	case lt.Kind == TypeString && rt.Kind == TypeString:
		return boolType
	case lt.Kind == TypeBool && rt.Kind == TypeBool:
		return boolType
	case lt.Kind == TypePointer && rt.Kind == TypePointer:
		// Pointer identity comparison (see LANGUAGE.md's "Pointers" section) -
		// both sides must point to the exact same pointee type, same
		// no-implicit-conversion rule every other operator here follows.
		if lt.Equal(rt) {
			return boolType
		}
	case lt.IsNumeric() && rt.IsNumeric():
		if c.resolveComparisonOperands(lNode, rNode, lt, rt) {
			return boolType
		}
	}
	c.errorAt(n, "operator %s not defined for %s and %s", op, lt, rt)
	return invalidType
}

// checkNilEquality types `p == nil`/`nil == p`-shaped comparisons (see
// LANGUAGE.md's "Pointers" section) - the one place nil's own untyped Type
// (TypeUntypedNil) is compared, deliberately kept out of
// checkEqualityOperands' own general switch above, the same way this
// language's numeric untyped constants get their own dedicated resolution
// path (resolveComparisonOperands) rather than folding into a generic
// Type.Equal check. Comparing nil against nil is rejected outright - there's
// no pointer type either side could adapt to.
func (c *checker) checkNilEquality(n, lNode, rNode ast.NodeIndex, lt, rt Type, op string) Type {
	switch {
	case lt.Kind == TypeUntypedNil && rt.Kind == TypeUntypedNil:
		c.errorAt(n, "cannot compare nil with nil (no pointer type to infer)")
		return invalidType
	case lt.Kind == TypeUntypedNil:
		if rt.Kind != TypePointer {
			c.errorAt(n, "cannot compare nil with %s", rt)
			return invalidType
		}
		c.retypeUntyped(lNode, rt)
		return boolType
	default:
		if lt.Kind != TypePointer {
			c.errorAt(n, "cannot compare nil with %s", lt)
			return invalidType
		}
		c.retypeUntyped(rNode, lt)
		return boolType
	}
}

// checkOrderingOperands types `< <= > >=`.
func (c *checker) checkOrderingOperands(n, lNode, rNode ast.NodeIndex, lt, rt Type, op string) Type {
	if lt.Kind == TypeString && rt.Kind == TypeString {
		return boolType
	}
	if lt.IsNumeric() && rt.IsNumeric() && c.resolveComparisonOperands(lNode, rNode, lt, rt) {
		return boolType
	}
	c.errorAt(n, "operator %s requires numeric or string operands, got %s and %s", op, lt, rt)
	return invalidType
}

// resolveComparisonOperands is resolveNumericOperands specialized for a
// comparison operator (==, !=, < <= > >=): unlike an arithmetic operator, a
// comparison never has anywhere further up the tree to defer resolution to
// (its own result is always bool, never numeric) - so, unlike
// checkNumericBinary, a still-untyped result here is immediately defaulted
// (defaultUntyped) rather than left deferred, since codegen needs a concrete
// LLVM type for both operands right here to emit icmp/fcmp.
func (c *checker) resolveComparisonOperands(lNode, rNode ast.NodeIndex, lt, rt Type) bool {
	resolved, ok := c.resolveNumericOperands(lNode, rNode, lt, rt)
	if !ok {
		return false
	}
	if resolved.IsUntyped() {
		def := c.defaultUntyped(resolved)
		c.retypeUntyped(lNode, def)
		c.retypeUntyped(rNode, def)
	}
	return true
}

func (c *checker) checkIndexExpr(n ast.NodeIndex) Type {
	target := c.tree.Child(n, 0)
	index := c.tree.Child(n, 1)

	tt := c.checkValueExpr(target)
	it := c.checkValueExpr(index)
	if !it.IsInvalid() {
		switch {
		case it.Kind == TypeUntypedInt:
			c.retypeUntyped(index, i32Type)
		case it.Kind != TypeI32:
			c.errorAt(index, "array index must be int, got %s", it)
		}
	}

	if tt.IsInvalid() {
		return invalidType
	}
	if tt.Kind != TypeArray {
		c.errorAt(n, "cannot index into %s (not an array)", tt)
		return invalidType
	}
	return *tt.Elem
}

// checkSliceExpr types a Go-style slice expression `s[a:b]` / `s[:b]` /
// `s[a:]` / `s[:]` (see LANGUAGE.md's "Slicing" section) - low/high (n's
// second/third child) may each be ast.InvalidNode when omitted (see
// ast.Node's own SliceExpr doc comment). Three different operand shapes
// share this one grammar, each producing a different result type:
//   - a dynamic array (`[]T`) slices to another `[]T` - the exact same Type,
//     a fresh header sharing the same backing memory, no copy.
//   - a string slices to another `string`, same no-copy sharing.
//   - a fixed-size array (`[N]T`) slices to a genuine `[]T` (a dynamic
//     array), not another fixed-size array, matching Go's own real behavior
//   - and requires its operand to be addressable (checkArraySliceAddressable),
//     since the resulting slice needs a real, stable backing address to
//     alias into; a bare fixed-array rvalue (e.g. a function call's result)
//     has no such storage.
//
// low/high are ordinary runtime `int`-typed expressions (checkSliceBound),
// not required to be compile-time constants, mirroring make's own n/cap
// (checkMakeSizeArg) - the actual `0 <= low <= high <= cap-or-len-or-N`
// range check is a codegen-level runtime trap (see CODEGEN.md), not
// something sema can reject here in general.
func (c *checker) checkSliceExpr(n ast.NodeIndex) Type {
	objNode := c.tree.Child(n, 0)
	lowNode := c.tree.Child(n, 1)
	highNode := c.tree.Child(n, 2)

	objType := c.checkValueExpr(objNode)
	c.checkSliceBound(lowNode)
	c.checkSliceBound(highNode)

	if objType.IsInvalid() {
		return invalidType
	}

	switch {
	case objType.Kind == TypeArray && objType.Dynamic:
		return objType
	case objType.Kind == TypeArray:
		if !c.checkArraySliceAddressable(objNode) {
			c.errorAt(objNode, "cannot slice a non-addressable array value")
			return invalidType
		}
		return Type{
			Kind:    TypeArray,
			Dynamic: true,
			Elem:    objType.Elem,
		}
	case objType.Kind == TypeString:
		return stringType
	default:
		c.errorAt(n, "cannot slice %s (not an array or string)", objType)
		return invalidType
	}
}

// checkArraySliceAddressable reports whether objNode - already type-checked
// by checkSliceExpr's own checkValueExpr call as a fixed-size array - is a
// real addressable location, recursively, exactly the same rule `&` needs
// (isAddressableChain): slicing a fixed array needs a real, stable backing
// address to alias into (see LANGUAGE.md's "Slicing" section), and that
// requirement doesn't stop at objNode's own outermost shape - a
// `.field`/`[index]` chain is only addressable if *its own* object/target is
// addressable all the way down (Go's own rule; see isAddressableChain's own
// doc comment for the full case-by-case reasoning, including the
// pointer-auto-deref and dynamic-array-index exceptions). This is a pure
// shape/addressability check with no re-type-checking side effect of its own
// (unlike checkAddressable, which both type-checks and shape-checks its
// operand in one call) - objNode was already fully checked by the caller.
func (c *checker) checkArraySliceAddressable(objNode ast.NodeIndex) bool {
	switch c.tree.Nodes[objNode].Kind {
	case enums.NodeKinds.Ident:
		sym := c.info.Refs[objNode]
		return sym.Kind == SymVar || sym.Kind == SymParam
	case enums.NodeKinds.MemberExpr,
		enums.NodeKinds.IndexExpr:
		return c.isAddressableChain(objNode)
	case enums.NodeKinds.UnaryExpr:
		return c.tree.Text(objNode) == "*"
	default:
		return false
	}
}

// checkSliceBound type-checks one of a SliceExpr's own low/high bound
// expressions - a no-op when node is ast.InvalidNode (the bound was
// omitted). Any integer type works, with an untyped constant defaulting to
// int exactly like make's own n/cap (checkMakeSizeArg) - there's no
// int-width restriction beyond "integer", matching every other int-typed
// position in this language.
func (c *checker) checkSliceBound(node ast.NodeIndex) {
	if node == ast.InvalidNode {
		return
	}
	t := c.defaultIfUntyped(node, c.checkValueExpr(node))
	if t.IsInvalid() {
		return
	}
	if t.Kind != TypeI32 {
		c.errorAt(node, "slice bound must be int, got %s", t)
	}
}

// checkMemberExpr types a plain field/package-member access (`p.field`, or
// `pkg.Name`, not a call). See resolveMember for how the name itself gets
// resolved - the work Resolve deliberately deferred to this pass for a
// struct-value field/method (unlike a package member, already resolved by
// Resolve itself - see resolve.go's resolvePackageMemberExpr).
func (c *checker) checkMemberExpr(n ast.NodeIndex) Type {
	sym, ok := c.resolveMember(n)
	if !ok {
		return invalidType
	}
	if c.memberObjectIsPackage(n) {
		// A package export can be any top-level kind (var, func, struct
		// type) - handle it exactly like a bare identifier reference, not
		// like "must be a field" (that restriction only makes sense for an
		// actual struct-value member).
		return c.typeOfSymbolValue(n, sym)
	}
	if sym.Kind != SymField {
		c.errorAt(n, "%s is a method, not a field (call it with ())", c.tree.Text(n))
		return invalidType
	}
	// The field's own Field node lives in its struct's file, which may
	// differ from n's own file (see Symbol.Tree's doc comment).
	restore := c.pushTree(sym.Tree)
	t := c.typeFromNode(c.tree.Child(sym.Decl, 1)) // Field: [name, type]
	restore()
	return t
}

// memberObjectIsPackage reports whether n (a MemberExpr) is a
// package-qualified access (`pkg.Name`) - its object is a bare Ident already
// resolved (by Resolve, lexically) to a SymPackage symbol - as opposed to an
// ordinary struct-value field/method access.
func (c *checker) memberObjectIsPackage(n ast.NodeIndex) bool {
	object := c.tree.Child(n, 0)
	if c.tree.Nodes[object].Kind != enums.NodeKinds.Ident {
		return false
	}
	sym, ok := c.info.Refs[object]
	return ok && sym.Kind == SymPackage
}

// resolveMember infers a MemberExpr's object type, requires it to be a
// struct, and looks its name (n's Tok - see ast.Node's doc comment) up in
// that struct's Fields/Methods catalog, recording whichever it finds into
// info.Refs[n] - same as every other reference, resolving occurrence and
// declaration alike to the same *Symbol. This is shared between a plain
// field access (checkMemberExpr) and a method-call callee
// (methodSigForCallee), which differ only in which kind of symbol they
// require the result to be.
//
// A package-qualified access (`pkg.Name`) is a completely different
// resolution path - Resolve itself already fully resolved it (see
// resolve.go's resolvePackageMemberExpr, which needs no type information at
// all) - so this just reads back whatever Info.Refs[n] already holds
// instead of doing any struct-value lookup of its own.
//
// Memoized on Info.Refs[n] itself (rather than a separate cache map) - a
// given MemberExpr node's resolution never changes between calls, and two
// call sites can now legitimately ask about the same callee node in one
// Check pass (methodSigForCallee's own struct-field fallback, then
// funcSigForCall's indirect-call check re-deriving the same node's Type via
// checkExpr/checkMemberExpr) - without this, the second call would redo
// checkValueExpr(object) and checkExportedAccess from scratch.
func (c *checker) resolveMember(n ast.NodeIndex) (*Symbol, bool) {
	if sym, ok := c.info.Refs[n]; ok {
		return sym, true
	}
	if c.memberObjectIsPackage(n) {
		// Resolve already fully resolved this - Info.Refs[n] would have
		// already hit the memoization check above if it had succeeded, so
		// reaching here means Resolve itself already reported why.
		return nil, false
	}

	object := c.tree.Child(n, 0)
	objType := c.checkValueExpr(object)
	name := c.tree.Text(n)

	if objType.IsInvalid() {
		return nil, false
	}
	// A pointer-to-struct object auto-derefs for member access (see
	// LANGUAGE.md's "Pointers" section): `p.field`/`p.method(...)` on a `*T`
	// behaves exactly like `(*p).field`/`(*p).method(...)` would, matching
	// Go's own automatic pointer-dereference rule for selector expressions.
	// codegen's genAddr/genMethodCall mirror this same auto-deref at the
	// value-address level.
	if objType.Kind == TypePointer {
		objType = *objType.Elem
	}
	if objType.Kind != TypeStruct {
		c.errorAt(n, "%s undefined (%s is not a struct)", name, objType)
		return nil, false
	}

	info := objType.Struct
	var found *Symbol
	if sym, ok := info.Fields[name]; ok {
		found = sym
	} else if sym, ok := info.Methods[name]; ok {
		found = sym
	} else {
		c.errorAt(n, "%s has no field or method %s", info.Symbol.Name, name)
		return nil, false
	}
	if !c.checkExportedAccess(n, found) {
		return nil, false
	}
	c.info.Refs[n] = found
	return found, true
}

// checkExportedAccess enforces export visibility (see LANGUAGE.md's
// "Imports" section) for a struct field/method found by name on a value:
// an unexported (lowercase-first-letter) one is only accessible from within
// its own declaring package. Same-package access is always allowed
// regardless of case, matching the pre-existing (pre-imports) multi-file
// guarantee - see LANGUAGE.md's "Multi-file packages" section - and, when
// c.curPkgScope is nil (single-package Check/CheckPackage, with no
// cross-package concept at all - see CheckProgram's doc comment), nothing
// is ever restricted, matching that pre-existing behavior exactly.
//
// A package-qualified access's own export check happens earlier, during
// Resolve (resolve.go's resolvePackageMemberExpr/resolveTypeMemberExpr) -
// this only covers a struct-value field/method access, resolved here in
// Check because it needs the value's type.
func (c *checker) checkExportedAccess(n ast.NodeIndex, sym *Symbol) bool {
	if c.curPkgScope == nil || sym.Exported {
		return true
	}
	if packageScopeOf(sym.Scope) == c.curPkgScope {
		return true
	}
	c.errorAtLabel(n, "unexported symbol", "%s is not exported", sym.Name)
	return false
}

// crossPackageStructConstruction reports whether constructing a value of
// info's struct type (a composite literal naming it) is happening from
// outside info's own declaring package - the same "does this cross a
// package boundary" question checkExportedAccess answers for a member
// access, asked instead of the struct type itself: construction has no
// single occurrence's own Exported flag to check (the struct type name is
// always visible - LANGUAGE.md's export rule is about member names, not
// type names) - see checkStructCompositeLit, the only caller, for what this
// gates. nil c.curPkgScope (the plain single-package Check/CheckPackage
// case, same as checkExportedAccess) never counts as cross-package.
func (c *checker) crossPackageStructConstruction(info *StructInfo) bool {
	return c.curPkgScope != nil && packageScopeOf(info.Symbol.Scope) != c.curPkgScope
}

// firstUnexportedField returns the name of the first (in declaration order)
// unexported field among fields - Field nodes belonging to tree, which may
// differ from whichever tree is currently active in the checker (see
// checkStructCompositeLit) - and whether one was found at all. Reads each
// field's already-resolved Symbol.Exported bit (via info.Fields) rather than
// re-deriving it from the field's source text a second time - isExportedName
// is only ever meant to be called once, at declaration time (see its own doc
// comment on scope.go: "so Exported never needs recomputing later"), and this
// used to violate that.
func firstUnexportedField(tree *ast.Tree, info *StructInfo, fields []ast.NodeIndex) (string, bool) {
	for _, f := range fields {
		name := tree.Text(tree.Child(f, 0))
		if sym, ok := info.Fields[name]; ok && !sym.Exported {
			return name, true
		}
	}
	return "", false
}

// checkCallExpr type-checks a call: builtin print (special-cased - see
// isPrintCall), an explicit numeric conversion `T(x)` (checkConversionCall),
// a free function, a method call (`p.move()`), or an indirect call through
// a function-typed value (`fn(1, 2)` where fn is a variable/parameter, or
// any other expression whose value is itself a function - see
// funcSigForCall). Argument count and each argument's type are checked
// against the resolved callee's signature the same way regardless of which
// of those a given call turns out to be.
func (c *checker) checkCallExpr(n ast.NodeIndex) Type {
	children := c.tree.Children(n)
	callee, args := children[0], children[1:]

	if c.isPrintCall(callee) {
		return c.checkPrintCall(n, args)
	}
	if c.isBuiltinCall(callee, "make") {
		return c.checkMakeCall(n, args)
	}
	if c.isBuiltinCall(callee, "append") {
		return c.checkAppendCall(n, args)
	}
	if c.isBuiltinCall(callee, "len") {
		return c.checkLenCall(n, args)
	}
	if c.isBuiltinCall(callee, "args") {
		return c.checkArgsCall(n, args)
	}
	if t, ok := c.checkConstructorCall(n, callee, args); ok {
		return t
	}
	if t, ok := c.checkConversionCall(n, callee, args); ok {
		return t
	}

	sig, ok := c.funcSigForCall(callee)
	if !ok {
		for _, a := range args {
			c.checkValueExpr(a)
		}
		return invalidType
	}

	if len(args) != len(sig.Params) {
		c.errorAtNodes(args, n, "wrong number of arguments in call: got %d, want %d", len(args), len(sig.Params))
		for _, a := range args {
			c.checkValueExpr(a)
		}
		return sig.Return
	}

	for i, a := range args {
		at := c.checkValueExpr(a)
		if c.checkAssignable(a, sig.Params[i], at, fmt.Sprintf("argument %d", i+1)) {
			// allowFresh=true: passing a freshly-constructed non-copyable
			// value as an argument is sound with no extra machinery at all -
			// the callee's own parameter is a plain local exactly like any
			// other (see pushDestructorEntry, codegen/func.go), and becomes
			// that fresh value's one and only owner, destructing it at its
			// own scope exit - only an *existing* value handed in by
			// reference to another live owner is the real double-destruction
			// risk (see checkNoIllegalCopy's own doc comment for why return
			// is different).
			c.checkNoIllegalCopy(a, sig.Params[i], true, fmt.Sprintf("argument %d", i+1))
		}
	}
	return sig.Return
}

// isPrintCall reports whether callee names the predeclared print function
// (see scope.go's universeScope - it has no declaration site, Decl is
// InvalidNode, so it can't go through the normal FuncDecl-based signature
// machinery every user function does).
func (c *checker) isPrintCall(callee ast.NodeIndex) bool {
	if c.tree.Nodes[callee].Kind != enums.NodeKinds.Ident {
		return false
	}
	sym, ok := c.info.Refs[callee]
	return ok && sym.Kind == SymFunc && sym.Decl == ast.InvalidNode && sym.Name == "print"
}

// checkPrintCall accepts exactly one argument, of any type - print has no
// declaration to derive a stricter signature from, and AGENTS.md's examples
// use it on both int and string arguments interchangeably (see AGENTS.md's
// Operators section and BLOCKERS.md for this decision). An untyped argument
// (a bare numeric literal, e.g. `print(42)`) defaults like any other value
// context with no other type to adapt to (defaultIfUntyped) - print still
// needs a concrete numeric type to pick a format specifier from (see
// codegen's genPrintCall).
func (c *checker) checkPrintCall(n ast.NodeIndex, args []ast.NodeIndex) Type {
	if len(args) != 1 {
		c.errorAtNodes(args, n, "print takes exactly 1 argument, got %d", len(args))
	}
	for _, a := range args {
		c.defaultIfUntyped(a, c.checkValueExpr(a))
	}
	return voidType
}

// isBuiltinCall reports whether callee names the predeclared builtin
// function name (make/append/len - print has its own isPrintCall, unchanged)
// - see scope.go's universeScope: each of these, like print, is a SymFunc
// with no real declaration (Decl is InvalidNode), so it can't go through the
// normal FuncDecl-based signature machinery every user function does.
func (c *checker) isBuiltinCall(callee ast.NodeIndex, name string) bool {
	if c.tree.Nodes[callee].Kind != enums.NodeKinds.Ident {
		return false
	}
	sym, ok := c.info.Refs[callee]
	return ok && sym.Kind == SymFunc && sym.Decl == ast.InvalidNode && sym.Name == name
}

// checkMakeCall type-checks `make([]T, n)` / `make([]T, n, cap)` - see
// LANGUAGE.md's "Dynamic arrays" section. Unlike every other call this
// language has, make's first argument (args[0]) is a type-position node (an
// ArrayType, built by the parser's own bespoke make grammar - see
// parser/expr.go's parseMakeArgs), not a value expression, so it goes
// through typeFromNode rather than checkValueExpr. n and cap are ordinary
// runtime int expressions - unlike [N]T's N (see constArraySize), neither is
// required to be a compile-time constant; that's the entire point of
// "dynamic" (see codegen's genMakeCall for the runtime cap>=n check this
// implies, since sema can't reject a bad runtime relationship between two
// arbitrary expressions at compile time).
func (c *checker) checkMakeCall(n ast.NodeIndex, args []ast.NodeIndex) Type {
	if len(args) < 2 || len(args) > 3 {
		c.errorAtNodes(args, n, "make requires 2 or 3 arguments, got %d", len(args))
		if len(args) > 0 {
			c.typeFromNode(args[0])
			for _, a := range args[1:] {
				c.checkValueExpr(a)
			}
		}
		return invalidType
	}

	typeNode := args[0]
	target := c.typeFromNode(typeNode)
	if target.IsInvalid() {
		for _, a := range args[1:] {
			c.checkValueExpr(a)
		}
		return invalidType
	}
	if target.Kind != TypeArray || !target.Dynamic {
		c.errorAt(typeNode, "make requires a dynamic array type ([]T), got %s", target)
		for _, a := range args[1:] {
			c.checkValueExpr(a)
		}
		return invalidType
	}

	c.checkMakeSizeArg(args[1])
	if len(args) == 3 {
		c.checkMakeSizeArg(args[2])
	}
	return target
}

// checkMakeSizeArg type-checks one of make's own n/cap arguments: any
// integer type, with an untyped constant (a bare literal) defaulting to int
// exactly like any other value context with no declared type to adapt to
// (defaultIfUntyped) - there's no int-width restriction on this beyond
// "integer", matching every other int-typed position in this language.
func (c *checker) checkMakeSizeArg(node ast.NodeIndex) {
	t := c.defaultIfUntyped(node, c.checkValueExpr(node))
	if t.IsInvalid() {
		return
	}
	if t.Kind != TypeI32 {
		c.errorAt(node, "make argument must be int, got %s", t)
	}
}

// checkAppendCall type-checks `append(slice, elem)` - scoped to exactly one
// element per call this round (see LANGUAGE.md's "Dynamic arrays" section:
// this language has no variadic functions to build Go's full
// `append(s, e1, e2, ...)` form on top of; appending several elements is
// just `s = append(s, a); s = append(s, b)`).
func (c *checker) checkAppendCall(n ast.NodeIndex, args []ast.NodeIndex) Type {
	if len(args) != 2 {
		c.errorAtNodes(args, n, "append requires exactly 2 arguments (a slice and one element), got %d", len(args))
		for _, a := range args {
			c.checkValueExpr(a)
		}
		return invalidType
	}

	sliceType := c.checkValueExpr(args[0])
	if sliceType.IsInvalid() {
		c.checkValueExpr(args[1])
		return invalidType
	}
	if sliceType.Kind != TypeArray || !sliceType.Dynamic {
		c.errorAt(args[0], "append requires a dynamic array ([]T), got %s", sliceType)
		c.checkValueExpr(args[1])
		return invalidType
	}

	elemType := c.checkValueExpr(args[1])
	c.checkAssignable(args[1], *sliceType.Elem, elemType, "append argument 2")
	return sliceType
}

// checkLenCall type-checks `len(x)` - a dynamic array's runtime length, a
// fixed-size array's compile-time-known size, or a string's runtime length
// (see LANGUAGE.md's "Dynamic arrays" section) - matching Go's own `len`
// working across all three. Not meaningful for anything else (a struct,
// numeric type, or bool), rejected with a clear diagnostic.
func (c *checker) checkLenCall(n ast.NodeIndex, args []ast.NodeIndex) Type {
	if len(args) != 1 {
		c.errorAtNodes(args, n, "len takes exactly 1 argument, got %d", len(args))
		for _, a := range args {
			c.checkValueExpr(a)
		}
		return invalidType
	}
	t := c.checkValueExpr(args[0])
	if t.IsInvalid() {
		return invalidType
	}
	if t.Kind == TypeArray || t.Kind == TypeString {
		return i32Type
	}
	c.errorAt(args[0], "len is not defined for %s", t)
	return invalidType
}

// checkArgsCall type-checks `args()` - the predeclared builtin returning the
// program's own command-line arguments as a []string (see LANGUAGE.md's "The
// args() builtin" section). Takes no arguments at all, unlike every other
// predeclared builtin here - there's nothing to type-check against, so any
// argument at all is a real error. Always returns the same []string type
// (Elem always &stringType - see codegen's genArgsCall for the runtime value
// this actually returns, marshaled once at program startup, not per call).
func (c *checker) checkArgsCall(n ast.NodeIndex, args []ast.NodeIndex) Type {
	if len(args) != 0 {
		c.errorAtNodes(args, n, "args takes no arguments, got %d", len(args))
		for _, a := range args {
			c.checkValueExpr(a)
		}
	}
	return Type{Kind: TypeArray, Dynamic: true, Elem: &stringType}
}

// checkConstructorCall recognizes and type-checks `Name(args)` where Name
// resolves to a struct type with at least one declared constructor (see
// LANGUAGE.md's "Constructors" section) - callee may be a plain Ident (a
// same-package struct type) or a MemberExpr (a package-qualified one,
// `pkg.Point(args)` - resolved to the struct's own Symbol already, during
// Resolve's resolvePackageMemberExpr, exactly like `pkg.SomeFunc` or
// `pkg.SomeVar` - see resolve.go). A struct with *zero* declared
// constructors is deliberately left unclaimed here (ok=false) - it falls
// through to checkConversionCall's existing handling completely unchanged,
// which is exactly the pre-existing "not a numeric conversion target"
// diagnostic a bare struct-type call already produced before this feature
// existed (see LANGUAGE.md: this feature only adds a new legal case, it
// doesn't touch the zero-constructor case at all).
//
// Once a match is found, the selected constructor's own Symbol (not just the
// struct's) is recorded over callee's own Info.Refs entry - codegen needs to
// know exactly *which* constructor a call resolved to, not just that the
// callee names a struct with some constructor, the same reason an ordinary
// method call's callee gets its own specific Symbol recorded (resolveMember)
// rather than merely "this is a MemberExpr naming a struct value".
func (c *checker) checkConstructorCall(n, callee ast.NodeIndex, args []ast.NodeIndex) (Type, bool) {
	switch c.tree.Nodes[callee].Kind {
	case enums.NodeKinds.Ident, enums.NodeKinds.MemberExpr:
	default:
		return invalidType, false
	}
	sym, ok := c.info.Refs[callee]
	if !ok || sym.Kind != SymStruct || sym.StructInfo == nil || len(sym.StructInfo.Constructors) == 0 {
		return invalidType, false
	}

	info := sym.StructInfo
	ctorSym, ok := info.Constructors[len(args)]
	if !ok {
		c.errorAtNodes(args, n, "%s has no constructor taking %d argument(s)", info.Symbol.Name, len(args))
		for _, a := range args {
			c.checkValueExpr(a)
		}
		c.info.Types[n] = invalidType
		return invalidType, true
	}

	// The constructor may be declared in a different file - or, for a
	// package-qualified call, a different package entirely - than this call
	// site (see LANGUAGE.md's "Multi-file packages" and "Imports" sections).
	restore := c.pushTree(ctorSym.Tree)
	sig := c.constructorSigForDecl(ctorSym.Decl)
	restore()

	for i, a := range args {
		at := c.checkValueExpr(a)
		if c.checkAssignable(a, sig.Params[i], at, fmt.Sprintf("argument %d", i+1)) {
			c.checkNoIllegalCopy(a, sig.Params[i], true, fmt.Sprintf("argument %d", i+1))
		}
	}

	c.info.Refs[callee] = ctorSym
	target := Type{Kind: TypeStruct, Struct: info}
	c.info.Types[n] = target
	return target, true
}

// checkConversionCall recognizes and type-checks `T(x)` - an explicit
// conversion, not a call - the moment callee is a plain Ident whose
// Info.Refs resolution (already populated by Resolve's ordinary lexical
// lookup - resolveExpr's CallExpr case resolves every child, callee
// included, exactly like any other identifier) is a type symbol
// (SymBuiltinType or SymStruct), not a function. This reuses the CallExpr
// grammar entirely - `i64(x)` parses identically to a function call `f(x)`;
// no parser changes were needed for this feature (see parser/expr.go's
// parseCallExpr).
//
// Scoped to numeric-to-numeric conversions only (see AGENTS.md's "Explicit
// conversions" section) - string/struct/array/bool conversions aren't
// meaningfully "conversions" here and remain unsupported, reported as such
// rather than falling through to funcSigForCall's "not callable" wording
// (which would be a confusing message for `Point(x)` - the real problem is
// that Point isn't a numeric conversion target, not that it isn't callable).
//
// Returns ok=false only for a plain function/method call (an ordinary
// SymFunc callee, or a MemberExpr) - checkCallExpr falls through to its
// normal handling in that case. Every other path (right argument count and
// numeric types, wrong argument count, non-numeric argument/target) returns
// ok=true along with the Type checkCallExpr should use directly, and records
// that same Type as n's own info.Types entry - see AGENTS.md's "Explicit
// conversions" section for codegen's other half of this (recognizing the
// exact same CallExpr via Info.Refs, no separate mechanism).
func (c *checker) checkConversionCall(n, callee ast.NodeIndex, args []ast.NodeIndex) (Type, bool) {
	if c.tree.Nodes[callee].Kind != enums.NodeKinds.Ident {
		return invalidType, false
	}
	sym, ok := c.info.Refs[callee]
	if !ok || (sym.Kind != SymBuiltinType && sym.Kind != SymStruct) {
		return invalidType, false
	}

	target := c.typeFromNode(callee)
	if len(args) != 1 {
		c.errorAtNodes(args, n, "conversion to %s requires exactly one argument, got %d", target, len(args))
		for _, a := range args {
			c.checkValueExpr(a)
		}
		c.info.Types[n] = invalidType
		return invalidType, true
	}

	argType := c.checkValueExpr(args[0])
	if target.IsInvalid() || argType.IsInvalid() {
		c.info.Types[n] = invalidType
		return invalidType, true
	}
	if !target.IsNumeric() || !argType.IsNumeric() {
		c.errorAt(n, "cannot convert %s to %s", argType, target)
		c.info.Types[n] = invalidType
		return invalidType, true
	}

	// An untyped argument (a bare literal, or an expression built entirely
	// from them) defaults first - an explicit conversion is itself the
	// concrete context that resolves it, but always via a real, uniform
	// from-type-to-type lowering in codegen (sext/trunc/sitofp/fptosi/
	// fpext/fptrunc) rather than special-casing "the argument happened to
	// be a literal" - so `i64(5)` is treated exactly like `i64(someI32)`.
	if argType.IsUntyped() {
		def := c.defaultUntyped(argType)
		c.retypeUntyped(args[0], def)
	}

	c.info.Types[n] = target
	return target, true
}

// checkNewExpr type-checks `new T(args)`/`new T{...}` (see LANGUAGE.md's
// "Pointers" section): inner (a CallExpr or CompositeLit - see parseNewExpr)
// is checked exactly as it would be unwrapped, reusing checkCallExpr/
// checkCompositeLit's own machinery completely unchanged - `new` itself only
// adds one thing, wrapping the result in a pointer to a real heap allocation
// instead of a stack/inline one (see codegen's genNewExpr for the malloc
// this produces).
//
// The CallExpr case is further restricted to an actual constructor call
// (Info.Refs on its own callee resolving to SymConstructor, exactly the way
// checkConstructorCall itself marks one) - checkCallExpr's ordinary call/
// conversion-call fallbacks still run first (so an undefined callee, wrong
// argument count, etc. all still get their own correct diagnostics), but
// `new someFunc(...)`/`new i64(5)` are rejected once the call shape itself
// turns out not to be a constructor call, same as any other misuse of
// `new`.
func (c *checker) checkNewExpr(n ast.NodeIndex) Type {
	inner := c.tree.Child(n, 0)
	switch c.tree.Nodes[inner].Kind {
	case enums.NodeKinds.CompositeLit:
		// Routed through checkExpr (not checkCompositeLit directly) so
		// inner's own info.Types entry actually gets memoized - checkExpr,
		// not checkCompositeLit itself, is what does that (see checkExpr's
		// doc comment); codegen's genNewExpr reads it back via
		// g.info.Types[inner]'s sibling lookups the same way any other
		// checked CompositeLit node would.
		t := c.checkExpr(inner)
		if t.IsInvalid() {
			return invalidType
		}
		return Type{
			Kind: TypePointer,
			Elem: &t,
		}
	case enums.NodeKinds.CallExpr:
		t := c.checkExpr(inner)
		calleeNode := c.tree.Child(inner, 0)
		sym, ok := c.info.Refs[calleeNode]
		if t.IsInvalid() || !ok || sym.Kind != SymConstructor {
			c.errorAt(n, "new requires a struct constructor call or composite literal")
			return invalidType
		}
		return Type{
			Kind: TypePointer,
			Elem: &t,
		}
	default:
		// Still check inner so an undefined identifier/etc. inside it gets
		// its own diagnostic too, rather than being silently skipped.
		c.checkValueExpr(inner)
		c.errorAt(n, "new requires a struct constructor call or composite literal")
		return invalidType
	}
}

// funcSigForCall resolves a CallExpr's callee to the funcSignature its
// arguments must be checked against - the same argument-count/type checking
// applies whether the call turns out to be direct or indirect (see
// LANGUAGE.md's "First-class functions" section; codegen's own dispatch -
// isDirectFuncCall, codegen/expr.go, documented in CODEGEN.md - mirrors this
// exact distinction to choose a plain, direct `call` versus extracting a
// fat pointer and calling through it, since only the calling convention
// differs, never the type-checking):
//
//   - A direct call - a plain Ident resolving (via Info.Refs) to an actual
//     declared free function (SymFunc with a real declaration, i.e.
//     Decl != InvalidNode - this excludes the predeclared `print` builtin,
//     though isPrintCall already intercepts that case earlier and never
//     reaches this function at all), or a MemberExpr naming a method
//     (methodSigForCallee) - gets no info.Types entry for callee itself:
//     the callee names a fixed declaration, not a value with its own Type,
//     same as before this round. The declaration itself may be a FuncDecl or
//     an ExternFuncDecl (see LANGUAGE.md's "External functions (FFI)"
//     section) - funcSigForDecl dispatches on which, so this call site (and
//     codegen's identical isDirectFuncCall/genFuncCall) needs no awareness of
//     the distinction at all: a call to an extern-backed function type-checks
//     and lowers exactly like a call to an ordinary one.
//   - Anything else that type-checks as callable - a function-typed
//     variable/parameter, an ordinary (non-method) struct field of function
//     type (`cb.fn(5)` - methodSigForCallee's isField result), or any other
//     expression (e.g. a call whose own result is itself a function) - is an
//     *indirect* call: callee is checked as an ordinary value expression (so
//     it does get a real info.Types entry - codegen needs it to actually
//     evaluate the function value before calling through it) and its Type
//     must be TypeFunc.
func (c *checker) funcSigForCall(callee ast.NodeIndex) (funcSignature, bool) {
	switch c.tree.Nodes[callee].Kind {
	case enums.NodeKinds.MemberExpr:
		sig, ok, isField := c.methodSigForCallee(callee)
		if !isField {
			return sig, ok
		}
		// callee names an ordinary struct field, not a method or package
		// function - falls through to the same indirect-call check below
		// that a func-typed Ident/parameter already gets (a func-typed field
		// is called exactly the same way - see LANGUAGE.md's "First-class
		// functions" section). methodSigForCallee's own resolveMember call
		// already fully resolved callee (recorded into info.Refs) -
		// resolveMember memoizes on that, so checkValueExpr below re-resolves
		// nothing; it only re-reads the cached result to also fill in
		// callee's own info.Types entry.
	case enums.NodeKinds.Ident:
		if sym, ok := c.info.Refs[callee]; ok && sym.Kind == SymFunc && sym.Decl != ast.InvalidNode {
			// sym's FuncDecl may live in a different file than this call
			// site (see LANGUAGE.md's "Multi-file packages" section).
			restore := c.pushTree(sym.Tree)
			sig := c.funcSigForDecl(sym.Decl)
			restore()
			return sig, true
		}
	}

	t := c.checkValueExpr(callee)
	if t.IsInvalid() {
		return funcSignature{}, false
	}
	if t.Kind != TypeFunc {
		switch c.tree.Nodes[callee].Kind {
		case enums.NodeKinds.Ident, enums.NodeKinds.MemberExpr:
			c.errorAt(callee, "cannot call %s (%s is not a function)", c.tree.Text(callee), t)
		default:
			c.errorAt(callee, "cannot call this expression (not a function)")
		}
		return funcSignature{}, false
	}
	return funcSignature{Params: t.Params, Return: *t.Return}, true
}

// methodSigForCallee resolves a call's callee when it's a MemberExpr - an
// ordinary method call (`p.move()`), a package-qualified function call
// (`mathutils.Add()`, see resolve.go's resolvePackageMemberExpr), or an
// ordinary struct field (isField=true) - all three go through resolveMember,
// which already tells them apart internally.
//
// A field isn't necessarily callable itself - funcSigForCall's own indirect-
// call fallback decides that, exactly mirroring its Ident-callee fallback,
// once it knows the field's actual Type (see LANGUAGE.md's "First-class
// functions" section: a func-typed field, `cb.fn(5)`, is a valid indirect
// call the same way a func-typed Ident/parameter already is) - so this
// reports a real diagnostic only for the definitively-terminal case (a
// package member that isn't a function at all); a plain struct field just
// reports isField=true and leaves the "is it actually callable" verdict to
// the caller. This intentionally never touches method values (`p.move`
// referenced uncalled) - that's a different node shape entirely
// (checkMemberExpr's own value-position check, which still rejects it
// exactly as before) and is out of scope here.
func (c *checker) methodSigForCallee(callee ast.NodeIndex) (sig funcSignature, ok bool, isField bool) {
	sym, resolved := c.resolveMember(callee)
	if !resolved {
		return funcSignature{}, false, false
	}
	if sym.Kind != SymFunc {
		if c.memberObjectIsPackage(callee) {
			c.errorAt(callee, "cannot call %s (%s is not a function)", c.tree.Text(callee), sym.Kind)
			return funcSignature{}, false, false
		}
		return funcSignature{}, false, true
	}
	// The method/function may be declared in a different file - or, for a
	// package-qualified call, a different package entirely - than this call
	// site (see LANGUAGE.md's "Multi-file packages" and "Imports" sections).
	restore := c.pushTree(sym.Tree)
	sig = c.funcSigForDecl(sym.Decl)
	restore()
	return sig, true, false
}

// checkCompositeLit types a composite literal (`Point{...}` or `[N]T{...}`)
// against its target type, and - for the struct case - resolves each keyed
// element's field name (the work Resolve deferred; see resolveCompositeLit
// in resolve.go).
func (c *checker) checkCompositeLit(n ast.NodeIndex) Type {
	typeNode, elems := c.tree.CompositeLitElems(n)

	target := c.typeFromNode(typeNode)
	if target.IsInvalid() {
		for _, e := range elems {
			c.checkCompositeLitElemFallback(e)
		}
		return invalidType
	}

	switch target.Kind {
	case TypeStruct:
		c.checkStructCompositeLit(n, target, elems)
	case TypeArray:
		c.checkArrayCompositeLit(n, target, elems)
	default:
		c.errorAt(n, "%s is not a valid composite literal type", target)
		for _, e := range elems {
			c.checkCompositeLitElemFallback(e)
		}
		return invalidType
	}
	return target
}

// checkCompositeLitElemFallback still type-checks elem's value (so nested
// errors surface and Types entries exist) when the literal's own target
// type couldn't be determined - there's nothing to validate elem against,
// but the value expression inside it is still real code worth checking.
func (c *checker) checkCompositeLitElemFallback(elem ast.NodeIndex) {
	if c.tree.IsKeyedElement(elem) {
		c.checkValueExpr(c.tree.Child(elem, 1))
		return
	}
	c.checkValueExpr(elem)
}

// checkStructCompositeLit enforces: every element must be the same kind
// (all positional, or all keyed - never a mix, the standard rule); a
// positional literal must supply exactly one value per field, in
// declaration order; a keyed literal's names must be real fields, and no
// field may be specified twice.
func (c *checker) checkStructCompositeLit(n ast.NodeIndex, target Type, elems []ast.NodeIndex) {
	info := target.Struct

	// A fully-empty literal (`T{}`) is unconditionally valid and simply
	// zero-fills every field - it vacuously satisfies both the positional
	// and keyed interpretations (there's nothing to check either way), so
	// it skips both the cross-package-unexported check and the arity check
	// below entirely, same as Go's own "zero value via T{}" idiom. This
	// holds even across a package boundary with unexported fields present:
	// supplying zero values isn't "setting" anything the way a real
	// positional literal with actual values would be, unlike the (still
	// correct) rule just below that rejects a positional literal with real
	// values against a struct with any unexported field.
	if len(elems) == 0 {
		return
	}

	// The struct's own Field nodes live in its declaring file, which may
	// differ from n's own file (a composite literal naming a struct
	// declared elsewhere in the package - see LANGUAGE.md's "Multi-file
	// packages" section).
	restore := c.pushTree(info.Symbol.Tree)
	fields := c.tree.StructFields(info.Symbol.Decl) // Field nodes, declaration order
	restore()
	keyed := c.tree.IsKeyedElement(elems[0])

	// Go's own stricter rule for a *positional* literal constructing a
	// struct from another package: reject it if the struct has ANY
	// unexported field, even one this literal never mentions by name and
	// even if every value actually supplied is itself exported - there's no
	// way to positionally "skip" a field, so allowing this would silently
	// let outside code set a private field's value. A keyed literal has no
	// such problem - checkKeyedStructElem's own export check already
	// rejects explicitly naming an unexported field, and simply omitting one
	// leaves it untouched - so only the positional form is restricted here.
	// Same-package construction is never restricted either way (export only
	// ever matters across a package boundary - see
	// crossPackageStructConstruction).
	if !keyed && c.crossPackageStructConstruction(info) {
		if name, ok := firstUnexportedField(info.Symbol.Tree, info, fields); ok {
			c.errorAt(n, "cannot use a positional literal to construct %s from another package: field %s is unexported", info.Symbol.Name, name)
		}
	}

	seen := make(map[string]bool)

	for i, elem := range elems {
		isKV := c.tree.IsKeyedElement(elem)
		if isKV != keyed {
			c.errorAt(elem, "cannot mix keyed and positional elements in a composite literal")
			c.checkCompositeLitElemFallback(elem)
			continue
		}
		if keyed {
			c.checkKeyedStructElem(elem, info, seen)
		} else {
			c.checkPositionalStructElem(elem, i, info, fields)
		}
	}

	if !keyed && len(elems) != len(fields) {
		c.errorAtNodes(elems, n, "%s composite literal has %d fields, want %d", info.Symbol.Name, len(elems), len(fields))
	}
}

// checkPositionalStructElem checks one positional composite-literal element.
// elem is always in the literal's own (current) file; fields[i] belongs to
// info's own declaring file (see checkStructCompositeLit) - checkValueExpr
// runs against the caller's current tree first, and only the narrow
// fieldType/fieldName lookup below switches into the struct's own file.
func (c *checker) checkPositionalStructElem(elem ast.NodeIndex, i int, info *StructInfo, fields []ast.NodeIndex) {
	vt := c.checkValueExpr(elem)
	if i >= len(fields) {
		return // count mismatch already reported by the caller
	}
	restore := c.pushTree(info.Symbol.Tree)
	fieldType := c.typeFromNode(c.tree.Child(fields[i], 1))
	fieldName := c.tree.Text(c.tree.Child(fields[i], 0))
	restore()
	if c.checkAssignable(elem, fieldType, vt, fmt.Sprintf("field %s", fieldName)) {
		c.checkNoIllegalCopy(elem, fieldType, true, fmt.Sprintf("field %s", fieldName))
	}
}

func (c *checker) checkKeyedStructElem(elem ast.NodeIndex, info *StructInfo, seen map[string]bool) {
	key := c.tree.Child(elem, 0)
	value := c.tree.Child(elem, 1)
	vt := c.checkValueExpr(value)

	if c.tree.Nodes[key].Kind != enums.NodeKinds.Ident {
		c.errorAt(key, "field name must be an identifier")
		return
	}
	name := c.tree.Text(key)
	fieldSym, ok := info.Fields[name]
	if !ok {
		c.errorAt(key, "%s has no field %s", info.Symbol.Name, name)
		return
	}
	c.info.Refs[key] = fieldSym
	// A keyed literal explicitly naming an unexported field is ordinary
	// unexported-access, from another package - the same restriction any
	// other struct-value field access enforces (checkExportedAccess). This
	// is what keeps a keyed literal safe without checkStructCompositeLit's
	// stricter positional-literal rule: simply omitting a field is fine
	// (nothing is implicitly set), but naming one explicitly is not.
	if !c.checkExportedAccess(key, fieldSym) {
		return
	}
	if seen[name] {
		c.errorAt(key, "field %s specified twice", name)
	}
	seen[name] = true

	// fieldSym.Decl is a Field node in the struct's own declaring file,
	// which may differ from elem's own file - see checkStructCompositeLit.
	restore := c.pushTree(fieldSym.Tree)
	fieldType := c.typeFromNode(c.tree.Child(fieldSym.Decl, 1))
	restore()
	if c.checkAssignable(value, fieldType, vt, fmt.Sprintf("field %s", name)) {
		c.checkNoIllegalCopy(value, fieldType, true, fmt.Sprintf("field %s", name))
	}
}

// checkArrayCompositeLit only supports positional elements (`[N]T{a, b}`) -
// Go-style index-keyed array literals (`[5]int{2: 9}`) aren't part of this
// language's grammar's semantics yet (see BLOCKERS.md).
func (c *checker) checkArrayCompositeLit(n ast.NodeIndex, target Type, elems []ast.NodeIndex) {
	for _, elem := range elems {
		if c.tree.IsKeyedElement(elem) {
			c.errorAt(elem, "keyed elements are not supported in array literals")
			c.checkValueExpr(c.tree.Child(elem, 1))
			continue
		}
		vt := c.checkValueExpr(elem)
		if c.checkAssignable(elem, *target.Elem, vt, "array element") {
			c.checkNoIllegalCopy(elem, *target.Elem, true, "array element")
		}
	}
	if !target.Dynamic && int64(len(elems)) != target.Size {
		c.errorAt(n, "array literal has %d elements, want %d", len(elems), target.Size)
	}
}

// isTerminatingStmt reports whether n is a "terminating statement" in the
// sense of Go's own spec (the "Terminating statements" section), adapted
// down to this language's smaller statement grammar - no goto/labels,
// switch, select, or panic exist here, so only the handful of cases that do
// apply are implemented:
//
//   - a `return` always terminates.
//   - a bare `for {}` (no cond clause) terminates, unless its body contains
//     a `break` that targets *it* directly - a nested loop's own break
//     doesn't count (see forHasOwnBreak). A for with a cond clause never
//     terminates: the condition can always be false and fall through.
//   - an `if`/`else` terminates only when both branches are present and
//     both themselves terminate. An `if` with no `else` - including the
//     one-line `if cond: stmt` form, which is grammatically identical (see
//     ast.Node's IfStmt doc comment) - can never terminate: there's always
//     a path where the condition is false and control falls straight
//     through past it.
//   - a Block terminates iff its own last statement does (an empty block
//     never does - nothing in it could have).
//
// See checkFuncDecl's "missing return" check (the only caller) and
// AGENTS.md's "Missing return" section for the full rule and examples.
func isTerminatingStmt(tree *ast.Tree, n ast.NodeIndex) bool {
	if n == ast.InvalidNode {
		return false
	}
	switch tree.Nodes[n].Kind {
	case enums.NodeKinds.ReturnStmt:
		return true
	case enums.NodeKinds.ForStmt:
		cond := tree.Child(n, 1)
		body := tree.Child(n, 3)
		return cond == ast.InvalidNode && !forHasOwnBreak(tree, body)
	case enums.NodeKinds.IfStmt:
		elseBranch := tree.Child(n, 2)
		if elseBranch == ast.InvalidNode {
			return false
		}
		return isTerminatingStmt(tree, tree.Child(n, 1)) && isTerminatingStmt(tree, elseBranch)
	case enums.NodeKinds.Block:
		stmts := tree.Children(n)
		if len(stmts) == 0 {
			return false
		}
		return isTerminatingStmt(tree, stmts[len(stmts)-1])
	default:
		return false
	}
}

// forHasOwnBreak reports whether n - a ForStmt's own body, or (recursively)
// something nested inside it - contains a `break` that would target *that*
// loop directly, as opposed to some inner loop's own break. Only descends
// into the statement kinds that can nest another statement without crossing
// a loop boundary in this grammar: Block, and IfStmt's then/else. A nested
// ForStmt's body is deliberately never descended into - a break inside it
// targets that inner loop, not the one isTerminatingStmt is asking about.
func forHasOwnBreak(tree *ast.Tree, n ast.NodeIndex) bool {
	if n == ast.InvalidNode {
		return false
	}
	switch tree.Nodes[n].Kind {
	case enums.NodeKinds.BreakStmt:
		return true
	case enums.NodeKinds.Block:
		for _, stmt := range tree.Children(n) {
			if forHasOwnBreak(tree, stmt) {
				return true
			}
		}
		return false
	case enums.NodeKinds.IfStmt:
		if forHasOwnBreak(tree, tree.Child(n, 1)) {
			return true
		}
		return forHasOwnBreak(tree, tree.Child(n, 2))
	default:
		// ForStmt (its break targets the inner loop, not this one) and
		// every other non-nesting statement kind: nothing to find here.
		return false
	}
}
