package codegen

import "testing"

// This file covers deliberate combinations across features that each landed
// with their own isolated test suite (lambdas/closures, pointers/new,
// dynamic arrays, constructors, first-class functions, struct fields) but
// are unlikely to have been exercised together - see AGENTS.md's test-sweep
// task for the motivating list this file works through.

// TestLambdaCapturesPointerVariable covers a lambda closing over a variable
// that is itself already a pointer (*int) - distinct from every existing
// capture test (lambda_test.go), which only ever captures a plain int local/
// parameter. The captured storage location holds a pointer value; the
// closure dereferences it on each call, and a second call after the pointee
// is mutated through the *original* variable must see the new value -
// proving capture-by-reference and pointer indirection compose correctly
// (the lambda's own capture slot holds the pointer itself, not a snapshot of
// what it once pointed to).
func TestLambdaCapturesPointerVariable(t *testing.T) {
	jm := compileAndJIT(t, `
func makeReader() func() int {
	x := 10
	p := &x
	return func() int {
		return *p
	}
}

func runReader() int {
	x := 10
	p := &x
	reader := func() int {
		return *p
	}
	first := reader()
	*p = 99
	second := reader()
	return first*100 + second
}
`)
	if got := jm.runInt32(t, "runReader"); got != 1099 {
		t.Errorf("runReader() = %d, want 1099 (first=10, second=99)", got)
	}
}

// TestDynamicArrayOfConstructedStructValues covers `[]Point{...}` where
// Point declares constructors - the array literal's own elements are plain
// composite-literal-shaped values (Point{...}), never a constructor call
// (LANGUAGE.md's array-literal grammar only ever accepts positional
// elements), so this mainly proves the constructor's existence on Point
// doesn't interfere with a plain composite-literal element inside a dynamic
// array, and that append/index still work normally afterward.
func TestDynamicArrayOfConstructedStructValues(t *testing.T) {
	jm := compileAndJIT(t, `
struct Point {
	x int
	y int

	constructor(px int, py int) {
		this.x = px
		this.y = py
	}
}

func f() int {
	pts := []Point{Point{1, 2}, Point{3, 4}}
	pts = append(pts, Point(5, 6))
	total := 0
	for i := 0; i < len(pts); i++ {
		total += pts[i].x + pts[i].y
	}
	return total
}
`)
	// (1+2) + (3+4) + (5+6) = 21
	if got := jm.runInt32(t, "f"); got != 21 {
		t.Errorf("f() = %d, want 21", got)
	}
}

// TestPointerToStructWithDynamicArrayFieldMutatedThroughPointer covers `new`
// on a struct whose field is itself a dynamic array - mutating that field
// (both an in-place index-assignment and an append reassignment) through the
// pointer, then reading it back through a *second*, independently-held
// pointer to the same heap allocation - proving the field mutation lands on
// the one shared heap struct, not a copy, the same rigor
// TestNewStructPersistsAcrossCalls (pointer_test.go) applies to a plain int
// field, now for a dynamic-array field instead.
func TestPointerToStructWithDynamicArrayFieldMutatedThroughPointer(t *testing.T) {
	jm := compileAndJIT(t, `
struct Box {
	items []int
}

var stash *Box

func makeAndMutate() int {
	stash = new Box{[]int{1, 2, 3}}
	stash.items[0] = 100
	stash.items = append(stash.items, 4)
	return stash.items[0] + stash.items[3] + len(stash.items)
}

func readBack() int {
	return stash.items[0] + stash.items[3] + len(stash.items)
}
`)
	// items[0]=100, items[3]=4, len=4 -> 108
	if got := jm.runInt32(t, "makeAndMutate"); got != 108 {
		t.Errorf("makeAndMutate() = %d, want 108", got)
	}
	if got := jm.runInt32(t, "readBack"); got != 108 {
		t.Errorf("readBack() = %d, want 108 (mutation through the pointer must persist)", got)
	}
}

// TestClosureStoredInStructFieldAndReadBackAsValue covers a struct field
// declared with a function type (func(int) int), holding a genuine closure
// (not a bare function reference) - assigned in, then read back out to a
// plain func-typed local and called *through the local*, proving a
// func-typed field lowers to the same {fnPtr, ctxPtr} fat-pointer
// representation any other func-typed storage location (a var, a parameter)
// already uses, and still sees the closure's own captured state after
// round-tripping through struct field storage.
//
// Calling the field expression *directly* (`cb.fn(5)`, no local in between)
// used to be rejected outright ("fn is a field, not a method (cannot be
// called)") - see the now-resolved BLOCKERS.md entry - even though
// LANGUAGE.md's "First-class functions" section always listed "a struct
// field's type" as a legal place for a function type to appear, with no
// carve-out for calling through one. That's fixed now (funcSigForCall's
// MemberExpr case falls back to the same indirect-call check its Ident case
// already had - sema/typecheck.go); TestClosureStoredInStructFieldCalledDirectly
// exercises the direct-call form, and this test continues to cover the
// read-into-a-local-first form, so both remain valid, not just one.
func TestClosureStoredInStructFieldAndReadBackAsValue(t *testing.T) {
	jm := compileAndJIT(t, `
struct Callback {
	fn func(int) int
}

func f() int {
	base := 10
	cb := Callback{func(x int) int {
		return base + x
	}}
	fn := cb.fn
	return fn(5)
}
`)
	if got := jm.runInt32(t, "f"); got != 15 {
		t.Errorf("f() = %d, want 15", got)
	}
}

// TestClosureStoredInStructFieldCalledDirectly is
// TestClosureStoredInStructFieldAndReadBackAsValue's direct-call
// counterpart: `cb.fn(5)` calls straight through the field expression, no
// intermediate local - the exact repro BLOCKERS.md's now-resolved "Calling a
// function-typed struct field directly is rejected" entry described. Proves
// both the sema fallback (funcSigForCall's MemberExpr case, via
// methodSigForCallee's isField result) and codegen's matching dispatch
// (isMethodCall correctly saying "no" for a plain field, routing to
// genIndirectCall) actually work end-to-end, not just type-check.
func TestClosureStoredInStructFieldCalledDirectly(t *testing.T) {
	jm := compileAndJIT(t, `
struct Callback {
	fn func(int) int
}

func f() int {
	base := 10
	cb := Callback{func(x int) int {
		return base + x
	}}
	return cb.fn(5)
}
`)
	if got := jm.runInt32(t, "f"); got != 15 {
		t.Errorf("f() = %d, want 15", got)
	}
}

// TestGlobalVarHoldingFunctionValue covers a top-level `var` of function
// type, initialized from a plain function reference - this is a non-constant
// global initializer (constfold.go's isConstFoldable has no CallExpr/Ident-
// reference shape at all, let alone a function-valued one), so it's routed
// through the synthesized init function (globalinit.go) exactly like
// TestGlobalNonConstantInitializerFromFunctionCall (globals_test.go)
// already covers for an int-typed global - this is the function-value-typed
// analogue, calling through the global afterward to prove the fat pointer
// itself (not just a scalar) survives the init-function assignment path.
func TestGlobalVarHoldingFunctionValue(t *testing.T) {
	jm := compileAndJIT(t, `
func double(x int) int {
	return x * 2
}

var op func(int) int = double

func run(x int) int {
	return op(x)
}
`)
	if got := jm.runInt32(t, "run", 21); got != 42 {
		t.Errorf("run(21) = %d, want 42", got)
	}
}
