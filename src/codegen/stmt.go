package codegen

import (
	"llvm_lang/src/ast"
	"llvm_lang/src/enums"
	"llvm_lang/src/sema"

	"tinygo.org/x/go-llvm"
)

// genBlock lowers every statement of block in order, stopping as soon as one
// of them terminates its basic block (a return/break/continue, or an if
// whose every branch terminates) - anything after that point is genuinely
// unreachable (this language has no goto/labels to ever jump back into it),
// so it's simply not generated, rather than built into a dead block. Reports
// whether the block as a whole terminated, so an enclosing if/for knows
// whether to branch to what follows it.
//
// base records how many entries were already on Generator.destructors before
// this block started - every VarDecl/ShortVarDecl generated directly inside
// it (genVarDecl/genShortVarDecl) may push its own entry via
// pushDestructorEntry (func.go), and this is exactly the range this block
// itself is responsible for unwinding (see LANGUAGE.md's "Destructors"
// section - "every point control can leave its own declaring block"):
//   - if every statement runs without any of them terminating the block
//     (the ordinary "falls off the end" case), this block's own locals are
//     destructed here, in reverse declaration order, right as it falls
//     through to whatever follows it.
//   - if some statement DID terminate the block (a return/break/continue, or
//     an if whose every branch terminates), that statement has already
//     unwound everything relevant itself, at the exact point it emitted its
//     own terminator (see genReturnStmt/genBreakStmt/genContinueStmt) - to a
//     target that may sit *below* this block's own base (a break/continue
//     several blocks deep unwinds all the way back to the enclosing loop's
//     own destructorBase, not just this block's), so genBlock deliberately
//     does nothing further to Generator.destructors here: whatever the
//     terminating statement already left it as is exactly right, and this
//     block is unreachable from here on anyway (the terminator's own basic
//     block has already been closed).
func (g *Generator) genBlock(block ast.NodeIndex) bool {
	base := len(g.destructors)
	for _, stmt := range g.tree.Children(block) {
		if g.genStmt(stmt) {
			return true
		}
	}
	g.unwindDestructorsTo(base)
	return false
}

// unwindDestructorsTo emits a real destructor call - in reverse declaration
// order, exactly as LANGUAGE.md's "Destructors" section requires - for every
// entry currently on Generator.destructors above target, then truncates the
// stack down to it. Shared by every real scope-exit trigger this feature
// has: a block falling off its own end (genBlock, target = that block's own
// saved base), a return (genReturnStmt/finishBody, target = 0 - the whole
// function unwinds), and a break/continue (genBreakStmt/genContinueStmt,
// target = the enclosing loop's own destructorBase - everything declared
// since entering the loop, but nothing outside it).
func (g *Generator) unwindDestructorsTo(target int) {
	for i := len(g.destructors) - 1; i >= target; i-- {
		e := g.destructors[i]
		g.genDestructorCall(g.locals[e.sym], e.fn, e.fnTy)
	}
	g.destructors = g.destructors[:target]
}

// genDestructorCall calls the destructor function fn/fnTy (a struct's or an
// enum's - see destructorFuncFor, func.go) against addr as its implicit
// `this` - the same implicit-first-pointer-parameter convention an ordinary
// method or constructor call already uses (see CODEGEN.md's "Method
// receivers" section).
func (g *Generator) genDestructorCall(addr, fn llvm.Value, fnTy llvm.Type) {
	g.builder.CreateCall(fnTy, fn, []llvm.Value{addr}, "")
}

// genStmt lowers one statement, reporting whether it terminated the current
// basic block. See ast.Node's doc comment: IfStmt's then/else and ForStmt's
// init/post slots may hold any single statement, not just a Block, so this
// same dispatch (in particular its Block case, recursing into genBlock)
// handles both a nested block and a bare single statement uniformly.
func (g *Generator) genStmt(n ast.NodeIndex) bool {
	switch g.tree.Nodes[n].Kind {
	case enums.NodeKinds.VarDecl:
		g.genVarDecl(n)
		return false
	case enums.NodeKinds.ShortVarDecl:
		g.genShortVarDecl(n)
		return false
	case enums.NodeKinds.MultiShortVarDecl:
		g.genMultiShortVarDecl(n)
		return false
	case enums.NodeKinds.AssignStmt:
		g.genAssignStmt(n)
		return false
	case enums.NodeKinds.MultiAssignStmt:
		g.genMultiAssignStmt(n)
		return false
	case enums.NodeKinds.IncDecStmt:
		g.genIncDecStmt(n)
		return false
	case enums.NodeKinds.ExprStmt:
		g.genExpr(g.tree.Child(n, 0))
		return false
	case enums.NodeKinds.DeleteStmt:
		g.genDeleteStmt(n)
		return false
	case enums.NodeKinds.ReturnStmt:
		return g.genReturnStmt(n)
	case enums.NodeKinds.BreakStmt:
		return g.genBreakStmt(n)
	case enums.NodeKinds.ContinueStmt:
		return g.genContinueStmt(n)
	case enums.NodeKinds.Block:
		return g.genBlock(n)
	case enums.NodeKinds.IfStmt:
		return g.genIfStmt(n)
	case enums.NodeKinds.ForStmt:
		return g.genForStmt(n)
	case enums.NodeKinds.MatchStmt:
		return g.genMatchStmt(n)
	default:
		return false
	}
}

// genVarDecl lowers `var name Type`, `var name = expr`, or
// `var name Type = expr` as a local: a stack slot (see createEntryAlloca),
// zero-initialized when there's no initializer (matching Go's own
// zero-value default), or filled from the initializer otherwise.
func (g *Generator) genVarDecl(n ast.NodeIndex) {
	nameNode := g.tree.Child(n, 0)
	initNode := g.tree.Child(n, 2)
	sym := g.info.Refs[nameNode]
	t := g.info.Types[n]

	llt := g.llvmType(t)
	addr := g.allocLocalSlot(sym, llt, sym.Name)
	g.locals[sym] = addr
	g.pushDestructorEntry(sym, t)

	if initNode == ast.InvalidNode {
		g.builder.CreateStore(llvm.ConstNull(llt), addr)
		return
	}
	g.storeValueInto(addr, initNode)
}

// genShortVarDecl lowers `name := expr` - always has an initializer (the
// parser requires one), so unlike genVarDecl there's no zero-init case.
func (g *Generator) genShortVarDecl(n ast.NodeIndex) {
	nameNode := g.tree.Child(n, 0)
	initNode := g.tree.Child(n, 1)
	sym := g.info.Refs[nameNode]
	t := g.info.Types[n]

	llt := g.llvmType(t)
	addr := g.allocLocalSlot(sym, llt, sym.Name)
	g.locals[sym] = addr
	g.pushDestructorEntry(sym, t)
	g.storeValueInto(addr, initNode)
}

// genMultiShortVarDecl lowers `a, b := f(...)` (see LANGUAGE.md's "Go-style
// multi-return values" section): f is called exactly once - its own real
// LLVM signature already returns the matching anonymous struct (see
// llvmType's TypeMultiReturn case) - and each name's own storage is then
// filled by CreateExtractValue-ing that one aggregate value, exactly
// analogous to how an ordinary struct's own fields are extracted elsewhere in
// this package (see CODEGEN.md's Structs section).
func (g *Generator) genMultiShortVarDecl(n ast.NodeIndex) {
	names := g.tree.MultiShortVarDeclNames(n)
	valueNode := g.tree.MultiShortVarDeclValue(n)

	// `v, ok := m[k]` (see LANGUAGE.md's "Maps" section) - Go's own "two-
	// result index expression," specific to map indexing, not a real
	// TypeMultiReturn-typed call result (see sema's checkDestructureSource)
	// - is a genuinely distinct source shape from every other case this
	// function handles: valueNode is an IndexExpr, not a CallExpr, and
	// genMapIndexRead already returns both components as two separate Go
	// values directly, with no aggregate struct to ExtractValue out of at
	// all.
	if g.tree.Nodes[valueNode].Kind == enums.NodeKinds.IndexExpr {
		value, found := g.genMapIndexRead(valueNode)
		values := [2]llvm.Value{value, found}
		for i, nameNode := range names {
			sym := g.info.Refs[nameNode]
			t := g.info.Types[nameNode]
			llt := g.llvmType(t)
			addr := g.allocLocalSlot(sym, llt, sym.Name)
			g.locals[sym] = addr
			g.pushDestructorEntry(sym, t)
			g.builder.CreateStore(values[i], addr)
		}
		return
	}

	aggregate := g.genExpr(valueNode)
	for i, nameNode := range names {
		sym := g.info.Refs[nameNode]
		t := g.info.Types[nameNode]
		llt := g.llvmType(t)
		addr := g.allocLocalSlot(sym, llt, sym.Name)
		g.locals[sym] = addr
		g.pushDestructorEntry(sym, t)
		g.builder.CreateStore(g.builder.CreateExtractValue(aggregate, i, ""), addr)
	}
}

// storeValueInto stores valueNode's value into the already-computed address
// addr. A composite literal gets filled directly into addr (see
// genCompositeLitInto) rather than built as a temporary and copied - the
// same avoid-the-extra-copy approach used for a var-decl/assignment/
// composite-literal-field destination alike, anywhere a destination address
// is already known up front.
func (g *Generator) storeValueInto(addr llvm.Value, valueNode ast.NodeIndex) {
	if g.tree.Nodes[valueNode].Kind == enums.NodeKinds.CompositeLit {
		g.genCompositeLitInto(addr, valueNode)
		return
	}
	g.builder.CreateStore(g.genExpr(valueNode), addr)
}

// genDeleteStmt lowers `delete p` (see LANGUAGE.md's "Pointers" section): a
// direct call to libc's `free` against p's own pointer value - the real,
// separate heap `new` mallocs from (runtime.go's setupRuntime), never the
// bump-allocator arena, which has no per-allocation free at all.
//
// If p's pointee type declares its own destructor() (see LANGUAGE.md's
// "Destructors" section), that destructor is called - against p itself as
// the implicit `this`, exactly like an ordinary method call - before the
// free, not after: reading through the still-live pointer one last time
// (e.g. a FileHandle's destructor reading/deleting a field of its own) needs
// the pointee's memory to still be valid.
//
// After the free itself, if the deleted operand is a bare local variable/
// parameter reference (deleteLocalSlot below), its own stack slot is also
// stored-over with a null pointer - a narrow, partial use-after-free
// mitigation: it turns *some* immediate reuse-through-the-same-variable bugs
// (`delete p; *p = 5`) into a clean, deterministic null-pointer-dereference
// trap instead of silently corrupting whatever memory got reallocated into
// that freed slot. This is intentionally the only case handled - see
// deleteLocalSlot's own doc comment and LANGUAGE.md's "Pointers" section for
// exactly what this does and doesn't cover (a struct field, an array/slice
// element, a second variable/parameter holding a copy of the same address,
// and a captured-by-reference outer local are all real, deliberately
// unmitigated use-after-free surfaces still - nulling one variable's own
// slot can never reach any of those).
func (g *Generator) genDeleteStmt(n ast.NodeIndex) {
	operand := g.tree.Child(n, 0)
	ptr := g.genExpr(operand)

	if entry, ok := g.destructorFuncForPointee(operand); ok {
		g.genDestructorCall(ptr, entry.fn, entry.fnType)
	}

	g.builder.CreateCall(g.freeType, g.freeFn, []llvm.Value{ptr}, "")

	if addr, ok := g.deleteLocalSlot(operand); ok {
		g.builder.CreateStore(llvm.ConstNull(g.ptrTy), addr)
	}
}

// destructorFuncForPointee reports whether operand's own pointer type (see
// LANGUAGE.md's "Pointers" section - operand must already be pointer-typed,
// sema.checkDeleteStmt guarantees this) points to a struct or enum that
// declares its own destructor() - the delete-specific counterpart to
// pushDestructorEntry (func.go), which asks the identical question about a
// plain local/parameter's own declared type instead of a pointer's pointee -
// both now share destructorFuncFor's own struct-vs-enum dispatch.
func (g *Generator) destructorFuncForPointee(operand ast.NodeIndex) (funcEntry, bool) {
	t := g.info.Types[operand]
	if t.Kind != sema.TypePointer || t.Elem == nil {
		return funcEntry{}, false
	}
	return g.destructorFuncFor(*t.Elem)
}

// deleteLocalSlot reports whether operand (delete's own operand expression,
// stripped of any enclosing parentheses) is a bare reference to a local
// variable/parameter declared directly in the function currently being
// generated - the one case where "the pointer's own storage slot" is
// unambiguous and directly addressable - and if so, returns that slot's
// address (the exact same alloca genAddr's own Ident case would resolve to).
//
// Deliberately narrow: an Ident resolving to a *global* (g.globals, not
// g.locals) or to an outer function's captured symbol (reached through a
// lambda's own closure context, not a plain alloca at all - see
// addrOfSymbol) does not count either, on top of the non-Ident shapes this
// naturally excludes already (a MemberExpr/`.field`, an IndexExpr/`[i]`, or
// any other expression) - only a real, direct alloca in the current
// function's own g.locals is ever nulled.
func (g *Generator) deleteLocalSlot(operand ast.NodeIndex) (llvm.Value, bool) {
	for g.tree.Nodes[operand].Kind == enums.NodeKinds.ParenExpr {
		operand = g.tree.Child(operand, 0)
	}
	if g.tree.Nodes[operand].Kind != enums.NodeKinds.Ident {
		return llvm.Value{}, false
	}
	addr, ok := g.locals[g.info.Refs[operand]]
	return addr, ok
}

// genAssignStmt lowers `=` and the compound forms `+= -= *= /=`. `+=` also
// accepts string (concatenation), matching `+`'s own overload; the rest are
// numeric (any int width or float width - see AGENTS.md's Operators
// section), dispatching to the matching float instruction whenever the
// target's type is a float kind.
func (g *Generator) genAssignStmt(n ast.NodeIndex) {
	targetNode := g.tree.Child(n, 0)
	valueNode := g.tree.Child(n, 1)
	op := g.tree.Text(n)

	// A map-index target (`m[k] = v`) never goes through genAddr - unlike an
	// array element, a map slot may not exist yet, and inserting one is a
	// real get-or-insert-with-possible-growth operation (see maps.go's
	// genMapWriteAddr/genMapGetOrInsertAddr and LANGUAGE.md's "Maps"
	// section), not a plain address computation. Compound assignment/++/--
	// against a map element is rejected by sema (checkAssignStmt/
	// checkIncDecStmt's own isMapIndexTarget checks) - this is always a
	// plain "=" insert-or-update by the time it reaches here.
	if g.isMapIndex(targetNode) {
		addr := g.genMapWriteAddr(targetNode)
		g.storeValueInto(addr, valueNode)
		return
	}

	addr := g.genAddr(targetNode)
	if op == "=" {
		g.storeValueInto(addr, valueNode)
		return
	}

	targetType := g.info.Types[targetNode]
	cur := g.builder.CreateLoad(g.llvmType(targetType), addr, "")
	rhs := g.genExpr(valueNode)
	isFloat := targetType.IsFloatKind()

	var result llvm.Value
	switch op {
	case "+=", "-=", "*=", "/=":
		baseOp := op[:1] // "+=" -> "+", etc.
		if baseOp == "+" && targetType.Kind == sema.TypeString {
			result = g.genStringConcat(cur, rhs)
		} else {
			result = g.genArithOp(baseOp, cur, rhs, isFloat)
		}
	default:
		panic("codegen: unsupported compound assignment operator " + op)
	}
	g.builder.CreateStore(result, addr)
}

// genMultiAssignStmt lowers `a, b = f(...)` - the assignment-form counterpart
// to genMultiShortVarDecl, storing into each target's own already-existing
// address (genAddr, exactly like an ordinary AssignStmt's single target)
// instead of allocating fresh storage. Every target's address is computed
// before the call itself runs, mirroring plain AssignStmt's own
// address-then-value evaluation order.
func (g *Generator) genMultiAssignStmt(n ast.NodeIndex) {
	targets := g.tree.MultiAssignStmtTargets(n)
	valueNode := g.tree.MultiAssignStmtValue(n)

	addrs := make([]llvm.Value, len(targets))
	for i, target := range targets {
		if g.isMapIndex(target) {
			addrs[i] = g.genMapWriteAddr(target)
		} else {
			addrs[i] = g.genAddr(target)
		}
	}

	// `v, ok = m[k]` - the assignment-form counterpart to
	// genMultiShortVarDecl's own identical special case just above; see its
	// doc comment for why this is a distinct IndexExpr shape, not a real
	// TypeMultiReturn-typed CallExpr result.
	if g.tree.Nodes[valueNode].Kind == enums.NodeKinds.IndexExpr {
		value, found := g.genMapIndexRead(valueNode)
		values := [2]llvm.Value{value, found}
		for i, addr := range addrs {
			g.builder.CreateStore(values[i], addr)
		}
		return
	}

	aggregate := g.genExpr(valueNode)
	for i, addr := range addrs {
		g.builder.CreateStore(g.builder.CreateExtractValue(aggregate, i, ""), addr)
	}
}

// genIncDecStmt lowers `++`/`--` - any numeric type (any int width or float
// width - see AGENTS.md's Operators section), using the target's own actual
// type/width rather than assuming i32.
func (g *Generator) genIncDecStmt(n ast.NodeIndex) {
	target := g.tree.Child(n, 0)
	addr := g.genAddr(target)
	t := g.info.Types[target]
	llt := g.llvmType(t)
	cur := g.builder.CreateLoad(llt, addr, "")
	isInc := g.tree.Text(n) == "++"

	isFloat := t.IsFloatKind()
	var one llvm.Value
	if isFloat {
		one = llvm.ConstFloat(llt, 1)
	} else {
		one = llvm.ConstInt(llt, 1, true)
	}
	op := "-"
	if isInc {
		op = "+"
	}
	result := g.genArithOp(op, cur, one, isFloat)
	g.builder.CreateStore(result, addr)
}

// genReturnStmt lowers `return` (bare or with a value). A bare return in a
// function declaring no return type - main included - produces `ret void`,
// except main itself always needs a real i32 exit code (see
// declareFuncSignature): a bare `return` in main is `ret i32 0`.
func (g *Generator) genReturnStmt(n ast.NodeIndex) bool {
	valueNode := g.tree.Child(n, 0)
	if valueNode == ast.InvalidNode {
		g.unwindDestructorsTo(0)
		if g.curFunc.isMain {
			g.builder.CreateRet(llvm.ConstInt(g.i32Ty, 0, false))
		} else {
			g.builder.CreateRetVoid()
		}
		return true
	}
	// Evaluate the returned value first, while every still-in-scope local is
	// still alive/valid, then unwind (see LANGUAGE.md's "Destructors"
	// section: a return exits the whole function, so this always unwinds
	// every entry currently on the stack, not just the innermost block's
	// own), then actually return the already-computed value.
	var v llvm.Value
	if g.tree.Nodes[valueNode].Kind == enums.NodeKinds.MultiValueExpr {
		v = g.genMultiValueExpr(valueNode)
	} else {
		v = g.genExpr(valueNode)
	}
	g.unwindDestructorsTo(0)
	g.builder.CreateRet(v)
	return true
}

// genMultiValueExpr builds the anonymous LLVM struct aggregate a multi-value
// `return a, b, ...` (see LANGUAGE.md's "Go-style multi-return values"
// section) lowers to - reusing the exact same "return a struct by value" ABI
// an ordinary struct-returning function already uses (see CODEGEN.md): the
// enclosing function's own real LLVM return type (g.curFunc.retType, see
// funcCtx's own doc comment) is this same anonymous struct type, built here
// via llvm.Undef + one CreateInsertValue per value - the same runtime-
// aggregate-construction approach genFuncLit's own closure value already
// uses (a ConstStruct can't be used here: unlike a bare free-function
// reference's fat pointer, each returned value is a genuine, independently-
// computed runtime SSA value, never a compile-time constant in general).
func (g *Generator) genMultiValueExpr(n ast.NodeIndex) llvm.Value {
	retTy := g.llvmType(g.curFunc.retType)
	result := llvm.Undef(retTy)
	for i, valueNode := range g.tree.Children(n) {
		result = g.builder.CreateInsertValue(result, g.genExpr(valueNode), i, "")
	}
	return result
}

// genBreakStmt/genContinueStmt branch to the innermost enclosing loop's
// break/continue target (see genForStmt). `sema.Check` now guarantees a
// break/continue only ever appears inside a ForStmt's body (see
// checkBreakOrContinue in sema/typecheck.go, and BLOCKERS.md's codegen-phase
// entry #6 for the gap this closed) - this was previously one of the few
// checks codegen performed on its own (reported as a diagnostic, lowered to
// `unreachable`), back when that guarantee didn't exist yet. An empty
// loopStack here now means the tree wasn't actually valid per sema, which
// this whole package already assumes never happens (see the package doc
// comment) - so, same as everywhere else that assumption is relied on, this
// is a panic rather than a diagnostic.
func (g *Generator) genBreakStmt(n ast.NodeIndex) bool {
	if len(g.loopStack) == 0 {
		panic("codegen: break outside a loop - sema.Check should have rejected this")
	}
	top := g.loopStack[len(g.loopStack)-1]
	g.unwindDestructorsTo(top.destructorBase)
	g.builder.CreateBr(top.breakTarget)
	return true
}

func (g *Generator) genContinueStmt(n ast.NodeIndex) bool {
	if len(g.loopStack) == 0 {
		panic("codegen: continue outside a loop - sema.Check should have rejected this")
	}
	top := g.loopStack[len(g.loopStack)-1]
	g.unwindDestructorsTo(top.destructorBase)
	g.builder.CreateBr(top.continueTarget)
	return true
}

// genIfStmt lowers both grammar forms (`if cond: stmt` and the brace form
// with an optional else/else-if chain) - they produce the identical
// [cond, then, else] shape post-parse (see ast.Node's IfStmt doc comment),
// so a single lowering handles both. Reports termination only when both
// branches exist and both terminate; when there's no else, control can
// always still fall through to what follows, so the statement as a whole
// never terminates.
//
// then/else are alternate, mutually exclusive continuations from the exact
// same starting point - not a sequential continuation of each other the way
// two statements in the same Block are - so each one's own codegen must
// start from (and whatever follows the if must end up seeing) the identical
// pre-if Generator.destructors state, never a bookkeeping side effect
// generating the *other* branch happened to leave behind: a
// return/break/continue inside one branch legitimately pops entries a
// sibling branch never actually saw removed at runtime (only one branch
// ever really executes), and, unlike a Block's own genBlock call, there's no
// enclosing "restore to my own base" logic for either branch on its own -
// genIfStmt is what has to do it explicitly, once per branch (preIf is
// copied, not just its length re-sliced, since a branch's own codegen may
// itself push fresh entries into the same backing array, which a
// length-only restore could then read back incorrectly).
func (g *Generator) genIfStmt(n ast.NodeIndex) bool {
	condNode := g.tree.Child(n, 0)
	thenNode := g.tree.Child(n, 1)
	elseNode := g.tree.Child(n, 2)
	hasElse := elseNode != ast.InvalidNode

	condVal := g.genExpr(condNode)

	thenBB := g.ctx.AddBasicBlock(g.curFn, "if.then")
	mergeBB := g.ctx.AddBasicBlock(g.curFn, "if.merge")
	elseBB := mergeBB
	if hasElse {
		elseBB = g.ctx.AddBasicBlock(g.curFn, "if.else")
	}
	g.builder.CreateCondBr(condVal, thenBB, elseBB)

	preIf := append([]destructorEntry(nil), g.destructors...)

	g.builder.SetInsertPointAtEnd(thenBB)
	thenTerm := g.genStmt(thenNode)
	if !thenTerm {
		g.builder.CreateBr(mergeBB)
	}
	g.destructors = append([]destructorEntry(nil), preIf...)

	elseTerm := false
	if hasElse {
		g.builder.SetInsertPointAtEnd(elseBB)
		elseTerm = g.genStmt(elseNode)
		if !elseTerm {
			g.builder.CreateBr(mergeBB)
		}
		g.destructors = append([]destructorEntry(nil), preIf...)
	}

	g.builder.SetInsertPointAtEnd(mergeBB)
	terminates := hasElse && thenTerm && elseTerm
	if terminates {
		// Both branches terminated, so neither one ever emitted a CreateBr
		// into mergeBB (each only does that in its own `if !thenTerm`/
		// `if !elseTerm` guard above) - mergeBB is a genuinely unreachable
		// block with zero predecessors, and genBlock's caller will never
		// generate anything past this statement to reach it either (see
		// genBlock's own doc comment: it stops calling genStmt the moment
		// one reports termination). Give it a real terminator before
		// returning rather than leaving an empty, terminator-less block
		// behind for LLVM's verifier to reject - the same `unreachable`
		// convention this package's own AGENTS.md "Terminator safety"
		// section already uses for exactly this "structurally impossible to
		// reach, but LLVM still requires a valid terminator" situation (see
		// emitFallbackTerminator, func.go).
		g.builder.CreateUnreachable()
	}
	return terminates
}

// genForStmt lowers all three Go-style for-loop forms uniformly - bare
// `for {}`, cond-only `for cond {}`, and the full `for init; cond; post {}` -
// since ForStmt's [init, cond, post, body] shape already represents every
// form the same way, with the unused clauses simply InvalidNode (see
// ast.Node's ForStmt doc comment).
//
// continue branches to the post-statement block (so post always runs before
// the condition is re-checked, same as Go); break branches past the whole
// loop. A for-loop is conservatively reported as never terminating the
// statement it's part of (control can always reach for.end, at least
// structurally) - detecting a true infinite `for {}` with no reachable
// break is exactly the kind of full flow analysis this project's
// type-checking phase already deferred (see AGENTS.md/BLOCKERS.md), and
// isn't reattempted here.
//
// preInitBase is captured before generating initNode, not after: init's own
// declared local (a non-copyable `for r := Resource(1); ...; ...`, say) is
// visible to the condition/post/body but scoped to nowhere past the loop's
// own exit (see sema/resolve.go's doc comment on this exact `for` scoping
// rule), so it must destruct right there, at endBB - loopCtx's own
// destructorBase (captured after init, once the builder is at bodyBB) is
// deliberately a separate, later snapshot: that one is break/continue's
// target and must exclude init's own entry, since a `continue` re-enters the
// loop without leaving its scope at all (init's local legitimately survives
// across iterations), and a `break` already reaches endBB via the branch
// below, where this function's own unwindDestructorsTo(preInitBase) call
// runs anyway. Only once, right here, after the loop has structurally
// finished (natural condition-false exit or a break landing at endBB) does
// init's own local actually get destructed.
//
// Per-iteration loop variable (Go 1.22+ semantics - see LANGUAGE.md's
// "Lambdas" section): if init declares exactly the one name this grammar
// allows (`i := ...`/`var i ... = ...`) and sema marked that symbol
// Captured (some FuncLit in the body closes over it - sema/capture.go's
// analyzeFuncLitCaptures), a closure created on iteration K must see
// iteration K's own value, not whatever i++ mutates it to by the time that
// closure is actually called (a shared arena slot - see allocLocalSlot,
// func.go - would otherwise make every closure observe the loop's final
// value, since init only ever runs once, before bodyBB/postBB even exist).
// loopVarSym/loopVarOrigAddr/loopVarType/loopVarEligible are plain local
// variables scoped to this one genForStmt call/goroutine-stack-frame
// (deliberately not Generator fields) so nested for-loops each track their
// own independent per-iteration variable via ordinary Go recursion, with no
// cross-contamination between levels.
//
// The fix itself is two symmetric hand-offs around the body:
//   - entering bodyBB: copy origAddr's current value into a fresh arena slot
//     and repoint g.locals[sym] at it, so the body (and any FuncLit inside
//     it) reads/writes this iteration's own private copy - a FuncLit
//     capturing sym therefore captures that fresh address, never origAddr.
//   - entering postBB (before postNode runs): copy the fresh slot's
//     (possibly body-mutated) value back into origAddr and repoint
//     g.locals[sym] back to it, so the condition/post clause keep observing
//     the single real loop-variable slot exactly as before. postBB's entry
//     is reached by both the ordinary body-fallthrough branch and every
//     `continue` (continueTarget is postBB - see loopCtx above), so this one
//     placement covers both uniformly; `break` branches straight to endBB,
//     bypassing postBB entirely, so it never runs this hand-off at all
//     (nothing needs to - endBB's own unwindDestructorsTo(preInitBase)
//     destructs origAddr's real symbol exactly as it always has, and the
//     abandoned fresh slot is simply unreferenced arena garbage, consistent
//     with this project's already-documented arena philosophy).
//
// Guarded by sema.IsNonCopyable below: a non-copyable loop variable (a
// disallowed shape today - AGENTS.md's Types section - but guarded
// defensively regardless) keeps today's exact shared-slot behavior, since an
// implicit copy here would silently violate this language's "non-copyable,
// zero exceptions" rule. Reuses sema's own package-level IsNonCopyable
// (src/sema/typecheck.go) rather than a hand-maintained codegen-local copy -
// safe to call unconditionally here since codegen only ever runs on a tree
// that already passed a complete sema.Check/sema.CheckPackage pass, which
// has already memoized every struct type's own Copyable field (see
// IsNonCopyable's own doc comment for exactly why that assumption is what
// makes it safe here but not mid-checking).
//
// This eligibility switch also only ever matches a single-name ShortVarDecl/
// VarDecl init clause - a multi-return destructuring init
// (`for a, b := f(); ...; ... {}`, MultiShortVarDecl - see LANGUAGE.md's
// "Functions" section) falls outside it by construction and so keeps
// today's exact shared-slot capture behavior for both names, same as this
// language's own pre-1.22-Go-style default before this per-iteration fix
// existed at all. A deliberately narrow, out-of-scope gap for now (this
// feature's own scope was kept to "destructuring only, no expansion" - see
// DECISIONS.md's dated "Go-style multi-return values" entry) rather than an
// oversight: it can be added later (looping over every declared name instead
// of assuming exactly one) if a real program ever needs a closure inside a
// multi-return-destructuring `for` loop to see fresh per-iteration values.
func (g *Generator) genForStmt(n ast.NodeIndex) bool {
	initNode := g.tree.Child(n, 0)
	condNode := g.tree.Child(n, 1)
	postNode := g.tree.Child(n, 2)
	bodyNode := g.tree.Child(n, 3)

	preInitBase := len(g.destructors)

	var (
		loopVarSym      *sema.Symbol
		loopVarOrigAddr llvm.Value
		loopVarType     llvm.Type
		loopVarEligible bool
	)
	if initNode != ast.InvalidNode {
		g.genStmt(initNode)

		switch g.tree.Nodes[initNode].Kind {
		case enums.NodeKinds.ShortVarDecl, enums.NodeKinds.VarDecl:
			nameNode := g.tree.Child(initNode, 0)
			sym := g.info.Refs[nameNode]
			if sym.Captured {
				t := g.info.Types[initNode]
				if !sema.IsNonCopyable(t) {
					loopVarSym = sym
					loopVarOrigAddr = g.locals[sym]
					loopVarType = g.llvmType(t)
					loopVarEligible = true
				}
			}
		}
	}

	condBB := g.ctx.AddBasicBlock(g.curFn, "for.cond")
	bodyBB := g.ctx.AddBasicBlock(g.curFn, "for.body")
	postBB := g.ctx.AddBasicBlock(g.curFn, "for.post")
	endBB := g.ctx.AddBasicBlock(g.curFn, "for.end")

	g.builder.CreateBr(condBB)
	g.builder.SetInsertPointAtEnd(condBB)
	if condNode != ast.InvalidNode {
		g.builder.CreateCondBr(g.genExpr(condNode), bodyBB, endBB)
	} else {
		g.builder.CreateBr(bodyBB)
	}

	g.builder.SetInsertPointAtEnd(bodyBB)
	if loopVarEligible {
		freshAddr := g.genArenaAlloc(llvm.SizeOf(loopVarType))
		cur := g.builder.CreateLoad(loopVarType, loopVarOrigAddr, "")
		g.builder.CreateStore(cur, freshAddr)
		g.locals[loopVarSym] = freshAddr
	}
	g.loopStack = append(g.loopStack, loopCtx{
		breakTarget:    endBB,
		continueTarget: postBB,
		destructorBase: len(g.destructors),
	})
	bodyTerm := g.genBlock(bodyNode)
	g.loopStack = g.loopStack[:len(g.loopStack)-1]
	if !bodyTerm {
		g.builder.CreateBr(postBB)
	}

	g.builder.SetInsertPointAtEnd(postBB)
	if loopVarEligible {
		cur := g.builder.CreateLoad(loopVarType, g.locals[loopVarSym], "")
		g.builder.CreateStore(cur, loopVarOrigAddr)
		g.locals[loopVarSym] = loopVarOrigAddr
	}
	if postNode != ast.InvalidNode {
		g.genStmt(postNode)
	}
	g.builder.CreateBr(condBB)

	g.builder.SetInsertPointAtEnd(endBB)
	g.unwindDestructorsTo(preInitBase)
	return false
}
