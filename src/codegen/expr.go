package codegen

import (
	"fmt"
	"strconv"

	"llvm_lang/src/ast"
	"llvm_lang/src/enums"
	"llvm_lang/src/sema"

	"tinygo.org/x/go-llvm"
)

// addrOfSymbol resolves the address backing sym - a declared var/param
// Symbol - by asking, in order: is it a local declared directly in the
// function currently being generated (g.locals - works identically whether
// that local's own storage is an ordinary stack alloca or an arena
// allocation, see allocLocalSlot, func.go); is it a top-level global
// (g.globals); or - only reachable when the function currently being
// generated is itself a lambda's own synthesized function (see
// genLambdaFunc) - is it one of *that* lambda's own captured symbols, reached
// by loading the matching field out of its own ctxPtr parameter
// (g.curCaptureIndex/g.curCtxPtr/g.curCaptureTy - see CODEGEN.md's "Lambdas"
// section).
//
// This one function serves two call sites: genAddr's own Ident case (an
// ordinary identifier reference anywhere in a function body), and
// genFuncLit's own capture-context-building lookup, resolving each captured
// symbol's address from the *enclosing* function's perspective before
// switching into the literal's own generation state. Routing both through
// the identical lookup is what makes a doubly-nested lambda's transitive
// capture relay (see sema/capture.go's own doc comment) fall out for free
// here too, with no special-casing: if the function currently being
// generated is itself a lambda relaying some symbol it doesn't own directly
// (sema's capture analysis already decided it must, since something nested
// inside it needs it), this same third branch is exactly what finds it,
// whether the caller is an ordinary Ident reference or another, deeper
// FuncLit's own capture-context construction.
func (g *Generator) addrOfSymbol(sym *sema.Symbol) llvm.Value {
	if addr, ok := g.locals[sym]; ok {
		return addr
	}
	if addr, ok := g.globals[sym]; ok {
		return addr
	}
	if idx, ok := g.curCaptureIndex[sym]; ok {
		fieldAddr := g.builder.CreateStructGEP(g.curCaptureTy, g.curCtxPtr, idx, "")
		return g.builder.CreateLoad(g.ptrTy, fieldAddr, "")
	}
	panic("codegen: identifier " + sym.Name + " has no storage")
}

// genAddr computes the address of an lvalue expression - an Ident, `this`,
// a MemberExpr (struct field), an IndexExpr (array element), or (through
// ParenExpr) any of those parenthesized. Anything else - a value expression
// used somewhere an address is needed (a method-call receiver that isn't
// itself addressable, e.g. indexing straight into a call's result) - is
// evaluated as a plain rvalue and spilled into a fresh stack slot, so every
// caller (assignment, ++/--, member/index GEP chains, a method call's
// receiver) can treat "get me an address" uniformly regardless of how the
// underlying expression came to exist.
func (g *Generator) genAddr(n ast.NodeIndex) llvm.Value {
	switch g.tree.Nodes[n].Kind {
	case enums.NodeKinds.Ident:
		return g.addrOfSymbol(g.info.Refs[n])

	case enums.NodeKinds.ThisExpr:
		// The receiver is already a pointer parameter (see
		// declareFuncSignature/genFuncBody) - its address *is* its value,
		// no alloca of its own needed.
		return g.curReceiver

	case enums.NodeKinds.MemberExpr:
		objNode := g.tree.Child(n, 0)
		base, structInfo := g.genReceiverAddr(objNode)
		layout := g.structLayouts[structInfo]
		idx := layout.fieldIndex[g.info.Refs[n]]
		return g.builder.CreateStructGEP(layout.llvmType, base, idx, "")

	case enums.NodeKinds.UnaryExpr:
		// `*p` as an lvalue (see LANGUAGE.md's "Pointers" section, and
		// sema's checkLValue - the only UnaryExpr shape ever reachable
		// here, since `&x` is never itself assignable/addressable) - the
		// address a dereference reads/writes through *is* p's own value,
		// not p's own address, so this evaluates p as a plain rvalue
		// (genExpr), not genAddr(operand).
		return g.genExpr(g.tree.Child(n, 0))

	case enums.NodeKinds.IndexExpr:
		targetNode := g.tree.Child(n, 0)
		indexNode := g.tree.Child(n, 1)
		idx := g.genExpr(indexNode)
		targetType := g.info.Types[targetNode]

		if targetType.Dynamic {
			// A dynamic array's backing storage lives on the arena heap, not
			// in the slice variable's own storage - so, unlike a fixed-size
			// array, there's no need for this expression's own address at
			// all (genAddr(targetNode)): the slice's {ptr, len, cap} value
			// already carries everything needed to compute an element's
			// address, for both a read (genLoad) and a write (genAssignStmt)
			// alike.
			sliceVal := g.genExpr(targetNode)
			ptr := g.builder.CreateExtractValue(sliceVal, 0, "")
			length := g.builder.CreateExtractValue(sliceVal, 1, "")
			g.genBoundsCheck(idx, length)
			elemLLType := g.llvmType(*targetType.Elem)
			return g.builder.CreateInBoundsGEP(elemLLType, ptr, []llvm.Value{idx}, "")
		}

		base := g.genAddr(targetNode)
		g.genBoundsCheck(idx, llvm.ConstInt(g.i32Ty, uint64(targetType.Size), true))
		arrType := g.llvmType(targetType)
		zero := llvm.ConstInt(g.i32Ty, 0, false)
		return g.builder.CreateInBoundsGEP(arrType, base, []llvm.Value{zero, idx}, "")

	case enums.NodeKinds.ParenExpr:
		return g.genAddr(g.tree.Child(n, 0))

	case enums.NodeKinds.CompositeLit:
		t := g.llvmType(g.info.Types[n])
		tmp := g.createEntryAlloca(t, "lit")
		g.genCompositeLitInto(tmp, n)
		return tmp

	default:
		v := g.genExpr(n)
		t := g.llvmType(g.info.Types[n])
		tmp := g.createEntryAlloca(t, "tmp")
		g.builder.CreateStore(v, tmp)
		return tmp
	}
}

// genReceiverAddr computes the address of a struct-value expression used as
// a MemberExpr's object (a plain field access or a method-call receiver) -
// or, when objNode is itself pointer-typed (`*T`), auto-derefs it (see
// LANGUAGE.md's "Pointers" section: `p.field`/`p.method(...)` on a `*T`
// behaves exactly like `(*p).field`/`(*p).method(...)`) by evaluating the
// pointer's own value directly, rather than the address of whatever
// variable happens to be holding it. Shared by genAddr's MemberExpr case and
// genMethodCall - the two call sites that need a struct receiver's address.
func (g *Generator) genReceiverAddr(objNode ast.NodeIndex) (llvm.Value, *sema.StructInfo) {
	objType := g.info.Types[objNode]
	if objType.Kind == sema.TypePointer {
		return g.genExpr(objNode), objType.Elem.Struct
	}
	return g.genAddr(objNode), objType.Struct
}

// genLoad computes n's address (genAddr) and loads its value - the common
// path shared by every lvalue-shaped expression kind used as an rvalue
// (Ident naming a var/param, MemberExpr, IndexExpr, ThisExpr). Ident's own
// genExpr case doesn't always go through this - a bare reference to a
// declared *function* has no address to load from at all (see genExpr's
// Ident case, and genFuncValue).
func (g *Generator) genLoad(n ast.NodeIndex) llvm.Value {
	addr := g.genAddr(n)
	return g.builder.CreateLoad(g.llvmType(g.info.Types[n]), addr, "")
}

// genFuncValue builds the fat-pointer value {fnPtr, ctxPtr} for a bare,
// uncalled reference to a free function (`add`, not `add(...)`) - see
// CODEGEN.md's "Lambdas" section (which supersedes the plain-null-ctxPtr
// description the "First-class functions" section originally shipped with)
// for the representation this implements and why it changed.
//
// fnPtr is no longer sym's own real function address directly. A genuine
// lambda's real underlying function (see genLambdaFunc) must take its own
// ctxPtr as a real first parameter - it needs to actually dereference it to
// reach its captures - but a free function's real declared signature
// (g.funcs[sym]) still has no such parameter at all, so that a *direct* call
// (genFuncCall/isDirectFuncCall) stays genuinely zero-overhead, completely
// unchanged from before this round. Both kinds of function value can flow
// through the exact same func(...)-typed variable, and an *indirect* call
// (genIndirectCall) has no way to tell which one it's holding at the call
// site - so both must present the identical, uniform ctxPtr-first calling
// convention the moment they're called through that fat pointer. genFuncThunk
// builds (and memoizes) a tiny adapter function for sym that does exactly
// that: takes (and ignores) a ctxPtr parameter, then calls straight through
// to sym's own real function with the rest - fnPtr here is that thunk's
// address, not sym's own.
//
// ctxPtr itself is still always a null pointer constant - a free-function
// reference never captures anything (there's nothing to close over), unlike
// a genuine lambda's own real, non-null capture context (see genFuncLit).
func (g *Generator) genFuncValue(sym *sema.Symbol) llvm.Value {
	thunk := g.genFuncThunk(sym)
	ctxPtr := llvm.ConstNull(g.ptrTy)
	// Must go through g.ctx (not the package-level llvm.ConstStruct), same
	// reasoning as constStringValue: otherwise the result's type is a
	// structurally-identical but distinct anonymous struct type from
	// g.funcValTy, and LLVM's verifier rejects assigning it to anything
	// actually typed g.funcValTy (a local's alloca, a param, a return slot).
	return g.ctx.ConstStruct([]llvm.Value{thunk, ctxPtr}, false)
}

// genFuncThunk returns sym's (a declared free function's) uniform-ABI thunk,
// building and memoizing it (g.thunks) on first use - however many separate
// bare references to the same function exist in the program, each one reuses
// the identical thunk rather than synthesizing a new one. See genFuncValue's
// own doc comment for why this exists: the thunk's real LLVM signature is
// sym's own real signature with one extra leading ctxPtr parameter, which its
// body simply ignores before calling straight through to sym's own actual
// function and returning its result unchanged - it exists purely so
// genIndirectCall's own uniform "always pass ctxPtr first" calling
// convention has something valid to call through no matter which kind of
// function value it's holding (see CODEGEN.md's "Lambdas" section).
func (g *Generator) genFuncThunk(sym *sema.Symbol) llvm.Value {
	if thunk, ok := g.thunks[sym]; ok {
		return thunk
	}

	entry := g.funcs[sym]
	realParamTypes := entry.fnType.ParamTypes()
	thunkParamTypes := make([]llvm.Type, len(realParamTypes)+1)
	thunkParamTypes[0] = g.ptrTy
	copy(thunkParamTypes[1:], realParamTypes)
	thunkType := llvm.FunctionType(entry.fnType.ReturnType(), thunkParamTypes, false)

	thunk := llvm.AddFunction(g.mod, sym.Name+".thunk", thunkType)
	thunk.SetLinkage(llvm.PrivateLinkage)
	g.thunks[sym] = thunk

	// A thunk is only ever synthesized while generating some other
	// function's body (a bare function reference is always an expression
	// inside a function - see genExpr's Ident case, the only caller of
	// genFuncValue) - the builder always has a valid current block to
	// restore once the thunk's own tiny body is done.
	savedBB := g.builder.GetInsertBlock()
	entryBB := g.ctx.AddBasicBlock(thunk, "entry")
	g.builder.SetInsertPointAtEnd(entryBB)

	args := make([]llvm.Value, len(realParamTypes))
	for i := range realParamTypes {
		args[i] = thunk.Param(i + 1)
	}
	result := g.builder.CreateCall(entry.fnType, entry.fn, args, "")
	if entry.retType.Kind == sema.TypeVoid {
		g.builder.CreateRetVoid()
	} else {
		g.builder.CreateRet(result)
	}

	g.builder.SetInsertPointAtEnd(savedBB)
	return thunk
}

// genFuncLit builds a genuine closure value {fnPtr, ctxPtr} for a
// function-literal expression (see LANGUAGE.md's "Lambdas" section) - the
// counterpart to genFuncValue's bare-free-function-reference case, this time
// with a real, non-null ctxPtr: a fresh arena-allocated capture-context
// struct, one pointer field per symbol the literal captures by reference
// (info.Captures[n] - sema's capture analysis, sema/capture.go), each field
// holding that captured symbol's own real address (addrOfSymbol) rather than
// a copy of its value - Go-style closures capture by reference, not by
// value, so a later mutation through either the lambda or the original
// variable is visible to both.
//
// Every captured symbol's address is resolved right here, still in the
// *enclosing* function's own generation state (addrOfSymbol reads whatever
// g.locals/g.globals/g.curCaptureIndex currently describe, i.e. wherever
// this genFuncLit call itself is running from) - genLambdaFunc only switches
// Generator over to the literal's own fresh function-generation state
// afterward, to lower its body. This ordering is what makes a doubly-nested
// literal's own transitive capture relay work correctly with no special
// handling here: if the *enclosing* function is itself a lambda relaying a
// symbol it doesn't own directly, addrOfSymbol's own third branch already
// knows how to fetch it (through the enclosing lambda's own ctxPtr) - see
// addrOfSymbol's doc comment.
func (g *Generator) genFuncLit(n ast.NodeIndex) llvm.Value {
	captures := g.info.Captures[n]

	if len(captures) == 0 {
		// No capture context to allocate at all - both fields of the fat
		// pointer are genuine LLVM constants (a function's own address, and
		// a null pointer), so this can stay a real ConstStruct, exactly like
		// genFuncValue's own free-function case.
		fn := g.genLambdaFunc(n, nil, llvm.Type{})
		return g.ctx.ConstStruct([]llvm.Value{fn, llvm.ConstNull(g.ptrTy)}, false)
	}

	fieldTypes := make([]llvm.Type, len(captures))
	for i := range captures {
		fieldTypes[i] = g.ptrTy
	}
	ctxTy := g.ctx.StructType(fieldTypes, false)

	// Every captured symbol's address must be computed before the arena
	// allocation below writes anything - genArenaAlloc/genAddr calls don't
	// nest safely with an in-progress, not-yet-fully-stored struct.
	addrs := make([]llvm.Value, len(captures))
	for i, sym := range captures {
		addrs[i] = g.addrOfSymbol(sym)
	}

	raw := g.genArenaAlloc(llvm.SizeOf(ctxTy))
	for i, addr := range addrs {
		fieldAddr := g.builder.CreateStructGEP(ctxTy, raw, i, "")
		g.builder.CreateStore(addr, fieldAddr)
	}

	fn := g.genLambdaFunc(n, captures, ctxTy)

	// Unlike the no-capture case above, ctxPtr here (raw) is a genuine
	// runtime value (an arena_alloc call's result), not a compile-time
	// constant - LLVM's ConstStruct requires every field to itself be a
	// constant, so a real closure's fat pointer has to be built as a real
	// runtime aggregate instead (llvm.Undef + CreateInsertValue), the same
	// approach genStringConcat/genMakeCall already use for their own
	// multi-field runtime-computed aggregate results.
	result := llvm.Undef(g.funcValTy)
	result = g.builder.CreateInsertValue(result, fn, 0, "")
	result = g.builder.CreateInsertValue(result, raw, 1, "")
	return result
}

// genLambdaFunc lowers n's (a FuncLit's) own body as a real, independent,
// top-level LLVM function with a synthesized, collision-free name
// ("llvm_lang.lambda.N", g.lambdaCounter - see CODEGEN.md's "Lambdas"
// section), and returns that function's own address.
//
// Its real LLVM signature always takes ctxPtr as a genuine first parameter,
// regardless of whether captures is empty - unlike an ordinary free
// function's signature (declareFuncSignature), which never has one at all -
// because a lambda is always called *indirectly*, through the fat-pointer
// representation (there's no way to call a FuncLit "directly" the way a
// statically-named free function can be - see isDirectFuncCall, which a
// FuncLit callee never matches), and every indirect call
// (genIndirectCall) now uniformly passes ctxPtr as a real argument no matter
// which kind of function value it's calling through (see genFuncThunk's own
// doc comment for the other half of this uniform-ABI design).
//
// Generating this literal's own body means temporarily replacing every one
// of Generator's per-function-frame fields (curFn/entryBlock/locals/
// loopStack/curFunc/curReceiver/curCtxPtr/curCaptureIndex/curCaptureTy) with
// this literal's own fresh state, and the builder's own current insert
// block with this literal's own entry block - saved in plain local
// variables and restored once this literal's body is fully generated, the
// same save-in-a-local/restore-after-recursing shape sema/typecheck.go's
// checkFuncLit already uses for curFunc, one layer up. Since this is an
// ordinary (non-reentrant-within-itself) function call, Go's own call stack
// already handles arbitrary nesting depth for free - a FuncLit nested inside
// this one simply recurses into genLambdaFunc again, one level deeper,
// saving and restoring the exact same fields around its own body in turn.
func (g *Generator) genLambdaFunc(n ast.NodeIndex, captures []*sema.Symbol, ctxTy llvm.Type) llvm.Value {
	paramListNode := g.tree.FuncLitParamList(n)
	returnTypeNode := g.tree.FuncLitReturnType(n)
	body := g.tree.FuncLitBody(n)
	paramNodes := g.tree.Children(paramListNode)

	retType := sema.Type{Kind: sema.TypeVoid}
	if returnTypeNode != ast.InvalidNode {
		retType = g.info.Types[returnTypeNode]
	}

	paramTypes := make([]llvm.Type, len(paramNodes)+1)
	paramTypes[0] = g.ptrTy
	for i, paramNode := range paramNodes {
		paramTypes[i+1] = g.llvmType(g.info.Types[g.tree.Child(paramNode, 1)])
	}
	fnType := llvm.FunctionType(g.llvmType(retType), paramTypes, false)

	name := fmt.Sprintf("llvm_lang.lambda.%d", g.lambdaCounter)
	g.lambdaCounter++
	fn := llvm.AddFunction(g.mod, name, fnType)
	fn.SetLinkage(llvm.PrivateLinkage)

	savedFn, savedEntry, savedLocals := g.curFn, g.entryBlock, g.locals
	savedLoopStack, savedFunc, savedReceiver := g.loopStack, g.curFunc, g.curReceiver
	savedCtxPtr, savedCaptureIndex, savedCaptureTy := g.curCtxPtr, g.curCaptureIndex, g.curCaptureTy
	savedDestructors := g.destructors
	savedBB := g.builder.GetInsertBlock()

	g.curFn = fn
	g.entryBlock = g.ctx.AddBasicBlock(fn, "entry")
	g.builder.SetInsertPointAtEnd(g.entryBlock)
	g.locals = make(map[*sema.Symbol]llvm.Value)
	g.loopStack = nil
	g.destructors = nil
	g.curReceiver = llvm.Value{}

	g.curCtxPtr = fn.Param(0)
	g.curCaptureTy = ctxTy
	captureIndex := make(map[*sema.Symbol]int, len(captures))
	for i, sym := range captures {
		captureIndex[sym] = i
	}
	g.curCaptureIndex = captureIndex

	for i, paramNode := range paramNodes {
		psym := g.info.Refs[g.tree.Child(paramNode, 0)]
		ptype := g.info.Types[g.tree.Child(paramNode, 1)]
		addr := g.allocLocalSlot(psym, g.llvmType(ptype), psym.Name)
		g.builder.CreateStore(fn.Param(i+1), addr)
		g.locals[psym] = addr
		g.pushDestructorEntry(psym, ptype)
	}

	g.curFunc = &funcCtx{
		isMain:    false,
		hasReturn: returnTypeNode != ast.InvalidNode,
	}

	g.finishBody(body)

	g.curFn, g.entryBlock, g.locals = savedFn, savedEntry, savedLocals
	g.loopStack, g.curFunc, g.curReceiver = savedLoopStack, savedFunc, savedReceiver
	g.curCtxPtr, g.curCaptureIndex, g.curCaptureTy = savedCtxPtr, savedCaptureIndex, savedCaptureTy
	g.destructors = savedDestructors
	g.builder.SetInsertPointAtEnd(savedBB)

	return fn
}

// genBoundsCheck emits a real runtime check that idx (an i32) satisfies
// `0 <= idx < size`, trapping immediately (llvm.trap followed by
// unreachable - see setupRuntime, runtime.go) rather than falling through to
// an out-of-bounds GEP, which would otherwise be silent undefined behavior -
// a read/write through arbitrary memory. See AGENTS.md's "Array bounds
// checking" section.
//
// size is an arbitrary already-computed i32 llvm.Value, not a compile-time
// constant - a fixed-size array's caller (genAddr's IndexExpr case) passes a
// plain ConstInt built from its own compile-time-known Size, exactly as
// before; a dynamic array's caller passes its slice value's actual runtime
// len field instead (see LANGUAGE.md's "Dynamic arrays" section) - this
// function itself needs no change at all to serve both, since an LLVM
// ICmp/CondBr already works identically over a constant or a runtime value.
//
// Structurally the same CreateCondBr/AddBasicBlock shape genIfStmt/
// genForStmt/genShortCircuit already use elsewhere in this package: a
// condition, a taken ("trap") block, and a not-taken ("continue") block.
// Leaves the builder positioned at the end of the continue block, so a
// caller simply keeps emitting the actual GEP/load/store right after this
// call, exactly as if no check had run at all.
func (g *Generator) genBoundsCheck(idx, size llvm.Value) {
	zero := llvm.ConstInt(g.i32Ty, 0, true)
	geZero := g.builder.CreateICmp(llvm.IntSGE, idx, zero, "")
	ltSize := g.builder.CreateICmp(llvm.IntSLT, idx, size, "")
	inBounds := g.builder.CreateAnd(geZero, ltSize, "")

	trapBB := g.ctx.AddBasicBlock(g.curFn, "idx.trap")
	okBB := g.ctx.AddBasicBlock(g.curFn, "idx.ok")
	g.builder.CreateCondBr(inBounds, okBB, trapBB)

	g.builder.SetInsertPointAtEnd(trapBB)
	g.builder.CreateCall(g.trapType, g.trapFn, nil, "")
	g.builder.CreateUnreachable()

	g.builder.SetInsertPointAtEnd(okBB)
}

// genSliceRangeCheck emits a real runtime check that low/high (i32 values)
// satisfy `0 <= low <= high <= max` - the range-check counterpart to
// genBoundsCheck's single-index check (see CODEGEN.md's "Slicing" section),
// trapping immediately (llvm.trap + unreachable, same mechanism/convention
// as genBoundsCheck/genMakeSizeCheck) rather than ever building a slice
// header from an out-of-range pair.
//
// max is an arbitrary already-computed i32 llvm.Value, not necessarily a
// compile-time constant, mirroring genBoundsCheck's own size parameter: a
// dynamic array's caller passes its own runtime cap field (a reslice may
// extend into spare capacity beyond the current length - see LANGUAGE.md's
// "Slicing" section), a string's caller passes its own runtime len field
// (strings have no separate capacity), and a fixed-size array's caller
// passes a plain ConstInt built from its own compile-time-known Size.
func (g *Generator) genSliceRangeCheck(low, high, max llvm.Value) {
	zero := llvm.ConstInt(g.i32Ty, 0, true)
	lowNonNeg := g.builder.CreateICmp(llvm.IntSGE, low, zero, "")
	lowLEHigh := g.builder.CreateICmp(llvm.IntSLE, low, high, "")
	highLEMax := g.builder.CreateICmp(llvm.IntSLE, high, max, "")
	ok := g.builder.CreateAnd(lowNonNeg, lowLEHigh, "")
	ok = g.builder.CreateAnd(ok, highLEMax, "")

	trapBB := g.ctx.AddBasicBlock(g.curFn, "slice.trap")
	okBB := g.ctx.AddBasicBlock(g.curFn, "slice.ok")
	g.builder.CreateCondBr(ok, okBB, trapBB)

	g.builder.SetInsertPointAtEnd(trapBB)
	g.builder.CreateCall(g.trapType, g.trapFn, nil, "")
	g.builder.CreateUnreachable()

	g.builder.SetInsertPointAtEnd(okBB)
}

// genSliceBounds evaluates a SliceExpr's own low/high child nodes (either may
// be ast.InvalidNode - see ast.Node's own SliceExpr doc comment), defaulting
// an omitted low to 0 and an omitted high to defaultHigh, then range-checks
// the resolved pair against max (genSliceRangeCheck) before handing both
// back. Every one of the three slicing paths below (string/dynamic array/
// fixed array) shares this exact "resolve defaults, then range-check" shape -
// they differ only in what defaultHigh/max actually are (see each call site):
// a dynamic array's high defaults to its own runtime len but range-checks
// against its runtime cap (LANGUAGE.md's "Slicing" section - a reslice may
// extend into spare capacity); a string's/fixed array's default and max are
// the same value (len, or the compile-time-known N), since neither has a
// separate capacity concept.
func (g *Generator) genSliceBounds(lowNode, highNode ast.NodeIndex, defaultHigh, max llvm.Value) (low, high llvm.Value) {
	low = llvm.ConstInt(g.i32Ty, 0, true)
	if lowNode != ast.InvalidNode {
		low = g.genExpr(lowNode)
	}
	high = defaultHigh
	if highNode != ast.InvalidNode {
		high = g.genExpr(highNode)
	}
	g.genSliceRangeCheck(low, high, max)
	return low, high
}

// genSliceExpr lowers a Go-style slice expression `s[a:b]`/`s[:b]`/`s[a:]`/
// `s[:]` (see LANGUAGE.md's "Slicing" section) - dispatching on the operand's
// own already-resolved sema.Type to one of three lowering paths, exactly
// mirroring sema's own checkSliceExpr dispatch.
func (g *Generator) genSliceExpr(n ast.NodeIndex) llvm.Value {
	objNode := g.tree.Child(n, 0)
	lowNode := g.tree.Child(n, 1)
	highNode := g.tree.Child(n, 2)
	objType := g.info.Types[objNode]

	switch {
	case objType.Kind == sema.TypeString:
		return g.genStringSlice(objNode, lowNode, highNode)
	case objType.Kind == sema.TypeArray && objType.Dynamic:
		return g.genDynArraySlice(objNode, objType, lowNode, highNode)
	case objType.Kind == sema.TypeArray:
		return g.genFixedArraySlice(objNode, objType, lowNode, highNode)
	default:
		// Only a string, dynamic array, or fixed-size array reach here on a
		// tree that already passed sema.Check (see checkSliceExpr,
		// sema/typecheck.go, and the package doc comment).
		panic("codegen: genSliceExpr reached an unsupported operand type " + objType.String())
	}
}

// genStringSlice lowers `s[a:b]` for a string operand: a fresh {ptr, len}
// value sharing s's own backing bytes (GEP'd forward by low), never copied -
// see LANGUAGE.md's "Slicing" section and "string representation" (both
// CODEGEN.md and LANGUAGE.md). A string has no separate capacity concept, so
// its own runtime len field serves as both the omitted-high default and the
// range check's own upper bound.
func (g *Generator) genStringSlice(objNode, lowNode, highNode ast.NodeIndex) llvm.Value {
	sv := g.genExpr(objNode)
	ptr := g.builder.CreateExtractValue(sv, 0, "")
	length := g.builder.CreateExtractValue(sv, 1, "")

	low, high := g.genSliceBounds(lowNode, highNode, length, length)

	newPtr := g.builder.CreateInBoundsGEP(g.i8Ty, ptr, []llvm.Value{low}, "")
	newLen := g.builder.CreateSub(high, low, "")

	result := llvm.Undef(g.stringTy)
	result = g.builder.CreateInsertValue(result, newPtr, 0, "")
	result = g.builder.CreateInsertValue(result, newLen, 1, "")
	return result
}

// genDynArraySlice lowers `s[a:b]` for a dynamic-array (`[]T`) operand: a
// fresh {ptr, len, cap} value sharing s's own backing buffer (GEP'd forward
// by low, using T's own LLVM element type), never copied. Per LANGUAGE.md's
// "Slicing" section, the omitted-high default is s's own runtime *len* (not
// cap - matching Go's real `s[a:]` rule exactly), but the range check's own
// upper bound is s's runtime *cap* - a reslice is allowed to extend into
// spare capacity beyond the current length, which is exactly what makes
// Go's own slice-growth idioms work.
func (g *Generator) genDynArraySlice(objNode ast.NodeIndex, objType sema.Type, lowNode, highNode ast.NodeIndex) llvm.Value {
	sv := g.genExpr(objNode)
	ptr := g.builder.CreateExtractValue(sv, 0, "")
	length := g.builder.CreateExtractValue(sv, 1, "")
	capacity := g.builder.CreateExtractValue(sv, 2, "")

	low, high := g.genSliceBounds(lowNode, highNode, length, capacity)

	elemLLType := g.llvmType(*objType.Elem)
	newPtr := g.builder.CreateInBoundsGEP(elemLLType, ptr, []llvm.Value{low}, "")
	newLen := g.builder.CreateSub(high, low, "")
	newCap := g.builder.CreateSub(capacity, low, "")

	result := llvm.Undef(g.dynArrTy)
	result = g.builder.CreateInsertValue(result, newPtr, 0, "")
	result = g.builder.CreateInsertValue(result, newLen, 1, "")
	result = g.builder.CreateInsertValue(result, newCap, 2, "")
	return result
}

// genFixedArraySlice lowers `arr[a:b]` for a fixed-size-array (`[N]T`)
// operand into a genuine dynamic array (`[]T`), matching Go's own real
// behavior (see LANGUAGE.md's "Slicing" section) - sema's own
// checkArraySliceAddressable already guaranteed objNode is addressable, so
// this can take its real address (genAddr, the same helper `&`/a method
// receiver/an ordinary index already use) and alias directly into it, the
// same {ptr, len, cap} construction genDynArraySlice uses, just built from
// N (the array's own compile-time-known Size - both the omitted-high default
// and the range check's own upper bound, since a fixed array has no separate
// capacity concept of its own either) instead of a runtime len/cap pair.
func (g *Generator) genFixedArraySlice(objNode ast.NodeIndex, objType sema.Type, lowNode, highNode ast.NodeIndex) llvm.Value {
	base := g.genAddr(objNode)
	sizeConst := llvm.ConstInt(g.i32Ty, uint64(objType.Size), true)

	low, high := g.genSliceBounds(lowNode, highNode, sizeConst, sizeConst)

	arrType := g.llvmType(objType)
	zero := llvm.ConstInt(g.i32Ty, 0, false)
	newPtr := g.builder.CreateInBoundsGEP(arrType, base, []llvm.Value{zero, low}, "")
	newLen := g.builder.CreateSub(high, low, "")
	newCap := g.builder.CreateSub(sizeConst, low, "")

	result := llvm.Undef(g.dynArrTy)
	result = g.builder.CreateInsertValue(result, newPtr, 0, "")
	result = g.builder.CreateInsertValue(result, newLen, 1, "")
	result = g.builder.CreateInsertValue(result, newCap, 2, "")
	return result
}

// genExpr lowers n to its rvalue.
func (g *Generator) genExpr(n ast.NodeIndex) llvm.Value {
	switch g.tree.Nodes[n].Kind {
	case enums.NodeKinds.Ident:
		// A bare, uncalled reference to a declared free function (`add`,
		// not `add(...)`) has no storage location to load from - genAddr
		// would panic on it (see its own Ident case) - so it's built
		// directly as a fat-pointer value instead. `nil` (see LANGUAGE.md's
		// "Pointers" section) is the same story - a predeclared value with
		// no storage of its own (see sema/scope.go's universeScope) - and
		// lowers to a plain null pointer constant regardless of which
		// concrete *T sema resolved it to (genAddr's fallback would
		// otherwise try to spill it into a temp the same as any other
		// rvalue, which would still work but is needless indirection for
		// what's always just a constant). Every other Ident (a var/param)
		// still goes through the ordinary addr+load path.
		switch sym := g.info.Refs[n]; sym.Kind {
		case sema.SymFunc:
			return g.genFuncValue(sym)
		case sema.SymBuiltinValue:
			return llvm.ConstNull(g.ptrTy)
		}
		return g.genLoad(n)
	case enums.NodeKinds.MemberExpr, enums.NodeKinds.IndexExpr:
		return g.genLoad(n)
	case enums.NodeKinds.ThisExpr:
		// `this` is typed *T now (checkThisExpr, sema/typecheck.go), matching
		// what it already is at this level: the receiver parameter itself, a
		// real pointer value with no alloca/storage of its own (see genAddr's
		// own ThisExpr case, and CODEGEN.md's "Method receivers" section).
		// genLoad's generic "genAddr then load" shape assumes its address is
		// storage holding the value - true for an Ident/MemberExpr/IndexExpr,
		// but not for `this`: genAddr(ThisExpr) already returns g.curReceiver
		// (the pointer value itself), so loading through it again would
		// dereference one indirection too many, reading the receiver's own
		// first field's bytes as if they were the pointer. A bare `this` used
		// as an ordinary value (`return this`, `x := this`, an argument) just
		// needs that same pointer value directly, no load at all.
		return g.curReceiver
	case enums.NodeKinds.SliceExpr:
		return g.genSliceExpr(n)
	case enums.NodeKinds.NumberLit:
		return g.genNumberLit(n)
	case enums.NodeKinds.StringLit:
		return g.constStringValue(g.tree.File.StringValue(g.tree.Nodes[n].Tok))
	case enums.NodeKinds.BoolLit:
		return g.genBoolLit(n)
	case enums.NodeKinds.ParenExpr:
		return g.genExpr(g.tree.Child(n, 0))
	case enums.NodeKinds.UnaryExpr:
		return g.genUnaryExpr(n)
	case enums.NodeKinds.BinaryExpr:
		return g.genBinaryExpr(n)
	case enums.NodeKinds.CallExpr:
		return g.genCallExpr(n)
	case enums.NodeKinds.CompositeLit:
		t := g.llvmType(g.info.Types[n])
		tmp := g.createEntryAlloca(t, "lit")
		g.genCompositeLitInto(tmp, n)
		return g.builder.CreateLoad(t, tmp, "")
	case enums.NodeKinds.FuncLit:
		return g.genFuncLit(n)
	case enums.NodeKinds.NewExpr:
		return g.genNewExpr(n)
	default:
		panic("codegen: cannot generate an expression of kind " + g.tree.Nodes[n].Kind.String())
	}
}

// genNumberLit lowers n to its already-resolved concrete numeric constant.
// sema's untyped-constant resolution (see AGENTS.md's Types section) always
// pins info.Types[n] to one of the six concrete numeric kinds by the time a
// checked tree reaches codegen - never one of the two untyped bookkeeping
// kinds - so this only ever needs to pick the matching integer/float
// constructor for that kind's width.
func (g *Generator) genNumberLit(n ast.NodeIndex) llvm.Value {
	t := g.info.Types[n]
	text := g.tree.Text(n)
	switch t.Kind {
	case sema.TypeI8:
		return g.genIntLit(n, text, g.i8Ty, 8)
	case sema.TypeI16:
		return g.genIntLit(n, text, g.i16Ty, 16)
	case sema.TypeI32:
		return g.genIntLit(n, text, g.i32Ty, 32)
	case sema.TypeI64:
		return g.genIntLit(n, text, g.i64Ty, 64)
	case sema.TypeF32:
		return g.genFloatLit(text, g.f32Ty)
	case sema.TypeF64:
		return g.genFloatLit(text, g.f64Ty)
	default:
		panic("codegen: genNumberLit reached a non-numeric type " + t.String())
	}
}

// genIntLit parses text (n's literal text, always plain decimal digits - the
// lexer's exponent/decimal-point syntax means a *float*-kind literal, handled
// by genFloatLit instead) into a bits-wide signed integer constant. An
// out-of-range literal for that width still gets a codegen diagnostic rather
// than silently wrapping, since sema itself never checks literal range.
func (g *Generator) genIntLit(n ast.NodeIndex, text string, llt llvm.Type, bits int) llvm.Value {
	v, err := strconv.ParseInt(text, 10, bits)
	if err != nil {
		g.errorAt(n, "integer literal %q is out of range for a %d-bit int", text, bits)
		return llvm.ConstInt(llt, 0, true)
	}
	return llvm.ConstInt(llt, uint64(v), true)
}

// genFloatLit parses text (a decimal-point/exponent literal) into a
// constant of the given float type (f32 or f64) - ConstFloat takes a Go
// float64 regardless of the target LLVM type's own width, truncating as
// needed for f32.
func (g *Generator) genFloatLit(text string, llt llvm.Type) llvm.Value {
	v, err := strconv.ParseFloat(text, 64)
	if err != nil {
		// sema already validated this is a real numeric literal - reaching
		// a parse failure here would mean the lexer accepted something
		// strconv can't parse, which shouldn't happen on a checked tree.
		return llvm.ConstFloat(llt, 0)
	}
	return llvm.ConstFloat(llt, v)
}

func (g *Generator) genBoolLit(n ast.NodeIndex) llvm.Value {
	return g.constBool(g.tree.Nodes[n].Tok.Keyword == enums.Keywords.True)
}

// genUnaryExpr lowers `-`/`!`/`&`/`*`. `&`/`*` (see LANGUAGE.md's "Pointers"
// section) are handled before the operand is evaluated as a plain rvalue:
// `&x` needs x's *address*, not its value (genAddr - the same address a
// plain assignment to x would compute), and `*p` needs p's value (the
// pointer itself) loaded through, not p's own address.
func (g *Generator) genUnaryExpr(n ast.NodeIndex) llvm.Value {
	operand := g.tree.Child(n, 0)
	switch g.tree.Text(n) {
	case "&":
		return g.genAddr(operand)
	case "*":
		ptr := g.genExpr(operand)
		return g.builder.CreateLoad(g.llvmType(g.info.Types[n]), ptr, "")
	}

	v := g.genExpr(operand)
	switch g.tree.Text(n) {
	case "-":
		if g.info.Types[operand].IsFloatKind() {
			return g.builder.CreateFNeg(v, "")
		}
		return g.builder.CreateNeg(v, "")
	case "!":
		return g.builder.CreateNot(v, "")
	default:
		panic("codegen: unsupported unary operator " + g.tree.Text(n))
	}
}

// genBinaryExpr lowers every binary operator AGENTS.md's Operators section
// documents. `&&`/`||` short-circuit via genShortCircuit (their right
// operand must not evaluate when the left already decides the result -
// important once it can have side effects, e.g. a call); every other
// operator evaluates both operands eagerly. A numeric operator dispatches to
// the matching float instruction (CreateFAdd/CreateFCmp/...) whenever the
// left operand's already-resolved concrete type (sema guarantees both
// operands share one - see AGENTS.md's Types section) is a float kind;
// otherwise the existing integer instructions apply directly, unchanged
// across every int width (an LLVM integer instruction is generic over bit
// width as long as both operands share the same LLVM type, which sema
// already guarantees here).
func (g *Generator) genBinaryExpr(n ast.NodeIndex) llvm.Value {
	op := g.tree.Text(n)
	lNode := g.tree.Child(n, 0)
	rNode := g.tree.Child(n, 1)

	if op == "&&" || op == "||" {
		return g.genShortCircuit(op, lNode, rNode)
	}

	lv := g.genExpr(lNode)
	rv := g.genExpr(rNode)
	lt := g.info.Types[lNode]
	isFloat := lt.IsFloatKind()

	switch op {
	case "+", "-", "*", "/":
		if op == "+" && lt.Kind == sema.TypeString {
			return g.genStringConcat(lv, rv)
		}
		return g.genArithOp(op, lv, rv, isFloat)
	case "%":
		return g.builder.CreateSRem(lv, rv, "")
	case "&":
		return g.builder.CreateAnd(lv, rv, "")
	case "|":
		return g.builder.CreateOr(lv, rv, "")
	case "^":
		return g.builder.CreateXor(lv, rv, "")
	case "==":
		switch {
		case lt.Kind == sema.TypeString:
			return g.genStringEqual(lv, rv, true)
		case lt.Kind == sema.TypeStruct, lt.Kind == sema.TypeArray:
			return g.genValueEqual(lt, lv, rv)
		case isFloat:
			return g.builder.CreateFCmp(llvm.FloatOEQ, lv, rv, "")
		default:
			return g.builder.CreateICmp(llvm.IntEQ, lv, rv, "")
		}
	case "!=":
		switch {
		case lt.Kind == sema.TypeString:
			return g.genStringEqual(lv, rv, false)
		case lt.Kind == sema.TypeStruct, lt.Kind == sema.TypeArray:
			return g.builder.CreateNot(g.genValueEqual(lt, lv, rv), "")
		case isFloat:
			return g.builder.CreateFCmp(llvm.FloatUNE, lv, rv, "")
		default:
			return g.builder.CreateICmp(llvm.IntNE, lv, rv, "")
		}
	case "<", "<=", ">", ">=":
		if lt.Kind == sema.TypeString {
			return g.genStringOrder(op, lv, rv)
		}
		if isFloat {
			return g.genFloatOrder(op, lv, rv)
		}
		return g.genIntOrder(op, lv, rv)
	default:
		panic("codegen: unsupported binary operator " + op)
	}
}

// genArithOp lowers `+ - * /` for an already-evaluated operand pair (lv, rv),
// dispatching to the matching float instruction whenever isFloat - shared by
// genBinaryExpr, genAssignStmt's compound-assignment cases (`+= -= *= /=`),
// and genIncDecStmt (`++`/`--`, which only ever passes "+"/"-"), so this
// float-vs-int dispatch lives in exactly one place instead of three
// hand-written copies of the same switch (see AGENTS.md's Architecture
// section). String concatenation (`+`'s other overload) isn't handled here -
// every caller special-cases it first, since only some of them (genBinaryExpr,
// genAssignStmt's `+=`) ever see a string operand at all.
func (g *Generator) genArithOp(op string, lv, rv llvm.Value, isFloat bool) llvm.Value {
	switch op {
	case "+":
		if isFloat {
			return g.builder.CreateFAdd(lv, rv, "")
		}
		return g.builder.CreateAdd(lv, rv, "")
	case "-":
		if isFloat {
			return g.builder.CreateFSub(lv, rv, "")
		}
		return g.builder.CreateSub(lv, rv, "")
	case "*":
		if isFloat {
			return g.builder.CreateFMul(lv, rv, "")
		}
		return g.builder.CreateMul(lv, rv, "")
	case "/":
		if isFloat {
			return g.builder.CreateFDiv(lv, rv, "")
		}
		return g.builder.CreateSDiv(lv, rv, "")
	default:
		panic("codegen: genArithOp called with unsupported operator " + op)
	}
}

// genIntOrder lowers `< <= > >=` for two already-evaluated, same-type signed
// integer operands (any width - see AGENTS.md's Operators section).
func (g *Generator) genIntOrder(op string, lv, rv llvm.Value) llvm.Value {
	switch op {
	case "<":
		return g.builder.CreateICmp(llvm.IntSLT, lv, rv, "")
	case "<=":
		return g.builder.CreateICmp(llvm.IntSLE, lv, rv, "")
	case ">":
		return g.builder.CreateICmp(llvm.IntSGT, lv, rv, "")
	default:
		return g.builder.CreateICmp(llvm.IntSGE, lv, rv, "")
	}
}

// genFloatOrder lowers `< <= > >=` for two already-evaluated, same-type
// float operands (f32 or f64). Uses the *ordered* FCmp predicates (OLT/OLE/
// OGT/OGE) - false whenever either operand is NaN, matching Go's own float
// comparison semantics (see AGENTS.md's Operators section).
func (g *Generator) genFloatOrder(op string, lv, rv llvm.Value) llvm.Value {
	switch op {
	case "<":
		return g.builder.CreateFCmp(llvm.FloatOLT, lv, rv, "")
	case "<=":
		return g.builder.CreateFCmp(llvm.FloatOLE, lv, rv, "")
	case ">":
		return g.builder.CreateFCmp(llvm.FloatOGT, lv, rv, "")
	default:
		return g.builder.CreateFCmp(llvm.FloatOGE, lv, rv, "")
	}
}

// genValueEqual reports whether two already-evaluated struct or array values
// (or, recursively, any field/element type reachable from one - int, string,
// bool, a nested struct, or a nested array) are equal, per Go's own rule: a
// struct equals another of the same type iff every field does, recursively;
// an array equals another iff every element does, recursively (see
// AGENTS.md's Operators section). Always returns "are these equal" - never
// pre-negated (unlike genStringEqual) - a `!=` caller just Nots the result
// once (see genBinaryExpr).
//
// Every field/element access here goes through ExtractValue on the already-
// loaded aggregate value, mirroring genPrintStructValue/genPrintArrayValue
// (runtime.go) rather than genAddr's GEP-based addressing: lv/rv are rvalues
// already sitting in SSA registers (structs/arrays are real LLVM aggregate
// values in this package - see AGENTS.md), not memory locations, so there is
// no address to compute in the first place.
//
// This builds one full comparison that ANDs every field/element's result
// together, rather than short-circuiting on the first unequal one - simpler
// codegen (no extra basic blocks per field/element to wire up), and every
// operand here is a pure value comparison with nothing side-effecting to
// avoid re-evaluating. Short-circuiting would be a reasonable alternative
// (and likely produces tighter codegen for a large early-differing
// aggregate) but isn't required for correctness, so the simpler shape was
// chosen - see AGENTS.md.
func (g *Generator) genValueEqual(t sema.Type, lv, rv llvm.Value) llvm.Value {
	switch t.Kind {
	case sema.TypeInt, sema.TypeBool:
		return g.builder.CreateICmp(llvm.IntEQ, lv, rv, "")
	case sema.TypeString:
		return g.genStringEqual(lv, rv, true)
	case sema.TypeStruct:
		layout := g.structLayouts[t.Struct]
		result := llvm.ConstInt(g.boolTy, 1, false)
		for i, ft := range layout.fieldSemaTypes {
			lf := g.builder.CreateExtractValue(lv, i, "")
			rf := g.builder.CreateExtractValue(rv, i, "")
			result = g.builder.CreateAnd(result, g.genValueEqual(ft, lf, rf), "")
		}
		return result
	case sema.TypeArray:
		result := llvm.ConstInt(g.boolTy, 1, false)
		for i := 0; i < int(t.Size); i++ {
			le := g.builder.CreateExtractValue(lv, i, "")
			re := g.builder.CreateExtractValue(rv, i, "")
			result = g.builder.CreateAnd(result, g.genValueEqual(*t.Elem, le, re), "")
		}
		return result
	default:
		// Only int/string/bool/struct/array Types exist (see sema/types.go),
		// and every one is handled above - unreachable on a tree that
		// already passed sema.Check (see the package doc comment).
		panic("codegen: genValueEqual reached an unsupported type " + t.String())
	}
}

// genShortCircuit lowers `&&`/`||` with real short-circuit control flow (not
// an eager bitwise AND/OR): the right operand's basic block is only reached
// when the left operand hasn't already decided the result.
func (g *Generator) genShortCircuit(op string, lNode, rNode ast.NodeIndex) llvm.Value {
	lv := g.genExpr(lNode)
	startBB := g.builder.GetInsertBlock()

	rhsBB := g.ctx.AddBasicBlock(g.curFn, "sc.rhs")
	contBB := g.ctx.AddBasicBlock(g.curFn, "sc.cont")
	if op == "&&" {
		g.builder.CreateCondBr(lv, rhsBB, contBB)
	} else {
		g.builder.CreateCondBr(lv, contBB, rhsBB)
	}

	g.builder.SetInsertPointAtEnd(rhsBB)
	rv := g.genExpr(rNode)
	rhsEndBB := g.builder.GetInsertBlock()
	g.builder.CreateBr(contBB)

	g.builder.SetInsertPointAtEnd(contBB)
	phi := g.builder.CreatePHI(g.boolTy, "")
	shortVal := llvm.ConstInt(g.boolTy, 0, false)
	if op == "||" {
		shortVal = llvm.ConstInt(g.boolTy, 1, false)
	}
	phi.AddIncoming([]llvm.Value{shortVal, rv}, []llvm.BasicBlock{startBB, rhsEndBB})
	return phi
}

// genCallExpr lowers a call: the predeclared print builtin (see
// isPrintCall), an explicit numeric conversion `T(x)` (see isConversionCall),
// a free function, a method call, or an indirect call through a
// function-typed value (see isDirectFuncCall/genIndirectCall). print returns
// no value (void) - see AGENTS.md's "print builtin" section - so its
// "result" is never used by anything else in a checked tree.
func (g *Generator) genCallExpr(n ast.NodeIndex) llvm.Value {
	children := g.tree.Children(n)
	calleeNode, argNodes := children[0], children[1:]

	if g.isPrintCall(calleeNode) {
		g.genPrintCall(argNodes[0])
		return llvm.Value{}
	}
	if g.isBuiltinCall(calleeNode, "make") {
		return g.genMakeCall(n, argNodes)
	}
	if g.isBuiltinCall(calleeNode, "append") {
		return g.genAppendCall(argNodes)
	}
	if g.isBuiltinCall(calleeNode, "len") {
		return g.genLenCall(argNodes[0])
	}
	if g.isConstructorCall(calleeNode) {
		return g.genConstructorCall(calleeNode, argNodes)
	}
	if g.isConversionCall(calleeNode) {
		return g.genConversion(n, argNodes[0])
	}
	switch {
	case g.tree.Nodes[calleeNode].Kind == enums.NodeKinds.MemberExpr && g.isPackageQualifiedCall(calleeNode):
		// `mathutils.Add(...)` - a plain direct call to a free function
		// exported from another package (see LANGUAGE.md's "Imports"
		// section), already resolved straight to its own *sema.Symbol by
		// sema (Info.Refs[calleeNode] - see resolve.go's
		// resolvePackageMemberExpr). There is no receiver to compute here,
		// unlike an ordinary method call - genFuncCall is exactly the same
		// direct-call lowering a same-package free function call already
		// uses.
		return g.genFuncCall(calleeNode, argNodes)
	case g.isMethodCall(calleeNode):
		return g.genMethodCall(calleeNode, argNodes)
	case g.isDirectFuncCall(calleeNode):
		return g.genFuncCall(calleeNode, argNodes)
	default:
		return g.genIndirectCall(calleeNode, argNodes)
	}
}

// isPackageQualifiedCall reports whether calleeNode (a MemberExpr) is a
// package-qualified function call - its object is a bare Ident resolving
// (via Info.Refs) to a SymPackage symbol (an import binding), as opposed to
// an ordinary struct-value method call. Mirrors sema's own
// memberObjectIsPackage (sema/typecheck.go) exactly, for the identical
// reason isDirectFuncCall/isConversionCall already mirror their own sema
// counterparts - see CODEGEN.md.
func (g *Generator) isPackageQualifiedCall(calleeNode ast.NodeIndex) bool {
	objNode := g.tree.Child(calleeNode, 0)
	if g.tree.Nodes[objNode].Kind != enums.NodeKinds.Ident {
		return false
	}
	sym, ok := g.info.Refs[objNode]
	return ok && sym.Kind == sema.SymPackage
}

// isMethodCall reports whether calleeNode (a MemberExpr, already known not
// to be package-qualified) is a real method call - its resolved member
// (Info.Refs[calleeNode], set by sema's own resolveMember) is a SymFunc, not
// an ordinary struct field. Mirrors sema's own methodSigForCallee dispatch
// (sema/typecheck.go) exactly, for the identical reason isDirectFuncCall/
// isConversionCall already mirror their own sema counterparts - see
// CODEGEN.md.
//
// A func-typed struct field (`cb.fn(5)` - LANGUAGE.md's "First-class
// functions" section) deliberately fails this check even though it's a
// MemberExpr: it's not a method, so genCallExpr's switch falls through to
// isDirectFuncCall (also false, since calleeNode isn't an Ident) and then to
// genIndirectCall - the exact same fat-pointer-extract-and-call path a
// func-typed Ident/parameter already goes through, since sema type-checks
// and codegen lowers a func-typed field's call exactly like any other
// indirect call, distinguished only by which node kind ultimately holds the
// function value.
func (g *Generator) isMethodCall(calleeNode ast.NodeIndex) bool {
	if g.tree.Nodes[calleeNode].Kind != enums.NodeKinds.MemberExpr {
		return false
	}
	sym, ok := g.info.Refs[calleeNode]
	return ok && sym.Kind == sema.SymFunc
}

// isDirectFuncCall mirrors sema's own dispatch (funcSigForCall in
// sema/typecheck.go): a call's callee compiles to a plain, direct `call`
// instruction with zero fat-pointer/indirect-call overhead only when it's a
// plain Ident resolving (via Info.Refs) to an actual declared free function
// (SymFunc with a real FuncDecl, i.e. Decl != InvalidNode) - the
// predeclared `print` builtin is intercepted earlier by isPrintCall and
// never reaches here on a checked tree, but Decl != InvalidNode still
// guards against it defensively. Anything else that type-checked as
// callable - a function-typed variable/parameter, an ordinary struct field
// of function type (see isMethodCall), or any other expression whose value
// is itself a function (e.g. a call result) - goes through genIndirectCall
// instead (see CODEGEN.md's "First-class functions" section).
func (g *Generator) isDirectFuncCall(calleeNode ast.NodeIndex) bool {
	if g.tree.Nodes[calleeNode].Kind != enums.NodeKinds.Ident {
		return false
	}
	sym, ok := g.info.Refs[calleeNode]
	return ok && sym.Kind == sema.SymFunc && sym.Decl != ast.InvalidNode
}

// genIndirectCall lowers a call through a function-typed value - a
// variable/parameter holding a function reference, or any other expression
// whose value is itself a function (e.g. one returned from another call).
// Unlike a direct call (genFuncCall, which looks the callee's LLVM function
// straight up in g.funcs), this actually evaluates calleeNode as an
// ordinary value expression to get its fat-pointer {fnPtr, ctxPtr}
// representation (see genFuncValue/genFuncLit and CODEGEN.md's "Lambdas"
// section), extracts both fnPtr and ctxPtr, and builds the llvm.FunctionType
// to call through fnPtr directly from the callee's own sema.Type (Params/
// Return) - there's no FuncDecl node backing an indirect callee the way
// genFuncCall's funcEntry lookup relies on.
//
// ctxPtr is now always passed along as fnPtr's own real first argument -
// this is the one change this round made to this function, and it's what
// makes an indirect call correctly polymorphic over which of the two kinds
// of function value it's actually holding at runtime: fnPtr is always either
// a free function's own memoized thunk (genFuncThunk) or a genuine lambda's
// own synthesized function (genLambdaFunc), and both now share the exact
// same real, uniform ctxPtr-first signature - so calling through either one
// this same way is always valid, well-typed IR, never a mismatched-signature
// call. A *direct* call (genFuncCall) never reaches this function at all and
// is completely unaffected - it keeps calling a statically-known function's
// own real (ctxPtr-less) signature directly, exactly as before.
func (g *Generator) genIndirectCall(calleeNode ast.NodeIndex, argNodes []ast.NodeIndex) llvm.Value {
	fnVal := g.genExpr(calleeNode)
	fnPtr := g.builder.CreateExtractValue(fnVal, 0, "")
	ctxPtr := g.builder.CreateExtractValue(fnVal, 1, "")

	calleeType := g.info.Types[calleeNode]
	paramTypes := make([]llvm.Type, len(calleeType.Params)+1)
	paramTypes[0] = g.ptrTy
	for i, pt := range calleeType.Params {
		paramTypes[i+1] = g.llvmType(pt)
	}
	fnType := llvm.FunctionType(g.llvmType(*calleeType.Return), paramTypes, false)

	args := make([]llvm.Value, len(argNodes)+1)
	args[0] = ctxPtr
	for i, a := range argNodes {
		args[i+1] = g.genExpr(a)
	}
	return g.builder.CreateCall(fnType, fnPtr, args, "")
}

// isConstructorCall mirrors sema's own recognition of `Name(args)` as
// constructing a struct via one of its declared constructors (see
// LANGUAGE.md's "Constructors" section and sema's checkConstructorCall) -
// the callee (an Ident for a same-package struct type, or a MemberExpr for a
// package-qualified one) resolves, via Info.Refs, directly to the specific
// constructor Symbol sema already selected by argument count - not merely
// "the struct type", the way a bare, uncalled struct-type reference or an
// unmatched conversion call's callee still would.
func (g *Generator) isConstructorCall(calleeNode ast.NodeIndex) bool {
	sym, ok := g.info.Refs[calleeNode]
	return ok && sym.Kind == sema.SymConstructor
}

// genConstructorCall lowers `Name(args)`: allocate a fresh stack slot for
// the struct being built - the same alloca-then-load approach a struct
// composite literal already uses (genAddr's CompositeLit case) - call the
// selected constructor, reusing the exact same implicit-first-pointer-
// parameter convention an ordinary method call already uses (genMethodCall,
// and CODEGEN.md's "Method receivers" section) with the alloca's own address
// as the constructor's `this`, then load and return the now-populated value,
// matching how this package already returns struct/array/string values by
// value elsewhere (see CODEGEN.md's "Structs/arrays/strings are passed and
// returned as real LLVM aggregate types" section).
func (g *Generator) genConstructorCall(calleeNode ast.NodeIndex, argNodes []ast.NodeIndex) llvm.Value {
	sym := g.info.Refs[calleeNode]
	layout := g.structLayouts[sym.StructInfo]

	dst := g.createEntryAlloca(layout.llvmType, "ctor")
	g.genConstructorCallInto(dst, calleeNode, argNodes)
	return g.builder.CreateLoad(layout.llvmType, dst, "")
}

// genConstructorCallInto runs calleeNode's selected constructor (see
// isConstructorCall/genConstructorCall) against an already-addressed
// destination, rather than always allocating its own fresh stack slot -
// factored out of genConstructorCall so genNewExpr (see LANGUAGE.md's
// "Pointers" section) can run the exact same constructor-call lowering
// against a real malloc'd address instead, without duplicating the
// this-pointer-as-hidden-first-argument convention both share (see
// AGENTS.md's "Method receivers" section - a constructor call and a method
// call use the identical calling convention).
func (g *Generator) genConstructorCallInto(dst llvm.Value, calleeNode ast.NodeIndex, argNodes []ast.NodeIndex) {
	sym := g.info.Refs[calleeNode]
	entry := g.ctors[sym]

	args := make([]llvm.Value, len(argNodes)+1)
	args[0] = dst
	for i, a := range argNodes {
		args[i+1] = g.genExpr(a)
	}
	g.builder.CreateCall(entry.fnType, entry.fn, args, "")
}

// genNewExpr lowers `new T(args)`/`new T{...}` (see LANGUAGE.md's "Pointers"
// section): mallocs exactly sizeof(T) bytes on a real, genuinely freeable
// heap - a separate heap from the bump-allocator arena string concatenation/
// dynamic arrays use (see runtime.go's setupRuntime and BLOCKERS.md) - then
// initializes it in place, reusing the exact same constructor-call
// (genConstructorCallInto) or composite-literal (genCompositeLitInto)
// lowering an ordinary stack- or field-allocated `T(args)`/`T{...}` already
// uses; sema's checkNewExpr already restricted inner to exactly those two
// shapes, so this needs no further validation of its own. Returns the
// malloc'd pointer directly, never loading through it - unlike
// genConstructorCall/a plain CompositeLit's own genExpr case (which both
// return the constructed value itself, since this language passes structs by
// value everywhere else - see AGENTS.md's codegen section), `new`'s entire
// point is to hand back a pointer to a heap allocation that outlives the
// current stack frame.
func (g *Generator) genNewExpr(n ast.NodeIndex) llvm.Value {
	inner := g.tree.Child(n, 0)
	elemType := *g.info.Types[n].Elem
	llt := g.llvmType(elemType)
	size := llvm.SizeOf(llt)
	ptr := g.builder.CreateCall(g.mallocType, g.mallocFn, []llvm.Value{size}, "")

	switch g.tree.Nodes[inner].Kind {
	case enums.NodeKinds.CompositeLit:
		g.genCompositeLitInto(ptr, inner)
	case enums.NodeKinds.CallExpr:
		children := g.tree.Children(inner)
		g.genConstructorCallInto(ptr, children[0], children[1:])
	default:
		panic("codegen: new wrapping an unsupported expression kind " + g.tree.Nodes[inner].Kind.String())
	}
	return ptr
}

// isConversionCall mirrors sema's own recognition of `T(x)` as an explicit
// numeric conversion rather than an ordinary call (see
// sema/typecheck.go's checkConversionCall): the callee is a plain Ident
// whose Info.Refs resolution is a type symbol, not a function. On a tree
// that already passed sema.Check, this can only ever be SymBuiltinType - a
// non-numeric conversion target (SymStruct) would already have been
// rejected by sema and so can never reach codegen at all (see the package
// doc comment).
func (g *Generator) isConversionCall(calleeNode ast.NodeIndex) bool {
	if g.tree.Nodes[calleeNode].Kind != enums.NodeKinds.Ident {
		return false
	}
	sym, ok := g.info.Refs[calleeNode]
	return ok && sym.Kind == sema.SymBuiltinType
}

// genConversion lowers a validated numeric conversion `T(x)`: sema has
// already confirmed both the argument's and the call's own (target) types
// are numeric and recorded the target as the CallExpr node's own Type in
// info.Types (checkConversionCall), so this reads both ends straight out of
// info.Types with no re-derivation of its own. A same-Kind "conversion"
// (`i32(someI32Value)`) passes the value through unchanged rather than
// emitting a pointless instruction; otherwise the correct LLVM conversion
// instruction is chosen for the source/target pair - sign-extend for a
// wider integer, truncate for a narrower one (every integer here is signed -
// see AGENTS.md's Types section), int-to-float/float-to-int via
// CreateSIToFP/CreateFPToSI, and extend/truncate between float widths via
// CreateFPExt/CreateFPTrunc.
func (g *Generator) genConversion(n, argNode ast.NodeIndex) llvm.Value {
	from := g.info.Types[argNode]
	to := g.info.Types[n]
	v := g.genExpr(argNode)

	if from.Kind == to.Kind {
		return v
	}
	toLLT := g.llvmType(to)

	switch {
	case from.IsIntegerKind() && to.IsIntegerKind():
		if to.Bits() > from.Bits() {
			return g.builder.CreateSExt(v, toLLT, "")
		}
		return g.builder.CreateTrunc(v, toLLT, "")
	case from.IsIntegerKind() && to.IsFloatKind():
		return g.builder.CreateSIToFP(v, toLLT, "")
	case from.IsFloatKind() && to.IsIntegerKind():
		return g.builder.CreateFPToSI(v, toLLT, "")
	case from.IsFloatKind() && to.IsFloatKind():
		if to.Bits() > from.Bits() {
			return g.builder.CreateFPExt(v, toLLT, "")
		}
		return g.builder.CreateFPTrunc(v, toLLT, "")
	default:
		panic("codegen: genConversion reached a non-numeric type pair on a checked tree")
	}
}

func (g *Generator) genFuncCall(calleeNode ast.NodeIndex, argNodes []ast.NodeIndex) llvm.Value {
	sym := g.info.Refs[calleeNode]
	entry := g.funcs[sym]
	args := make([]llvm.Value, len(argNodes))
	for i, a := range argNodes {
		args[i] = g.genExpr(a)
	}
	return g.builder.CreateCall(entry.fnType, entry.fn, args, "")
}

// genMethodCall lowers `p.move(...)`: the receiver's address (not its
// loaded value - see AGENTS.md, every method is implicitly by-reference)
// becomes the call's hidden first argument. genReceiverAddr auto-derefs a
// pointer-typed receiver (`ptr.move(...)` where ptr is `*Point` - see
// LANGUAGE.md's "Pointers" section), so this needs no awareness of the
// distinction itself.
func (g *Generator) genMethodCall(calleeNode ast.NodeIndex, argNodes []ast.NodeIndex) llvm.Value {
	objNode := g.tree.Child(calleeNode, 0)
	sym := g.info.Refs[calleeNode]
	entry := g.funcs[sym]

	receiverAddr, _ := g.genReceiverAddr(objNode)
	args := make([]llvm.Value, len(argNodes)+1)
	args[0] = receiverAddr
	for i, a := range argNodes {
		args[i+1] = g.genExpr(a)
	}
	return g.builder.CreateCall(entry.fnType, entry.fn, args, "")
}

// genCompositeLitInto fills dst (an already-addressed struct or array
// destination) field-by-field/element-by-element, rather than building a
// temporary aggregate and copying it - the same "fill the known destination
// directly" approach storeValueInto uses for a var-decl/assignment.
//
// A struct literal zero-fills every field first: a positional literal
// always supplies exactly one value per field (sema requires it), but a
// keyed literal may leave some unmentioned (see AGENTS.md/sema - it's not
// required to name every field), and those need a real zero value, not
// whatever garbage the destination's memory already held.
func (g *Generator) genCompositeLitInto(dst llvm.Value, n ast.NodeIndex) {
	t := g.info.Types[n]
	_, elems := g.tree.CompositeLitElems(n)

	switch t.Kind {
	case sema.TypeStruct:
		layout := g.structLayouts[t.Struct]
		for i, ft := range layout.fieldTypes {
			addr := g.builder.CreateStructGEP(layout.llvmType, dst, i, "")
			g.builder.CreateStore(llvm.ConstNull(ft), addr)
		}
		keyed := len(elems) > 0 && g.tree.IsKeyedElement(elems[0])
		for i, e := range elems {
			idx, valueNode := g.structLitFieldSlot(layout, e, i, keyed)
			addr := g.builder.CreateStructGEP(layout.llvmType, dst, idx, "")
			g.storeValueInto(addr, valueNode)
		}

	case sema.TypeArray:
		if t.Dynamic {
			g.genDynArrayLitInto(dst, t, elems)
			return
		}
		arrType := g.llvmType(t)
		zero := llvm.ConstInt(g.i32Ty, 0, false)
		for i, e := range elems {
			idx := llvm.ConstInt(g.i32Ty, uint64(i), false)
			addr := g.builder.CreateInBoundsGEP(arrType, dst, []llvm.Value{zero, idx}, "")
			g.storeValueInto(addr, e)
		}
	}
}

// genDynArrayLitInto lowers a slice composite literal (`[]T{1, 2, 3}` - see
// LANGUAGE.md's "Dynamic arrays" section): sugar that allocates a properly-
// sized backing buffer via the same arena path make itself uses
// (genArenaAllocElems), sized to the literal's own element count (known at
// codegen time - unlike make's own n/cap, a composite literal's element
// count is always fixed by how many elements it lexically lists), and fills
// it positionally - mirroring the fixed-size array literal's own element-by-
// element lowering just above, but writing into a fresh heap allocation
// instead of dst's own inline storage, then storing the resulting
// {ptr, count, count} value into dst (a pointer to the dynArrTy struct)
// field-by-field via CreateStructGEP, the same way a struct literal's own
// fields are filled.
func (g *Generator) genDynArrayLitInto(dst llvm.Value, t sema.Type, elems []ast.NodeIndex) {
	elemLLType := g.llvmType(*t.Elem)
	count := llvm.ConstInt(g.i32Ty, uint64(len(elems)), false)

	buf, _, _ := g.genArenaAllocElems(elemLLType, count)
	for i, e := range elems {
		idx := llvm.ConstInt(g.i32Ty, uint64(i), false)
		addr := g.builder.CreateInBoundsGEP(elemLLType, buf, []llvm.Value{idx}, "")
		g.storeValueInto(addr, e)
	}

	ptrAddr := g.builder.CreateStructGEP(g.dynArrTy, dst, 0, "")
	g.builder.CreateStore(buf, ptrAddr)
	lenAddr := g.builder.CreateStructGEP(g.dynArrTy, dst, 1, "")
	g.builder.CreateStore(count, lenAddr)
	capAddr := g.builder.CreateStructGEP(g.dynArrTy, dst, 2, "")
	g.builder.CreateStore(count, capAddr)
}
