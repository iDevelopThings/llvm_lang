package codegen

import (
	"fmt"

	"llvm_lang/src/ast"
	"llvm_lang/src/enums"
	"llvm_lang/src/sema"

	"tinygo.org/x/go-llvm"
)

// setupRuntime declares every libc extern this package's lowering leans on -
// there's no runtime of our own yet (see AGENTS.md's codegen section): print
// lowers to a call into libc's printf, string concatenation goes through the
// bump-allocator arena (setupArena, which itself grows via malloc), and
// string equality/ordering go through memcmp. Heap memory handed out by the
// arena is still never freed - there's no GC and the arena itself has no
// free-single-allocation operation, so a program that concatenates strings in
// a loop still leaks; a documented, accepted limitation for that chunk of
// work (see BLOCKERS.md), not an oversight. `new`/`delete` (see LANGUAGE.md's
// "Pointers" section) are a real, separate exception to that: each is its own
// direct malloc/free pair, a genuinely freeable allocation, entirely outside
// the arena.
func (g *Generator) setupRuntime() {
	g.printfType = llvm.FunctionType(g.i32Ty, []llvm.Type{g.ptrTy}, true)
	g.printfFn = llvm.AddFunction(g.mod, "printf", g.printfType)

	g.mallocType = llvm.FunctionType(g.ptrTy, []llvm.Type{g.i64Ty}, false)
	g.mallocFn = llvm.AddFunction(g.mod, "malloc", g.mallocType)

	// free - `delete` (see LANGUAGE.md's "Pointers" section) calls this
	// directly, exactly the same libc-extern-call shape malloc already uses
	// above. Deliberately a real, separate heap from the bump-allocator
	// arena setupArena builds just below: `new`/`delete` manage their own
	// memory one real malloc'd block at a time (each freed individually),
	// never touching the arena's own never-freed bump cursor, and vice
	// versa - mixing the two would mean `delete`ing a sub-block out of a
	// shared arena chunk another allocation still lives in.
	g.freeType = llvm.FunctionType(g.voidTy, []llvm.Type{g.ptrTy}, false)
	g.freeFn = llvm.AddFunction(g.mod, "free", g.freeType)

	g.memcpyType = llvm.FunctionType(g.ptrTy, []llvm.Type{g.ptrTy, g.ptrTy, g.i64Ty}, false)
	g.memcpyFn = llvm.AddFunction(g.mod, "memcpy", g.memcpyType)

	g.memcmpType = llvm.FunctionType(g.i32Ty, []llvm.Type{g.ptrTy, g.ptrTy, g.i64Ty}, false)
	g.memcmpFn = llvm.AddFunction(g.mod, "memcmp", g.memcmpType)

	// memset - used by genMakeCall (expr.go) to zero-fill a freshly
	// arena-allocated dynamic-array backing buffer (see LANGUAGE.md's
	// "Dynamic arrays" section: make always zero-fills its entire allocated
	// buffer, the simplest, safest choice - it avoids ever reading
	// uninitialized arena memory).
	g.memsetType = llvm.FunctionType(g.ptrTy, []llvm.Type{g.ptrTy, g.i32Ty, g.i64Ty}, false)
	g.memsetFn = llvm.AddFunction(g.mod, "memset", g.memsetType)

	// llvm.trap - used by genBoundsCheck (expr.go) to abort immediately on an
	// out-of-range array index rather than proceeding to an out-of-bounds
	// GEP (see AGENTS.md's "Array bounds checking" section). Declared as a
	// plain extern void() function, same as printf/malloc/memcpy/memcmp
	// above - LLVM recognizes the "llvm." name prefix as an intrinsic
	// regardless of how it's declared in the IR, so this needs no special
	// go-llvm binding beyond AddFunction.
	g.trapType = llvm.FunctionType(g.voidTy, nil, false)
	g.trapFn = llvm.AddFunction(g.mod, "llvm.trap", g.trapType)

	g.setupArena()

	// print always appends a trailing newline (see AGENTS.md's "print
	// builtin" section) - printf's own format string does the formatting;
	// %.*s takes an explicit length so a non-null-terminated string value
	// never needs a strlen-based format. i8/i16 reuse this same "%d" - the
	// value is sign-extended to i32 before the call (see genPrintCall),
	// matching C's own default-argument-promotion rule for a variadic call
	// (which this package's manually-built printf calls don't get for free,
	// since there's no real C compiler in the loop). i64 uses "%lld" - see
	// AGENTS.md's codegen section for why this (not a naive assumption) is
	// the confirmed-correct specifier for this project's mingw64/msvcrt
	// toolchain. f32/f64 share "%f" - a float argument is always widened to
	// double before the call (see genPrintCall), matching C's own variadic
	// float promotion.
	g.fmtInt = g.defineCString(".fmt.int", "%d\n")
	g.fmtInt64 = g.defineCString(".fmt.int64", "%lld\n")
	g.fmtFloat = g.defineCString(".fmt.float", "%f\n")
	g.fmtStr = g.defineCString(".fmt.str", "%.*s\n")

	// Struct/array printing (genPrintStructValue/genPrintArrayValue below)
	// builds its output from repeated printf calls instead of one format
	// string - a "bare" (no newline) specifier for each field/element, plus
	// the literal punctuation between them. None of these contain a '%', so
	// calling printf with just the format pointer and no variadic arguments
	// is safe.
	g.fmtIntBare = g.defineCString(".fmt.int.bare", "%d")
	g.fmtInt64Bare = g.defineCString(".fmt.int64.bare", "%lld")
	g.fmtFloatBare = g.defineCString(".fmt.float.bare", "%f")
	g.fmtStrBare = g.defineCString(".fmt.str.bare", "%.*s")
	g.fmtSpace = g.defineCString(".fmt.space", " ")
	g.fmtLBrace = g.defineCString(".fmt.lbrace", "{")
	g.fmtRBrace = g.defineCString(".fmt.rbrace", "}")
	g.fmtLBracket = g.defineCString(".fmt.lbracket", "[")
	g.fmtRBracket = g.defineCString(".fmt.rbracket", "]")
	g.fmtNewline = g.defineCString(".fmt.newline", "\n")
}

// arenaChunkSize is the size (in bytes) of one block the arena grows by via
// malloc - see setupArena's doc comment for the full design. A single
// request bigger than this still gets served (its own oversized block, sized
// to fit exactly), so nothing is ever rejected for being "too big for the
// arena" - this only controls how often a normal small request has to grow
// the arena at all.
const arenaChunkSize = 64 * 1024

// setupArena builds this module's single bump allocator: a generated LLVM
// function (`llvm_lang.arena_alloc`, not a libc call) that every heap-needing
// string operation calls into instead of malloc directly (see
// genStringConcat) - centralizing "how this language allocates heap memory"
// behind one primitive, per AGENTS.md's codegen section and BLOCKERS.md.
//
// Design: one process-lifetime arena, growing in malloc'd chunks (never
// freed - there is still no GC/`free` in this language; this remains a real,
// intentional leak overall, exactly as before - see BLOCKERS.md). Two
// mutable globals hold the arena's live state:
//   - `.arena.cursor` - a pointer to the next free byte in the current block.
//   - `.arena.remaining` - how many bytes are left in that block.
//
// Allocating size bytes: if the current block doesn't have size bytes left,
// malloc a fresh block first (sized to arenaChunkSize, or exactly size for a
// single request bigger than that) and point the arena at it - whatever was
// left in the old block is simply abandoned, not reclaimed (a real, accepted
// waste in the name of never needing a free/realloc-in-place dance). Either
// way, the cursor is then bumped forward by size and the pre-bump address
// handed back as the allocation.
//
// This is deliberately not a general-purpose allocator: no per-allocation
// header, no way to free a single allocation, no thread-safety (this
// language has no concurrency yet). It only needs to satisfy "hand out
// successive non-overlapping regions," which is exactly what every current
// caller (string concatenation) needs.
func (g *Generator) setupArena() {
	g.arenaCursorGlobal = llvm.AddGlobal(g.mod, g.ptrTy, ".arena.cursor")
	g.arenaCursorGlobal.SetInitializer(llvm.ConstNull(g.ptrTy))
	g.arenaCursorGlobal.SetLinkage(llvm.PrivateLinkage)

	g.arenaRemainingGlobal = llvm.AddGlobal(g.mod, g.i64Ty, ".arena.remaining")
	g.arenaRemainingGlobal.SetInitializer(llvm.ConstInt(g.i64Ty, 0, false))
	g.arenaRemainingGlobal.SetLinkage(llvm.PrivateLinkage)

	g.arenaAllocType = llvm.FunctionType(g.ptrTy, []llvm.Type{g.i64Ty}, false)
	fn := llvm.AddFunction(g.mod, "llvm_lang.arena_alloc", g.arenaAllocType)
	fn.SetLinkage(llvm.PrivateLinkage)
	g.arenaAllocFn = fn
	size := fn.Param(0)

	entryBB := g.ctx.AddBasicBlock(fn, "entry")
	growBB := g.ctx.AddBasicBlock(fn, "arena.grow")
	okBB := g.ctx.AddBasicBlock(fn, "arena.ok")

	g.builder.SetInsertPointAtEnd(entryBB)
	remaining := g.builder.CreateLoad(g.i64Ty, g.arenaRemainingGlobal, "")
	fits := g.builder.CreateICmp(llvm.IntUGE, remaining, size, "")
	g.builder.CreateCondBr(fits, okBB, growBB)

	g.builder.SetInsertPointAtEnd(growBB)
	chunkConst := llvm.ConstInt(g.i64Ty, arenaChunkSize, false)
	needsBigger := g.builder.CreateICmp(llvm.IntUGT, size, chunkConst, "")
	blockSize := g.builder.CreateSelect(needsBigger, size, chunkConst, "")
	newBlock := g.builder.CreateCall(g.mallocType, g.mallocFn, []llvm.Value{blockSize}, "")
	g.builder.CreateStore(newBlock, g.arenaCursorGlobal)
	g.builder.CreateStore(blockSize, g.arenaRemainingGlobal)
	g.builder.CreateBr(okBB)

	g.builder.SetInsertPointAtEnd(okBB)
	cursor := g.builder.CreateLoad(g.ptrTy, g.arenaCursorGlobal, "")
	curRemaining := g.builder.CreateLoad(g.i64Ty, g.arenaRemainingGlobal, "")
	nextCursor := g.builder.CreateInBoundsGEP(g.i8Ty, cursor, []llvm.Value{size}, "")
	nextRemaining := g.builder.CreateSub(curRemaining, size, "")
	g.builder.CreateStore(nextCursor, g.arenaCursorGlobal)
	g.builder.CreateStore(nextRemaining, g.arenaRemainingGlobal)
	g.builder.CreateRet(cursor)
}

// genArenaAlloc calls into the generated arena allocator (setupArena) for
// size bytes - the one place any string operation should ever ask for heap
// memory, instead of calling malloc directly.
func (g *Generator) genArenaAlloc(size llvm.Value) llvm.Value {
	return g.builder.CreateCall(g.arenaAllocType, g.arenaAllocFn, []llvm.Value{size}, "")
}

// defineCString creates a private, unnamed-addr global holding text plus a
// trailing NUL, and returns a pointer to it - for the small set of format
// strings this package itself needs (see setupRuntime), not general
// language-level string literals (see constStringValue for those - they
// don't need null termination at all, since every consumer already carries
// an explicit length).
func (g *Generator) defineCString(name, text string) llvm.Value {
	data := g.ctx.ConstString(text, true)
	glob := llvm.AddGlobal(g.mod, data.Type(), name)
	glob.SetInitializer(data)
	glob.SetGlobalConstant(true)
	glob.SetLinkage(llvm.PrivateLinkage)
	glob.SetUnnamedAddr(true)
	return glob
}

// constStringValue builds a {ptr, i32} string value for text - a literal
// {i8* dataPtr, i32 length} constant, backing data deduplicated per distinct
// text via strLiterals. Used both for an ordinary StringLit expression and
// for a compile-time-constant string (constfold.go's constExpr), since a
// global's address is itself a valid LLVM constant expression - no
// difference between "runtime" and "constant-context" string literals here.
func (g *Generator) constStringValue(text string) llvm.Value {
	glob, ok := g.strLiterals[text]
	if !ok {
		glob = g.defineCString(fmt.Sprintf(".str.%d", len(g.strLiterals)), text)
		g.strLiterals[text] = glob
	}
	// Must go through g.ctx (not the package-level llvm.ConstStruct, which
	// builds its literal struct type in llvm's process-global context) -
	// otherwise the resulting value's type is a structurally-identical but
	// distinct anonymous struct type from g.stringTy, and LLVM's verifier
	// rejects assigning it to anything actually typed g.stringTy (e.g. a
	// global var's initializer).
	return g.ctx.ConstStruct([]llvm.Value{
		glob,
		llvm.ConstInt(g.i32Ty, uint64(len(text)), false),
	}, false)
}

// genStringConcat implements `+` (and `+=`) on two already-evaluated string
// values: ask the arena (setupArena/genArenaAlloc) for a buffer sized to fit
// both, memcpy each operand's bytes into it in order, and build the
// resulting {ptr, len} value.
func (g *Generator) genStringConcat(lhs, rhs llvm.Value) llvm.Value {
	lp := g.builder.CreateExtractValue(lhs, 0, "")
	ll := g.builder.CreateExtractValue(lhs, 1, "")
	rp := g.builder.CreateExtractValue(rhs, 0, "")
	rl := g.builder.CreateExtractValue(rhs, 1, "")

	total := g.builder.CreateAdd(ll, rl, "")
	totalSize := g.builder.CreateZExt(total, g.i64Ty, "")
	buf := g.genArenaAlloc(totalSize)

	llSize := g.builder.CreateZExt(ll, g.i64Ty, "")
	rlSize := g.builder.CreateZExt(rl, g.i64Ty, "")
	g.builder.CreateCall(g.memcpyType, g.memcpyFn, []llvm.Value{buf, lp, llSize}, "")
	tail := g.builder.CreateInBoundsGEP(g.i8Ty, buf, []llvm.Value{ll}, "")
	g.builder.CreateCall(g.memcpyType, g.memcpyFn, []llvm.Value{tail, rp, rlSize}, "")

	result := llvm.Undef(g.stringTy)
	result = g.builder.CreateInsertValue(result, buf, 0, "")
	result = g.builder.CreateInsertValue(result, total, 1, "")
	return result
}

// genStringEqual implements `==`/`!=` on two already-evaluated string
// values: unequal lengths short-circuit to unequal content (memcmp only
// runs when the lengths already match), matching AGENTS.md's rule that
// string equality is a real content comparison, not pointer identity.
func (g *Generator) genStringEqual(lhs, rhs llvm.Value, wantEqual bool) llvm.Value {
	lp := g.builder.CreateExtractValue(lhs, 0, "")
	ll := g.builder.CreateExtractValue(lhs, 1, "")
	rp := g.builder.CreateExtractValue(rhs, 0, "")
	rl := g.builder.CreateExtractValue(rhs, 1, "")
	lenEq := g.builder.CreateICmp(llvm.IntEQ, ll, rl, "")

	startBB := g.builder.GetInsertBlock()
	cmpBB := g.ctx.AddBasicBlock(g.curFn, "streq.cmp")
	contBB := g.ctx.AddBasicBlock(g.curFn, "streq.cont")
	g.builder.CreateCondBr(lenEq, cmpBB, contBB)

	g.builder.SetInsertPointAtEnd(cmpBB)
	llSize := g.builder.CreateZExt(ll, g.i64Ty, "")
	cmpResult := g.builder.CreateCall(g.memcmpType, g.memcmpFn, []llvm.Value{lp, rp, llSize}, "")
	contentEq := g.builder.CreateICmp(llvm.IntEQ, cmpResult, llvm.ConstInt(g.i32Ty, 0, false), "")
	cmpEndBB := g.builder.GetInsertBlock()
	g.builder.CreateBr(contBB)

	g.builder.SetInsertPointAtEnd(contBB)
	phi := g.builder.CreatePHI(g.boolTy, "")
	phi.AddIncoming(
		[]llvm.Value{llvm.ConstInt(g.boolTy, 0, false), contentEq},
		[]llvm.BasicBlock{startBB, cmpEndBB},
	)
	if wantEqual {
		return phi
	}
	return g.builder.CreateNot(phi, "")
}

// genStringOrder implements `< <= > >=` on two already-evaluated string
// values: a real byte-by-byte lexicographic comparison (Go's own rule for
// string ordering), not a length or pointer compare. op is one of
// "<"/"<="/">"/">=" (see genBinaryExpr).
func (g *Generator) genStringOrder(op string, lhs, rhs llvm.Value) llvm.Value {
	cmp := g.genStringCompareSign(lhs, rhs)
	zero := llvm.ConstInt(g.i32Ty, 0, false)
	switch op {
	case "<":
		return g.builder.CreateICmp(llvm.IntSLT, cmp, zero, "")
	case "<=":
		return g.builder.CreateICmp(llvm.IntSLE, cmp, zero, "")
	case ">":
		return g.builder.CreateICmp(llvm.IntSGT, cmp, zero, "")
	case ">=":
		return g.builder.CreateICmp(llvm.IntSGE, cmp, zero, "")
	default:
		panic("codegen: genStringOrder called with unsupported operator " + op)
	}
}

// genStringCompareSign returns an i32 whose sign (negative/zero/positive)
// reflects lhs's lexicographic relation to rhs - negative if lhs sorts
// before rhs, zero if they're equal, positive if lhs sorts after rhs. This
// is a genuine content comparison, byte by byte over the shared prefix (via
// memcmp), with a length-based tie-break for the same-prefix-different-length
// case (e.g. "ab" < "abc"): memcmp over just the shorter operand's length
// reports the two equal whenever one is a prefix of the other, so the
// lengths themselves decide the final order exactly the way Go's own string
// comparison does at the byte level.
//
// Built entirely with arithmetic/select, no branching: memcmp already only
// examines the shared prefix, so there is no "run memcmp only if X" control
// flow needed the way genStringEqual's length short-circuit has (that one
// skips memcmp entirely on a length mismatch as a cheap optimization; here
// the prefix comparison is required unconditionally to know the true
// lexicographic order, not just equal-vs-not).
func (g *Generator) genStringCompareSign(lhs, rhs llvm.Value) llvm.Value {
	lp := g.builder.CreateExtractValue(lhs, 0, "")
	ll := g.builder.CreateExtractValue(lhs, 1, "")
	rp := g.builder.CreateExtractValue(rhs, 0, "")
	rl := g.builder.CreateExtractValue(rhs, 1, "")

	shorter := g.builder.CreateSelect(g.builder.CreateICmp(llvm.IntULT, ll, rl, ""), ll, rl, "")
	shorterSize := g.builder.CreateZExt(shorter, g.i64Ty, "")
	prefixCmp := g.builder.CreateCall(g.memcmpType, g.memcmpFn, []llvm.Value{lp, rp, shorterSize}, "")

	lenDiff := g.builder.CreateSub(ll, rl, "")
	prefixDiffers := g.builder.CreateICmp(llvm.IntNE, prefixCmp, llvm.ConstInt(g.i32Ty, 0, false), "")
	return g.builder.CreateSelect(prefixDiffers, prefixCmp, lenDiff, "")
}

// isPrintCall reports whether calleeNode names the predeclared print
// builtin - see sema/typecheck.go's isPrintCall, which this mirrors: print
// has no declaration site (Decl is ast.InvalidNode), so it can't be looked
// up in g.funcs like every user function. A user function/method literally
// named "print" (legal - see sema/scope.go's universeScope: it lives in the
// outermost scope, so package scope can shadow it) is not this - its Decl is
// a real FuncDecl, so it goes through the normal call path instead.
func (g *Generator) isPrintCall(calleeNode ast.NodeIndex) bool {
	if g.tree.Nodes[calleeNode].Kind != enums.NodeKinds.Ident {
		return false
	}
	sym, ok := g.info.Refs[calleeNode]
	return ok && sym.Kind == sema.SymFunc && sym.Decl == ast.InvalidNode && sym.Name == "print"
}

// isBuiltinCall reports whether calleeNode names the predeclared builtin
// function name (make/append/len - print has its own isPrintCall above,
// unchanged) - mirrors sema/typecheck.go's own isBuiltinCall exactly.
func (g *Generator) isBuiltinCall(calleeNode ast.NodeIndex, name string) bool {
	if g.tree.Nodes[calleeNode].Kind != enums.NodeKinds.Ident {
		return false
	}
	sym, ok := g.info.Refs[calleeNode]
	return ok && sym.Kind == sema.SymFunc && sym.Decl == ast.InvalidNode && sym.Name == name
}

// genArenaAllocElems asks the arena for a buffer sized to fit count elements
// of elemLLType, returning the allocated pointer, the element size (an i64
// constant, via llvm.SizeOf - the classic null-pointer-GEP trick, resolved to
// a real constant by LLVM without this package needing its own target-data-
// layout query), and the total byte count already computed to size the
// allocation (count zero-extended to i64, times elemSize) - shared by
// genMakeCall, genAppendCall's growth path, and a dynamic-array composite
// literal (genCompositeLitInto). Returning totalBytes lets genMakeCall reuse
// it directly for its own memset call instead of recomputing the identical
// zext+mul pair a second time.
func (g *Generator) genArenaAllocElems(elemLLType llvm.Type, count llvm.Value) (buf, elemSize, totalBytes llvm.Value) {
	elemSize = llvm.SizeOf(elemLLType)
	countSize := g.builder.CreateZExt(count, g.i64Ty, "")
	totalBytes = g.builder.CreateMul(countSize, elemSize, "")
	return g.genArenaAlloc(totalBytes), elemSize, totalBytes
}

// genMakeCall lowers `make([]T, n)` / `make([]T, n, cap)` (see
// LANGUAGE.md's "Dynamic arrays" section): allocate a fresh backing buffer
// sized for cap elements (n, when cap is omitted) via the arena allocator -
// the exact same primitive genStringConcat already routes through, per
// AGENTS.md's "one centralized allocation point" rule - zero-fill the whole
// allocated buffer (memset), and return the resulting {ptr, n, cap} value.
// n/cap are ordinary runtime values (see sema's checkMakeCall) - ok, since a
// dynamic array's whole point is a runtime-determined size - so n<0, cap<0,
// and cap<n (when cap is given) are all checked here, at runtime, the same
// trap-based mechanism genBoundsCheck already uses for an out-of-range index
// (there is no exception handling in this language - see AGENTS.md's "Array
// bounds checking" section - so this is a hard process abort, exactly like
// that one). The negative-size checks matter even when cap defaults to n
// (the 2-argument form): n/cap are zero-extended (not sign-extended) to i64
// for the byte-count computation below, so a negative i32 n would otherwise
// become an enormous unsigned byte count instead of a clean trap.
func (g *Generator) genMakeCall(callNode ast.NodeIndex, args []ast.NodeIndex) llvm.Value {
	target := g.info.Types[callNode]
	elemLLType := g.llvmType(*target.Elem)

	nVal := g.genExpr(args[1])
	capVal := nVal
	if len(args) == 3 {
		capVal = g.genExpr(args[2])
	}
	g.genMakeSizeCheck(nVal, capVal)

	buf, _, capBytes := g.genArenaAllocElems(elemLLType, capVal)
	g.builder.CreateCall(g.memsetType, g.memsetFn, []llvm.Value{buf, llvm.ConstInt(g.i32Ty, 0, false), capBytes}, "")

	result := llvm.Undef(g.dynArrTy)
	result = g.builder.CreateInsertValue(result, buf, 0, "")
	result = g.builder.CreateInsertValue(result, nVal, 1, "")
	result = g.builder.CreateInsertValue(result, capVal, 2, "")
	return result
}

// genMakeSizeCheck traps (llvm.trap + unreachable, same as genBoundsCheck) on
// any of make's own runtime size invariants failing: nVal < 0, capVal < 0, or
// capVal < nVal ("cap can't be smaller than the requested length" - see
// genMakeCall's doc comment for why these are all runtime checks rather than
// sema diagnostics). All three conditions are folded into one trap block
// rather than three separate ones - they're all the same class of "make was
// asked for a nonsensical size" failure, so there's no value in distinguishing
// which one fired once the process is about to abort anyway.
func (g *Generator) genMakeSizeCheck(nVal, capVal llvm.Value) {
	zero := llvm.ConstInt(g.i32Ty, 0, true)
	nNonNegative := g.builder.CreateICmp(llvm.IntSGE, nVal, zero, "")
	capNonNegative := g.builder.CreateICmp(llvm.IntSGE, capVal, zero, "")
	capAtLeastN := g.builder.CreateICmp(llvm.IntSGE, capVal, nVal, "")
	ok := g.builder.CreateAnd(nNonNegative, capNonNegative, "")
	ok = g.builder.CreateAnd(ok, capAtLeastN, "")

	trapBB := g.ctx.AddBasicBlock(g.curFn, "make.size.trap")
	okBB := g.ctx.AddBasicBlock(g.curFn, "make.size.ok")
	g.builder.CreateCondBr(ok, okBB, trapBB)

	g.builder.SetInsertPointAtEnd(trapBB)
	g.builder.CreateCall(g.trapType, g.trapFn, nil, "")
	g.builder.CreateUnreachable()

	g.builder.SetInsertPointAtEnd(okBB)
}

// genAppendCall lowers `append(slice, elem)` (see LANGUAGE.md's "Dynamic
// arrays" section - scoped to exactly one element per call): if len < cap,
// elem is written directly into the existing backing buffer at index len and
// the result reuses the same pointer/cap, mutating in place, matching Go's
// own (sometimes-surprising but well-defined) aliasing behavior when capacity
// allows it; if len == cap (cap == 0 included), a new, larger buffer is
// allocated via the arena (newcap = max(1, cap*2) - simple doubling, see
// DECISIONS.md), the existing len elements are memcpy'd over, and the result
// carries the new pointer/capacity instead - the old buffer is simply
// abandoned, consistent with this project's existing, explicit "the arena
// never frees" design.
func (g *Generator) genAppendCall(args []ast.NodeIndex) llvm.Value {
	sliceType := g.info.Types[args[0]]
	elemLLType := g.llvmType(*sliceType.Elem)

	sliceVal := g.genExpr(args[0])
	elemVal := g.genExpr(args[1])

	ptr := g.builder.CreateExtractValue(sliceVal, 0, "")
	length := g.builder.CreateExtractValue(sliceVal, 1, "")
	capacity := g.builder.CreateExtractValue(sliceVal, 2, "")

	hasRoom := g.builder.CreateICmp(llvm.IntSLT, length, capacity, "")

	fitBB := g.ctx.AddBasicBlock(g.curFn, "append.fit")
	growBB := g.ctx.AddBasicBlock(g.curFn, "append.grow")
	contBB := g.ctx.AddBasicBlock(g.curFn, "append.cont")
	g.builder.CreateCondBr(hasRoom, fitBB, growBB)

	g.builder.SetInsertPointAtEnd(fitBB)
	fitEndBB := g.builder.GetInsertBlock()
	g.builder.CreateBr(contBB)

	g.builder.SetInsertPointAtEnd(growBB)
	one := llvm.ConstInt(g.i32Ty, 1, false)
	two := llvm.ConstInt(g.i32Ty, 2, false)
	doubled := g.builder.CreateMul(capacity, two, "")
	newCap := g.builder.CreateSelect(g.builder.CreateICmp(llvm.IntSLT, doubled, one, ""), one, doubled, "")
	newBuf, elemSize, _ := g.genArenaAllocElems(elemLLType, newCap)
	oldBytes := g.builder.CreateMul(g.builder.CreateZExt(length, g.i64Ty, ""), elemSize, "")
	g.builder.CreateCall(g.memcpyType, g.memcpyFn, []llvm.Value{newBuf, ptr, oldBytes}, "")
	growEndBB := g.builder.GetInsertBlock()
	g.builder.CreateBr(contBB)

	g.builder.SetInsertPointAtEnd(contBB)
	finalPtr := g.builder.CreatePHI(g.ptrTy, "")
	finalPtr.AddIncoming([]llvm.Value{ptr, newBuf}, []llvm.BasicBlock{fitEndBB, growEndBB})
	finalCap := g.builder.CreatePHI(g.i32Ty, "")
	finalCap.AddIncoming([]llvm.Value{capacity, newCap}, []llvm.BasicBlock{fitEndBB, growEndBB})

	elemAddr := g.builder.CreateInBoundsGEP(elemLLType, finalPtr, []llvm.Value{length}, "")
	g.builder.CreateStore(elemVal, elemAddr)
	newLen := g.builder.CreateAdd(length, llvm.ConstInt(g.i32Ty, 1, false), "")

	result := llvm.Undef(g.dynArrTy)
	result = g.builder.CreateInsertValue(result, finalPtr, 0, "")
	result = g.builder.CreateInsertValue(result, newLen, 1, "")
	result = g.builder.CreateInsertValue(result, finalCap, 2, "")
	return result
}

// genLenCall lowers `len(x)` - a dynamic array's runtime len field, a
// fixed-size array's compile-time-known size (folded to a constant directly,
// the same value its own bounds check already uses - see sema's
// checkLenCall), or a string's runtime length field.
func (g *Generator) genLenCall(argNode ast.NodeIndex) llvm.Value {
	t := g.info.Types[argNode]
	switch {
	case t.Kind == sema.TypeArray && t.Dynamic:
		v := g.genExpr(argNode)
		return g.builder.CreateExtractValue(v, 1, "")
	case t.Kind == sema.TypeArray:
		return llvm.ConstInt(g.i32Ty, uint64(t.Size), false)
	case t.Kind == sema.TypeString:
		v := g.genExpr(argNode)
		return g.builder.CreateExtractValue(v, 1, "")
	default:
		// Only a dynamic array, fixed-size array, or string reach here on a
		// tree that already passed sema.Check (see checkLenCall,
		// sema/typecheck.go, and the package doc comment).
		panic("codegen: genLenCall reached an unsupported type " + t.String())
	}
}

// genPrintCall lowers `print(arg)`. Every numeric width/bool/string prints
// directly via a single self-contained printf call (its own format string
// already includes the trailing newline - see AGENTS.md's "print builtin"
// section); a struct or array value is rendered recursively
// (genPrintStructValue/genPrintArrayValue), field-by-field/element-by-
// element, with the trailing newline appended once at the very end instead.
// See AGENTS.md's codegen section for the exact struct/array format chosen
// (`{1 2}`, `[1 2 3]`) and the printf-format-specifier gotcha this project
// hit for i64 specifically.
func (g *Generator) genPrintCall(argNode ast.NodeIndex) {
	t := g.info.Types[argNode]
	v := g.genExpr(argNode)
	switch t.Kind {
	case sema.TypeI8, sema.TypeI16:
		g.builder.CreateCall(g.printfType, g.printfFn, []llvm.Value{g.fmtInt, g.builder.CreateSExt(v, g.i32Ty, "")}, "")
	case sema.TypeI32:
		g.builder.CreateCall(g.printfType, g.printfFn, []llvm.Value{g.fmtInt, v}, "")
	case sema.TypeI64:
		g.builder.CreateCall(g.printfType, g.printfFn, []llvm.Value{g.fmtInt64, v}, "")
	case sema.TypeF32:
		g.builder.CreateCall(g.printfType, g.printfFn, []llvm.Value{g.fmtFloat, g.builder.CreateFPExt(v, g.f64Ty, "")}, "")
	case sema.TypeF64:
		g.builder.CreateCall(g.printfType, g.printfFn, []llvm.Value{g.fmtFloat, v}, "")
	case sema.TypeBool:
		g.genPrintStringValue(g.boolStringValue(v))
	case sema.TypeString:
		g.genPrintStringValue(v)
	case sema.TypeStruct:
		g.genPrintStructValue(t, v)
		g.genPrintLiteral(g.fmtNewline)
	case sema.TypeArray:
		g.genPrintArrayValue(t, v)
		g.genPrintLiteral(g.fmtNewline)
	default:
		// Every numeric width, string/bool/struct/array are the only Types
		// print's single argument can ever have on a tree that already
		// passed sema.Check (see checkPrintCall in sema/typecheck.go and the
		// package doc comment) - TypeInvalid/TypeVoid/either untyped kind
		// can't reach here.
		panic("codegen: print does not support values of type " + t.String())
	}
}

// boolStringValue selects between the two cached "true"/"false" string
// values based on v - shared between genPrintCall's own top-level bool case
// and genPrintValueBare's (a struct/array's nested bool field/element).
func (g *Generator) boolStringValue(v llvm.Value) llvm.Value {
	trueVal := g.constStringValue("true")
	falseVal := g.constStringValue("false")
	return g.builder.CreateSelect(v, trueVal, falseVal, "")
}

func (g *Generator) genPrintStringValue(v llvm.Value) {
	ptr := g.builder.CreateExtractValue(v, 0, "")
	ln := g.builder.CreateExtractValue(v, 1, "")
	g.builder.CreateCall(g.printfType, g.printfFn, []llvm.Value{g.fmtStr, ln, ptr}, "")
}

// genPrintLiteral calls printf with a literal, argument-less format string -
// used for struct/array punctuation and the trailing newline after one. Safe
// because none of these contain a '%'.
func (g *Generator) genPrintLiteral(fmtGlobal llvm.Value) {
	g.builder.CreateCall(g.printfType, g.printfFn, []llvm.Value{fmtGlobal}, "")
}

// genPrintValueBare renders v (of type t) with no trailing newline - the
// recursive building block genPrintStructValue/genPrintArrayValue use for
// each field/element in turn. Mirrors genPrintCall's own switch, but every
// case's format string omits the newline genPrintCall's top-level call
// includes (see fmtIntBare/fmtStrBare vs fmtInt/fmtStr in setupRuntime) -
// only the outermost print(...) call ever emits one, after the whole value
// has finished rendering.
func (g *Generator) genPrintValueBare(t sema.Type, v llvm.Value) {
	switch t.Kind {
	case sema.TypeI8, sema.TypeI16:
		g.builder.CreateCall(g.printfType, g.printfFn, []llvm.Value{g.fmtIntBare, g.builder.CreateSExt(v, g.i32Ty, "")}, "")
	case sema.TypeI32:
		g.builder.CreateCall(g.printfType, g.printfFn, []llvm.Value{g.fmtIntBare, v}, "")
	case sema.TypeI64:
		g.builder.CreateCall(g.printfType, g.printfFn, []llvm.Value{g.fmtInt64Bare, v}, "")
	case sema.TypeF32:
		g.builder.CreateCall(g.printfType, g.printfFn, []llvm.Value{g.fmtFloatBare, g.builder.CreateFPExt(v, g.f64Ty, "")}, "")
	case sema.TypeF64:
		g.builder.CreateCall(g.printfType, g.printfFn, []llvm.Value{g.fmtFloatBare, v}, "")
	case sema.TypeBool:
		g.genPrintStringValueBare(g.boolStringValue(v))
	case sema.TypeString:
		g.genPrintStringValueBare(v)
	case sema.TypeStruct:
		g.genPrintStructValue(t, v)
	case sema.TypeArray:
		g.genPrintArrayValue(t, v)
	default:
		// Every numeric width, string/bool/struct/array are the only Types
		// that exist (see sema/types.go) besides TypeInvalid/TypeVoid/either
		// untyped kind - every reachable one is handled above, so this is
		// unreachable on a tree that already passed sema.Check (see the
		// package doc comment).
		panic("codegen: genPrintValueBare reached an unsupported type " + t.String())
	}
}

func (g *Generator) genPrintStringValueBare(v llvm.Value) {
	ptr := g.builder.CreateExtractValue(v, 0, "")
	ln := g.builder.CreateExtractValue(v, 1, "")
	g.builder.CreateCall(g.printfType, g.printfFn, []llvm.Value{g.fmtStrBare, ln, ptr}, "")
}

// genPrintStructValue renders a struct value as `{f0 f1 ...}` - each field's
// own value (recursively rendered the same way, for a nested struct/array
// field), space-separated, in declaration order, wrapped in braces. See
// AGENTS.md's codegen section for this exact, Go-fmt-`%v`-inspired choice.
func (g *Generator) genPrintStructValue(t sema.Type, v llvm.Value) {
	layout := g.structLayouts[t.Struct]
	g.genPrintLiteral(g.fmtLBrace)
	for i, ft := range layout.fieldSemaTypes {
		if i > 0 {
			g.genPrintLiteral(g.fmtSpace)
		}
		fv := g.builder.CreateExtractValue(v, i, "")
		g.genPrintValueBare(ft, fv)
	}
	g.genPrintLiteral(g.fmtRBrace)
}

// genPrintArrayValue renders an array value as `[e0 e1 ...]` - each
// element's own value, space-separated, in index order, wrapped in
// brackets. See AGENTS.md's codegen section. A fixed-size array's element
// count is known at codegen time, so this is a static unrolled sequence of
// printf calls, same as before; a dynamic array's isn't (see
// genPrintDynArrayValue), so it needs an actual runtime loop instead.
func (g *Generator) genPrintArrayValue(t sema.Type, v llvm.Value) {
	if t.Dynamic {
		g.genPrintDynArrayValue(t, v)
		return
	}
	g.genPrintLiteral(g.fmtLBracket)
	for i := int64(0); i < t.Size; i++ {
		if i > 0 {
			g.genPrintLiteral(g.fmtSpace)
		}
		ev := g.builder.CreateExtractValue(v, int(i), "")
		g.genPrintValueBare(*t.Elem, ev)
	}
	g.genPrintLiteral(g.fmtRBracket)
}

// genPrintDynArrayValue renders a dynamic array value the same way a
// fixed-size one is (`[e0 e1 ...]`), reading its runtime len field to know
// how many elements to walk - a real runtime loop (CreateCondBr/
// AddBasicBlock, the same control-flow shape genForStmt/genBoundsCheck
// already use elsewhere in this package), not a static unrolled sequence of
// printf calls, since the element count isn't known until the program
// actually runs.
func (g *Generator) genPrintDynArrayValue(t sema.Type, v llvm.Value) {
	ptr := g.builder.CreateExtractValue(v, 0, "")
	length := g.builder.CreateExtractValue(v, 1, "")
	elemLLType := g.llvmType(*t.Elem)

	g.genPrintLiteral(g.fmtLBracket)

	idxAddr := g.createEntryAlloca(g.i32Ty, "print.idx")
	g.builder.CreateStore(llvm.ConstInt(g.i32Ty, 0, false), idxAddr)

	condBB := g.ctx.AddBasicBlock(g.curFn, "print.arr.cond")
	bodyBB := g.ctx.AddBasicBlock(g.curFn, "print.arr.body")
	spaceBB := g.ctx.AddBasicBlock(g.curFn, "print.arr.space")
	elemBB := g.ctx.AddBasicBlock(g.curFn, "print.arr.elem")
	endBB := g.ctx.AddBasicBlock(g.curFn, "print.arr.end")

	g.builder.CreateBr(condBB)

	g.builder.SetInsertPointAtEnd(condBB)
	idx := g.builder.CreateLoad(g.i32Ty, idxAddr, "")
	g.builder.CreateCondBr(g.builder.CreateICmp(llvm.IntSLT, idx, length, ""), bodyBB, endBB)

	g.builder.SetInsertPointAtEnd(bodyBB)
	isFirst := g.builder.CreateICmp(llvm.IntEQ, idx, llvm.ConstInt(g.i32Ty, 0, false), "")
	g.builder.CreateCondBr(isFirst, elemBB, spaceBB)

	g.builder.SetInsertPointAtEnd(spaceBB)
	g.genPrintLiteral(g.fmtSpace)
	g.builder.CreateBr(elemBB)

	g.builder.SetInsertPointAtEnd(elemBB)
	elemAddr := g.builder.CreateInBoundsGEP(elemLLType, ptr, []llvm.Value{idx}, "")
	elemVal := g.builder.CreateLoad(elemLLType, elemAddr, "")
	g.genPrintValueBare(*t.Elem, elemVal)
	nextIdx := g.builder.CreateAdd(idx, llvm.ConstInt(g.i32Ty, 1, false), "")
	g.builder.CreateStore(nextIdx, idxAddr)
	g.builder.CreateBr(condBB)

	g.builder.SetInsertPointAtEnd(endBB)
	g.genPrintLiteral(g.fmtRBracket)
}
