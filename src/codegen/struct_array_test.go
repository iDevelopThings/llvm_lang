package codegen

import "testing"

// TestStructFieldsAndMethods covers struct field access/assignment through
// `this` (member GEP), a mutating method (every method is implicitly
// by-reference - see AGENTS.md), and a composite literal.
func TestStructFieldsAndMethods(t *testing.T) {
	jm := compileAndJIT(t, `
struct Point {
	x int
	y int
}

func (Point) sum() int {
	return this.x + this.y
}

func (Point) move(dx int, dy int) {
	this.x = this.x + dx
	this.y = this.y + dy
}

func pointSumAfterMove(px int, py int, dx int, dy int) int {
	p := Point{px, py}
	p.move(dx, dy)
	return p.sum()
}
`)

	if got := jm.runInt32(t, "pointSumAfterMove", 1, 2, 3, 4); got != 10 {
		t.Errorf("pointSumAfterMove(1,2,3,4) = %d, want 10", got)
	}
}

// TestKeyedStructLiteralZeroFillsUnmentionedFields covers a keyed composite
// literal that only names some fields - the rest must be zero, not garbage
// stack memory (see genCompositeLitInto).
func TestKeyedStructLiteralZeroFillsUnmentionedFields(t *testing.T) {
	jm := compileAndJIT(t, `
struct Point {
	x int
	y int
}

func onlyX() int {
	p := Point{x: 7}
	return p.x + p.y
}
`)

	if got := jm.runInt32(t, "onlyX"); got != 7 {
		t.Errorf("onlyX() = %d, want 7", got)
	}
}

// TestArrayIndexing covers fixed-size array element load/store via GEP, and
// an array composite literal.
func TestArrayIndexing(t *testing.T) {
	jm := compileAndJIT(t, `
func arraySum() int {
	a := [5]int{1, 2, 3, 4, 5}
	total := 0
	for i := 0; i < 5; i++ {
		total += a[i]
	}
	return total
}
`)

	if got := jm.runInt32(t, "arraySum"); got != 15 {
		t.Errorf("arraySum() = %d, want 15", got)
	}
}

// TestArrayZeroInitAndAssign covers `var a [N]T` with no initializer
// (zero-filled) plus index-assignment.
func TestArrayZeroInitAndAssign(t *testing.T) {
	jm := compileAndJIT(t, `
func arrayAssign() int {
	var a [3]int
	zeroSum := a[0] + a[1] + a[2]
	a[0] = 10
	a[1] = 20
	a[2] = a[0] + a[1]
	return zeroSum + a[2]
}
`)

	if got := jm.runInt32(t, "arrayAssign"); got != 30 {
		t.Errorf("arrayAssign() = %d, want 30", got)
	}
}

// TestLenOnFixedArrayAndString covers genLenCall's other two branches
// (runtime.go): a fixed-size array's length folds directly to a compile-time
// constant (the same value its own bounds check already uses), and a
// string's length reads its runtime {ptr,len} field - both previously
// exercised only at the sema level (see sema's TestLenOnFixedArray/
// TestLenOnString), never actually JIT-executed and asserted on here, unlike
// the dynamic-array case (TestMakeIndexAndLen, dynamic_array_test.go).
func TestLenOnFixedArrayAndString(t *testing.T) {
	jm := compileAndJIT(t, `
func fixedArrayLen() int {
	a := [5]int{1, 2, 3, 4, 5}
	return len(a)
}

func stringLen() int {
	s := "hello"
	return len(s)
}
`)
	if got := jm.runInt32(t, "fixedArrayLen"); got != 5 {
		t.Errorf("fixedArrayLen() = %d, want 5", got)
	}
	if got := jm.runInt32(t, "stringLen"); got != 5 {
		t.Errorf("stringLen() = %d, want 5", got)
	}
}

// TestStructWithArrayField covers a struct whose field is itself a fixed-
// size array, exercising a GEP chain through both a struct field and an
// array index in the same expression.
func TestStructWithArrayField(t *testing.T) {
	jm := compileAndJIT(t, `
struct Row {
	values [3]int
}

func rowSum() int {
	r := Row{[3]int{4, 5, 6}}
	return r.values[0] + r.values[1] + r.values[2]
}
`)

	if got := jm.runInt32(t, "rowSum"); got != 15 {
		t.Errorf("rowSum() = %d, want 15", got)
	}
}
