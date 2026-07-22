package codegen

import "testing"

// TestStructEqualityFieldWise covers `==`/`!=` on two struct values of the
// same type: equal iff every corresponding field is equal (see AGENTS.md's
// Operators section and genValueEqual in expr.go).
func TestStructEqualityFieldWise(t *testing.T) {
	jm := compileAndJIT(t, `
struct Point {
	x int
	y int
}

func pointsEqual(ax int, ay int, bx int, by int) bool {
	a := Point{ax, ay}
	b := Point{bx, by}
	return a == b
}

func pointsNotEqual(ax int, ay int, bx int, by int) bool {
	a := Point{ax, ay}
	b := Point{bx, by}
	return a != b
}
`)

	if !jm.runBool(t, "pointsEqual", 1, 2, 1, 2) {
		t.Error("pointsEqual(1,2,1,2) = false, want true")
	}
	if jm.runBool(t, "pointsEqual", 1, 2, 1, 3) {
		t.Error("pointsEqual(1,2,1,3) = true, want false (differing y field)")
	}
	if jm.runBool(t, "pointsNotEqual", 1, 2, 1, 2) {
		t.Error("pointsNotEqual(1,2,1,2) = true, want false")
	}
	if !jm.runBool(t, "pointsNotEqual", 1, 2, 1, 3) {
		t.Error("pointsNotEqual(1,2,1,3) = false, want true")
	}
}

// TestArrayEqualityElementWise covers `==`/`!=` on two fixed-size array
// values of the same type: equal iff every corresponding element is equal.
func TestArrayEqualityElementWise(t *testing.T) {
	jm := compileAndJIT(t, `
func arraysEqual(a0 int, a1 int, a2 int, b0 int, b1 int, b2 int) bool {
	a := [3]int{a0, a1, a2}
	b := [3]int{b0, b1, b2}
	return a == b
}

func arraysNotEqual(a0 int, a1 int, a2 int, b0 int, b1 int, b2 int) bool {
	a := [3]int{a0, a1, a2}
	b := [3]int{b0, b1, b2}
	return a != b
}
`)

	if !jm.runBool(t, "arraysEqual", 1, 2, 3, 1, 2, 3) {
		t.Error("arraysEqual(1,2,3,1,2,3) = false, want true")
	}
	if jm.runBool(t, "arraysEqual", 1, 2, 3, 1, 9, 3) {
		t.Error("arraysEqual(1,2,3,1,9,3) = true, want false (differing element 1)")
	}
	if jm.runBool(t, "arraysNotEqual", 1, 2, 3, 1, 2, 3) {
		t.Error("arraysNotEqual(1,2,3,1,2,3) = true, want false")
	}
	if !jm.runBool(t, "arraysNotEqual", 1, 2, 3, 1, 9, 3) {
		t.Error("arraysNotEqual(1,2,3,1,9,3) = false, want true")
	}
}

// TestNestedStructWithArrayFieldEquality proves the recursion actually
// descends more than one level deep - not just comparing top-level scalar
// fields - by comparing two structs whose single field is itself a
// fixed-size array.
func TestNestedStructWithArrayFieldEquality(t *testing.T) {
	jm := compileAndJIT(t, `
struct Row {
	values [3]int
}

func rowsEqual(a0 int, a1 int, a2 int, b0 int, b1 int, b2 int) bool {
	a := Row{[3]int{a0, a1, a2}}
	b := Row{[3]int{b0, b1, b2}}
	return a == b
}
`)

	if !jm.runBool(t, "rowsEqual", 1, 2, 3, 1, 2, 3) {
		t.Error("rowsEqual(1,2,3,1,2,3) = false, want true")
	}
	if jm.runBool(t, "rowsEqual", 1, 2, 3, 1, 2, 9) {
		t.Error("rowsEqual(1,2,3,1,2,9) = true, want false (differing nested array element)")
	}
}

// TestArrayOfStructsEquality proves the same recursion in the other
// direction: an array whose element type is itself a struct.
func TestArrayOfStructsEquality(t *testing.T) {
	jm := compileAndJIT(t, `
struct Point {
	x int
	y int
}

func pointArraysEqual(ax0 int, ay0 int, ax1 int, ay1 int, bx0 int, by0 int, bx1 int, by1 int) bool {
	a := [2]Point{Point{ax0, ay0}, Point{ax1, ay1}}
	b := [2]Point{Point{bx0, by0}, Point{bx1, by1}}
	return a == b
}
`)

	if !jm.runBool(t, "pointArraysEqual", 1, 2, 3, 4, 1, 2, 3, 4) {
		t.Error("pointArraysEqual with identical points = false, want true")
	}
	if jm.runBool(t, "pointArraysEqual", 1, 2, 3, 4, 1, 2, 3, 9) {
		t.Error("pointArraysEqual with a differing nested field = true, want false")
	}
}
