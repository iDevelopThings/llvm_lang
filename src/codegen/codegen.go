// Package codegen lowers a resolved and type-checked *ast.Tree (plus its
// *sema.Info) - or, for a multi-file package, every file's own tree/info
// pair together (see GeneratePackage and LANGUAGE.md's "Multi-file
// packages" section) - into one LLVM Module, via tinygo.org/x/go-llvm.
//
// This package assumes its input is already fully valid: Generate/
// GeneratePackage are meant to be called only on tree(s) for which
// sema.Resolve/sema.ResolvePackage and sema.Check/sema.CheckPackage all
// returned an empty diagnostic Bag (see AGENTS.md - "a whole program is
// available as tree+info with everything already validated; you are
// lowering already-correct, already-typed code to LLVM IR, not re-deriving
// semantics"). Feeding it a tree with unresolved names or type errors is not
// supported and may panic - every lookup here (info.Refs, info.Types,
// info.Structs) assumes the entry it wants is actually present.
//
// A top-level `var`'s initializer is real generated code now, not required
// to be a compile-time constant (see CODEGEN.md's "Global var initializers"
// section and globalinit.go) - one still-possible codegen-only diagnostic is
// a genuine error inside an otherwise constant-*shaped* initializer (division
// by zero, an out-of-range literal), which lands in the affected file's own
// diag.Bag rather than panicking, same as every other pass in this compiler.
package codegen

import (
	"iter"

	"llvm_lang/src/ast"
	"llvm_lang/src/diag"
	"llvm_lang/src/enums"
	"llvm_lang/src/sema"

	"tinygo.org/x/go-llvm"
)

// Module is a generated LLVM module together with the Context that owns it.
// Both must be disposed once the caller is done (verifying, JIT-executing,
// printing) - see Dispose.
type Module struct {
	Ctx  llvm.Context
	LLVM llvm.Module
}

// Dispose releases the module and its owning context. Safe to call once,
// after which m must not be used again.
func (m *Module) Dispose() {
	m.LLVM.Dispose()
	m.Ctx.Dispose()
}

// loopCtx is one nested for-loop's break/continue branch targets - see
// genForStmt and genBreakStmt/genContinueStmt.
type loopCtx struct {
	// breakTarget/continueTarget are used when returnFromCallback is false -
	// every ordinary loop kind (plain for, array/map range-for).
	breakTarget    llvm.BasicBlock
	continueTarget llvm.BasicBlock

	// returnFromCallback is true only for a generator-consuming range-for's
	// own synthesized callback body (see genRangeGeneratorCallbackFunc and
	// CODEGEN.md's "Generator functions" section) - there is no real loop
	// inside the callback at all (the generator itself does the looping, by
	// calling this callback repeatedly), so break/continue can't branch to a
	// real basic block the way every other loop kind's own breakTarget/
	// continueTarget do; they return a bool directly from the callback's own
	// frame instead (see genBreakStmt/genContinueStmt) - false (stop early)
	// for break, true (keep going) for continue. breakTarget/continueTarget
	// are simply unused (zero-valued) when this is true.
	returnFromCallback bool

	// destructorBase is a destructorScope snapshot (stmt.go) of
	// Generator.destructors at the point this loop's own body started
	// generating - see genBreakStmt/genContinueStmt's own
	// unwindDestructorsToScope calls and LANGUAGE.md's "Destructors" section:
	// a break/continue only ever unwinds what was declared since entering
	// the loop, never anything declared in an enclosing scope outside it
	// (that scope isn't being exited at all). A destructorScope, not a plain
	// int, since `move` can remove an entry from anywhere in the stack, not
	// just the top - see destructorScope's own doc comment.
	destructorBase destructorScope
}

// matchExprCodegenCtx is one live expression-mode match's own codegen
// state - pushed onto Generator.matchExprStack for the duration of
// genMatchExpr, mirroring loopCtx's identical per-construct-instance
// stacking one type above (see its own doc comment) - a nested match
// expression (a `yield`'s own expr that is itself another match expression)
// pushes its own frame on top, so a nested yield's own incoming pair is
// recorded against ITS OWN frame, never the enclosing one's.
type matchExprCodegenCtx struct {
	// destructorBase is a destructorScope snapshot (stmt.go) at the point
	// this match expression's own arms started generating - see
	// genYieldStmt's own unwindDestructorsToScope call and LANGUAGE.md's
	// "Destructors" section: a yield only unwinds locals declared since
	// entering the match expression itself, never anything declared in an
	// enclosing scope outside it - exactly loopCtx's own destructorBase, one
	// construct over.
	destructorBase destructorScope

	// mergeBB is this match expression's own single merge block - every
	// yield anywhere in any arm (however deeply nested inside an if/for)
	// branches here (genYieldStmt), and this is where genMatchExpr builds
	// the real CreatePHI collecting every one of their contributed values
	// once every arm has finished generating.
	mergeBB llvm.BasicBlock

	// incomingVals/incomingBlocks accumulate one (value, block) pair per
	// yield actually generated across every arm (genYieldStmt appends to
	// both, in whatever order codegen happens to generate each arm) -
	// batched into one CreatePHI/AddIncoming call at the very end
	// (genMatchExpr), matching every other phi call site already in this
	// package (see enum.go's genEnumEqual/genHashEnumInto, expr.go's
	// short-circuit `&&`, maps.go, runtime.go - none of them call
	// AddIncoming incrementally either; every one builds its full incoming
	// slices first, then calls CreatePHI+AddIncoming exactly once).
	incomingVals   []llvm.Value
	incomingBlocks []llvm.BasicBlock
}

// destructorEntry is one local variable's or parameter's own symbol, plus
// the already-resolved destructor function to call for it (fn/fnTy - see
// destructorFuncFor, func.go, which resolves either a struct's or an enum's
// own destructor entry into this same shape, so this struct itself never
// needs to know which type kind gave it a destructor) - pushed onto
// Generator.destructors (see pushDestructorEntry, func.go) the moment such a
// local/parameter's storage is initialized, and popped (with a real
// destructor call emitted for each entry popped) by unwindDestructorsTo
// (stmt.go) at every point LANGUAGE.md's "Destructors" section identifies as
// a real scope exit: a block falling off its own end, a return, or a
// break/continue exiting an enclosing loop.
type destructorEntry struct {
	sym  *sema.Symbol
	fn   llvm.Value
	fnTy llvm.Type
}

// funcCtx is pushed once per function body being generated - the state
// genReturnStmt and the function's final fallback-terminator logic need
// about the function currently being built. A function literal (FuncLit -
// see LANGUAGE.md's "Lambdas" section) can now nest arbitrarily deep inside
// another function or another literal; Generator still only needs a single
// field for this (not an explicit stack), the same save-in-a-local/restore
// shape sema/typecheck.go's curFunc uses one layer up - see genLambdaFunc,
// which saves every one of Generator's per-function-frame fields (this one
// included) before switching to a nested literal's own fresh state, and
// restores them once that literal's body is fully generated.
type funcCtx struct {
	isMain    bool
	hasReturn bool

	// retType is this function's own declared return type (sema.Type{Kind:
	// TypeVoid} when hasReturn is false) - genReturnStmt reads this to build
	// a multi-value `return a, b, ...`'s own aggregate struct value (see
	// genMultiValueExpr, stmt.go): unlike a single-value return (which just
	// evaluates its one value expression directly, whatever type it already
	// is), a multi-value return has to build the enclosing function's own
	// real LLVM return struct type from scratch, since a MultiValueExpr node
	// itself carries no type of its own to read back out of info.Types the
	// way an ordinary expression node would.
	retType sema.Type
}

// funcEntry is what the module-scope pass over every FuncDecl/method
// records, before any body is generated - see declareFuncSignature. Looking
// a call's callee up in Generator.funcs never has to re-derive a function's
// LLVM type from its AST node, however many call sites reference it.
type funcEntry struct {
	fn       llvm.Value
	fnType   llvm.Type
	retType  sema.Type
	isMethod bool

	// sretReturn is true iff fnType's own LLVM return type is void with a
	// synthesized leading `ptr` parameter standing in for retType's real
	// value (see ffi.go's externReturnType) - only ever set for an
	// ExternFuncDecl whose declared struct return doesn't fit the Windows
	// x64 "as an integer" case. genFuncCall reads this to allocate that
	// hidden return slot and thread it through as the call's real first
	// argument.
	sretReturn bool
}

// structLayout is one struct type's LLVM shape: its (named) LLVM struct
// type, each field's positional GEP index (keyed by the field's *sema.Symbol
// - the same one info.Refs resolves a MemberExpr's field name to), and each
// field's LLVM type in declaration order (needed to build a per-field zero
// value for a keyed composite literal that doesn't mention every field).
// fieldSemaTypes parallels fieldTypes but keeps each field's sema.Type
// (rather than its LLVM type) in the same declaration order - genPrintCall's
// struct case (runtime.go) needs the actual sema.Type to decide how to
// render each field, recursively.
type structLayout struct {
	llvmType       llvm.Type
	fieldIndex     map[*sema.Symbol]int
	fieldTypes     []llvm.Type
	fieldSemaTypes []sema.Type
}

// Generator holds every piece of state needed to lower a whole package (one
// or more *ast.Trees, sharing one *sema.Info per tree - see GeneratePackage)
// into one Module. It's used once per GeneratePackage call, never reused.
//
// tree/info/diags always describe "whichever file's declarations are
// currently being lowered" - genPackage's own pass loops switch these via
// enter as they move from one file to the next. Unlike sema/typecheck.go's
// checker, nothing here ever needs to *switch back* mid-declaration to
// follow a Symbol into another file: every cross-file reference a function
// body can contain (a call to a function declared elsewhere, a struct type
// named elsewhere, a global var declared elsewhere) is looked up through
// funcs/globals/structLayouts below, all keyed by *sema.Symbol or
// *sema.StructInfo pointer identity rather than by ast.NodeIndex - so once
// declareStructType/defineStructBody/genGlobalVarDecl/declareFuncSignature
// have each run once (across every file, before any function body is
// generated - see genPackage), every one of those lookups is a plain,
// tree-agnostic map read, regardless of which file originally declared the
// symbol being referenced.
type Generator struct {
	infos    map[*ast.Tree]*sema.Info
	allDiags map[*ast.Tree]*diag.Bag

	tree *ast.Tree
	info *sema.Info

	ctx     llvm.Context
	mod     llvm.Module
	builder llvm.Builder
	diags   *diag.Bag

	// Primitive LLVM types - computed once in setupTypes. See AGENTS.md's
	// codegen section for why `int` is i32 (not i64) and `string` is the
	// literal struct {ptr, i32} (pointer + length, not null-terminated).
	i8Ty     llvm.Type
	i16Ty    llvm.Type
	i32Ty    llvm.Type
	i64Ty    llvm.Type
	f32Ty    llvm.Type
	f64Ty    llvm.Type
	boolTy   llvm.Type
	ptrTy    llvm.Type
	stringTy llvm.Type
	voidTy   llvm.Type

	// funcValTy is the fat-pointer LLVM representation of a first-class
	// function value: the literal struct {ptr, ptr} = {fnPtr, ctxPtr} - see
	// CODEGEN.md's "First-class functions" section and genFuncValue
	// (expr.go), the one site that actually constructs one.
	funcValTy llvm.Type

	// dynArrTy is a dynamic array's (`[]T`) LLVM representation: the literal
	// struct {ptr, i32, i32} = {dataPtr, len, cap} - see CODEGEN.md's
	// "Dynamic arrays" section and setupTypes for why one shared struct type
	// serves every element type.
	dynArrTy llvm.Type

	// enumValTy is an enum value's (`sema.TypeEnum`) LLVM representation: the
	// literal struct {i32, ptr} = {discriminant, payload} - see setupTypes'
	// own doc comment and CODEGEN.md's "Enums" section.
	enumValTy llvm.Type

	// mapCtrlTy is a map's (`map[K]V`) control-block LLVM representation:
	// the literal struct {ptr, i32, i32, i32} = {bucketsPtr, count,
	// bucketCount, tombstoneCount} - see maps.go's own top-of-file doc
	// comment and CODEGEN.md's "Maps" section. A map's own runtime value is
	// a single `ptr` (like TypePointer - see llvmType) pointing at one of
	// these, arena-allocated once by genMapMake and never moved thereafter -
	// only its buckets/count/bucketCount/tombstoneCount fields change in
	// place as the table grows or entries are removed (genMapGrowIfNeeded,
	// genMapRemoveCall).
	mapCtrlTy llvm.Type

	// fmtMapNilTrap is the cached format-string global for the "assignment
	// to entry in nil map" runtime trap (genMapTrapIfNil, maps.go) - built
	// once, in setupMapTypes, exactly like every other cached trap-message
	// global in this package (see the fmt*Trap fields below).
	fmtMapNilTrap llvm.Value

	structLayouts map[*sema.StructInfo]*structLayout

	// enumLayouts is structLayouts' enum-kind counterpart - see enum.go's own
	// enumLayout doc comment for exactly what it caches per enum (each
	// variant's own payload LLVM struct type, and each payload field's own
	// sema.Type in the same order).
	enumLayouts map[*sema.EnumInfo]*enumLayout

	globals map[*sema.Symbol]llvm.Value

	// globalInits queues every non-constant top-level `var`'s initializer -
	// appended by genGlobalVarDecl, in source declaration order across every
	// file in the package (the same order genPackage's own per-tree loops
	// already visit declarations in) - for buildGlobalInitFn (globalinit.go)
	// to consume, once, after every global/function/constructor signature in the
	// whole package already exists. See CODEGEN.md's "Global var
	// initializers" section for why declaration order (not a full
	// dependency-graph topological sort) is this round's deliberately scoped
	// ordering rule.
	globalInits []globalInitEntry

	// funcs is keyed by *sema.Symbol (the declared free function or
	// method's own symbol, from Info.Refs), not by its FuncDecl's
	// ast.NodeIndex - a NodeIndex is only meaningful relative to the one
	// Tree it came from (see ast.NodeIndex's doc comment), and once a
	// package can span multiple files/trees, two unrelated functions in
	// different files can share the same NodeIndex value. A *sema.Symbol
	// pointer is already globally unique (one fresh Symbol per declaration,
	// see sema/resolve.go), so keying on it sidesteps the collision
	// entirely - the same reasoning globals/structLayouts already apply.
	funcs map[*sema.Symbol]funcEntry

	// ctors is funcs' counterpart for constructors (see LANGUAGE.md's
	// "Constructors" section) - kept as its own map, rather than folded into
	// funcs, since a constructor's Symbol (sema.SymConstructor) is a
	// distinct kind from an ordinary free-function/method's (sema.SymFunc):
	// keying both kinds into one map would work (the two Symbol pointer
	// spaces never collide), but a dedicated map keeps "which of these two
	// completely different declaration shapes does this call resolve to"
	// obvious at every read site, the same reason structLayouts/globals are
	// each their own map rather than one generic "everything" map. Populated
	// by declareConstructorSignature, read by genConstructorCall - see
	// func.go.
	ctors map[*sema.Symbol]funcEntry

	// dtors is ctors' destructor-kind counterpart (see LANGUAGE.md's
	// "Destructors" section) - keyed directly by *sema.StructInfo rather
	// than by a destructor's own *sema.Symbol (sema.SymDestructor): unlike a
	// constructor, a destructor is never selected by an arity lookup or any
	// other call-site resolution - every real caller of this map already
	// holds the StructInfo it wants the destructor for (a local/parameter's
	// own declared type, or delete's pointee type), so there's no reason to
	// go through the Symbol indirection at all. Populated by
	// declareDestructorSignature, read by genDestructorCall - see func.go.
	dtors map[*sema.StructInfo]funcEntry

	// enumDtors is dtors' enum-kind counterpart - keyed by *sema.EnumInfo
	// rather than *sema.StructInfo, identical reasoning. Populated by
	// declareEnumDestructorSignature, read by pushDestructorEntry/
	// destructorFuncForPointee (func.go/stmt.go) - see enum.go.
	enumDtors map[*sema.EnumInfo]funcEntry

	// destructors is the current function's flat, function-scoped stack of
	// still-in-scope locals/parameters whose own declared type has its own
	// destructor - reset at the start of every function/constructor/
	// destructor/synthesized-init/lambda body (see beginSyntheticFunc,
	// func.go), pushed onto by
	// pushDestructorEntry (func.go) the moment such a local/parameter's
	// storage is initialized, and unwound (see unwindDestructorsTo, stmt.go)
	// at every real scope exit this feature fires at. A flat slice rather
	// than a stack of per-block frames: every unwind operation - a block's
	// own fall-through, a return, or a break/continue - already knows
	// exactly which index to unwind back down to (a block's own saved
	// `base`, 0 for a return, or a loopCtx's own destructorBase for a
	// break/continue), so there's no need for an extra nested-frame layer on
	// top of the flat slice itself.
	destructors []destructorEntry

	// locals is reset at the start of every function (see beginSyntheticFunc,
	// func.go) -
	// every VarDecl/ShortVarDecl/Param declaration node produces its own
	// distinct *sema.Symbol (see sema/resolve.go's declareLocal), so a flat
	// map needs no explicit scope-stack to avoid collisions between two
	// same-named variables in different nested blocks of the same function.
	locals map[*sema.Symbol]llvm.Value

	// strLiterals dedupes identical string-literal contents into a single
	// backing global (keyed by the literal's already-unescaped text).
	strLiterals map[string]llvm.Value

	// curFn/entryBlock/curFunc/loopStack/curReceiver are all per-function
	// generation state, reset via beginSyntheticFunc (func.go) at the start
	// of every function/constructor/destructor/synthesized-init body -
	// curFunc (untouched there) and curReceiver (reset only to "no
	// receiver") are each then set by the call site itself, being
	// call-site-specific business logic rather than shared reset state.
	// genLambdaFunc calls beginSyntheticFunc's own returned restore func
	// (plus separately saving/restoring curFunc and the builder's insert
	// block) around a nested FuncLit's own body - see its own doc comment.
	curFn       llvm.Value
	entryBlock  llvm.BasicBlock
	curFunc     *funcCtx
	loopStack   []loopCtx
	curReceiver llvm.Value

	// matchExprStack is loopStack's expression-mode-match counterpart - one
	// live frame per currently-being-generated match expression, innermost
	// last (see matchExprCodegenCtx's own doc comment) - pushed/popped by
	// genMatchExpr (enum.go), read by genYieldStmt (stmt.go). Reset to nil
	// per function alongside loopStack (see beginSyntheticFunc, func.go): a
	// match expression's own frame never needs to survive past the function
	// body it's generated inside of.
	matchExprStack []*matchExprCodegenCtx

	// curCtxPtr/curCaptureIndex/curCaptureTy describe the function currently
	// being generated only when it's itself a lambda's own synthesized
	// function (see genLambdaFunc) - the zero value/nil for an ordinary
	// function, method, or constructor, none of which ever receives a ctxPtr
	// parameter at all (see CODEGEN.md's "Lambdas" section: only a genuine
	// lambda's real underlying function does). curCtxPtr is that function's
	// own real first parameter; curCaptureIndex maps each symbol it captures
	// (info.Captures of the FuncLit genLambdaFunc is generating) to that
	// value's own field index within curCaptureTy, its capture-context
	// struct type (needed as CreateStructGEP's aggregate-type argument) -
	// see addrOfSymbol, the one place both are read.
	curCtxPtr       llvm.Value
	curCaptureIndex map[*sema.Symbol]int
	curCaptureTy    llvm.Type

	// curIsGenerator/curGeneratorCallback/curGeneratorElem describe the
	// function currently being generated only when it's itself a generator
	// function's own real body (see declareFuncSignature/genFuncBody and
	// CODEGEN.md's "Generator functions" section) - zero/false for an
	// ordinary function/method/constructor/lambda, none of which take an
	// implicit trailing callback parameter at all. curGeneratorCallback is
	// that trailing parameter's own fat-pointer value (the same {fnPtr,
	// ctxPtr} representation genFuncValue/genFuncLit already build);
	// curGeneratorElem is the generator's own declared `yield T` element
	// type - both read only by genYieldStmt.
	curIsGenerator       bool
	curGeneratorCallback llvm.Value
	curGeneratorElem     sema.Type

	// curIsAsync/curCoroId/curCoroHandle/curCoroTeardownBB describe the
	// function currently being generated only when it's itself an `async
	// func`'s own real body (see LANGUAGE.md's "Coroutines" section and
	// CODEGEN.md's "Coroutines" section) - zero/nil for every other
	// function kind. curCoroId/curCoroHandle are the token/ptr values
	// llvm.coro.id/llvm.coro.begin produce in the function's own entry
	// block (genCoroPrologue) - safe to keep as plain SSA values rather than
	// re-loading from a slot, since the entry block dominates every later
	// use, including every per-await cleanup block. curCoroTeardownBB is
	// the function's own shared final-teardown block (coroEndBlock),
	// lazily created on first use since a coroutine with zero awaits still
	// needs exactly one.
	curIsAsync        bool
	curCoroId         llvm.Value
	curCoroHandle     llvm.Value
	curCoroTeardownBB llvm.BasicBlock

	// rangeGenCounter synthesizes each generator-consuming range-for's own
	// unique, collision-free callback function name (genRangeGeneratorCallbackFunc) -
	// lambdaCounter's own counterpart, one construct over.
	rangeGenCounter int

	// thunks memoizes, per free-function Symbol, the small uniform-ABI thunk
	// genFuncValue builds the first time that function is ever referenced
	// bare (as a value, not called) - see genFuncThunk's own doc comment for
	// why this exists at all (CODEGEN.md's "Lambdas" section: a direct call
	// bypasses this entirely and stays genuinely zero-overhead).
	thunks map[*sema.Symbol]llvm.Value

	// lambdaCounter synthesizes each FuncLit's own unique, collision-free LLVM
	// function name (genLambdaFunc) - a plain monotonically increasing
	// counter shared across the whole module is simpler than deriving a name
	// from "whichever function lexically encloses this literal" and equally
	// guaranteed collision-free, since every lambda gets its own fresh number
	// regardless of nesting depth or which function contains it.
	lambdaCounter int

	// Runtime externs and cached format-string globals - see runtime.go.
	printfType llvm.Type
	printfFn   llvm.Value
	// noBuiltinAttrKind is the "nobuiltin" enum attribute kind ID (looked up
	// once in setupRuntime, cached here rather than re-resolving the kind ID
	// by name on every single call site) - see callPrintf's own doc comment
	// (runtime.go) for why every printf call this package emits needs it.
	noBuiltinAttrKind uint
	mallocType        llvm.Type
	mallocFn          llvm.Value
	freeType          llvm.Type
	freeFn            llvm.Value
	memcpyType        llvm.Type
	memcpyFn          llvm.Value
	memcmpType        llvm.Type
	memcmpFn          llvm.Value
	memsetType        llvm.Type
	memsetFn          llvm.Value
	// strlenType/strlenFn cache strlenExtern's (runtime.go) own lazily
	// resolved "strlen" declaration - reused as-is by both of its callers
	// (the args() builtin's own argv marshaling, args.go, and the
	// cstring->string conversion, genCStringToString, runtime.go) rather
	// than each adding its own colliding declaration of the same symbol.
	// strlenFn.IsNil() means "not yet resolved for this module".
	strlenType llvm.Type
	strlenFn   llvm.Value
	trapType   llvm.Type
	trapFn     llvm.Value
	fflushType llvm.Type
	fflushFn   llvm.Value
	fmtInt     llvm.Value
	fmtInt64   llvm.Value
	fmtUInt    llvm.Value
	fmtUInt64  llvm.Value
	fmtFloat   llvm.Value
	fmtStr     llvm.Value

	// Runtime trap diagnostic messages (see CODEGEN.md's "Runtime trap
	// diagnostics" section) - printed via printf immediately before every
	// llvm.trap+unreachable site (genBoundsCheck/genSliceRangeCheck, expr.go;
	// genMakeSizeCheck, runtime.go), each with the actual runtime values
	// already in hand at that point substituted in, exactly like Go's own
	// runtime panic messages: informative output, then a genuine hard abort -
	// never a softer recovery mechanism.
	fmtBoundsTrap     llvm.Value
	fmtSliceRangeTrap llvm.Value
	fmtMakeSizeTrap   llvm.Value

	// The bump-allocator arena (see setupArena in runtime.go): a generated
	// LLVM function every heap-needing string operation calls into instead of
	// malloc directly, plus the mutable globals backing its state (the
	// current block's bump cursor, remaining byte count, and the tracked
	// baseline size the *next* normal growth chunk should use - see
	// arenaChunkSize/arenaChunkMaxSize's own doc comments in runtime.go for
	// the geometric-growth design this last global drives). See AGENTS.md's
	// codegen section for the exact design.
	arenaAllocType       llvm.Type
	arenaAllocFn         llvm.Value
	arenaCursorGlobal    llvm.Value
	arenaRemainingGlobal llvm.Value
	arenaNextChunkGlobal llvm.Value

	// Struct/array printing (see genPrintStructValue/genPrintArrayValue in
	// runtime.go) needs a handful of additional cached format-string
	// globals: "bare" (no trailing newline) int/string specifiers for a
	// nested field/element, plus the literal punctuation gluing them
	// together. See AGENTS.md's codegen section for the exact format.
	fmtIntBare    llvm.Value
	fmtInt64Bare  llvm.Value
	fmtUIntBare   llvm.Value
	fmtUInt64Bare llvm.Value
	fmtFloatBare  llvm.Value
	fmtStrBare    llvm.Value
	fmtSpace      llvm.Value
	fmtLBrace     llvm.Value
	fmtRBrace     llvm.Value
	fmtLBracket   llvm.Value
	fmtRBracket   llvm.Value
	fmtNewline    llvm.Value

	// fmtPtr/fmtPtrBare are the newline/bare format-string pair for a
	// TypePointer value (see genPrintCall/genPrintValueBare, runtime.go) -
	// same "%p"-based pattern as every other fmtInt*/fmtFloat*/fmtStr* pair
	// above, just for a raw pointer value: standard C printf's own
	// pointer-address specifier, matching this project's existing
	// libc-printf-based printing convention exactly, no new runtime
	// primitive needed.
	fmtPtr     llvm.Value
	fmtPtrBare llvm.Value

	// fmtLParen/fmtRParen are a tuple-variant's own print-time punctuation
	// (see genPrintEnumVariant, enum.go) - the enum-specific counterpart to
	// fmtLBrace/fmtRBrace above (a struct variant reuses those two directly).
	fmtLParen llvm.Value
	fmtRParen llvm.Value

	// argsGlobal is the private llvm_lang.args global genArgsCall reads from
	// and buildArgsInitFn (args.go) populates once, at startup - see that
	// file's own doc comment for the full args() builtin design. argsUsed is
	// set the first time genArgsCall actually runs anywhere in the whole
	// program; genCtors (globalinit.go) only builds buildArgsInitFn (and its
	// own __argc/__argv extern globals) when this ends up true, deliberately
	// keeping every other program's module free of that extra external-symbol
	// surface.
	argsGlobal llvm.Value
	argsUsed   bool

	// Coroutine intrinsics (see CODEGEN.md's "Coroutines" section) - declared
	// once in setupCoroutines via the generic intrinsic-declaration mechanism
	// (LookupIntrinsicID/IntrinsicType/IntrinsicDeclaration - none of these
	// have a dedicated llvm-c header). coroSize is always the i64 overload,
	// matching mallocFn's own i64 size parameter with no cast needed.
	coroIdFn        llvm.Value
	coroIdType      llvm.Type
	coroSizeFn      llvm.Value
	coroSizeType    llvm.Type
	coroBeginFn     llvm.Value
	coroBeginType   llvm.Type
	coroFreeFn      llvm.Value
	coroFreeType    llvm.Type
	coroEndFn       llvm.Value
	coroEndType     llvm.Type
	coroSuspendFn   llvm.Value
	coroSuspendType llvm.Type
	coroSaveFn      llvm.Value
	coroSaveType    llvm.Type
	coroResumeFn    llvm.Value
	coroResumeType  llvm.Type
	coroDestroyFn   llvm.Value
	coroDestroyType llvm.Type
	coroDoneFn      llvm.Value
	coroDoneType    llvm.Type

	// presplitCoroutineAttrKind is the "presplitcoroutine" enum attribute
	// kind ID (see CODEGEN.md's "Coroutines" section) - every async
	// function's own LLVM function must carry this, or LLVM's coroutine-
	// splitting passes silently never look at it.
	presplitCoroutineAttrKind uint

	// coroDestroyLocalFn/-Type is a small synthesized wrapper this package
	// builds once - void(ptr addr): loads the handle stored at addr, then
	// calls coroDestroyFn on it. destructorFuncFor's TypeCoroutine case
	// returns this, so a coroutine-handle local's own automatic scope-exit
	// cleanup reuses pushDestructorEntry/unwindDestructorsTo wholesale (both
	// already forward a local's own storage ADDRESS, never its loaded
	// value - exactly what a struct/enum destructor's pointer receiver
	// already expects, but llvm.coro.destroy needs the handle BY VALUE, thus
	// this adapter) - see CODEGEN.md's "Coroutines" section.
	coroDestroyLocalFn   llvm.Value
	coroDestroyLocalType llvm.Type

	// coroTokenTy is llvm.coro.end's own "no token" operand type - built
	// once in setupCoroutines rather than re-querying ctx.TokenType() at
	// every coroEndBlock call site.
	coroTokenTy llvm.Type
}

// Generate lowers tree (with its resolved/checked info) into a fresh LLVM
// Module named moduleName. See the package doc comment for the validity
// assumption this makes about tree/info, and BLOCKERS.md for the codegen-
// level restrictions (constant global initializers, unsupported print
// argument types) that can still produce diagnostics here.
//
// Generate is the single-file case of GeneratePackage - see its doc comment
// for the multi-file package entry point this wraps.
//
// The returned Module owns its own Context; the caller must call
// Module.Dispose once done with it (after verifying/JIT-executing/printing).
func Generate(tree *ast.Tree, info *sema.Info, moduleName string) (*Module, *diag.Bag) {
	mod, diags := GeneratePackage([]*ast.Tree{tree}, map[*ast.Tree]*sema.Info{tree: info}, moduleName)
	return mod, diags[tree]
}

// GeneratePackage lowers every file in trees (each with its own already-
// resolved/checked *sema.Info, sharing one package-wide struct catalog and
// symbol identity - see sema.CheckPackage) into one shared LLVM Module named
// moduleName: a function or global declared in one file is directly
// callable/referenceable from another file's code in the same module, using
// the same *sema.Symbol/*sema.StructInfo pointer identity codegen already
// keys every lookup by (see the Generator doc comment) - not one Module per
// file with cross-module linking, which this package has no need for since
// every file in a package always ends up in the exact same module anyway.
//
// The returned Module owns its own Context; the caller must call
// Module.Dispose once done with it (after verifying/JIT-executing/printing).
// Diagnostics come back one *diag.Bag per file (a diagnostic's Pos is only
// meaningful relative to the one file it's reported against - see
// sema.CheckPackage's own doc comment for the identical reasoning), keyed by
// tree, same as sema.ResolvePackage/sema.CheckPackage.
func GeneratePackage(trees []*ast.Tree, infos map[*ast.Tree]*sema.Info, moduleName string) (*Module, map[*ast.Tree]*diag.Bag) {
	ctx := llvm.NewContext()
	mod := ctx.NewModule(moduleName)
	builder := ctx.NewBuilder()

	g := &Generator{
		infos:         infos,
		allDiags:      make(map[*ast.Tree]*diag.Bag, len(trees)),
		ctx:           ctx,
		mod:           mod,
		builder:       builder,
		structLayouts: make(map[*sema.StructInfo]*structLayout),
		enumLayouts:   make(map[*sema.EnumInfo]*enumLayout),
		globals:       make(map[*sema.Symbol]llvm.Value),
		funcs:         make(map[*sema.Symbol]funcEntry),
		ctors:         make(map[*sema.Symbol]funcEntry),
		dtors:         make(map[*sema.StructInfo]funcEntry),
		enumDtors:     make(map[*sema.EnumInfo]funcEntry),
		strLiterals:   make(map[string]llvm.Value),
		thunks:        make(map[*sema.Symbol]llvm.Value),
	}
	for _, tree := range trees {
		g.allDiags[tree] = diag.NewBag()
	}
	g.setupTypes()
	g.setupMapTypes()
	g.setupRuntime()
	if programUsesCoroutines(trees, infos) {
		// Lazy, like g.argsGlobal/argsUsed just below - not merely for
		// cleanliness here: coroDestroyLocalFn's own body calls
		// llvm.coro.destroy, which only ever gets lowered by the
		// optimization pipeline's coroutine-cleanup passes when the module
		// actually contains a presplitcoroutine function (see CODEGEN.md's
		// "Coroutines" section) - declaring it unconditionally would leave
		// an unlowerable intrinsic call in every program that never uses
		// coroutines at all, a real, confirmed instruction-selection crash.
		g.setupCoroutines()
	}
	g.setupArgsGlobal()
	g.genPackage(trees)

	builder.Dispose()

	diags := make(map[*ast.Tree]*diag.Bag, len(trees))
	for _, tree := range trees {
		diags[tree] = g.allDiags[tree]
	}
	return &Module{
		Ctx:  ctx,
		LLVM: mod,
	}, diags
}

// enter switches the Generator's current-file bookkeeping to tree - see the
// Generator doc comment for why nothing here ever needs to switch back
// mid-declaration the way sema/typecheck.go's checker does.
func (g *Generator) enter(tree *ast.Tree) {
	g.tree = tree
	g.info = g.infos[tree]
	g.diags = g.allDiags[tree]
}

// genPackage drives the whole module in passes across every file in trees,
// mirroring sema's own resolvePackage/checkPackage shape (struct catalogs,
// then globals, then function/constructor signatures, then function/
// constructor bodies - each pass covering every file before the next begins)
// so declaration order never matters, either within one file or across the
// whole package: a function can call another declared later (in the same
// file or a different one), a global's type can name a struct declared
// later (ditto), and a constructor call can reach a constructor declared
// later, or on a struct in a different file/package entirely (see
// LANGUAGE.md's "Constructors" section) - every constructor's signature is
// declared across the whole program before any function/constructor body is
// generated, exactly like an ordinary function's.
func (g *Generator) genPackage(trees []*ast.Tree) {
	for _, tree := range trees {
		g.enter(tree)
		for d := range g.declsOfKind(enums.NodeKinds.StructDecl) {
			g.declareStructType(d)
		}
	}
	for _, tree := range trees {
		g.enter(tree)
		for d := range g.declsOfKind(enums.NodeKinds.StructDecl) {
			g.defineStructBody(d)
		}
	}
	// Every enum's own layout (its shared {i32, ptr} outer value plus each
	// variant's own payload struct type) is built in one single pass, right
	// after every struct body is fully defined - a variant's own associated
	// data may itself embed a struct by value (needing that struct's own
	// already-complete llvmType), but never needs another enum's own layout
	// at all (a recursive/self-referential or enum-in-enum variant only ever
	// holds a *pointer*, g.ptrTy, never the pointee's own full layout - see
	// enum.go's declareEnumLayouts) - so, unlike structs, there's no
	// forward-reference problem here needing its own separate
	// declare-then-define split.
	for _, tree := range trees {
		g.enter(tree)
		for d := range g.declsOfKind(enums.NodeKinds.EnumDecl) {
			g.declareEnumLayout(d)
		}
	}
	for _, tree := range trees {
		g.enter(tree)
		for d := range g.declsOfKind(enums.NodeKinds.VarDecl) {
			g.genGlobalVarDecl(d)
		}
	}
	for _, tree := range trees {
		g.enter(tree)
		for d := range g.declsOfKind(enums.NodeKinds.FuncDecl) {
			g.declareFuncSignature(d)
		}
		// ExternFuncDecl gets a signature declared here, exactly like an
		// ordinary FuncDecl - but, deliberately, no corresponding entry in
		// the "generate every body" pass below: it has no body at all (see
		// declareExternFuncSignature's own doc comment).
		for d := range g.declsOfKind(enums.NodeKinds.ExternFuncDecl) {
			g.declareExternFuncSignature(d)
		}
		for d := range g.declsOfKind(enums.NodeKinds.StructDecl) {
			for ctor := range tree.StructConstructors(d) {
				g.declareConstructorSignature(ctor)
			}
			for dtor := range tree.StructDestructors(d) {
				g.declareDestructorSignature(dtor)
			}
		}
		for d := range g.declsOfKind(enums.NodeKinds.EnumDecl) {
			for dtor := range tree.EnumDestructors(d) {
				g.declareEnumDestructorSignature(dtor)
			}
		}
	}
	for _, tree := range trees {
		g.enter(tree)
		for d := range g.declsOfKind(enums.NodeKinds.FuncDecl) {
			g.genFuncBody(d)
		}
		for d := range g.declsOfKind(enums.NodeKinds.StructDecl) {
			for ctor := range tree.StructConstructors(d) {
				g.genConstructorBody(ctor)
			}
			for dtor := range tree.StructDestructors(d) {
				g.genDestructorBody(dtor)
			}
		}
		for d := range g.declsOfKind(enums.NodeKinds.EnumDecl) {
			for dtor := range tree.EnumDestructors(d) {
				g.genEnumDestructorBody(dtor)
			}
		}
	}
	// genCtors (globalinit.go) builds and registers every @llvm.global_ctors
	// entry this package itself synthesizes - llvm_lang.global_init (a
	// non-constant global's real initializer, if the package has any) and
	// llvm_lang.args_init (args()'s own startup marshaling, if the program
	// actually calls args() anywhere - see args.go). Deliberately run *after*
	// every function/constructor/destructor body above, unlike this pass's
	// own signature-declaration passes further up (which all still run
	// before any body, since a body can call any of them) - genCtors needs to
	// know whether g.argsUsed ended up true, which genArgsCall only sets while
	// generating some function's body, so it can only be decided once every
	// body has already been generated. Neither synthesized ctor function's
	// own body generation actually depends on any *other* function's body
	// existing first - only on every global/function/constructor signature
	// already existing (true well before this point either way) - so moving
	// this to the end changes nothing about correctness, only about when
	// g.argsUsed's final value becomes known.
	g.genCtors()
}

// declsOfKind yields every declaration of kind the current file contributes to
// the module: its own parsed top-level declarations, minus any generic
// template (which has no concrete types to lower at all), plus the
// monomorphized specializations sema synthesized into the same tree
// (Info.Specializations - see LANGUAGE.md's "Generics" section). A
// specialization is an ordinary FuncDecl/StructDecl in every respect, but
// isn't a child of Tree.Root, so TopLevelDeclsOfKind alone would both skip
// every specialization and wrongly include every template - which is exactly
// why every pass in genPackage goes through this instead.
func (g *Generator) declsOfKind(kind enums.NodeKind) iter.Seq[ast.NodeIndex] {
	return func(yield func(ast.NodeIndex) bool) {
		for d := range g.tree.TopLevelDeclsOfKind(kind) {
			if g.info.IsGenericTemplate(g.tree, d) {
				continue
			}
			if !yield(d) {
				return
			}
		}
		for _, d := range g.info.Specializations {
			if g.tree.Nodes[d].Kind != kind {
				continue
			}
			if !yield(d) {
				return
			}
		}
	}
}

// errorAt records a codegen-level diagnostic at n's position - see the
// package doc comment for what still gets a real diagnostic instead of a
// panic (non-constant global initializers, unsupported print argument
// types) versus what's assumed impossible on a validated tree.
func (g *Generator) errorAt(n ast.NodeIndex, format string, a ...any) {
	span := g.tree.SpanOf(n)
	g.diags.ErrorfSpan(span.Start, span.End, format, a...)
}

// declareStructType creates decl's named (but still empty/opaque) LLVM
// struct type - split from defineStructBody into its own pass so two
// mutually-referencing structs (A has a field of type B, B has a field of
// type A's... well, arrays/values of each other) can each find the other
// struct's type handle already created, regardless of declaration order.
func (g *Generator) declareStructType(decl ast.NodeIndex) {
	info := g.structInfoOf(decl)
	g.structLayouts[info] = &structLayout{
		llvmType:   g.ctx.StructCreateNamed(info.Symbol.Name),
		fieldIndex: make(map[*sema.Symbol]int),
	}
}

// structInfoOf returns decl's (a StructDecl's) catalog, via the Symbol sema
// already recorded for its name node - never a lookup keyed by that node's
// source text, which no longer identifies a struct uniquely: every
// instantiation of a generic struct is a clone of the same declaration, so
// they all share the same name text (see sema's generics.go).
func (g *Generator) structInfoOf(decl ast.NodeIndex) *sema.StructInfo {
	return g.info.Refs[g.tree.StructName(decl)].StructInfo
}

// defineStructBody fills in decl's struct type body, once every struct's
// named (opaque) type already exists - see declareStructType.
func (g *Generator) defineStructBody(decl ast.NodeIndex) {
	layout := g.structLayouts[g.structInfoOf(decl)]

	fieldNodes := g.tree.StructFields(decl)
	fieldTypes := make([]llvm.Type, len(fieldNodes))
	fieldSemaTypes := make([]sema.Type, len(fieldNodes))
	i := 0
	for fieldNameNode, fieldTypeNode := range g.tree.StructFieldNodes(decl) {
		sym := g.info.Refs[fieldNameNode]

		fieldSemaTypes[i] = g.info.Types[fieldTypeNode]
		fieldTypes[i] = g.llvmType(fieldSemaTypes[i])
		layout.fieldIndex[sym] = i
		i++
	}
	layout.fieldTypes = fieldTypes
	layout.fieldSemaTypes = fieldSemaTypes
	layout.llvmType.StructSetBody(fieldTypes, false)
}

// structLitFieldSlot resolves one struct composite-literal element - elems[i]
// in declaration order, e itself, keyed reporting whether the whole literal
// is keyed (see ast.Tree.IsKeyedElement) - to the field index it fills and
// the value expression node that supplies it: a positional element fills
// field i directly from e itself; a keyed element's field comes from its own
// key (already resolved by sema - see Info.Refs), with i unused, and its
// value from the KeyValueExpr's own value child. Shared by constfold.go's
// constCompositeLit and expr.go's genCompositeLitInto - the one difference
// left between those two callers (building a []llvm.Value vs storing into an
// already-addressed destination) is all that remains once this element-to-
// field mapping is factored out into one place.
func (g *Generator) structLitFieldSlot(layout *structLayout, e ast.NodeIndex, i int, keyed bool) (fieldIndex int, valueNode ast.NodeIndex) {
	if !keyed {
		return i, e
	}
	sym := g.info.Refs[g.tree.Child(e, 0)]
	return layout.fieldIndex[sym], g.tree.Child(e, 1)
}

// globalInitEntry is one non-constant top-level `var`'s initializer, queued
// by genGlobalVarDecl (Generator.globalInits) for buildGlobalInitFn
// (globalinit.go) to lower later, once every global/function/constructor
// signature in the whole package already exists. tree is recorded alongside
// glob/initNode (rather than assuming whatever tree is currently entered)
// since globalInits accumulates across every file in the package before
// buildGlobalInitFn ever consumes it - initNode is only ever meaningful
// relative to the one Tree it came from (see ast.NodeIndex's doc comment), so
// buildGlobalInitFn must re-enter the right tree before generating each entry.
type globalInitEntry struct {
	tree     *ast.Tree
	glob     llvm.Value
	initNode ast.NodeIndex
}

// genGlobalVarDecl lowers one top-level `var` into a real LLVM global, always
// given a zero-value initializer up front (matching Go's own zero-value
// convention for a global whose real initializer hasn't run yet - see
// CODEGEN.md's "Global var initializers" section). An initializer that's
// foldable at compile time (isConstFoldable, constfold.go) is folded
// immediately, overwriting that zero initializer with the real constant - the
// exact same behavior this function has always had, unchanged. Anything else
// keeps the zero initializer and is instead queued into g.globalInits, to be
// lowered as real generated code inside the synthesized init function
// buildGlobalInitFn builds once every global in the package has been declared
// (globalinit.go) - see LANGUAGE.md's "Global var initializers" section for
// what's now legal there (a function call, a reference to another global, a
// dynamic-array/slice literal, ...) and the declaration-order guarantee this
// queuing relies on.
func (g *Generator) genGlobalVarDecl(decl ast.NodeIndex) {
	nameNode := g.tree.Child(decl, 0)
	initNode := g.tree.Child(decl, 2)
	sym := g.info.Refs[nameNode]

	llt := g.llvmType(g.info.Types[decl])
	glob := llvm.AddGlobal(g.mod, llt, sym.Name)
	glob.SetInitializer(llvm.ConstNull(llt))
	g.globals[sym] = glob

	if initNode == ast.InvalidNode {
		return
	}
	if g.isConstFoldable(initNode) {
		// isConstFoldable already guarantees constExpr won't hit its "not a
		// constant at all" default cases - only a genuinely-erroneous
		// constant expression (division by zero, an out-of-range literal)
		// can still fail here, in which case the zero initializer already in
		// place is exactly the same recovery this package has always used.
		if v, ok := g.constExpr(initNode); ok {
			glob.SetInitializer(v)
		}
		return
	}

	g.globalInits = append(g.globalInits, globalInitEntry{
		tree:     g.tree,
		glob:     glob,
		initNode: initNode,
	})
}
