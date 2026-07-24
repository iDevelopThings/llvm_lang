package codegen

import "testing"

// --- basic value production, both arm shapes, both subject kinds ---

// TestMatchExprBareArmValue covers a bare-expression arm (`pattern => expr`)
// used as a `:=` right-hand side - the desugared implicit-yield path.
func TestMatchExprBareArmValue(t *testing.T) {
	jm := compileAndJIT(t, `
func classify(x int) int {
	y := match x {
		1, 2 => 10
		_ => 20
	}
	return y
}
`)
	if got := jm.runInt32(t, "classify", 1); got != 10 {
		t.Errorf("classify(1) = %d, want 10", got)
	}
	if got := jm.runInt32(t, "classify", 9); got != 20 {
		t.Errorf("classify(9) = %d, want 20", got)
	}
}

// TestMatchExprBlockArmMultipleYields covers a block-bodied arm with several
// yields reachable along different paths (the LANGUAGE.md motivating
// example: an if with no else, followed by a trailing yield) - both arm
// shapes (this one and the wildcard's bare-expression form) coexisting in
// the same match, exactly like the worked example.
func TestMatchExprBlockArmMultipleYields(t *testing.T) {
	jm := compileAndJIT(t, `
func describe(x int, special bool) int {
	y := match x {
		1 => {
			if special {
				yield 100
			}
			yield 1
		}
		_ => 0
	}
	return y
}
`)
	if got := jm.runInt32(t, "describe", 1, 0); got != 1 {
		t.Errorf("describe(1, false) = %d, want 1", got)
	}
	if got := jm.runInt32(t, "describe", 1, 1); got != 100 {
		t.Errorf("describe(1, true) = %d, want 100", got)
	}
	if got := jm.runInt32(t, "describe", 9, 0); got != 0 {
		t.Errorf("describe(9, false) = %d, want 0", got)
	}
}

// TestMatchExprAsReturnValue covers `return match ... {}` directly, with no
// intermediate `:=` at all - proving the match expression's own value flows
// straight out as a genuine expression, not one special-cased to `:=` alone.
func TestMatchExprAsReturnValue(t *testing.T) {
	jm := compileAndJIT(t, `
func classify(x int) int {
	return match x {
		1, 2, 3 => 1
		4, 5 => {
			yield 2
		}
		_ => 0
	}
}
`)
	cases := []struct {
		in, want int32
	}{
		{1, 1}, {3, 1}, {5, 2}, {9, 0},
	}
	for _, c := range cases {
		if got := jm.runInt32(t, "classify", c.in); got != c.want {
			t.Errorf("classify(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

// TestMatchExprAsCallArgument covers a match expression used directly as a
// function call argument, proving it's a genuine expression usable anywhere
// one is legal, not just `:=`/`return`.
func TestMatchExprAsCallArgument(t *testing.T) {
	jm := compileAndJIT(t, `
func double(n int) int {
	return n * 2
}
func classify(x int) int {
	return double(match x {
		1 => 5
		_ => 7
	})
}
`)
	if got := jm.runInt32(t, "classify", 1); got != 10 {
		t.Errorf("classify(1) = %d, want 10", got)
	}
	if got := jm.runInt32(t, "classify", 9); got != 14 {
		t.Errorf("classify(9) = %d, want 14", got)
	}
}

// TestMatchExprEnumSubject covers an enum-match used as an expression -
// genMatchExpr's own frame threads through genMatchStmt's existing enum-path
// switch-on-discriminant lowering completely unchanged.
func TestMatchExprEnumSubject(t *testing.T) {
	jm := compileAndJIT(t, `
enum Shape {
	Circle,
	Square
}
func area(s Shape) int {
	return match s {
		Shape.Circle => 1
		Shape.Square => {
			yield 2
		}
	}
}
func areaOfCircle() int {
	return area(Shape.Circle)
}
func areaOfSquare() int {
	return area(Shape.Square)
}
`)
	if got := jm.runInt32(t, "areaOfCircle"); got != 1 {
		t.Errorf("areaOfCircle() = %d, want 1", got)
	}
	if got := jm.runInt32(t, "areaOfSquare"); got != 2 {
		t.Errorf("areaOfSquare() = %d, want 2", got)
	}
}

// TestMatchExprBoolSubject covers a bool value-match used as an expression -
// genValueMatchStmt's own comparison-chain lowering threaded through the
// same frame.
func TestMatchExprBoolSubject(t *testing.T) {
	jm := compileAndJIT(t, `
func toggle(b bool) int {
	return match b {
		true => 1
		_ => 0
	}
}
`)
	if got := jm.runInt32(t, "toggle", 1); got != 1 {
		t.Errorf("toggle(true) = %d, want 1", got)
	}
	if got := jm.runInt32(t, "toggle", 0); got != 0 {
		t.Errorf("toggle(false) = %d, want 0", got)
	}
}

// TestNestedMatchExprInYield covers `yield match other {...}` - a match
// expression nested inside another one's own yield, exercising
// Generator.matchExprStack's own real stacking (genMatchExpr pushes its own
// frame on top of the outer one, so the inner match's own yields/phi never
// interfere with the outer's).
func TestNestedMatchExprInYield(t *testing.T) {
	jm := compileAndJIT(t, `
func f(x int, y int) int {
	return match x {
		1 => {
			yield match y {
				1 => 10
				_ => 20
			}
		}
		_ => 99
	}
}
`)
	if got := jm.runInt32(t, "f", 1, 1); got != 10 {
		t.Errorf("f(1, 1) = %d, want 10", got)
	}
	if got := jm.runInt32(t, "f", 1, 9); got != 20 {
		t.Errorf("f(1, 9) = %d, want 20", got)
	}
	if got := jm.runInt32(t, "f", 9, 1); got != 99 {
		t.Errorf("f(9, 1) = %d, want 99", got)
	}
}

// --- destructor interaction ---

// TestMatchExprYieldFiresArmLocalDestructorAtYieldPoint is this round's own
// destructor-interaction test: a match-expression arm's block declares a
// destructor-having local BEFORE its own yield - the local's destructor
// must fire right there, at the yield (genYieldStmt's own
// unwindDestructorsTo(top.destructorBase) call), not be skipped and not
// fire twice.
func TestMatchExprYieldFiresArmLocalDestructorAtYieldPoint(t *testing.T) {
	jm := compileAndJIT(t, `
struct Resource {
	constructor() {
	}
	destructor() {
		calls = calls + 1
	}
}

var calls int = 0

func useIt(x int) int {
	y := match x {
		1 => {
			r := Resource()
			yield 10
		}
		_ => 20
	}
	return y
}

func afterUse(x int) int {
	v := useIt(x)
	return calls*1000 + v
}
`)
	// calls is a real global, persisting across these two calls within the
	// same JIT module (exactly like any other global variable would) - so
	// the wildcard case (which never declares r at all) is checked FIRST,
	// while calls is still genuinely 0, rather than trying to reason about
	// an already-incremented baseline.
	//
	// x=9 takes the wildcard - calls must still be 0.
	if got := jm.runInt32(t, "afterUse", 9); got != 20 {
		t.Errorf("afterUse(9) = %d, want 20 (calls=0, v=20 - no Resource ever declared on this path)", got)
	}
	// x=1 takes the arm that declares r - its destructor must have fired
	// exactly once (calls=1) by the time useIt returns.
	if got := jm.runInt32(t, "afterUse", 1); got != 1010 {
		t.Errorf("afterUse(1) = %d, want 1010 (calls=1, v=10 - r's destructor must fire exactly once at the yield)", got)
	}
}

// TestMatchExprYieldDoesNotUnwindEnclosingScopeLocal is this round's own
// destructor-interaction test's other required half: a destructor-having
// local declared BEFORE the match expression itself (in the function's own
// enclosing scope) must be completely untouched by a yield's own unwind -
// genYieldStmt unwinds only back to the match expression's own
// destructorBase (captured at the match's own entry, which already
// includes this local), never past it.
func TestMatchExprYieldDoesNotUnwindEnclosingScopeLocal(t *testing.T) {
	jm := compileAndJIT(t, `
struct Resource {
	constructor() {
	}
	destructor() {
		calls = calls + 1
	}
}

var calls int = 0

func duringMatch(x int) int {
	r := Resource()
	y := match x {
		1 => 10
		_ => 20
	}
	// Captured before this function's own return statement unwinds r's
	// destructor - if a yield's own unwind had incorrectly reached past the
	// match's own entry and destructed r early, calls would already be 1
	// here.
	afterMatch := calls
	return afterMatch*100000 + y
}

func afterDuringMatchReturns(x int) int {
	v := duringMatch(x)
	return v + calls
}
`)
	// v = afterMatch*100000 + y = 0*100000 + 10 = 10 (afterMatch must be 0 -
	// r's destructor must NOT have fired during the match's own yield).
	// Then, once duringMatch itself actually returns, r's destructor DOES
	// fire normally (once) - calls becomes 1, added on top by the wrapper.
	if got := jm.runInt32(t, "afterDuringMatchReturns", 1); got != 11 {
		t.Errorf("afterDuringMatchReturns(1) = %d, want 11 (afterMatch=0 proves the yield left r untouched; +1 proves r's destructor still fires normally once duringMatch actually returns)", got)
	}
}
