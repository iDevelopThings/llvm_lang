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

// globalCtorPriority is the priority entry this package registers
// llvm_lang.global_init under in @llvm.global_ctors - 65535 is the
// conventional "no particular priority" default every other producer of this
// array (clang's own `__attribute__((constructor))` with no explicit
// priority, for one) already uses. Deliberately higher (runs later) than
// argsInitCtorPriority (args.go) - see that constant's own doc comment for
// why args() marshaling must run first.
const globalCtorPriority = 65535

// genCtors is genPackage's own final pass (see its doc comment for why this
// now runs *after* every function/constructor/destructor body, not before):
// it builds every synthesized ctor function this package registers into
// `@llvm.global_ctors` - llvm_lang.global_init (buildGlobalInitFn, if the
// package has any non-constant global) and llvm_lang.args_init
// (buildArgsInitFn, args.go, if the program actually calls args() anywhere -
// g.argsUsed, only known for certain once every body has been generated) -
// and registers whichever of the two actually got built via
// registerGlobalCtors. A program using neither feature gets no
// `@llvm.global_ctors` array at all, exactly as before either feature
// existed.
func (g *Generator) genCtors() {
	var entries []ctorEntry
	if fn, ok := g.buildGlobalInitFn(); ok {
		entries = append(entries, ctorEntry{
			priority: globalCtorPriority,
			fn:       fn,
		})
	}
	if g.argsUsed {
		// Lower priority than global_init's - see argsInitCtorPriority's own
		// doc comment (args.go): a non-constant global's own initializer may
		// itself call args(), so the marshaling must run first.
		entries = append(entries, ctorEntry{
			priority: argsInitCtorPriority,
			fn:       g.buildArgsInitFn(),
		})
	}
	g.registerGlobalCtors(entries)
}

// buildGlobalInitFn builds one synthesized, parameterless function
// (`llvm_lang.global_init`) - every non-constant global's real initializer
// expression evaluated and stored, in g.globalInits' own order (source
// declaration order across the whole package - see CODEGEN.md's "Global var
// initializers" section for why this round deliberately scopes ordering this
// way rather than a full dependency-graph topological sort). Reports false
// (and builds nothing) whenever every global in the package turned out to be
// compile-time constant, so an ordinary program with no non-constant globals
// gets no trace of this mechanism in its IR.
//
// This function keeps AddFunction's own default linkage (external), rather
// than the private linkage every other synthesized helper in this package
// uses (see expr.go/runtime.go) - deliberately: `cmd/llvmc`'s JIT driver
// looks it up by this exact name and calls it directly (see CODEGEN.md's
// "Global var initializers" section), which a private symbol has no name
// for at all.
//
// Must run after every global's own LLVM value (genGlobalVarDecl) and every
// function/constructor signature (declareFuncSignature/
// declareConstructorSignature) already exist - a non-constant initializer's
// expression can reference either - see genPackage's own ordering. Callable
// (like buildArgsInitFn) either before or after every function/constructor/
// destructor body is generated - its own body only ever reads globals/calls
// functions, never another synthesized ctor or a source-level function's own
// body's contents.
func (g *Generator) buildGlobalInitFn() (llvm.Value, bool) {
	if len(g.globalInits) == 0 {
		return llvm.Value{}, false
	}

	fnType := llvm.FunctionType(g.voidTy, nil, false)
	fn := llvm.AddFunction(g.mod, "llvm_lang.global_init", fnType)

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

	return fn, true
}

// ctorEntry is one function registerGlobalCtors adds to `@llvm.global_ctors`
// - see that function's own doc comment for the array's real shape.
type ctorEntry struct {
	priority uint64
	fn       llvm.Value
}

// registerGlobalCtors declares the special `@llvm.global_ctors` global: an
// *appending*-linkage array of `{ i32, ptr, ptr }` entries - `{ priority,
// ctor function pointer, associated data }`, one per entries - that any real
// linked/loaded program's C runtime startup sequence scans and calls, in
// priority order (ascending - a lower number runs earlier), before ever
// reaching `main`. This is the standard, well-documented LLVM mechanism for
// exactly this purpose - see
// https://llvm.org/docs/LangRef.html#the-llvm-global-ctors-global-variable.
// A no-op (no array declared at all) when entries is empty, so a program
// using neither feature that populates it (global_init/args_init) leaves no
// trace of this mechanism in its IR, exactly as before either existed.
//
// The `associated data` field is always null here - none of this package's
// own ctor entries have a COMDAT key to associate with (that field only
// matters for a linker dropping an unreferenced COMDAT section along with
// its constructor, which this project's single-module, single-translation-
// unit build never needs).
func (g *Generator) registerGlobalCtors(entries []ctorEntry) {
	if len(entries) == 0 {
		return
	}

	entryTy := g.ctx.StructType([]llvm.Type{g.i32Ty, g.ptrTy, g.ptrTy}, false)
	values := make([]llvm.Value, len(entries))
	for i, e := range entries {
		values[i] = g.ctx.ConstStruct([]llvm.Value{
			llvm.ConstInt(g.i32Ty, e.priority, false),
			e.fn,
			llvm.ConstNull(g.ptrTy),
		}, false)
	}

	ctors := llvm.AddGlobal(g.mod, llvm.ArrayType(entryTy, len(values)), "llvm.global_ctors")
	ctors.SetInitializer(llvm.ConstArray(entryTy, values))
	ctors.SetLinkage(llvm.AppendingLinkage)
}
