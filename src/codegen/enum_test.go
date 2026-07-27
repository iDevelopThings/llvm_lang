package codegen

import "testing"

// --- construction & basic values ---

func TestEnumUnitVariantConstructionAndDiscriminant(t *testing.T) {
	jm := compileAndJIT(t, `
enum Shape {
	Point,
	Circle(f64)
}
func isPoint() bool {
	s := Shape.Point
	match s {
		Shape.Point => {
			return true
		}
		Shape.Circle(r) => {
			return false
		}
	}
}
`)
	if got := jm.runBool(t, "isPoint"); !got {
		t.Errorf("isPoint() = %v, want true", got)
	}
}

// TestEnumTupleVariantConstructionAndDestructuring, like every other f64-
// producing test in this package, asserts via a bool-returning function
// comparing against the expected value inside the language itself - a float
// result can't safely round-trip through this test harness's raw
// syscall.SyscallN calling convention (see numeric_test.go's own doc
// comment on TestFloatArithmetic for why).
func TestEnumTupleVariantConstructionAndDestructuring(t *testing.T) {
	jm := compileAndJIT(t, `
enum Shape {
	Circle(f64),
	Point
}
func radiusIsFive() bool {
	s := Shape.Circle(5.0)
	match s {
		Shape.Circle(r) => {
			return r == 5.0
		}
		Shape.Point => {
			return false
		}
	}
}
`)
	if got := jm.runBool(t, "radiusIsFive"); !got {
		t.Errorf("radiusIsFive() = %v, want true", got)
	}
}

func TestEnumStructVariantConstructionAndDestructuring(t *testing.T) {
	jm := compileAndJIT(t, `
enum Shape {
	Triangle { base f64, height f64 }
}
func areaIsSix() bool {
	s := Shape.Triangle{base: 4.0, height: 3.0}
	match s {
		Shape.Triangle{base: b, height: h} => {
			return 0.5 * b * h == 6.0
		}
	}
}
`)
	if got := jm.runBool(t, "areaIsSix"); !got {
		t.Errorf("areaIsSix() = %v, want true", got)
	}
}

func TestEnumStructVariantPositionalConstruction(t *testing.T) {
	jm := compileAndJIT(t, `
enum Shape {
	Triangle { base f64, height f64 }
}
func areaIsSix() bool {
	s := Shape.Triangle{4.0, 3.0}
	match s {
		Shape.Triangle{base: b, height: h} => {
			return 0.5 * b * h == 6.0
		}
	}
}
`)
	if got := jm.runBool(t, "areaIsSix"); !got {
		t.Errorf("areaIsSix() = %v, want true", got)
	}
}

// --- methods with an enum receiver, using match internally ---

func TestEnumMethodUsingMatchInternally(t *testing.T) {
	jm := compileAndJIT(t, `
enum Shape {
	Circle(f64),
	Rectangle(f64, f64),
	Point
}
func (Shape) Area() f64 {
	match this {
		Shape.Circle(r) => {
			return 3.0 * r * r
		}
		Shape.Rectangle(w, h) => {
			return w * h
		}
		Shape.Point => {
			return 0.0
		}
	}
}
func circleAreaIsTwelve() bool {
	c := Shape.Circle(2.0)
	return c.Area() == 12.0
}
func rectAreaIsTwelve() bool {
	r := Shape.Rectangle(3.0, 4.0)
	return r.Area() == 12.0
}
func pointAreaIsZero() bool {
	p := Shape.Point
	return p.Area() == 0.0
}
`)
	if got := jm.runBool(t, "circleAreaIsTwelve"); !got {
		t.Errorf("circleAreaIsTwelve() = %v, want true", got)
	}
	if got := jm.runBool(t, "rectAreaIsTwelve"); !got {
		t.Errorf("rectAreaIsTwelve() = %v, want true", got)
	}
	if got := jm.runBool(t, "pointAreaIsZero"); !got {
		t.Errorf("pointAreaIsZero() = %v, want true", got)
	}
}

// --- equality ---

const enumEqualitySrc = `
enum Shape {
	Circle(f64),
	Rectangle(f64, f64),
	Point
}
func sameVariantSameData() bool {
	a := Shape.Circle(2.0)
	b := Shape.Circle(2.0)
	return a == b
}
func sameVariantDifferentData() bool {
	a := Shape.Circle(2.0)
	b := Shape.Circle(9.0)
	return a == b
}
func differentVariant() bool {
	a := Shape.Circle(2.0)
	b := Shape.Point
	return a == b
}
func notEqualOperator() bool {
	a := Shape.Circle(2.0)
	b := Shape.Circle(9.0)
	return a != b
}
func unitVariantsEqual() bool {
	a := Shape.Point
	b := Shape.Point
	return a == b
}
`

func TestEnumEqualitySameVariantSameData(t *testing.T) {
	jm := compileAndJIT(t, enumEqualitySrc)
	if got := jm.runBool(t, "sameVariantSameData"); !got {
		t.Errorf("sameVariantSameData() = %v, want true", got)
	}
}

func TestEnumEqualitySameVariantDifferentData(t *testing.T) {
	jm := compileAndJIT(t, enumEqualitySrc)
	if got := jm.runBool(t, "sameVariantDifferentData"); got {
		t.Errorf("sameVariantDifferentData() = %v, want false", got)
	}
}

func TestEnumEqualityDifferentVariant(t *testing.T) {
	jm := compileAndJIT(t, enumEqualitySrc)
	if got := jm.runBool(t, "differentVariant"); got {
		t.Errorf("differentVariant() = %v, want false", got)
	}
}

func TestEnumInequalityOperator(t *testing.T) {
	jm := compileAndJIT(t, enumEqualitySrc)
	if got := jm.runBool(t, "notEqualOperator"); !got {
		t.Errorf("notEqualOperator() = %v, want true", got)
	}
}

func TestEnumEqualityUnitVariants(t *testing.T) {
	jm := compileAndJIT(t, enumEqualitySrc)
	if got := jm.runBool(t, "unitVariantsEqual"); !got {
		t.Errorf("unitVariantsEqual() = %v, want true", got)
	}
}

// --- print() ---

func TestEnumPrintUnitVariant(t *testing.T) {
	jm := compileAndJIT(t, `
enum Shape {
	Point
}
func main() {
	print(Shape.Point)
}
`)
	out := captureStdout(t, func() { jm.runInt32(t, "main") })
	if out != "Point\n" {
		t.Errorf("captured stdout = %q, want %q", out, "Point\n")
	}
}

func TestEnumPrintTupleVariant(t *testing.T) {
	jm := compileAndJIT(t, `
enum Shape {
	Circle(f64)
}
func main() {
	print(Shape.Circle(2.0))
}
`)
	out := captureStdout(t, func() { jm.runInt32(t, "main") })
	want := "Circle(2.000000)\n"
	if out != want {
		t.Errorf("captured stdout = %q, want %q", out, want)
	}
}

func TestEnumPrintMultiFieldTupleVariant(t *testing.T) {
	jm := compileAndJIT(t, `
enum Shape {
	Rectangle(f64, f64)
}
func main() {
	print(Shape.Rectangle(3.0, 4.0))
}
`)
	out := captureStdout(t, func() { jm.runInt32(t, "main") })
	want := "Rectangle(3.000000 4.000000)\n"
	if out != want {
		t.Errorf("captured stdout = %q, want %q", out, want)
	}
}

func TestEnumPrintStructVariant(t *testing.T) {
	jm := compileAndJIT(t, `
enum Shape {
	Triangle { base f64, height f64 }
}
func main() {
	print(Shape.Triangle{base: 3.0, height: 4.0})
}
`)
	out := captureStdout(t, func() { jm.runInt32(t, "main") })
	// A struct variant prints exactly like a real struct value already does
	// - braces, field values only, no names (see genPrintStructValue's own
	// established convention, reused verbatim by genPrintEnumVariant).
	want := "Triangle{3.000000 4.000000}\n"
	if out != want {
		t.Errorf("captured stdout = %q, want %q", out, want)
	}
}

// --- recursive / self-referential enum ---

func TestRecursiveEnumSumMultiElementList(t *testing.T) {
	jm := compileAndJIT(t, `
enum List {
	Cons(int, *List),
	Nil
}
func (List) Sum() int {
	match this {
		List.Cons(v, next) => {
			return v + next.Sum()
		}
		List.Nil => {
			return 0
		}
	}
}
func main() int {
	n3 := List.Nil
	n2 := List.Cons(3, &n3)
	n1 := List.Cons(2, &n2)
	n0 := List.Cons(1, &n1)
	return n0.Sum()
}
`)
	if got := jm.runInt32(t, "main"); got != 6 {
		t.Errorf("main() = %d, want 6 (1+2+3)", got)
	}
}

func TestRecursiveEnumSumSingleElementList(t *testing.T) {
	jm := compileAndJIT(t, `
enum List {
	Cons(int, *List),
	Nil
}
func (List) Sum() int {
	match this {
		List.Cons(v, next) => {
			return v + next.Sum()
		}
		List.Nil => {
			return 0
		}
	}
}
func main() int {
	n := List.Nil
	c := List.Cons(42, &n)
	return c.Sum()
}
`)
	if got := jm.runInt32(t, "main"); got != 42 {
		t.Errorf("main() = %d, want 42", got)
	}
}

// --- match with a wildcard arm ---

func TestMatchWildcardArmCoversRemainingVariants(t *testing.T) {
	jm := compileAndJIT(t, `
enum Shape {
	Circle(f64),
	Rectangle(f64, f64),
	Point
}
func classify(v int) int {
	var s Shape
	if v == 0 {
		s = Shape.Circle(1.0)
	}
	if v == 1 {
		s = Shape.Rectangle(1.0, 2.0)
	}
	if v == 2 {
		s = Shape.Point
	}
	match s {
		Shape.Circle(r) => {
			return 1
		}
		_ => {
			return 2
		}
	}
}
`)
	if got := jm.runInt32(t, "classify", 0); got != 1 {
		t.Errorf("classify(0) = %d, want 1", got)
	}
	if got := jm.runInt32(t, "classify", 1); got != 2 {
		t.Errorf("classify(1) = %d, want 2", got)
	}
	if got := jm.runInt32(t, "classify", 2); got != 2 {
		t.Errorf("classify(2) = %d, want 2", got)
	}
}

// TestBreakInsideMatchArmExitsEnclosingLoop covers a genuinely new
// interaction surface this round introduces: match is not itself a loop, so
// a `break`/`continue` inside one of its arms must still target the
// *enclosing* loop directly (see LANGUAGE.md's "match" section) - proven
// here by actually running the loop, not just type-checking it (see
// sema's own TestForWithBreakInsideMatchIsNotTerminating for the companion
// compile-time flow-analysis gap this same interaction surface exposed).
func TestBreakInsideMatchArmExitsEnclosingLoop(t *testing.T) {
	jm := compileAndJIT(t, `
enum Shape {
	Circle(f64),
	Point
}
func run() int {
	sum := 0
	for i := 0; i < 10; i++ {
		s := Shape.Circle(1.0)
		if i == 5 {
			s = Shape.Point
		}
		match s {
			Shape.Circle(r) => {
				sum = sum + 1
			}
			Shape.Point => {
				break
			}
		}
	}
	return sum
}
`)
	if got := jm.runInt32(t, "run"); got != 5 {
		t.Errorf("run() = %d, want 5 (loop runs for i=0..4 as Circle, then breaks at i=5 via the match arm)", got)
	}
}

func TestContinueInsideMatchArmContinuesEnclosingLoop(t *testing.T) {
	jm := compileAndJIT(t, `
enum Shape {
	Circle(f64),
	Point
}
func run() int {
	sum := 0
	for i := 0; i < 10; i++ {
		s := Shape.Circle(1.0)
		if i%2 == 0 {
			s = Shape.Point
		}
		match s {
			Shape.Circle(r) => {
				sum = sum + 1
			}
			Shape.Point => {
				continue
			}
		}
	}
	return sum
}
`)
	if got := jm.runInt32(t, "run"); got != 5 {
		t.Errorf("run() = %d, want 5 (only the 5 odd iterations reach Circle and increment sum)", got)
	}
}

// --- destructor firing at every control-flow exit shape ---

func TestEnumDestructorFiresAtNormalScopeEnd(t *testing.T) {
	jm := compileAndJIT(t, `
enum Resource {
	Owned(int),
	destructor() {
		calls = calls + 1
	}
}
var calls int = 0
func useIt() {
	r := Resource.Owned(1)
}
func afterUse() int {
	useIt()
	return calls
}
`)
	if got := jm.runInt32(t, "afterUse"); got != 1 {
		t.Errorf("afterUse() = %d, want 1", got)
	}
}

// enumDestructorEarlyReturnSrc is shared by
// TestEnumDestructorFiresOnEarlyReturn/
// TestEnumDestructorFiresOnFallThroughReturn - two separate JIT modules (each
// gets its own fresh `calls` global) so each test's single call is the only
// thing ever incrementing it, exactly mirroring destructor_test.go's own
// destructorEarlyReturnSrc/doc comment one type kind over: sharing one
// module's global state across two sequential calls would make the second
// call's own expected count depend on the first's, not a real bug in either
// path individually.
const enumDestructorEarlyReturnSrc = `
enum Resource {
	Owned(int),
	destructor() {
		calls = calls + 1
	}
}
var calls int = 0
func useIt(early bool) int {
	r := Resource.Owned(1)
	if early {
		return 1
	}
	return 2
}
func run(early bool) int {
	useIt(early)
	return calls
}
`

func TestEnumDestructorFiresOnEarlyReturn(t *testing.T) {
	jm := compileAndJIT(t, enumDestructorEarlyReturnSrc)
	if got := jm.runInt32(t, "run", 1); got != 1 {
		t.Errorf("run(true) = %d, want 1 (destructor must fire on the early return)", got)
	}
}

func TestEnumDestructorFiresOnFallThroughReturn(t *testing.T) {
	jm := compileAndJIT(t, enumDestructorEarlyReturnSrc)
	if got := jm.runInt32(t, "run", 0); got != 1 {
		t.Errorf("run(false) = %d, want 1 (the fall-through return path must fire its own destructor call too)", got)
	}
}

func TestEnumDestructorFiresOnBreak(t *testing.T) {
	jm := compileAndJIT(t, `
enum Resource {
	Owned(int),
	destructor() {
		calls = calls + 1
	}
}
var calls int = 0
func run() int {
	for i := 0; i < 5; i++ {
		r := Resource.Owned(i)
		if i == 2 {
			break
		}
	}
	return calls
}
`)
	if got := jm.runInt32(t, "run"); got != 3 {
		t.Errorf("run() = %d, want 3 (one destructor call per iteration through the break, i=0,1,2)", got)
	}
}

func TestEnumDestructorFiresOnContinue(t *testing.T) {
	jm := compileAndJIT(t, `
enum Resource {
	Owned(int),
	destructor() {
		calls = calls + 1
	}
}
var calls int = 0
func run() int {
	for i := 0; i < 5; i++ {
		r := Resource.Owned(i)
		if i == 2 {
			continue
		}
	}
	return calls
}
`)
	if got := jm.runInt32(t, "run"); got != 5 {
		t.Errorf("run() = %d, want 5 (one destructor call per iteration, continue included)", got)
	}
}

// TestEnumAsMapKey covers a gap the review pass for this round caught: sema
// already allowed an enum type as a map key (typeIsComparable's own TypeEnum
// case), but maps.go's own genMapKeyEqual/genHashInto initially had no case
// for it at all, which would have panicked at codegen time on a tree that
// already passed sema.Check (see maps.go's own doc comments on those two
// functions) - this proves the fix actually works end to end, not just that
// it compiles.
func TestEnumAsMapKey(t *testing.T) {
	jm := compileAndJIT(t, `
enum Shape {
	Circle(f64),
	Point
}
func run() int {
	m := make(map[Shape]int)
	m[Shape.Point] = 1
	m[Shape.Circle(2.0)] = 2
	m[Shape.Circle(9.0)] = 3
	a := m[Shape.Point]
	b := m[Shape.Circle(2.0)]
	c := m[Shape.Circle(9.0)]
	return a*100 + b*10 + c
}
`)
	if got := jm.runInt32(t, "run"); got != 123 {
		t.Errorf("run() = %d, want 123 (Point->1, Circle(2.0)->2, Circle(9.0)->3, distinguished by both discriminant and payload)", got)
	}
}

// matchArmDestructorLeakSrc is shared by
// TestMatchArmDestructorStackDoesNotLeakAcrossArms/
// TestMatchSecondArmDestructorStackDoesNotLeakFromFirst - a regression pair
// for the match-statement counterpart to genIfStmt's own preIf snapshot/
// restore fix (see CODEGEN.md's "genIfStmt's then/else save-restore"
// section, and genMatchStmt's own preMatch snapshot, enum.go): every arm
// here is an independent switch case, only one of which ever actually
// executes at runtime, but codegen generates every one of them sequentially
// against the same shared Generator.destructors slice - without restoring a
// real snapshot before each arm's own generation, an earlier arm's own
// `return` (which unwinds the *whole* destructor stack, truncating the
// shared slice) would leave a later arm's own return operating on an
// already-emptied stack, silently skipping the outer-scope `outer`'s own
// destructor call on that arm's own runtime path.
const matchArmDestructorLeakSrc = `
enum Shape {
	Circle(f64),
	Point
}
struct Resource {
	id int
	constructor(v int) {
		this.id = v
	}
	destructor() {
		calls = calls + 1
	}
}
var calls int = 0
func run(useCircle bool) int {
	outer := Resource(1)
	var s Shape
	if useCircle {
		s = Shape.Circle(1.0)
	}
	if !useCircle {
		s = Shape.Point
	}
	match s {
		Shape.Circle(r) => {
			return 1
		}
		Shape.Point => {
			return 2
		}
	}
}
func afterCircle() int {
	run(true)
	return calls
}
func afterPoint() int {
	run(false)
	return calls
}
`

// TestMatchArmDestructorStackDoesNotLeakAcrossArms covers the FIRST arm
// (Circle, declared first in source and so generated first) - the one
// direction that already worked even before the preMatch fix, since nothing
// has mutated the shared stack yet by the time it generates.
func TestMatchArmDestructorStackDoesNotLeakAcrossArms(t *testing.T) {
	jm := compileAndJIT(t, matchArmDestructorLeakSrc)
	if got := jm.runInt32(t, "afterCircle"); got != 1 {
		t.Errorf("afterCircle() = %d, want 1 (outer's destructor must still fire once)", got)
	}
}

// TestMatchSecondArmDestructorStackDoesNotLeakFromFirst covers the SECOND
// arm (Point) - the direction that actually exposes the bug: without the
// preMatch snapshot/restore, the first arm's own `return` already truncated
// Generator.destructors to empty by the time this arm's own codegen runs,
// silently skipping outer's destructor call on this arm's own path.
func TestMatchSecondArmDestructorStackDoesNotLeakFromFirst(t *testing.T) {
	jm := compileAndJIT(t, matchArmDestructorLeakSrc)
	if got := jm.runInt32(t, "afterPoint"); got != 1 {
		t.Errorf("afterPoint() = %d, want 1 (outer's destructor must fire on the SECOND arm's own path too, not just the first)", got)
	}
}

func TestEnumWithDestructorRejectsCopy(t *testing.T) {
	// Real compile-time rejection, not a runtime concern - see the dedicated
	// sema test (TestEnumWithDestructorIsNonCopyable); this codegen-side test
	// just confirms a *copyable* enum (no destructor) still works completely
	// normally end to end, as a sanity check alongside the non-copyable
	// tests above.
	jm := compileAndJIT(t, `
enum Shape {
	Circle(f64),
	Point
}
func main() int {
	a := Shape.Circle(1.0)
	b := a
	match b {
		Shape.Circle(r) => {
			return 1
		}
		Shape.Point => {
			return 0
		}
	}
}
`)
	if got := jm.runInt32(t, "main"); got != 1 {
		t.Errorf("main() = %d, want 1", got)
	}
}

// --- value-match (plain int/bool/string patterns - genValueMatchStmt) ---

// TestValueMatchIntMultiPatternArm covers an int subject with a multi-value
// arm (`case 2, 3, 4:`-equivalent) alongside single-value arms and the
// mandatory wildcard - proving genValueMatchStmt's own short-circuit
// comparison chain picks the right arm for a value that matches the SECOND
// pattern in a multi-pattern arm specifically (not just the first), and
// falls through to the wildcard for anything uncovered.
func TestValueMatchIntMultiPatternArm(t *testing.T) {
	jm := compileAndJIT(t, `
func classify(x int) int {
	match x {
		1 => {
			return 10
		}
		2, 3, 4 => {
			return 20
		}
		_ => {
			return 99
		}
	}
}
`)
	tests := []struct {
		in, want int32
	}{
		{1, 10},
		{2, 20},
		{3, 20},
		{4, 20},
		{5, 99},
	}
	for _, tc := range tests {
		if got := jm.runInt32(t, "classify", tc.in); got != tc.want {
			t.Errorf("classify(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// TestValueMatchStringSubject covers a string subject - proving
// genValueMatchStmt reuses genValueEqual's own string-equality codegen
// (genStringEqual) rather than a raw pointer/discriminant comparison.
func TestValueMatchStringSubject(t *testing.T) {
	jm := compileAndJIT(t, `
func code(s string) int {
	match s {
		"a" => {
			return 1
		}
		"b", "c" => {
			return 2
		}
		_ => {
			return 0
		}
	}
}
func isA() bool {
	return code("a") == 1
}
func isBOrC() bool {
	return code("b") == 2 && code("c") == 2
}
func isOther() bool {
	return code("z") == 0
}
`)
	if got := jm.runBool(t, "isA"); !got {
		t.Errorf("isA() = %v, want true", got)
	}
	if got := jm.runBool(t, "isBOrC"); !got {
		t.Errorf("isBOrC() = %v, want true", got)
	}
	if got := jm.runBool(t, "isOther"); !got {
		t.Errorf("isOther() = %v, want true", got)
	}
}

// TestValueMatchBoolSubject covers a bool subject, and that a variable
// reference (not just a bare literal) is a legal value pattern - resolved as
// an ordinary value expression (see resolve.go's resolvePattern).
func TestValueMatchBoolSubject(t *testing.T) {
	jm := compileAndJIT(t, `
func describe(b bool) int {
	yes := true
	match b {
		yes => {
			return 1
		}
		_ => {
			return 0
		}
	}
}
func trueIsOne() bool {
	return describe(true) == 1
}
func falseIsZero() bool {
	return describe(false) == 0
}
`)
	if got := jm.runBool(t, "trueIsOne"); !got {
		t.Errorf("trueIsOne() = %v, want true", got)
	}
	if got := jm.runBool(t, "falseIsZero"); !got {
		t.Errorf("falseIsZero() = %v, want true", got)
	}
}

// TestValueMatchWildcardOnlyIsFine covers the degenerate case (no
// non-wildcard arms at all) - genValueMatchStmt's own valueArms loop never
// runs, so the wildcard's body must still generate correctly straight off
// the subject's own evaluation point.
func TestValueMatchWildcardOnlyIsFine(t *testing.T) {
	jm := compileAndJIT(t, `
func always42(x int) int {
	match x {
		_ => {
			return 42
		}
	}
}
`)
	if got := jm.runInt32(t, "always42", 7); got != 42 {
		t.Errorf("always42(7) = %d, want 42", got)
	}
}

// valueMatchDestructorLeakSrc mirrors matchArmDestructorLeakSrc one lowering
// strategy over - genValueMatchStmt applies the identical preMatch
// destructor snapshot/restore discipline genMatchStmt's own enum path
// already uses, at every arm. Split into two separate tests below (rather
// than two calls inside one), exactly like matchArmDestructorLeakSrc's own
// two tests are: `calls` is a real global, persisting across every call made
// against the same compileAndJIT module, so each direction needs its own
// fresh module/state to assert against, not a second call layered onto the
// same one.
const valueMatchDestructorLeakSrc = `
struct Resource {
	id int
	constructor(v int) {
		this.id = v
	}
	destructor() {
		calls = calls + 1
	}
}
var calls int = 0
func run(x int) int {
	outer := Resource(1)
	match x {
		1 => {
			return 1
		}
		_ => {
			return 2
		}
	}
}
func afterFirst() int {
	run(1)
	return calls
}
func afterWildcard() int {
	run(9)
	return calls
}
`

// TestValueMatchFirstArmDestructorStackDoesNotLeak covers the first
// (non-wildcard) arm's own path.
func TestValueMatchFirstArmDestructorStackDoesNotLeak(t *testing.T) {
	jm := compileAndJIT(t, valueMatchDestructorLeakSrc)
	if got := jm.runInt32(t, "afterFirst"); got != 1 {
		t.Errorf("afterFirst() = %d, want 1 (outer's destructor must fire on the first arm's own path)", got)
	}
}

// TestValueMatchWildcardArmDestructorStackDoesNotLeak covers the wildcard's
// own final-fallback path - the direction that actually exercises
// genValueMatchStmt's own preMatch restore right before generating the
// wildcard body, mirroring TestMatchSecondArmDestructorStackDoesNotLeakFromFirst's
// identical reasoning one lowering strategy over.
func TestValueMatchWildcardArmDestructorStackDoesNotLeak(t *testing.T) {
	jm := compileAndJIT(t, valueMatchDestructorLeakSrc)
	if got := jm.runInt32(t, "afterWildcard"); got != 1 {
		t.Errorf("afterWildcard() = %d, want 1 (outer's destructor must fire on the wildcard's own path too)", got)
	}
}

// TestMethodCallOnFreshUnitVariantReceiver covers a method called directly
// on a bare unit-variant construction (`E.A.m()`, receiver never stored in a
// variable) - genAddr's MemberExpr case used to assume every MemberExpr was
// a struct-field chain and recursed into "E" (the enum type name) as if it
// were a further receiver, panicking with "identifier E has no storage".
func TestMethodCallOnFreshUnitVariantReceiver(t *testing.T) {
	jm := compileAndJIT(t, `
enum E {
	A,
}
func (E) m() int {
	return 1
}
func f() int {
	return E.A.m()
}
`)

	if got := jm.runInt32(t, "f"); got != 1 {
		t.Errorf("f() = %d, want 1", got)
	}
}
