package codegen

import "testing"

// TestConstructorZeroAndMultiArgOnSameStruct JIT-executes a real program
// using both a zero-arg and a one-arg constructor declared on the same
// struct (see LANGUAGE.md's "Constructors" section), asserting on the
// resulting field values - the exact example from that section:
// `Point(5)` calls the one-arg constructor (a.x == 5), `Point()` calls the
// zero-arg one (b.x == 99), and a plain composite literal `Point{1}`
// remains completely unaffected, coexisting with both constructors.
func TestConstructorZeroAndMultiArgOnSameStruct(t *testing.T) {
	jm := compileAndJIT(t, `
struct Point {
	x int

	constructor() {
		this.x = 99
	}
	constructor(v int) {
		this.x = v
	}
}

func sumConstructed() int {
	a := Point(5)
	b := Point()
	c := Point{1}
	return a.x + b.x + c.x
}
`)

	if got := jm.runInt32(t, "sumConstructed"); got != 105 {
		t.Errorf("sumConstructed() = %d, want 105 (5 + 99 + 1)", got)
	}
}

// TestConstructorWithMultipleParams covers a constructor taking more than
// one parameter, exercising the implicit-receiver-plus-declared-params
// calling convention (declareConstructorSignature/genConstructorCall) beyond
// the zero/one-arg cases above.
func TestConstructorWithMultipleParams(t *testing.T) {
	jm := compileAndJIT(t, `
struct Point {
	x int
	y int

	constructor(px int, py int) {
		this.x = px
		this.y = py
	}
}

func pointSum(px int, py int) int {
	p := Point(px, py)
	return p.x + p.y
}
`)

	if got := jm.runInt32(t, "pointSum", 3, 4); got != 7 {
		t.Errorf("pointSum(3,4) = %d, want 7", got)
	}
}

// TestConstructorCanCallAnotherConstructor covers a constructor body itself
// constructing another struct via Name(args) - the same call-expression
// path any other function body already uses, exercising
// declareConstructorSignature's "declared before any body is generated,
// regardless of order" guarantee.
func TestConstructorCanCallAnotherConstructor(t *testing.T) {
	jm := compileAndJIT(t, `
struct Inner {
	v int

	constructor(v int) {
		this.v = v
	}
}

struct Outer {
	inner Inner

	constructor(v int) {
		this.inner = Inner(v)
	}
}

func outerValue(v int) int {
	o := Outer(v)
	return o.inner.v
}
`)

	if got := jm.runInt32(t, "outerValue", 42); got != 42 {
		t.Errorf("outerValue(42) = %d, want 42", got)
	}
}
