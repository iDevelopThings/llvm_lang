// This file lowers everything specific to sema.TypeEnum (see LANGUAGE.md's
// "Enums" and "match" sections, and CODEGEN.md's "Enums" section for the
// representation this implements): variant construction, the destructor
// pass, `==`/`!=` and `print()`'s real runtime discriminant dispatch, and
// `match`'s own real multi-way branch. Kept in its own file, mirroring
// maps.go's own precedent for a self-contained subsystem, rather than
// scattering enum-specific cases across expr.go/stmt.go/runtime.go alongside
// every other type kind's own handling.
//
// Representation recap (see types.go's setupTypes for the authoritative
// version): every enum value is the one shared LLVM struct {i32, ptr} -
// {discriminant, payload}. The payload is always an opaque arena-allocated
// pointer (never a stack address - an enum value is passed/returned by
// value like a struct/array/string elsewhere in this package, so a
// constructing function's own stack frame must never be what a returned
// enum value's payload depends on), pointing at a per-variant payload
// struct (built by declareEnumLayout below) holding that variant's own
// associated data in declaration order - null for a unit variant, which has
// none. This sidesteps every forward-reference/self-size problem a
// recursive variant (`Cons(i32, *List)`) or an enum-of-enum field would
// otherwise raise: a pointer is always just g.ptrTy, regardless of what it
// points to, so a variant's own payload type never needs another enum's
// (or its own) layout to already be complete.
package codegen

import (
	"llvm_lang/src/ast"
	"llvm_lang/src/enums"
	"llvm_lang/src/sema"

	"tinygo.org/x/go-llvm"
)

// enumLayout is one enum type's own per-variant payload shape - the
// enum-kind counterpart to structLayout, but much smaller: the *outer* enum
// value never needs its own named LLVM type (every enum shares g.enumValTy -
// see setupTypes), so all this actually caches is each variant's own
// unnamed payload struct type (empty for a unit variant, since it carries no
// associated data at all) and each payload field's own sema.Type in the
// identical order, needed for equality/print/pattern-binding codegen to
// recurse correctly without re-deriving it from the AST every time.
type enumLayout struct {
	variantPayloadType  map[*sema.EnumVariant]llvm.Type
	variantPayloadTypes map[*sema.EnumVariant][]sema.Type
}

// declareEnumLayout builds decl's (an EnumDecl's) own enumLayout - one pass,
// not a declare-then-define split the way structs need (see genPackage's own
// doc comment on this pass for why no enum ever has a forward-reference
// problem here): every variant's own payload struct type is built directly
// from its own already-resolved associated-data Types (EnumVariant.Tuple/
// Fields, populated by sema.checkEnumDecl).
func (g *Generator) declareEnumLayout(decl ast.NodeIndex) {
	nameNode := g.tree.Child(decl, 0)
	info := g.info.Enums[g.tree.Text(nameNode)]

	layout := &enumLayout{
		variantPayloadType:  make(map[*sema.EnumVariant]llvm.Type, len(info.Order)),
		variantPayloadTypes: make(map[*sema.EnumVariant][]sema.Type, len(info.Order)),
	}
	for _, variant := range info.Order {
		fieldTypes := enumVariantFieldSemaTypes(variant)
		llTypes := make([]llvm.Type, len(fieldTypes))
		for i, ft := range fieldTypes {
			llTypes[i] = g.llvmType(ft)
		}
		layout.variantPayloadType[variant] = g.ctx.StructType(llTypes, false)
		layout.variantPayloadTypes[variant] = fieldTypes
	}
	g.enumLayouts[info] = layout
}

// enumVariantFieldSemaTypes returns variant's own associated-data types, in
// payload-struct order, regardless of whether it's a tuple or struct variant
// (nil for a unit variant) - the one place both EnumVariant.Tuple/Fields
// shapes are flattened into the single ordered []sema.Type every codegen
// call site here actually needs.
func enumVariantFieldSemaTypes(variant *sema.EnumVariant) []sema.Type {
	switch variant.Kind {
	case sema.EnumVariantTuple:
		return variant.Tuple
	case sema.EnumVariantStruct:
		types := make([]sema.Type, len(variant.Fields))
		for i, f := range variant.Fields {
			types[i] = f.Type
		}
		return types
	default:
		return nil
	}
}

// declareEnumDestructorSignature declares dtor's (a DestructorDecl nested in
// an EnumDecl) LLVM function signature - the enum-kind counterpart to
// declareDestructorSignature (func.go), identical shape: an implicit first
// pointer parameter (the enum instance being destructed, addressed via the
// shared g.enumValTy - see LANGUAGE.md's "Enums" section: an enum's
// destructor fires once, regardless of which variant is actually active, so
// it needs no per-variant dispatch of its own), no parameters of its own,
// always void. Named "Enum.destructor", the same convention
// declareDestructorSignature already uses one type kind over.
func (g *Generator) declareEnumDestructorSignature(dtor ast.NodeIndex) {
	sym := g.info.Refs[dtor]
	info := sym.EnumInfo

	paramTypes := []llvm.Type{llvm.PointerType(g.enumValTy, 0)}
	fnType := llvm.FunctionType(g.voidTy, paramTypes, false)
	name := info.Symbol.Name + ".destructor"
	g.enumDtors[info] = funcEntry{
		fn:       llvm.AddFunction(g.mod, name, fnType),
		fnType:   fnType,
		retType:  sema.Type{Kind: sema.TypeVoid},
		isMethod: true,
	}
}

// genEnumDestructorBody lowers dtor's body, given its signature already
// declared (see declareEnumDestructorSignature) - mirrors genDestructorBody
// exactly, one type kind over.
func (g *Generator) genEnumDestructorBody(dtor ast.NodeIndex) {
	sym := g.info.Refs[dtor]
	entry := g.enumDtors[sym.EnumInfo]
	body := g.tree.DestructorBody(dtor)

	g.beginSyntheticFunc(entry.fn)
	g.curReceiver = g.curFn.Param(0)

	g.curFunc = &funcCtx{
		isMain:    false,
		hasReturn: false,
		retType:   sema.Type{Kind: sema.TypeVoid},
	}

	g.finishBody(body)
	g.curFunc = nil
}

// genEnumVariantValue builds an enum aggregate value {tag, payload} for
// variant, given its own associated field values already evaluated, in
// payload-struct order (nil/empty for a unit variant) - the shared
// construction path behind a unit-variant bare reference, a tuple-variant
// call, and a struct-variant composite literal (see genEnumUnitVariantValue/
// genEnumVariantCall/genEnumCompositeLitInto below). A non-unit variant's
// payload is arena-allocated (never a stack address - see this file's own
// top-of-file doc comment for why) and filled via a single aggregate store,
// exactly like this package already builds every other small aggregate
// value (llvm.Undef + CreateInsertValue per field, see e.g. genFuncValue).
func (g *Generator) genEnumVariantValue(variant *sema.EnumVariant, fieldValues []llvm.Value) llvm.Value {
	payloadPtr := llvm.ConstNull(g.ptrTy)
	if variant.Kind != sema.EnumVariantUnit {
		payloadTy := g.enumLayouts[variant.Enum].variantPayloadType[variant]
		payloadPtr = g.genArenaAlloc(llvm.SizeOf(payloadTy))
		payloadVal := llvm.Undef(payloadTy)
		for i, v := range fieldValues {
			payloadVal = g.builder.CreateInsertValue(payloadVal, v, i, "")
		}
		g.builder.CreateStore(payloadVal, payloadPtr)
	}

	enumVal := llvm.Undef(g.enumValTy)
	enumVal = g.builder.CreateInsertValue(enumVal, llvm.ConstInt(g.i32Ty, uint64(variant.Index), false), 0, "")
	enumVal = g.builder.CreateInsertValue(enumVal, payloadPtr, 1, "")
	return enumVal
}

// genEnumUnitVariantValue builds a bare, uncalled `EnumName.Variant`
// reference's own value (see LANGUAGE.md's "Enums" section: "unit variant:
// bare value... a MemberExpr naming a variant with no associated data at
// all") - genExpr's own MemberExpr case recognizes this shape (a
// SymEnumVariant reference) before ever falling through to the generic
// genLoad/genAddr path, which has no notion of "the object is a type name,
// not a value" that this reference's own object child (the enum type name
// itself) would otherwise need.
func (g *Generator) genEnumUnitVariantValue(sym *sema.Symbol) llvm.Value {
	return g.genEnumVariantValue(sym.Variant, nil)
}

// isEnumVariantCall mirrors sema's own recognition of a tuple-variant
// construction call (`Shape.Circle(5.0)` - see LANGUAGE.md's "Enums"
// section and sema's checkEnumVariantCall) - the enum-kind counterpart to
// isConstructorCall.
func (g *Generator) isEnumVariantCall(calleeNode ast.NodeIndex) bool {
	sym, ok := g.info.Refs[calleeNode]
	return ok && sym.Kind == sema.SymEnumVariant
}

// genEnumVariantCall lowers a tuple-variant construction call
// (`Shape.Circle(5.0)`).
func (g *Generator) genEnumVariantCall(calleeNode ast.NodeIndex, argNodes []ast.NodeIndex) llvm.Value {
	sym := g.info.Refs[calleeNode]
	values := make([]llvm.Value, len(argNodes))
	for i, a := range argNodes {
		values[i] = g.genExpr(a)
	}
	return g.genEnumVariantValue(sym.Variant, values)
}

// genEnumCompositeLitInto lowers a struct-variant construction literal
// (`Shape.Triangle{base: 3.0, height: 4.0}`) into dst - genCompositeLitInto's
// own TypeEnum case. Unlike a real struct's own CompositeLit case (which
// fills dst field-by-field via GEP, since a struct's own storage IS dst
// directly), an enum value is built as a whole aggregate first (its payload
// arena-allocated by genEnumVariantValue) and then stored into dst in one
// go - there's no way to GEP "the payload struct living inside dst" before
// that payload's own storage even exists yet.
func (g *Generator) genEnumCompositeLitInto(dst llvm.Value, n ast.NodeIndex) {
	typeNode, elems := g.tree.CompositeLitElems(n)
	sym := g.info.Refs[typeNode]
	variant := sym.Variant

	fieldTypes := g.enumLayouts[variant.Enum].variantPayloadTypes[variant]
	values := make([]llvm.Value, len(fieldTypes))
	for i, ft := range fieldTypes {
		values[i] = llvm.ConstNull(g.llvmType(ft))
	}

	keyed := len(elems) > 0 && g.tree.IsKeyedElement(elems[0])
	for i, e := range elems {
		idx, valueNode := g.enumVariantLitFieldSlot(variant, e, i, keyed)
		values[idx] = g.genExpr(valueNode)
	}

	g.builder.CreateStore(g.genEnumVariantValue(variant, values), dst)
}

// enumVariantLitFieldSlot is structLitFieldSlot's enum-variant counterpart -
// resolves one struct-variant composite-literal element to the payload
// field index it fills and the value expression node that supplies it.
func (g *Generator) enumVariantLitFieldSlot(variant *sema.EnumVariant, e ast.NodeIndex, i int, keyed bool) (fieldIndex int, valueNode ast.NodeIndex) {
	if !keyed {
		return i, e
	}
	sym := g.info.Refs[g.tree.Child(e, 0)]
	for idx, f := range variant.Fields {
		if f.Sym == sym {
			return idx, g.tree.Child(e, 1)
		}
	}
	return 0, g.tree.Child(e, 1) // unreachable on a tree that already passed sema.Check
}

// genEnumEqual reports whether two already-evaluated enum values of the same
// enum type are equal (see LANGUAGE.md's "Enums" section, comparability, and
// CODEGEN.md's own note on this: unlike a struct, whose every field is
// always present, an enum's active variant isn't known until runtime, so
// this needs a real conditional/switch in the generated IR, not a
// compile-time-resolved path) - genValueEqual's own TypeEnum case. First
// compares the two discriminants; only when they match does a real runtime
// switch dispatch to the active variant's own payload comparison (a unit
// variant trivially compares equal; a tuple/struct variant recurses into
// each associated value exactly like genValueEqual's own struct/array cases
// already do).
func (g *Generator) genEnumEqual(t sema.Type, lv, rv llvm.Value) llvm.Value {
	info := t.Enum
	ltag := g.builder.CreateExtractValue(lv, 0, "")
	rtag := g.builder.CreateExtractValue(rv, 0, "")
	lpay := g.builder.CreateExtractValue(lv, 1, "")
	rpay := g.builder.CreateExtractValue(rv, 1, "")

	tagsEq := g.builder.CreateICmp(llvm.IntEQ, ltag, rtag, "")

	preBB := g.builder.GetInsertBlock()
	payloadBB := g.ctx.AddBasicBlock(g.curFn, "enumeq.payload")
	mergeBB := g.ctx.AddBasicBlock(g.curFn, "enumeq.merge")
	g.builder.CreateCondBr(tagsEq, payloadBB, mergeBB)

	g.builder.SetInsertPointAtEnd(payloadBB)
	unreachableBB := g.ctx.AddBasicBlock(g.curFn, "enumeq.unreachable")
	sw := g.builder.CreateSwitch(ltag, unreachableBB, len(info.Order))

	incomingVals := make([]llvm.Value, 0, len(info.Order)+1)
	incomingBlocks := make([]llvm.BasicBlock, 0, len(info.Order)+1)
	for _, variant := range info.Order {
		caseBB := g.ctx.AddBasicBlock(g.curFn, "enumeq.case."+variant.Name)
		sw.AddCase(llvm.ConstInt(g.i32Ty, uint64(variant.Index), false), caseBB)

		g.builder.SetInsertPointAtEnd(caseBB)
		result := g.genVariantPayloadEqual(variant, lpay, rpay)
		incomingVals = append(incomingVals, result)
		incomingBlocks = append(incomingBlocks, g.builder.GetInsertBlock())
		g.builder.CreateBr(mergeBB)
	}

	g.builder.SetInsertPointAtEnd(unreachableBB)
	g.builder.CreateUnreachable()

	g.builder.SetInsertPointAtEnd(mergeBB)
	phi := g.builder.CreatePHI(g.boolTy, "")
	incomingVals = append(incomingVals, llvm.ConstInt(g.boolTy, 0, false))
	incomingBlocks = append(incomingBlocks, preBB)
	phi.AddIncoming(incomingVals, incomingBlocks)
	return phi
}

// genVariantPayloadEqual compares two enum values' own already-known-equal-
// discriminant payloads, as variant's own associated data - true
// unconditionally for a unit variant (nothing to compare), otherwise
// loading each side's payload struct and recursing field-by-field through
// genValueEqual exactly like a real struct's own equality already does.
func (g *Generator) genVariantPayloadEqual(variant *sema.EnumVariant, lpay, rpay llvm.Value) llvm.Value {
	if variant.Kind == sema.EnumVariantUnit {
		return llvm.ConstInt(g.boolTy, 1, false)
	}
	layout := g.enumLayouts[variant.Enum]
	payloadTy := layout.variantPayloadType[variant]
	fieldTypes := layout.variantPayloadTypes[variant]

	lStruct := g.builder.CreateLoad(payloadTy, lpay, "")
	rStruct := g.builder.CreateLoad(payloadTy, rpay, "")

	result := llvm.ConstInt(g.boolTy, 1, false)
	for i, ft := range fieldTypes {
		lf := g.builder.CreateExtractValue(lStruct, i, "")
		rf := g.builder.CreateExtractValue(rStruct, i, "")
		result = g.builder.CreateAnd(result, g.genValueEqual(ft, lf, rf), "")
	}
	return result
}

// genHashEnumInto mixes an enum value's own logically-relevant bits into
// seed - the map-key hashing counterpart to genEnumEqual (see maps.go's own
// genMapHash/genHashInto for why a map key's hash recurses over its own
// logical structure rather than its raw in-memory bytes), needed the moment
// an enum type is legal as a map key at all (see LANGUAGE.md's "Enums"
// section: comparable iff every variant's own data is, the same rule
// mapTypeFromNode's own key check already enforces). Always mixes the
// discriminant in first (so two different variants - even ones whose own
// payload bit patterns might otherwise coincide - never hash identically by
// construction alone), then a runtime switch dispatches into whichever
// variant is actually active, recursing through genHashInto for each of its
// own associated fields exactly like genVariantPayloadEqual recurses
// through genValueEqual for equality.
func (g *Generator) genHashEnumInto(t sema.Type, v, seed llvm.Value) llvm.Value {
	info := t.Enum
	tag := g.builder.CreateExtractValue(v, 0, "")
	payload := g.builder.CreateExtractValue(v, 1, "")
	seed = g.fnvMix(seed, tag)

	unreachableBB := g.ctx.AddBasicBlock(g.curFn, "enumhash.unreachable")
	mergeBB := g.ctx.AddBasicBlock(g.curFn, "enumhash.merge")
	sw := g.builder.CreateSwitch(tag, unreachableBB, len(info.Order))

	incomingVals := make([]llvm.Value, 0, len(info.Order))
	incomingBlocks := make([]llvm.BasicBlock, 0, len(info.Order))
	for _, variant := range info.Order {
		caseBB := g.ctx.AddBasicBlock(g.curFn, "enumhash.case."+variant.Name)
		sw.AddCase(llvm.ConstInt(g.i32Ty, uint64(variant.Index), false), caseBB)

		g.builder.SetInsertPointAtEnd(caseBB)
		result := g.hashVariantPayload(variant, payload, seed)
		incomingVals = append(incomingVals, result)
		incomingBlocks = append(incomingBlocks, g.builder.GetInsertBlock())
		g.builder.CreateBr(mergeBB)
	}

	g.builder.SetInsertPointAtEnd(unreachableBB)
	g.builder.CreateUnreachable()

	g.builder.SetInsertPointAtEnd(mergeBB)
	phi := g.builder.CreatePHI(g.i32Ty, "")
	phi.AddIncoming(incomingVals, incomingBlocks)
	return phi
}

// hashVariantPayload mixes variant's own associated data (if any) into seed
// - trivially a no-op for a unit variant, which has none.
func (g *Generator) hashVariantPayload(variant *sema.EnumVariant, payload, seed llvm.Value) llvm.Value {
	if variant.Kind == sema.EnumVariantUnit {
		return seed
	}
	layout := g.enumLayouts[variant.Enum]
	payloadTy := layout.variantPayloadType[variant]
	fieldTypes := layout.variantPayloadTypes[variant]
	payloadVal := g.builder.CreateLoad(payloadTy, payload, "")
	for i, ft := range fieldTypes {
		fv := g.builder.CreateExtractValue(payloadVal, i, "")
		seed = g.genHashInto(ft, fv, seed)
	}
	return seed
}

// genPrintEnumValue renders an enum value - genPrintCall/genPrintValueBare's
// own TypeEnum case - via a real runtime switch on its discriminant (see
// LANGUAGE.md's "Enums" section: printing "always... recurse into the
// *active* variant's own associated data", the identical runtime-dispatch
// requirement genEnumEqual already documents). Renders as the variant's own
// name, followed by its associated data (if any) in parens for a tuple
// variant (`Circle(5)`) or braces for a struct variant (`Triangle{3 4}` -
// deliberately reusing genPrintStructValue's own established "field values
// only, no names" convention, for the identical reason a real struct's own
// print already omits them) - a unit variant prints as its bare name alone
// (`Point`).
func (g *Generator) genPrintEnumValue(t sema.Type, v llvm.Value) {
	info := t.Enum
	tag := g.builder.CreateExtractValue(v, 0, "")
	payload := g.builder.CreateExtractValue(v, 1, "")

	defaultBB := g.ctx.AddBasicBlock(g.curFn, "printenum.unreachable")
	mergeBB := g.ctx.AddBasicBlock(g.curFn, "printenum.merge")
	sw := g.builder.CreateSwitch(tag, defaultBB, len(info.Order))

	for _, variant := range info.Order {
		caseBB := g.ctx.AddBasicBlock(g.curFn, "printenum.case."+variant.Name)
		sw.AddCase(llvm.ConstInt(g.i32Ty, uint64(variant.Index), false), caseBB)

		g.builder.SetInsertPointAtEnd(caseBB)
		g.genPrintEnumVariant(variant, payload)
		g.builder.CreateBr(mergeBB)
	}

	g.builder.SetInsertPointAtEnd(defaultBB)
	g.builder.CreateUnreachable()

	g.builder.SetInsertPointAtEnd(mergeBB)
}

// genPrintEnumVariant renders one already-selected variant's own name plus
// (for a non-unit variant) its associated data - the per-case body
// genPrintEnumValue's own switch dispatches to.
func (g *Generator) genPrintEnumVariant(variant *sema.EnumVariant, payload llvm.Value) {
	g.genPrintStringValueBare(g.constStringValue(variant.Name))
	if variant.Kind == sema.EnumVariantUnit {
		return
	}

	layout := g.enumLayouts[variant.Enum]
	payloadTy := layout.variantPayloadType[variant]
	fieldTypes := layout.variantPayloadTypes[variant]
	payloadVal := g.builder.CreateLoad(payloadTy, payload, "")

	open, close := g.fmtLParen, g.fmtRParen
	if variant.Kind == sema.EnumVariantStruct {
		open, close = g.fmtLBrace, g.fmtRBrace
	}

	g.genPrintLiteral(open)
	for i, ft := range fieldTypes {
		if i > 0 {
			g.genPrintLiteral(g.fmtSpace)
		}
		fv := g.builder.CreateExtractValue(payloadVal, i, "")
		g.genPrintValueBare(ft, fv)
	}
	g.genPrintLiteral(close)
}

// genMatchStmt lowers a `match subject { pattern => body, ... }` (see
// LANGUAGE.md's "match" section), dispatching on the subject's own type to
// one of two genuinely different lowering strategies (see CODEGEN.md's
// "match codegen" section): an enum subject gets a real LLVM `switch` on
// its compile-time-constant discriminant (this function, below - unchanged
// from before this round), while a plain scalar (int/bool/string) subject
// is routed to genValueMatchStmt instead - a value pattern isn't a
// compile-time-constant discriminant LLVM's `switch` instruction needs, so
// it needs a genuinely different runtime-comparison-chain lowering, kept in
// its own dedicated function rather than tangled into this one (see AGENTS.md's
// layering/no-mixing-concerns standard).
//
// frame is nil for an ordinary statement-position match (genStmt's own
// dispatch calls this with nil, exactly as before this round) - non-nil
// only when genMatchExpr (this file, below) is lowering an expression-
// position match instead, in which case every arm's own merge point is
// frame's own shared mergeBB (matchMergeBB) rather than a fresh one created
// here, so that every yield anywhere in either lowering strategy's own arms
// branches into the exact same block genMatchExpr will build its final phi
// against. This is the ONLY thing frame changes about this function's own
// switch-construction logic below - reused completely unchanged otherwise,
// per AGENTS.md's no-duplication standard (see genMatchExpr's own doc
// comment for the full reasoning).
//
// The enum path: load the discriminant, then a real multi-way branch
// (LLVM's own `switch` instruction, matching against each variant's own
// assigned discriminant index) - each arm's block extracts/loads the
// payload into its bound local names (if any), runs the arm's body, then
// branches to the match's own single merge/exit block. The wildcard arm
// (if present) becomes the switch's default destination; if no wildcard
// exists (a fully-exhaustive match - sema's own checkEnumMatchStmt already
// guarantees this on a tree that passed sema.Check), the default
// destination is `unreachable`, matching this project's own existing
// "genuinely impossible per sema's own guarantee" convention already used
// elsewhere for defensive backstops (see emitFallbackTerminator, func.go).
func (g *Generator) genMatchStmt(n ast.NodeIndex, frame *matchExprCodegenCtx) bool {
	subjectNode := g.tree.MatchSubject(n)
	subjType := g.info.Types[subjectNode]

	enumType := subjType
	if enumType.Kind == sema.TypePointer && enumType.Elem != nil {
		enumType = *enumType.Elem
	}
	if enumType.Kind != sema.TypeEnum {
		return g.genValueMatchStmt(n, subjType, frame)
	}

	var enumVal llvm.Value
	if subjType.Kind == sema.TypePointer {
		ptr := g.genExpr(subjectNode)
		enumVal = g.builder.CreateLoad(g.enumValTy, ptr, "")
	} else {
		enumVal = g.genExpr(subjectNode)
	}

	tag := g.builder.CreateExtractValue(enumVal, 0, "")
	payload := g.builder.CreateExtractValue(enumVal, 1, "")

	// preMatch snapshots Generator.destructors before any arm's own body has
	// generated anything - the match-statement counterpart to genIfStmt's own
	// preIf snapshot (stmt.go), generalized from two mutually exclusive
	// branches to N: every arm here is an independent switch case, only one
	// of which ever actually executes at runtime, but codegen still
	// generates every one of them sequentially against the *same* shared
	// Generator.destructors slice. See snapshotDestructors/restoreDestructors
	// (stmt.go) for the full reasoning this shares with genIfStmt.
	preMatch := g.snapshotDestructors()

	arms := g.tree.MatchArms(n)
	mergeBB := g.matchMergeBB(frame, "match.merge")

	var wildcardArm ast.NodeIndex = ast.InvalidNode
	variantArms := make([]ast.NodeIndex, 0, len(arms))
	for _, arm := range arms {
		if g.tree.IsWildcardMatchArm(arm) {
			wildcardArm = arm
			continue
		}
		variantArms = append(variantArms, arm)
	}

	defaultBB := mergeBB
	var wildcardTerminated bool
	if wildcardArm != ast.InvalidNode {
		defaultBB = g.ctx.AddBasicBlock(g.curFn, "match.wildcard")
	} else {
		defaultBB = g.ctx.AddBasicBlock(g.curFn, "match.unreachable")
	}

	sw := g.builder.CreateSwitch(tag, defaultBB, len(variantArms))

	allTerminated := true
	for _, arm := range variantArms {
		pattern := g.tree.MatchArmPattern(arm)
		variant := g.matchArmVariant(pattern)

		caseBB := g.ctx.AddBasicBlock(g.curFn, "match.case."+variant.Name)
		sw.AddCase(llvm.ConstInt(g.i32Ty, uint64(variant.Index), false), caseBB)

		g.builder.SetInsertPointAtEnd(caseBB)
		g.restoreDestructors(preMatch)
		terminated := g.genMatchArm(arm, pattern, variant, payload)
		if !terminated {
			g.builder.CreateBr(mergeBB)
		}
		allTerminated = allTerminated && terminated
	}

	g.builder.SetInsertPointAtEnd(defaultBB)
	g.restoreDestructors(preMatch)
	if wildcardArm != ast.InvalidNode {
		wildcardTerminated = g.genBlock(g.tree.MatchArmBody(wildcardArm))
		if !wildcardTerminated {
			g.builder.CreateBr(mergeBB)
		}
	} else {
		g.builder.CreateUnreachable()
	}
	allTerminated = allTerminated && (wildcardArm == ast.InvalidNode || wildcardTerminated)

	// Whatever follows the match (reached only when at least one arm didn't
	// terminate) must see exactly the pre-match state too, never a
	// bookkeeping side effect the last-generated arm happened to leave
	// behind - the same restore-once-more-afterward step genIfStmt's own
	// save-restore takes after both branches.
	g.restoreDestructors(preMatch)

	// mergeBB is unreachable itself only when every arm (including the
	// wildcard, if any) always terminates AND this is a genuine statement-
	// position match (frame == nil) - sema's own checkEnumMatchStmt already
	// guarantees exhaustiveness, so the only remaining question codegen
	// itself needs to answer there is whether every reachable arm body
	// actually terminates; if so, nothing ever branches into mergeBB, but
	// LLVM still requires every basic block that exists at all to end in a
	// real terminator (see AGENTS.md's "Missing return" section and
	// emitFallbackTerminator, func.go, for this project's own identical
	// "genuinely impossible per sema's own guarantee, so unreachable
	// documents it directly" convention) - matching isTerminatingStmt's own
	// sema-side verdict (matchStmtTerminates) exactly. When frame != nil
	// (an expression-position match - see genMatchExpr), mergeBB is instead
	// genuinely reachable - every yield anywhere in any arm branches
	// straight into it (genYieldStmt) regardless of this function's own
	// allTerminated bookkeeping, which genMatchExpr's own caller ignores
	// entirely - so it must never be marked unreachable here; genMatchExpr
	// itself finishes it with a real CreatePHI once every arm is done.
	g.builder.SetInsertPointAtEnd(mergeBB)
	if frame == nil && allTerminated {
		g.builder.CreateUnreachable()
	}
	return allTerminated
}

// matchMergeBB returns the basic block a match's own arms should all
// eventually branch into once done - frame's own mergeBB (already created by
// genMatchExpr before dispatching here, shared across the enum/value
// dispatch so a yield's own genYieldStmt branches into the SAME block
// regardless of which of the two lowering strategies actually produced the
// arm it's inside) when this match is being lowered in expression mode, or
// a fresh block otherwise (name is only used for the fresh-block case - a
// frame's own mergeBB already has its own name from when genMatchExpr
// created it).
func (g *Generator) matchMergeBB(frame *matchExprCodegenCtx, name string) llvm.BasicBlock {
	if frame != nil {
		return frame.mergeBB
	}
	return g.ctx.AddBasicBlock(g.curFn, name)
}

// genMatchExpr lowers a `match` used in EXPRESSION position (see
// LANGUAGE.md's "match" section's "match as an expression" subsection) -
// reached via genExpr's own dispatch (a MatchStmt node reached there is
// always this flavor; a statement-position match is genStmt's own
// MatchStmt case, genMatchStmt(n, nil), completely unchanged - see
// ast.Node's own MatchStmt doc comment for how the two are told apart, and
// the regression test proving a bare top-level `match x {...}` statement
// still goes through the unchanged path).
//
// Pushes a fresh matchExprCodegenCtx frame (destructorBase = len(g.destructors)
// right now, this match expression's own entry point - not the enclosing
// function's, not any enclosing loop's, see genYieldStmt) with its own
// fresh mergeBB, then reuses genMatchStmt's entire existing enum-vs-value
// dispatch/switch/comparison-chain lowering completely unchanged, just
// threading this frame through instead of nil - see genMatchStmt's own doc
// comment for exactly what sharing that gets: nothing here duplicates the
// switch-on-discriminant or comparison-chain construction (AGENTS.md's
// layering/no-duplication standard), only genMatchExpr's own tail is
// genuinely new: a real CreatePHI collecting every yield's contributed
// value, built and populated in one batched AddIncoming call once every arm
// has finished generating - matching every other phi call site in this
// package (genEnumEqual/genHashEnumInto just above, expr.go's short-circuit
// `&&`, maps.go, runtime.go: none of them call AddIncoming incrementally
// either, every one batches its full incoming slices into one call at the
// end) rather than an incremental per-yield AddIncoming call. The builder is
// left positioned at mergeBB when this returns, exactly like any other
// phi-producing expression in this package - mergeBB is never marked
// unreachable (see genMatchStmt/genValueMatchStmt's own frame != nil
// carve-out): it's genuinely reachable code, hosting the phi and whatever
// the caller emits next.
func (g *Generator) genMatchExpr(n ast.NodeIndex) llvm.Value {
	frame := &matchExprCodegenCtx{
		destructorBase: len(g.destructors),
		mergeBB:        g.ctx.AddBasicBlock(g.curFn, "match.expr.merge"),
	}
	g.matchExprStack = append(g.matchExprStack, frame)
	g.genMatchStmt(n, frame)
	g.matchExprStack = g.matchExprStack[:len(g.matchExprStack)-1]

	g.builder.SetInsertPointAtEnd(frame.mergeBB)
	resultTy := g.llvmType(g.info.Types[n])
	phi := g.builder.CreatePHI(resultTy, "")
	phi.AddIncoming(frame.incomingVals, frame.incomingBlocks)
	return phi
}

// genValueMatchStmt lowers a `match subject { pattern0, pattern1 => body,
// ... }` statement whose subject is a plain scalar value (int/bool/string -
// see LANGUAGE.md's "match" section's plain-value-pattern extension and
// sema's isValueMatchType), genMatchStmt's own dispatch target whenever the
// subject isn't an enum. A value pattern's own runtime equality isn't a
// compile-time-constant discriminant LLVM's `switch` instruction needs the
// way an enum variant's own discriminant is, so this lowers to something
// genuinely different: the subject is evaluated once, then each arm (in
// source order) becomes a short-circuit chain of runtime equality
// comparisons (genValueEqual - the exact same scalar-equality codegen an
// ordinary `==` operator already uses, reused verbatim rather than
// reinvented) against every one of that arm's own patterns, branching into
// that arm's shared body block on the first match. The mandatory wildcard
// arm (sema's own checkValueMatchStmt already guarantees exactly one is
// present) becomes the unconditional final fallback - tested last,
// regardless of where it's actually written among the source arms, exactly
// like Go's own `default` case needs no particular position either.
//
// Applies the identical preMatch destructor snapshot/restore discipline
// genMatchStmt's own enum path already uses, at every arm and once more
// after the whole statement - see that function's own doc comment for why:
// every arm here is an independent, mutually exclusive branch generated
// sequentially against the same shared Generator.destructors slice. frame
// is genMatchStmt's own frame parameter, threaded straight through
// unchanged - see that function's own doc comment for what it means.
func (g *Generator) genValueMatchStmt(n ast.NodeIndex, subjType sema.Type, frame *matchExprCodegenCtx) bool {
	subjectNode := g.tree.MatchSubject(n)
	subjVal := g.genExpr(subjectNode)

	arms := g.tree.MatchArms(n)
	mergeBB := g.matchMergeBB(frame, "match.merge")

	var wildcardArm ast.NodeIndex = ast.InvalidNode
	valueArms := make([]ast.NodeIndex, 0, len(arms))
	for _, arm := range arms {
		if g.tree.IsWildcardMatchArm(arm) {
			wildcardArm = arm
			continue
		}
		valueArms = append(valueArms, arm)
	}

	preMatch := g.snapshotDestructors()

	allTerminated := true
	for _, arm := range valueArms {
		patterns := g.tree.MatchArmPatterns(arm)
		bodyBB := g.ctx.AddBasicBlock(g.curFn, "match.case")
		nextBB := g.ctx.AddBasicBlock(g.curFn, "match.next")

		for i, pattern := range patterns {
			patVal := g.genExpr(pattern)
			eq := g.genValueEqual(subjType, subjVal, patVal)

			testBB := nextBB
			if i < len(patterns)-1 {
				testBB = g.ctx.AddBasicBlock(g.curFn, "match.test")
			}
			g.builder.CreateCondBr(eq, bodyBB, testBB)
			g.builder.SetInsertPointAtEnd(testBB)
		}

		g.builder.SetInsertPointAtEnd(bodyBB)
		g.restoreDestructors(preMatch)
		terminated := g.genBlock(g.tree.MatchArmBody(arm))
		if !terminated {
			g.builder.CreateBr(mergeBB)
		}
		allTerminated = allTerminated && terminated

		// Generating the arm's own body (just above) moved the builder's
		// insert point away from nextBB (into bodyBB, and wherever the
		// body's own nested control flow left it) - explicitly reposition
		// back to nextBB, the block the pattern-testing loop above already
		// wired as this arm's own false-edge continuation, before the next
		// arm's own test chain (or, on the last arm, the wildcard's own
		// body, just below) starts generating into it.
		g.builder.SetInsertPointAtEnd(nextBB)
	}

	// The last arm's own nextBB (or, when there are no non-wildcard arms at
	// all, the same block subjVal was evaluated in - the loop above never
	// ran, so the builder is still positioned exactly there) is the
	// wildcard's own body - sema's own checkValueMatchStmt already
	// guarantees it's present.
	g.restoreDestructors(preMatch)
	wildcardTerminated := g.genBlock(g.tree.MatchArmBody(wildcardArm))
	if !wildcardTerminated {
		g.builder.CreateBr(mergeBB)
	}
	allTerminated = allTerminated && wildcardTerminated

	// Whatever follows the match (reached only when at least one arm didn't
	// terminate) must see exactly the pre-match state too, never a
	// bookkeeping side effect the last-generated arm happened to leave
	// behind - the same restore-once-more-afterward step genMatchStmt's own
	// enum path (and genIfStmt's save-restore) already takes.
	g.restoreDestructors(preMatch)

	// mergeBB is unreachable itself only when every arm (including the
	// wildcard) always terminates AND this is a genuine statement-position
	// match (frame == nil) - matching genMatchStmt's own enum-path reasoning
	// and matchStmtTerminates' identical sema-side verdict exactly (see
	// AGENTS.md's "Missing return" section and emitFallbackTerminator,
	// func.go, for this project's own "genuinely impossible per sema's own
	// guarantee, so unreachable documents it directly" convention) - see
	// genMatchStmt's own identical frame != nil carve-out just above for why
	// an expression-position match's mergeBB must never be marked
	// unreachable here regardless of allTerminated.
	g.builder.SetInsertPointAtEnd(mergeBB)
	if frame == nil && allTerminated {
		g.builder.CreateUnreachable()
	}
	return allTerminated
}

// matchArmVariant resolves pattern's own already-checked variant reference
// (see sema's checkedPatternVariant) - the MemberExpr node itself for a
// unit-variant pattern, or its leading callee/type-expr child for a tuple-/
// struct-variant one.
func (g *Generator) matchArmVariant(pattern ast.NodeIndex) *sema.EnumVariant {
	var memberNode ast.NodeIndex
	switch g.tree.Nodes[pattern].Kind {
	case enums.NodeKinds.MemberExpr:
		memberNode = pattern
	default:
		memberNode = g.tree.Child(pattern, 0)
	}
	return g.info.Refs[memberNode].Variant
}

// genMatchArm lowers one non-wildcard arm's own bindings (if any) and body,
// given the builder already positioned at that arm's own switch-case block -
// reports whether the arm's own body terminated (so genMatchStmt knows
// whether to branch to its own merge block afterward).
func (g *Generator) genMatchArm(arm, pattern ast.NodeIndex, variant *sema.EnumVariant, payload llvm.Value) bool {
	if variant.Kind != sema.EnumVariantUnit {
		layout := g.enumLayouts[variant.Enum]
		payloadTy := layout.variantPayloadType[variant]
		payloadVal := g.builder.CreateLoad(payloadTy, payload, "")
		g.bindMatchArmPattern(pattern, variant, payloadVal)
	}
	return g.genBlock(g.tree.MatchArmBody(arm))
}

// bindMatchArmPattern allocates a fresh local slot for each of pattern's own
// bound names (a tuple pattern's positional bindings, or a struct pattern's
// own keyed bindings), storing each one's own extracted payload field value -
// exactly the same allocLocalSlot/g.locals/pushDestructorEntry shape any
// other local declaration in this package already goes through, so a
// reference to a bound name inside the arm's own body works completely
// unchanged (genAddr's own Ident case).
func (g *Generator) bindMatchArmPattern(pattern ast.NodeIndex, variant *sema.EnumVariant, payloadVal llvm.Value) {
	switch g.tree.Nodes[pattern].Kind {
	case enums.NodeKinds.CallExpr:
		bindings := g.tree.Children(pattern)[1:]
		for i, b := range bindings {
			if i >= len(variant.Tuple) {
				continue
			}
			g.bindPatternName(b, variant.Tuple[i], g.builder.CreateExtractValue(payloadVal, i, ""))
		}
	case enums.NodeKinds.CompositeLit:
		_, elems := g.tree.CompositeLitElems(pattern)
		for _, e := range elems {
			if !g.tree.IsKeyedElement(e) {
				continue
			}
			keyNode := g.tree.Child(e, 0)
			valueNode := g.tree.Child(e, 1)
			fieldSym := g.info.Refs[keyNode]
			idx, field := enumFieldIndexBySym(variant, fieldSym)
			if idx < 0 {
				continue
			}
			g.bindPatternName(valueNode, field.Type, g.builder.CreateExtractValue(payloadVal, idx, ""))
		}
	}
}

// enumFieldIndexBySym finds fieldSym's own positional index within variant's
// Fields (a struct-variant pattern's keyed binding resolves its field by
// Symbol identity, mirroring structLitFieldSlot's identical lookup).
func enumFieldIndexBySym(variant *sema.EnumVariant, fieldSym *sema.Symbol) (int, sema.EnumField) {
	for i, f := range variant.Fields {
		if f.Sym == fieldSym {
			return i, f
		}
	}
	return -1, sema.EnumField{}
}

// bindPatternName allocates a fresh local slot for one match pattern's own
// bound name, storing v (its extracted payload field value) into it and
// registering the slot into g.locals - the exact same shape genShortVarDecl
// already uses for an ordinary `:=` declaration, just without an rvalue
// expression node to evaluate (v is already computed).
func (g *Generator) bindPatternName(nameNode ast.NodeIndex, t sema.Type, v llvm.Value) {
	sym := g.info.Refs[nameNode]
	llt := g.llvmType(t)
	addr := g.allocLocalSlot(sym, llt, sym.Name)
	g.locals[sym] = addr
	g.pushDestructorEntry(sym, t)
	g.builder.CreateStore(v, addr)
}
