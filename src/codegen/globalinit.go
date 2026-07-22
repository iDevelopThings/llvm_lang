// This file implements the other half of "Global var initializers" (see
// CODEGEN.md's own section by that name): every non-constant top-level
// `var`'s initializer (queued by genGlobalVarDecl into Generator.globalInits,
// codegen.go) is lowered here as real generated code, inside one synthesized
// function this package builds itself, registered into LLVM's own
// `@llvm.global_ctors` mechanism so it runs automatically before `main` in
// any normal linked/loaded program - matching Go's real behavior (an
// arbitrary package-level `var` initializer, not just a compile-time
// constant one), without this language needing its own `func init() {}`
// syntax to spell that out by hand.
package codegen

import (
	"llvm_lang/src/sema"

	"tinygo.org/x/go-llvm"
)

// globalCtorPriority is the priority entry this package registers its own
// synthesized init function under in @llvm.global_ctors - 65535 is the
// conventional "no particular priority" default every other producer of this
// array (clang's own `__attribute__((constructor))` with no explicit
// priority, for one) already uses. There's only ever one entry this package
// itself ever registers, so the actual number never matters for ordering
// against anything else - it's simply the least surprising value to pick.
const globalCtorPriority = 65535

// genGlobalCtors builds one synthesized, parameterless, internal-linkage
// function - every non-constant global's real initializer expression
// evaluated and stored, in g.globalInits' own order (source declaration
// order across the whole package - see CODEGEN.md's "Global var
// initializers" section for why this round deliberately scopes ordering this
// way rather than a full dependency-graph topological sort) - and registers
// it into `@llvm.global_ctors`. A no-op (no function built, no array
// declared at all) whenever every global in the package turned out to be
// compile-time constant, so an ordinary program with no non-constant globals
// gets no trace of this mechanism in its IR.
//
// Must run after every global's own LLVM value (genGlobalVarDecl) and every
// function/constructor signature (declareFuncSignature/
// declareConstructorSignature) already exist - a non-constant initializer's
// expression can reference either - but before any function/constructor body
// is generated (see genPackage's own ordering).
func (g *Generator) genGlobalCtors() {
	if len(g.globalInits) == 0 {
		return
	}

	fnType := llvm.FunctionType(g.voidTy, nil, false)
	fn := llvm.AddFunction(g.mod, "llvm_lang.global_init", fnType)
	fn.SetLinkage(llvm.PrivateLinkage)

	// This is the exact same per-function generation state genFuncBody/
	// genConstructorBody/genLambdaFunc each set up before lowering a body of
	// their own (see codegen.go's Generator doc comment) - a synthesized init
	// function is, as far as genExpr/genStmt/storeValueInto are concerned,
	// just one more ordinary function body to generate: an entry block, a
	// fresh (empty) locals map, no enclosing loop/receiver/lambda-capture
	// context, and a funcCtx that's neither main nor declares a return type
	// (matching a constructor's own "always void, never main" shape).
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

	for _, entry := range g.globalInits {
		g.enter(entry.tree)
		g.storeValueInto(entry.glob, entry.initNode)
	}

	g.builder.CreateRetVoid()
	g.curFunc = nil

	g.registerGlobalCtor(fn)
}

// registerGlobalCtor declares (or, in principle, appends to - though this
// package itself only ever calls this once per module) the special
// `@llvm.global_ctors` global: an *appending*-linkage array of
// `{ i32, ptr, ptr }` entries - `{ priority, ctor function pointer,
// associated data }` - that any real linked/loaded program's C runtime
// startup sequence scans and calls, in priority order, before ever reaching
// `main`. This is the standard, well-documented LLVM mechanism for exactly
// this purpose - see
// https://llvm.org/docs/LangRef.html#the-llvm-global-ctors-global-variable.
//
// The `associated data` field is always null here - this package's own
// single ctor entry has no COMDAT key to associate with (that field only
// matters for a linker dropping an unreferenced COMDAT section along with
// its constructor, which this project's single-module, single-translation-
// unit build never needs).
func (g *Generator) registerGlobalCtor(fn llvm.Value) {
	entryTy := g.ctx.StructType([]llvm.Type{g.i32Ty, g.ptrTy, g.ptrTy}, false)
	entry := g.ctx.ConstStruct([]llvm.Value{
		llvm.ConstInt(g.i32Ty, globalCtorPriority, false),
		fn,
		llvm.ConstNull(g.ptrTy),
	}, false)

	ctors := llvm.AddGlobal(g.mod, llvm.ArrayType(entryTy, 1), "llvm.global_ctors")
	ctors.SetInitializer(llvm.ConstArray(entryTy, []llvm.Value{entry}))
	ctors.SetLinkage(llvm.AppendingLinkage)
}
