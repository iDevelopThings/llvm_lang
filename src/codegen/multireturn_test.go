package codegen

import "testing"

// This file covers this round's Go-style multi-return values feature (see
// LANGUAGE.md's "Go-style multi-return values" section and CODEGEN.md's own
// section of the same name for the aggregate-struct-return lowering):
// return a, b, ...`, `a, b := f()`, and `a, b = f()`, JIT-executed end to
// end - following lambda_test.go's/extern_test.go's own established pattern
// of folding a multi-value result into a single scalar this suite's raw-
// syscall JIT harness (runInt32/runInt64/runBool) can observe directly,
// since a multi-return function's own real ABI returns an anonymous LLVM
// struct, not a plain scalar - see compileAndJIT's own doc comment for why
// every JIT-executed test in this package already follows this shape for
// any non-scalar result (a string, a struct, ...), not just this feature.

// TestMultiReturnDivideIdiom is the exact worked "error-handling idiom"
// motivating this whole feature (see LANGUAGE.md): divide-by-zero signaled
// via a second bool result instead of a sentinel value, both branches
// exercised.
func TestMultiReturnDivideIdiom(t *testing.T) {
	jm := compileAndJIT(t, `
func divide(a int, b int) (int, bool) {
	if b == 0 {
		return 0, false
	}
	return a / b, true
}

func testOk() int {
	result, ok := divide(10, 2)
	if !ok {
		return -1
	}
	return result
}

func testDivByZero() int {
	result, ok := divide(10, 0)
	if ok {
		return -1
	}
	return result
}
`)

	if got := jm.runInt32(t, "testOk"); got != 5 {
		t.Errorf("testOk() = %d, want 5", got)
	}
	if got := jm.runInt32(t, "testDivByZero"); got != 0 {
		t.Errorf("testDivByZero() = %d, want 0", got)
	}
}

// TestMultiReturnFindIdiom mirrors Go's own map-lookup-style `v, ok := m[k]`
// idiom conceptually: index-or-found-flag over a fixed-size array.
func TestMultiReturnFindIdiom(t *testing.T) {
	jm := compileAndJIT(t, `
func find(items [5]int, target int) (int, bool) {
	i := 0
	for i < 5 {
		if items[i] == target {
			return i, true
		}
		i++
	}
	return -1, false
}

func testFound() int {
	items := [5]int{10, 20, 30, 40, 50}
	idx, ok := find(items, 30)
	if !ok {
		return -1
	}
	return idx
}

func testNotFound() bool {
	items := [5]int{10, 20, 30, 40, 50}
	idx, ok := find(items, 99)
	return ok || idx != -1
}
`)

	if got := jm.runInt32(t, "testFound"); got != 2 {
		t.Errorf("testFound() = %d, want 2", got)
	}
	if got := jm.runBool(t, "testNotFound"); got != false {
		t.Errorf("testNotFound() = %v, want false", got)
	}
}

// TestMultiReturnMixedWidthTypes proves the aggregate lowering isn't
// accidentally assuming uniform component types - i64 alongside bool.
func TestMultiReturnMixedWidthTypes(t *testing.T) {
	jm := compileAndJIT(t, `
func bigOrSmall(x i64) (i64, bool) {
	if x > 1000000000 {
		return x * 2, true
	}
	return x, false
}

func testBig() i64 {
	v, isBig := bigOrSmall(4000000000)
	if !isBig {
		return -1
	}
	return v
}

func testSmall() bool {
	v, isBig := bigOrSmall(5)
	return isBig || v != 5
}
`)

	if got := jm.runInt64(t, "testBig"); got != 8000000000 {
		t.Errorf("testBig() = %d, want 8000000000", got)
	}
	if got := jm.runBool(t, "testSmall"); got != false {
		t.Errorf("testSmall() = %v, want false", got)
	}
}

// TestMultiReturnFloatAndStringTypes covers a further mixed-kind pair
// (f64/string) - proving the aggregate construction/extraction generalizes
// across every LLVM type shape this language has (a scalar float alongside
// a real {ptr,i32} string struct field within the outer aggregate).
func TestMultiReturnFloatAndStringTypes(t *testing.T) {
	jm := compileAndJIT(t, `
func describe(x f64) (f64, string) {
	if x < 0.0 {
		return x, "negative"
	}
	return x, "non-negative"
}

func testNegative() bool {
	v, label := describe(-2.5)
	return v != -2.5 || label != "negative"
}

func testNonNegative() bool {
	v, label := describe(3.5)
	return v != 3.5 || label != "non-negative"
}
`)

	if got := jm.runBool(t, "testNegative"); got != false {
		t.Errorf("testNegative() = %v, want false", got)
	}
	if got := jm.runBool(t, "testNonNegative"); got != false {
		t.Errorf("testNonNegative() = %v, want false", got)
	}
}

// TestMultiAssignDestructuringIntoExistingTargets covers the `=` form
// (MultiAssignStmt), including a struct field and an array element as
// targets - not just plain identifiers - proving the assignment form isn't
// special-cased to only idents (see LANGUAGE.md).
func TestMultiAssignDestructuringIntoExistingTargets(t *testing.T) {
	jm := compileAndJIT(t, `
struct Pair {
	q int
	ok bool
}

func divide(a int, b int) (int, bool) {
	if b == 0 {
		return 0, false
	}
	return a / b, true
}

func testAssignIntoStructFieldAndArrayElem() int {
	p := Pair{0, false}
	arr := [1]bool{false}
	p.q, arr[0] = divide(20, 4)
	if !arr[0] {
		return -1
	}
	return p.q
}

func testReassignPlainVars() int {
	q := 0
	ok := false
	q, ok = divide(9, 3)
	if !ok {
		return -1
	}
	return q
}
`)

	if got := jm.runInt32(t, "testAssignIntoStructFieldAndArrayElem"); got != 5 {
		t.Errorf("testAssignIntoStructFieldAndArrayElem() = %d, want 5", got)
	}
	if got := jm.runInt32(t, "testReassignPlainVars"); got != 3 {
		t.Errorf("testReassignPlainVars() = %d, want 3", got)
	}
}

// TestMultiReturnThreeValues proves the feature generalizes past exactly two
// return values.
func TestMultiReturnThreeValues(t *testing.T) {
	jm := compileAndJIT(t, `
func split(x int) (int, int, bool) {
	return x / 2, x % 2, x >= 0
}

func testSplit() int {
	half, rem, nonNeg := split(7)
	if !nonNeg {
		return -1
	}
	return half*10 + rem
}
`)

	if got := jm.runInt32(t, "testSplit"); got != 31 {
		t.Errorf("testSplit() = %d, want 31 (half=3, rem=1)", got)
	}
}
