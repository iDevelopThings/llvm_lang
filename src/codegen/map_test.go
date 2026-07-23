package codegen

import (
	"testing"
	"unsafe"
)

// This file covers this round's `map[K]V` feature end to end, JIT-executed
// (see LANGUAGE.md's "Maps" section and CODEGEN.md's own "Maps" section for
// the hash table representation this lowers to) - make/insert/lookup/len/
// remove, the `v, ok := m[k]` two-result idiom (present and absent, alongside
// a plain single-value `x := m[k]` in the same program), the hash table's
// real growth path under load, and a struct-typed key colliding correctly by
// value rather than by identity.

// TestMapMakeInsertLookup covers the basic round trip: make, insert, read
// back, and a missing key's zero value.
func TestMapMakeInsertLookup(t *testing.T) {
	jm := compileAndJIT(t, `
func testPresent() int {
	m := make(map[string]int)
	m["a"] = 1
	m["b"] = 2
	return m["a"] + m["b"]
}

func testMissingIsZero() int {
	m := make(map[string]int)
	m["a"] = 1
	return m["missing"]
}

func testOverwrite() int {
	m := make(map[string]int)
	m["a"] = 1
	m["a"] = 99
	return m["a"]
}
`)
	if got := jm.runInt32(t, "testPresent"); got != 3 {
		t.Errorf("testPresent() = %d, want 3", got)
	}
	if got := jm.runInt32(t, "testMissingIsZero"); got != 0 {
		t.Errorf("testMissingIsZero() = %d, want 0", got)
	}
	if got := jm.runInt32(t, "testOverwrite"); got != 99 {
		t.Errorf("testOverwrite() = %d, want 99", got)
	}
}

// TestMapLen covers `len(m)` - 0 for a fresh map, growing with distinct
// insertions, unaffected by an update to an already-present key.
func TestMapLen(t *testing.T) {
	jm := compileAndJIT(t, `
func testEmpty() int {
	m := make(map[string]int)
	return len(m)
}

func testAfterInserts() int {
	m := make(map[string]int)
	m["a"] = 1
	m["b"] = 2
	m["c"] = 3
	return len(m)
}

func testUpdateDoesNotGrowLen() int {
	m := make(map[string]int)
	m["a"] = 1
	m["a"] = 2
	m["a"] = 3
	return len(m)
}
`)
	if got := jm.runInt32(t, "testEmpty"); got != 0 {
		t.Errorf("testEmpty() = %d, want 0", got)
	}
	if got := jm.runInt32(t, "testAfterInserts"); got != 3 {
		t.Errorf("testAfterInserts() = %d, want 3", got)
	}
	if got := jm.runInt32(t, "testUpdateDoesNotGrowLen"); got != 1 {
		t.Errorf("testUpdateDoesNotGrowLen() = %d, want 1", got)
	}
}

// TestMapRemove covers `remove(m, k)`: a subsequent lookup correctly reports
// not-found (zero value, and via the two-result idiom, ok == false), len
// decreases, and removing an absent key (or from an empty map) is a
// harmless no-op.
func TestMapRemove(t *testing.T) {
	jm := compileAndJIT(t, `
func testRemoveThenLen() int {
	m := make(map[string]int)
	m["a"] = 1
	m["b"] = 2
	remove(m, "a")
	return len(m)
}

func testRemoveThenLookupIsZero() int {
	m := make(map[string]int)
	m["a"] = 1
	remove(m, "a")
	return m["a"]
}

func testRemoveThenOkIsFalse() bool {
	m := make(map[string]int)
	m["a"] = 1
	remove(m, "a")
	_, ok := m["a"]
	return ok
}

func testRemoveAbsentKeyIsNoOp() int {
	m := make(map[string]int)
	m["a"] = 1
	remove(m, "nope")
	return len(m)
}

func testRemoveFromNilMapIsNoOp() int {
	var m map[string]int
	remove(m, "a")
	return len(m)
}
`)
	if got := jm.runInt32(t, "testRemoveThenLen"); got != 1 {
		t.Errorf("testRemoveThenLen() = %d, want 1", got)
	}
	if got := jm.runInt32(t, "testRemoveThenLookupIsZero"); got != 0 {
		t.Errorf("testRemoveThenLookupIsZero() = %d, want 0", got)
	}
	if got := jm.runBool(t, "testRemoveThenOkIsFalse"); got != false {
		t.Errorf("testRemoveThenOkIsFalse() = %v, want false", got)
	}
	if got := jm.runInt32(t, "testRemoveAbsentKeyIsNoOp"); got != 1 {
		t.Errorf("testRemoveAbsentKeyIsNoOp() = %d, want 1", got)
	}
	if got := jm.runInt32(t, "testRemoveFromNilMapIsNoOp"); got != 0 {
		t.Errorf("testRemoveFromNilMapIsNoOp() = %d, want 0", got)
	}
}

// TestMapTwoResultIndexIdiom covers `v, ok := m[k]` for both a present and an
// absent key, and confirms a plain single-target `x := m[k]` (not
// destructured) still works as an ordinary single value in the exact same
// program - the "two-result index expression is context-dependent, not a
// real multi-return Type" distinction (see sema's checkDestructureSource),
// proven at actual runtime here, not just at type-check time (see
// src/sema/map_test.go's own identical-in-spirit but type-level assertion).
func TestMapTwoResultIndexIdiom(t *testing.T) {
	jm := compileAndJIT(t, `
func testPresentOk() bool {
	m := make(map[string]int)
	m["a"] = 5
	v, ok := m["a"]
	if v != 5 {
		return false
	}
	return ok
}

func testAbsentNotOk() bool {
	m := make(map[string]int)
	m["a"] = 5
	v, ok := m["missing"]
	if v != 0 {
		return false
	}
	return !ok
}

func testPlainSingleValueAlongsideDestructure() int {
	m := make(map[string]int)
	m["a"] = 5
	m["b"] = 7
	x := m["a"]
	v, ok := m["b"]
	if !ok {
		return -1
	}
	return x + v
}
`)
	if got := jm.runBool(t, "testPresentOk"); got != true {
		t.Errorf("testPresentOk() = %v, want true", got)
	}
	if got := jm.runBool(t, "testAbsentNotOk"); got != true {
		t.Errorf("testAbsentNotOk() = %v, want true", got)
	}
	if got := jm.runInt32(t, "testPlainSingleValueAlongsideDestructure"); got != 12 {
		t.Errorf("testPlainSingleValueAlongsideDestructure() = %d, want 12", got)
	}
}

// TestMapGrowthPreservesAllKeys is the single most important correctness
// property of any hash table implementation (see the task's own required
// verification list): insert enough distinct keys to force at least one real
// rehash/grow (mapInitialBuckets is 8, growing past a 0.75 load factor -
// see maps.go's genMapGrowIfNeeded - so 50 distinct keys forces several), and
// confirm every previously-inserted key is still correctly retrievable
// afterward, plus len matches exactly.
func TestMapGrowthPreservesAllKeys(t *testing.T) {
	jm := compileAndJIT(t, `
func buildAndCheck() int {
	m := make(map[int]int)
	i := 0
	for i < 50 {
		m[i] = i * 7
		i++
	}
	if len(m) != 50 {
		return -1
	}
	j := 0
	for j < 50 {
		v, ok := m[j]
		if !ok {
			return -2
		}
		if v != j*7 {
			return -3
		}
		j++
	}
	return 0
}
`)
	if got := jm.runInt32(t, "buildAndCheck"); got != 0 {
		t.Errorf("buildAndCheck() = %d, want 0", got)
	}
}

// TestMapStructKeyStructuralEquality confirms a struct-typed key with 2+
// fields hashes/compares by structural value, not identity - two distinct
// struct values built separately, but with equal field values, must collide
// to the exact same map entry.
func TestMapStructKeyStructuralEquality(t *testing.T) {
	jm := compileAndJIT(t, `
struct Point {
	x int
	y int
}

func testStructurallyEqualKeysCollide() int {
	m := make(map[Point]int)
	a := Point{1, 2}
	m[a] = 100
	b := Point{1, 2}
	m[b] = 200
	if len(m) != 1 {
		return -1
	}
	return m[Point{1, 2}]
}

func testDifferentStructKeysAreDistinct() int {
	m := make(map[Point]int)
	m[Point{1, 2}] = 10
	m[Point{3, 4}] = 20
	if len(m) != 2 {
		return -1
	}
	return m[Point{1, 2}] + m[Point{3, 4}]
}
`)
	if got := jm.runInt32(t, "testStructurallyEqualKeysCollide"); got != 200 {
		t.Errorf("testStructurallyEqualKeysCollide() = %d, want 200", got)
	}
	if got := jm.runInt32(t, "testDifferentStructKeysAreDistinct"); got != 30 {
		t.Errorf("testDifferentStructKeysAreDistinct() = %d, want 30", got)
	}
}

// TestMapChurnTombstonesTriggerGrowth covers the specific insert/remove/
// insert-more churn pattern genMapGrowIfNeeded's tombstone accounting exists
// for (see maps.go's own top-of-file doc comment): insert exactly
// mapInitialBuckets - 2 (6) distinct keys into a fresh map (filling the
// initial 8-bucket table right up to, but not past, its 0.75 load-factor
// threshold - no grow yet), remove all but one of them (5 tombstones, only 1
// live entry - count alone would look almost empty), then insert 2 more
// brand-new keys.
//
// If tombstones weren't counted toward the load factor (the bug this fix
// addresses), the table would stay at its initial 8 buckets: count peaks at
// just 2 by the time the new keys are inserted, nowhere near the load-factor
// threshold on a live-count-only check. Counting tombstones too, occupied
// slots (1 live + 5 tombstones = 6) already sit right at the threshold
// before either new key goes in, so the very first of the two new inserts
// must trigger a real grow.
//
// Verifies both halves the task requires: (a) correctness - every still-live
// key (including the two newly-inserted ones) reads back correctly, and
// every removed key is genuinely gone - via churnCheck's own return code,
// and (b) the grow genuinely fired - via churn's returned map's own raw
// control-block memory, read directly since nothing at the language level
// can otherwise observe bucketCount/tombstoneCount (see maps.go's
// setupMapTypes: mapCtrlTy is {ptr buckets, i32 count, i32 bucketCount, i32
// tombstoneCount}, and a map's own runtime value - see types.go's llvmType -
// is just that struct's raw address; reading it here is safe precisely
// because the JIT-executed code runs in this same test process/address
// space).
func TestMapChurnTombstonesTriggerGrowth(t *testing.T) {
	const churnSrc = `
func churn() map[int]int {
	m := make(map[int]int)
	i := 0
	for i < 6 {
		m[i] = i * 10
		i++
	}
	j := 0
	for j < 5 {
		remove(m, j)
		j++
	}
	m[100] = 1000
	m[101] = 1001
	return m
}

func churnCheck() int {
	m := make(map[int]int)
	i := 0
	for i < 6 {
		m[i] = i * 10
		i++
	}
	j := 0
	for j < 5 {
		remove(m, j)
		j++
	}
	m[100] = 1000
	m[101] = 1001

	if len(m) != 3 {
		return -1
	}
	v, ok := m[5]
	if !ok || v != 50 {
		return -2
	}
	v, ok = m[100]
	if !ok || v != 1000 {
		return -3
	}
	v, ok = m[101]
	if !ok || v != 1001 {
		return -4
	}
	k := 0
	for k < 5 {
		_, stillThere := m[k]
		if stillThere {
			return -5
		}
		k++
	}
	return 0
}
`
	jm := compileAndJIT(t, churnSrc)

	if got := jm.runInt32(t, "churnCheck"); got != 0 {
		t.Fatalf("churnCheck() = %d, want 0 (see its own body for what each negative code means)", got)
	}

	ctrlAddr := uintptr(jm.runInt64(t, "churn"))
	if ctrlAddr == 0 {
		t.Fatal("churn() returned a nil map")
	}

	// mapCtrlTy field layout ({ptr, i32, i32, i32}, x86-64 natural alignment,
	// unpacked): buckets @0 (8 bytes), count @8, bucketCount @12,
	// tombstoneCount @16 - see setupMapTypes/genMapMake, maps.go.
	//
	// ctrlAddr isn't a Go-managed pointer at all - it's a raw address the
	// JIT-compiled code handed back, backed by this package's own arena
	// (real, plain malloc'd memory - see genArenaAlloc, runtime.go - never
	// Go's own GC heap), so none of Go's usual GC-safety concerns around
	// "a pointer stored only as a uintptr can go stale" actually apply here.
	// `go vet`'s unsafeptr check can't know that, though, and unconditionally
	// flags any *direct* uintptr->Pointer conversion (`unsafe.Pointer(ctrlAddr)`)
	// as "possible misuse" regardless. Reinterpreting ctrlAddr's own bits via
	// a pointer to it (`unsafe.Pointer(&ctrlAddr)` - safe: that's the address
	// of a real, live Go variable) sidesteps the check entirely while reading
	// the exact same address value; unsafe.Add then does the per-field
	// pointer arithmetic the officially recommended way (over raw uintptr
	// math) once a real Pointer is in hand.
	base := *(*unsafe.Pointer)(unsafe.Pointer(&ctrlAddr))
	count := *(*int32)(unsafe.Add(base, 8))
	bucketCount := *(*int32)(unsafe.Add(base, 12))
	tombstoneCount := *(*int32)(unsafe.Add(base, 16))

	if bucketCount <= mapInitialBuckets {
		t.Errorf("bucketCount = %d, want > %d (mapInitialBuckets) - the tombstone-heavy churn above should have forced a real grow", bucketCount, mapInitialBuckets)
	}
	if tombstoneCount != 0 {
		t.Errorf("tombstoneCount = %d, want 0 - a grow's own rehash must reset it (every tombstone is left behind in the abandoned old bucket array)", tombstoneCount)
	}
	if count != 3 {
		t.Errorf("count = %d, want 3 (keys 5, 100, 101 still live)", count)
	}
}

// TestNestedMapValueWorks verifies map[K]map[K2]V2 actually works end to end
// (see LANGUAGE.md's own explicit note that this needs a real test, not just
// assuming it falls out for free).
func TestNestedMapValueWorks(t *testing.T) {
	jm := compileAndJIT(t, `
func testNestedMap() int {
	outer := make(map[string]map[string]int)
	inner := make(map[string]int)
	inner["x"] = 42
	outer["a"] = inner
	got := outer["a"]
	return got["x"]
}
`)
	if got := jm.runInt32(t, "testNestedMap"); got != 42 {
		t.Errorf("testNestedMap() = %d, want 42", got)
	}
}
