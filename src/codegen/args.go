// This file implements the predeclared `args() []string` builtin (see
// LANGUAGE.md's "The args() builtin" section) - a dynamic array of every
// command-line argument the running program was invoked with, built exactly
// once at process startup rather than re-marshaled on every call.
//
// Design (see DECISIONS.md's dated "args() builtin" entry for the full
// why): rather than changing `main`'s own real LLVM signature/linkage (see
// CODEGEN.md's "`main` is the real entry point" section) to take a real
// `(argc, argv)` parameter pair - which would mean every existing JIT call
// site invoking `main` via a raw zero-argument syscall (cmd/llvmc's
// jitRunMain, and dozens of this package's own `jm.runInt32(t, "main")`
// tests) would suddenly need to pass two real, meaningful arguments instead,
// a real regression risk this round explicitly avoids - this reads argc/argv
// from `__argc`/`__argv`, two real global symbols mingw64's own C runtime
// startup (msvcrt/ucrt) already populates before any `@llvm.global_ctors`
// entry or `main` itself ever runs, exactly the same well-established
// extension real MSVC/mingw programs already rely on. `main`'s own signature
// and every existing JIT call site are completely untouched by this feature.
package codegen

import (
	"llvm_lang/src/sema"

	"tinygo.org/x/go-llvm"
)

// argsInitCtorPriority is deliberately lower than globalCtorPriority
// (globalinit.go) - see genCtors: a lower @llvm.global_ctors priority number
// runs earlier (per LLVM's own LangRef), and a non-constant global's own
// initializer might itself call args() (e.g. `var prog string =
// args()[0]`), so the args marshaling must run before llvm_lang.global_init
// does, not after or in whatever order AddFunction happened to build them.
const argsInitCtorPriority = 100

// setupArgsGlobal declares this module's single `llvm_lang.args` global - a
// private, zero-initialized `{ptr, i32, i32}` (dynArrTy) slot every
// genArgsCall reads from, and buildArgsInitFn (below) populates once, at
// startup, for a real AOT-compiled program. Always declared, regardless of
// whether the compiled program ever actually calls args() anywhere - cheap,
// entirely self-contained (no external symbol, unlike buildArgsInitFn's own
// __argc/__argv reads below), the same "always set up, never conditional on
// use" convention setupRuntime's other cached globals already follow.
func (g *Generator) setupArgsGlobal() {
	g.argsGlobal = llvm.AddGlobal(g.mod, g.dynArrTy, "llvm_lang.args")
	g.argsGlobal.SetInitializer(llvm.ConstNull(g.dynArrTy))
	g.argsGlobal.SetLinkage(llvm.PrivateLinkage)
}

// genArgsCall lowers `args()` - see checkArgsCall (sema/typecheck.go) for the
// type-checking half. Just a load of the already-marshaled llvm_lang.args
// global's current value - no per-call marshaling work at all, matching
// LANGUAGE.md's own "constructed once, at program startup" promise.
//
// Sets g.argsUsed, read by genCtors (globalinit.go) once every function body
// in the whole program has been generated: buildArgsInitFn (and the
// __argc/__argv externs it needs) is only actually built for a program that
// calls args() somewhere - see that function's own doc comment for why this
// matters (a real, if narrow, JIT-execution regression risk otherwise).
func (g *Generator) genArgsCall() llvm.Value {
	g.argsUsed = true
	return g.builder.CreateLoad(g.dynArrTy, g.argsGlobal, "")
}

// buildArgsInitFn builds (and returns) a small, parameterless, private
// function - `llvm_lang.args_init` - that marshals the real OS argc/argv
// (read from mingw64's own `__argc`/`__argv` globals, declared here as plain
// extern globals - the exact same "declare, let the linker/process-symbol
// generator resolve it" shape every other extern this package uses already
// has, see runtime.go's setupRuntime) into a freshly arena-allocated
// []string, stored into llvm_lang.args (setupArgsGlobal).
//
// Deliberately only ever called (by genCtors, globalinit.go) when
// g.argsUsed is true - i.e. only for a program that actually calls args()
// somewhere. This is not just an optimization: __argc/__argv are real
// external symbols this package has no control over the resolvability of
// under JIT execution (unlike malloc/printf/memcpy, already proven
// resolvable by this entire project's existing test suite) - keeping them
// (and this function, and its @llvm.global_ctors registration) out of every
// other program's module entirely means a program that never calls args()
// carries zero new external-symbol risk at all, exactly as before this
// round. A program that does call args() gets these declared, and (see
// cmd/llvmc's bindMinGWMainThunk) __argc/__argv are bound to harmless
// process-local memory for the JIT path specifically, so even the
// unresolved-symbol risk this function would otherwise carry is covered.
//
// Each element's length comes from a real libc strlen call - not a
// hand-rolled `while (argv[i][j] != 0) j++` byte-scanning loop - reusing the
// exact same "declare a libc extern, call it directly" convention this
// package already uses everywhere else (malloc/memcpy/memcmp/memset,
// runtime.go) rather than reinventing that logic as generated IR.
func (g *Generator) buildArgsInitFn() llvm.Value {
	argcGlobal := llvm.AddGlobal(g.mod, g.i32Ty, "__argc")
	argvGlobal := llvm.AddGlobal(g.mod, g.ptrTy, "__argv")

	strlenType := llvm.FunctionType(g.i64Ty, []llvm.Type{g.ptrTy}, false)
	strlenFn := llvm.AddFunction(g.mod, "strlen", strlenType)

	fnType := llvm.FunctionType(g.voidTy, nil, false)
	fn := llvm.AddFunction(g.mod, "llvm_lang.args_init", fnType)
	fn.SetLinkage(llvm.PrivateLinkage)

	// The exact same per-function generation state genFuncBody/
	// genGlobalCtors already set up for a synthesized/ordinary function body
	// of their own - see codegen.go's Generator doc comment.
	g.curFn = fn
	g.entryBlock = g.ctx.AddBasicBlock(fn, "entry")
	g.builder.SetInsertPointAtEnd(g.entryBlock)
	g.locals = make(map[*sema.Symbol]llvm.Value)
	g.loopStack = nil
	g.destructors = nil
	g.curReceiver = llvm.Value{}
	g.curFunc = &funcCtx{
		isMain:    false,
		hasReturn: false,
	}
	g.curCtxPtr = llvm.Value{}
	g.curCaptureIndex = nil
	g.curCaptureTy = llvm.Type{}

	argc := g.builder.CreateLoad(g.i32Ty, argcGlobal, "")
	argv := g.builder.CreateLoad(g.ptrTy, argvGlobal, "")

	buf, _, _ := g.genArenaAllocElems(g.stringTy, argc)

	idxAddr := g.createEntryAlloca(g.i32Ty, "args.idx")
	g.builder.CreateStore(llvm.ConstInt(g.i32Ty, 0, false), idxAddr)

	condBB := g.ctx.AddBasicBlock(fn, "args.cond")
	bodyBB := g.ctx.AddBasicBlock(fn, "args.body")
	endBB := g.ctx.AddBasicBlock(fn, "args.end")
	g.builder.CreateBr(condBB)

	g.builder.SetInsertPointAtEnd(condBB)
	idx := g.builder.CreateLoad(g.i32Ty, idxAddr, "")
	g.builder.CreateCondBr(g.builder.CreateICmp(llvm.IntSLT, idx, argc, ""), bodyBB, endBB)

	g.builder.SetInsertPointAtEnd(bodyBB)
	argvSlotAddr := g.builder.CreateInBoundsGEP(g.ptrTy, argv, []llvm.Value{idx}, "")
	cstr := g.builder.CreateLoad(g.ptrTy, argvSlotAddr, "")
	strLen64 := g.builder.CreateCall(strlenType, strlenFn, []llvm.Value{cstr}, "")
	strLen32 := g.builder.CreateTrunc(strLen64, g.i32Ty, "")
	hdr := llvm.Undef(g.stringTy)
	hdr = g.builder.CreateInsertValue(hdr, cstr, 0, "")
	hdr = g.builder.CreateInsertValue(hdr, strLen32, 1, "")
	destAddr := g.builder.CreateInBoundsGEP(g.stringTy, buf, []llvm.Value{idx}, "")
	g.builder.CreateStore(hdr, destAddr)
	nextIdx := g.builder.CreateAdd(idx, llvm.ConstInt(g.i32Ty, 1, false), "")
	g.builder.CreateStore(nextIdx, idxAddr)
	g.builder.CreateBr(condBB)

	g.builder.SetInsertPointAtEnd(endBB)
	result := llvm.Undef(g.dynArrTy)
	result = g.builder.CreateInsertValue(result, buf, 0, "")
	result = g.builder.CreateInsertValue(result, argc, 1, "")
	result = g.builder.CreateInsertValue(result, argc, 2, "")
	g.builder.CreateStore(result, g.argsGlobal)

	g.builder.CreateRetVoid()
	g.curFunc = nil

	return fn
}
