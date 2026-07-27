package codegen

import (
	"fmt"

	"llvm_lang/src/ast"
	"llvm_lang/src/enums"
	"llvm_lang/src/sema"

	"tinygo.org/x/go-llvm"
)

// Any (sema.TypeAny) is a type-erased boxed value, lowered as the literal
// struct {ptr, ptr} = {dataPtr, descriptorPtr} - see DECISIONS.md for why
// this shape was chosen over a tagged-union-of-primitives-plus-fallback
// design. dataPtr always points into arena-allocated memory (genAnyBox) -
// never a stack address - so a boxed value stays valid regardless of
// whether it outlives the frame it was boxed in.
//
// descriptorPtr points at one shared, interned, read-only TypeDescriptor
// global (typeDescriptorTy below) per distinct boxed type - one per
// sema.TypeKind for every scalar/primitive kind (anyPrimitiveDescs, built
// eagerly in setupAnyRuntime, mirroring Go's own one-runtime._type-per-type
// model), one per *sema.StructInfo (structAnyDescs), built lazily the first
// time that struct is actually boxed anywhere in the program, and one per
// distinct array shape (arrayAnyDescs - see arrayDescriptor's own doc
// comment for why an array type needs its own interning key instead of
// structDescriptor's pointer-identity one).
//
//	typeDescriptorTy  = { i32 kind, string name, i32 fieldCount, ptr fieldsPtr, ptr elemDescPtr, i32 arrayLen, i64 elemSize }
//	fieldDescriptorTy = { string name, ptr fieldDescriptorPtr, i64 offset }
//
// kind is the boxed type's own sema.TypeKind wire value (AnyKind reads it
// directly, as a plain i32 - see checkAnyKindCall's own doc comment for why
// this round doesn't expose TypeKind as a language-level enum). fieldsPtr is
// null/fieldCount is 0 for every non-struct descriptor; for a struct, it
// points at a global array of fieldCount FieldDescriptors, each recursively
// naming its own field's type descriptor and its byte offset within the
// struct (read off structLayouts - the same layout codegen already computes
// for ordinary field access, not re-derived here).
//
// elemDescPtr/arrayLen/elemSize are meaningful only for a TypeArray
// descriptor (null/0 otherwise, same "zero for anything that doesn't apply"
// convention fieldCount/fieldsPtr already establish): elemDescPtr is the
// element type's own recursively-built descriptor; arrayLen is the real
// compile-time length for a fixed array, or the -1 sentinel for a dynamic
// array (whose real length lives on the boxed VALUE, not this shared
// type-level descriptor - see AnyLen/AnyIndex); elemSize is one element's
// real byte width (llvm.SizeOf), needed because AnyIndex must stride through
// a boxed array's backing bytes with no static LLVM element type of its own
// to GEP against (T is fully erased at that call site, unlike genAnyBox's
// own from-type-still-in-hand case) - this third field goes beyond what was
// originally scoped for this round, see DECISIONS.md.

// anyPrimitiveKinds is every non-struct kind this round gives Any a
// descriptor for (see LANGUAGE.md's "Any" section and sema's
// isBoxableIntoAny) - built eagerly since there are few enough of them for
// this to cost nothing at program startup, unlike a struct descriptor, which
// is built lazily per distinct struct type.
var anyPrimitiveKinds = []sema.TypeKind{
	sema.TypeI8, sema.TypeI16, sema.TypeI32, sema.TypeI64,
	sema.TypeU8, sema.TypeU16, sema.TypeU32, sema.TypeU64,
	sema.TypeF32, sema.TypeF64,
	sema.TypeBool, sema.TypeString, sema.TypeCString, sema.TypePointer,
}

// anyPrimitiveDisplayName is the name baked into a primitive kind's own
// descriptor (AnyName's result) - TypeKind.Display() directly for every kind
// that declares one in type_kind.yml, except TypePointer (no display column,
// since Type.String() renders it via its own recursive "*"+Elem case
// instead - see sema/types.go).
func anyPrimitiveDisplayName(k sema.TypeKind) string {
	if k == sema.TypePointer {
		return "pointer"
	}
	return k.Display()
}

// setupAnyRuntime builds the descriptor LLVM types and every primitive
// descriptor's own global, once per Generator - see this file's own
// top-of-file doc comment for the exact shape. Must run after setupTypes
// (needs g.stringTy/g.ptrTy/g.i32Ty/g.i64Ty already built).
func (g *Generator) setupAnyRuntime() {
	g.typeDescriptorTy = g.ctx.StructType([]llvm.Type{g.i32Ty, g.stringTy, g.i32Ty, g.ptrTy, g.ptrTy, g.i32Ty, g.i64Ty}, false)
	g.fieldDescriptorTy = g.ctx.StructType([]llvm.Type{g.stringTy, g.ptrTy, g.i64Ty}, false)

	g.anyPrimitiveDescs = make(map[sema.TypeKind]llvm.Value, len(anyPrimitiveKinds))
	g.structAnyDescs = make(map[*sema.StructInfo]llvm.Value)
	g.arrayAnyDescs = make(map[arrayAnyDescKey]llvm.Value)
	noElemDesc, noArrayLen, noElemSize := llvm.ConstNull(g.ptrTy), llvm.ConstInt(g.i32Ty, 0, false), llvm.ConstInt(g.i64Ty, 0, false)
	for _, k := range anyPrimitiveKinds {
		name := anyPrimitiveDisplayName(k)
		g.anyPrimitiveDescs[k] = g.buildTypeDescriptorGlobal(k, name, 0, llvm.ConstNull(g.ptrTy), noElemDesc, noArrayLen, noElemSize, ".any.desc."+name)
	}
}

// buildTypeDescriptorGlobal emits one new, private, read-only
// TypeDescriptor global and returns its address - shared by every primitive
// descriptor (setupAnyRuntime), structDescriptor, and arrayDescriptor below.
// elemDescPtr/arrayLen/elemSize are meaningful only for a TypeArray kind -
// every other caller passes null/0/0 (see this file's top-of-file doc
// comment).
func (g *Generator) buildTypeDescriptorGlobal(kind sema.TypeKind, name string, fieldCount int, fieldsPtr, elemDescPtr, arrayLen, elemSize llvm.Value, globalName string) llvm.Value {
	descConst := g.ctx.ConstStruct([]llvm.Value{
		llvm.ConstInt(g.i32Ty, uint64(kind), false),
		g.constStringValue(name),
		llvm.ConstInt(g.i32Ty, uint64(fieldCount), false),
		fieldsPtr,
		elemDescPtr,
		arrayLen,
		elemSize,
	}, false)
	glob := llvm.AddGlobal(g.mod, g.typeDescriptorTy, globalName)
	glob.SetInitializer(descConst)
	glob.SetGlobalConstant(true)
	glob.SetLinkage(llvm.PrivateLinkage)
	glob.SetUnnamedAddr(true)
	return glob
}

// constFieldOffset computes fieldIdx's own byte offset within structLLType
// as a real i64 constant - the classic null-pointer-GEP-then-ptrtoint trick
// (see genArenaAllocElems's own doc comment for the identical idiom backing
// llvm.SizeOf), letting LLVM's own target-data layout resolve it without
// this package needing a DataLayout handle of its own.
func (g *Generator) constFieldOffset(structLLType llvm.Type, fieldIdx int) llvm.Value {
	zero := llvm.ConstInt(g.i32Ty, 0, false)
	idx := llvm.ConstInt(g.i32Ty, uint64(fieldIdx), false)
	gep := llvm.ConstGEP(structLLType, llvm.ConstNull(g.ptrTy), []llvm.Value{zero, idx})
	return llvm.ConstPtrToInt(gep, g.i64Ty)
}

// typeDescriptorFor returns t's own shared type descriptor - structDescriptor/
// arrayDescriptor's own lazy build/intern for a struct/array, or a plain
// primitive-kind lookup for everything else.
func (g *Generator) typeDescriptorFor(t sema.Type) llvm.Value {
	switch t.Kind {
	case sema.TypeStruct:
		return g.structDescriptor(t.Struct)
	case sema.TypeArray:
		return g.arrayDescriptor(t)
	default:
		desc, ok := g.anyPrimitiveDescs[t.Kind]
		if !ok {
			panic(fmt.Sprintf("codegen: %s has no Any descriptor - isBoxableIntoAny should have rejected boxing it", t))
		}
		return desc
	}
}

// structDescriptor returns info's own shared TypeDescriptor, building and
// interning it the first time info is actually boxed anywhere in the
// program (see LANGUAGE.md's "Any" section) - deduped by *sema.StructInfo
// identity, this codebase's standing convention for struct identity (see
// sema.Type.Equal's own TypeStruct case). Each field's own descriptor is
// built recursively via typeDescriptorFor - safe against unbounded
// recursion since a struct can only ever cycle through itself via a pointer
// field (TypePointer's own descriptor doesn't recurse into Elem), never by
// value.
func (g *Generator) structDescriptor(info *sema.StructInfo) llvm.Value {
	if desc, ok := g.structAnyDescs[info]; ok {
		return desc
	}
	layout := g.structLayouts[info]

	fieldCount := len(layout.fieldNames)
	fieldsPtr := llvm.ConstNull(g.ptrTy)
	if fieldCount > 0 {
		fieldDescs := make([]llvm.Value, fieldCount)
		for i, name := range layout.fieldNames {
			fieldDescs[i] = g.ctx.ConstStruct([]llvm.Value{
				g.constStringValue(name),
				g.typeDescriptorFor(layout.fieldSemaTypes[i]),
				g.constFieldOffset(layout.llvmType, i),
			}, false)
		}
		arrConst := llvm.ConstArray(g.fieldDescriptorTy, fieldDescs)
		glob := llvm.AddGlobal(g.mod, arrConst.Type(), ".any.fields."+info.Symbol.Name)
		glob.SetInitializer(arrConst)
		glob.SetGlobalConstant(true)
		glob.SetLinkage(llvm.PrivateLinkage)
		fieldsPtr = glob
	}

	noElemDesc, noArrayLen, noElemSize := llvm.ConstNull(g.ptrTy), llvm.ConstInt(g.i32Ty, 0, false), llvm.ConstInt(g.i64Ty, 0, false)
	desc := g.buildTypeDescriptorGlobal(sema.TypeStruct, info.Symbol.Name, fieldCount, fieldsPtr, noElemDesc, noArrayLen, noElemSize, ".any.desc."+info.Symbol.Name)
	g.structAnyDescs[info] = desc
	return desc
}

// arrayAnyDescKey interns one shared descriptor per distinct array shape -
// an array type has no *sema.StructInfo-equivalent identity object to dedupe
// by pointer (structDescriptor's own convention), since it's structurally,
// not nominally, defined. elemDesc is the element type's own already-
// interned descriptor pointer, standing in for "same element type".
type arrayAnyDescKey struct {
	dynamic  bool
	size     int64
	elemDesc llvm.Value
}

// arrayDescriptor returns t's own shared TypeDescriptor for a fixed ([N]T)
// or dynamic ([]T) array type, building and interning it the first time this
// exact shape is boxed anywhere in the program - see arrayAnyDescKey and this
// file's top-of-file doc comment for the elemDescPtr/arrayLen/elemSize
// fields this fills in.
func (g *Generator) arrayDescriptor(t sema.Type) llvm.Value {
	elemDesc := g.typeDescriptorFor(*t.Elem)
	key := arrayAnyDescKey{dynamic: t.Dynamic, size: t.Size, elemDesc: elemDesc}
	if desc, ok := g.arrayAnyDescs[key]; ok {
		return desc
	}

	arrayLen := llvm.ConstInt(g.i32Ty, uint64(t.Size), false)
	if t.Dynamic {
		// -1 sentinel (an all-ones i32 bit pattern): a dynamic array's real
		// length lives on the boxed VALUE's own {ptr, len, cap} header, not
		// this shared type-level descriptor - see genAnyLenCall.
		arrayLen = llvm.ConstInt(g.i32Ty, 0xFFFFFFFF, false)
	}
	elemSize := llvm.SizeOf(g.llvmType(*t.Elem))

	globalName := fmt.Sprintf(".any.desc.array.%d", len(g.arrayAnyDescs))
	desc := g.buildTypeDescriptorGlobal(sema.TypeArray, t.String(), 0, llvm.ConstNull(g.ptrTy), elemDesc, arrayLen, elemSize, globalName)
	g.arrayAnyDescs[key] = desc
	return desc
}

// genAnyBox lowers `Any(x)` (see genConversion's own TypeAny dispatch): x's
// bytes are copied into a fresh arena slot (never a stack address - see this
// file's top-of-file doc comment for why), paired with from's own shared
// type descriptor.
//
// Any(x) where x is itself already Any is specified as a flattening no-op
// copy (LANGUAGE.md's "Any" section): v is already the real {dataPtr,
// descriptorPtr} pair, so it's returned unchanged rather than re-wrapped in
// a fresh box that would report TypeAny as its own kind - genConversion's
// from.Kind == to.Kind check already takes this same shortcut before ever
// calling here, but typeDescriptorFor has no TypeAny entry of its own (Any
// isn't in anyPrimitiveKinds), so this case is handled here too rather than
// relying solely on that caller never changing.
func (g *Generator) genAnyBox(from sema.Type, v llvm.Value) llvm.Value {
	if from.Kind == sema.TypeAny {
		return v
	}

	elemLLType := g.llvmType(from)
	buf := g.genArenaAlloc(llvm.SizeOf(elemLLType))
	g.builder.CreateStore(v, buf)

	result := llvm.Undef(g.anyTy)
	result = g.builder.CreateInsertValue(result, buf, 0, "")
	result = g.builder.CreateInsertValue(result, g.typeDescriptorFor(from), 1, "")
	return result
}

// genAnyKindCall lowers the `AnyKind(a Any) i32` builtin - the boxed value's
// own descriptor's kind field (offset 0, so no GEP is needed - a direct
// i32 load off the descriptor pointer).
func (g *Generator) genAnyKindCall(argNode ast.NodeIndex) llvm.Value {
	a := g.genExpr(argNode)
	descPtr := g.builder.CreateExtractValue(a, 1, "")
	return g.builder.CreateLoad(g.i32Ty, descPtr, "")
}

// genAnyNameCall lowers the `AnyName(a Any) string` builtin - the boxed
// value's own descriptor's name field.
func (g *Generator) genAnyNameCall(argNode ast.NodeIndex) llvm.Value {
	a := g.genExpr(argNode)
	descPtr := g.builder.CreateExtractValue(a, 1, "")
	nameAddr := g.builder.CreateStructGEP(g.typeDescriptorTy, descPtr, 1, "")
	return g.builder.CreateLoad(g.stringTy, nameAddr, "")
}

// isAnyAsCall reports whether calleeNode is `AnyAs[T]` - mirrors sema's own
// checkAnyAsCall recognition (genericCallee's IndexExpr case), since AnyAs
// is a predeclared builtin (see universeScope) reached through the same
// call shape a real generic function's explicit instantiation uses.
func (g *Generator) isAnyAsCall(calleeNode ast.NodeIndex) bool {
	if g.tree.Nodes[calleeNode].Kind != enums.NodeKinds.IndexExpr {
		return false
	}
	return g.isBuiltinCall(g.tree.Child(calleeNode, 0), "AnyAs")
}

// genAnyAsCall lowers `AnyAs[T](a Any) (T, bool)` (see checkAnyAsCall's own
// doc comment) - Go's own type-assertion shape. matches compares the boxed
// descriptor against T's own descriptor; T's value is only ever loaded on
// the matching branch (real control flow, not an eagerly-computed load
// gated by CreateSelect afterward) - the mismatched case's boxed data may be
// smaller than sizeof(T), so loading it unconditionally would be an
// out-of-bounds read.
func (g *Generator) genAnyAsCall(n, argNode ast.NodeIndex) llvm.Value {
	result := g.info.Types[n]
	target := result.Params[0]
	targetLLType := g.llvmType(target)

	a := g.genExpr(argNode)
	dataPtr := g.builder.CreateExtractValue(a, 0, "")
	descPtr := g.builder.CreateExtractValue(a, 1, "")

	actualKind := g.builder.CreateLoad(g.i32Ty, descPtr, "")
	wantKind := llvm.ConstInt(g.i32Ty, uint64(target.Kind), false)
	matches := g.builder.CreateICmp(llvm.IntEQ, actualKind, wantKind, "")
	// Two different structs (or two different array shapes) both report the
	// identical sema.TypeKind as their kind - descriptor pointer identity
	// (structDescriptor/arrayDescriptor each intern one shared descriptor per
	// distinct type) is what actually tells them apart.
	switch target.Kind {
	case sema.TypeStruct:
		wantDesc := g.structDescriptor(target.Struct)
		matches = g.builder.CreateAnd(matches, g.builder.CreateICmp(llvm.IntEQ, descPtr, wantDesc, ""), "")
	case sema.TypeArray:
		wantDesc := g.arrayDescriptor(target)
		matches = g.builder.CreateAnd(matches, g.builder.CreateICmp(llvm.IntEQ, descPtr, wantDesc, ""), "")
	}

	matchBB := g.ctx.AddBasicBlock(g.curFn, "anyas.match")
	noMatchBB := g.ctx.AddBasicBlock(g.curFn, "anyas.nomatch")
	contBB := g.ctx.AddBasicBlock(g.curFn, "anyas.cont")
	g.builder.CreateCondBr(matches, matchBB, noMatchBB)

	g.builder.SetInsertPointAtEnd(matchBB)
	matchedVal := g.builder.CreateLoad(targetLLType, dataPtr, "")
	g.builder.CreateBr(contBB)

	g.builder.SetInsertPointAtEnd(noMatchBB)
	zeroVal := llvm.ConstNull(targetLLType)
	g.builder.CreateBr(contBB)

	g.builder.SetInsertPointAtEnd(contBB)
	valPhi := g.builder.CreatePHI(targetLLType, "")
	valPhi.AddIncoming([]llvm.Value{matchedVal, zeroVal}, []llvm.BasicBlock{matchBB, noMatchBB})

	agg := llvm.Undef(g.llvmType(result))
	agg = g.builder.CreateInsertValue(agg, valPhi, 0, "")
	agg = g.builder.CreateInsertValue(agg, matches, 1, "")
	return agg
}

// isAnyFieldsRangeSubject reports whether subjectNode is a direct
// AnyFields(...) call - genRangeForStmt's own signal to take
// genRangeForAnyFields's 2-binding path instead of an ordinary generator's
// callback-based one (see sema's own isAnyFieldsRangeSubject, mirrored here
// exactly, and checkRangeForStmt's own doc comment for why).
func (g *Generator) isAnyFieldsRangeSubject(subjectNode ast.NodeIndex) bool {
	if g.tree.Nodes[subjectNode].Kind != enums.NodeKinds.CallExpr {
		return false
	}
	return g.isBuiltinCall(g.tree.Child(subjectNode, 0), "AnyFields")
}

// genRangeForAnyFields lowers `for name, value := range AnyFields(a) { ... }`
// - unlike a real generator (genRangeForGenerator's push/callback lowering),
// this is an ordinary runtime-bounded index loop (0..fieldCount-1) reading
// each iteration's own name/value straight off a's descriptor's own field
// table, exactly the same indexed-loop shape genRangeForArray already uses -
// there's no generator function to call back into at all, since a struct's
// fields are a fixed runtime table, not a push-driven sequence. A field's
// own Any value points directly into the parent's own already-arena-
// allocated backing storage (dataPtr + offset) - no fresh copy per field,
// since the field's lifetime is already exactly the parent's.
func (g *Generator) genRangeForAnyFields(keyNode, valueNode, subjectNode, bodyNode ast.NodeIndex) bool {
	argNode := g.tree.Children(subjectNode)[1]
	a := g.genExpr(argNode)
	dataPtr := g.builder.CreateExtractValue(a, 0, "")
	descPtr := g.builder.CreateExtractValue(a, 1, "")

	fieldCount := g.builder.CreateLoad(g.i32Ty, g.builder.CreateStructGEP(g.typeDescriptorTy, descPtr, 2, ""), "")
	fieldsPtr := g.builder.CreateLoad(g.ptrTy, g.builder.CreateStructGEP(g.typeDescriptorTy, descPtr, 3, ""), "")

	idxAddr := g.createEntryAlloca(g.i32Ty, "anyfields.idx")
	g.builder.CreateStore(llvm.ConstInt(g.i32Ty, 0, false), idxAddr)

	condBB := g.ctx.AddBasicBlock(g.curFn, "anyfields.cond")
	bodyBB := g.ctx.AddBasicBlock(g.curFn, "anyfields.body")
	postBB := g.ctx.AddBasicBlock(g.curFn, "anyfields.post")
	endBB := g.ctx.AddBasicBlock(g.curFn, "anyfields.end")

	g.builder.CreateBr(condBB)
	g.builder.SetInsertPointAtEnd(condBB)
	idx := g.builder.CreateLoad(g.i32Ty, idxAddr, "")
	g.builder.CreateCondBr(g.builder.CreateICmp(llvm.IntSLT, idx, fieldCount, ""), bodyBB, endBB)

	g.builder.SetInsertPointAtEnd(bodyBB)
	bodyIdx := g.builder.CreateLoad(g.i32Ty, idxAddr, "")
	fieldAddr := g.builder.CreateInBoundsGEP(g.fieldDescriptorTy, fieldsPtr, []llvm.Value{bodyIdx}, "")
	name := g.builder.CreateLoad(g.stringTy, g.builder.CreateStructGEP(g.fieldDescriptorTy, fieldAddr, 0, ""), "")
	fieldDescPtr := g.builder.CreateLoad(g.ptrTy, g.builder.CreateStructGEP(g.fieldDescriptorTy, fieldAddr, 1, ""), "")
	offset := g.builder.CreateLoad(g.i64Ty, g.builder.CreateStructGEP(g.fieldDescriptorTy, fieldAddr, 2, ""), "")
	fieldDataPtr := g.builder.CreateInBoundsGEP(g.i8Ty, dataPtr, []llvm.Value{offset}, "")

	fieldValue := llvm.Undef(g.anyTy)
	fieldValue = g.builder.CreateInsertValue(fieldValue, fieldDataPtr, 0, "")
	fieldValue = g.builder.CreateInsertValue(fieldValue, fieldDescPtr, 1, "")

	preBindBase := g.snapshotDestructorScope()
	g.bindRangeVar(keyNode, name)
	g.bindRangeVar(valueNode, fieldValue)

	g.loopStack = append(g.loopStack, loopCtx{
		breakTarget:    endBB,
		continueTarget: postBB,
		destructorBase: preBindBase,
	})
	bodyTerm := g.genBlock(bodyNode)
	g.loopStack = g.loopStack[:len(g.loopStack)-1]
	if !bodyTerm {
		g.unwindDestructorsToScope(preBindBase)
		g.builder.CreateBr(postBB)
	}

	g.builder.SetInsertPointAtEnd(postBB)
	nextIdx := g.builder.CreateAdd(g.builder.CreateLoad(g.i32Ty, idxAddr, ""), llvm.ConstInt(g.i32Ty, 1, false), "")
	g.builder.CreateStore(nextIdx, idxAddr)
	g.builder.CreateBr(condBB)

	g.builder.SetInsertPointAtEnd(endBB)
	return false
}

// genAnyLenCall lowers the `AnyLen(a Any) int` builtin: a boxed fixed
// array's compile-time length, read straight off its own shared descriptor's
// arrayLen field, or a boxed dynamic array's real runtime length, read off
// the arena-copied {ptr, len, cap} header's own len field (offset 1) -
// dataPtr points straight at that header, since genAnyBox copies a dynamic
// array's whole 3-word value wholesale, not just its backing buffer. Returns
// 0 for any non-array boxed kind, mirroring AnyFields' own "wrong kind at
// runtime yields a harmless zero" permissiveness (checkAnyLenCall) rather
// than a crash - Any erases the static type, so there's nothing to check at
// compile time.
func (g *Generator) genAnyLenCall(argNode ast.NodeIndex) llvm.Value {
	a := g.genExpr(argNode)
	dataPtr := g.builder.CreateExtractValue(a, 0, "")
	descPtr := g.builder.CreateExtractValue(a, 1, "")

	actualKind := g.builder.CreateLoad(g.i32Ty, descPtr, "")
	isArray := g.builder.CreateICmp(llvm.IntEQ, actualKind, llvm.ConstInt(g.i32Ty, uint64(sema.TypeArray), false), "")

	arrayBB := g.ctx.AddBasicBlock(g.curFn, "anylen.array")
	notArrayBB := g.ctx.AddBasicBlock(g.curFn, "anylen.notarray")
	contBB := g.ctx.AddBasicBlock(g.curFn, "anylen.cont")
	g.builder.CreateCondBr(isArray, arrayBB, notArrayBB)

	g.builder.SetInsertPointAtEnd(notArrayBB)
	notArrayEndBB := g.builder.GetInsertBlock()
	g.builder.CreateBr(contBB)

	g.builder.SetInsertPointAtEnd(arrayBB)
	descArrayLen := g.builder.CreateLoad(g.i32Ty, g.builder.CreateStructGEP(g.typeDescriptorTy, descPtr, 5, ""), "")
	isDynamic := g.builder.CreateICmp(llvm.IntSLT, descArrayLen, llvm.ConstInt(g.i32Ty, 0, true), "")

	fixedBB := g.ctx.AddBasicBlock(g.curFn, "anylen.fixed")
	dynamicBB := g.ctx.AddBasicBlock(g.curFn, "anylen.dynamic")
	g.builder.CreateCondBr(isDynamic, dynamicBB, fixedBB)

	g.builder.SetInsertPointAtEnd(fixedBB)
	fixedEndBB := g.builder.GetInsertBlock()
	g.builder.CreateBr(contBB)

	g.builder.SetInsertPointAtEnd(dynamicBB)
	runtimeLen := g.builder.CreateLoad(g.i32Ty, g.builder.CreateStructGEP(g.dynArrTy, dataPtr, 1, ""), "")
	dynamicEndBB := g.builder.GetInsertBlock()
	g.builder.CreateBr(contBB)

	g.builder.SetInsertPointAtEnd(contBB)
	result := g.builder.CreatePHI(g.i32Ty, "")
	result.AddIncoming(
		[]llvm.Value{llvm.ConstInt(g.i32Ty, 0, false), descArrayLen, runtimeLen},
		[]llvm.BasicBlock{notArrayEndBB, fixedEndBB, dynamicEndBB},
	)
	return result
}

// genAnyIndexCall lowers the `AnyIndex(a Any, i int) (Any, bool)` builtin:
// bounds-checked element access into a boxed array, mirroring genAnyAsCall's
// own match/no-match branching so an out-of-range index or a non-array `a`
// never crashes and never reads out of bounds - it just reports ok=false,
// with the resulting Any left as its zero value. On a match, the element's
// address is computed as basePtr + i*elemSize (both read off the array's own
// shared descriptor - see this file's top-of-file doc comment for why
// elemSize has to be a descriptor field at all: T is fully erased here, so
// there's no static LLVM element type to GEP against). The returned Any
// shares the parent's own already-arena-allocated storage, no fresh copy -
// the same "field lifetime is already exactly the parent's" reasoning
// genRangeForAnyFields already uses for a struct field.
func (g *Generator) genAnyIndexCall(n ast.NodeIndex, args []ast.NodeIndex) llvm.Value {
	resultType := g.info.Types[n]

	a := g.genExpr(args[0])
	idx := g.genExpr(args[1])
	dataPtr := g.builder.CreateExtractValue(a, 0, "")
	descPtr := g.builder.CreateExtractValue(a, 1, "")

	actualKind := g.builder.CreateLoad(g.i32Ty, descPtr, "")
	isArray := g.builder.CreateICmp(llvm.IntEQ, actualKind, llvm.ConstInt(g.i32Ty, uint64(sema.TypeArray), false), "")

	arrayBB := g.ctx.AddBasicBlock(g.curFn, "anyindex.array")
	failBB := g.ctx.AddBasicBlock(g.curFn, "anyindex.fail")
	matchBB := g.ctx.AddBasicBlock(g.curFn, "anyindex.match")
	contBB := g.ctx.AddBasicBlock(g.curFn, "anyindex.cont")
	g.builder.CreateCondBr(isArray, arrayBB, failBB)

	g.builder.SetInsertPointAtEnd(arrayBB)
	descArrayLen := g.builder.CreateLoad(g.i32Ty, g.builder.CreateStructGEP(g.typeDescriptorTy, descPtr, 5, ""), "")
	elemDescPtr := g.builder.CreateLoad(g.ptrTy, g.builder.CreateStructGEP(g.typeDescriptorTy, descPtr, 4, ""), "")
	elemSize := g.builder.CreateLoad(g.i64Ty, g.builder.CreateStructGEP(g.typeDescriptorTy, descPtr, 6, ""), "")
	isDynamic := g.builder.CreateICmp(llvm.IntSLT, descArrayLen, llvm.ConstInt(g.i32Ty, 0, true), "")

	dynBB := g.ctx.AddBasicBlock(g.curFn, "anyindex.dynamic")
	fixedBB := g.ctx.AddBasicBlock(g.curFn, "anyindex.fixed")
	lenBB := g.ctx.AddBasicBlock(g.curFn, "anyindex.lenready")
	g.builder.CreateCondBr(isDynamic, dynBB, fixedBB)

	g.builder.SetInsertPointAtEnd(fixedBB)
	fixedEndBB := g.builder.GetInsertBlock()
	g.builder.CreateBr(lenBB)

	g.builder.SetInsertPointAtEnd(dynBB)
	// genAnyBox copies a dynamic array's whole {ptr, len, cap} header
	// wholesale - dataPtr points at that arena-copied header, so its own
	// ptr/len fields (offsets 0/1) are the real backing buffer and length.
	realDataPtr := g.builder.CreateLoad(g.ptrTy, g.builder.CreateStructGEP(g.dynArrTy, dataPtr, 0, ""), "")
	realLenDyn := g.builder.CreateLoad(g.i32Ty, g.builder.CreateStructGEP(g.dynArrTy, dataPtr, 1, ""), "")
	dynEndBB := g.builder.GetInsertBlock()
	g.builder.CreateBr(lenBB)

	g.builder.SetInsertPointAtEnd(lenBB)
	realLen := g.builder.CreatePHI(g.i32Ty, "")
	realLen.AddIncoming([]llvm.Value{descArrayLen, realLenDyn}, []llvm.BasicBlock{fixedEndBB, dynEndBB})
	basePtr := g.builder.CreatePHI(g.ptrTy, "")
	basePtr.AddIncoming([]llvm.Value{dataPtr, realDataPtr}, []llvm.BasicBlock{fixedEndBB, dynEndBB})

	geZero := g.builder.CreateICmp(llvm.IntSGE, idx, llvm.ConstInt(g.i32Ty, 0, true), "")
	ltLen := g.builder.CreateICmp(llvm.IntSLT, idx, realLen, "")
	inBounds := g.builder.CreateAnd(geZero, ltLen, "")
	g.builder.CreateCondBr(inBounds, matchBB, failBB)

	g.builder.SetInsertPointAtEnd(matchBB)
	idx64 := g.builder.CreateSExt(idx, g.i64Ty, "")
	byteOffset := g.builder.CreateMul(idx64, elemSize, "")
	elemAddr := g.builder.CreateInBoundsGEP(g.i8Ty, basePtr, []llvm.Value{byteOffset}, "")
	matchedAny := llvm.Undef(g.anyTy)
	matchedAny = g.builder.CreateInsertValue(matchedAny, elemAddr, 0, "")
	matchedAny = g.builder.CreateInsertValue(matchedAny, elemDescPtr, 1, "")
	matchEndBB := g.builder.GetInsertBlock()
	g.builder.CreateBr(contBB)

	g.builder.SetInsertPointAtEnd(failBB)
	zeroAny := llvm.ConstNull(g.anyTy)
	failEndBB := g.builder.GetInsertBlock()
	g.builder.CreateBr(contBB)

	g.builder.SetInsertPointAtEnd(contBB)
	anyPhi := g.builder.CreatePHI(g.anyTy, "")
	anyPhi.AddIncoming([]llvm.Value{matchedAny, zeroAny}, []llvm.BasicBlock{matchEndBB, failEndBB})
	okPhi := g.builder.CreatePHI(g.boolTy, "")
	okPhi.AddIncoming(
		[]llvm.Value{llvm.ConstInt(g.boolTy, 1, false), llvm.ConstInt(g.boolTy, 0, false)},
		[]llvm.BasicBlock{matchEndBB, failEndBB},
	)

	agg := llvm.Undef(g.llvmType(resultType))
	agg = g.builder.CreateInsertValue(agg, anyPhi, 0, "")
	agg = g.builder.CreateInsertValue(agg, okPhi, 1, "")
	return agg
}
