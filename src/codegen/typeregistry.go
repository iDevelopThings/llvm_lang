// This file is the global type registry backing TypeId[T]/TypeIdOf/
// TypeByName/AnyNew (see LANGUAGE.md's "Type registry" section) - the
// compiler-minimal primitives the Any/reflection effort (any.go) builds
// toward, closing it out. Every distinct interned Any descriptor (nominal or
// structural) gets exactly one small integer id, assigned the first time
// it's interned via internDescriptor: eagerly for every primitive
// (setupAnyRuntime) and every declared struct/enum (setupTypeRegistry,
// walked in deterministic tree/declaration order so a struct/enum's own id
// never depends on which function happens to reference it first), lazily
// for an array/map/pointer shape the first time TypeId[T]/TypeIdOf/AnySet[T]
// actually asks about it.
//
// The runtime id -> descriptor array can't be sized until every id has been
// handed out, which for a lazily-interned shape may not happen until partway
// through generating some function's body - so the real backing arrays
// (descRegistry/descRegistryConstructible) are only materialized into LLVM
// constants at the very end (finalizeTypeRegistry, called once after every
// function body already exists). Body codegen that needs to read them before
// then (AnyNew) goes through one level of indirection instead: two small,
// fixed-type globals (typeRegistryDescsGlobal/typeRegistryConstructibleGlobal)
// hold the real array's own address, set once by finalizeTypeRegistry.
package codegen

import (
	"llvm_lang/src/ast"
	"llvm_lang/src/enums"
	"llvm_lang/src/sema"

	"tinygo.org/x/go-llvm"
)

// internDescriptor assigns desc its own registry id, or returns the one
// already assigned - the shared "next free slot" allocator every
// descriptor-interning site in this package goes through. constructible is
// recorded alongside (see the descRegistryConstructible field's own doc
// comment) but never consulted again for an already-interned desc, so a
// caller re-interning the same descriptor with a different constructible
// value (which never legitimately happens - a type's own constructibility
// doesn't change) would silently keep the first answer.
func (g *Generator) internDescriptor(desc llvm.Value, constructible bool) int {
	if id, ok := g.descRegistryIndex[desc]; ok {
		return id
	}
	id := len(g.descRegistry)
	g.descRegistryIndex[desc] = id
	g.descRegistry = append(g.descRegistry, desc)
	g.descRegistryConstructible = append(g.descRegistryConstructible, constructible)
	return id
}

// registryConstructible reports whether t's own registry id may be handed to
// AnyNew - every kind except an enum (BLOCKERS.md's "An enum's zero value..."
// entry: AnyNew must never hand out a new easy path to that landmine) and a
// non-copyable struct/array (the analogous risk for AnyNew specifically: a
// fresh AnyNew instance is a real, distinct value, so constructing one is
// itself sound - see LANGUAGE.md's "move"/fresh-construction precedent - but
// AnySet/AnyAs reading it back out would then perform exactly the implicit
// copy this language otherwise never allows for a non-copyable type).
func (g *Generator) registryConstructible(t sema.Type) bool {
	return t.Kind != sema.TypeEnum && g.codegenCopyable(t)
}

// codegenCopyable mirrors sema's structCopyable/enumCopyable (typecheck.go)
// exactly - non-copyable iff a type declares its own destructor, or any
// field/associated-data type is itself non-copyable, recursively - but
// re-derived here from codegen's own already-resolved StructInfo.Destructor/
// EnumInfo.Destructor and structLayouts/enumLayouts, not sema's own Copyable
// flag: that flag is memoized lazily, forced only for a struct/enum actually
// reached by a real copy-check somewhere in the checked program (see
// IsNonCopyable's own doc comment) - setupTypeRegistry's eager, whole-program
// walk asks about every declared struct/enum regardless of whether the
// program ever copies it, which an unmemoized Copyable would silently (and
// wrongly) answer "non-copyable" for.
func (g *Generator) codegenCopyable(t sema.Type) bool {
	switch t.Kind {
	case sema.TypeStruct:
		if t.Struct == nil {
			return true
		}
		if t.Struct.Destructor != nil {
			return false
		}
		for _, ft := range g.structLayouts[t.Struct].fieldSemaTypes {
			if !g.codegenCopyable(ft) {
				return false
			}
		}
		return true
	case sema.TypeEnum:
		if t.Enum == nil {
			return true
		}
		if t.Enum.Destructor != nil {
			return false
		}
		layout := g.enumLayouts[t.Enum]
		for _, variant := range t.Enum.Order {
			for _, ft := range layout.variantPayloadTypes[variant] {
				if !g.codegenCopyable(ft) {
					return false
				}
			}
		}
		return true
	case sema.TypeArray:
		if t.Dynamic || t.Elem == nil {
			return true
		}
		return g.codegenCopyable(*t.Elem)
	default:
		return true
	}
}

// registryAnyBoxable reports whether t's own Any representation can be
// built at all - re-derived from codegen's own already-built structLayouts/
// enumLayouts, the same "every field/associated-data type must itself be
// boxable, Any nested anywhere is the one exception" rule sema's own
// isBoxableIntoAny/isNestedBoxableIntoAny enforce (typecheck.go), but
// computed here because setupTypeRegistry's eager, whole-program walk asks
// this of every declared struct/enum regardless of whether the program ever
// boxes it - sema's own check only ever runs lazily, per real Any-usage call
// site, so it was never actually run for a struct/enum that's never boxed
// anywhere. A pointer's own pointee doesn't recurse (falls to the default
// case below) - the same cycle-safety a self-referential struct relies on.
func (g *Generator) registryAnyBoxable(t sema.Type) bool {
	switch t.Kind {
	case sema.TypeFunc, sema.TypeCFunc, sema.TypeAny,
		sema.TypeMultiReturn, sema.TypeGenerator, sema.TypeCoroutine:
		return false
	case sema.TypeArray:
		return g.registryAnyBoxable(*t.Elem)
	case sema.TypeStruct:
		if t.Struct == nil {
			return true
		}
		for _, ft := range g.structLayouts[t.Struct].fieldSemaTypes {
			if !g.registryAnyBoxable(ft) {
				return false
			}
		}
		return true
	case sema.TypeEnum:
		if t.Enum == nil {
			return true
		}
		layout := g.enumLayouts[t.Enum]
		for _, variant := range t.Enum.Order {
			for _, ft := range layout.variantPayloadTypes[variant] {
				if !g.registryAnyBoxable(ft) {
					return false
				}
			}
		}
		return true
	default:
		return true
	}
}

// registryIDFor returns t's own registry id, interning its descriptor first
// if this is the first time t's exact shape has ever been asked about (an
// array/map/pointer/primitive - every struct/enum is already interned
// eagerly by setupTypeRegistry, so this just finds their existing id).
func (g *Generator) registryIDFor(t sema.Type) int {
	desc := g.typeDescriptorFor(t)
	return g.internDescriptor(desc, g.registryConstructible(t))
}

// typeByNameEntryTy backs typeByNameTable - a plain {name, id} pair, one per
// registered struct/enum.
func (g *Generator) typeByNameEntryType() llvm.Type {
	if g.typeByNameEntryTy.IsNil() {
		g.typeByNameEntryTy = g.ctx.StructType([]llvm.Type{g.stringTy, g.i32Ty}, false)
	}
	return g.typeByNameEntryTy
}

// setupTypeRegistry eagerly registers every declared struct/enum across
// every file in trees into the shared descriptor registry (internDescriptor)
// and builds TypeByName's own name table - see this file's own top-of-file
// doc comment for why this must run after every struct body/enum layout is
// built (genPackage's own pass ordering already guarantees that window) and
// before any function body. Iterates trees in the caller's own order, and
// within each tree, TopLevelDeclsOfKind order (via declsOfKind) - the same
// AST-order determinism genPackage's own passes already rely on elsewhere,
// so a struct/enum's own id is stable across recompiles of identical source.
func (g *Generator) setupTypeRegistry(trees []*ast.Tree) {
	entryTy := g.typeByNameEntryType()
	var nameEntries []llvm.Value

	registerNamed := func(desc llvm.Value, name string, constructible bool) {
		id := g.internDescriptor(desc, constructible)
		nameEntries = append(nameEntries, g.ctx.ConstStruct([]llvm.Value{
			g.constStringValue(name),
			llvm.ConstInt(g.i32Ty, uint64(id), false),
		}, false))
	}

	for _, tree := range trees {
		g.enter(tree)
		for d := range g.declsOfKind(enums.NodeKinds.StructDecl) {
			info := g.structInfoOf(d)
			structType := sema.Type{Kind: sema.TypeStruct, Struct: info}
			// A struct holding a field with no Any representation at all (a
			// function value, most commonly) was never checked by sema's own
			// isBoxableIntoAny - that check only runs lazily, per real
			// Any-usage call site, and this eager whole-program walk is the
			// first thing that would ever ask about such a struct otherwise.
			// Skipped entirely (no id, no name-table entry) rather than
			// crashing structDescriptor - there is no legal way to reach this
			// type via TypeId[T]/TypeIdOf either, since sema rejects it there
			// for the same reason.
			if !g.registryAnyBoxable(structType) {
				continue
			}
			registerNamed(g.structDescriptor(info), info.Symbol.Name, g.codegenCopyable(structType))
		}
		for d := range g.declsOfKind(enums.NodeKinds.EnumDecl) {
			info := g.enumInfoOf(d)
			enumType := sema.Type{Kind: sema.TypeEnum, Enum: info}
			if !g.registryAnyBoxable(enumType) {
				continue
			}
			registerNamed(g.enumNestedDescriptor(info), info.Symbol.Name, false)
		}
	}

	g.typeByNameCount = len(nameEntries)
	if g.typeByNameCount == 0 {
		return
	}
	arrConst := llvm.ConstArray(entryTy, nameEntries)
	glob := llvm.AddGlobal(g.mod, arrConst.Type(), ".any.typebyname.table")
	glob.SetInitializer(arrConst)
	glob.SetGlobalConstant(true)
	glob.SetLinkage(llvm.PrivateLinkage)
	g.typeByNameTable = glob
}

// setupTypeRegistryGlobals declares the two placeholder globals AnyNew's own
// codegen reads through (see this file's own top-of-file doc comment) -
// called once, early, before any function body might reference them.
func (g *Generator) setupTypeRegistryGlobals() {
	g.typeRegistryDescsGlobal = llvm.AddGlobal(g.mod, g.ptrTy, ".any.registry.descs")
	g.typeRegistryDescsGlobal.SetInitializer(llvm.ConstNull(g.ptrTy))
	g.typeRegistryDescsGlobal.SetLinkage(llvm.PrivateLinkage)

	g.typeRegistryConstructibleGlobal = llvm.AddGlobal(g.mod, g.ptrTy, ".any.registry.constructible")
	g.typeRegistryConstructibleGlobal.SetInitializer(llvm.ConstNull(g.ptrTy))
	g.typeRegistryConstructibleGlobal.SetLinkage(llvm.PrivateLinkage)

	g.typeRegistryCountGlobal = llvm.AddGlobal(g.mod, g.i32Ty, ".any.registry.count")
	g.typeRegistryCountGlobal.SetInitializer(llvm.ConstInt(g.i32Ty, 0, false))
	g.typeRegistryCountGlobal.SetLinkage(llvm.PrivateLinkage)
}

// finalizeTypeRegistry builds the real backing `[N]ptr`/`[N]i8` arrays, now
// that every id (including any lazily-interned array/map/pointer shape) has
// been handed out, and points the two placeholder globals at them - see this
// file's own top-of-file doc comment. A no-op if AnyNew/TypeId/TypeIdOf were
// never actually used anywhere in the program (the two globals just keep
// their null/0 placeholder values, and AnyNew's own bounds check - id < 0 -
// then always fails, exactly as it should for an empty registry).
func (g *Generator) finalizeTypeRegistry() {
	n := len(g.descRegistry)
	if n == 0 {
		return
	}

	descsConst := llvm.ConstArray(g.ptrTy, g.descRegistry)
	descsGlob := llvm.AddGlobal(g.mod, descsConst.Type(), ".any.registry.descs.data")
	descsGlob.SetInitializer(descsConst)
	descsGlob.SetGlobalConstant(true)
	descsGlob.SetLinkage(llvm.PrivateLinkage)

	flagBytes := make([]llvm.Value, n)
	for i, ok := range g.descRegistryConstructible {
		v := uint64(0)
		if ok {
			v = 1
		}
		flagBytes[i] = llvm.ConstInt(g.i8Ty, v, false)
	}
	flagsConst := llvm.ConstArray(g.i8Ty, flagBytes)
	flagsGlob := llvm.AddGlobal(g.mod, flagsConst.Type(), ".any.registry.constructible.data")
	flagsGlob.SetInitializer(flagsConst)
	flagsGlob.SetGlobalConstant(true)
	flagsGlob.SetLinkage(llvm.PrivateLinkage)

	g.typeRegistryDescsGlobal.SetInitializer(descsGlob)
	g.typeRegistryConstructibleGlobal.SetInitializer(flagsGlob)
	g.typeRegistryCountGlobal.SetInitializer(llvm.ConstInt(g.i32Ty, uint64(n), false))
}

// isTypeIdCall reports whether calleeNode is `TypeId[T]` - mirrors
// isAnyAsCall/isAnySetCall exactly, since TypeId is registered in
// universeScope the same no-Decl-Generic way (see sema's checkTypeIdCall).
func (g *Generator) isTypeIdCall(calleeNode ast.NodeIndex) bool {
	if g.tree.Nodes[calleeNode].Kind != enums.NodeKinds.IndexExpr {
		return false
	}
	return g.isBuiltinCall(g.tree.Child(calleeNode, 0), "TypeId")
}

// genTypeIdCall lowers `TypeId[T]() int` - T's own registry id, baked in as
// a plain i32 constant: T is fully known at compile time, so unlike AnyNew's
// runtime id lookup, there's nothing to compute at runtime here at all. T
// itself was stashed on calleeNode (the `TypeId[T]` IndexExpr) by
// checkTypeIdCall - see that function's own doc comment for why (TypeId's
// plain int return type can't carry T the way AnyAs[T]'s (T, bool) does).
func (g *Generator) genTypeIdCall(calleeNode ast.NodeIndex) llvm.Value {
	target := g.info.Types[calleeNode]
	return llvm.ConstInt(g.i32Ty, uint64(g.registryIDFor(target)), false)
}

// genTypeIdOfCall lowers `TypeIdOf(x T) int` - identical to TypeId[T]() but
// reading T off x's own already-checked static type instead of an explicit
// type argument. x itself is never evaluated - see checkLenCall's identical
// fixed-array-length precedent (genLenCall, runtime.go): only the static
// type matters, so there is nothing to gain from generating x at all.
func (g *Generator) genTypeIdOfCall(argNode ast.NodeIndex) llvm.Value {
	target := g.info.Types[argNode]
	return llvm.ConstInt(g.i32Ty, uint64(g.registryIDFor(target)), false)
}

// genTypeByNameCall lowers `TypeByName(name string) []int` - a plain linear
// scan over the compiler-built {name, id} table (setupTypeRegistry),
// appending every entry's own id whose name matches exactly (see
// LANGUAGE.md's "Type registry" section for why every match, not just the
// first). typeByNameCount is a compile-time constant (the table's size never
// changes after setupTypeRegistry runs), so this unrolls into a fixed-length
// loop, not a runtime-bounded one.
func (g *Generator) genTypeByNameCall(argNode ast.NodeIndex) llvm.Value {
	name := g.genExpr(argNode)

	resultAddr := g.createEntryAlloca(g.dynArrTy, "typebyname.result")
	g.builder.CreateStore(llvm.ConstNull(g.dynArrTy), resultAddr)

	entryTy := g.typeByNameEntryType()
	for i := range g.typeByNameCount {
		entryAddr := g.builder.CreateInBoundsGEP(entryTy, g.typeByNameTable, []llvm.Value{llvm.ConstInt(g.i32Ty, uint64(i), false)}, "")
		entryName := g.builder.CreateLoad(g.stringTy, g.builder.CreateStructGEP(entryTy, entryAddr, 0, ""), "")
		entryID := g.builder.CreateLoad(g.i32Ty, g.builder.CreateStructGEP(entryTy, entryAddr, 1, ""), "")
		matches := g.genStringEqual(name, entryName, true)

		matchBB := g.ctx.AddBasicBlock(g.curFn, "typebyname.match")
		contBB := g.ctx.AddBasicBlock(g.curFn, "typebyname.cont")
		g.builder.CreateCondBr(matches, matchBB, contBB)

		g.builder.SetInsertPointAtEnd(matchBB)
		cur := g.builder.CreateLoad(g.dynArrTy, resultAddr, "")
		appended := g.genAppendValue(cur, entryID, g.i32Ty)
		g.builder.CreateStore(appended, resultAddr)
		g.builder.CreateBr(contBB)

		g.builder.SetInsertPointAtEnd(contBB)
	}

	return g.builder.CreateLoad(g.dynArrTy, resultAddr, "")
}

// genAnyNewCall lowers `AnyNew(id int) (Any, bool)` (see LANGUAGE.md's "Type
// registry" section) - bounds-checks id against the registry's own live
// count (mirroring genAnyIndexCall's identical style), then rejects an
// enum id or a non-constructible id (registryConstructible) the same way -
// never a crash, ok=false and a zero Any instead. On success, arena-allocates
// and zero-fills the descriptor's own size bytes: every boxable type's zero
// value is all-zero bytes (see genVarDecl's own ConstNull zero-init, which
// this mirrors at the byte level - AnyNew has no static LLVM type in hand to
// ConstNull directly, only a runtime descriptor).
func (g *Generator) genAnyNewCall(n, argNode ast.NodeIndex) llvm.Value {
	resultType := g.info.Types[n]
	id := g.genExpr(argNode)

	count := g.builder.CreateLoad(g.i32Ty, g.typeRegistryCountGlobal, "")
	geZero := g.builder.CreateICmp(llvm.IntSGE, id, llvm.ConstInt(g.i32Ty, 0, true), "")
	ltCount := g.builder.CreateICmp(llvm.IntSLT, id, count, "")
	inBounds := g.builder.CreateAnd(geZero, ltCount, "")

	boundsBB := g.ctx.AddBasicBlock(g.curFn, "anynew.bounds")
	buildBB := g.ctx.AddBasicBlock(g.curFn, "anynew.build")
	failBB := g.ctx.AddBasicBlock(g.curFn, "anynew.fail")
	contBB := g.ctx.AddBasicBlock(g.curFn, "anynew.cont")
	g.builder.CreateCondBr(inBounds, boundsBB, failBB)

	g.builder.SetInsertPointAtEnd(boundsBB)
	descsBase := g.builder.CreateLoad(g.ptrTy, g.typeRegistryDescsGlobal, "")
	flagsBase := g.builder.CreateLoad(g.ptrTy, g.typeRegistryConstructibleGlobal, "")
	descAddr := g.builder.CreateInBoundsGEP(g.ptrTy, descsBase, []llvm.Value{id}, "")
	descPtr := g.builder.CreateLoad(g.ptrTy, descAddr, "")
	flagAddr := g.builder.CreateInBoundsGEP(g.i8Ty, flagsBase, []llvm.Value{id}, "")
	flagByte := g.builder.CreateLoad(g.i8Ty, flagAddr, "")
	constructible := g.builder.CreateICmp(llvm.IntNE, flagByte, llvm.ConstInt(g.i8Ty, 0, false), "")
	g.builder.CreateCondBr(constructible, buildBB, failBB)

	g.builder.SetInsertPointAtEnd(buildBB)
	size := g.builder.CreateLoad(g.i64Ty, g.builder.CreateStructGEP(g.typeDescriptorTy, descPtr, 7, ""), "")
	buf := g.genArenaAlloc(size)
	g.builder.CreateCall(g.memsetType, g.memsetFn, []llvm.Value{buf, llvm.ConstInt(g.i32Ty, 0, false), size}, "")
	builtAny := llvm.Undef(g.anyTy)
	builtAny = g.builder.CreateInsertValue(builtAny, buf, 0, "")
	builtAny = g.builder.CreateInsertValue(builtAny, descPtr, 1, "")
	buildEndBB := g.builder.GetInsertBlock()
	g.builder.CreateBr(contBB)

	g.builder.SetInsertPointAtEnd(failBB)
	failEndBB := g.builder.GetInsertBlock()
	g.builder.CreateBr(contBB)

	g.builder.SetInsertPointAtEnd(contBB)
	anyPhi := g.builder.CreatePHI(g.anyTy, "")
	anyPhi.AddIncoming([]llvm.Value{builtAny, llvm.ConstNull(g.anyTy)}, []llvm.BasicBlock{buildEndBB, failEndBB})
	okPhi := g.builder.CreatePHI(g.boolTy, "")
	okPhi.AddIncoming(
		[]llvm.Value{llvm.ConstInt(g.boolTy, 1, false), llvm.ConstInt(g.boolTy, 0, false)},
		[]llvm.BasicBlock{buildEndBB, failEndBB},
	)

	agg := llvm.Undef(g.llvmType(resultType))
	agg = g.builder.CreateInsertValue(agg, anyPhi, 0, "")
	agg = g.builder.CreateInsertValue(agg, okPhi, 1, "")
	return agg
}
