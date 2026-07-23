package codegen

import "testing"

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
