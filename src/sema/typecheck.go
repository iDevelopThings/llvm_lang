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
// pushed once per FuncDecl. Functions never nest in this grammar (no
// closures, no nested func literals), so a single field on checker holds it;
// there's nothing to stack.
type enclosingFunc struct {
	hasReturn bool // whether the function declared a return type at all
	ret       Type // meaningful only when hasReturn is true
}

type checker struct {
	tree  *ast.Tree
	diags *diag.Bag
	info  *Info

	// declTypes memoizes the type of each var-introducing declaration
	// (VarDecl, ShortVarDecl, Param), keyed by the declaration node itself
	// - not by *Symbol, since every declaration node is already unique
	// tree-wide and this way computeDeclType doesn't need a Symbol handed
	// to it. computingDecl is a cycle guard: a top-level var can
	// forward-reference another (see resolve.go's pass ordering), so
	// declType is re-entrant and needs to detect `var a = b; var b = a`.
	declTypes     map[ast.NodeIndex]Type
	computingDecl map[ast.NodeIndex]bool

	// typeNodeCache memoizes typeFromNode by the type-position node
	// itself. Without it, the same struct field's type node - reachable
	// from both checkStructDecl's upfront pass and every composite literal
	// that names that field - would re-run (and, for a dynamic-array
	// field, re-report) the same conversion once per reference.
	typeNodeCache map[ast.NodeIndex]Type

	// funcSigs memoizes each FuncDecl's signature, keyed by the FuncDecl
	// node. Computing it also type-checks its params/return type exactly
	// once, however many call sites (or zero) end up asking for it.
	funcSigs map[ast.NodeIndex]funcSignature

	curFunc *enclosingFunc

	// loopDepth counts how many enclosing ForStmt bodies checkStmt is
	// currently inside - incremented/decremented around checkForStmt's own
	// checkBlock call, mirroring how curFunc tracks "am I inside a
	// function" for checkReturnStmt. A BreakStmt/ContinueStmt is only
	// valid while this is > 0 - see checkBreakOrContinue. It doesn't need
	// to distinguish *which* enclosing loop (that's codegen's loopStack's
	// job, once this pass has already guaranteed one exists at all).
	loopDepth int
}

// Check type-checks tree, using the name/scope resolution info already
// computed by Resolve (Info.Refs, Info.Scopes, Info.Structs). It fills in
// info.Types with every expression's inferred Type, and resolves what
// Resolve deliberately deferred - a MemberExpr's field/method name and a
// CompositeLit's keyed element names - now that an object's type can
// actually be inferred (see resolve.go's doc comments for exactly what was
// left unresolved and why).
//
// Diagnostics land in a fresh Bag, separate from Resolve's - same
// convention as lexer errors staying distinct from parser ones - so a
// caller can tell which pass reported what. Check never panics on a type
// error: every violation is recorded and checking continues, so one mistake
// doesn't hide the rest (see AGENTS.md; unlike the parser's token-stream
// bailout, walking an already-finite tree has no unbounded-input risk to
// guard against, so there's no error-count cap here).
func Check(tree *ast.Tree, info *Info) *diag.Bag {
	c := &checker{
		tree:          tree,
		diags:         diag.NewBag(),
		info:          info,
		declTypes:     make(map[ast.NodeIndex]Type),
		computingDecl: make(map[ast.NodeIndex]bool),
		typeNodeCache: make(map[ast.NodeIndex]Type),
		funcSigs:      make(map[ast.NodeIndex]funcSignature),
	}
	info.Types = make(map[ast.NodeIndex]Type)
	c.checkFile()
	return c.diags
}

func (c *checker) errorAt(n ast.NodeIndex, format string, a ...any) {
	c.diags.Errorf(c.tree.SpanOf(n).Start, format, a...)
}

// checkFile mirrors resolveFile's two-pass shape (struct catalogs before
// bodies): struct field types first, so a dynamic-array-field or bad-size
// diagnostic in a struct nobody ever instantiates still surfaces, then every
// top-level var's type and every function's body.
func (c *checker) checkFile() {
	decls := c.tree.Children(c.tree.Root)

	for _, decl := range decls {
		if c.tree.Nodes[decl].Kind == enums.NodeKinds.StructDecl {
			c.checkStructDecl(decl)
		}
	}
	for _, decl := range decls {
		switch c.tree.Nodes[decl].Kind {
		case enums.NodeKinds.VarDecl:
			c.declType(decl)
		case enums.NodeKinds.FuncDecl:
			c.checkFuncDecl(decl)
		}
	}
}

func (c *checker) checkStructDecl(decl ast.NodeIndex) {
	for _, field := range c.tree.Children(decl)[1:] {
		c.typeFromNode(c.tree.Child(field, 1))
	}
}

func (c *checker) checkFuncDecl(decl ast.NodeIndex) {
	sig := c.funcSigForDecl(decl) // also checks params/return type exactly once
	body := c.tree.Child(decl, 4)

	prevFunc := c.curFunc
	c.curFunc = &enclosingFunc{
		hasReturn: c.tree.Child(decl, 3) != ast.InvalidNode,
		ret:       sig.Return,
	}
	c.checkBlock(body)
	if c.curFunc.hasReturn && !isTerminatingStmt(c.tree, body) {
		c.errorAt(decl, "missing return")
	}
	c.curFunc = prevFunc
}

// funcSigForDecl returns decl's (a FuncDecl's) signature, computing and
// caching it on first use.
func (c *checker) funcSigForDecl(decl ast.NodeIndex) funcSignature {
	if sig, ok := c.funcSigs[decl]; ok {
		return sig
	}
	sig := c.computeFuncSig(decl)
	c.funcSigs[decl] = sig
	return sig
}

func (c *checker) computeFuncSig(decl ast.NodeIndex) funcSignature {
	paramList := c.tree.Child(decl, 2)
	returnTypeNode := c.tree.Child(decl, 3)

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

// declType returns the type of a VarDecl, ShortVarDecl, or Param
// declaration node, computing and memoizing it on first use so a later
// reference (an Ident resolving to this decl) never redoes the work or
// re-reports a diagnostic already raised while computing it.
func (c *checker) declType(decl ast.NodeIndex) Type {
	if t, ok := c.declTypes[decl]; ok {
		return t
	}
	if c.computingDecl[decl] {
		c.errorAt(decl, "type-checking cycle while inferring this declaration's type")
		return invalidType
	}
	c.computingDecl[decl] = true
	t := c.computeDeclType(decl)
	delete(c.computingDecl, decl)
	c.declTypes[decl] = t
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
	c.checkAssignable(initNode, declared, initType, "variable declaration")
	return declared
}

func (c *checker) checkShortVarDeclNode(decl ast.NodeIndex) Type {
	// `:=` never has a declared type (see the grammar) - same untyped
	// defaulting rule as a type-less `var`, above.
	initNode := c.tree.Child(decl, 1)
	return c.defaultIfUntyped(initNode, c.checkValueExpr(initNode))
}

// defaultIfUntyped applies Go's own untyped-constant defaulting rule (see
// AGENTS.md's Types section) whenever exprNode's checked type t is still
// untyped by the time it reaches a position with no explicit target type to
// resolve against: untyped int defaults to i32, untyped float to f64.
// Retypes exprNode's whole subtree (retypeUntyped) to match, since nothing
// else will ever revisit it. A non-untyped t passes through unchanged.
func (c *checker) defaultIfUntyped(exprNode ast.NodeIndex, t Type) Type {
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
	if t, ok := c.typeNodeCache[n]; ok {
		return t
	}
	t := c.computeTypeFromNode(n)
	c.typeNodeCache[n] = t
	c.info.Types[n] = t
	return t
}

func (c *checker) computeTypeFromNode(n ast.NodeIndex) Type {
	switch c.tree.Nodes[n].Kind {
	case enums.NodeKinds.Ident:
		sym, ok := c.info.Refs[n]
		if !ok {
			return invalidType // undefined name; already reported by Resolve
		}
		switch sym.Kind {
		case SymBuiltinType:
			switch c.tree.Text(n) {
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
			info, ok := c.info.Structs[c.tree.Text(n)]
			if !ok {
				return invalidType
			}
			return Type{
				Kind:   TypeStruct,
				Struct: info,
			}
		default:
			return invalidType // "not a type"; already reported by Resolve
		}
	case enums.NodeKinds.ArrayType:
		return c.arrayTypeFromNode(n)
	case enums.NodeKinds.FuncType:
		return c.funcTypeFromNode(n)
	default:
		return invalidType
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

// arrayTypeFromNode converts an ArrayType node. A dynamic array (`[]T`,
// size == InvalidNode) is reported exactly once, right here - the one place
// every dynamic array type, wherever it appears (a var's type, a param, a
// field, a composite literal's target type, a return type), passes through -
// and still returns a fully-formed Type with Dynamic set, per the task: the
// form must be represented, not just rejected outright, so a caller that
// only cares about the shape (codegen, once it exists) isn't forced to
// special-case an invalid Type just to learn "this was a slice".
func (c *checker) arrayTypeFromNode(n ast.NodeIndex) Type {
	sizeNode := c.tree.Child(n, 0)
	elemNode := c.tree.Child(n, 1)
	elem := c.typeFromNode(elemNode)

	if sizeNode == ast.InvalidNode {
		c.errorAt(n, "dynamic arrays ([]T) are not supported yet - only fixed-size [N]T")
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
	case enums.NodeKinds.VarDecl, enums.NodeKinds.ShortVarDecl:
		c.declType(n)
	case enums.NodeKinds.AssignStmt:
		c.checkAssignStmt(n)
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
		c.checkAssignable(value, tt, vt, "assignment")
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
	case enums.NodeKinds.MemberExpr, enums.NodeKinds.IndexExpr:
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

	vt := c.checkValueExpr(value)
	if !fn.hasReturn {
		c.errorAt(value, "function does not return a value")
		return
	}
	c.checkAssignable(value, fn.ret, vt, "return statement")
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
	case enums.NodeKinds.MemberExpr:
		return c.checkMemberExpr(n)
	case enums.NodeKinds.CompositeLit:
		return c.checkCompositeLit(n)
	default:
		// ArrayType (reachable only via the same parse-error recovery path
		// resolve.go's resolveExpr documents) and Bad both have no
		// sensible value type; already diagnosed upstream.
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
	switch sym.Kind {
	case SymVar, SymParam:
		return c.declType(sym.Decl)
	case SymFunc:
		if sym.Decl == ast.InvalidNode {
			c.errorAt(n, "%s is a builtin, not a value", c.tree.Text(n))
			return invalidType
		}
		return funcType(c.funcSigForDecl(sym.Decl))
	case SymStruct, SymBuiltinType:
		c.errorAt(n, "%s is a type, not a value", c.tree.Text(n))
		return invalidType
	default:
		return invalidType
	}
}

// checkThisExpr types `this` as the enclosing method's receiver struct.
// sym.Decl is that struct's own StructDecl node (see resolve.go's
// fnScope.Receiver construction), not a variable declaration, so this
// doesn't go through declType.
func (c *checker) checkThisExpr(n ast.NodeIndex) Type {
	sym, ok := c.info.Refs[n]
	if !ok {
		return invalidType // "this outside a method"; already reported by Resolve
	}
	name := c.tree.Text(c.tree.Child(sym.Decl, 0))
	info, ok := c.info.Structs[name]
	if !ok {
		return invalidType
	}
	return Type{
		Kind:   TypeStruct,
		Struct: info,
	}
}

// checkUnaryExpr types `-`/`!`. Unary `-` now works on any numeric type
// (every int width, every float width, or an untyped constant - see
// AGENTS.md's Types section) and always yields the exact same Type/Kind it
// was given, untyped included: an untyped operand simply stays untyped,
// deferring resolution further up the tree exactly like a NumberLit would on
// its own (see retypeUntyped's ParenExpr/UnaryExpr case, which knows to
// recurse into a unary-minus operand the same way).
func (c *checker) checkUnaryExpr(n ast.NodeIndex) Type {
	t := c.checkValueExpr(c.tree.Child(n, 0))
	if t.IsInvalid() {
		return invalidType
	}
	op := c.tree.Text(n)
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

// checkEqualityOperands types `==`/`!=`.
func (c *checker) checkEqualityOperands(n, lNode, rNode ast.NodeIndex, lt, rt Type, op string) Type {
	switch {
	case lt.Kind == TypeStruct, lt.Kind == TypeArray:
		if lt.Equal(rt) {
			return boolType
		}
	case lt.Kind == TypeString && rt.Kind == TypeString:
		return boolType
	case lt.Kind == TypeBool && rt.Kind == TypeBool:
		return boolType
	case lt.IsNumeric() && rt.IsNumeric():
		if c.resolveComparisonOperands(lNode, rNode, lt, rt) {
			return boolType
		}
	}
	c.errorAt(n, "operator %s not defined for %s and %s", op, lt, rt)
	return invalidType
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

// checkMemberExpr types a plain field access (`p.field`, not a call). See
// resolveMember for how the field name itself gets resolved - the work
// Resolve deliberately deferred to this pass.
func (c *checker) checkMemberExpr(n ast.NodeIndex) Type {
	sym, ok := c.resolveMember(n)
	if !ok {
		return invalidType
	}
	if sym.Kind != SymField {
		c.errorAt(n, "%s is a method, not a field (call it with ())", c.tree.Text(n))
		return invalidType
	}
	return c.typeFromNode(c.tree.Child(sym.Decl, 1)) // Field: [name, type]
}

// resolveMember infers a MemberExpr's object type, requires it to be a
// struct, and looks its name (n's Tok - see ast.Node's doc comment) up in
// that struct's Fields/Methods catalog, recording whichever it finds into
// info.Refs[n] - same as every other reference, resolving occurrence and
// declaration alike to the same *Symbol. This is shared between a plain
// field access (checkMemberExpr) and a method-call callee
// (methodSigForCallee), which differ only in which kind of symbol they
// require the result to be.
func (c *checker) resolveMember(n ast.NodeIndex) (*Symbol, bool) {
	object := c.tree.Child(n, 0)
	objType := c.checkValueExpr(object)
	name := c.tree.Text(n)

	if objType.IsInvalid() {
		return nil, false
	}
	if objType.Kind != TypeStruct {
		c.errorAt(n, "%s undefined (%s is not a struct)", name, objType)
		return nil, false
	}

	info := objType.Struct
	if sym, ok := info.Fields[name]; ok {
		c.info.Refs[n] = sym
		return sym, true
	}
	if sym, ok := info.Methods[name]; ok {
		c.info.Refs[n] = sym
		return sym, true
	}
	c.errorAt(n, "%s has no field or method %s", info.Symbol.Name, name)
	return nil, false
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
		c.errorAt(n, "wrong number of arguments in call: got %d, want %d", len(args), len(sig.Params))
		for _, a := range args {
			c.checkValueExpr(a)
		}
		return sig.Return
	}

	for i, a := range args {
		at := c.checkValueExpr(a)
		c.checkAssignable(a, sig.Params[i], at, fmt.Sprintf("argument %d", i+1))
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
		c.errorAt(n, "print takes exactly 1 argument, got %d", len(args))
	}
	for _, a := range args {
		c.defaultIfUntyped(a, c.checkValueExpr(a))
	}
	return voidType
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
		c.errorAt(n, "conversion to %s requires exactly one argument, got %d", target, len(args))
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
//     declared free function (SymFunc with a real FuncDecl, i.e.
//     Decl != InvalidNode - this excludes the predeclared `print` builtin,
//     though isPrintCall already intercepts that case earlier and never
//     reaches this function at all), or a MemberExpr naming a method
//     (methodSigForCallee) - gets no info.Types entry for callee itself:
//     the callee names a fixed declaration, not a value with its own Type,
//     same as before this round.
//   - Anything else that type-checks as callable - a function-typed
//     variable/parameter, or any other expression (e.g. a call whose own
//     result is itself a function) - is an *indirect* call: callee is
//     checked as an ordinary value expression (so it does get a real
//     info.Types entry - codegen needs it to actually evaluate the
//     function value before calling through it) and its Type must be
//     TypeFunc.
func (c *checker) funcSigForCall(callee ast.NodeIndex) (funcSignature, bool) {
	switch c.tree.Nodes[callee].Kind {
	case enums.NodeKinds.MemberExpr:
		return c.methodSigForCallee(callee)
	case enums.NodeKinds.Ident:
		if sym, ok := c.info.Refs[callee]; ok && sym.Kind == SymFunc && sym.Decl != ast.InvalidNode {
			return c.funcSigForDecl(sym.Decl), true
		}
	}

	t := c.checkValueExpr(callee)
	if t.IsInvalid() {
		return funcSignature{}, false
	}
	if t.Kind != TypeFunc {
		if c.tree.Nodes[callee].Kind == enums.NodeKinds.Ident {
			c.errorAt(callee, "cannot call %s (%s is not a function)", c.tree.Text(callee), t)
		} else {
			c.errorAt(callee, "cannot call this expression (not a function)")
		}
		return funcSignature{}, false
	}
	return funcSignature{Params: t.Params, Return: *t.Return}, true
}

func (c *checker) methodSigForCallee(callee ast.NodeIndex) (funcSignature, bool) {
	sym, ok := c.resolveMember(callee)
	if !ok {
		return funcSignature{}, false
	}
	if sym.Kind != SymFunc {
		c.errorAt(callee, "%s is a field, not a method (cannot be called)", c.tree.Text(callee))
		return funcSignature{}, false
	}
	return c.funcSigForDecl(sym.Decl), true
}

// checkCompositeLit types a composite literal (`Point{...}` or `[N]T{...}`)
// against its target type, and - for the struct case - resolves each keyed
// element's field name (the work Resolve deferred; see resolveCompositeLit
// in resolve.go).
func (c *checker) checkCompositeLit(n ast.NodeIndex) Type {
	children := c.tree.Children(n)
	typeNode, elems := children[0], children[1:]

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
	if c.tree.Nodes[elem].Kind == enums.NodeKinds.KeyValueExpr {
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
	fields := c.tree.Children(info.Symbol.Decl)[1:] // Field nodes, declaration order
	keyed := len(elems) > 0 && c.tree.Nodes[elems[0]].Kind == enums.NodeKinds.KeyValueExpr
	seen := make(map[string]bool)

	for i, elem := range elems {
		isKV := c.tree.Nodes[elem].Kind == enums.NodeKinds.KeyValueExpr
		if isKV != keyed {
			c.errorAt(elem, "cannot mix keyed and positional elements in a composite literal")
			c.checkCompositeLitElemFallback(elem)
			continue
		}
		if keyed {
			c.checkKeyedStructElem(elem, info, seen)
		} else {
			c.checkPositionalStructElem(elem, i, fields)
		}
	}

	if !keyed && len(elems) != len(fields) {
		c.errorAt(n, "%s composite literal has %d fields, want %d", info.Symbol.Name, len(elems), len(fields))
	}
}

func (c *checker) checkPositionalStructElem(elem ast.NodeIndex, i int, fields []ast.NodeIndex) {
	vt := c.checkValueExpr(elem)
	if i >= len(fields) {
		return // count mismatch already reported by the caller
	}
	fieldType := c.typeFromNode(c.tree.Child(fields[i], 1))
	fieldName := c.tree.Text(c.tree.Child(fields[i], 0))
	c.checkAssignable(elem, fieldType, vt, fmt.Sprintf("field %s", fieldName))
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
	if seen[name] {
		c.errorAt(key, "field %s specified twice", name)
	}
	seen[name] = true

	fieldType := c.typeFromNode(c.tree.Child(fieldSym.Decl, 1))
	c.checkAssignable(value, fieldType, vt, fmt.Sprintf("field %s", name))
}

// checkArrayCompositeLit only supports positional elements (`[N]T{a, b}`) -
// Go-style index-keyed array literals (`[5]int{2: 9}`) aren't part of this
// language's grammar's semantics yet (see BLOCKERS.md).
func (c *checker) checkArrayCompositeLit(n ast.NodeIndex, target Type, elems []ast.NodeIndex) {
	for _, elem := range elems {
		if c.tree.Nodes[elem].Kind == enums.NodeKinds.KeyValueExpr {
			c.errorAt(elem, "keyed elements are not supported in array literals")
			c.checkValueExpr(c.tree.Child(elem, 1))
			continue
		}
		vt := c.checkValueExpr(elem)
		c.checkAssignable(elem, *target.Elem, vt, "array element")
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
