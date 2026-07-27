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
// model), and one per *sema.StructInfo (structAnyDescs), built lazily the
// first time that struct is actually boxed anywhere in the program.
//
//	typeDescriptorTy  = { i32 kind, string name, i32 fieldCount, ptr fieldsPtr }
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
	g.typeDescriptorTy = g.ctx.StructType([]llvm.Type{g.i32Ty, g.stringTy, g.i32Ty, g.ptrTy}, false)
	g.fieldDescriptorTy = g.ctx.StructType([]llvm.Type{g.stringTy, g.ptrTy, g.i64Ty}, false)

	g.anyPrimitiveDescs = make(map[sema.TypeKind]llvm.Value, len(anyPrimitiveKinds))
	g.structAnyDescs = make(map[*sema.StructInfo]llvm.Value)
	for _, k := range anyPrimitiveKinds {
		name := anyPrimitiveDisplayName(k)
		g.anyPrimitiveDescs[k] = g.buildTypeDescriptorGlobal(k, name, 0, llvm.ConstNull(g.ptrTy), ".any.desc."+name)
	}
}

// buildTypeDescriptorGlobal emits one new, private, read-only
// TypeDescriptor global and returns its address - shared by every primitive
// descriptor (setupAnyRuntime) and structDescriptor below.
func (g *Generator) buildTypeDescriptorGlobal(kind sema.TypeKind, name string, fieldCount int, fieldsPtr llvm.Value, globalName string) llvm.Value {
	descConst := g.ctx.ConstStruct([]llvm.Value{
		llvm.ConstInt(g.i32Ty, uint64(kind), false),
		g.constStringValue(name),
		llvm.ConstInt(g.i32Ty, uint64(fieldCount), false),
		fieldsPtr,
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

// typeDescriptorFor returns t's own shared type descriptor - a primitive
// lookup for anything not TypeStruct, or structDescriptor's lazy build/
// intern for a struct.
func (g *Generator) typeDescriptorFor(t sema.Type) llvm.Value {
	if t.Kind == sema.TypeStruct {
		return g.structDescriptor(t.Struct)
	}
	desc, ok := g.anyPrimitiveDescs[t.Kind]
	if !ok {
		panic(fmt.Sprintf("codegen: %s has no Any descriptor - isBoxableIntoAny should have rejected boxing it", t))
	}
	return desc
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

	desc := g.buildTypeDescriptorGlobal(sema.TypeStruct, info.Symbol.Name, fieldCount, fieldsPtr, ".any.desc."+info.Symbol.Name)
	g.structAnyDescs[info] = desc
	return desc
}

// genAnyBox lowers `Any(x)` (see genConversion's own TypeAny dispatch): x's
// bytes are copied into a fresh arena slot (never a stack address - see this
// file's top-of-file doc comment for why), paired with from's own shared
// type descriptor.
func (g *Generator) genAnyBox(from sema.Type, v llvm.Value) llvm.Value {
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
	if target.Kind == sema.TypeStruct {
		// Two different structs both report sema.TypeStruct as their kind -
		// descriptor pointer identity (structDescriptor interns one shared
		// descriptor per *sema.StructInfo) is what actually tells them apart.
		wantDesc := g.structDescriptor(target.Struct)
		sameDesc := g.builder.CreateICmp(llvm.IntEQ, descPtr, wantDesc, "")
		matches = g.builder.CreateAnd(matches, sameDesc, "")
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
