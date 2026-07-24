package codegen

import "testing"

// This file covers `for [key[, value]] := range subject { ... }` end to end,
// JIT-executed (see LANGUAGE.md's "Range loops" section): the two-binding
// form over both maps and arrays (fixed and dynamic), the one-binding
// map-binds-key/array-binds-index wrinkle proved with concrete runtime
// values (not just "it compiles"), the zero-binding form's side-effect-only
// iteration, break/continue loop control, and destructor unwinding for a
// non-copyable value binding.

// TestRangeForMapTwoBinding covers the two-binding form over a map, summing
// every value.
func TestRangeForMapTwoBinding(t *testing.T) {
	jm := compileAndJIT(t, `
func sumMap() int {
	m := make(map[string]int)
	m["a"] = 1
	m["b"] = 2
	m["c"] = 3
	total := 0
	for k, v := range m {
		total = total + v
	}
	return total
}
`)
	if got := jm.runInt32(t, "sumMap"); got != 6 {
		t.Errorf("sumMap() = %d, want 6", got)
	}
}

// TestRangeForMapOneBindingBindsKey is the concrete-value proof of Go's own
// real one-binding rule for a map: the single name binds the KEY, never the
// value - a single-entry map with key 5 mapping to value 100 makes the two
// impossible to confuse: if this bound the value instead, the result would
// be 100, not 5.
func TestRangeForMapOneBindingBindsKey(t *testing.T) {
	jm := compileAndJIT(t, `
func oneBindingKey() int {
	m := make(map[int]int)
	m[5] = 100
	result := 0
	for k := range m {
		result = k
	}
	return result
}
`)
	if got := jm.runInt32(t, "oneBindingKey"); got != 5 {
		t.Errorf("oneBindingKey() = %d, want 5 (the key, not 100 - the value)", got)
	}
}

// TestRangeForMapZeroBinding covers the zero-binding form - iteration for
// side effects only, no fresh bindings at all.
func TestRangeForMapZeroBinding(t *testing.T) {
	jm := compileAndJIT(t, `
func countMap() int {
	m := make(map[string]int)
	m["a"] = 1
	m["b"] = 2
	count := 0
	for range m {
		count = count + 1
	}
	return count
}
`)
	if got := jm.runInt32(t, "countMap"); got != 2 {
		t.Errorf("countMap() = %d, want 2", got)
	}
}

// TestRangeForMapBreakContinue covers loop control over a map: skipping one
// key via continue, order-independent since every other key still
// contributes regardless of visitation order.
func TestRangeForMapBreakContinue(t *testing.T) {
	jm := compileAndJIT(t, `
func sumExceptKey2() int {
	m := make(map[int]int)
	m[1] = 10
	m[2] = 20
	m[3] = 30
	total := 0
	for k, v := range m {
		if k == 2 {
			continue
		}
		total = total + v
	}
	return total
}
`)
	if got := jm.runInt32(t, "sumExceptKey2"); got != 40 {
		t.Errorf("sumExceptKey2() = %d, want 40 (10 + 30, skipping key 2's value 20)", got)
	}
}

// TestRangeForFixedArrayTwoBinding covers the two-binding form over a
// fixed-size [N]T array: i=0,v=10 / i=1,v=20 / i=2,v=30 -> sum(i)+sum(v) =
// (0+1+2) + (10+20+30) = 63.
func TestRangeForFixedArrayTwoBinding(t *testing.T) {
	jm := compileAndJIT(t, `
func sumFixed() int {
	a := [3]int{10, 20, 30}
	total := 0
	for i, v := range a {
		total = total + i + v
	}
	return total
}
`)
	if got := jm.runInt32(t, "sumFixed"); got != 63 {
		t.Errorf("sumFixed() = %d, want 63", got)
	}
}

// TestRangeForDynamicArrayTwoBinding is TestRangeForFixedArrayTwoBinding's
// dynamic-array (`[]T`, make'd) counterpart - same lowering strategy
// (genRangeForArray), just sourced from a slice instead of a fixed array.
func TestRangeForDynamicArrayTwoBinding(t *testing.T) {
	jm := compileAndJIT(t, `
func sumDynamic() int {
	a := make([]int, 3)
	a[0] = 10
	a[1] = 20
	a[2] = 30
	total := 0
	for i, v := range a {
		total = total + i + v
	}
	return total
}
`)
	if got := jm.runInt32(t, "sumDynamic"); got != 63 {
		t.Errorf("sumDynamic() = %d, want 63", got)
	}
}

// TestRangeForArrayOneBindingBindsIndex is the concrete-value proof of Go's
// own real one-binding rule for an array: the single name binds the INDEX,
// never the element - a single-element array holding 99 at index 0 makes
// the two impossible to confuse: if this bound the element instead, the
// result would be 99, not 0.
func TestRangeForArrayOneBindingBindsIndex(t *testing.T) {
	jm := compileAndJIT(t, `
func oneBindingIndex() int {
	a := [1]int{99}
	result := -1
	for i := range a {
		result = i
	}
	return result
}
`)
	if got := jm.runInt32(t, "oneBindingIndex"); got != 0 {
		t.Errorf("oneBindingIndex() = %d, want 0 (the index, not 99 - the element)", got)
	}
}

// TestRangeForArrayZeroBinding covers the zero-binding form over a dynamic
// array.
func TestRangeForArrayZeroBinding(t *testing.T) {
	jm := compileAndJIT(t, `
func countArr() int {
	s := make([]int, 4)
	count := 0
	for range s {
		count = count + 1
	}
	return count
}
`)
	if got := jm.runInt32(t, "countArr"); got != 4 {
		t.Errorf("countArr() = %d, want 4", got)
	}
}

// TestRangeForArrayBreakContinue mirrors TestBreakContinue's own plain-for
// loop-control coverage, over a range-for instead:
//
//	v=0 even -> continue
//	v=1 odd  -> total += 1 = 1
//	v=2 even -> continue
//	v=3 odd  -> total += 3 = 4
//	v=4 even -> continue
//	v=5      -> break
func TestRangeForArrayBreakContinue(t *testing.T) {
	jm := compileAndJIT(t, `
func sumSkip() int {
	a := [6]int{0, 1, 2, 3, 4, 5}
	total := 0
	for _, v := range a {
		if v == 5 {
			break
		}
		if v % 2 == 0 {
			continue
		}
		total = total + v
	}
	return total
}
`)
	if got := jm.runInt32(t, "sumSkip"); got != 4 {
		t.Errorf("sumSkip() = %d, want 4", got)
	}
}

// TestRangeForValueBindingDestructsEachIteration covers destructor
// unwinding for a non-copyable value binding (see bindRangeVar's own doc
// comment): a fixed array of a destructor-owning struct type is legal (see
// sema's typeIsNonCopyable - only a DYNAMIC array element type is rejected
// outright), so each iteration's own `r` binding is a fresh copy that must
// be destructed before the next iteration's copy is bound - ranging over
// all 3 elements with no early exit must fire the destructor exactly 3
// times, once per iteration, never more (a double-destruct) or fewer (a
// leaked entry left on Generator.destructors).
func TestRangeForValueBindingDestructsEachIteration(t *testing.T) {
	jm := compileAndJIT(t, `
struct Resource {
	id int
	constructor(x int) {
		this.id = x
	}
	destructor() {
		calls = calls + 1
	}
}

var calls int = 0

func runAll() int {
	arr := [3]Resource{Resource(1), Resource(2), Resource(3)}
	for _, r := range arr {
	}
	return calls
}
`)
	if got := jm.runInt32(t, "runAll"); got != 3 {
		t.Errorf("runAll() = %d, want 3 (one destructor call per iteration's own value binding)", got)
	}
}

// TestRangeForBreakDestructsCurrentIterationOnly is
// TestRangeForValueBindingDestructsEachIteration's break-unwind counterpart:
// breaking out on the second iteration (index 1) must still destruct that
// iteration's own already-bound `r` (break unwinds to the loop's own
// destructorBase, captured right before key/value are bound - see
// genRangeForArray's own doc comment) before leaving the loop, but never
// reaches the third element at all - exactly 2 destructor calls, not 3 (the
// full count) and not 1 (missing the breaking iteration's own unwind) - a
// separate JIT module from the "runs to completion" test above, so the
// shared `calls` global only ever measures this one call's own side
// effects.
func TestRangeForBreakDestructsCurrentIterationOnly(t *testing.T) {
	jm := compileAndJIT(t, `
struct Resource {
	id int
	constructor(x int) {
		this.id = x
	}
	destructor() {
		calls = calls + 1
	}
}

var calls int = 0

func runBreakEarly() int {
	arr := [3]Resource{Resource(1), Resource(2), Resource(3)}
	for i, r := range arr {
		if i == 1 {
			break
		}
	}
	return calls
}
`)
	if got := jm.runInt32(t, "runBreakEarly"); got != 2 {
		t.Errorf("runBreakEarly() = %d, want 2 (index 0's and index 1's own value bindings, never index 2's)", got)
	}
}
