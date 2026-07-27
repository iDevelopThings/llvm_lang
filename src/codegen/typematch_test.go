package codegen

import "testing"

// This file JIT-executes type matching over an Any subject (see LANGUAGE.md's
// "Type matching" section and genTypeMatchStmt) - the arm actually taken at
// runtime, and the value its binding actually holds, not merely that it
// compiles.

// TestTypeMatchPrimitiveArmDispatch covers the plain kind-switch path: one
// arm per primitive kind, each reached only by its own boxed value.
func TestTypeMatchPrimitiveArmDispatch(t *testing.T) {
	jm := compileAndJIT(t, `
func classify(a Any) int {
	match a {
		v int => { return 1 }
		v string => { return 2 }
		v bool => { return 3 }
		v f64 => { return 4 }
		v u8 => { return 5 }
		_ => { return 0 }
	}
}
func fromInt() int { return classify(Any(5)) }
func fromString() int { return classify(Any("hi")) }
func fromBool() int { return classify(Any(true)) }
func fromF64() int { return classify(Any(1.5)) }
func fromU8() int {
	var x u8 = 3
	return classify(Any(x))
}
func fromUnmatched() int {
	var x i16 = 3
	return classify(Any(x))
}
`)
	for _, tc := range []struct {
		fn   string
		want int32
	}{
		{"fromInt", 1},
		{"fromString", 2},
		{"fromBool", 3},
		{"fromF64", 4},
		{"fromU8", 5},
		{"fromUnmatched", 0},
	} {
		if got := jm.runInt32(t, tc.fn); got != tc.want {
			t.Errorf("%s() = %d, want %d", tc.fn, got, tc.want)
		}
	}
}

// TestTypeMatchBindingHoldsBoxedValue proves the binding is the real boxed
// value read back out, not just a correctly-selected arm.
func TestTypeMatchBindingHoldsBoxedValue(t *testing.T) {
	jm := compileAndJIT(t, `
func unbox(a Any) int {
	match a {
		v int => { return v + 1 }
		_ => { return -1 }
	}
}
func f() int { return unbox(Any(41)) }
`)
	if got := jm.runInt32(t, "f"); got != 42 {
		t.Errorf("f() = %d, want 42", got)
	}
}

// TestTypeMatchTwoStructArmsDisambiguate is the kind-bucket test: two
// different structs both report TypeStruct, so the switch case alone can't
// tell them apart - only the per-arm descriptor identity chain can.
func TestTypeMatchTwoStructArmsDisambiguate(t *testing.T) {
	jm := compileAndJIT(t, `
struct Point {
	X int
	Y int
}
struct Size {
	W int
	H int
}
func classify(a Any) int {
	match a {
		p Point => { return 100 + p.X }
		s Size => { return 200 + s.W }
		_ => { return 0 }
	}
}
func fromPoint() int { return classify(Any(Point{X: 7, Y: 8})) }
func fromSize() int { return classify(Any(Size{W: 9, H: 10})) }
func fromNeither() int { return classify(Any(5)) }
`)
	for _, tc := range []struct {
		fn   string
		want int32
	}{
		{"fromPoint", 107},
		{"fromSize", 209},
		{"fromNeither", 0},
	} {
		if got := jm.runInt32(t, tc.fn); got != tc.want {
			t.Errorf("%s() = %d, want %d", tc.fn, got, tc.want)
		}
	}
}

// TestTypeMatchDiscardArm covers the binding-less form - the arm still
// selects correctly with nothing loaded out of the box at all.
func TestTypeMatchDiscardArm(t *testing.T) {
	jm := compileAndJIT(t, `
struct Point {
	X int
	Y int
}
func classify(a Any) int {
	match a {
		Point => { return 1 }
		string => { return 2 }
		_ => { return 0 }
	}
}
func fromPoint() int { return classify(Any(Point{X: 1, Y: 2})) }
func fromString() int { return classify(Any("x")) }
func fromOther() int { return classify(Any(true)) }
`)
	for _, tc := range []struct {
		fn   string
		want int32
	}{
		{"fromPoint", 1},
		{"fromString", 2},
		{"fromOther", 0},
	} {
		if got := jm.runInt32(t, tc.fn); got != tc.want {
			t.Errorf("%s() = %d, want %d", tc.fn, got, tc.want)
		}
	}
}

// TestTypeMatchEnumArmMatchesEveryVariant covers anyDescMatches' own
// per-variant descriptor set: a boxed enum carries whichever variant was
// live when it was boxed, and an enum-typed arm matches all of them.
func TestTypeMatchEnumArmMatchesEveryVariant(t *testing.T) {
	jm := compileAndJIT(t, `
enum Shape {
	Circle(int)
	Square
	Rect { W int, H int }
}
func classify(a Any) int {
	match a {
		s Shape => { return 1 }
		v int => { return 2 }
		_ => { return 0 }
	}
}
func fromCircle() int { return classify(Any(Shape.Circle(3))) }
func fromSquare() int { return classify(Any(Shape.Square)) }
func fromRect() int { return classify(Any(Shape.Rect{W: 1, H: 2})) }
func fromInt() int { return classify(Any(5)) }
`)
	for _, tc := range []struct {
		fn   string
		want int32
	}{
		{"fromCircle", 1},
		{"fromSquare", 1},
		{"fromRect", 1},
		{"fromInt", 2},
	} {
		if got := jm.runInt32(t, tc.fn); got != tc.want {
			t.Errorf("%s() = %d, want %d", tc.fn, got, tc.want)
		}
	}
}

// TestTypeMatchTwoEnumArmsDisambiguate proves two different enums sharing
// the TypeEnum kind are still told apart - the same bucket-then-identity
// path the two-struct case exercises, one kind over.
func TestTypeMatchTwoEnumArmsDisambiguate(t *testing.T) {
	jm := compileAndJIT(t, `
enum Shape {
	Circle(int)
	Square
}
enum Color {
	Red
	Green
}
func classify(a Any) int {
	match a {
		s Shape => { return 1 }
		c Color => { return 2 }
		_ => { return 0 }
	}
}
func fromShape() int { return classify(Any(Shape.Square)) }
func fromColor() int { return classify(Any(Color.Green)) }
`)
	if got := jm.runInt32(t, "fromShape"); got != 1 {
		t.Errorf("fromShape() = %d, want 1", got)
	}
	if got := jm.runInt32(t, "fromColor"); got != 2 {
		t.Errorf("fromColor() = %d, want 2", got)
	}
}

// TestTypeMatchPointerArmIsPointeeAgnostic documents (and pins) the accepted
// imprecision described in DECISIONS.md: every pointer shares one interned
// descriptor, so a `*Point` arm matches any pointer at all.
func TestTypeMatchPointerArmIsPointeeAgnostic(t *testing.T) {
	jm := compileAndJIT(t, `
struct Point {
	X int
	Y int
}
func classify(a Any) int {
	match a {
		p *Point => { return 1 }
		_ => { return 0 }
	}
}
func fromPointPtr() int {
	p := Point{X: 1, Y: 2}
	return classify(Any(&p))
}
func fromIntPtr() int {
	x := 5
	return classify(Any(&x))
}
func fromNonPointer() int { return classify(Any(5)) }
`)
	if got := jm.runInt32(t, "fromPointPtr"); got != 1 {
		t.Errorf("fromPointPtr() = %d, want 1", got)
	}
	if got := jm.runInt32(t, "fromIntPtr"); got != 1 {
		t.Errorf("fromIntPtr() = %d, want 1 (a pointer arm matches any pointer - see DECISIONS.md)", got)
	}
	if got := jm.runInt32(t, "fromNonPointer"); got != 0 {
		t.Errorf("fromNonPointer() = %d, want 0", got)
	}
}

// TestTypeMatchArrayArms covers array arms: a fixed and a dynamic array of
// the same element type are distinct descriptors, so they must not collide.
func TestTypeMatchArrayArms(t *testing.T) {
	jm := compileAndJIT(t, `
func classify(a Any) int {
	match a {
		v []int => { return 1 }
		[3]int => { return 2 }
		_ => { return 0 }
	}
}
func fromDynamic() int {
	nums := make([]int, 0)
	nums = append(nums, 1)
	return classify(Any(nums))
}
func fromFixed() int {
	arr := [3]int{1, 2, 3}
	return classify(Any(arr))
}
func fromOther() int { return classify(Any(5)) }
`)
	for _, tc := range []struct {
		fn   string
		want int32
	}{
		{"fromDynamic", 1},
		{"fromFixed", 2},
		{"fromOther", 0},
	} {
		if got := jm.runInt32(t, tc.fn); got != tc.want {
			t.Errorf("%s() = %d, want %d", tc.fn, got, tc.want)
		}
	}
}

// TestTypeMatchAsExpression proves the expression-position form lowers
// through the same path, with every arm's yield feeding one phi.
func TestTypeMatchAsExpression(t *testing.T) {
	jm := compileAndJIT(t, `
func classify(a Any) int {
	return match a {
		v int => v * 2
		v bool => 99
		_ => -1
	}
}
func fromInt() int { return classify(Any(21)) }
func fromBool() int { return classify(Any(false)) }
func fromOther() int { return classify(Any("x")) }
`)
	for _, tc := range []struct {
		fn   string
		want int32
	}{
		{"fromInt", 42},
		{"fromBool", 99},
		{"fromOther", -1},
	} {
		if got := jm.runInt32(t, tc.fn); got != tc.want {
			t.Errorf("%s() = %d, want %d", tc.fn, got, tc.want)
		}
	}
}

// TestTypeMatchArmFallsThroughToWildcard covers a bucket whose identity
// chain fails outright: a struct kind is present, but the boxed struct is
// neither of the named ones, so control must reach the wildcard rather than
// the last tested arm.
func TestTypeMatchArmFallsThroughToWildcard(t *testing.T) {
	jm := compileAndJIT(t, `
struct A {
	X int
}
struct B {
	Y int
}
struct C {
	Z int
}
func classify(a Any) int {
	match a {
		x A => { return 1 }
		y B => { return 2 }
		_ => { return 9 }
	}
}
func fromC() int { return classify(Any(C{Z: 1})) }
`)
	if got := jm.runInt32(t, "fromC"); got != 9 {
		t.Errorf("fromC() = %d, want 9 (wildcard)", got)
	}
}

// TestTypeMatchArmBodyControlFlow proves an arm body that doesn't terminate
// falls through to whatever follows the match, alongside one that does.
func TestTypeMatchArmBodyControlFlow(t *testing.T) {
	jm := compileAndJIT(t, `
func classify(a Any) int {
	total := 0
	match a {
		v int => { total = total + v }
		v string => { return -1 }
		_ => { total = 100 }
	}
	return total + 1
}
func fromInt() int { return classify(Any(5)) }
func fromString() int { return classify(Any("x")) }
func fromOther() int { return classify(Any(true)) }
`)
	for _, tc := range []struct {
		fn   string
		want int32
	}{
		{"fromInt", 6},
		{"fromString", -1},
		{"fromOther", 101},
	} {
		if got := jm.runInt32(t, tc.fn); got != tc.want {
			t.Errorf("%s() = %d, want %d", tc.fn, got, tc.want)
		}
	}
}

// TestTypeMatchNestedInsideArm covers a type match nested inside another
// type match's own arm - each match's own merge/wildcard blocks must stay
// independent.
func TestTypeMatchNestedInsideArm(t *testing.T) {
	jm := compileAndJIT(t, `
func inner(a Any) int {
	match a {
		v string => { return 10 }
		_ => { return 20 }
	}
}
func outer(a Any, b Any) int {
	match a {
		v int => {
			match b {
				w bool => { return v + 1 }
				_ => { return v + inner(b) }
			}
		}
		_ => { return 0 }
	}
}
func f() int { return outer(Any(5), Any("x")) }
func g() int { return outer(Any(5), Any(true)) }
`)
	if got := jm.runInt32(t, "f"); got != 15 {
		t.Errorf("f() = %d, want 15", got)
	}
	if got := jm.runInt32(t, "g"); got != 6 {
		t.Errorf("g() = %d, want 6", got)
	}
}

// TestValueMatchMultiplicationPatternStillEvaluates is a regression test for
// this round's own `name *Type` grammar change (see
// parser.TestValueMatchMultiplicationPatternStaysOrdinary for the parse-shape
// half of this proof): an ordinary int value-match's `x * y`-shaped pattern
// must still evaluate as real multiplication, including RHS shapes (a call,
// a parenthesized expression) that are never actually ambiguous with the new
// pointer-type-pattern grammar at all.
func TestValueMatchMultiplicationPatternStillEvaluates(t *testing.T) {
	jm := compileAndJIT(t, `
func g() int { return 5 }
func classify(x int) int {
	base := 3
	match x {
		base * 2 => { return 1 }
		base * g() => { return 2 }
		base * (base + 1) => { return 3 }
		_ => { return 0 }
	}
}
`)
	for _, tc := range []struct {
		in   int32
		want int32
	}{
		{6, 1},  // base * 2
		{15, 2}, // base * g()
		{12, 3}, // base * (base + 1)
		{1, 0},  // no arm matches
	} {
		if got := jm.runInt32(t, "classify", tc.in); got != tc.want {
			t.Errorf("classify(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// TestTypeMatchPointerBindingUsableInBody proves an Any-match pointer arm's
// binding isn't just declared (TestTypeMatchPointerArmIsPointeeAgnostic
// already covers dispatch) but actually usable - dereferencing a field
// through it inside the arm body, the same `name *Type` grammar shape
// TestValueMatchMultiplicationPatternStillEvaluates proves stays ordinary
// multiplication for a non-Any subject.
func TestTypeMatchPointerBindingUsableInBody(t *testing.T) {
	jm := compileAndJIT(t, `
struct Point {
	x int
	y int
}
func describe(a Any) int {
	match a {
		p *Point => { return p.x + p.y }
		_ => { return -1 }
	}
}
func f() int {
	p := Point{3, 4}
	return describe(Any(&p))
}
`)
	if got := jm.runInt32(t, "f"); got != 7 {
		t.Errorf("f() = %d, want 7", got)
	}
}
