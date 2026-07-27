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
//
// Variadic marks a declared function/method whose last parameter is `...T`.
// Params' own last entry is already the ordinary `[]T` dynamic-array Type in
// that case (computeDeclType wraps it), so nothing here duplicates the
// element type; checkCallArgs reads *Params[len(Params)-1].Elem for it.
// Always false for an indirect call's own funcSignature (built from a
// TypeFunc's Params via funcType) - a variadic function can never be
// referenced as a value in the first place (see typeOfSymbolValue), so that
// shape never arises.
type funcSignature struct {
	Params   []Type
	Return   Type
	Variadic bool
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

	// isGenerator/yieldElem describe a `yield T` generator function's own
	// body (see LANGUAGE.md's "Generator functions" section) - yieldElem is
	// T, meaningful only when isGenerator is true. A generator's hasReturn
	// stays false (an ordinary `return value` is illegal, exactly like a
	// void function's - only a bare `return` is legal, see checkReturnStmt),
	// so checkYieldStmt is the only place yieldElem is actually read.
	isGenerator bool
	yieldElem   Type

	// isAsync marks an `async func`'s own body (see LANGUAGE.md's
	// "Coroutines" section) - checkAwaitStmt's only reader. Async and
	// generator are mutually exclusive in practice (checkFuncDecl already
	// rejects an async func declaring any return type, which a `yield T`
	// return-type marker is one), but nothing here assumes that itself.
	isAsync bool
}

// moveState is one function/constructor/destructor/lambda body's own `move`
// flow-tracking state (see LANGUAGE.md's "Destructors" section's "move"
// subsection) - reset per body exactly like enclosingFunc itself (see
// checker.enterFuncBody).
//
// moved is the set of symbols moved-from on the path reached so far; absence
// means "definitely not moved yet". There is no third "maybe moved" state
// held here - checkIfStmt/checkMatchDispatch instead reject outright the
// moment two converging paths disagree (see their own doc comments), so by
// construction moved never needs to represent an ambiguous symbol at all.
//
// declLoopDepth records, for a symbol declared inside at least one loop
// body, the checker.loopDepth in effect at its own declaration - missing
// means "declared outside every loop" (loopDepth 0), which is also the
// correct answer for every symbol never recorded here (a function parameter,
// or any local declared outside a loop), so only an in-loop declaration ever
// needs an actual entry. checkMoveExpr rejects moving a symbol whose
// declLoopDepth is less than the CURRENT loopDepth: declared outside (or in
// a shallower enclosing loop than) the loop the move itself is inside, which
// could run the move more than once, moving-from an already-moved value on a
// later iteration (see DECISIONS.md's dated entry for why this is rejected
// outright rather than reconciled via a per-iteration fixed point).
type moveState struct {
	moved         map[*Symbol]bool
	declLoopDepth map[*Symbol]int
}

// matchExprCheckCtx is one live expression-mode match's own running result
// type - see checker.matchExprStack's own doc comment for why this needs a
// real stack rather than a bare counter. resultTypeSet distinguishes "no
// yield checked yet" from "a yield already fixed resultType" - resultType's
// own zero value (Type{}) is otherwise indistinguishable from a genuinely
// checked-but-invalid type.
type matchExprCheckCtx struct {
	resultType    Type
	resultTypeSet bool
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

	// move is the current function/constructor/destructor/lambda body's own
	// move-tracking state (see moveState's own doc comment) - reset alongside
	// curFunc at every one of its same save/restore sites (enterFuncBody).
	move *moveState

	// loopDepth counts how many enclosing ForStmt bodies checkStmt is
	// currently inside - incremented/decremented around checkForStmt's own
	// checkBlock call, mirroring how curFunc tracks "am I inside a
	// function" for checkReturnStmt. A BreakStmt/ContinueStmt is only
	// valid while this is > 0 - see checkBreakOrContinue. It doesn't need
	// to distinguish *which* enclosing loop (that's codegen's loopStack's
	// job, once this pass has already guaranteed one exists at all).
	loopDepth int

	// matchExprStack is the expression-mode-match counterpart to loopDepth -
	// a real stack, not just a counter, since a `yield` needs live access to
	// its own enclosing match expression's own running result type (the
	// first yield seen anywhere in the whole match fixes it; every
	// subsequent one, in any arm, must unify against it - see
	// checkYieldStmt), not just a yes/no "am I nested deep enough" answer.
	// Pushed once for the whole arms-checking pass of an expression-mode
	// match (checkMatchExprStmt), popped once after every arm has been
	// checked - a nested match expression (a `yield match other {...}`'s
	// own wrapped match) pushes its own fresh frame on top, so its own
	// yields unify against ITS OWN frame, never leaking into the enclosing
	// one's. A plain statement-position match (checkMatchStmt) never
	// touches this at all - it has no result type to unify, and its own
	// arm bodies are checked via plain checkBlock, exactly as before this
	// round.
	matchExprStack []*matchExprCheckCtx

	// inGeneratorRangeBody is > 0 while checking a generator-consuming
	// range-for's own body (checkRangeForStmt's TypeGenerator case) - a
	// `return` reached directly inside it (not nested inside a further
	// FuncLit, which gets its own fresh curFunc/depth entirely - see
	// checkFuncLit) is rejected: that body becomes a genuinely separate,
	// independent callback function at codegen time (see CODEGEN.md's
	// "Generator functions" section), which has no way to make the real
	// enclosing function return early the way an ordinary nested return can
	// - true non-local return is a real, separate feature this round
	// doesn't build (see checkReturnStmt).
	inGeneratorRangeBody int

	// computingCopyable is structCopyable's own cycle guard - a struct's
	// Copyable can depend (transitively, through a field) on another
	// struct's, possibly not computed yet (see structCopyable) - mirroring
	// computingDecl's identical role for declType, just keyed by *StructInfo
	// instead of nodeRef, since copyability is a fact about the struct type
	// itself, not about any one declaration node.
	computingCopyable map[*StructInfo]bool

	// computingEnumCopyable is enumCopyable's own identical cycle guard, one
	// type kind over - see structCopyable/computingCopyable's own doc
	// comments.
	computingEnumCopyable map[*EnumInfo]bool

	// pending holds each generic specialization's deferred body check (see
	// enqueueBody, generics.go) - drained once no ordinary body is in
	// progress, since curFunc/move/loopDepth above all belong to exactly one
	// body at a time. instantiations counts how many have been created at all,
	// against maxInstantiations.
	pending        []func()
	instantiations int

	// memberFailed records the MemberExpr nodes resolveMember already
	// rejected - Info.Refs only memoizes a success, so without this a second
	// caller asking about the same node re-reports the identical diagnostic.
	memberFailed map[nodeRef]bool
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
		infos:                 infos,
		allDiags:              make(map[*ast.Tree]*diag.Bag, len(trees)),
		treePackage:           treePackage,
		declTypes:             make(map[nodeRef]Type),
		computingDecl:         make(map[nodeRef]bool),
		typeNodeCache:         make(map[nodeRef]Type),
		funcSigs:              make(map[nodeRef]funcSignature),
		computingCopyable:     make(map[*StructInfo]bool),
		computingEnumCopyable: make(map[*EnumInfo]bool),
		memberFailed:          make(map[nodeRef]bool),
	}
	for _, tree := range trees {
		c.allDiags[tree] = diag.NewBag()
		infos[tree].Types = make(map[ast.NodeIndex]Type)
	}

	c.checkPackage(trees)
	c.drainPending()

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
			if isGenericDecl(tree, c.info.Generics, decl) {
				continue
			}
			c.checkStructDecl(decl)
		}
		for decl := range tree.TopLevelDeclsOfKind(enums.NodeKinds.EnumDecl) {
			c.checkEnumDecl(decl)
		}
	}
	for _, tree := range trees {
		c.enter(tree)
		for _, decl := range tree.Children(tree.Root) {
			// A generic template is never checked as written - only its
			// specializations are (see generics.go).
			if isGenericDecl(tree, c.info.Generics, decl) {
				c.checkMainNotGeneric(decl)
				continue
			}
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
	for op := range c.tree.StructOperators(decl) {
		c.checkOperatorDecl(op)
	}
	c.checkOperatorOverloadDuplicates(decl)
}

// checkOperatorOverloadDuplicates re-checks decl's own binary operator
// overloads (see LANGUAGE.md's "Operator overloading" section) for a
// duplicate the Resolve-time textual check (declareOperator's own
// ParamTypeText comparison, resolve.go) cannot see: two overloads of the
// same token whose declared parameter types are only spelled differently
// but are the exact same real Type - `int` and `i32` being the same
// underlying Type is the concrete case (see LANGUAGE.md's "Numeric types"
// section) - since Resolve runs before any Type is computable at all. This
// runs once every real declared parameter Type is available (operatorSigForDecl,
// already computed and cached by checkOperatorDecl's own pass just above),
// comparing every pair sharing a token via the real Type.Equal, not text.
//
// Unary overloads need no such re-check: a duplicate there has no parameter
// type to even be ambiguous about (declareOperator's own arity-only
// "already has a unary operator" check is already exact).
func (c *checker) checkOperatorOverloadDuplicates(decl ast.NodeIndex) {
	type binaryOverload struct {
		typ Type
		op  ast.NodeIndex
	}
	byToken := make(map[string][]binaryOverload)
	for op := range c.tree.StructOperators(decl) {
		paramNodes := c.tree.Children(c.tree.OperatorParamList(op))
		if len(paramNodes) != 1 {
			continue
		}
		tok := c.tree.Text(op)
		sig := c.operatorSigForDecl(op)
		byToken[tok] = append(byToken[tok], binaryOverload{typ: sig.Params[0], op: op})
	}
	for _, overloads := range byToken {
		for i := 1; i < len(overloads); i++ {
			for j := 0; j < i; j++ {
				if !overloads[i].typ.Equal(overloads[j].typ) {
					continue
				}
				structName := c.info.Refs[overloads[i].op].StructInfo.Symbol.Name
				c.errorAt(overloads[i].op, "struct %s already has an operator %s overload taking %s", structName, c.tree.Text(overloads[i].op), overloads[i].typ)
				break
			}
		}
	}
}

// checkEnumDecl type-checks decl's (an EnumDecl's) own variants - populating
// each variant's own EnumVariant.Tuple/Fields (associated-data Types),
// deliberately computed here rather than by Resolve, mirroring how a struct
// field's own Type is likewise computed lazily via typeFromNode - and its
// own (at most one) destructor, mirroring checkStructDecl one type kind
// over. No constructors to check - see EnumInfo's own doc comment for why
// there's nothing to catalog there.
func (c *checker) checkEnumDecl(decl ast.NodeIndex) {
	nameNode := c.tree.Child(decl, 0)
	info, ok := c.info.Enums[c.tree.Text(nameNode)]
	if !ok {
		return
	}

	for _, variantNode := range c.tree.EnumVariants(decl) {
		variant := info.Variants[c.tree.Text(variantNode)]
		if variant == nil {
			continue // a redeclared variant name - already reported by Resolve
		}
		switch c.tree.ClassifyEnumVariant(variantNode) {
		case ast.EnumVariantTuple:
			typeNodes := c.tree.Children(variantNode)
			variant.Tuple = make([]Type, len(typeNodes))
			for i, tn := range typeNodes {
				variant.Tuple[i] = c.typeFromNode(tn)
			}
		case ast.EnumVariantStruct:
			fieldNodes := c.tree.Children(variantNode)
			variant.Fields = make([]EnumField, len(fieldNodes))
			for i, fieldNode := range fieldNodes {
				fieldNameNode := c.tree.Child(fieldNode, 0)
				fieldName := c.tree.Text(fieldNameNode)
				fieldType := c.typeFromNode(c.tree.Child(fieldNode, 1))
				fieldSym := &Symbol{
					Name:     fieldName,
					Kind:     SymField,
					Decl:     fieldNode,
					Tree:     c.tree,
					Scope:    info.Symbol.Scope,
					Exported: isExportedName(fieldName),
				}
				c.info.Refs[fieldNameNode] = fieldSym
				variant.Fields[i] = EnumField{
					Name: fieldName,
					Type: fieldType,
					Sym:  fieldSym,
				}
			}
		}
	}

	for dtor := range c.tree.EnumDestructors(decl) {
		c.checkEnumDestructorDecl(dtor)
	}
}

// enterFuncBody resets move-tracking state (moveState) for a new function/
// constructor/destructor/lambda body, returning a closure that restores the
// enclosing body's own state - the move-tracking counterpart to each of
// these call sites' own inline curFunc save/restore, called alongside it.
func (c *checker) enterFuncBody() (restore func()) {
	prev := c.move
	c.move = &moveState{
		moved:         make(map[*Symbol]bool),
		declLoopDepth: make(map[*Symbol]int),
	}
	return func() { c.move = prev }
}

// recordLocalDeclLoopDepth notes nameNode's own symbol as declared at the
// current loopDepth, if it's declared inside at least one loop body (see
// moveState.declLoopDepth's own doc comment - a no-op outside any loop,
// where the zero-value default is already the right answer). Called by
// every ordinary local-declaring construct (var/short-var-decl, a
// multi-short-var-decl name, a match pattern binding); see
// recordLoopBindingDeclLoopDepth for the one binding shape (a range-for's
// own key/value) that needs a different depth.
func (c *checker) recordLocalDeclLoopDepth(nameNode ast.NodeIndex) {
	if c.loopDepth == 0 {
		return
	}
	if sym, ok := c.info.Refs[nameNode]; ok {
		c.move.declLoopDepth[sym] = c.loopDepth
	}
}

// recordLoopBindingDeclLoopDepth is recordLocalDeclLoopDepth's counterpart
// for a range-for's own key/value binding (seedRangeBinding): checkRangeForStmt
// seeds these before incrementing loopDepth for the body, but the binding
// itself is exactly as loop-scoped (fresh every iteration) as an ordinary
// local declared directly inside the body would be - so this always records
// loopDepth+1 (the depth the body is about to run at), unconditionally,
// rather than skipping at loopDepth 0 the way recordLocalDeclLoopDepth does.
func (c *checker) recordLoopBindingDeclLoopDepth(nameNode ast.NodeIndex) {
	if sym, ok := c.info.Refs[nameNode]; ok {
		c.move.declLoopDepth[sym] = c.loopDepth + 1
	}
}

// checkEnumDestructorDecl type-checks one enum `destructor() {...}` block -
// mirroring checkDestructorDecl exactly, one type kind over.
func (c *checker) checkEnumDestructorDecl(dtor ast.NodeIndex) {
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
	restoreMove := c.enterFuncBody()
	c.checkBlock(body)
	restoreMove()
	c.curFunc = prevFunc
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
	restoreMove := c.enterFuncBody()
	c.checkBlock(body)
	restoreMove()
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
	restoreMove := c.enterFuncBody()
	c.checkBlock(body)
	restoreMove()
	c.curFunc = prevFunc
}

// checkOperatorDecl type-checks one `operator OP(param) RetType {...}`
// block's params, declared return type, and body (see LANGUAGE.md's
// "Operator overloading" section) - mirroring checkFuncDecl's own shape,
// minus the receiver/generator/async concerns a free function/method can
// have: an operator overload always declares a real return type, so
// hasReturn is always true and a missing "missing return" is checked
// exactly like an ordinary function's.
func (c *checker) checkOperatorDecl(op ast.NodeIndex) {
	sig := c.operatorSigForDecl(op)
	body := c.tree.OperatorBody(op)

	prevFunc := c.curFunc
	c.curFunc = &enclosingFunc{
		hasReturn: true,
		ret:       sig.Return,
	}
	restoreMove := c.enterFuncBody()
	c.checkBlock(body)
	restoreMove()
	if !isTerminatingStmt(c.tree, c.info, body) {
		c.errorAt(op, "missing return")
	}
	c.curFunc = prevFunc
}

// operatorSigForDecl returns op's (an OperatorDecl's) signature - its
// declared parameter type(s) and its own real declared return type, unlike
// constructorSigForDecl's synthetic "returns the struct" one - computed and
// cached on first use, reusing c.funcSigs exactly like
// constructorSigForDecl does (an OperatorDecl's own NodeIndex never
// collides with a FuncDecl's or ConstructorDecl's - see nodeRef).
func (c *checker) operatorSigForDecl(op ast.NodeIndex) funcSignature {
	key := nodeRef{c.tree, op}
	if sig, ok := c.funcSigs[key]; ok {
		return sig
	}
	sig := c.buildSigFromParamListAndReturnType(c.tree.OperatorParamList(op), c.tree.OperatorReturnType(op), nil)
	c.funcSigs[key] = sig
	return sig
}

func (c *checker) checkFuncDecl(decl ast.NodeIndex) {
	sig := c.funcSigForDecl(decl) // also checks params/return type exactly once
	c.checkMainReturnType(decl, sig)
	body := c.tree.FuncBody(decl)

	isGenerator := sig.Return.Kind == TypeGenerator
	if isGenerator && c.tree.FuncReceiver(decl) != ast.InvalidNode {
		c.errorAt(decl, "a method cannot be a generator function (yield return type)")
	}

	isAsync := c.tree.FuncIsAsync(decl)
	if isAsync && c.tree.FuncReceiver(decl) != ast.InvalidNode {
		c.errorAt(decl, "a method cannot be an async function")
	}
	if isAsync && c.tree.FuncReturnType(decl) != ast.InvalidNode {
		// Reading an async function's own final result (once done(h) is
		// true) needs llvm.coro.promise-based storage this round explicitly
		// doesn't build - see LANGUAGE.md's "Coroutines" section for the
		// full reasoning. An async func is void-only for now.
		c.errorAt(decl, "async functions cannot declare a return type yet - see LANGUAGE.md's Coroutines section")
	}

	prevFunc := c.curFunc
	c.curFunc = &enclosingFunc{
		// A generator's hasReturn stays false, exactly like a void function -
		// see enclosingFunc's own doc comment and checkReturnStmt. An async
		// func is void-only this round (see above), so hasReturn is simply
		// whatever the ordinary rule already computes for a void function.
		hasReturn:   c.tree.FuncReturnType(decl) != ast.InvalidNode && !isGenerator,
		ret:         sig.Return,
		isGenerator: isGenerator,
		isAsync:     isAsync,
	}
	if isGenerator {
		c.curFunc.yieldElem = *sig.Return.Elem
	}
	restoreMove := c.enterFuncBody()
	c.checkBlock(body)
	restoreMove()
	if c.curFunc.hasReturn && !isTerminatingStmt(c.tree, c.info, body) {
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

// checkMainNotGeneric is checkMainReturnType's counterpart for the one shape
// that never reaches it: a template is skipped by checkPackage, so without
// this `func main[T]()` produces no diagnostic at all and the driver only
// reports an unpositioned "no main function found in module".
func (c *checker) checkMainNotGeneric(decl ast.NodeIndex) {
	if c.tree.Nodes[decl].Kind != enums.NodeKinds.FuncDecl {
		return
	}
	if c.tree.FuncReceiver(decl) != ast.InvalidNode {
		return
	}
	if c.tree.Text(c.tree.FuncName(decl)) != "main" {
		return
	}
	c.errorAt(c.tree.FuncTypeParamList(decl), "main must not be generic")
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
	return c.buildSigFromParamListAndReturnType(c.tree.FuncParamList(decl), c.tree.FuncReturnType(decl), nil)
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
// own [name, paramList, returnType] shape (see ast.Node's doc comment) -
// shares computeFuncSig's own buildSigFromParamListAndReturnType logic, with
// one thing layered on top: every parameter type and the return type must
// also be a type this round's FFI mechanism can pass across a real C ABI
// boundary (checkExternType), passed through as the validate hook below - an
// ordinary FuncDecl's own signature never needed that.
func (c *checker) computeExternFuncSig(decl ast.NodeIndex) funcSignature {
	return c.buildSigFromParamListAndReturnType(
		c.tree.ExternFuncParamList(decl),
		c.tree.ExternFuncReturnType(decl),
		c.checkExternType,
	)
}

// buildSigFromParamListAndReturnType builds a funcSignature from paramList's
// own Param children (via declType) and returnTypeNode (via typeFromNode,
// defaulting to voidType when absent) - the shape computeFuncSig and
// computeExternFuncSig both need, since a FuncDecl's and an ExternFuncDecl's
// own [paramList, returnType] children are structurally identical even
// though their surrounding node shapes differ.
//
// validate, when non-nil, is called once per parameter and once for the
// return type (skipped when there's no declared return type) with that
// position's Type and a "parameter"/"return" label - computeExternFuncSig's
// own extra checkExternType restriction; computeFuncSig passes nil.
func (c *checker) buildSigFromParamListAndReturnType(paramList, returnTypeNode ast.NodeIndex, validate func(n ast.NodeIndex, t Type, what string)) funcSignature {
	paramNodes := c.tree.Children(paramList)
	params := make([]Type, len(paramNodes))
	for i, param := range paramNodes {
		t := c.declType(param)
		params[i] = t
		if validate != nil {
			validate(param, t, "parameter")
		}
	}
	variadic := len(paramNodes) > 0 && c.tree.ParamIsVariadic(paramNodes[len(paramNodes)-1])

	ret := voidType
	if returnTypeNode != ast.InvalidNode {
		ret = c.typeFromNode(returnTypeNode)
		if validate != nil {
			validate(returnTypeNode, ret, "return")
		}
	}
	return funcSignature{
		Params:   params,
		Return:   ret,
		Variadic: variadic,
	}
}

// checkExternType reports a diagnostic at n when t isn't one of the types
// this round's FFI mechanism allows crossing an extern func's signature (see
// isFFISafeType) - what names the position for the diagnostic's own wording
// ("parameter" or "return"). A TypeInvalid t is skipped silently - already
// reported by whatever produced it (an undefined type name, e.g.), so this
// would only add a redundant follow-on error, not a new root cause.
func (c *checker) checkExternType(n ast.NodeIndex, t Type, what string) {
	if t.IsInvalid() {
		return
	}
	if !c.isFFISafeType(t) {
		c.errorAt(n, "extern func %s type %s is not supported - only numeric types, bool, cstring, pointer types, and structs made entirely of FFI-safe fields can cross an extern function signature", what, t)
	}
}

// isFFISafeType reports whether t may cross an extern func signature at top
// level - a parameter or the return type (see LANGUAGE.md's "External
// functions (FFI)" section). A numeric type, bool, cstring, and a pointer
// are always safe (isFFISafeScalar); a struct is safe iff every one of its
// fields is (isFFISafeStructField, structIsFFISafe); a cfunc type is safe
// iff every one of its own parameter/return types is, recursively
// (cfuncIsFFISafe) - a bare function pointer is just as much a raw address
// at the ABI level as a pointer is. A bare array is deliberately rejected
// here even though the identical element type is fine as a struct field
// (see isFFISafeStructField) - a C array parameter decays to a pointer, a
// conversion this compiler doesn't model implicitly, so there's no legal
// way to pass one directly.
func (c *checker) isFFISafeType(t Type) bool {
	switch t.Kind {
	case TypeStruct:
		return t.Struct != nil && c.structIsFFISafe(t.Struct, nil)
	case TypeCFunc:
		return c.cfuncIsFFISafe(t)
	default:
		return isFFISafeScalar(t)
	}
}

// cfuncIsFFISafe reports whether every one of t's (Kind == TypeCFunc)
// parameter/return types is itself FFI-safe - the cfunc counterpart to
// structIsFFISafe, checked eagerly at every cfunc type's own construction
// site (cfuncTypeFromNode) as well as here; this second check only matters
// when a cfunc type nested inside something else (e.g. a struct field) was
// never itself walked by cfuncTypeFromNode's own per-occurrence diagnostic.
func (c *checker) cfuncIsFFISafe(t Type) bool {
	for _, p := range t.Params {
		if !c.isFFISafeType(p) {
			return false
		}
	}
	return t.Return == nil || t.Return.Kind == TypeVoid || c.isFFISafeType(*t.Return)
}

// isFFISafeScalar reports whether t is FFI-safe on its own, with no
// recursion needed: a numeric type of any width (i8/i16/i32/i64/f32/f64),
// bool, cstring, or a pointer type. A pointer is safe unconditionally,
// whatever its own pointee type is (even one otherwise disallowed here, like
// *string) - a pointer is always just a raw address at the ABI level
// regardless of what it points to. cstring is likewise just a raw pointer
// (see TypeCString's own doc comment). Explicitly excluded: string (a
// {ptr,i32} fat struct), a function type (a fat closure pointer), a map, and
// an enum - none has a well-defined "just pass this to a real C function"
// representation in this compiler.
func isFFISafeScalar(t Type) bool {
	switch t.Kind {
	case TypeI8, TypeI16, TypeI32, TypeI64,
		TypeU8, TypeU16, TypeU32, TypeU64,
		TypeF32, TypeF64, TypeBool, TypeCString, TypePointer:
		return true
	default:
		return false
	}
}

// isFFISafeStructField reports whether t is legal as a field of an FFI-safe
// struct - like isFFISafeType, but a fixed-size array of an FFI-safe element
// type is additionally allowed: a real C struct may embed an array field, so
// `[N]T` (T itself FFI-safe) is safe here even though a *bare* array
// parameter/return is rejected by isFFISafeType. TypeCFunc is handled here
// (not in isFFISafeScalar) because it needs the same recursive param/return
// walk cfuncIsFFISafe already does for bare cfunc params - a bare function
// pointer is just as legal as a C struct field as it is as an extern param.
// A dynamic array (`[]T`, a fat struct) still falls through to
// isFFISafeScalar's `default: false`.
func (c *checker) isFFISafeStructField(t Type, seen map[*StructInfo]bool) bool {
	switch t.Kind {
	case TypeStruct:
		return t.Struct != nil && c.structIsFFISafe(t.Struct, seen)
	case TypeArray:
		return !t.Dynamic && c.isFFISafeStructField(*t.Elem, seen)
	case TypeCFunc:
		return c.cfuncIsFFISafe(t)
	default:
		return isFFISafeScalar(t)
	}
}

// structIsFFISafe reports whether every field of info's struct is itself
// FFI-safe (isFFISafeStructField), recursively. seen guards against a
// genuinely cyclic struct definition the same way structCopyable's own
// computingCopyable does - simply breaks the recursion by treating it as
// safe, since a cyclic by-value struct is either unconstructable or a
// distinct problem elsewhere, not one this check needs to diagnose. Unlike
// StructInfo.Copyable, this isn't memoized on info - checkExternType only
// ever runs once per declared extern signature, so there's no repeated-call
// cost worth caching.
func (c *checker) structIsFFISafe(info *StructInfo, seen map[*StructInfo]bool) bool {
	if seen == nil {
		seen = make(map[*StructInfo]bool)
	}
	if seen[info] {
		return true
	}
	seen[info] = true

	restore := c.pushTree(info.Symbol.Tree)
	defer restore()
	for _, field := range c.tree.StructFields(info.Symbol.Decl) {
		fieldType := c.typeFromNode(c.tree.Child(field, 1))
		if !c.isFFISafeStructField(fieldType, seen) {
			return false
		}
	}
	return true
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
	prevInGeneratorRangeBody := c.inGeneratorRangeBody
	c.inGeneratorRangeBody = 0
	c.curFunc = &enclosingFunc{
		hasReturn: returnTypeNode != ast.InvalidNode,
		ret:       ret,
	}
	restoreMove := c.enterFuncBody()
	c.checkBlock(body)
	restoreMove()
	if c.curFunc.hasReturn && !isTerminatingStmt(c.tree, c.info, body) {
		c.errorAt(n, "missing return")
	}
	c.curFunc = prevFunc
	c.inGeneratorRangeBody = prevInGeneratorRangeBody

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

// paramIsLastInList reports whether param is the last child of its own
// ParamList - see computeDeclType's Param case.
func (c *checker) paramIsLastInList(param ast.NodeIndex) bool {
	children := c.tree.Children(c.tree.Parent(param))
	return len(children) > 0 && children[len(children)-1] == param
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
		// `...T` (see LANGUAGE.md's "Variadic parameters" section): the
		// declared type node names T, the element type - the parameter's own
		// real, effective type is []T, an ordinary dynamic array from here
		// on (codegen's declared LLVM parameter type, a reference to the
		// parameter inside the body, everything reads this wrapped Type, not
		// the bare element one - see Tree.ParamIsVariadic). Only wraps when
		// this is really the list's last parameter (paramIsLastInList) -
		// parseParamList already reports "only the last parameter may be
		// variadic" for any other position, so this avoids compounding that
		// one diagnostic with a confusing, cascading type mismatch.
		elem := c.typeFromNode(c.tree.Child(decl, 1))
		if c.tree.ParamIsVariadic(decl) && c.paramIsLastInList(decl) {
			return Type{Kind: TypeArray, Dynamic: true, Elem: &elem}
		}
		return elem
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
	c.recordLocalDeclLoopDepth(c.tree.Child(decl, 0))
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
	c.recordLocalDeclLoopDepth(c.tree.Child(decl, 0))
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
		c.recordLocalDeclLoopDepth(nameNode)
		c.checkNoIllegalCopy(c.destructureSourceAt(value, i), t, true, "short variable declaration")
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
			// value, not target: target is always an existing lvalue (never
			// a fresh construction), so freshness has to be checked against
			// the destructured source - see checkMultiShortVarDeclNode's own
			// identical call, its `:=` counterpart.
			c.checkNoIllegalCopy(c.destructureSourceAt(value, i), targetTypes[i], true, fmt.Sprintf("assignment target %d", i+1))
		}
	}
}

// destructureSourceAt returns the actual source expression checkNoIllegalCopy
// should ask "is this fresh?" about for a destructuring statement's i'th
// name/target - see checkDestructureSource's own doc comment for value's
// three possible shapes. Only the parallel MultiValueExpr form has a
// distinct expression per position; the other two are one shared expression,
// returned as-is regardless of i.
func (c *checker) destructureSourceAt(value ast.NodeIndex, i int) ast.NodeIndex {
	if c.tree.Nodes[value].Kind == enums.NodeKinds.MultiValueExpr {
		children := c.tree.Children(value)
		if i < len(children) {
			return children[i]
		}
	}
	return value
}

// checkDestructureSource type-checks value - a multi-target destructuring
// statement's (MultiShortVarDecl/MultiAssignStmt) sole right-hand side - and
// returns the wantCount component types to match against each name/target in
// order. value is one of three shapes:
//
//   - a map two-result index (`v, ok := m[k]`/`v, ok = m[k]` - see
//     LANGUAGE.md's "Maps" section) - an IndexExpr, checked structurally.
//   - a multi-return call (`a, b := f()`/`a, b = f()` - see LANGUAGE.md's
//     "Go-style multi-return values" section) - a CallExpr whose own
//     signature returns exactly wantCount values.
//   - a genuine Go-style parallel multi-assignment (`a, b := 1, 2`/
//     `a, b = 1, 2`, each side individually evaluated and paired
//     positionally - LANGUAGE.md's own section above) - a MultiValueExpr
//     wrapping exactly wantCount independent value expressions.
//
// Any other expression shape, or a mismatched count on any of the three
// branches, is rejected with a clean diagnostic. Every rejection path still
// returns a same-length, invalidType-filled slice (rather than nil or a short
// one) so every caller can always safely index it once by position, the same
// "already reported once, don't cascade" recovery invalidType itself always
// provides elsewhere in this pass.
func (c *checker) checkDestructureSource(value ast.NodeIndex, wantCount int, context string) []Type {
	invalid := make([]Type, wantCount)
	for i := range invalid {
		invalid[i] = invalidType
	}

	// `a, b := 1, 2` - this round's own general Go-style parallel
	// multi-assignment (see LANGUAGE.md's "Go-style multi-return values"
	// section): each value is an ordinary, wholly independent expression,
	// checked (and, where untyped, defaulted) exactly like a plain
	// single-value `x := expr` already checks its own one value - never
	// unified against each other or against some further "want" type the way
	// checkMultiValueReturn's own per-value checkAssignable against a
	// function's declared return type is. This is checked before the
	// IndexExpr/CallExpr branches below since a MultiValueExpr is never
	// itself produced by anything else - only finishMultiShortVarDecl/
	// finishMultiAssignStmt build one, exactly when a comma actually follows
	// the first parsed value (see their own doc comments).
	if c.tree.Nodes[value].Kind == enums.NodeKinds.MultiValueExpr {
		values := c.tree.Children(value)
		if len(values) != wantCount {
			c.errorAtNodes(values, value, "assignment mismatch: %d variable%s but %d value%s", wantCount, plural(wantCount), len(values), plural(len(values)))
			for _, v := range values {
				c.checkValueExpr(v)
			}
			return invalid
		}
		types := make([]Type, len(values))
		for i, v := range values {
			types[i] = c.defaultIfUntyped(v, c.checkValueExpr(v))
		}
		return types
	}

	// `v, ok := m[k]` - Go's own "two-result index expression" rule, specific
	// to map indexing (see LANGUAGE.md's "Maps" section) - is a genuinely
	// distinct case from a multi-return function call: a map IndexExpr is
	// never itself a TypeMultiReturn-typed expression (checkIndexExpr always
	// yields V alone, the ordinary single-value case - see its own doc
	// comment), so this is checked structurally, up front, entirely
	// separately from the CallExpr/TypeMultiReturn path below. checkExpr (not
	// checkValueExpr) drives the actual target/key type-checking exactly
	// once, via checkIndexExpr - reading back the target's own already-cached
	// Type (populated by checkIndexExpr's own checkValueExpr(target) call)
	// to decide whether this is really a map index at all, rather than
	// re-checking the target a second time and risking duplicate diagnostics.
	if c.tree.Nodes[value].Kind == enums.NodeKinds.IndexExpr {
		targetNode := c.tree.Child(value, 0)
		vt := c.checkExpr(value)
		tt := c.info.Types[targetNode]
		if tt.Kind != TypeMap {
			c.errorAt(value, "cannot destructure a single-value index expression (%s) into %d targets", vt, wantCount)
			return invalid
		}
		if wantCount != 2 {
			c.errorAt(value, "wrong number of values in %s: a map index yields 2 values (value, ok), got %d target(s)", context, wantCount)
			return invalid
		}
		if vt.IsInvalid() {
			return invalid
		}
		return []Type{vt, boolType}
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

// plural returns "s" for anything but exactly 1 - shared pluralization for
// checkDestructureSource's own Go-style "1 variable but 2 values" wording
// (parser.plural, src/parser/stmt.go, is this same helper's package-local
// twin for the identical single-target parse-time diagnostic).
func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
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
//
// A got of TypeFunc converting to a want of TypeCFunc with a structurally
// matching signature (sameFuncShape) is the one other implicit conversion
// this function allows, subject to checkFuncToCFuncConversion's own
// narrower rules (see LANGUAGE.md's "External functions (FFI)" section).
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
	if want.Kind == TypeCFunc && got.Kind == TypeFunc && sameFuncShape(want, got) {
		return c.checkFuncToCFuncConversion(at, want)
	}
	if !want.Equal(got) {
		c.errorAt(at, "cannot use %s as %s in %s", got, want, context)
		return false
	}
	return true
}

// sameFuncShape reports whether a and b - one TypeFunc, one TypeCFunc -
// declare the identical parameter/return shape: the structural comparison
// checkAssignable's own func-to-cfunc conversion needs, since Type.Equal
// itself always requires an identical Kind first and TypeFunc/TypeCFunc are
// deliberately never Equal to one another.
func sameFuncShape(a, b Type) bool {
	if len(a.Params) != len(b.Params) {
		return false
	}
	for i := range a.Params {
		if !a.Params[i].Equal(b.Params[i]) {
			return false
		}
	}
	return a.Return.Equal(*b.Return)
}

// cfuncSourceSymbol reports whether n is a bare Ident referencing a real,
// already-declared top-level FuncDecl or ExternFuncDecl - the only shape
// checkFuncToCFuncConversion accepts. A function literal, a variable/
// parameter/field already holding a function value, or any other
// expression that merely type-checks as TypeFunc are all rejected: a cfunc
// value is a bare function pointer with no capture context at all, so only
// a function whose real address never changes at runtime can ever become
// one (see DECISIONS.md's dated entry - no trampoline this round).
func (c *checker) cfuncSourceSymbol(n ast.NodeIndex) (*Symbol, bool) {
	if c.tree.Nodes[n].Kind != enums.NodeKinds.Ident {
		return nil, false
	}
	sym, ok := c.info.Refs[n]
	if !ok || sym.Kind != SymFunc || sym.Decl == ast.InvalidNode {
		return nil, false
	}
	return sym, true
}

// cfuncHasStructShape reports whether t (a TypeCFunc) declares a
// struct-by-value parameter or return - checkFuncToCFuncConversion's own
// guard against the one real ABI hazard a func-to-cfunc conversion can hit:
// see that function's own doc comment.
func cfuncHasStructShape(t Type) bool {
	for _, p := range t.Params {
		if p.Kind == TypeStruct {
			return true
		}
	}
	return t.Return != nil && t.Return.Kind == TypeStruct
}

// checkFuncToCFuncConversion validates and completes checkAssignable's own
// func-to-cfunc special case: at must be a bare reference to a real
// top-level FuncDecl/ExternFuncDecl (cfuncSourceSymbol) - anything else (a
// closure, a stored function value) is a compile error, not a silent
// fallback. want's signature is already known to structurally match at's
// own TypeFunc (checkAssignable's sameFuncShape check, before this is ever
// called).
//
// A struct-by-value parameter/return is additionally rejected when the
// source is an ordinary FuncDecl, not an ExternFuncDecl: an extern func's
// own real LLVM signature is already built with the C-ABI struct coercion
// (externParamType/externReturnType, src/codegen/ffi.go) a cfunc call site
// applies too, so the two agree automatically - but an ordinary FuncDecl's
// real signature uses this compiler's own internal (uncoerced) struct-
// passing convention, which would silently disagree with a cfunc call
// site's C-ABI coercion. Teaching ordinary-FuncDecl codegen to carry a
// second, ABI-coerced signature just for this case is separate, non-trivial
// scope, not taken on speculatively this round.
//
// On success, at's own info.Types entry is overwritten to want (TypeCFunc)
// - the same in-place retyping convention retypeUntyped already uses, so a
// later pass (codegen) sees the context that finally pinned this
// expression to a concrete type: checkIdentExpr itself always types a bare
// function reference as TypeFunc regardless of where it flows.
func (c *checker) checkFuncToCFuncConversion(at ast.NodeIndex, want Type) bool {
	sym, ok := c.cfuncSourceSymbol(at)
	if !ok {
		c.errorAt(at, "cannot convert this function value to %s: only a direct reference to a top-level func or extern func is allowed (no closures)", want)
		return false
	}
	restore := c.pushTree(sym.Tree)
	isExtern := c.tree.Nodes[sym.Decl].Kind == enums.NodeKinds.ExternFuncDecl
	restore()
	if !isExtern && cfuncHasStructShape(want) {
		c.errorAt(at, "cannot convert func %s to %s: a struct-by-value parameter/return requires the source to be an extern func", sym.Name, want)
		return false
	}
	c.info.Types[at] = want
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

// enumCopyable computes (and memoizes onto info.Copyable) whether info's
// enum type may be freely copied - the enum-kind counterpart to
// structCopyable, mirroring it exactly: non-copyable iff this enum declares
// its own Destructor, or (transitively) any variant's any associated-data
// type is itself non-copyable - unit variants trivially contribute nothing
// (see LANGUAGE.md's "Enums" section, non-copyable propagation).
func (c *checker) enumCopyable(info *EnumInfo) bool {
	if info.copyableComputed {
		return info.Copyable
	}
	if c.computingEnumCopyable[info] {
		return true
	}
	c.computingEnumCopyable[info] = true
	defer delete(c.computingEnumCopyable, info)

	copyable := info.Destructor == nil
	if copyable {
		restore := c.pushTree(info.Symbol.Tree)
		for _, variantNode := range c.tree.EnumVariants(info.Symbol.Decl) {
			variant := info.Variants[c.tree.Text(variantNode)]
			if variant == nil {
				continue
			}
			switch variant.Kind {
			case EnumVariantTuple:
				for _, t := range variant.Tuple {
					if c.typeIsNonCopyable(t) {
						copyable = false
						break
					}
				}
			case EnumVariantStruct:
				for _, f := range variant.Fields {
					if c.typeIsNonCopyable(f.Type) {
						copyable = false
						break
					}
				}
			}
			if !copyable {
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
// StructInfo.Copyable, or a fixed-size array of a non-copyable element type.
// A dynamic array element's own copyability is deliberately not consulted
// here - see arrayTypeFromNode's own dedicated diagnostic instead.
//
// Forces every struct type this walk reaches through c.structCopyable first
// (memoizing Copyable, respecting its cycle guard) before delegating the
// actual answer to the package-level IsNonCopyable below - mid-checking, a
// struct's Copyable may not be memoized yet, unlike once codegen runs (see
// IsNonCopyable's own doc comment).
func (c *checker) typeIsNonCopyable(t Type) bool {
	switch t.Kind {
	case TypeStruct:
		if t.Struct != nil {
			c.structCopyable(t.Struct) // force Copyable to be memoized
		}
	case TypeEnum:
		if t.Enum != nil {
			c.enumCopyable(t.Enum) // force Copyable to be memoized
		}
	case TypeArray:
		if !t.Dynamic && t.Elem != nil {
			c.typeIsNonCopyable(*t.Elem) // force any nested struct's Copyable too
		}
	}
	return IsNonCopyable(t)
}

// IsNonCopyable is typeIsNonCopyable's package-level counterpart, for a
// caller with no *checker to force a not-yet-memoized Copyable through (see
// codegen/stmt.go's genForStmt) - safe once every struct it's asked about
// already has Copyable computed by a prior, complete Check/CheckPackage
// pass, the same assumption every other codegen lookup into sema's output
// already makes. An unmemoized Copyable reads back false here, folding into
// "non-copyable" - the same conservative direction as above.
func IsNonCopyable(t Type) bool {
	switch t.Kind {
	case TypeStruct:
		if t.Struct == nil {
			return false
		}
		return !t.Struct.Copyable
	case TypeEnum:
		if t.Enum == nil {
			return false
		}
		return !t.Enum.Copyable
	case TypeArray:
		if t.Dynamic || t.Elem == nil {
			return false
		}
		return IsNonCopyable(*t.Elem)
	case TypeCoroutine:
		// Unconditionally non-copyable - a coroutine handle always owns a
		// real heap frame, unlike a struct/array, which only sometimes owns
		// one (see LANGUAGE.md's "Coroutines" section).
		return true
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
		// Also covers a struct-variant construction literal
		// (`Shape.Triangle{...}` - see LANGUAGE.md's "Enums" section) - the
		// identical "building the one instance, not duplicating an existing
		// one" reasoning applies there exactly as it does for a struct's own
		// composite literal.
		return true
	case enums.NodeKinds.CallExpr:
		callee := c.tree.Child(n, 0)
		sym, ok := c.info.Refs[callee]
		if ok && (sym.Kind == SymConstructor || sym.Kind == SymEnumVariant) {
			// A tuple-variant construction call (`Shape.Circle(5.0)`) is
			// exactly as fresh as a struct constructor call - see
			// LANGUAGE.md's "Enums" section.
			return true
		}
		// Calling an async func (see LANGUAGE.md's "Coroutines" section) is
		// exactly as fresh too - every call mints a brand-new heap-allocated
		// coroutine frame, never an existing one.
		if c.calleeIsAsyncFunc(callee) {
			return true
		}
		// Calling ANY function (free or method) whose own declared return
		// type is itself non-copyable is just as fresh: that function could
		// only have type-checked at all if every one of its own return
		// statements already satisfied this same fresh-or-move rule (see
		// checkNoIllegalCopy), so every call to it transitively hands back
		// sole ownership - no per-function annotation or escape analysis
		// needed beyond what its own successful compilation already proves.
		//
		// A multi-return call (TypeMultiReturn - see checkMultiValueReturn)
		// is unconditionally fresh by the same reasoning: it could only
		// type-check via a literal `return a, b, ...`, whose own values are
		// already individually fresh-or-move checked at the return site, so
		// every destructuring call site inherits that guarantee too.
		if sig, ok := c.funcSigForCall(callee); ok && (sig.Return.Kind == TypeMultiReturn || c.typeIsNonCopyable(sig.Return)) {
			return true
		}
		return false
	case enums.NodeKinds.MemberExpr:
		// A bare unit-variant reference (`Shape.Point`) - the enum-kind
		// counterpart to the two cases above, for the one variant kind with
		// no call/literal syntax of its own at all: there's no "existing
		// value" here to duplicate, only a fresh, dataless value being named
		// directly (see LANGUAGE.md's "Enums" section, unit variant
		// construction).
		sym, ok := c.info.Refs[n]
		return ok && sym.Kind == SymEnumVariant
	default:
		return false
	}
}

// isMoveExpr reports whether n (unwrapped of any enclosing ParenExpr, same
// as isFreshConstruction) is a `move x` expression - checkMoveExpr has
// already fully checked it (including the "already moved"/loop/capture
// rules) by the time checkNoIllegalCopy ever sees it, since every call site
// below always calls checkValueExpr(at) before this - so this is purely a
// shape test, not a re-check.
func (c *checker) isMoveExpr(n ast.NodeIndex) bool {
	for c.tree.Nodes[n].Kind == enums.NodeKinds.ParenExpr {
		n = c.tree.Child(n, 0)
	}
	return c.tree.Nodes[n].Kind == enums.NodeKinds.MoveExpr
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
// allowFresh names the one real asymmetry left after `move` (see
// LANGUAGE.md's "move" subsection): every one of the four call sites now
// accepts a fresh construction (isFreshConstruction) OR `move x`
// (isMoveExpr) as the non-copy exception - including a return statement,
// which used to allow no exception at all (see DECISIONS.md's dated entry
// for how `move` resolved the escape-analysis concern that used to justify
// that asymmetry). allowFresh=false is left only for a call site where
// there's no *named* existing value to legally move from at all - an
// element read straight out of a map/array during a range binding
// (seedRangeBindingChecked) is a genuine copy-out of container internals,
// not a reference to some other bare identifier `move` could apply to.
func (c *checker) checkNoIllegalCopy(at ast.NodeIndex, want Type, allowFresh bool, context string) bool {
	if !c.typeIsNonCopyable(want) {
		return true
	}
	if allowFresh && (c.isFreshConstruction(at) || c.isMoveExpr(at)) {
		return true
	}
	c.errorAt(at, "cannot copy %s in %s: it (or a field of it) has a destructor, so it cannot be copied - only constructed fresh, moved (move x), or referenced through a pointer", want, context)
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
		if sym.Generic != nil {
			c.errorAt(n, "%s is generic - name its type arguments, e.g. %s[%s]",
				sym.Name, sym.Name, strings.Join(sym.Generic.Params, ", "))
			return invalidType
		}
		return c.typeFromSymbol(sym)
	case enums.NodeKinds.IndexExpr:
		// `SlotMap[int]` - a generic instantiation in type position (see
		// LANGUAGE.md's "Generics" section). Anything else wearing the same
		// IndexExpr shape isn't a type at all.
		t, ok := c.checkGenericTypeExpr(n)
		if !ok {
			c.errorAt(n, "invalid type expression")
		}
		return t
	case enums.NodeKinds.ArrayType:
		return c.arrayTypeFromNode(n)
	case enums.NodeKinds.MapType:
		return c.mapTypeFromNode(n)
	case enums.NodeKinds.PointerType:
		return c.pointerTypeFromNode(n)
	case enums.NodeKinds.FuncType:
		return c.funcTypeFromNode(n)
	case enums.NodeKinds.CFuncType:
		return c.cfuncTypeFromNode(n)
	case enums.NodeKinds.MultiReturnType:
		return c.multiReturnTypeFromNode(n)
	case enums.NodeKinds.YieldReturnType:
		return c.yieldReturnTypeFromNode(n)
	default:
		return invalidType
	}
}

// yieldReturnTypeFromNode converts a YieldReturnType type-position node (a
// FuncDecl's own `yield T` return-type marker - see LANGUAGE.md's "Generator
// functions" section and ast.Node's own YieldReturnType doc comment) into a
// Type - the generator counterpart to pointerTypeFromNode/arrayTypeFromNode,
// wrapping T via Elem the same "wraps one other Type" way.
func (c *checker) yieldReturnTypeFromNode(n ast.NodeIndex) Type {
	elem := c.typeFromNode(c.tree.Child(n, 0))
	return Type{
		Kind: TypeGenerator,
		Elem: &elem,
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
		case "u8":
			return u8Type
		case "u16":
			return u16Type
		case "u32":
			return u32Type
		case "u64":
			return u64Type
		case "f32":
			return f32Type
		case "f64":
			return f64Type
		case "string":
			return stringType
		case "cstring":
			return cstringType
		case "bool":
			return boolType
		case "coroutine":
			// Elem always points at voidType here - async funcs declare no
			// return type this round (see LANGUAGE.md's "Coroutines"
			// section), and Equal/String both assume a non-nil Elem for
			// TypeCoroutine (see the call-expr construction site below).
			ret := voidType
			return Type{Kind: TypeCoroutine, Elem: &ret}
		case "Any":
			return Type{Kind: TypeAny}
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
	case SymEnum:
		if sym.EnumInfo == nil {
			return invalidType
		}
		return Type{
			Kind: TypeEnum,
			Enum: sym.EnumInfo,
		}
	case SymTypeParam:
		// One instantiation's own binding for a type parameter (see
		// generics.go) - always already concrete.
		return *sym.TypeParamBound
	case SymEnumVariant:
		// A bare EnumName.Variant reached ordinary type position (not a
		// composite-literal's own type-expr slot, which checkCompositeLit
		// intercepts before ever reaching here) - e.g. `var x Shape.Circle` -
		// a variant is never itself a standalone type, only its owning enum
		// is.
		return invalidType
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

// cfuncTypeFromNode converts a CFuncType type-position node (`cfunc(T1, T2)
// R` - see LANGUAGE.md's "External functions (FFI)" section) into a Type -
// funcTypeFromNode's bare-C-function-pointer counterpart, identical shape
// (ParamTypeList child, optional return-type child). Unlike an ordinary
// FuncType, every parameter and the return type (when declared) must
// itself be FFI-safe (isFFISafeType) - enforced unconditionally here,
// regardless of where the cfunc type appears (an extern signature, an
// ordinary var/param/field, ...), since a cfunc value always lowers to a
// bare function pointer called with real C-ABI marshaling (see CODEGEN.md).
func (c *checker) cfuncTypeFromNode(n ast.NodeIndex) Type {
	paramListNode := c.tree.Child(n, 0)
	returnNode := c.tree.Child(n, 1)

	paramNodes := c.tree.Children(paramListNode)
	params := make([]Type, len(paramNodes))
	for i, p := range paramNodes {
		t := c.typeFromNode(p)
		params[i] = t
		c.checkCFuncElemType(p, t, "parameter")
	}

	ret := voidType
	if returnNode != ast.InvalidNode {
		ret = c.typeFromNode(returnNode)
		c.checkCFuncElemType(returnNode, ret, "return")
	}
	return Type{
		Kind:   TypeCFunc,
		Params: params,
		Return: &ret,
	}
}

// checkCFuncElemType reports a diagnostic at n when t isn't FFI-safe
// (isFFISafeType) - checkExternType's identical check, worded for a bare
// cfunc type's own parameter/return position instead of an extern func's.
func (c *checker) checkCFuncElemType(n ast.NodeIndex, t Type, what string) {
	if t.IsInvalid() {
		return
	}
	if !c.isFFISafeType(t) {
		c.errorAt(n, "cfunc %s type %s is not supported - only numeric types, bool, cstring, pointer types, cfunc types, and structs made entirely of FFI-safe fields are allowed", what, t)
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

// mapTypeFromNode converts a MapType type-position node (`map[K]V` - see
// LANGUAGE.md's "Maps" section and ast.Node's own MapType doc comment) into a
// Type - the map counterpart to arrayTypeFromNode, minus the size handling a
// map type has no use for. A map's key type is checked right here, at every
// declaration site, against this language's own comparability rule
// (typeIsComparable) - a dynamic array, function type, or another map is
// rejected outright with a real diagnostic, the same "grammar accepts the
// general shape, sema enforces the feature's own narrower rule" division of
// labor arrayTypeFromNode's own non-copyable-element check already uses.
//
// Both key and value are also rejected if non-copyable (typeIsNonCopyable) -
// every map read/probe/grow copies bytes around with no destructor-cascading
// concept, the same hazard arrayTypeFromNode's own element check guards
// against.
func (c *checker) mapTypeFromNode(n ast.NodeIndex) Type {
	keyNode := c.tree.Child(n, 0)
	elemNode := c.tree.Child(n, 1)
	key := c.typeFromNode(keyNode)
	elem := c.typeFromNode(elemNode)

	if !key.IsInvalid() && !c.typeIsComparable(key) {
		c.errorAt(keyNode, "invalid map key type %s: a map key must be comparable (a dynamic array, function type, or another map cannot be used as a key)", key)
	}
	if !key.IsInvalid() && c.typeIsNonCopyable(key) {
		c.errorAt(keyNode, "map key type %s is non-copyable (has a destructor); a non-copyable type cannot be used as a map key", key)
	}
	if !elem.IsInvalid() && c.typeIsNonCopyable(elem) {
		c.errorAt(elemNode, "map value type %s is non-copyable (has a destructor); a non-copyable type cannot be used as a map value", elem)
	}

	return Type{
		Kind: TypeMap,
		Key:  &key,
		Elem: &elem,
	}
}

// typeIsComparable reports whether t is a type this language's own `==`/`!=`
// actually supports (see LANGUAGE.md's "Maps" section for the map-key use
// this was originally written for, and its Operators section for the
// struct/array-equality use it now shares too): any numeric type, bool,
// string, a pointer, or a struct/fixed-size array whose own fields/elements
// are themselves all comparable, recursively (mirroring structCopyable's own
// recursive-field-walk shape, just checking comparability instead of
// copyability) - a dynamic array (`[]T`), a function type, another map, or
// cstring are all explicitly rejected, anywhere they appear, even nested
// arbitrarily deep inside a struct field or fixed-array element: none of
// them are meaningfully hashable/comparable the way this language currently
// represents them (see AGENTS.md's Operators section - `==`/`!=` themselves
// already reject a bare dynamic array outright, and never define anything
// for a bare function or map type at all).
//
// This is the single source of truth both a map's key type (mapTypeFromNode)
// and `==`/`!=`'s own struct/array operand (checkEqualityOperands) now
// share - deliberately not the same set typeIsPrintable defines below: a
// dynamic array is printable but never comparable (see that function's own
// doc comment for why the two allowlists genuinely differ).
func (c *checker) typeIsComparable(t Type) bool {
	switch t.Kind {
	case TypeArray:
		if t.Dynamic {
			return false
		}
		return t.Elem == nil || c.typeIsComparable(*t.Elem)
	case TypeFunc, TypeCFunc, TypeMap, TypeCString, TypeAny:
		// Any is deliberately excluded this round - see LANGUAGE.md's "Any"
		// section: no defined `==` lowering (genValueEqual has no TypeAny
		// case), the same reasoning TypeCString's own doc comment gives.
		return false
	case TypeStruct:
		if t.Struct == nil {
			return true
		}
		restore := c.pushTree(t.Struct.Symbol.Tree)
		defer restore()
		for _, field := range c.tree.StructFields(t.Struct.Symbol.Decl) {
			fieldType := c.typeFromNode(c.tree.Child(field, 1))
			if !c.typeIsComparable(fieldType) {
				return false
			}
		}
		return true
	case TypeEnum:
		return c.enumAssociatedTypesAll(t.Enum, c.typeIsComparable)
	default:
		return true
	}
}

// enumAssociatedTypesAll reports whether pred holds for every associated-data
// type of every variant of info - not just one variant, since comparability/
// printability is a compile-time property that must hold across every
// possible runtime variant, exactly like a struct's fields all being checked
// regardless of which code path actually sets them (see LANGUAGE.md's
// "Enums" section). Shared by typeIsComparable/typeIsPrintable's own
// TypeEnum case - the two allowlists genuinely differ (a dynamic array is
// printable but never comparable - see typeIsPrintable's own doc comment),
// but both walk every variant's every associated type the identical way, so
// only the leaf predicate itself differs between the two callers. A nil info
// (an enum type reached before its own EnumInfo could be resolved) is
// vacuously true, the same recovery every other nil-catalog case in this
// file already uses.
func (c *checker) enumAssociatedTypesAll(info *EnumInfo, pred func(Type) bool) bool {
	if info == nil {
		return true
	}
	restore := c.pushTree(info.Symbol.Tree)
	defer restore()
	for _, variant := range info.Order {
		switch variant.Kind {
		case EnumVariantTuple:
			for _, t := range variant.Tuple {
				if !pred(t) {
					return false
				}
			}
		case EnumVariantStruct:
			for _, f := range variant.Fields {
				if !pred(f.Type) {
					return false
				}
			}
		}
	}
	return true
}

// typeIsPrintable reports whether t is a type `print` can actually lower
// (see codegen's genPrintCall/genPrintValueBare, runtime.go): the same
// numeric/bool/string/pointer/struct/array base typeIsComparable accepts,
// recursing into struct fields and BOTH fixed and dynamic array elements,
// but strictly larger than typeIsComparable in one deliberate way - a
// dynamic array (`[]T`) IS printable (genPrintArrayValue already renders one
// correctly today, an existing working feature this must not regress) even
// though it is never comparable (see typeIsComparable's own doc comment). A
// function type, a map type, or cstring are rejected either way - codegen
// has no rendering for any of them and never will, the same as
// typeIsComparable's own rejection of them, just for a different reason
// (nothing to hash/compare vs. nothing to print).
func (c *checker) typeIsPrintable(t Type) bool {
	switch t.Kind {
	case TypeArray:
		return t.Elem == nil || c.typeIsPrintable(*t.Elem)
	case TypeFunc, TypeCFunc, TypeMap, TypeCString, TypeAny:
		// Any is deliberately excluded this round - see LANGUAGE.md's "Any"
		// section: wiring it into print() is explicit future work, not this
		// round's scope, same reasoning as TypeCString's own exclusion.
		return false
	case TypeStruct:
		if t.Struct == nil {
			return true
		}
		restore := c.pushTree(t.Struct.Symbol.Tree)
		defer restore()
		for _, field := range c.tree.StructFields(t.Struct.Symbol.Decl) {
			fieldType := c.typeFromNode(c.tree.Child(field, 1))
			if !c.typeIsPrintable(fieldType) {
				return false
			}
		}
		return true
	case TypeEnum:
		return c.enumAssociatedTypesAll(t.Enum, c.typeIsPrintable)
	default:
		return true
	}
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
		t := c.checkExpr(expr)
		if !c.rejectGeneratorValue(expr, t) {
			c.defaultIfUntyped(expr, t)
		}
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
	case enums.NodeKinds.RangeForStmt:
		c.checkRangeForStmt(n)
	case enums.NodeKinds.MatchStmt:
		c.checkMatchStmt(n)
	case enums.NodeKinds.YieldStmt:
		c.checkYieldStmt(n)
	case enums.NodeKinds.AwaitStmt:
		c.checkAwaitStmt(n)
	}
}

// checkYieldStmt type-checks `yield expr` - legal inside a match-expression
// arm's own block OR a generator function's own body (see LANGUAGE.md's
// "match" and "Generator functions" sections). c.matchExprStack is checked
// first and takes priority - a yield inside a still-innermost match-
// expression arm always targets that match, never an enclosing generator,
// even when the enclosing function is itself one.
//
// Inside a match expression, the first yield seen anywhere across the whole
// enclosing match fixes its result type; every subsequent yield must be
// assignable to it. Inside a generator, every yield is checked directly
// against the generator's own declared element type (curFunc.yieldElem).
func (c *checker) checkYieldStmt(n ast.NodeIndex) {
	value := c.tree.Child(n, 0)

	if len(c.matchExprStack) > 0 {
		frame := c.matchExprStack[len(c.matchExprStack)-1]
		vt := c.defaultIfUntyped(value, c.checkValueExpr(value))
		if vt.IsInvalid() {
			return
		}
		if !frame.resultTypeSet {
			frame.resultType = vt
			frame.resultTypeSet = true
			return
		}
		c.checkAssignable(value, frame.resultType, vt, "match arm yield")
		return
	}

	if c.curFunc != nil && c.curFunc.isGenerator {
		vt := c.checkValueExpr(value)
		c.checkAssignable(value, c.curFunc.yieldElem, vt, "yield")
		return
	}

	c.errorAt(n, "yield outside a match expression or a generator function")
	c.checkValueExpr(value)
}

// checkAwaitStmt type-checks a bare `await` (see LANGUAGE.md's "Coroutines"
// section) - legal only inside an async function's own body, at any nesting
// depth, mirroring checkYieldStmt's "outside a generator function" rejection
// one construct over. There is no operand to check this round.
func (c *checker) checkAwaitStmt(n ast.NodeIndex) {
	if c.curFunc == nil || !c.curFunc.isAsync {
		c.errorAt(n, "await outside an async function")
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
// section) - p must have a pointer type, OR a coroutine handle (see
// LANGUAGE.md's "Coroutines" section: `delete h` explicitly destroys a
// not-yet-done coroutine early, reusing this same `new`/`delete` vocabulary
// rather than inventing a separate destroy-statement form) - delete itself
// produces no value either way.
func (c *checker) checkDeleteStmt(n ast.NodeIndex) {
	expr := c.tree.Child(n, 0)
	t := c.defaultIfUntyped(expr, c.checkValueExpr(expr))
	if t.IsInvalid() {
		return
	}
	if t.Kind != TypePointer && t.Kind != TypeCoroutine {
		c.errorAt(n, "delete requires a pointer or a coroutine handle, got %s", t)
	}
}

func (c *checker) checkAssignStmt(n ast.NodeIndex) {
	target := c.tree.Child(n, 0)
	value := c.tree.Child(n, 1)

	if c.isSelfMove(target, value) {
		c.errorAt(value, "cannot move %s into itself", c.tree.Text(target))
	}

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
	if c.isMapIndexTarget(target) {
		c.errorAt(target, "map element does not support compound assignment (%s) - read it, modify the value, and store it back with a plain = instead", op)
		return
	}
	c.checkCompoundOp(value, op, tt, vt)
}

// isSelfMove reports whether value is `move x` where x resolves to the same
// symbol target (an Ident lvalue) does - checkAssignStmt's own guard against
// `f = move f`, a degenerate self-reference this language's linear ownership
// tracking has no sound meaning for (see genAssignStmt's own reassignment-
// leak fix, CODEGEN.md: the two would race over which of "destruct f's old
// value" and "load f's current value for the move" runs first).
func (c *checker) isSelfMove(target, value ast.NodeIndex) bool {
	if c.tree.Nodes[target].Kind != enums.NodeKinds.Ident {
		return false
	}
	for c.tree.Nodes[value].Kind == enums.NodeKinds.ParenExpr {
		value = c.tree.Child(value, 0)
	}
	if c.tree.Nodes[value].Kind != enums.NodeKinds.MoveExpr {
		return false
	}
	operand := c.tree.Child(value, 0)
	if c.tree.Nodes[operand].Kind != enums.NodeKinds.Ident {
		return false
	}
	targetSym, ok1 := c.info.Refs[target]
	operandSym, ok2 := c.info.Refs[operand]
	return ok1 && ok2 && targetSym == operandSym
}

// isMapIndexTarget reports whether n is a map index expression (`m[k]`) -
// shared by checkAssignStmt's own compound-assignment rejection and
// checkIncDecStmt's identical one just below: `m[k] = v` (a plain insert-or-
// update) is the only assignment form maps support this round (see
// LANGUAGE.md's "Maps" section) - `m[k] += v`/`m[k]++`/`m[k]--` would each
// need a real "read-modify-write in one map operation" primitive this round
// deliberately doesn't build, so they're rejected here with a clear
// diagnostic instead of reaching codegen (which has no lowering for them at
// all) on an otherwise-valid-looking tree.
func (c *checker) isMapIndexTarget(n ast.NodeIndex) bool {
	if c.tree.Nodes[n].Kind != enums.NodeKinds.IndexExpr {
		return false
	}
	target := c.tree.Child(n, 0)
	return c.info.Types[target].Kind == TypeMap
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
	if c.isMapIndexTarget(target) {
		c.errorAt(n, "map element does not support %s - read it, modify the value, and store it back with a plain = instead", c.tree.Text(n))
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

	if c.inGeneratorRangeBody > 0 {
		c.errorAt(n, "return is not supported inside a generator-consuming range-for's own body (this would require non-local return, not implemented)")
		if value != ast.InvalidNode {
			c.checkValueExpr(value)
		}
		return
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
		// A fresh construction or `move x` is now a legal return value too -
		// see checkNoIllegalCopy's own doc comment and DECISIONS.md's dated
		// entry for why this no longer needs its own escape-analysis carve-out.
		c.checkNoIllegalCopy(value, fn.ret, true, "return statement")
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
			// Same fresh-or-move exception as checkReturnStmt's own
			// single-value tail.
			c.checkNoIllegalCopy(v, want[i], true, context)
		}
	}
}

// cloneMoved returns a real copy of m - moveState.moved is a reference type,
// so checkIfStmt/checkMatchDispatch's own then/else/arm branches each need
// their own independent copy to mutate, the move-tracking counterpart to
// codegen's identical snapshotDestructors/restoreDestructors need (see
// CODEGEN.md's "Destructors" section).
func cloneMoved(m map[*Symbol]bool) map[*Symbol]bool {
	clone := make(map[*Symbol]bool, len(m))
	for sym := range m {
		clone[sym] = true
	}
	return clone
}

// armMoveResult is checkMatchDispatch's own per-arm move-tracking result -
// the moved-set its body ended up with, and whether it diverts (see
// branchDivertsControl) - collected once per arm and merged together
// afterward (mergeMovedAcrossPaths).
type armMoveResult struct {
	moved   map[*Symbol]bool
	diverts bool
}

// mergeMovedAcrossPaths is checkIfStmt/checkMatchDispatch's own shared
// join-or-reject core: paths is every reachable (non-diverting) path's own
// resulting moved-set out of some branching construct, base the moved-set
// before it. A symbol moved on only SOME of paths is rejected right here
// ("may already have been moved") rather than reconciled - see
// DECISIONS.md's dated entry for why this is a deliberate trade-off, not a
// missing feature: it's what lets codegen reuse removeDestructorEntry/
// pushDestructorEntry completely unchanged, with no runtime "moved" flag
// ever needed on any type's own representation.
func (c *checker) mergeMovedAcrossPaths(at ast.NodeIndex, base map[*Symbol]bool, paths []map[*Symbol]bool) map[*Symbol]bool {
	if len(paths) == 0 {
		return base
	}
	seen := make(map[*Symbol]bool)
	for _, p := range paths {
		for sym := range p {
			seen[sym] = true
		}
	}
	merged := cloneMoved(base)
	for sym := range seen {
		if base[sym] {
			continue
		}
		allMoved := true
		for _, p := range paths {
			if !p[sym] {
				allMoved = false
				break
			}
		}
		if !allMoved {
			// Reported once, right here - deliberately NOT marked moved in
			// the merged result, so a later read doesn't also cascade its
			// own separate "use of moved value" diagnostic for the same
			// root ambiguity.
			c.errorAt(at, "%s may already have been moved: moved on only some of this construct's reachable paths", sym.Name)
			continue
		}
		merged[sym] = true
	}
	return merged
}

func (c *checker) checkIfStmt(n ast.NodeIndex) {
	c.checkCondition(c.tree.Child(n, 0))

	thenBranch := c.tree.Child(n, 1)
	elseBranch := c.tree.Child(n, 2)

	preMoved := c.move.moved
	c.move.moved = cloneMoved(preMoved)
	c.checkStmt(thenBranch)
	thenMoved := c.move.moved
	thenDiverts := branchDivertsControl(c.tree, c.info, thenBranch)

	var paths []map[*Symbol]bool
	if !thenDiverts {
		paths = append(paths, thenMoved)
	}
	if elseBranch == ast.InvalidNode {
		// No else: the implicit "condition was false" path never moves
		// anything new, and never diverts - always a reachable path.
		paths = append(paths, preMoved)
	} else {
		c.move.moved = cloneMoved(preMoved)
		c.checkStmt(elseBranch)
		if !branchDivertsControl(c.tree, c.info, elseBranch) {
			paths = append(paths, c.move.moved)
		}
	}

	c.move.moved = c.mergeMovedAcrossPaths(n, preMoved, paths)
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

// checkRangeForStmt type-checks `for [key[, value]] := range subject { ... }`
// (see LANGUAGE.md's "Range loops" section) - subject must be a map or a
// fixed/dynamic array; anything else is a clean diagnostic. Per Go's own real
// rule (deliberately easy to get backwards by symmetry/intuition - see
// DECISIONS.md's dated entry for this round): a map's one-binding form binds
// the KEY, never the value; an array's one-binding form binds the INDEX
// (always int), never the element. The two-binding form is (K, V) for a map,
// (int, elem) for an array. key/value each have no single declaring node of
// their own (mirroring MultiShortVarDecl's identical binding shape - see
// checkMultiShortVarDeclNode), so their Type is seeded directly here rather
// than computed lazily via declType.
//
// Every iteration's key/value is a genuine copy out of the map/array's own
// storage (see codegen's bindRangeVar) - exactly like any other short-var-decl
// destructuring a call/index result - so a non-copyable K/V is rejected here
// too (checkNoIllegalCopy, allowFresh=false: an existing element read out of
// a container is never a fresh construction), the same rule `v := m[k]`/
// `v := arr[i]` already enforce. Without this, ranging over a container of a
// destructor-having element type would silently produce an extra, illegal
// destructor call per iteration on a value the type's own copy rule says
// should never exist as a duplicate at all.
func (c *checker) checkRangeForStmt(n ast.NodeIndex) {
	keyNode := c.tree.RangeForKey(n)
	valueNode := c.tree.RangeForValue(n)
	subjectNode := c.tree.RangeForSubject(n)

	subjType := c.checkRangeForSubjectExpr(subjectNode)

	isGeneratorSubject := subjType.Kind == TypeGenerator
	switch subjType.Kind {
	case TypeMap:
		c.seedRangeBindingChecked(keyNode, *subjType.Key, "range key binding")
		c.seedRangeBindingChecked(valueNode, *subjType.Elem, "range value binding")
	case TypeArray:
		c.seedRangeBinding(keyNode, i32Type)
		c.seedRangeBindingChecked(valueNode, *subjType.Elem, "range value binding")
	case TypeGenerator:
		if c.isAnyFieldsRangeSubject(subjectNode) {
			// AnyFields(a) is the one deliberate exception to "a generator
			// produces at most 1 value" below: it always yields a (field
			// name, field value) pair, so ranging over it always binds two
			// loop variables, exactly like a map's (key, value) - not
			// generalized to real generators, which have no comparable
			// paired-yield shape.
			if valueNode == ast.InvalidNode {
				c.errorAt(subjectNode, "range over AnyFields requires two bindings: for name, value := range AnyFields(a) { ... }")
				c.seedRangeBinding(keyNode, invalidType)
			} else {
				c.seedRangeBindingChecked(keyNode, stringType, "range field-name binding")
				c.seedRangeBindingChecked(valueNode, Type{Kind: TypeAny}, "range field-value binding")
			}
			break
		}
		c.checkGeneratorRangeSubject(subjectNode)
		if c.curFunc != nil && c.curFunc.isGenerator {
			c.errorAt(subjectNode, "a generator function's own body cannot range over another generator (nested generator composition is not supported)")
		}
		if valueNode != ast.InvalidNode {
			c.errorAt(subjectNode, "range over a generator produces at most 1 value, got 2")
			c.seedRangeBinding(keyNode, invalidType)
			c.seedRangeBinding(valueNode, invalidType)
		} else {
			// There is no key/index binding at all for a generator subject,
			// unlike a map (key) or array (index) - the single binding, when
			// present, is seeded from keyNode (the one-binding form's own
			// slot - see ast.Node's own RangeForStmt doc comment) but means
			// the yielded VALUE here, not a key or index.
			c.seedRangeBindingChecked(keyNode, *subjType.Elem, "range value binding")
		}
	case TypeInvalid:
		c.seedRangeBinding(keyNode, invalidType)
		c.seedRangeBinding(valueNode, invalidType)
	default:
		c.errorAt(subjectNode, "range requires a map, array, or generator value, got %s", subjType)
		c.seedRangeBinding(keyNode, invalidType)
		c.seedRangeBinding(valueNode, invalidType)
	}

	if isGeneratorSubject {
		c.inGeneratorRangeBody++
	}
	c.loopDepth++
	c.checkBlock(c.tree.RangeForBody(n))
	c.loopDepth--
	if isGeneratorSubject {
		c.inGeneratorRangeBody--
	}
}

// checkGeneratorRangeSubject enforces that subjectNode (already known to be
// TypeGenerator) is a direct call to a function declared by name -
// directFuncCallSymbol (resolve.go, shared with isGeneratorRangeSubject)
// is the actual predicate: the generator-consuming range-for's own codegen
// (genRangeForGenerator) needs the callee's real FuncDecl-based signature to
// append its synthesized callback argument to, which only a direct call has.
func (c *checker) checkGeneratorRangeSubject(subjectNode ast.NodeIndex) {
	if _, ok := directFuncCallSymbol(c.tree, c.info, subjectNode); !ok {
		c.errorAt(subjectNode, "range over a generator requires calling it directly by name (for v := range Gen(...) { ... }), not through a stored function value or any other indirection")
	}
}

// isAnyFieldsRangeSubject reports whether subjectNode is a direct
// AnyFields(...) call - checkRangeForStmt's own signal to take its 2-binding
// (name, value) path instead of an ordinary generator's 1-binding one.
// AnyFields is a predeclared builtin (Decl == ast.InvalidNode, see
// universeScope), not a real FuncDecl, so it deliberately bypasses
// directFuncCallSymbol/checkGeneratorRangeSubject rather than trying to
// satisfy their "real declared generator" requirement.
func (c *checker) isAnyFieldsRangeSubject(subjectNode ast.NodeIndex) bool {
	if c.tree.Nodes[subjectNode].Kind != enums.NodeKinds.CallExpr {
		return false
	}
	return c.isBuiltinCall(c.tree.Child(subjectNode, 0), "AnyFields")
}

// seedRangeBinding seeds nameNode's (a RangeForStmt's key or value binding,
// when present - a no-op for ast.InvalidNode, the omitted-binding case) Type
// directly into both declType's memoization cache and info.Types, the same
// "no single declaring node" seeding checkMultiShortVarDeclNode's own doc
// comment explains.
func (c *checker) seedRangeBinding(nameNode ast.NodeIndex, t Type) {
	if nameNode == ast.InvalidNode {
		return
	}
	c.declTypes[nodeRef{c.tree, nameNode}] = t
	c.info.Types[nameNode] = t
	c.recordLoopBindingDeclLoopDepth(nameNode)
}

// seedRangeBindingChecked is seedRangeBinding plus checkNoIllegalCopy - see
// checkRangeForStmt's own doc comment for why every real (non-omitted)
// key/value binding needs this and a plain array index (always int) doesn't.
func (c *checker) seedRangeBindingChecked(nameNode ast.NodeIndex, t Type, context string) {
	c.seedRangeBinding(nameNode, t)
	if nameNode != ast.InvalidNode {
		c.checkNoIllegalCopy(nameNode, t, false, context)
	}
}

func (c *checker) checkCondition(n ast.NodeIndex) {
	t := c.checkValueExpr(n)
	if !t.IsInvalid() && t.Kind != TypeBool {
		c.errorAt(n, "condition must be bool, got %s", t)
	}
}

// checkMatchStmt type-checks a bare statement-position `match subject {
// pattern => body, ... }` (see LANGUAGE.md's "match" section) - reached only
// via checkStmt's own dispatch, never checkExpr's (an expression-position
// match is checkMatchExprStmt, wired at checkExpr/inferExpr's own dispatch
// instead - see that function's own doc comment). A thin wrapper around
// checkMatchDispatch, passing checkBlock as the "how do I check one arm's
// own body" callback - exactly what this function's own body always did,
// before checkMatchExprStmt needed the identical dispatch logic with a
// different arm-body checker layered on top instead of re-implementing it.
func (c *checker) checkMatchStmt(n ast.NodeIndex) {
	c.checkMatchDispatch(n, c.checkBlock)
}

// checkMatchExprStmt type-checks a `match` used in EXPRESSION position
// (`x := match subject {...}`, a function call argument, nested inside
// another expression - see LANGUAGE.md's "match" section's "match as an
// expression" subsection) - reached via checkExpr/inferExpr's own dispatch,
// never checkStmt's. Reuses checkMatchDispatch's entire dispatch-on-
// subject-type/exhaustiveness/duplicate-arm/wildcard machinery verbatim
// (the same one checkMatchStmt uses, completely unchanged) - the only
// genuinely new behavior layered on top is (1) pushing/popping a
// matchExprCheckCtx frame around the whole arms-checking pass, so a `yield`
// anywhere in any arm can unify a running result type across every arm (see
// checker.matchExprStack's own doc comment), and (2) checking, per arm,
// that every reachable path through its own block actually yields
// (checkMatchExprArmBody, this function's own checkArm callback).
//
// A match expression with no reachable yield at all (every arm's every path
// returns/breaks/continues instead - mustYieldEveryPath still accepts that,
// mirroring isTerminatingStmt's own treatment of return/break/continue, see
// that function's own doc comment) never establishes a result type; rather
// than silently letting an untyped/zero-value Type leak into Info.Types
// (which the AGENTS.md review process exists specifically to catch - a
// silently-wrong type is worse than a loud diagnostic), this is reported
// directly here as its own clean error, and invalidType is returned so
// downstream cascading checks (an enclosing `:=`'s own declType, say) still
// have something to compare against without a second, unrelated diagnostic.
func (c *checker) checkMatchExprStmt(n ast.NodeIndex) Type {
	frame := &matchExprCheckCtx{}
	c.matchExprStack = append(c.matchExprStack, frame)
	c.checkMatchDispatch(n, c.checkMatchExprArmBody)
	c.matchExprStack = c.matchExprStack[:len(c.matchExprStack)-1]

	if !frame.resultTypeSet {
		c.errorAt(n, "match expression has no arm that ever yields a value")
		return invalidType
	}
	return frame.resultType
}

// checkMatchExprArmBody is checkMatchExprStmt's own checkArm callback (see
// checkMatchDispatch/checkEnumMatchStmt/checkValueMatchStmt/
// checkMatchArmFallback's shared checkArm parameter): checks body exactly
// like the statement-mode checkBlock does, then additionally requires every
// reachable path through it to end in a yield (mustYieldEveryPath) - the one
// new rule an expression-mode match's arm body must satisfy that a
// statement-mode one never did (see LANGUAGE.md's "match" section's "match
// as an expression" subsection).
func (c *checker) checkMatchExprArmBody(body ast.NodeIndex) {
	c.checkBlock(body)
	if !mustYieldEveryPath(c.tree, c.info, body) {
		c.errorAt(body, "match arm does not yield a value on every path")
	}
}

// checkMatchDispatch is checkMatchStmt/checkMatchExprStmt's own shared
// dispatch core (see LANGUAGE.md's "match" section) - dispatching on the
// subject's own resolved type to one of two genuinely different checked
// shapes - an enum value (checkEnumMatchStmt, the original exhaustiveness-
// checked feature) or a plain scalar value (checkValueMatchStmt, the
// Go-`switch`-style generalization - see isValueMatchType). The subject may
// be an enum value directly, or a pointer to one (`this` inside a method -
// see checkThisExpr) - auto-dereferenced here, the same auto-deref every
// other struct/enum-receiver access in this language already gets; a
// value-match subject is never a pointer (isValueMatchType only admits
// scalar leaf kinds, and a pointer is never one). checkArm is how each arm's
// own body gets checked - plain checkBlock for a statement-position match,
// or checkMatchExprArmBody's extra "every path yields" rule for an
// expression-position one - threaded down into checkEnumMatchStmt/
// checkValueMatchStmt/checkMatchArmFallback so none of that dispatch/
// exhaustiveness logic needs re-implementing for the expression-mode case at
// all.
func (c *checker) checkMatchDispatch(n ast.NodeIndex, checkArm func(body ast.NodeIndex)) {
	subjectNode := c.tree.MatchSubject(n)
	subjType := c.checkValueExpr(subjectNode)

	// Wrap checkArm so every arm this dispatch ends up calling it against
	// (whichever of checkEnumMatchStmt/checkValueMatchStmt/
	// checkMatchArmFallback below actually runs) checks its own body against
	// an independent moved-set copy, then records that result - the
	// move-tracking counterpart to genMatchStmt/genValueMatchStmt's own
	// snapshotDestructors around each arm (CODEGEN.md's "Destructors"
	// section). The merge itself runs in a defer, so it applies uniformly
	// regardless of which of this function's several return points fires.
	preMoved := c.move.moved
	var armResults []armMoveResult
	origCheckArm := checkArm
	checkArm = func(body ast.NodeIndex) {
		c.move.moved = cloneMoved(preMoved)
		origCheckArm(body)
		armResults = append(armResults, armMoveResult{
			moved:   c.move.moved,
			diverts: branchDivertsControl(c.tree, c.info, body),
		})
	}
	defer func() {
		var paths []map[*Symbol]bool
		if !matchIsExhaustive(c.tree, c.info, n) {
			paths = append(paths, preMoved)
		}
		for _, r := range armResults {
			if !r.diverts {
				paths = append(paths, r.moved)
			}
		}
		c.move.moved = c.mergeMovedAcrossPaths(n, preMoved, paths)
	}()

	enumType := subjType
	if enumType.Kind == TypePointer && enumType.Elem != nil {
		enumType = *enumType.Elem
	}

	if enumType.IsInvalid() {
		for _, arm := range c.tree.MatchArms(n) {
			c.checkMatchArmFallback(arm, checkArm)
		}
		return
	}

	if enumType.Kind == TypeEnum {
		c.checkEnumMatchStmt(n, enumType, checkArm)
		return
	}

	// A bare untyped-constant subject (`match 5 { ... }`) defaults exactly
	// like any other context that provides no further type information at
	// all (see AGENTS.md's "Untyped numeric constants" section: no declared
	// type context anywhere for it to adapt to) - the resulting concrete
	// type then drives every arm's own pattern check uniformly, rather than
	// leaving it to whichever pattern happens to be checked first via
	// checkEqualityOperands' own equality-style resolution.
	if subjType.IsUntyped() {
		def := c.defaultUntyped(subjType)
		c.retypeUntyped(subjectNode, def)
		subjType = def
	}

	if !isValueMatchType(subjType) {
		c.errorAt(subjectNode, "match requires an enum value, or an int/bool/string value to switch on, got %s", subjType)
		for _, arm := range c.tree.MatchArms(n) {
			c.checkMatchArmFallback(arm, checkArm)
		}
		return
	}

	c.checkValueMatchStmt(n, subjectNode, subjType, checkArm)
}

// isValueMatchType reports whether t is a legal value-match subject type
// (see LANGUAGE.md's "match" section's plain-value-pattern extension): any
// int width, or bool/string - deliberately excluding f32/f64 (float
// equality is a footgun this language already avoids leaning into elsewhere
// - see DECISIONS.md's dated entry for this round) and every aggregate/
// reference type (struct, array, pointer, map, func - none of which have a
// scalar "leaf" equality that makes sense to switch on here; an enum
// subject never reaches this function at all, having already been routed to
// checkEnumMatchStmt by checkMatchStmt).
func isValueMatchType(t Type) bool {
	switch t.Kind {
	case TypeI8, TypeI16, TypeI32, TypeI64,
		TypeU8, TypeU16, TypeU32, TypeU64,
		TypeBool, TypeString:
		return true
	default:
		return false
	}
}

// checkEnumMatchStmt type-checks a match whose subject is an enum value (or
// a pointer to one) - the real, hard exhaustiveness check this feature
// exists to provide: every non-wildcard arm's pattern must name one of the
// matched enum's own declared variants (a pattern naming some other enum's
// variant, or a nonexistent one, is a clean diagnostic, not a panic - see
// checkMatchArmPattern), no variant may be matched by more than one arm, and
// either every variant is covered by some arm or a wildcard `_` arm is
// present. New this round: an enum-match arm may bind only ONE variant
// pattern - binding several differently-shaped variant patterns into one
// shared arm body (unifying their bindings) is a real, separate feature,
// deliberately deferred rather than silently only checking pattern 0 or
// guessing which variant's own shape the body's bindings should follow (see
// DECISIONS.md's dated entry for this round). checkArm is checkMatchDispatch's
// own "how do I check one arm's own body" callback (see its own doc comment).
func (c *checker) checkEnumMatchStmt(n ast.NodeIndex, enumType Type, checkArm func(body ast.NodeIndex)) {
	info := enumType.Enum
	covered := make(map[string]bool, len(info.Order))
	hasWildcard := false

	for _, arm := range c.tree.MatchArms(n) {
		patterns := c.tree.MatchArmPatterns(arm)
		body := c.tree.MatchArmBody(arm)

		if c.tree.IsWildcardMatchArm(arm) {
			if hasWildcard {
				c.errorAt(patterns[0], "match has more than one wildcard (_) arm")
			}
			hasWildcard = true
			checkArm(body)
			continue
		}

		if len(patterns) > 1 {
			c.errorAtNodes(patterns, arm, "an enum match arm may bind only one variant pattern, got %d", len(patterns))
			for _, pattern := range patterns {
				c.checkMatchArmPatternBindingsFallback(pattern)
			}
			checkArm(body)
			continue
		}

		variant, ok := c.checkMatchArmPattern(patterns[0], info)
		if ok {
			if covered[variant.Name] {
				c.errorAt(patterns[0], "variant %s.%s already matched by an earlier arm", info.Symbol.Name, variant.Name)
			}
			covered[variant.Name] = true
		}
		checkArm(body)
	}

	if hasWildcard {
		return
	}
	var missing []string
	for _, v := range info.Order {
		if !covered[v.Name] {
			missing = append(missing, v.Name)
		}
	}
	if len(missing) > 0 {
		c.errorAt(n, "match is not exhaustive: missing variant(s) %s of enum %s (add an arm for each, or a wildcard _ arm)", strings.Join(missing, ", "), info.Symbol.Name)
	}
}

// checkValueMatchStmt type-checks a match whose subject is a plain scalar
// value (int/bool/string - see isValueMatchType and LANGUAGE.md's "match"
// section's plain-value-pattern extension), Go-`switch`-style: every arm's
// every pattern is an ordinary value expression, checked for equality-
// comparability against the subject exactly like an ordinary `==` operand
// pair (checkEqualityOperands - untyped-literal defaulting against the
// subject's own type, then requiring the same concrete type). Unlike an
// enum match, there is no closed set of "variants" to exhaustively check an
// unbounded domain like int/string has none (the identical reasoning
// DECISIONS.md's own "why match is scoped to enum-variant patterns only"
// entry already gives) - so a wildcard `_` arm is instead made MANDATORY
// here, a deliberate stricter-than-Go choice: a value-match missing one is a
// clean compile error, unlike Go's own switch, which happily allows no
// `default` and no matching case to just silently fall through doing
// nothing (see DECISIONS.md's dated entry for this round for why match
// stays a real safety net instead of mirroring that particular Go
// looseness). checkArm is checkMatchDispatch's own "how do I check one arm's
// own body" callback (see its own doc comment).
func (c *checker) checkValueMatchStmt(n, subjectNode ast.NodeIndex, subjType Type, checkArm func(body ast.NodeIndex)) {
	hasWildcard := false
	seenLiterals := make(map[literalPatternKey]ast.NodeIndex)

	for _, arm := range c.tree.MatchArms(n) {
		body := c.tree.MatchArmBody(arm)

		if c.tree.IsWildcardMatchArm(arm) {
			if hasWildcard {
				c.errorAt(c.tree.MatchArmPatterns(arm)[0], "match has more than one wildcard (_) arm")
			}
			hasWildcard = true
			checkArm(body)
			continue
		}

		for _, pattern := range c.tree.MatchArmPatterns(arm) {
			patType := c.checkValueExpr(pattern)
			if !patType.IsInvalid() {
				c.checkEqualityOperands(pattern, subjectNode, pattern, subjType, patType, "==")
				c.checkDuplicateValuePattern(pattern, seenLiterals)
			}
		}
		checkArm(body)
	}

	if !hasWildcard {
		c.errorAt(n, "value match requires a wildcard _ arm (exhaustiveness cannot be checked for %s)", subjType)
	}
}

// literalPatternKey is checkDuplicateValuePattern's own dedupe key - a
// literal pattern's node kind (NumberLit/StringLit/BoolLit) paired with its
// raw source text, distinguishing e.g. the int literal `1` from the string
// literal `"1"` even though their Text() would otherwise collide.
type literalPatternKey struct {
	kind enums.NodeKind
	text string
}

// checkDuplicateValuePattern flags pattern as a duplicate case value when
// another pattern already seen this same match (tracked via seen) is the
// exact same literal node kind with identical source text - a nice-to-have,
// not a hard blocker (see checkValueMatchStmt's own doc comment): only a
// bare literal (NumberLit/StringLit/BoolLit) can be compared this way at
// compile time; anything computed (a variable reference, a binary
// expression, ...) is silently skipped, the same limitation Go's own switch
// has for the identical reason (can't be determined at compile time).
func (c *checker) checkDuplicateValuePattern(pattern ast.NodeIndex, seen map[literalPatternKey]ast.NodeIndex) {
	kind := c.tree.Nodes[pattern].Kind
	switch kind {
	case enums.NodeKinds.NumberLit, enums.NodeKinds.StringLit, enums.NodeKinds.BoolLit:
	default:
		return
	}
	key := literalPatternKey{kind: kind, text: c.tree.Text(pattern)}
	if _, ok := seen[key]; ok {
		c.errorAt(pattern, "duplicate match case %s (already matched by an earlier pattern)", c.tree.Text(pattern))
		return
	}
	seen[key] = pattern
}

// checkMatchArmFallback still type-checks every one of an arm's own pattern
// bindings and its body when the match's own subject type couldn't be
// determined at all - there's nothing to validate any pattern against, but
// its fresh bindings and body are still real code worth checking (mirroring
// checkCompositeLitElemFallback's identical reasoning one construct over).
// checkArm is checkMatchDispatch's own "how do I check one arm's own body"
// callback (see its own doc comment) - this recovery path needs its own
// expression-mode equivalent too (checkMatchExprArmBody still requires
// "every path yields" even when the subject itself couldn't be resolved),
// exactly like checkEnumMatchStmt/checkValueMatchStmt do.
func (c *checker) checkMatchArmFallback(arm ast.NodeIndex, checkArm func(body ast.NodeIndex)) {
	for _, pattern := range c.tree.MatchArmPatterns(arm) {
		c.checkMatchArmPatternBindingsFallback(pattern)
	}
	checkArm(c.tree.MatchArmBody(arm))
}

// checkMatchArmPatternBindingsFallback seeds pattern's own fresh binding
// names (if it's a tuple-/struct-variant-shaped pattern) with invalidType,
// without validating pattern against any particular enum's own variant
// catalog - shared by checkMatchArmFallback (the match's own subject type
// couldn't be determined at all) and checkEnumMatchStmt's own >1-pattern
// rejection (binding-unification across several variant patterns sharing
// one arm is out of scope this round - see DECISIONS.md - so nothing here
// attempts to give these bindings a real type either, just enough of a
// fallback that a later reference inside the arm's own body doesn't cascade
// into a second, unrelated "undefined" diagnostic).
func (c *checker) checkMatchArmPatternBindingsFallback(pattern ast.NodeIndex) {
	switch c.tree.Nodes[pattern].Kind {
	case enums.NodeKinds.CallExpr:
		for _, b := range c.tree.Children(pattern)[1:] {
			c.checkPatternBindingFallback(b)
		}
	case enums.NodeKinds.CompositeLit:
		_, elems := c.tree.CompositeLitElems(pattern)
		for _, e := range elems {
			if c.tree.IsKeyedElement(e) {
				c.checkPatternBindingFallback(c.tree.Child(e, 1))
			}
		}
	}
}

// checkMatchArmPattern type-checks one non-wildcard arm's own pattern
// against info (the matched enum's own catalog): resolves it to one of
// info's declared variants (checkedPatternVariant - a pattern naming a
// variant belonging to some *other* enum type, or a nonexistent variant
// name, is a clean diagnostic here, not a panic), checks its own shape
// (unit/tuple/struct) actually matches that variant's own declared kind, and
// - for a tuple/struct pattern - seeds each fresh binding name's own Type
// directly (seedPatternBinding, mirroring checkMultiShortVarDeclNode's
// identical "no single declaring node" seeding one level up), so codegen's
// own g.info.Types lookup for each binding just works like any other checked
// declaration.
func (c *checker) checkMatchArmPattern(pattern ast.NodeIndex, info *EnumInfo) (*EnumVariant, bool) {
	switch c.tree.Nodes[pattern].Kind {
	case enums.NodeKinds.MemberExpr:
		variant, ok := c.checkedPatternVariant(pattern, info)
		if !ok {
			return nil, false
		}
		if variant.Kind != EnumVariantUnit {
			c.errorAt(pattern, "%s.%s requires a pattern with arguments (it is not a unit variant)", info.Symbol.Name, variant.Name)
		}
		return variant, true

	case enums.NodeKinds.CallExpr:
		children := c.tree.Children(pattern)
		callee, bindings := children[0], children[1:]
		variant, ok := c.checkedPatternVariant(callee, info)
		if !ok {
			for _, b := range bindings {
				c.checkPatternBindingFallback(b)
			}
			return nil, false
		}
		if variant.Kind != EnumVariantTuple {
			c.errorAt(pattern, "%s.%s is not a tuple variant", info.Symbol.Name, variant.Name)
			for _, b := range bindings {
				c.checkPatternBindingFallback(b)
			}
			return variant, true
		}
		if len(bindings) != len(variant.Tuple) {
			c.errorAtNodes(bindings, pattern, "%s.%s has %d associated value(s), pattern binds %d", info.Symbol.Name, variant.Name, len(variant.Tuple), len(bindings))
		}
		for i, b := range bindings {
			if c.tree.Nodes[b].Kind != enums.NodeKinds.Ident {
				c.errorAt(b, "match pattern binding must be a plain identifier")
				continue
			}
			if i >= len(variant.Tuple) {
				c.seedPatternBinding(b, invalidType)
				continue
			}
			c.seedPatternBinding(b, variant.Tuple[i])
		}
		return variant, true

	case enums.NodeKinds.CompositeLit:
		typeExpr, elems := c.tree.CompositeLitElems(pattern)
		variant, ok := c.checkedPatternVariant(typeExpr, info)
		if !ok {
			for _, e := range elems {
				if c.tree.IsKeyedElement(e) {
					c.checkPatternBindingFallback(c.tree.Child(e, 1))
				}
			}
			return nil, false
		}
		if variant.Kind != EnumVariantStruct {
			c.errorAt(pattern, "%s.%s is not a struct variant", info.Symbol.Name, variant.Name)
			return variant, true
		}
		seen := make(map[string]bool)
		for _, e := range elems {
			if !c.tree.IsKeyedElement(e) {
				c.errorAt(e, "a struct-variant pattern requires keyed fields (field: name), not a positional element")
				continue
			}
			key := c.tree.Child(e, 0)
			value := c.tree.Child(e, 1)
			if c.tree.Nodes[key].Kind != enums.NodeKinds.Ident {
				c.errorAt(key, "field name must be an identifier")
				c.checkPatternBindingFallback(value)
				continue
			}
			name := c.tree.Text(key)
			field, ok := variant.FieldByName(name)
			if !ok {
				c.errorAt(key, "%s.%s has no field %s", info.Symbol.Name, variant.Name, name)
				c.checkPatternBindingFallback(value)
				continue
			}
			c.info.Refs[key] = field.Sym
			if seen[name] {
				c.errorAt(key, "field %s specified twice", name)
			}
			seen[name] = true

			if c.tree.Nodes[value].Kind != enums.NodeKinds.Ident {
				c.errorAt(value, "match pattern binding must be a plain identifier")
				continue
			}
			c.seedPatternBinding(value, field.Type)
		}
		return variant, true

	default:
		c.errorAt(pattern, "invalid match pattern")
		return nil, false
	}
}

// checkedPatternVariant resolves memberNode (a pattern's own EnumName.Variant
// reference - already resolved by Resolve, resolveEnumVariantRef, to a
// SymEnumVariant symbol) and requires it to actually belong to info, the
// enum the enclosing match statement is matching against - a pattern naming
// a variant of some *other* declared enum is rejected right here with a
// clean diagnostic, exactly as LANGUAGE.md's "match" section requires.
func (c *checker) checkedPatternVariant(memberNode ast.NodeIndex, info *EnumInfo) (*EnumVariant, bool) {
	sym, ok := c.info.Refs[memberNode]
	if !ok || sym.Kind != SymEnumVariant {
		return nil, false // already reported by Resolve
	}
	if sym.EnumInfo != info {
		c.errorAt(memberNode, "%s.%s is not a variant of %s", sym.EnumInfo.Symbol.Name, sym.Variant.Name, info.Symbol.Name)
		return nil, false
	}
	return sym.Variant, true
}

// seedPatternBinding directly seeds n's (a match pattern's own fresh binding
// Ident's) Type into both declType's memoization cache and info.Types -
// mirroring checkMultiShortVarDeclNode's identical "no single declaring node
// of its own" seeding (see that function's own doc comment): a pattern
// binding's Decl is the same bare Ident node Resolve declared it against
// (see resolve.go's declarePatternBinding), which computeDeclType's own
// switch has no case for, so nothing would ever compute its Type lazily on
// its own - this closes that gap unconditionally, the moment the pattern
// itself is checked, regardless of whether the binding is ever referenced
// again inside its arm's own body.
func (c *checker) seedPatternBinding(n ast.NodeIndex, t Type) {
	c.declTypes[nodeRef{c.tree, n}] = t
	c.info.Types[n] = t
	c.recordLocalDeclLoopDepth(n)
}

// checkPatternBindingFallback seeds n with invalidType when it's at least a
// plain identifier (so a later reference inside the arm's own body doesn't
// cascade into a second, unrelated "undefined" diagnostic) - used wherever a
// pattern's own variant reference itself failed to resolve, so there's
// nothing real to bind each name's Type against.
func (c *checker) checkPatternBindingFallback(n ast.NodeIndex) {
	if c.tree.Nodes[n].Kind == enums.NodeKinds.Ident {
		c.seedPatternBinding(n, invalidType)
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
// ExprStmt case) and a range-for's own subject (see checkRangeForSubjectExpr).
// A void result (a call to a function with no declared return type) is valid
// in the former and nowhere else; a TypeGenerator result is valid in the
// latter and nowhere else.
func (c *checker) checkValueExpr(n ast.NodeIndex) Type {
	return c.checkValueExprAllowGenerator(n, false)
}

// checkRangeForSubjectExpr is checkValueExpr with exactly one exception: a
// TypeGenerator result is let through instead of rejected - see
// checkRangeForStmt, the one legal position a generator call's own result may
// appear in (LANGUAGE.md's "Generator functions" section).
func (c *checker) checkRangeForSubjectExpr(n ast.NodeIndex) Type {
	return c.checkValueExprAllowGenerator(n, true)
}

func (c *checker) checkValueExprAllowGenerator(n ast.NodeIndex, allowGenerator bool) Type {
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
	if !allowGenerator && c.rejectGeneratorValue(n, t) {
		return invalidType
	}
	return t
}

// rejectGeneratorValue reports (and reports a diagnostic for) whether t is a
// generator call's result (TypeGenerator - see LANGUAGE.md's "Generator
// functions" section) reaching a position other than the one it's legal in.
// Shared by checkValueExprAllowGenerator's own gate and checkStmt's ExprStmt
// case (a bare `Gen(...)` statement, discarding the result - the other
// position a generator's result could otherwise leak out uncaught, since
// ExprStmt intentionally calls checkExpr directly to let a genuine void
// result through).
func (c *checker) rejectGeneratorValue(n ast.NodeIndex, t Type) bool {
	if t.Kind != TypeGenerator {
		return false
	}
	c.errorAt(n, "a generator call's result can only be used directly as a range-for's own subject (for v := range Gen(...) { ... }); it cannot be stored, passed as an argument, returned, or discarded")
	return true
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
	case enums.NodeKinds.MoveExpr:
		return c.checkMoveExpr(n)
	case enums.NodeKinds.MatchStmt:
		// Always expression-position here - statement-position match is
		// dispatched by checkStmt directly, never through checkExpr.
		return c.checkMatchExprStmt(n)
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
	case enums.NodeKinds.RangeExpr:
		// Reachable only when `range` appears somewhere other than directly
		// as a for-loop header's value (parser/stmt.go's finishRangeForStmt
		// already unwraps and consumes a range-for's own RangeExpr before it
		// ever reaches checkExpr - see LANGUAGE.md's "Range loops" section) -
		// e.g. a bare `x := range m` statement, or `range m` nested inside
		// another expression. Still checks the subject, so a later reference
		// to it doesn't cascade into an unrelated diagnostic.
		c.checkValueExpr(c.tree.Child(n, 0))
		c.errorAt(n, "range is only valid directly in a for-loop header (for k, v := range x { ... })")
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
	if c.move != nil {
		if _, moved := c.move.moved[sym]; moved {
			c.errorAt(n, "use of moved value %s: it was moved earlier and no longer has a value", sym.Name)
			return invalidType
		}
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
	if sym.Generic != nil {
		// A template has no signature/layout of its own - only its
		// specializations do (see LANGUAGE.md's "Generics" section).
		c.errorAt(n, "%s is generic and cannot be used as a value; instantiate it, e.g. %s[%s]",
			sym.Name, sym.Name, strings.Join(sym.Generic.Params, ", "))
		return invalidType
	}
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
		if sig.Variadic {
			// See LANGUAGE.md's "Variadic parameters" section: out of scope
			// this round - a variadic function's own real call-site sugar
			// (collect/spread) only exists for a direct call, never for an
			// indirect one through a plain func(...)-typed value.
			c.errorAt(n, "%s is a variadic function and cannot be used as a value, only called directly", c.tree.Text(n))
			return invalidType
		}
		return funcType(sig)
	case SymStruct, SymBuiltinType, SymEnum:
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
// sym.Decl is that struct's (or enum's - see below) own StructDecl/EnumDecl
// node (see resolve.go's fnScope.Receiver construction), not a variable
// declaration, so this doesn't go through declType.
func (c *checker) checkThisExpr(n ast.NodeIndex) Type {
	sym, ok := c.info.Refs[n]
	if !ok {
		return invalidType // "this outside a method"; already reported by Resolve
	}
	// The receiver's catalog comes straight off the symbol (see
	// receiverSymbol, resolve.go) rather than a name lookup - a monomorphized
	// struct's catalog isn't reachable by its declaration's source text at all.
	if sym.StructInfo != nil {
		return Type{
			Kind: TypePointer,
			Elem: &Type{
				Kind:   TypeStruct,
				Struct: sym.StructInfo,
			},
		}
	}
	if sym.EnumInfo != nil {
		return Type{
			Kind: TypePointer,
			Elem: &Type{
				Kind: TypeEnum,
				Enum: sym.EnumInfo,
			},
		}
	}
	return invalidType
}

// checkMoveExpr type-checks `move x` (see LANGUAGE.md's "Destructors"
// section's "move" subsection). The parser already guarantees operand is a
// bare Ident (parseMoveExpr) - the non-Ident branch here only handles a
// malformed tree reaching this some other way, without re-reporting.
//
// Moving a COPYABLE-typed x is accepted as a harmless plain read - no
// ownership to track (see DECISIONS.md's dated entry). For a non-copyable
// x, this is the one place moveState.moved is actually populated: x must
// name a local variable/parameter (checkIdentExpr, called via
// checkValueExpr below, already rejects x if it was moved on some earlier,
// unconditional path), never one captured by a lambda (Symbol.Captured - a
// captured variable's lifetime already outlives straight-line tracking,
// out of scope this round), and must not have been declared outside the
// current innermost loop (declLoopDepth - see moveState's own doc comment:
// a later iteration of that loop could then move an already-moved value).
func (c *checker) checkMoveExpr(n ast.NodeIndex) Type {
	operand := c.tree.Child(n, 0)
	if c.tree.Nodes[operand].Kind != enums.NodeKinds.Ident {
		c.checkValueExpr(operand)
		return invalidType // already reported by the parser
	}

	t := c.checkValueExpr(operand)
	sym, ok := c.info.Refs[operand]
	if !ok || t.IsInvalid() {
		return invalidType // already reported by Resolve/checkIdentExpr
	}
	if sym.Kind != SymVar && sym.Kind != SymParam {
		c.errorAt(operand, "move requires a local variable or parameter, got %s", sym.Kind)
		return invalidType
	}
	if !c.typeIsNonCopyable(t) {
		return t
	}
	if sym.Captured {
		c.errorAt(operand, "cannot move %s: it is captured by a lambda", sym.Name)
		return t
	}
	if c.move.declLoopDepth[sym] < c.loopDepth {
		c.errorAt(operand, "cannot move %s inside a loop: it was declared outside this loop, and a later iteration could move an already-moved value", sym.Name)
		return t
	}
	c.move.moved[sym] = true
	return t
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
		if t.IsNumeric() {
			return t
		}
		// Fallback path (see LANGUAGE.md's "Operator overloading" section):
		// tried only once the existing numeric rule above already failed to
		// claim n. A struct with no unary `-` overload of its own falls
		// through unclaimed, to the exact same diagnostic below a
		// non-numeric, non-struct operand already got before this feature
		// existed - the regression case LANGUAGE.md documents explicitly.
		if result, ok := c.checkUnaryOperatorOverload(n, t, op); ok {
			return result
		}
		c.errorAt(n, "operator - not defined for %s", t)
		return invalidType
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
		if targetType.Kind == TypeMap {
			// A map value is never addressable - mirroring Go's own real
			// "cannot take the address of a map index expression" rule (see
			// LANGUAGE.md's "Maps" section): unlike a fixed-size array
			// element (inline storage inside the array's own addressable
			// backing), a map slot doesn't necessarily exist at all until an
			// actual insert runs, so there's no stable address to hand back
			// regardless of whether the map variable itself is addressable.
			return false
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
		// Fallback path (see LANGUAGE.md's "Operator overloading" section):
		// tried only once the existing string/numeric rules above already
		// failed to claim n - an operator overload never replaces either,
		// it's a new case tried after both.
		if result, ok := c.checkBinaryOperatorOverload(n, rNode, lt, rt, op); ok {
			return result
		}
		if lt.Kind == TypeStruct {
			c.errorAt(n, "no operator + overload on %s for argument type %s", lt, rt)
			return invalidType
		}
		c.errorAt(n, "operator + not defined for %s and %s", lt, rt)
		return invalidType
	case "-", "*", "/":
		if result, ok := c.checkNumericBinary(lNode, rNode, lt, rt, false); ok {
			return result
		}
		if result, ok := c.checkBinaryOperatorOverload(n, rNode, lt, rt, op); ok {
			return result
		}
		// A struct LHS with no matching overload gets its own wording naming
		// the actual problem (see LANGUAGE.md) - reusing the "requires
		// numeric operands" wording below would misdescribe why this
		// genuinely failed. A non-struct LHS (including the reverse,
		// scalar-on-the-left case, `2.0 * v` - see LANGUAGE.md's "left-
		// operand-only dispatch" note) keeps that existing wording
		// unchanged: from this check's own perspective, it's simply not
		// numeric, exactly as before this feature existed.
		if lt.Kind == TypeStruct {
			c.errorAt(n, "no operator %s overload on %s for argument type %s", op, lt, rt)
			return invalidType
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

// checkUnaryOperatorOverload resolves n (a UnaryExpr for operator op)
// against t's own declared unary `operator op()` overload, if t is a struct
// declaring one (see LANGUAGE.md's "Operator overloading" section) -
// checkUnaryExpr's own fallback, tried only once the existing numeric rule
// has already failed to claim n. Reports ok=false (claiming nothing) when t
// isn't a struct, or is a struct with no unary overload for op - the
// caller's own pre-existing diagnostic fires unchanged in that case.
func (c *checker) checkUnaryOperatorOverload(n ast.NodeIndex, t Type, op string) (Type, bool) {
	if t.Kind != TypeStruct {
		return invalidType, false
	}
	set, ok := t.Struct.Operators[op]
	if !ok || set.Unary == nil {
		return invalidType, false
	}
	restore := c.pushTree(set.Unary.Tree)
	sig := c.operatorSigForDecl(set.Unary.Decl)
	restore()

	c.info.Refs[n] = set.Unary
	c.info.Types[n] = sig.Return
	return sig.Return, true
}

// checkBinaryOperatorOverload resolves n (a BinaryExpr for operator op)
// against lt's own declared binary `operator op(param) RetType` overload
// set, if lt is a struct declaring one (see LANGUAGE.md's "Operator
// overloading" section) - checkBinaryExpr's own fallback, tried only once
// the existing string/numeric handling has already failed to claim n.
// Reports ok=false (claiming nothing) when lt isn't a struct, or is a
// struct with no overload for op whose declared parameter type accepts rt -
// the caller's own diagnostic (naming the actual problem for a struct LHS,
// vs. the pre-existing wording otherwise) fires unchanged in that case.
//
// rt selects among lt's possibly several same-token overloads by testing
// each one's own declared parameter type against rt in declaration order via
// operandTypeMatches - a silent, side-effect-free compatibility check
// (unlike checkAssignable, which both retypes an untyped operand and reports
// a diagnostic on mismatch) - so probing a non-matching candidate along the
// way never emits a spurious error or retypes rNode's own untyped literal
// before the real winner is known. Once a match is found, the *real*
// checkAssignable then runs against that one candidate only (this always
// succeeds, since operandTypeMatches already confirmed compatibility) to get
// its side effects: retyping an untyped rNode, and (via checkNoIllegalCopy)
// the same by-value-argument copy restriction an ordinary call's arguments
// already get (see checkConstructorCall).
func (c *checker) checkBinaryOperatorOverload(n, rNode ast.NodeIndex, lt, rt Type, op string) (Type, bool) {
	if lt.Kind != TypeStruct {
		return invalidType, false
	}
	set, ok := lt.Struct.Operators[op]
	if !ok {
		return invalidType, false
	}
	for _, overload := range set.Binary {
		restore := c.pushTree(overload.Symbol.Tree)
		sig := c.operatorSigForDecl(overload.Symbol.Decl)
		restore()

		if !operandTypeMatches(sig.Params[0], rt) {
			continue
		}
		if c.checkAssignable(rNode, sig.Params[0], rt, "right operand") {
			c.checkNoIllegalCopy(rNode, sig.Params[0], true, "right operand")
		}
		c.info.Refs[n] = overload.Symbol
		c.info.Types[n] = sig.Return
		return sig.Return, true
	}
	return invalidType, false
}

// operandTypeMatches silently reports whether got is compatible with want
// (an operator overload candidate's own declared parameter type) - the same
// decision checkAssignable makes, minus its side effects (retyping an
// untyped operand, reporting a diagnostic on mismatch), since this only
// ranks candidate overloads and must not disturb the AST or emit a
// diagnostic for every candidate that isn't the eventual winner (see
// checkBinaryOperatorOverload's own doc comment).
func operandTypeMatches(want, got Type) bool {
	if got.Kind == TypeUntypedNil {
		return want.Kind == TypePointer
	}
	if got.IsUntyped() {
		if !want.IsNumeric() {
			return false
		}
		return got.Kind != TypeUntypedFloat || !want.IsIntegerKind()
	}
	return want.Equal(got)
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
//
// The struct/array case below also runs typeIsComparable(lt) alongside
// Type.Equal - Type.Equal alone only confirms both operands share the same
// aggregate type, it says nothing about whether every field/element nested
// inside that type is itself something codegen's genValueEqual (expr.go) can
// actually lower (a map or function-typed field, or a dynamic-array field
// nested arbitrarily deep, none of which the top-level Dynamic check above
// can see). Without this, a struct containing a map/func field reached
// genValueEqual's own unsupported-type panic, and a struct containing a
// dynamic-array field silently compared as always-equal on that field
// (genValueEqual's array case loops to t.Size, which a dynamic array never
// sets) - both now get a clean diagnostic here instead.
func (c *checker) checkEqualityOperands(n, lNode, rNode ast.NodeIndex, lt, rt Type, op string) Type {
	if (lt.Kind == TypeArray && lt.Dynamic) || (rt.Kind == TypeArray && rt.Dynamic) {
		c.errorAt(n, "slices are not comparable with %s", op)
		return invalidType
	}
	if lt.Kind == TypeUntypedNil || rt.Kind == TypeUntypedNil {
		return c.checkNilEquality(n, lNode, rNode, lt, rt, op)
	}
	switch {
	case lt.Kind == TypeStruct, lt.Kind == TypeArray, lt.Kind == TypeEnum:
		if lt.Equal(rt) {
			if !c.typeIsComparable(lt) {
				c.errorAt(n, "cannot compare values of type %s: %s is not comparable (a dynamic array, function type, or map cannot appear in an equality comparison, even nested inside a struct, array, or enum)", lt, lt)
				return invalidType
			}
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

	// The `Foo[T]` / `arr[i]` shape collision is decided here, once: a target
	// naming a generic declaration means instantiation, everything else means
	// indexing. Reaching this in value position means the instantiation isn't
	// being called or constructed - a type, used as a value.
	if gi, ok := c.genericRef(target); ok {
		c.errorAt(n, "an instantiation of %s is not a value here - call it (%s[...](args)) or construct it (%s[...]{...})",
			gi.Symbol.Name, gi.Symbol.Name, gi.Symbol.Name)
		return invalidType
	}

	tt := c.checkValueExpr(target)

	// A map index (`m[k]`) checks its key against the map's own declared key
	// type instead of requiring a plain int (see LANGUAGE.md's "Maps"
	// section) - a plain single-target `x := m[k]` reads V, missing-key
	// returning V's own zero value at runtime (see CODEGEN.md); the "two-
	// result index expression" form (`v, ok := m[k]`) is a context-dependent
	// special case handled entirely in checkDestructureSource instead, not
	// here - this always yields just V, the ordinary single-value case.
	if tt.Kind == TypeMap {
		c.checkMapIndexKey(index, tt)
		return *tt.Elem
	}

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

// checkMapIndexKey type-checks a map index expression's key operand (`m[k]`)
// against mapType's own declared key type, adapting an untyped constant
// exactly like any other position (checkAssignable) - shared by
// checkIndexExpr's own map case and checkRemoveCall's own key argument.
func (c *checker) checkMapIndexKey(index ast.NodeIndex, mapType Type) {
	it := c.checkValueExpr(index)
	if it.IsInvalid() {
		return
	}
	c.checkAssignable(index, *mapType.Key, it, "map index")
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
	if sym.Kind == SymEnumVariant {
		// A bare, uncalled EnumName.Variant reference (see LANGUAGE.md's
		// "Enums" section: "unit variant: bare value... a MemberExpr naming a
		// variant with no associated data at all") - only ever legal for a
		// unit variant; a tuple/struct variant referenced this way (with no
		// call or composite-literal body) has associated data it never
		// supplied.
		if sym.Variant.Kind != EnumVariantUnit {
			c.errorAt(n, "%s.%s requires arguments to construct (it is not a unit variant)", sym.EnumInfo.Symbol.Name, sym.Variant.Name)
			return invalidType
		}
		return Type{Kind: TypeEnum, Enum: sym.EnumInfo}
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
// Memoized on Info.Refs[n] for a success and on checker.memberFailed for a
// failure - several call sites can legitimately ask about the same callee
// node in one Check pass (checkGenericCall's own is-this-generic test,
// methodSigForCallee's struct-field fallback, funcSigForCall's indirect-call
// check), and each must see the same answer reported exactly once.
func (c *checker) resolveMember(n ast.NodeIndex) (*Symbol, bool) {
	if sym, ok := c.info.Refs[n]; ok {
		return sym, true
	}
	key := nodeRef{c.tree, n}
	if c.memberFailed[key] {
		return nil, false
	}
	sym, ok := c.resolveMemberUncached(n)
	if !ok {
		c.memberFailed[key] = true
	}
	return sym, ok
}

func (c *checker) resolveMemberUncached(n ast.NodeIndex) (*Symbol, bool) {
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
	// A pointer-to-struct/enum object auto-derefs for member access (see
	// LANGUAGE.md's "Pointers" section): `p.field`/`p.method(...)` on a `*T`
	// behaves exactly like `(*p).field`/`(*p).method(...)` would, matching
	// Go's own automatic pointer-dereference rule for selector expressions.
	// codegen's genAddr/genMethodCall mirror this same auto-deref at the
	// value-address level.
	objType = objType.Underlying()

	var found *Symbol
	switch objType.Kind {
	case TypeStruct:
		info := objType.Struct
		if sym, ok := info.Fields[name]; ok {
			found = sym
		} else if sym, ok := info.Methods[name]; ok {
			found = sym
		} else {
			c.errorAt(n, "%s has no field or method %s", info.Symbol.Name, name)
			return nil, false
		}
	case TypeEnum:
		// An enum value has no exposed fields at all outside `match` (see
		// LANGUAGE.md's "Enums" section) - only a method call is ever legal
		// here.
		info := objType.Enum
		if sym, ok := info.Methods[name]; ok {
			found = sym
		} else {
			c.errorAt(n, "%s has no method %s", info.Symbol.Name, name)
			return nil, false
		}
	default:
		c.errorAt(n, "%s undefined (%s is not a struct or enum)", name, objType)
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
// a free function, a method call (`p.translate()`), or an indirect call through
// a function-typed value (`fn(1, 2)` where fn is a variable/parameter, or
// any other expression whose value is itself a function - see
// funcSigForCall). Argument count and each argument's type are checked
// against the resolved callee's signature the same way regardless of which
// of those a given call turns out to be.
func (c *checker) checkCallExpr(n ast.NodeIndex) Type {
	children := c.tree.Children(n)
	callee, args := children[0], children[1:]

	// A spread argument (`f(x...)` - see LANGUAGE.md's "Variadic parameters"
	// section) only ever makes sense reaching an ordinary or generic direct
	// function call, both of which route through checkCallArgs below and
	// check it there against that callee's own real Variadic-ness. Every
	// other callable shape below (a builtin, a conversion, a constructor, an
	// enum variant) can never be variadic, so a spread there is diagnosed
	// right here instead of silently accepted.
	if c.tree.CallHasSpread(n) && c.calleeNeverVariadic(callee) {
		c.errorAt(n, "... (spread) is only legal in a call to a variadic function")
		for _, a := range args {
			c.checkValueExpr(a)
		}
		return invalidType
	}

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
	if c.isBuiltinCall(callee, "remove") {
		return c.checkRemoveCall(n, args)
	}
	if c.isBuiltinCall(callee, "resume") {
		return c.checkResumeCall(n, args)
	}
	if c.isBuiltinCall(callee, "done") {
		return c.checkDoneCall(n, args)
	}
	if c.isBuiltinCall(callee, "AnyKind") {
		return c.checkAnyKindCall(n, args)
	}
	if c.isBuiltinCall(callee, "AnyName") {
		return c.checkAnyNameCall(n, args)
	}
	if c.isBuiltinCall(callee, "AnyFields") {
		return c.checkAnyFieldsCall(n, args)
	}
	if t, ok := c.checkConstructorCall(n, callee, args); ok {
		return t
	}
	if t, ok := c.checkEnumVariantCall(n, callee, args); ok {
		return t
	}
	if t, ok := c.checkConversionCall(n, callee, args); ok {
		return t
	}
	if t, ok := c.checkAnyAsCall(n, callee, args); ok {
		return t
	}
	if t, ok := c.checkGenericCall(n, callee, args); ok {
		return t
	}

	sig, ok := c.funcSigForCall(callee)
	if !ok {
		for _, a := range args {
			c.checkValueExpr(a)
		}
		return invalidType
	}

	c.checkCallArgs(n, args, nil, sig)

	// Calling an async func (see LANGUAGE.md's "Coroutines" section)
	// produces a coroutine handle, not its own declared (void, this round)
	// signature return type directly - the same "wrap the callee's ordinary
	// signature into a special result Type" shape a generator call needs no
	// equivalent of, since a generator's own declared return type already
	// IS TypeGenerator (see checkFuncDecl); async's marker lives on the
	// FuncDecl itself instead (Tree.FuncIsAsync), not the return-type
	// position, so it's the call site, not funcSigForCall, that wraps it.
	if c.calleeIsAsyncFunc(callee) {
		ret := sig.Return
		return Type{Kind: TypeCoroutine, Elem: &ret}
	}
	return sig.Return
}

// checkCallArgs checks a call's argument count and each argument's type
// against sig. argTypes, when non-nil, is each argument's already-computed
// type (checkExpr isn't memoized, so a caller that had to type the arguments
// before it could pick the callee - a generic call's own inference - passes
// them here rather than re-checking and double-reporting).
func (c *checker) checkCallArgs(n ast.NodeIndex, args []ast.NodeIndex, argTypes []Type, sig funcSignature) {
	argType := func(i int) Type {
		if argTypes != nil {
			return argTypes[i]
		}
		return c.checkValueExpr(args[i])
	}

	if sig.Variadic {
		c.checkVariadicCallArgs(n, args, argType, sig)
		return
	}

	if c.tree.CallHasSpread(n) {
		c.errorAt(n, "... (spread) is only legal in a call to a variadic function")
		for i := range args {
			argType(i)
		}
		return
	}

	if len(args) != len(sig.Params) {
		c.errorAtNodes(args, n, "wrong number of arguments in call: got %d, want %d", len(args), len(sig.Params))
		for i := range args {
			argType(i)
		}
		return
	}

	for i, a := range args {
		at := argType(i)
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
}

// checkVariadicCallArgs is checkCallArgs's own variadic-signature case (see
// LANGUAGE.md's "Variadic parameters" section): every fixed leading
// parameter checks exactly like an ordinary call, then the trailing
// arguments from the variadic position onward are handled one of two ways -
// a spread call (Tree.CallHasSpread) requires exactly one trailing argument,
// checked against the declared `[]T` parameter type itself (so its own type
// must be EXACTLY []T, the same no-implicit-conversion rule checkAssignable
// already enforces everywhere else); otherwise each trailing argument is
// checked individually against T, the element type, and collected at the
// call site (see codegen's genCallArgValues).
//
// When T is Any, collection is the one deliberate exception to this
// language's no-implicit-conversion rule: each trailing argument is boxed
// automatically (see isBoxableIntoAny) rather than requiring `Any(x)` at
// every call site - the same real-world justification Go's own `...any`
// variadics have. This is scoped to variadic collection specifically, not a
// general assignability change - an ordinary `var x Any = 5` still needs an
// explicit `Any(5)`.
func (c *checker) checkVariadicCallArgs(n ast.NodeIndex, args []ast.NodeIndex, argType func(int) Type, sig funcSignature) {
	fixed := sig.Params[:len(sig.Params)-1]
	sliceType := sig.Params[len(sig.Params)-1]
	elemType := *sliceType.Elem

	if len(args) < len(fixed) {
		c.errorAtNodes(args, n, "wrong number of arguments in call: got %d, want at least %d", len(args), len(fixed))
		for i := range args {
			argType(i)
		}
		return
	}

	for i, p := range fixed {
		at := argType(i)
		if c.checkAssignable(args[i], p, at, fmt.Sprintf("argument %d", i+1)) {
			c.checkNoIllegalCopy(args[i], p, true, fmt.Sprintf("argument %d", i+1))
		}
	}

	tail := args[len(fixed):]
	spread := c.tree.CallHasSpread(n)
	if spread && len(tail) != 1 {
		c.errorAtNodes(tail, n, "... (spread) requires a single argument for the variadic parameter, got %d", len(tail))
		spread = false
	}

	if spread {
		i := len(fixed)
		at := argType(i)
		if c.checkAssignable(args[i], sliceType, at, fmt.Sprintf("argument %d", i+1)) {
			c.checkNoIllegalCopy(args[i], sliceType, true, fmt.Sprintf("argument %d", i+1))
		}
		return
	}

	for j, a := range tail {
		i := len(fixed) + j
		at := argType(i)
		if elemType.Kind == TypeAny && at.Kind != TypeAny {
			c.checkBoxableIntoAny(a, at)
			continue
		}
		if c.checkAssignable(a, elemType, at, fmt.Sprintf("argument %d", i+1)) {
			c.checkNoIllegalCopy(a, elemType, true, fmt.Sprintf("argument %d", i+1))
		}
	}
}

// checkBoxableIntoAny is the shared `Any(x)` boxing check (isBoxableIntoAny/
// typeIsNonCopyable/untyped-defaulting) - both an explicit `Any(x)`
// conversion (checkConversionCall) and implicit collect-time boxing into a
// `...Any` variadic parameter (checkVariadicCallArgs) enforce the exact same
// rules through this one function. Reports whether at may be boxed at all.
func (c *checker) checkBoxableIntoAny(a ast.NodeIndex, at Type) bool {
	if !c.isBoxableIntoAny(at) {
		c.errorAt(a, "cannot box %s into Any - enums, arrays, maps, function values, and multi-value/generator/coroutine results have no Any representation this round (a struct is only boxable if every one of its own fields is)", at)
		return false
	}
	if c.typeIsNonCopyable(at) {
		c.errorAt(a, "cannot box non-copyable type %s into Any - Any(x) always copies x's bytes, which isn't sound for a type that isn't copyable", at)
		return false
	}
	if at.IsUntyped() {
		c.retypeUntyped(a, c.defaultUntyped(at))
	}
	return true
}

// calleeNeverVariadic reports whether callee names a callable form that can
// never be variadic - a builtin (print/make/append/len/args/remove/resume/
// done), an explicit conversion, a struct constructor, or an enum variant
// construction - none of which ever reach checkCallArgs' own Variadic check
// (see checkCallExpr's own spread guard, above). A callee that isn't one of
// these (an ordinary or generic function/method, still unresolved, or
// invalid) reports false, deferring to checkCallArgs.
func (c *checker) calleeNeverVariadic(callee ast.NodeIndex) bool {
	if c.isPrintCall(callee) {
		return true
	}
	for _, name := range [...]string{"make", "append", "len", "args", "remove", "resume", "done"} {
		if c.isBuiltinCall(callee, name) {
			return true
		}
	}
	sym, ok := c.info.Refs[callee]
	if !ok || sym.Generic != nil {
		return false
	}
	switch sym.Kind {
	case SymBuiltinType, SymStruct, SymEnumVariant:
		return true
	default:
		return false
	}
}

// calleeIsAsyncFunc reports whether callee names a declared async func (see
// LANGUAGE.md's "Coroutines" section) - async functions are top-level-only
// this round (no closures/FuncLit - see LANGUAGE.md), so callee is always a
// plain Ident resolving to a SymFunc with a real FuncDecl when this is true.
func (c *checker) calleeIsAsyncFunc(callee ast.NodeIndex) bool {
	if c.tree.Nodes[callee].Kind != enums.NodeKinds.Ident {
		return false
	}
	sym, ok := c.info.Refs[callee]
	if !ok || sym.Kind != SymFunc || sym.Decl == ast.InvalidNode {
		return false
	}
	return sym.Tree.FuncIsAsync(sym.Decl)
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

// checkPrintCall accepts exactly one argument, of any printable type - print
// has no declaration to derive a stricter signature from, and AGENTS.md's
// examples use it on both int and string arguments interchangeably (see
// AGENTS.md's Operators section and BLOCKERS.md for this decision). An
// untyped argument (a bare numeric literal, e.g. `print(42)`) defaults like
// any other value context with no other type to adapt to (defaultIfUntyped)
// - print still needs a concrete numeric type to pick a format specifier
// from (see codegen's genPrintCall).
//
// "Any type" was never quite true, though: codegen's genPrintCall/
// genPrintValueBare only ever implemented a fixed set of Kinds and panicked
// on everything else (a function value, a map value, or either one nested
// inside a struct field, all reached codegen's own unsupported-type panic
// with zero diagnostic first) - typeIsPrintable is the real, narrower rule
// enforced here instead, closing that gap with a clean compile-time error.
func (c *checker) checkPrintCall(n ast.NodeIndex, args []ast.NodeIndex) Type {
	if len(args) != 1 {
		c.errorAtNodes(args, n, "print takes exactly 1 argument, got %d", len(args))
	}
	for _, a := range args {
		at := c.defaultIfUntyped(a, c.checkValueExpr(a))
		if !at.IsInvalid() && !c.typeIsPrintable(at) {
			c.errorAt(a, "cannot print value of type %s: %s is not printable (a function type or map cannot be printed, even nested inside a struct or array)", at, at)
		}
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

// checkMakeCall type-checks `make([]T, n)` / `make([]T, n, cap)` (see
// LANGUAGE.md's "Dynamic arrays" section) or `make(map[K]V)` (see
// LANGUAGE.md's "Maps" section - a map's own make call takes no n/cap
// argument at all: it always starts out empty). Unlike every other call this
// language has, make's first argument (args[0]) is a type-position node (an
// ArrayType or MapType, built by the parser's own bespoke make grammar - see
// parser/expr.go's parseMakeArgs), not a value expression, so it goes
// through typeFromNode rather than checkValueExpr. A dynamic array's n/cap
// are ordinary runtime int expressions - unlike [N]T's N (see
// constArraySize), neither is required to be a compile-time constant; that's
// the entire point of "dynamic" (see codegen's genMakeCall for the runtime
// cap>=n check this implies, since sema can't reject a bad runtime
// relationship between two arbitrary expressions at compile time).
func (c *checker) checkMakeCall(n ast.NodeIndex, args []ast.NodeIndex) Type {
	if len(args) == 0 {
		c.errorAtNodes(args, n, "make requires at least 1 argument, got %d", len(args))
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

	if target.Kind == TypeMap {
		if len(args) != 1 {
			c.errorAtNodes(args[1:], n, "make(map[K]V) takes no further arguments, got %d", len(args)-1)
			for _, a := range args[1:] {
				c.checkValueExpr(a)
			}
		}
		return target
	}

	if target.Kind != TypeArray || !target.Dynamic {
		c.errorAt(typeNode, "make requires a dynamic array type ([]T) or a map type (map[K]V), got %s", target)
		for _, a := range args[1:] {
			c.checkValueExpr(a)
		}
		return invalidType
	}

	if len(args) < 2 || len(args) > 3 {
		c.errorAtNodes(args, n, "make requires 2 or 3 arguments, got %d", len(args))
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
// fixed-size array's compile-time-known size, a string's runtime length (see
// LANGUAGE.md's "Dynamic arrays" section), or a map's runtime entry count
// (see LANGUAGE.md's "Maps" section) - matching Go's own `len` working across
// all four. Not meaningful for anything else (a struct, numeric type, or
// bool), rejected with a clear diagnostic.
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
	if t.Kind == TypeArray || t.Kind == TypeString || t.Kind == TypeMap {
		return i32Type
	}
	c.errorAt(args[0], "len is not defined for %s", t)
	return invalidType
}

// checkResumeCall type-checks the predeclared `resume(h) bool` builtin (see
// LANGUAGE.md's "Coroutines" section) - resumes a suspended coroutine handle
// once, reporting whether it suspended again (true, more to do) or has
// finished (false).
func (c *checker) checkResumeCall(n ast.NodeIndex, args []ast.NodeIndex) Type {
	return c.checkCoroHandleArgCall(n, args, "resume")
}

// checkDoneCall type-checks the predeclared `done(h) bool` builtin (see
// LANGUAGE.md's "Coroutines" section) - reports whether h has already
// finished (normally, or via `delete`/scope exit).
func (c *checker) checkDoneCall(n ast.NodeIndex, args []ast.NodeIndex) Type {
	return c.checkCoroHandleArgCall(n, args, "done")
}

// checkCoroHandleArgCall type-checks resume/done's identical shape - exactly
// one argument, which must be a coroutine handle - returning bool either
// way. Shared since the two builtins differ only in name and codegen, not
// in how their one argument is checked.
func (c *checker) checkCoroHandleArgCall(n ast.NodeIndex, args []ast.NodeIndex, name string) Type {
	if len(args) != 1 {
		c.errorAtNodes(args, n, "%s takes exactly 1 argument, got %d", name, len(args))
		for _, a := range args {
			c.checkValueExpr(a)
		}
		return invalidType
	}
	t := c.checkValueExpr(args[0])
	if t.IsInvalid() {
		return invalidType
	}
	if t.Kind != TypeCoroutine {
		c.errorAt(args[0], "%s requires a coroutine handle, got %s", name, t)
		return invalidType
	}
	return boolType
}

// checkRemoveCall type-checks the predeclared `remove(m, k)` builtin (see
// LANGUAGE.md's "Maps" section) - a deliberately new, distinctly-named
// builtin for map key removal, not an extension of this language's existing
// `delete p` statement (real pointer/heap deallocation - a wholly unrelated
// operation; reusing that keyword here would be a confusing collision, see
// LANGUAGE.md). Returns void, like print - there's nothing to return.
func (c *checker) checkRemoveCall(n ast.NodeIndex, args []ast.NodeIndex) Type {
	if len(args) != 2 {
		c.errorAtNodes(args, n, "remove requires exactly 2 arguments (a map and a key), got %d", len(args))
		for _, a := range args {
			c.checkValueExpr(a)
		}
		return invalidType
	}

	mapType := c.checkValueExpr(args[0])
	if mapType.IsInvalid() {
		c.checkValueExpr(args[1])
		return invalidType
	}
	if mapType.Kind != TypeMap {
		c.errorAt(args[0], "remove requires a map (map[K]V), got %s", mapType)
		c.checkValueExpr(args[1])
		return invalidType
	}

	c.checkMapIndexKey(args[1], mapType)
	return voidType
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

// checkAnyArgCall type-checks the identical "exactly one Any-typed argument"
// shape AnyKind/AnyName/AnyFields all share, reporting a real diagnostic (not
// a silent accept) for a wrong argument count or a non-Any argument - see
// checkCoroHandleArgCall's own identical resume/done-sharing precedent.
func (c *checker) checkAnyArgCall(n ast.NodeIndex, args []ast.NodeIndex, name string) bool {
	if len(args) != 1 {
		c.errorAtNodes(args, n, "%s takes exactly 1 argument, got %d", name, len(args))
		for _, a := range args {
			c.checkValueExpr(a)
		}
		return false
	}
	t := c.checkValueExpr(args[0])
	if t.IsInvalid() {
		return false
	}
	if t.Kind != TypeAny {
		c.errorAt(args[0], "%s requires an Any argument, got %s", name, t)
		return false
	}
	return true
}

// checkAnyKindCall type-checks the predeclared `AnyKind(a Any) i32` builtin
// (see LANGUAGE.md's "Any" section) - the boxed value's own runtime
// sema.TypeKind wire value, as a plain i32. This round deliberately doesn't
// expose TypeKind itself as a nameable language-level enum (see DECISIONS.md)
// - AnyName/AnyAs already cover the "what kind is this" use case a named
// enum constant would otherwise be for.
func (c *checker) checkAnyKindCall(n ast.NodeIndex, args []ast.NodeIndex) Type {
	if !c.checkAnyArgCall(n, args, "AnyKind") {
		return invalidType
	}
	return i32Type
}

// checkAnyNameCall type-checks the predeclared `AnyName(a Any) string`
// builtin (see LANGUAGE.md's "Any" section) - the boxed value's own concrete
// type's display name.
func (c *checker) checkAnyNameCall(n ast.NodeIndex, args []ast.NodeIndex) Type {
	if !c.checkAnyArgCall(n, args, "AnyName") {
		return invalidType
	}
	return stringType
}

// checkAnyFieldsCall type-checks the predeclared `AnyFields(a Any)` builtin
// (see LANGUAGE.md's "Any" section) - legal only as a range-for's own
// subject expression, exactly like a real generator call. Unlike every other
// generator, ranging over AnyFields binds two loop variables (a field's own
// name and its recursively-boxed value), not one - see checkRangeForStmt's
// own AnyFields special case for why that's handled there rather than by
// generalizing TypeGenerator's single-binding rule.
func (c *checker) checkAnyFieldsCall(n ast.NodeIndex, args []ast.NodeIndex) Type {
	if !c.checkAnyArgCall(n, args, "AnyFields") {
		return invalidType
	}
	anyElem := Type{Kind: TypeAny}
	return Type{Kind: TypeGenerator, Elem: &anyElem}
}

// checkAnyAsCall type-checks the predeclared `AnyAs[T](a Any) (T, bool)`
// builtin (see LANGUAGE.md's "Any" section) - Go's own type-assertion shape
// (`v, ok := x.(int)`), reusing this language's existing generics/multi-
// return machinery for the surface syntax and result shape rather than
// inventing either from scratch. T is never inferred (a's own static type is
// already fully erased) - every call needs an explicit type argument,
// `AnyAs[i32](a)`.
//
// AnyAs is registered in universeScope with a Generic (see that doc comment)
// but no real Decl/Tree, so it must be intercepted here, before
// checkGenericCall - which would otherwise dereference gi.Tree/gi.Decl
// assuming a real user declaration to clone.
func (c *checker) checkAnyAsCall(n, callee ast.NodeIndex, args []ast.NodeIndex) (Type, bool) {
	gi, explicit, ok := c.genericCallee(callee)
	if !ok || gi.Symbol.Name != "AnyAs" {
		return invalidType, false
	}

	if len(args) != 1 {
		c.errorAtNodes(args, n, "AnyAs takes exactly 1 argument, got %d", len(args))
		for _, a := range args {
			c.checkValueExpr(a)
		}
		return invalidType, true
	}
	if explicit == ast.InvalidNode {
		c.errorAt(n, "AnyAs requires an explicit type argument, e.g. AnyAs[i32](a)")
		c.checkValueExpr(args[0])
		return invalidType, true
	}
	typeArgs, ok := c.typeArgsFromNode(explicit, gi)
	if !ok {
		c.checkValueExpr(args[0])
		return invalidType, true
	}
	target := typeArgs[0]

	argType := c.checkValueExpr(args[0])
	if argType.IsInvalid() || target.IsInvalid() {
		return invalidType, true
	}
	if argType.Kind != TypeAny {
		c.errorAt(args[0], "AnyAs requires an Any argument, got %s", argType)
		return invalidType, true
	}
	if !c.isBoxableIntoAny(target) {
		c.errorAt(explicit, "AnyAs[%s]: %s can never have been boxed into Any", target, target)
		return invalidType, true
	}

	return Type{Kind: TypeMultiReturn, Params: []Type{target, boolType}}, true
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
	case enums.NodeKinds.IndexExpr:
		// `Box[int](args)` - a generic struct's own constructor call.
		// Instantiating it first is all that's needed; from here on this is an
		// ordinary constructor call on an ordinary concrete struct.
		resolved, isGenericStruct := c.resolveGenericStructCallee(callee)
		if !isGenericStruct {
			return invalidType, false
		}
		if !resolved {
			return invalidType, true // already reported
		}
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

// checkEnumVariantCall recognizes and type-checks a tuple-variant
// construction call (`Shape.Circle(5.0)` - see LANGUAGE.md's "Enums"
// section) - the enum-kind counterpart to checkConstructorCall, structured
// identically: callee's Info.Refs entry, already fully resolved by Resolve
// (resolveEnumVariantRef - this needs no type information at all, unlike a
// struct constructor call, which is only resolved to a specific overload
// once the argument count is known), names a SymEnumVariant symbol.
func (c *checker) checkEnumVariantCall(n, callee ast.NodeIndex, args []ast.NodeIndex) (Type, bool) {
	switch c.tree.Nodes[callee].Kind {
	case enums.NodeKinds.Ident, enums.NodeKinds.MemberExpr:
	default:
		return invalidType, false
	}
	sym, ok := c.info.Refs[callee]
	if !ok || sym.Kind != SymEnumVariant {
		return invalidType, false
	}

	variant := sym.Variant
	target := Type{Kind: TypeEnum, Enum: sym.EnumInfo}
	c.info.Types[n] = target

	if variant.Kind != EnumVariantTuple {
		c.errorAt(n, "%s.%s is not a tuple variant (it takes no call arguments)", sym.EnumInfo.Symbol.Name, variant.Name)
		for _, a := range args {
			c.checkValueExpr(a)
		}
		return target, true
	}
	if len(args) != len(variant.Tuple) {
		c.errorAtNodes(args, n, "%s.%s has %d associated value(s), got %d argument(s)", sym.EnumInfo.Symbol.Name, variant.Name, len(variant.Tuple), len(args))
		for _, a := range args {
			c.checkValueExpr(a)
		}
		return target, true
	}
	for i, a := range args {
		at := c.checkValueExpr(a)
		if c.checkAssignable(a, variant.Tuple[i], at, fmt.Sprintf("argument %d", i+1)) {
			c.checkNoIllegalCopy(a, variant.Tuple[i], true, fmt.Sprintf("argument %d", i+1))
		}
	}
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
// Scoped to numeric-to-numeric conversions, plus two dedicated FFI
// crossings - `cstring(s)` and `string(cs)` (see LANGUAGE.md's "External
// functions (FFI)" section) - checked as their own special case below
// before the numeric fallback. Every other struct/array/bool conversion
// remains unsupported, reported as such rather than falling through to
// funcSigForCall's "not callable" wording (which would be a confusing
// message for `Point(x)` - the real problem is that Point isn't a numeric
// conversion target, not that it isn't callable).
//
// A struct callee only ever reaches here when checkConstructorCall found it
// has zero declared constructors (any arity mismatch against a struct that
// *does* have constructors is reported there instead, before this function
// ever runs - see its own doc comment) - so a struct target is rejected
// immediately, before the argument-count check below, regardless of how many
// arguments were given: a struct is never a valid conversion target at any
// arity, so "supply exactly one argument" would be misleading advice that
// doesn't actually fix anything. Checked via target.Kind rather than sym.Kind
// so this also catches a SymTypeParam callee (`T(x)` inside a generic body -
// see DECISIONS.md's dated entry) whose instantiation binds T to a struct
// type; sym.Kind is SymTypeParam there, never SymStruct directly.
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
	if !ok || (sym.Kind != SymBuiltinType && sym.Kind != SymStruct && sym.Kind != SymTypeParam) {
		return invalidType, false
	}

	target := c.typeFromNode(callee)

	if target.Kind == TypeStruct {
		c.errorAtNodes(args, n, "%s has no constructor - declare one, or use a composite literal (%s{...}) instead", target, target)
		for _, a := range args {
			c.checkValueExpr(a)
		}
		c.info.Types[n] = invalidType
		return invalidType, true
	}

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

	// cstring<->string: the two dedicated FFI marshaling conversions (see
	// LANGUAGE.md's "External functions (FFI)" section) - deliberately not
	// numeric, so checked ahead of the numeric-only fallback below.
	isCStringCrossing := (target.Kind == TypeCString && argType.Kind == TypeString) ||
		(target.Kind == TypeString && argType.Kind == TypeCString)
	if isCStringCrossing {
		c.info.Types[n] = target
		return target, true
	}

	// Any(x): boxing an arbitrary value into a type-erased Any (see
	// LANGUAGE.md's "Any" section) - deliberately not numeric, so checked
	// ahead of the numeric-only fallback below, exactly like the cstring<->
	// string crossing above. Any(x) where x is already Any is a legal,
	// cheap no-op copy - the same "redundant same-type conversion stays
	// legal" precedent i64(someI64) already establishes for the numeric
	// path (see isBoxableIntoAny).
	if target.Kind == TypeAny {
		if !c.checkBoxableIntoAny(args[0], argType) {
			c.info.Types[n] = invalidType
			return invalidType, true
		}
		c.info.Types[n] = target
		return target, true
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

// isBoxableIntoAny reports whether t has a defined Any-boxing representation
// this round (see LANGUAGE.md's "Any" section): every scalar/primitive kind,
// TypePointer, and TypeAny itself (a no-op re-box) - not an enum
// (variant-payload descriptor shape not designed yet), a dynamic/fixed array
// or map (no descriptor shape), a function/cfunc value, or any of the three
// kinds that are never a real storable value at all (TypeMultiReturn/
// TypeGenerator/TypeCoroutine). A TypeStruct is boxable only if every one of
// its own fields is - codegen's structDescriptor recurses into each field's
// own type descriptor unconditionally (any.go), so a field of an otherwise-
// unboxable kind (e.g. a `[]int`) must be rejected here, at the one real
// compile-time checkpoint, rather than surfacing as a runtime codegen panic
// the first time that particular struct is ever boxed anywhere in the
// program. A pointer field doesn't recurse into its own pointee (TypePointer
// is unconditionally boxable above) - the same cycle-safety a
// self-referential struct already relies on for typeIsComparable/
// typeIsPrintable.
func (c *checker) isBoxableIntoAny(t Type) bool {
	switch t.Kind {
	case TypeEnum, TypeArray, TypeMap, TypeFunc, TypeCFunc,
		TypeMultiReturn, TypeGenerator, TypeCoroutine:
		return false
	case TypeStruct:
		if t.Struct == nil {
			return true
		}
		restore := c.pushTree(t.Struct.Symbol.Tree)
		defer restore()
		for _, field := range c.tree.StructFields(t.Struct.Symbol.Decl) {
			fieldType := c.typeFromNode(c.tree.Child(field, 1))
			if !c.isBoxableIntoAny(fieldType) {
				return false
			}
		}
		return true
	default:
		return true
	}
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
//     must be TypeFunc or TypeCFunc - the latter calls through a bare
//     function pointer with no ctxPtr at all (see LANGUAGE.md's "External
//     functions (FFI)" section; codegen's isCFuncCall/genCFuncCall mirror
//     this exact distinction the same way isDirectFuncCall/genIndirectCall
//     already do for TypeFunc).
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
	if t.Kind != TypeFunc && t.Kind != TypeCFunc {
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
// ordinary method call (`p.translate()`), a package-qualified function call
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
// the caller. This intentionally never touches method values (`p.translate`
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

	// A struct-variant construction (`Shape.Triangle{base: 3.0, height:
	// 4.0}` - see LANGUAGE.md's "Enums" section) - typeNode's own Info.Refs
	// entry, already fully resolved by Resolve (resolveEnumVariantRef),
	// names a SymEnumVariant symbol; this is checked upfront, entirely
	// bypassing typeFromNode's own generic struct/array dispatch below (a
	// variant reference is never itself a standalone type in type position -
	// only its owning enum is).
	if sym, ok := c.info.Refs[typeNode]; ok && sym.Kind == SymEnumVariant {
		return c.checkEnumVariantCompositeLit(n, sym, elems)
	}

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

// checkEnumVariantCompositeLit type-checks a struct-variant construction
// literal (`Shape.Triangle{base: 3.0, height: 4.0}` - see LANGUAGE.md's
// "Enums" section) - the enum-kind counterpart to checkStructCompositeLit,
// supporting both positional and keyed elements the identical way a real
// struct composite literal does (reusing this project's existing
// keyed-literal grammar verbatim, per that section's own construction rule).
func (c *checker) checkEnumVariantCompositeLit(n ast.NodeIndex, sym *Symbol, elems []ast.NodeIndex) Type {
	variant := sym.Variant
	target := Type{Kind: TypeEnum, Enum: sym.EnumInfo}

	if variant.Kind != EnumVariantStruct {
		c.errorAt(n, "%s.%s is not a struct variant (it has no named fields)", sym.EnumInfo.Symbol.Name, variant.Name)
		for _, e := range elems {
			c.checkCompositeLitElemFallback(e)
		}
		return target
	}
	if len(elems) == 0 {
		return target
	}

	keyed := c.tree.IsKeyedElement(elems[0])
	seen := make(map[string]bool)
	for i, elem := range elems {
		isKV := c.tree.IsKeyedElement(elem)
		if isKV != keyed {
			c.errorAt(elem, "cannot mix keyed and positional elements in a composite literal")
			c.checkCompositeLitElemFallback(elem)
			continue
		}
		if keyed {
			c.checkKeyedEnumFieldElem(elem, sym.EnumInfo, variant, seen)
		} else {
			c.checkPositionalEnumFieldElem(elem, i, sym.EnumInfo, variant)
		}
	}
	if !keyed && len(elems) != len(variant.Fields) {
		c.errorAtNodes(elems, n, "%s.%s composite literal has %d fields, want %d", sym.EnumInfo.Symbol.Name, variant.Name, len(elems), len(variant.Fields))
	}
	return target
}

func (c *checker) checkPositionalEnumFieldElem(elem ast.NodeIndex, i int, info *EnumInfo, variant *EnumVariant) {
	vt := c.checkValueExpr(elem)
	if i >= len(variant.Fields) {
		return
	}
	field := variant.Fields[i]
	if c.checkAssignable(elem, field.Type, vt, fmt.Sprintf("field %s", field.Name)) {
		c.checkNoIllegalCopy(elem, field.Type, true, fmt.Sprintf("field %s", field.Name))
	}
}

func (c *checker) checkKeyedEnumFieldElem(elem ast.NodeIndex, info *EnumInfo, variant *EnumVariant, seen map[string]bool) {
	key := c.tree.Child(elem, 0)
	value := c.tree.Child(elem, 1)
	vt := c.checkValueExpr(value)

	if c.tree.Nodes[key].Kind != enums.NodeKinds.Ident {
		c.errorAt(key, "field name must be an identifier")
		return
	}
	name := c.tree.Text(key)
	field, ok := variant.FieldByName(name)
	if !ok {
		c.errorAt(key, "%s.%s has no field %s", info.Symbol.Name, variant.Name, name)
		return
	}
	c.info.Refs[key] = field.Sym
	if seen[name] {
		c.errorAt(key, "field %s specified twice", name)
	}
	seen[name] = true

	if c.checkAssignable(value, field.Type, vt, fmt.Sprintf("field %s", name)) {
		c.checkNoIllegalCopy(value, field.Type, true, fmt.Sprintf("field %s", name))
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
func isTerminatingStmt(tree *ast.Tree, info *Info, n ast.NodeIndex) bool {
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
	case enums.NodeKinds.RangeForStmt:
		// Always data-dependent - a map/array can have zero entries at
		// runtime, unlike a truly bare `for {}` with no cond clause at all -
		// so this can never terminate, regardless of its own body.
		return false
	case enums.NodeKinds.IfStmt:
		elseBranch := tree.Child(n, 2)
		if elseBranch == ast.InvalidNode {
			return false
		}
		return isTerminatingStmt(tree, info, tree.Child(n, 1)) && isTerminatingStmt(tree, info, elseBranch)
	case enums.NodeKinds.MatchStmt:
		return matchStmtTerminates(tree, info, n)
	case enums.NodeKinds.Block:
		stmts := tree.Children(n)
		if len(stmts) == 0 {
			return false
		}
		return isTerminatingStmt(tree, info, stmts[len(stmts)-1])
	default:
		return false
	}
}

// matchStmtTerminates reports whether a MatchStmt is a terminating statement
// in isTerminatingStmt's sense (see its own doc comment and LANGUAGE.md's
// "Missing return" section) - a thin wrapper around matchArmsAllTerminate,
// passing isTerminatingStmt itself as "how does one arm's own body
// terminate" (see that function's own doc comment for why this is
// parameterized at all: mustYieldEveryPath, just below, needs the identical
// per-arm-termination-and-exhaustiveness logic, generalized to accept
// YieldStmt as an additional terminating leaf, and sharing this one core
// rather than hand-rolling the same exhaustiveness walk twice is exactly
// the kind of duplication AGENTS.md's review process exists to catch).
func matchStmtTerminates(tree *ast.Tree, info *Info, n ast.NodeIndex) bool {
	return matchArmsAllTerminate(tree, info, n, func(body ast.NodeIndex) bool {
		return isTerminatingStmt(tree, info, body)
	})
}

// matchArmsAllTerminate is matchStmtTerminates' own parameterized core -
// every arm's own body must satisfy armTerminates, AND the match itself
// must be exhaustive - what "exhaustive" means depends on the subject's own
// type, mirroring checkMatchDispatch's identical enum-vs-value split:
//   - an enum match: a wildcard `_` arm present, or every one of the
//     subject enum's own variants covered by some arm.
//   - a value match (int/bool/string - see isValueMatchType):
//     checkValueMatchStmt already guarantees a wildcard `_` arm is present
//     for one of these to have passed sema.Check at all - but this function
//     is a pure, deliberately uncached recomputation of an already-checked
//     tree (see matchStmtTerminates' own doc comment one paragraph up), so
//     it re-derives that fact directly here too, rather than blindly
//     trusting the guarantee: termination reduces to "every arm terminates"
//     (already confirmed by the loop below) AND a wildcard arm is genuinely
//     present.
//
// An inexhaustive match always leaves a real fall-through path, exactly like
// an `if` with no `else` never terminates. This recomputes the identical
// exhaustiveness fact checkMatchDispatch itself already validated (with real
// diagnostics) - deliberately not cached anywhere: isTerminatingStmt/
// mustYieldEveryPath are both pure functions of an already-checked tree,
// with no *checker receiver to memoize onto, mirroring forHasOwnBreak's own
// identical no-caching precedent one construct over.
func matchArmsAllTerminate(tree *ast.Tree, info *Info, n ast.NodeIndex, armTerminates func(body ast.NodeIndex) bool) bool {
	arms := tree.MatchArms(n)
	if len(arms) == 0 {
		return false
	}
	for _, arm := range arms {
		if !armTerminates(tree.MatchArmBody(arm)) {
			return false
		}
	}

	subjType := info.Types[tree.MatchSubject(n)]
	if subjType.Kind == TypePointer && subjType.Elem != nil {
		subjType = *subjType.Elem
	}

	if subjType.Kind != TypeEnum || subjType.Enum == nil {
		for _, arm := range arms {
			if tree.IsWildcardMatchArm(arm) {
				return true
			}
		}
		return false
	}

	covered := make(map[string]bool, len(subjType.Enum.Order))
	for _, arm := range arms {
		if tree.IsWildcardMatchArm(arm) {
			return true // a wildcard arm alone makes it exhaustive
		}
		pattern := tree.MatchArmPattern(arm)
		if sym, ok := patternVariantSym(tree, info, pattern); ok && sym.Variant != nil {
			covered[sym.Variant.Name] = true
		}
	}
	return len(covered) == len(subjType.Enum.Order)
}

// mustYieldEveryPath reports whether n is a "yield-terminating" statement -
// isTerminatingStmt's exact recursive shape (ReturnStmt/ForStmt/IfStmt/
// MatchStmt/Block cases identical), generalized with one new base case:
// YieldStmt terminates too (see ast.Node's own YieldStmt doc comment - it
// exits just its own enclosing match-expression arm, exactly the way
// `break` exits its own enclosing loop, but from this function's "does
// control ever fall past this statement" perspective it's exactly as
// terminating as return/break/continue already are treated, or not treated,
// throughout this file). This is checkMatchExprArmBody's own per-arm "every
// reachable path yields" rule (see LANGUAGE.md's "match" section's "match
// as an expression" subsection): every arm's block must satisfy this, or
// its own arm-body check reports "match arm does not yield a value on every
// path". A `return` inside an arm (exiting the whole enclosing function,
// never producing a match value at all) still satisfies this exactly like
// isTerminatingStmt already treats it - that path never needs a yield of
// its own, since control never returns to consume the match expression's
// result along it, the same reasoning an `if` branch ending in `return`
// needs no further statement after it.
//
// Never descends into an expression - only ever into another nested
// STATEMENT shape (Block/IfStmt/ForStmt/MatchStmt), exactly like
// isTerminatingStmt itself - so a match expression nested somewhere inside
// an expression (a `yield match other {...}`'s own wrapped match, a `:=`
// initializer, a call argument, ...) is never visited by this walk at all;
// YieldStmt is a base case here that returns true unconditionally, without
// ever inspecting its own wrapped expression, so there's nothing to
// recurse into even for that shape. Its own arms are checked completely
// separately, by their own checkMatchExprStmt call (its own pushed
// matchExprCheckCtx frame - see checker.matchExprStack) whenever checkExpr
// happens to reach that expression, entirely independent of this walk. A
// MatchStmt node reached HERE (via a Block's own direct statement child)
// is always the statement-mode flavor (parseStmt's own keyword-first
// dispatch guarantees a bare `match x {...}` at statement start can never be
// anything else - see parser/stmt.go's own parseStmt doc comment), so
// recursing into its own arm bodies below (matchArmsAllTerminate) is exactly
// matchStmtTerminates' own existing reasoning, just yield-aware too.
func mustYieldEveryPath(tree *ast.Tree, info *Info, n ast.NodeIndex) bool {
	if n == ast.InvalidNode {
		return false
	}
	switch tree.Nodes[n].Kind {
	case enums.NodeKinds.YieldStmt:
		return true
	case enums.NodeKinds.ReturnStmt:
		return true
	case enums.NodeKinds.ForStmt:
		cond := tree.Child(n, 1)
		body := tree.Child(n, 3)
		return cond == ast.InvalidNode && !forHasOwnBreak(tree, body)
	case enums.NodeKinds.RangeForStmt:
		// Same reasoning as isTerminatingStmt's own identical case: always
		// data-dependent, so it can never guarantee a yield on every path
		// either.
		return false
	case enums.NodeKinds.IfStmt:
		elseBranch := tree.Child(n, 2)
		if elseBranch == ast.InvalidNode {
			return false
		}
		return mustYieldEveryPath(tree, info, tree.Child(n, 1)) && mustYieldEveryPath(tree, info, elseBranch)
	case enums.NodeKinds.MatchStmt:
		return matchArmsAllTerminate(tree, info, n, func(body ast.NodeIndex) bool {
			return mustYieldEveryPath(tree, info, body)
		})
	case enums.NodeKinds.Block:
		stmts := tree.Children(n)
		if len(stmts) == 0 {
			return false
		}
		return mustYieldEveryPath(tree, info, stmts[len(stmts)-1])
	default:
		return false
	}
}

// branchDivertsControl reports whether every reachable path through n (a
// single statement, possibly a Block) ends in a return/break/continue - i.e.
// never falls through to whatever follows it. checkIfStmt/checkMatchDispatch's
// own move-tracking merge test: a branch that diverts never reaches the join
// point at all, so its own moved-from symbols can never actually race
// whatever the other, non-diverting branch(es) left there.
//
// Same recursive shape as isTerminatingStmt/mustYieldEveryPath, one node
// kind over - unlike isTerminatingStmt (scoped to "does the function always
// return", where break/continue don't count since they don't return from
// it), break/continue very much do count here.
func branchDivertsControl(tree *ast.Tree, info *Info, n ast.NodeIndex) bool {
	if n == ast.InvalidNode {
		return false
	}
	switch tree.Nodes[n].Kind {
	case enums.NodeKinds.ReturnStmt, enums.NodeKinds.BreakStmt, enums.NodeKinds.ContinueStmt:
		return true
	case enums.NodeKinds.ForStmt:
		cond := tree.Child(n, 1)
		body := tree.Child(n, 3)
		return cond == ast.InvalidNode && !forHasOwnBreak(tree, body)
	case enums.NodeKinds.RangeForStmt:
		return false
	case enums.NodeKinds.IfStmt:
		elseBranch := tree.Child(n, 2)
		if elseBranch == ast.InvalidNode {
			return false
		}
		return branchDivertsControl(tree, info, tree.Child(n, 1)) && branchDivertsControl(tree, info, elseBranch)
	case enums.NodeKinds.MatchStmt:
		return matchArmsAllTerminate(tree, info, n, func(body ast.NodeIndex) bool {
			return branchDivertsControl(tree, info, body)
		})
	case enums.NodeKinds.Block:
		stmts := tree.Children(n)
		if len(stmts) == 0 {
			return false
		}
		return branchDivertsControl(tree, info, stmts[len(stmts)-1])
	default:
		return false
	}
}

// matchIsExhaustive reports whether n (a MatchStmt) covers every possible
// subject value - a wildcard arm, or (for an enum subject) every variant
// covered - reusing matchArmsAllTerminate's own exhaustiveness computation
// with an armTerminates that's trivially always true, so the result reduces
// to exactly the exhaustiveness half of that check.
func matchIsExhaustive(tree *ast.Tree, info *Info, n ast.NodeIndex) bool {
	return matchArmsAllTerminate(tree, info, n, func(ast.NodeIndex) bool { return true })
}

// patternVariantSym returns the Symbol a match arm's own pattern resolved to
// (see resolve.go's resolveEnumVariantRef) - the MemberExpr node itself for a
// unit-variant pattern, or its leading callee/type-expr child for a tuple-/
// struct-variant one.
func patternVariantSym(tree *ast.Tree, info *Info, pattern ast.NodeIndex) (*Symbol, bool) {
	switch tree.Nodes[pattern].Kind {
	case enums.NodeKinds.MemberExpr:
		sym, ok := info.Refs[pattern]
		return sym, ok
	case enums.NodeKinds.CallExpr, enums.NodeKinds.CompositeLit:
		sym, ok := info.Refs[tree.Child(pattern, 0)]
		return sym, ok
	default:
		return nil, false
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
	case enums.NodeKinds.MatchStmt:
		// A match arm's own body can contain a `break` that still targets
		// *this* enclosing loop directly (match is not itself a loop, so it
		// introduces no break target of its own - see LANGUAGE.md's "match"
		// section) - every arm must be checked, the same "recurse into every
		// nesting branch, not just the first" reasoning IfStmt's own
		// then/else case already applies, generalized from two branches to N.
		for _, arm := range tree.MatchArms(n) {
			if forHasOwnBreak(tree, tree.MatchArmBody(arm)) {
				return true
			}
		}
		return false
	default:
		// ForStmt/RangeForStmt (their own break targets the inner loop, not
		// this one) and every other non-nesting statement kind: nothing to
		// find here.
		return false
	}
}
