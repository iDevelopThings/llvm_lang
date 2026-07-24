package codegen

import (
	"slices"

	"llvm_lang/src/ast"
	"llvm_lang/src/enums"
	"llvm_lang/src/sema"

	"tinygo.org/x/go-llvm"
)

// programUsesCoroutines reports whether trees needs any coroutine codegen
// machinery at all - see GeneratePackage's own doc comment for why
// setupCoroutines is skipped entirely otherwise. Two independent triggers,
// both checked: declaring an `async func` (even if never called - its own
// body still needs genCoroPrologue/finishCoroBody's intrinsics), and the
// `coroutine` type keyword appearing in any var/field/param declaration
// (info.Types covers every type-position node - see typeFromNode's own doc
// comment) even with no async func anywhere to ever produce a live handle -
// a coroutine-typed local still reaches destructorFuncFor's TypeCoroutine
// case (coroDestroyLocalFn) at codegen time regardless.
func programUsesCoroutines(trees []*ast.Tree, infos map[*ast.Tree]*sema.Info) bool {
	for _, tree := range trees {
		for d := range tree.TopLevelDeclsOfKind(enums.NodeKinds.FuncDecl) {
			if tree.FuncIsAsync(d) {
				return true
			}
		}
		for _, t := range infos[tree].Types {
			if t.Kind == sema.TypeCoroutine {
				return true
			}
		}
	}
	return false
}

// setupCoroutines declares every llvm.coro.* intrinsic this package's
// async/await lowering needs (see CODEGEN.md's "Coroutines" section) via the
// generic intrinsic-declaration mechanism (LookupIntrinsicID/IntrinsicType/
// IntrinsicDeclaration - coroutine intrinsics have no dedicated llvm-c
// header the way malloc/printf's plain extern declarations do), resolves the
// presplitcoroutine function attribute every coroutine function must carry,
// and builds coroDestroyLocalFn (see its own field doc comment, codegen.go).
func (g *Generator) setupCoroutines() {
	g.coroIdFn, g.coroIdType = g.coroIntrinsic("llvm.coro.id", nil)
	g.coroSizeFn, g.coroSizeType = g.coroIntrinsic("llvm.coro.size", []llvm.Type{g.i64Ty})
	g.coroBeginFn, g.coroBeginType = g.coroIntrinsic("llvm.coro.begin", nil)
	g.coroFreeFn, g.coroFreeType = g.coroIntrinsic("llvm.coro.free", nil)
	g.coroEndFn, g.coroEndType = g.coroIntrinsic("llvm.coro.end", nil)
	g.coroSuspendFn, g.coroSuspendType = g.coroIntrinsic("llvm.coro.suspend", nil)
	g.coroSaveFn, g.coroSaveType = g.coroIntrinsic("llvm.coro.save", nil)
	g.coroResumeFn, g.coroResumeType = g.coroIntrinsic("llvm.coro.resume", nil)
	g.coroDestroyFn, g.coroDestroyType = g.coroIntrinsic("llvm.coro.destroy", nil)
	g.coroDoneFn, g.coroDoneType = g.coroIntrinsic("llvm.coro.done", nil)

	g.presplitCoroutineAttrKind = llvm.AttributeKindID("presplitcoroutine")
	g.coroTokenTy = g.ctx.TokenType()

	g.buildCoroDestroyLocalFn()
}

// coroIntrinsic resolves and declares the named LLVM intrinsic into the
// module, returning its callable Value alongside its real function Type -
// read back via IntrinsicType rather than a hand-built llvm.FunctionType, so
// a signature typo can never silently diverge from what LLVM itself expects
// (see CODEGEN.md's "Coroutines" section for a real example: llvm.coro.end's
// third parameter is a token, not an i1, a mistake this exact approach
// catches). paramTypes selects an overload (e.g. []llvm.Type{g.i64Ty} for
// llvm.coro.size.i64) - nil for every non-overloaded intrinsic.
func (g *Generator) coroIntrinsic(name string, paramTypes []llvm.Type) (llvm.Value, llvm.Type) {
	id := llvm.LookupIntrinsicID(name)
	fnTy := g.ctx.IntrinsicType(id, paramTypes)
	fn := g.mod.IntrinsicDeclaration(id, paramTypes)
	return fn, fnTy
}

// buildCoroDestroyLocalFn builds llvm_lang.coro.destroylocal - void(ptr
// addr) - see its own field doc comment (codegen.go) for why this small
// adapter exists at all.
//
// Nil-guarded like genResumeCall/genDoneCall/genCoroDeleteStmt: genIfStmt's
// snapshot/restore discipline (stmt.go) can resurrect a coroutine local's
// destructor entry after one branch already `delete`d and nulled it, so
// automatic scope-exit cleanup may run against an already-nulled handle -
// without this guard that's a double llvm.coro.destroy call (real UB, only
// silently "worked" because the optimizer happened to collapse it).
func (g *Generator) buildCoroDestroyLocalFn() {
	g.coroDestroyLocalType = llvm.FunctionType(g.voidTy, []llvm.Type{g.ptrTy}, false)
	fn := llvm.AddFunction(g.mod, "llvm_lang.coro.destroylocal", g.coroDestroyLocalType)
	fn.SetLinkage(llvm.PrivateLinkage)
	g.coroDestroyLocalFn = fn

	entryBB := g.ctx.AddBasicBlock(fn, "entry")
	g.builder.SetInsertPointAtEnd(entryBB)
	handle := g.builder.CreateLoad(g.ptrTy, fn.Param(0), "")

	isNil := g.builder.CreateIsNull(handle, "")
	destroyBB := g.ctx.AddBasicBlock(fn, "destroy")
	retBB := g.ctx.AddBasicBlock(fn, "ret")
	g.builder.CreateCondBr(isNil, retBB, destroyBB)

	g.builder.SetInsertPointAtEnd(destroyBB)
	g.builder.CreateCall(g.coroDestroyType, g.coroDestroyFn, []llvm.Value{handle}, "")
	g.builder.CreateBr(retBB)

	g.builder.SetInsertPointAtEnd(retBB)
	g.builder.CreateRetVoid()
}

// genCoroPrologue emits an async function's own ramp prologue - the fixed
// coro.id/coro.size/malloc/coro.begin sequence CODEGEN.md's "Coroutines"
// section documents - into the current (entry) block, storing the resulting
// id/handle onto Generator for genAwaitStmt/coroEndBlock/genFuncBody's
// bare-return case to read. No llvm.coro.alloc allocation-elision check is
// emitted (see CODEGEN.md) - every coroutine call always heap-allocates its
// own frame, matching the verified minimal shape this is grounded in.
func (g *Generator) genCoroPrologue() {
	id := g.builder.CreateCall(g.coroIdType, g.coroIdFn, []llvm.Value{
		llvm.ConstInt(g.i32Ty, 0, false),
		llvm.ConstNull(g.ptrTy),
		llvm.ConstNull(g.ptrTy),
		llvm.ConstNull(g.ptrTy),
	}, "")
	size := g.builder.CreateCall(g.coroSizeType, g.coroSizeFn, nil, "")
	mem := g.builder.CreateCall(g.mallocType, g.mallocFn, []llvm.Value{size}, "")
	hdl := g.builder.CreateCall(g.coroBeginType, g.coroBeginFn, []llvm.Value{id, mem}, "")

	g.curCoroId = id
	g.curCoroHandle = hdl
}

// coroEndBlock returns the current coroutine function's own shared "physical
// suspend point" block - lazily created on first use. Does ONLY
// llvm.coro.end + ret, never frees the frame itself - every destroy path
// (genAwaitStmt's destroyBB, finishCoroBody's own) frees the frame first and
// only then falls through here (see CODEGEN.md's "Coroutines" section).
func (g *Generator) coroEndBlock() llvm.BasicBlock {
	if !g.curCoroTeardownBB.IsNil() {
		return g.curCoroTeardownBB
	}
	savedBB := g.builder.GetInsertBlock()
	bb := g.ctx.AddBasicBlock(g.curFn, "coro.end")
	g.builder.SetInsertPointAtEnd(bb)
	g.builder.CreateCall(g.coroEndType, g.coroEndFn, []llvm.Value{
		g.curCoroHandle,
		llvm.ConstInt(g.boolTy, 0, false),
		llvm.ConstNull(g.coroTokenTy),
	}, "")
	g.builder.CreateRet(g.curCoroHandle)

	g.curCoroTeardownBB = bb
	g.builder.SetInsertPointAtEnd(savedBB)
	return bb
}

// genCoroFreeFrame emits llvm.coro.free + free against the current
// coroutine's own frame - shared by every real destroy path (genAwaitStmt's
// destroyBB, finishCoroBody's own) right before each one falls through to
// coroEndBlock.
func (g *Generator) genCoroFreeFrame() {
	mem := g.builder.CreateCall(g.coroFreeType, g.coroFreeFn, []llvm.Value{g.curCoroId, g.curCoroHandle}, "")
	g.builder.CreateCall(g.freeType, g.freeFn, []llvm.Value{mem}, "")
}

// finishCoroBody is finishBody's coroutine-specific counterpart (see
// genFuncBody), reached once body falls off its own end (genReturnStmt
// routes a bare `return` through the same sequence). Unwinds whatever's left
// on Generator.destructors, then suspends one final time (i1 true) per
// LLVM's own final-suspend shape (llvm.org/docs/Coroutines.html#coro-destroy):
// default = bare suspend (done(h) keeps reading the frame), case 1 = real
// destroy (frees the frame), case 0 = trap - resuming past final suspend is
// documented UB, which genResumeCall never does (it checks done(h) first).
func (g *Generator) finishCoroBody() {
	g.unwindDestructorsTo(0)
	save := g.builder.CreateCall(g.coroSaveType, g.coroSaveFn, []llvm.Value{llvm.ConstNull(g.ptrTy)}, "")
	suspend := g.builder.CreateCall(g.coroSuspendType, g.coroSuspendFn, []llvm.Value{
		save, llvm.ConstInt(g.boolTy, 1, false),
	}, "")

	trapBB := g.ctx.AddBasicBlock(g.curFn, "final.resumed.trap")
	destroyBB := g.ctx.AddBasicBlock(g.curFn, "final.destroy")
	sw := g.builder.CreateSwitch(suspend, g.coroEndBlock(), 2)
	sw.AddCase(llvm.ConstInt(g.i8Ty, 0, false), trapBB)
	sw.AddCase(llvm.ConstInt(g.i8Ty, 1, false), destroyBB)

	g.builder.SetInsertPointAtEnd(trapBB)
	g.builder.CreateCall(g.trapType, g.trapFn, nil, "")
	g.builder.CreateUnreachable()

	g.builder.SetInsertPointAtEnd(destroyBB)
	g.genCoroFreeFrame()
	g.builder.CreateBr(g.coroEndBlock())
}

// genAwaitStmt lowers a bare `await` - the core suspend/resume primitive
// (see CODEGEN.md's "Coroutines" section). Emits coro.save+coro.suspend
// (final=false), then a switch: case 0 (resumed) falls through to ordinary
// control flow; case 1 (destroyed while suspended here) runs destructor
// calls against a snapshot of Generator.destructors taken at this exact
// point, then frees the frame and falls through to coroEndBlock; the
// default arm (the ramp's own first pass reaching this point) goes straight
// to coroEndBlock with no cleanup - LLVM's own documented switch convention
// (llvm.org/docs/Coroutines.html#coro-destroy). Every await gets its own
// dedicated destroy block rather than a shared one dispatched via a saved
// suspend-index, since coro.suspend's own per-call switch already gives each
// suspend point a distinct case-1 target for free.
func (g *Generator) genAwaitStmt() bool {
	liveDestructors := slices.Clone(g.destructors)

	save := g.builder.CreateCall(g.coroSaveType, g.coroSaveFn, []llvm.Value{llvm.ConstNull(g.ptrTy)}, "")
	suspend := g.builder.CreateCall(g.coroSuspendType, g.coroSuspendFn, []llvm.Value{
		save, llvm.ConstInt(g.boolTy, 0, false),
	}, "")

	contBB := g.ctx.AddBasicBlock(g.curFn, "await.cont")
	destroyBB := g.ctx.AddBasicBlock(g.curFn, "await.destroy")
	sw := g.builder.CreateSwitch(suspend, g.coroEndBlock(), 2)
	sw.AddCase(llvm.ConstInt(g.i8Ty, 0, false), contBB)
	sw.AddCase(llvm.ConstInt(g.i8Ty, 1, false), destroyBB)

	g.builder.SetInsertPointAtEnd(destroyBB)
	for i := len(liveDestructors) - 1; i >= 0; i-- {
		e := liveDestructors[i]
		g.genDestructorCall(g.locals[e.sym], e.fn, e.fnTy)
	}
	g.genCoroFreeFrame()
	g.builder.CreateBr(g.coroEndBlock())

	g.builder.SetInsertPointAtEnd(contBB)
	return false
}

// genResumeCall implements the `resume(h) bool` builtin (see LANGUAGE.md's
// "Coroutines" section): resumes handle once if it's live and not already
// done, reporting whether it suspended again (true, more to do) or has now
// finished/was already finished (false). A nil handle (already `delete`d -
// see genCoroDeleteStmt) is a safe, defined no-op reporting false, mirroring
// the same handle-nulling convention plain pointer `delete` already uses.
// Checking coro.done immediately before AND after the raw coro.resume call
// is what makes this safe to call on an already-finished coroutine at all -
// resuming past a coroutine's own final suspend point is otherwise not a
// defined operation this package relies on.
func (g *Generator) genResumeCall(handle llvm.Value) llvm.Value {
	entryBB := g.builder.GetInsertBlock()
	isNil := g.builder.CreateIsNull(handle, "")

	liveBB := g.ctx.AddBasicBlock(g.curFn, "resume.live")
	mergeBB := g.ctx.AddBasicBlock(g.curFn, "resume.merge")
	g.builder.CreateCondBr(isNil, mergeBB, liveBB)

	g.builder.SetInsertPointAtEnd(liveBB)
	alreadyDone := g.builder.CreateCall(g.coroDoneType, g.coroDoneFn, []llvm.Value{handle}, "")
	doBB := g.ctx.AddBasicBlock(g.curFn, "resume.do")
	g.builder.CreateCondBr(alreadyDone, mergeBB, doBB)

	g.builder.SetInsertPointAtEnd(doBB)
	g.builder.CreateCall(g.coroResumeType, g.coroResumeFn, []llvm.Value{handle}, "")
	nowDone := g.builder.CreateCall(g.coroDoneType, g.coroDoneFn, []llvm.Value{handle}, "")
	stillSuspended := g.builder.CreateNot(nowDone, "")
	g.builder.CreateBr(mergeBB)
	doEndBB := g.builder.GetInsertBlock()

	g.builder.SetInsertPointAtEnd(mergeBB)
	phi := g.builder.CreatePHI(g.boolTy, "")
	phi.AddIncoming(
		[]llvm.Value{llvm.ConstInt(g.boolTy, 0, false), llvm.ConstInt(g.boolTy, 0, false), stillSuspended},
		[]llvm.BasicBlock{entryBB, liveBB, doEndBB},
	)
	return phi
}

// genDoneCall implements the `done(h) bool` builtin (see LANGUAGE.md's
// "Coroutines" section). A nil handle (already `delete`d) reports true - a
// deleted coroutine is unambiguously "done" from this language's own point
// of view, the same reasoning genResumeCall's own nil case uses.
func (g *Generator) genDoneCall(handle llvm.Value) llvm.Value {
	entryBB := g.builder.GetInsertBlock()
	isNil := g.builder.CreateIsNull(handle, "")

	liveBB := g.ctx.AddBasicBlock(g.curFn, "done.live")
	mergeBB := g.ctx.AddBasicBlock(g.curFn, "done.merge")
	g.builder.CreateCondBr(isNil, mergeBB, liveBB)

	g.builder.SetInsertPointAtEnd(liveBB)
	doneVal := g.builder.CreateCall(g.coroDoneType, g.coroDoneFn, []llvm.Value{handle}, "")
	g.builder.CreateBr(mergeBB)

	g.builder.SetInsertPointAtEnd(mergeBB)
	phi := g.builder.CreatePHI(g.boolTy, "")
	phi.AddIncoming(
		[]llvm.Value{llvm.ConstInt(g.boolTy, 1, false), doneVal},
		[]llvm.BasicBlock{entryBB, liveBB},
	)
	return phi
}

// genCoroDeleteStmt lowers `delete h` for a coroutine-handle operand (see
// genDeleteStmt's own dispatch and LANGUAGE.md's "Coroutines" section) -
// destroys handle if it isn't already nil (a defined no-op otherwise,
// mirroring genResumeCall/genDoneCall's identical guard), removes h's own
// entry from Generator.destructors (so automatic scope-exit cleanup never
// double-destroys it - see removeDestructorEntry), and, for a bare local
// variable operand, nulls its slot exactly like plain pointer `delete`
// already does (deleteLocalSlot) - the one case genuinely new here: a
// coroutine handle is both explicitly `delete`-able AND automatically
// destructor-tracked, unlike a plain pointer (never on the destructor stack
// at all) or a struct local (never explicitly `delete`-able).
func (g *Generator) genCoroDeleteStmt(operand ast.NodeIndex) {
	handle := g.genExpr(operand)

	isNil := g.builder.CreateIsNull(handle, "")
	destroyBB := g.ctx.AddBasicBlock(g.curFn, "coro.delete.destroy")
	mergeBB := g.ctx.AddBasicBlock(g.curFn, "coro.delete.merge")
	g.builder.CreateCondBr(isNil, mergeBB, destroyBB)

	g.builder.SetInsertPointAtEnd(destroyBB)
	g.builder.CreateCall(g.coroDestroyType, g.coroDestroyFn, []llvm.Value{handle}, "")
	g.builder.CreateBr(mergeBB)

	g.builder.SetInsertPointAtEnd(mergeBB)
	if sym, ok := g.localSymForOperand(operand); ok {
		g.removeDestructorEntry(sym)
		g.builder.CreateStore(llvm.ConstNull(g.ptrTy), g.locals[sym])
	}
}

// removeDestructorEntry drops sym's own entry from Generator.destructors,
// wherever it currently sits (not necessarily on top - other locals declared
// after it may still be in scope) - preserving every other entry's relative
// order, since unwindDestructorsTo's own reverse-declaration-order guarantee
// depends on it. A no-op if sym has no entry (never pushed one to begin
// with, e.g. any type without its own destructor).
func (g *Generator) removeDestructorEntry(sym *sema.Symbol) {
	for i, e := range g.destructors {
		if e.sym == sym {
			g.destructors = slices.Delete(g.destructors, i, i+1)
			return
		}
	}
}

// localSymForOperand reports the *sema.Symbol operand (stripped of any
// enclosing parens) resolves to, if it's a bare reference to a local
// variable/parameter declared directly in the function currently being
// generated - shared by deleteLocalSlot (a plain pointer's own identical
// question) and genCoroDeleteStmt.
func (g *Generator) localSymForOperand(operand ast.NodeIndex) (*sema.Symbol, bool) {
	for g.tree.Nodes[operand].Kind == enums.NodeKinds.ParenExpr {
		operand = g.tree.Child(operand, 0)
	}
	if g.tree.Nodes[operand].Kind != enums.NodeKinds.Ident {
		return nil, false
	}
	sym := g.info.Refs[operand]
	if _, ok := g.locals[sym]; !ok {
		return nil, false
	}
	return sym, true
}
