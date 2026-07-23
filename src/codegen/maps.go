package codegen

import (
	"llvm_lang/src/ast"
	"llvm_lang/src/sema"

	"tinygo.org/x/go-llvm"
)

// This file lowers `map[K]V` (see LANGUAGE.md's "Maps" section) - a real hash
// table, backed by the exact same arena allocator (genArenaAlloc/
// genArenaAllocElems) every other heap-needing feature in this package
// already routes through (dynamic arrays, string concatenation): never freed
// individually, consistent with this project's already-documented "the arena
// never frees" philosophy (see CODEGEN.md's "The arena allocator" section).
//
// See CODEGEN.md's own "Maps" section for the full documented design (bucket
// layout, hash function, growth trigger, collision strategy) - this comment
// only orients the code itself.
//
// Representation, in one sentence: a map value is a single opaque `ptr` -
// exactly like a pointer (sema.TypePointer, see llvmType) - pointing at a
// small, arena-allocated "control block" (g.mapCtrlTy: {ptr buckets, i32
// count, i32 bucketCount, i32 tombstoneCount}); the control block's own
// address never changes across the map's lifetime (so every copy of the map
// "header" - assigning one map-typed variable to another - shares the exact
// same buckets/count/tombstoneCount, Go's own real map-is-a-reference-type
// behavior), only what its buckets field points at (and bucketCount) changes,
// in place, when the table grows.
//
// tombstoneCount tracks slots left behind by remove(m, k) (mapTagTombstone,
// below) separately from count (live entries only): genMapGrowIfNeeded's own
// load-factor check must trigger off how *full* the bucket array actually is
// - live entries plus tombstones both occupy a real slot and lengthen probe
// sequences exactly the same way - not off live count alone, or a
// churn-heavy insert/remove/insert workload could pack the bucket array
// almost entirely with tombstones while count stays low, degrading every
// probe toward an O(bucketCount) scan without ever resizing. Reset to 0
// whenever the table actually grows (genMapGrowIfNeeded's rehash starts every
// still-occupied entry fresh into an all-empty array, carrying no
// tombstones - see genMapRemoveCall for where it's incremented).

// mapInitialBuckets is the bucket count a freshly make'd map starts with -
// always a power of two (so future growth by doubling stays a power of two
// too, though nothing here actually depends on that beyond it being a
// reasonable, simple starting size).
const mapInitialBuckets = 8

// Bucket tag values - one extra i8 field per bucket entry (see
// mapBucketType) distinguishing an empty slot, a live entry, and a
// tombstone (a deleted entry's slot - kept distinct from empty so an open-
// addressing probe sequence started before the deletion still correctly
// walks past it to whatever comes after, rather than stopping early as if
// the key were never present at all).
const (
	mapTagEmpty     = 0
	mapTagOccupied  = 1
	mapTagTombstone = 2
)

// FNV-1a's standard 32-bit offset basis/prime - see genMapHash's own doc
// comment for why this project's own hash combinator uses these as a mixing
// constant rather than a literal byte-for-byte FNV-1a pass.
const (
	fnvOffsetBasis32 = 2166136261
	fnvPrime32       = 16777619
)

// setupMapTypes builds g.mapCtrlTy and the map-nil-write trap's cached
// format-string global - called once, alongside setupTypes/setupRuntime, in
// GeneratePackage. mapCtrlTy is the one shared control-block layout every
// map instantiation uses regardless of its own K/V types (mirroring
// dynArrTy's own "one shared struct type serves every element type"
// reasoning - see CODEGEN.md's "Dynamic arrays" section): its own `buckets`
// field is already an opaque `ptr`, so the actual bucket layout (which does
// vary per K/V - see mapBucketType) only ever matters at the point some code
// computes an address into the bucket array itself, never in the control
// block's own shape.
func (g *Generator) setupMapTypes() {
	g.mapCtrlTy = g.ctx.StructType([]llvm.Type{g.ptrTy, g.i32Ty, g.i32Ty, g.i32Ty}, false)
	g.fmtMapNilTrap = g.defineCString(".fmt.trap.mapnil", "runtime error: assignment to entry in nil map\n")
}

// mapBucketType builds the (unnamed, structurally-interned by LLVM itself -
// see ctx.StructType's own semantics, no caching needed here for the same
// reason TypeMultiReturn's own llvmType case needs none) LLVM struct type for
// one bucket of a map[keyT]valT: {i8 tag, keyT key, valT value}. Computed
// fresh at every call site that needs it rather than cached per (K, V) pair -
// LLVM's own context already deduplicates two structurally-identical unnamed
// struct types, so a Generator-side cache would only save a little
// bookkeeping, not a real allocation.
func (g *Generator) mapBucketType(keyT, valT sema.Type) llvm.Type {
	return g.ctx.StructType([]llvm.Type{g.i8Ty, g.llvmType(keyT), g.llvmType(valT)}, false)
}

// genMapMake lowers `make(map[K]V)` (see LANGUAGE.md's "Maps" section):
// arena-allocates one control block plus one zero-filled, mapInitialBuckets-
// sized bucket array, wires the former to point at the latter, and returns
// the control block's own address - the map's whole runtime value. Zero-
// filling the bucket array (memset, exactly like genMakeCall's dynamic-array
// buffer) is what makes every bucket start life with tag == mapTagEmpty for
// free, with no separate per-bucket initialization loop needed.
func (g *Generator) genMapMake(mapType sema.Type) llvm.Value {
	bucketTy := g.mapBucketType(*mapType.Key, *mapType.Elem)

	ctrlPtr := g.genArenaAlloc(llvm.SizeOf(g.mapCtrlTy))

	initialCount := llvm.ConstInt(g.i32Ty, mapInitialBuckets, false)
	bucketsPtr, _, totalBytes := g.genArenaAllocElems(bucketTy, initialCount)
	g.builder.CreateCall(g.memsetType, g.memsetFn, []llvm.Value{bucketsPtr, llvm.ConstInt(g.i32Ty, 0, false), totalBytes}, "")

	bucketsAddr := g.builder.CreateStructGEP(g.mapCtrlTy, ctrlPtr, 0, "")
	countAddr := g.builder.CreateStructGEP(g.mapCtrlTy, ctrlPtr, 1, "")
	bucketCountAddr := g.builder.CreateStructGEP(g.mapCtrlTy, ctrlPtr, 2, "")
	tombstoneCountAddr := g.builder.CreateStructGEP(g.mapCtrlTy, ctrlPtr, 3, "")
	g.builder.CreateStore(bucketsPtr, bucketsAddr)
	g.builder.CreateStore(llvm.ConstInt(g.i32Ty, 0, false), countAddr)
	g.builder.CreateStore(initialCount, bucketCountAddr)
	g.builder.CreateStore(llvm.ConstInt(g.i32Ty, 0, false), tombstoneCountAddr)

	return ctrlPtr
}

// genMapLenValue lowers `len(m)` for an already-evaluated map value: 0 for a
// nil (never-made) map, matching Go's own real "len of a nil map is 0" rule,
// or the control block's own live `count` field otherwise.
func (g *Generator) genMapLenValue(mapVal llvm.Value) llvm.Value {
	isNil := g.builder.CreateICmp(llvm.IntEQ, mapVal, llvm.ConstNull(g.ptrTy), "")

	nilBB := g.ctx.AddBasicBlock(g.curFn, "map.len.nil")
	liveBB := g.ctx.AddBasicBlock(g.curFn, "map.len.live")
	contBB := g.ctx.AddBasicBlock(g.curFn, "map.len.cont")
	g.builder.CreateCondBr(isNil, nilBB, liveBB)

	g.builder.SetInsertPointAtEnd(nilBB)
	g.builder.CreateBr(contBB)

	g.builder.SetInsertPointAtEnd(liveBB)
	countAddr := g.builder.CreateStructGEP(g.mapCtrlTy, mapVal, 1, "")
	count := g.builder.CreateLoad(g.i32Ty, countAddr, "")
	liveEndBB := g.builder.GetInsertBlock()
	g.builder.CreateBr(contBB)

	g.builder.SetInsertPointAtEnd(contBB)
	phi := g.builder.CreatePHI(g.i32Ty, "")
	phi.AddIncoming([]llvm.Value{llvm.ConstInt(g.i32Ty, 0, false), count}, []llvm.BasicBlock{nilBB, liveEndBB})
	return phi
}

// isMapIndex reports whether n (an IndexExpr node) indexes a map-typed
// target - shared by genExpr's own IndexExpr case (a read), genAssignStmt's
// own AssignStmt case (a write), and genMultiAssignStmt's per-target loop -
// each of which routes a map index through this file's own dedicated
// lowering instead of genAddr/genLoad's generic array-indexing path.
func (g *Generator) isMapIndex(n ast.NodeIndex) bool {
	targetNode := g.tree.Child(n, 0)
	return g.info.Types[targetNode].Kind == sema.TypeMap
}

// genMapIndexRead lowers a map index expression `m[k]` used as a value - n is
// the IndexExpr node itself - to both its value (V's own zero value for a
// nil map or a genuinely missing key, matching Go's own "reading a missing
// key returns the zero value" rule) and whether the key was actually present
// (the second half of Go's own "two-result index expression" - see
// LANGUAGE.md's "Maps" section and sema's checkDestructureSource). An
// ordinary single-value read (genExpr's IndexExpr case) just discards the
// second result; `v, ok := m[k]` (genMultiShortVarDecl/genMultiAssignStmt)
// uses both.
func (g *Generator) genMapIndexRead(n ast.NodeIndex) (value, found llvm.Value) {
	targetNode := g.tree.Child(n, 0)
	indexNode := g.tree.Child(n, 1)
	mapType := g.info.Types[targetNode]
	keyType := *mapType.Key
	valType := *mapType.Elem
	valLLType := g.llvmType(valType)
	zeroVal := llvm.ConstNull(valLLType)

	mapVal := g.genExpr(targetNode)
	keyVal := g.genExpr(indexNode)

	isNil := g.builder.CreateICmp(llvm.IntEQ, mapVal, llvm.ConstNull(g.ptrTy), "")

	nilBB := g.ctx.AddBasicBlock(g.curFn, "map.get.nil")
	liveBB := g.ctx.AddBasicBlock(g.curFn, "map.get.live")
	foundBB := g.ctx.AddBasicBlock(g.curFn, "map.get.found")
	notFoundBB := g.ctx.AddBasicBlock(g.curFn, "map.get.notfound")
	contBB := g.ctx.AddBasicBlock(g.curFn, "map.get.cont")
	g.builder.CreateCondBr(isNil, nilBB, liveBB)

	g.builder.SetInsertPointAtEnd(nilBB)
	g.builder.CreateBr(contBB)

	g.builder.SetInsertPointAtEnd(liveBB)
	bucketTy := g.mapBucketType(keyType, valType)
	bucketsPtr := g.builder.CreateLoad(g.ptrTy, g.builder.CreateStructGEP(g.mapCtrlTy, mapVal, 0, ""), "")
	bucketCount := g.builder.CreateLoad(g.i32Ty, g.builder.CreateStructGEP(g.mapCtrlTy, mapVal, 2, ""), "")
	slotFound, slotIdx := g.genMapProbe(keyType, bucketTy, bucketsPtr, bucketCount, keyVal)
	g.builder.CreateCondBr(slotFound, foundBB, notFoundBB)

	g.builder.SetInsertPointAtEnd(foundBB)
	bucketAddr := g.builder.CreateInBoundsGEP(bucketTy, bucketsPtr, []llvm.Value{slotIdx}, "")
	foundVal := g.builder.CreateLoad(valLLType, g.builder.CreateStructGEP(bucketTy, bucketAddr, 2, ""), "")
	foundEndBB := g.builder.GetInsertBlock()
	g.builder.CreateBr(contBB)

	g.builder.SetInsertPointAtEnd(notFoundBB)
	g.builder.CreateBr(contBB)

	g.builder.SetInsertPointAtEnd(contBB)
	valPhi := g.builder.CreatePHI(valLLType, "")
	valPhi.AddIncoming([]llvm.Value{zeroVal, foundVal, zeroVal}, []llvm.BasicBlock{nilBB, foundEndBB, notFoundBB})
	foundPhi := g.builder.CreatePHI(g.boolTy, "")
	t, f := llvm.ConstInt(g.boolTy, 1, false), llvm.ConstInt(g.boolTy, 0, false)
	foundPhi.AddIncoming([]llvm.Value{f, t, f}, []llvm.BasicBlock{nilBB, foundEndBB, notFoundBB})
	return valPhi, foundPhi
}

// genMapWriteAddr lowers a map index expression used as an assignment target
// (`m[k] = v`) to the address to store the new value into - n is the
// IndexExpr node itself. Shared by genAssignStmt's own map branch and
// genMultiAssignStmt's per-target loop.
func (g *Generator) genMapWriteAddr(n ast.NodeIndex) llvm.Value {
	targetNode := g.tree.Child(n, 0)
	indexNode := g.tree.Child(n, 1)
	mapType := g.info.Types[targetNode]

	mapVal := g.genExpr(targetNode)
	keyVal := g.genExpr(indexNode)
	return g.genMapGetOrInsertAddr(mapType, mapVal, keyVal)
}

// genMapGetOrInsertAddr implements the actual `m[k] = v` insert-or-update
// logic: probe for keyVal first; an existing match just returns that bucket's
// own value-field address (a plain update, no growth, no count change). A
// miss grows the table first if needed (genMapGrowIfNeeded), then re-probes
// (guaranteed to miss again, since growing never removes/adds any live key)
// against whatever bucket array growth left in place, writes the new key and
// an mapTagOccupied tag into the slot it finds, bumps the live count, and
// returns that (fresh) bucket's own value-field address.
//
// mapVal must not be nil - see genMapTrapIfNil, called unconditionally first,
// mirroring Go's own real "assignment to entry in nil map" panic (this
// project's own hard llvm.trap+unreachable abort, per every other runtime
// safety check in this package - see CODEGEN.md's "Runtime trap
// diagnostics" section).
func (g *Generator) genMapGetOrInsertAddr(mapType sema.Type, mapVal, keyVal llvm.Value) llvm.Value {
	g.genMapTrapIfNil(mapVal)

	keyType := *mapType.Key
	valType := *mapType.Elem
	bucketTy := g.mapBucketType(keyType, valType)

	bucketsAddr := g.builder.CreateStructGEP(g.mapCtrlTy, mapVal, 0, "")
	countAddr := g.builder.CreateStructGEP(g.mapCtrlTy, mapVal, 1, "")
	bucketCountAddr := g.builder.CreateStructGEP(g.mapCtrlTy, mapVal, 2, "")
	tombstoneCountAddr := g.builder.CreateStructGEP(g.mapCtrlTy, mapVal, 3, "")

	bucketsPtr := g.builder.CreateLoad(g.ptrTy, bucketsAddr, "")
	bucketCount := g.builder.CreateLoad(g.i32Ty, bucketCountAddr, "")
	slotFound, slotIdx := g.genMapProbe(keyType, bucketTy, bucketsPtr, bucketCount, keyVal)

	foundBB := g.ctx.AddBasicBlock(g.curFn, "map.set.found")
	insertBB := g.ctx.AddBasicBlock(g.curFn, "map.set.insert")
	contBB := g.ctx.AddBasicBlock(g.curFn, "map.set.cont")
	g.builder.CreateCondBr(slotFound, foundBB, insertBB)

	g.builder.SetInsertPointAtEnd(foundBB)
	foundBucketAddr := g.builder.CreateInBoundsGEP(bucketTy, bucketsPtr, []llvm.Value{slotIdx}, "")
	foundValAddr := g.builder.CreateStructGEP(bucketTy, foundBucketAddr, 2, "")
	foundEndBB := g.builder.GetInsertBlock()
	g.builder.CreateBr(contBB)

	g.builder.SetInsertPointAtEnd(insertBB)
	g.genMapGrowIfNeeded(bucketTy, keyType, valType, bucketsAddr, countAddr, bucketCountAddr, tombstoneCountAddr)
	newBucketsPtr := g.builder.CreateLoad(g.ptrTy, bucketsAddr, "")
	newBucketCount := g.builder.CreateLoad(g.i32Ty, bucketCountAddr, "")
	_, insertIdx := g.genMapProbe(keyType, bucketTy, newBucketsPtr, newBucketCount, keyVal)
	insertBucketAddr := g.builder.CreateInBoundsGEP(bucketTy, newBucketsPtr, []llvm.Value{insertIdx}, "")
	g.builder.CreateStore(llvm.ConstInt(g.i8Ty, mapTagOccupied, false), g.builder.CreateStructGEP(bucketTy, insertBucketAddr, 0, ""))
	g.builder.CreateStore(keyVal, g.builder.CreateStructGEP(bucketTy, insertBucketAddr, 1, ""))
	curCount := g.builder.CreateLoad(g.i32Ty, countAddr, "")
	g.builder.CreateStore(g.builder.CreateAdd(curCount, llvm.ConstInt(g.i32Ty, 1, false), ""), countAddr)
	insertValAddr := g.builder.CreateStructGEP(bucketTy, insertBucketAddr, 2, "")
	insertEndBB := g.builder.GetInsertBlock()
	g.builder.CreateBr(contBB)

	g.builder.SetInsertPointAtEnd(contBB)
	valAddrPhi := g.builder.CreatePHI(g.ptrTy, "")
	valAddrPhi.AddIncoming([]llvm.Value{foundValAddr, insertValAddr}, []llvm.BasicBlock{foundEndBB, insertEndBB})
	return valAddrPhi
}

// genMapTrapIfNil prints an informative diagnostic and traps (the same
// printf-then-llvm.trap+unreachable mechanism every other runtime safety
// check in this package uses - genBoundsCheck/genSliceRangeCheck (expr.go),
// genMakeSizeCheck (runtime.go) - see CODEGEN.md's "Runtime trap
// diagnostics" section) if mapVal is a nil (never-made) map - mirroring Go's
// own real "assignment to entry in nil map" panic. A nil map is perfectly
// legal to *read* (genMapIndexRead/genMapLenValue both handle it gracefully,
// returning a zero value/false/0 - matching Go's own "reading a nil map is
// fine" rule) - only a write ever reaches this check.
func (g *Generator) genMapTrapIfNil(mapVal llvm.Value) {
	isNil := g.builder.CreateICmp(llvm.IntEQ, mapVal, llvm.ConstNull(g.ptrTy), "")

	trapBB := g.ctx.AddBasicBlock(g.curFn, "map.nil.trap")
	okBB := g.ctx.AddBasicBlock(g.curFn, "map.nil.ok")
	g.builder.CreateCondBr(isNil, trapBB, okBB)

	g.builder.SetInsertPointAtEnd(trapBB)
	g.callPrintf([]llvm.Value{g.fmtMapNilTrap})
	g.builder.CreateCall(g.fflushType, g.fflushFn, []llvm.Value{llvm.ConstNull(g.ptrTy)}, "")
	g.builder.CreateCall(g.trapType, g.trapFn, nil, "")
	g.builder.CreateUnreachable()

	g.builder.SetInsertPointAtEnd(okBB)
}

// genMapRemoveCall lowers the predeclared `remove(m, k)` builtin (see
// LANGUAGE.md's "Maps" section): a no-op against a nil map or a genuinely
// missing key (matching Go's own real `delete(m, k)` behavior - removing an
// absent key is never an error), otherwise marks the matching bucket a
// tombstone (mapTagTombstone - not mapTagEmpty, so a probe sequence that
// passed through this slot for some *other* still-live key continues past it
// correctly - see this file's own top-of-file doc comment), decrements the
// live count, and increments tombstoneCount (so genMapGrowIfNeeded's own
// load-factor check sees this now-unusable-until-a-grow slot too, not just
// live entries).
func (g *Generator) genMapRemoveCall(args []ast.NodeIndex) {
	mapNode := args[0]
	keyNode := args[1]
	mapType := g.info.Types[mapNode]
	keyType := *mapType.Key
	bucketTy := g.mapBucketType(keyType, *mapType.Elem)

	mapVal := g.genExpr(mapNode)
	keyVal := g.genExpr(keyNode)

	isNil := g.builder.CreateICmp(llvm.IntEQ, mapVal, llvm.ConstNull(g.ptrTy), "")

	liveBB := g.ctx.AddBasicBlock(g.curFn, "map.remove.live")
	contBB := g.ctx.AddBasicBlock(g.curFn, "map.remove.cont")
	g.builder.CreateCondBr(isNil, contBB, liveBB)

	g.builder.SetInsertPointAtEnd(liveBB)
	countAddr := g.builder.CreateStructGEP(g.mapCtrlTy, mapVal, 1, "")
	tombstoneCountAddr := g.builder.CreateStructGEP(g.mapCtrlTy, mapVal, 3, "")
	bucketsPtr := g.builder.CreateLoad(g.ptrTy, g.builder.CreateStructGEP(g.mapCtrlTy, mapVal, 0, ""), "")
	bucketCount := g.builder.CreateLoad(g.i32Ty, g.builder.CreateStructGEP(g.mapCtrlTy, mapVal, 2, ""), "")
	slotFound, slotIdx := g.genMapProbe(keyType, bucketTy, bucketsPtr, bucketCount, keyVal)

	removeBB := g.ctx.AddBasicBlock(g.curFn, "map.remove.found")
	g.builder.CreateCondBr(slotFound, removeBB, contBB)

	g.builder.SetInsertPointAtEnd(removeBB)
	bucketAddr := g.builder.CreateInBoundsGEP(bucketTy, bucketsPtr, []llvm.Value{slotIdx}, "")
	g.builder.CreateStore(llvm.ConstInt(g.i8Ty, mapTagTombstone, false), g.builder.CreateStructGEP(bucketTy, bucketAddr, 0, ""))
	count := g.builder.CreateLoad(g.i32Ty, countAddr, "")
	g.builder.CreateStore(g.builder.CreateSub(count, llvm.ConstInt(g.i32Ty, 1, false), ""), countAddr)
	tombstoneCount := g.builder.CreateLoad(g.i32Ty, tombstoneCountAddr, "")
	g.builder.CreateStore(g.builder.CreateAdd(tombstoneCount, llvm.ConstInt(g.i32Ty, 1, false), ""), tombstoneCountAddr)
	g.builder.CreateBr(contBB)

	g.builder.SetInsertPointAtEnd(contBB)
}

// genMapGrowIfNeeded doubles bucketCount (and rehashes every still-occupied
// old bucket into the fresh, all-empty array) whenever inserting one more
// entry would push the *occupied* fraction of the bucket array - live
// entries plus tombstones, since both occupy a real slot and lengthen probe
// sequences identically (see this file's own top-of-file doc comment) -
// above 3/4 - see CODEGEN.md's "Maps" section for exactly why this
// threshold. The old bucket array is simply abandoned once rehashing
// finishes, never freed - consistent with this project's already-documented
// "the arena never frees" design (see CODEGEN.md's "The arena allocator"
// section) and exactly the same growth pattern genAppendCall's own dynamic-
// array doubling already uses. A no-op (contBB reached directly) when the
// load factor doesn't yet require it.
func (g *Generator) genMapGrowIfNeeded(bucketTy llvm.Type, keyType, valType sema.Type, bucketsAddr, countAddr, bucketCountAddr, tombstoneCountAddr llvm.Value) {
	count := g.builder.CreateLoad(g.i32Ty, countAddr, "")
	bucketCount := g.builder.CreateLoad(g.i32Ty, bucketCountAddr, "")
	tombstoneCount := g.builder.CreateLoad(g.i32Ty, tombstoneCountAddr, "")

	// Grow when (count+tombstoneCount+1)*4 > bucketCount*3 - i.e. inserting
	// one more entry would push the occupied fraction above 0.75, counting
	// both live entries and tombstones as occupied slots (see this
	// function's own doc comment for why tombstones must count here too, not
	// just count). Computed in i64 to keep this simple and safe against i32
	// overflow for a very large map, mirroring this package's own existing
	// "compute size arithmetic in i64" habit (genArenaAllocElems).
	count64 := g.builder.CreateZExt(count, g.i64Ty, "")
	tombstoneCount64 := g.builder.CreateZExt(tombstoneCount, g.i64Ty, "")
	bucketCount64 := g.builder.CreateZExt(bucketCount, g.i64Ty, "")
	occupied64 := g.builder.CreateAdd(count64, tombstoneCount64, "")
	nextOccupied64 := g.builder.CreateAdd(occupied64, llvm.ConstInt(g.i64Ty, 1, false), "")
	lhs := g.builder.CreateMul(nextOccupied64, llvm.ConstInt(g.i64Ty, 4, false), "")
	rhs := g.builder.CreateMul(bucketCount64, llvm.ConstInt(g.i64Ty, 3, false), "")
	needGrow := g.builder.CreateICmp(llvm.IntUGT, lhs, rhs, "")

	growBB := g.ctx.AddBasicBlock(g.curFn, "map.grow")
	contBB := g.ctx.AddBasicBlock(g.curFn, "map.grow.cont")
	g.builder.CreateCondBr(needGrow, growBB, contBB)

	g.builder.SetInsertPointAtEnd(growBB)
	oldBucketsPtr := g.builder.CreateLoad(g.ptrTy, bucketsAddr, "")
	newBucketCount := g.builder.CreateMul(bucketCount, llvm.ConstInt(g.i32Ty, 2, false), "")
	newBucketsPtr, _, newTotalBytes := g.genArenaAllocElems(bucketTy, newBucketCount)
	g.builder.CreateCall(g.memsetType, g.memsetFn, []llvm.Value{newBucketsPtr, llvm.ConstInt(g.i32Ty, 0, false), newTotalBytes}, "")

	// Rehash every still-occupied old bucket into the new, larger, all-empty
	// array - a bounded runtime loop over the old bucket count. Reuses
	// genMapProbe verbatim for finding each rehashed key's new slot: a
	// freshly zeroed array with no duplicate keys inserted yet always reports
	// slotFound == false immediately (the very first probed slot is already
	// mapTagEmpty), so this needs no separate "probe for an empty slot only"
	// variant at all.
	idxAddr := g.createEntryAlloca(g.i32Ty, "map.grow.idx")
	g.builder.CreateStore(llvm.ConstInt(g.i32Ty, 0, false), idxAddr)

	condBB := g.ctx.AddBasicBlock(g.curFn, "map.grow.rehash.cond")
	bodyBB := g.ctx.AddBasicBlock(g.curFn, "map.grow.rehash.body")
	moveBB := g.ctx.AddBasicBlock(g.curFn, "map.grow.rehash.move")
	nextBB := g.ctx.AddBasicBlock(g.curFn, "map.grow.rehash.next")
	doneBB := g.ctx.AddBasicBlock(g.curFn, "map.grow.rehash.done")
	g.builder.CreateBr(condBB)

	g.builder.SetInsertPointAtEnd(condBB)
	idx := g.builder.CreateLoad(g.i32Ty, idxAddr, "")
	g.builder.CreateCondBr(g.builder.CreateICmp(llvm.IntSLT, idx, bucketCount, ""), bodyBB, doneBB)

	g.builder.SetInsertPointAtEnd(bodyBB)
	oldBucketAddr := g.builder.CreateInBoundsGEP(bucketTy, oldBucketsPtr, []llvm.Value{idx}, "")
	oldTag := g.builder.CreateLoad(g.i8Ty, g.builder.CreateStructGEP(bucketTy, oldBucketAddr, 0, ""), "")
	isOccupied := g.builder.CreateICmp(llvm.IntEQ, oldTag, llvm.ConstInt(g.i8Ty, mapTagOccupied, false), "")
	g.builder.CreateCondBr(isOccupied, moveBB, nextBB)

	g.builder.SetInsertPointAtEnd(moveBB)
	oldKey := g.builder.CreateLoad(g.llvmType(keyType), g.builder.CreateStructGEP(bucketTy, oldBucketAddr, 1, ""), "")
	oldVal := g.builder.CreateLoad(g.llvmType(valType), g.builder.CreateStructGEP(bucketTy, oldBucketAddr, 2, ""), "")
	_, newIdx := g.genMapProbe(keyType, bucketTy, newBucketsPtr, newBucketCount, oldKey)
	newBucketAddr := g.builder.CreateInBoundsGEP(bucketTy, newBucketsPtr, []llvm.Value{newIdx}, "")
	g.builder.CreateStore(llvm.ConstInt(g.i8Ty, mapTagOccupied, false), g.builder.CreateStructGEP(bucketTy, newBucketAddr, 0, ""))
	g.builder.CreateStore(oldKey, g.builder.CreateStructGEP(bucketTy, newBucketAddr, 1, ""))
	g.builder.CreateStore(oldVal, g.builder.CreateStructGEP(bucketTy, newBucketAddr, 2, ""))
	g.builder.CreateBr(nextBB)

	g.builder.SetInsertPointAtEnd(nextBB)
	g.builder.CreateStore(g.builder.CreateAdd(idx, llvm.ConstInt(g.i32Ty, 1, false), ""), idxAddr)
	g.builder.CreateBr(condBB)

	g.builder.SetInsertPointAtEnd(doneBB)
	g.builder.CreateStore(newBucketsPtr, bucketsAddr)
	g.builder.CreateStore(newBucketCount, bucketCountAddr)
	// The rehash above only ever moves mapTagOccupied entries - every
	// tombstone is left behind in the abandoned old array, so the fresh
	// table starts with zero tombstones of its own.
	g.builder.CreateStore(llvm.ConstInt(g.i32Ty, 0, false), tombstoneCountAddr)
	g.builder.CreateBr(contBB)

	g.builder.SetInsertPointAtEnd(contBB)
}

// genMapProbe builds a bounded linear-probe loop (open addressing, per this
// file's own top-of-file doc comment) over bucketsPtr's bucketCount buckets,
// starting at hash(keyVal) mod bucketCount and advancing by one (wrapping
// around) each step, for at most bucketCount steps - a bound the growth
// policy (genMapGrowIfNeeded, load factor kept <= 0.75) always guarantees is
// never actually reached, since a real mapTagEmpty slot is always found well
// before then. Returns:
//   - found == true, slotIdx == the matching bucket's own index, when an
//     mapTagOccupied bucket holding a key equal to keyVal (genMapKeyEqual) is
//     encountered - the probe stops there immediately, before ever reaching a
//     later mapTagEmpty slot.
//   - found == false, slotIdx == the first mapTagEmpty-or-mapTagTombstone
//     bucket index encountered along the way - a valid insertion point for
//     keyVal (whether inserting fresh or rehashing during growth). The probe
//     itself still stops at the first genuine mapTagEmpty slot (a
//     tombstone alone never ends the probe - a live key further along the
//     chain must still be reachable), remembering only the *first* available
//     slot it passed, exactly the same "prefer the earliest reusable slot,
//     including a tombstone, once we know the key truly isn't present
//     further along" strategy real open-addressing implementations use.
func (g *Generator) genMapProbe(keyType sema.Type, bucketTy llvm.Type, bucketsPtr, bucketCount, keyVal llvm.Value) (found, slotIdx llvm.Value) {
	hash := g.genMapHash(keyType, keyVal)
	startIdx := g.builder.CreateURem(hash, bucketCount, "")

	idxAddr := g.createEntryAlloca(g.i32Ty, "map.probe.idx")
	g.builder.CreateStore(startIdx, idxAddr)
	stepAddr := g.createEntryAlloca(g.i32Ty, "map.probe.step")
	g.builder.CreateStore(llvm.ConstInt(g.i32Ty, 0, false), stepAddr)
	foundAddr := g.createEntryAlloca(g.boolTy, "map.probe.found")
	g.builder.CreateStore(llvm.ConstInt(g.boolTy, 0, false), foundAddr)
	resultIdxAddr := g.createEntryAlloca(g.i32Ty, "map.probe.residx")
	g.builder.CreateStore(llvm.ConstInt(g.i32Ty, 0, false), resultIdxAddr)
	candFoundAddr := g.createEntryAlloca(g.boolTy, "map.probe.candfound")
	g.builder.CreateStore(llvm.ConstInt(g.boolTy, 0, false), candFoundAddr)
	candIdxAddr := g.createEntryAlloca(g.i32Ty, "map.probe.candidx")
	g.builder.CreateStore(llvm.ConstInt(g.i32Ty, 0, false), candIdxAddr)

	condBB := g.ctx.AddBasicBlock(g.curFn, "map.probe.cond")
	bodyBB := g.ctx.AddBasicBlock(g.curFn, "map.probe.body")
	emptyBB := g.ctx.AddBasicBlock(g.curFn, "map.probe.empty")
	checkOccBB := g.ctx.AddBasicBlock(g.curFn, "map.probe.checkocc")
	occBB := g.ctx.AddBasicBlock(g.curFn, "map.probe.occ")
	matchBB := g.ctx.AddBasicBlock(g.curFn, "map.probe.match")
	tombBB := g.ctx.AddBasicBlock(g.curFn, "map.probe.tomb")
	advanceBB := g.ctx.AddBasicBlock(g.curFn, "map.probe.advance")
	endBB := g.ctx.AddBasicBlock(g.curFn, "map.probe.end")

	g.builder.CreateBr(condBB)

	g.builder.SetInsertPointAtEnd(condBB)
	step := g.builder.CreateLoad(g.i32Ty, stepAddr, "")
	g.builder.CreateCondBr(g.builder.CreateICmp(llvm.IntSLT, step, bucketCount, ""), bodyBB, endBB)

	g.builder.SetInsertPointAtEnd(bodyBB)
	idx := g.builder.CreateLoad(g.i32Ty, idxAddr, "")
	bucketAddr := g.builder.CreateInBoundsGEP(bucketTy, bucketsPtr, []llvm.Value{idx}, "")
	tag := g.builder.CreateLoad(g.i8Ty, g.builder.CreateStructGEP(bucketTy, bucketAddr, 0, ""), "")
	isEmpty := g.builder.CreateICmp(llvm.IntEQ, tag, llvm.ConstInt(g.i8Ty, mapTagEmpty, false), "")
	g.builder.CreateCondBr(isEmpty, emptyBB, checkOccBB)

	g.builder.SetInsertPointAtEnd(emptyBB)
	g.recordMapProbeCandidate(candFoundAddr, candIdxAddr, idx)
	g.builder.CreateBr(endBB)

	g.builder.SetInsertPointAtEnd(checkOccBB)
	isOcc := g.builder.CreateICmp(llvm.IntEQ, tag, llvm.ConstInt(g.i8Ty, mapTagOccupied, false), "")
	g.builder.CreateCondBr(isOcc, occBB, tombBB)

	g.builder.SetInsertPointAtEnd(occBB)
	storedKey := g.builder.CreateLoad(g.llvmType(keyType), g.builder.CreateStructGEP(bucketTy, bucketAddr, 1, ""), "")
	eq := g.genMapKeyEqual(keyType, storedKey, keyVal)
	g.builder.CreateCondBr(eq, matchBB, advanceBB)

	g.builder.SetInsertPointAtEnd(matchBB)
	g.builder.CreateStore(llvm.ConstInt(g.boolTy, 1, false), foundAddr)
	g.builder.CreateStore(idx, resultIdxAddr)
	g.builder.CreateBr(endBB)

	g.builder.SetInsertPointAtEnd(tombBB)
	g.recordMapProbeCandidate(candFoundAddr, candIdxAddr, idx)
	g.builder.CreateBr(advanceBB)

	g.builder.SetInsertPointAtEnd(advanceBB)
	nextIdx := g.builder.CreateURem(g.builder.CreateAdd(idx, llvm.ConstInt(g.i32Ty, 1, false), ""), bucketCount, "")
	g.builder.CreateStore(nextIdx, idxAddr)
	g.builder.CreateStore(g.builder.CreateAdd(step, llvm.ConstInt(g.i32Ty, 1, false), ""), stepAddr)
	g.builder.CreateBr(condBB)

	g.builder.SetInsertPointAtEnd(endBB)
	foundVal := g.builder.CreateLoad(g.boolTy, foundAddr, "")
	resultIdxVal := g.builder.CreateLoad(g.i32Ty, resultIdxAddr, "")
	candIdxVal := g.builder.CreateLoad(g.i32Ty, candIdxAddr, "")
	finalIdx := g.builder.CreateSelect(foundVal, resultIdxVal, candIdxVal, "")
	return foundVal, finalIdx
}

// recordMapProbeCandidate remembers idx as genMapProbe's own eventual
// "insertion point" result the first time an available (empty or tombstone)
// slot is seen along a probe - a later available slot never overwrites an
// earlier one already recorded, so genMapProbe always reports the earliest
// available slot in probe order.
func (g *Generator) recordMapProbeCandidate(candFoundAddr, candIdxAddr, idx llvm.Value) {
	hadCand := g.builder.CreateLoad(g.boolTy, candFoundAddr, "")
	prevIdx := g.builder.CreateLoad(g.i32Ty, candIdxAddr, "")
	newIdx := g.builder.CreateSelect(hadCand, prevIdx, idx, "")
	g.builder.CreateStore(newIdx, candIdxAddr)
	g.builder.CreateStore(llvm.ConstInt(g.boolTy, 1, false), candFoundAddr)
}

// genMapKeyEqual compares two already-evaluated values of the same map key
// type t for equality - the map-key-specific counterpart to genValueEqual
// (expr.go, used for a whole-value `==`/`!=`): built as its own, separate,
// self-contained recursive function (not a reuse of genValueEqual) because a
// map key must support every type sema's own typeIsComparable accepts -
// every integer width, both float widths, and a pointer - while
// genValueEqual's own switch only actually implements TypeInt (i32)/
// TypeBool/TypeString/TypeStruct/TypeArray, panicking on anything else
// (i8/i16/i64/f32/f64/a pointer field) - a real, pre-existing gap in that
// function orthogonal to this feature (flagged separately, not fixed here -
// fixing genValueEqual's own general `==`/`!=` lowering is a wider change
// than this round's map-key-comparison needs). Every kind typeIsComparable
// accepts is implemented directly here instead.
func (g *Generator) genMapKeyEqual(t sema.Type, lv, rv llvm.Value) llvm.Value {
	switch t.Kind {
	case sema.TypeI8, sema.TypeI16, sema.TypeI32, sema.TypeI64, sema.TypeBool:
		return g.builder.CreateICmp(llvm.IntEQ, lv, rv, "")
	case sema.TypeF32, sema.TypeF64:
		return g.builder.CreateFCmp(llvm.FloatOEQ, lv, rv, "")
	case sema.TypeString:
		return g.genStringEqual(lv, rv, true)
	case sema.TypePointer:
		return g.builder.CreateICmp(llvm.IntEQ, lv, rv, "")
	case sema.TypeStruct:
		layout := g.structLayouts[t.Struct]
		result := llvm.ConstInt(g.boolTy, 1, false)
		for i, ft := range layout.fieldSemaTypes {
			lf := g.builder.CreateExtractValue(lv, i, "")
			rf := g.builder.CreateExtractValue(rv, i, "")
			result = g.builder.CreateAnd(result, g.genMapKeyEqual(ft, lf, rf), "")
		}
		return result
	case sema.TypeArray:
		result := llvm.ConstInt(g.boolTy, 1, false)
		for i := 0; i < int(t.Size); i++ {
			le := g.builder.CreateExtractValue(lv, i, "")
			re := g.builder.CreateExtractValue(rv, i, "")
			result = g.builder.CreateAnd(result, g.genMapKeyEqual(*t.Elem, le, re), "")
		}
		return result
	default:
		// sema's typeIsComparable (typecheck.go) rejects every other
		// Kind (a dynamic array, a function type, another map) as a map key
		// type outright - unreachable on a tree that already passed
		// sema.Check (see the package doc comment).
		panic("codegen: genMapKeyEqual reached an unsupported map key type " + t.String())
	}
}

// genMapHash computes a map key's own hash - the entry point for
// genHashInto's recursive combinator below.
//
// Deliberately a word-wise FNV-1a-*style* mixing combinator, recursing
// through a key's own logical structure (each numeric field/element's own
// bit pattern, a string's real bytes, ...) rather than a literal byte-for-
// byte FNV-1a pass over the key's raw in-memory representation: this
// project's own struct/array *values* (LLVM aggregates built via
// InsertValue - see genCompositeLitInto) never guarantee their own inter-
// field padding bytes are deterministically zeroed, so hashing "however many
// raw bytes the LLVM type occupies" could hash the identical logical struct
// value to two different results depending on whatever garbage happened to
// sit in its padding - silently breaking the one property most essential to
// any hash table (equal keys MUST hash equal). Recursing field-by-field and
// mixing only each field's own real bit pattern sidesteps that entirely,
// while still being the same "genuinely simple, well-known mixing function"
// FNV-1a is - see CODEGEN.md's "Maps" section for this exact tradeoff,
// documented in full.
func (g *Generator) genMapHash(t sema.Type, v llvm.Value) llvm.Value {
	seed := llvm.ConstInt(g.i32Ty, fnvOffsetBasis32, false)
	return g.genHashInto(t, v, seed)
}

// fnvMix folds one 32-bit word into seed, FNV-1a style: xor then multiply by
// the FNV prime.
func (g *Generator) fnvMix(seed, word llvm.Value) llvm.Value {
	x := g.builder.CreateXor(seed, word, "")
	return g.builder.CreateMul(x, llvm.ConstInt(g.i32Ty, fnvPrime32, false), "")
}

// genHashInto recursively mixes v (of type t) into seed and returns the
// updated running hash - see genMapHash's own doc comment for why this
// recurses over v's logical structure rather than its raw bytes. Every kind
// sema's typeIsComparable accepts is handled directly.
func (g *Generator) genHashInto(t sema.Type, v, seed llvm.Value) llvm.Value {
	switch t.Kind {
	case sema.TypeI8, sema.TypeI16:
		return g.fnvMix(seed, g.builder.CreateSExt(v, g.i32Ty, ""))
	case sema.TypeI32:
		return g.fnvMix(seed, v)
	case sema.TypeBool:
		return g.fnvMix(seed, g.builder.CreateZExt(v, g.i32Ty, ""))
	case sema.TypeI64:
		lo := g.builder.CreateTrunc(v, g.i32Ty, "")
		hi := g.builder.CreateTrunc(g.builder.CreateLShr(v, llvm.ConstInt(g.i64Ty, 32, false), ""), g.i32Ty, "")
		return g.fnvMix(g.fnvMix(seed, lo), hi)
	case sema.TypeF32:
		return g.fnvMix(seed, g.builder.CreateBitCast(v, g.i32Ty, ""))
	case sema.TypeF64:
		bits := g.builder.CreateBitCast(v, g.i64Ty, "")
		return g.genHashInto(sema.Type{Kind: sema.TypeI64}, bits, seed)
	case sema.TypePointer:
		asInt := g.builder.CreatePtrToInt(v, g.i64Ty, "")
		return g.genHashInto(sema.Type{Kind: sema.TypeI64}, asInt, seed)
	case sema.TypeString:
		ptr := g.builder.CreateExtractValue(v, 0, "")
		length := g.builder.CreateExtractValue(v, 1, "")
		return g.genHashStringInto(ptr, length, seed)
	case sema.TypeStruct:
		layout := g.structLayouts[t.Struct]
		for i, ft := range layout.fieldSemaTypes {
			fv := g.builder.CreateExtractValue(v, i, "")
			seed = g.genHashInto(ft, fv, seed)
		}
		return seed
	case sema.TypeArray:
		for i := 0; i < int(t.Size); i++ {
			ev := g.builder.CreateExtractValue(v, i, "")
			seed = g.genHashInto(*t.Elem, ev, seed)
		}
		return seed
	default:
		// Unreachable on a tree that already passed sema.Check - see
		// genMapKeyEqual's own identical closing remark.
		panic("codegen: genHashInto reached an unsupported map key type " + t.String())
	}
}

// genHashStringInto mixes a string's own real content bytes (ptr/length -
// see the "string representation" section, CODEGEN.md) into seed, one byte
// at a time, via a real bounded runtime loop (the same CreateCondBr/
// AddBasicBlock shape genPrintDynArrayValue's own element loop already uses,
// runtime.go) - a string's length isn't known until the program runs, so
// this can't be unrolled the way a fixed-size aggregate's own field/element
// loop (genHashInto's TypeStruct/TypeArray cases) can.
func (g *Generator) genHashStringInto(ptr, length, seed llvm.Value) llvm.Value {
	seedAddr := g.createEntryAlloca(g.i32Ty, "map.hash.str.seed")
	g.builder.CreateStore(seed, seedAddr)
	idxAddr := g.createEntryAlloca(g.i32Ty, "map.hash.str.idx")
	g.builder.CreateStore(llvm.ConstInt(g.i32Ty, 0, false), idxAddr)

	condBB := g.ctx.AddBasicBlock(g.curFn, "map.hash.str.cond")
	bodyBB := g.ctx.AddBasicBlock(g.curFn, "map.hash.str.body")
	endBB := g.ctx.AddBasicBlock(g.curFn, "map.hash.str.end")
	g.builder.CreateBr(condBB)

	g.builder.SetInsertPointAtEnd(condBB)
	idx := g.builder.CreateLoad(g.i32Ty, idxAddr, "")
	g.builder.CreateCondBr(g.builder.CreateICmp(llvm.IntSLT, idx, length, ""), bodyBB, endBB)

	g.builder.SetInsertPointAtEnd(bodyBB)
	b := g.builder.CreateLoad(g.i8Ty, g.builder.CreateInBoundsGEP(g.i8Ty, ptr, []llvm.Value{idx}, ""), "")
	curSeed := g.builder.CreateLoad(g.i32Ty, seedAddr, "")
	g.builder.CreateStore(g.fnvMix(curSeed, g.builder.CreateZExt(b, g.i32Ty, "")), seedAddr)
	g.builder.CreateStore(g.builder.CreateAdd(idx, llvm.ConstInt(g.i32Ty, 1, false), ""), idxAddr)
	g.builder.CreateBr(condBB)

	g.builder.SetInsertPointAtEnd(endBB)
	return g.builder.CreateLoad(g.i32Ty, seedAddr, "")
}
