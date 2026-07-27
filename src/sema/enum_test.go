package sema

import (
	"testing"

	"llvm_lang/src/ast"
	"llvm_lang/src/lexer"
	"llvm_lang/src/parser"
)

// allErrorsSrc parses, resolves, and checks src, returning the combined
// error count across Resolve and Check - some enum/match diagnostics land
// during Resolve (an undefined variant name, a malformed wildcard pattern -
// see resolve.go's resolveEnumVariantRef/resolvePattern), others only once
// Check has type information available (exhaustiveness, a duplicate-variant
// arm, a pattern naming the wrong enum's own variant - see typecheck.go's
// checkMatchStmt/checkMatchArmPattern) - a test asserting "this diagnostic
// exists somewhere in the pipeline" shouldn't need to know or care which of
// the two phases actually raised it.
func allErrorsSrc(t *testing.T, src string) (*ast.Tree, int) {
	t.Helper()
	tree, pdiags := parser.ParseFile(lexer.NewFile("t.ll", src), false)
	if pdiags.HasErrors() {
		t.Fatalf("unexpected parse errors for %q: %v", src, pdiags.All())
	}
	info, rdiags := Resolve(tree)
	cdiags := Check(tree, info)
	return tree, rdiags.ErrorCount() + cdiags.ErrorCount()
}

func expectAllErrors(t *testing.T, src string, want int) {
	t.Helper()
	_, got := allErrorsSrc(t, src)
	if got != want {
		t.Fatalf("total error count = %d, want %d", got, want)
	}
}

// --- declaration/catalog shape ---

func TestEnumDeclUnitTupleStructVariants(t *testing.T) {
	_, info := checkSrc(t, `
enum Shape {
	Point,
	Circle(f64),
	Triangle { base f64, height f64 }
}
func main() {}
`)
	shape := info.Enums["Shape"]
	if shape == nil {
		t.Fatal("Shape enum not catalogued")
	}
	if len(shape.Order) != 3 {
		t.Fatalf("len(Order) = %d, want 3", len(shape.Order))
	}
	point := shape.Variants["Point"]
	if point == nil || point.Kind != EnumVariantUnit || point.Index != 0 {
		t.Fatalf("Point variant wrong: %+v", point)
	}
	circle := shape.Variants["Circle"]
	if circle == nil || circle.Kind != EnumVariantTuple || circle.Index != 1 {
		t.Fatalf("Circle variant wrong: %+v", circle)
	}
	if len(circle.Tuple) != 1 || circle.Tuple[0].Kind != TypeF64 {
		t.Fatalf("Circle.Tuple = %+v, want [f64]", circle.Tuple)
	}
	tri := shape.Variants["Triangle"]
	if tri == nil || tri.Kind != EnumVariantStruct || tri.Index != 2 {
		t.Fatalf("Triangle variant wrong: %+v", tri)
	}
	if len(tri.Fields) != 2 || tri.Fields[0].Name != "base" || tri.Fields[1].Name != "height" {
		t.Fatalf("Triangle.Fields = %+v", tri.Fields)
	}
}

func TestEnumDuplicateVariantNameIsError(t *testing.T) {
	expectAllErrors(t, `
enum Shape {
	Point,
	Point
}
func main() {}
`, 1)
}

// A zero-variant enum can never have a real value - even its own zero
// value has no legitimate variant to zero-init into - so every codegen
// path that switches on an enum's own discriminant would build an
// unreachable-only switch with no real case. Rejected outright rather than
// hardened case-by-case in codegen.
func TestEnumZeroVariantsIsError(t *testing.T) {
	expectAllErrors(t, `
enum Empty {}
func main() {}
`, 1)
}

// --- construction ---

func TestEnumUnitVariantConstructionType(t *testing.T) {
	_, info := checkSrc(t, `
enum Shape {
	Point,
	Circle(f64)
}
func main() {
	p := Shape.Point
}
`)
	shape := info.Enums["Shape"]
	found := false
	for n, t2 := range info.Types {
		if t2.Kind == TypeEnum && t2.Enum == shape {
			found = true
			_ = n
		}
	}
	if !found {
		t.Fatal("no expression typed as Shape found")
	}
}

func TestEnumUnitVariantReferencedBareIsFine(t *testing.T) {
	checkSrc(t, `
enum Shape {
	Point
}
func main() {
	p := Shape.Point
}
`)
}

func TestEnumTupleVariantBareReferenceIsError(t *testing.T) {
	expectCheckErrors(t, `
enum Shape {
	Circle(f64)
}
func main() {
	p := Shape.Circle
}
`, 1)
}

func TestEnumTupleVariantCallWrongArgCountIsError(t *testing.T) {
	expectCheckErrors(t, `
enum Shape {
	Circle(f64)
}
func main() {
	c := Shape.Circle(1.0, 2.0)
}
`, 1)
}

func TestEnumTupleVariantCallWrongArgTypeIsError(t *testing.T) {
	expectCheckErrors(t, `
enum Shape {
	Circle(f64)
}
func main() {
	c := Shape.Circle("nope")
}
`, 1)
}

func TestEnumStructVariantConstructionKeyed(t *testing.T) {
	checkSrc(t, `
enum Shape {
	Triangle { base f64, height f64 }
}
func main() {
	tr := Shape.Triangle{base: 1.0, height: 2.0}
}
`)
}

func TestEnumStructVariantConstructionPositional(t *testing.T) {
	checkSrc(t, `
enum Shape {
	Triangle { base f64, height f64 }
}
func main() {
	tr := Shape.Triangle{1.0, 2.0}
}
`)
}

func TestEnumStructVariantUnknownFieldIsError(t *testing.T) {
	expectCheckErrors(t, `
enum Shape {
	Triangle { base f64, height f64 }
}
func main() {
	tr := Shape.Triangle{base: 1.0, nope: 2.0}
}
`, 1)
}

// --- methods ---

func TestEnumMethodDeclarationAndCall(t *testing.T) {
	checkSrc(t, `
enum Shape {
	Circle(f64),
	Point
}
func (Shape) Area() f64 {
	match this {
		Shape.Circle(r) => {
			return r * r
		}
		Shape.Point => {
			return 0.0
		}
	}
}
func main() {
	c := Shape.Circle(2.0)
	a := c.Area()
}
`)
}

func TestMethodOnUndeclaredReceiverIsError(t *testing.T) {
	expectAllErrors(t, `
func (NotDeclared) Foo() {}
func main() {}
`, 1)
}

// --- exhaustiveness / match validation ---

func TestMatchExhaustiveNoWildcardIsFine(t *testing.T) {
	checkSrc(t, `
enum Shape {
	Circle(f64),
	Point
}
func main() {
	s := Shape.Point
	match s {
		Shape.Circle(r) => {
		}
		Shape.Point => {
		}
	}
}
`)
}

func TestMatchMissingVariantNoWildcardIsError(t *testing.T) {
	expectCheckErrors(t, `
enum Shape {
	Circle(f64),
	Point
}
func main() {
	s := Shape.Point
	match s {
		Shape.Circle(r) => {
		}
	}
}
`, 1)
}

func TestMatchMissingVariantWithWildcardIsFine(t *testing.T) {
	checkSrc(t, `
enum Shape {
	Circle(f64),
	Point
}
func main() {
	s := Shape.Point
	match s {
		Shape.Circle(r) => {
		}
		_ => {
		}
	}
}
`)
}

func TestMatchDuplicateVariantArmIsError(t *testing.T) {
	expectCheckErrors(t, `
enum Shape {
	Circle(f64),
	Point
}
func main() {
	s := Shape.Point
	match s {
		Shape.Circle(r) => {
		}
		Shape.Circle(r2) => {
		}
		Shape.Point => {
		}
	}
}
`, 1)
}

func TestMatchWrongEnumVariantPatternIsError(t *testing.T) {
	expectCheckErrors(t, `
enum Shape {
	Circle(f64),
	Point
}
enum Color {
	Red,
	Blue
}
func main() {
	s := Shape.Point
	match s {
		Shape.Circle(r) => {
		}
		Color.Red => {
		}
		_ => {
		}
	}
}
`, 1)
}

func TestMatchUndefinedVariantNameIsError(t *testing.T) {
	expectAllErrors(t, `
enum Shape {
	Circle(f64),
	Point
}
func main() {
	s := Shape.Point
	match s {
		Shape.Circle(r) => {
		}
		Shape.Nonexistent => {
		}
		_ => {
		}
	}
}
`, 1)
}

// TestMatchOnNonEnumIsError used to assert that matching on a plain int was
// an error outright - before this round, match was scoped to enum values
// only (see DECISIONS.md's original "why match is scoped to enum-variant
// patterns only" entry). Now that a value-match (int/bool/string - see
// LANGUAGE.md's "match" section's plain-value-pattern extension) is a real,
// legal feature, matching on an int is no longer inherently an error - only
// a genuinely unsupported subject type (f64, and every aggregate/reference
// type - see sema.isValueMatchType) still is. See
// TestMatchOnUnsupportedValueTypeIsError below for that regression instead,
// and TestMatchOnIntWithWildcardIsFine/TestMatchOnIntMissingWildcardIsError
// in the value-match section further down for this exact case's own new,
// intentionally different behavior.
func TestMatchOnUnsupportedValueTypeIsError(t *testing.T) {
	expectCheckErrors(t, `
func main() {
	x := 5.5
	match x {
		_ => {
		}
	}
}
`, 1)
}

func TestMatchStructVariantBindingsTypeCorrectly(t *testing.T) {
	checkSrc(t, `
enum Shape {
	Triangle { base f64, height f64 }
}
func area() f64 {
	t := Shape.Triangle{base: 3.0, height: 4.0}
	match t {
		Shape.Triangle{base: b, height: h} => {
			return b * h
		}
	}
}
func main() {}
`)
}

func TestMatchAsLastStatementSatisfiesMissingReturn(t *testing.T) {
	// A fully exhaustive match, every arm ending in return, must satisfy the
	// "missing return" flow analysis with no further return needed after it
	// (see LANGUAGE.md's "Missing return" section and isTerminatingStmt's own
	// MatchStmt case).
	checkSrc(t, `
enum Shape {
	Circle(f64),
	Point
}
func (Shape) Area() f64 {
	match this {
		Shape.Circle(r) => {
			return r * r
		}
		Shape.Point => {
			return 0.0
		}
	}
}
func main() {}
`)
}

func TestMatchMissingReturnWhenNotExhaustive(t *testing.T) {
	expectCheckErrors(t, `
enum Shape {
	Circle(f64),
	Point
}
func (Shape) Area() f64 {
	match this {
		Shape.Circle(r) => {
			return r * r
		}
	}
}
func main() {}
`, 2) // one "missing return" plus one "not exhaustive"
}

// --- comparability / printability ---

func TestEnumEqualityWorksForSameEnumType(t *testing.T) {
	checkSrc(t, `
enum Shape {
	Circle(f64),
	Point
}
func main() {
	a := Shape.Point
	b := Shape.Point
	eq := a == b
}
`)
}

func TestEnumEqualityAcrossDifferentEnumTypesIsError(t *testing.T) {
	expectCheckErrors(t, `
enum Shape {
	Point
}
enum Color {
	Red
}
func main() {
	a := Shape.Point
	b := Color.Red
	eq := a == b
}
`, 1)
}

// TestEnumWithFuncFieldEqualityRejected mirrors
// TestStructWithFuncFieldEqualityRejected one type kind over - a func-typed
// variant field (anywhere nested inside any variant, not just the one
// actually constructed on either side) makes the whole enum type
// uncomparable, exactly like a struct's own identical rule (see
// typeIsComparable's TypeEnum case).
func TestEnumWithFuncFieldEqualityRejected(t *testing.T) {
	expectCheckErrors(t, `
func addOne(x int) int {
	return x + 1
}
enum Holder {
	Wrap(func(int) int)
}
func main() {
	a := Holder.Wrap(addOne)
	b := Holder.Wrap(addOne)
	eq := a == b
}
`, 1)
}

func TestEnumPrintableAllowsBasicVariants(t *testing.T) {
	checkSrc(t, `
enum Shape {
	Circle(f64),
	Point
}
func main() {
	s := Shape.Circle(1.0)
	print(s)
}
`)
}

func TestEnumWithMapFieldNotPrintable(t *testing.T) {
	expectCheckErrors(t, `
enum Holder {
	Wrap(map[string]int)
}
func main() {
	m := make(map[string]int)
	h := Holder.Wrap(m)
	print(h)
}
`, 1)
}

// --- non-copyable propagation ---

func TestEnumWithDestructorIsNonCopyable(t *testing.T) {
	expectCheckErrors(t, `
enum Resource {
	Owned(int)
	destructor() {
	}
}
func main() {
	a := Resource.Owned(1)
	b := a
}
`, 1)
}

func TestEnumWithNonCopyableVariantFieldIsNonCopyable(t *testing.T) {
	expectCheckErrors(t, `
struct Handle {
	id int
	destructor() {
	}
}
enum Wrapper {
	Wrap(Handle)
}
func main() {
	w := Wrapper.Wrap(Handle{1})
	w2 := w
}
`, 1)
}

func TestEnumWithoutNonCopyableFieldsIsCopyable(t *testing.T) {
	checkSrc(t, `
enum Shape {
	Circle(f64),
	Point
}
func main() {
	a := Shape.Circle(1.0)
	b := a
}
`)
}

// --- recursive/self-referential variants ---

// TestForWithBreakInsideMatchIsNotTerminating is a regression test for a real
// gap the dedicated review pass for this round caught: forHasOwnBreak
// (typecheck.go) originally had no MatchStmt case at all, so a `break`
// reachable only through a match arm's own body was invisible to it - an
// infinite `for {}` containing nothing but a match-with-a-break would then
// be wrongly treated as always-terminating (isTerminatingStmt), missing a
// real "missing return" diagnostic for whatever followed it, and risking a
// genuine `unreachable` trap at runtime on an otherwise valid program (see
// forHasOwnBreak's own updated doc comment).
func TestForWithBreakInsideMatchIsNotTerminating(t *testing.T) {
	expectCheckErrors(t, `
enum Shape {
	Point,
	Other
}
func f(s Shape) int {
	for {
		match s {
			Shape.Point => {
				break
			}
			Shape.Other => {
				return 1
			}
		}
	}
}
func main() {}
`, 1) // missing return after the loop
}

func TestRecursiveEnumViaPointerTypeChecks(t *testing.T) {
	checkSrc(t, `
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
func main() {
	n := List.Nil
	c := List.Cons(1, &n)
}
`)
}

// --- value-match (plain int/bool/string patterns - see LANGUAGE.md's
// "match" section's plain-value-pattern extension) ---

func TestValueMatchIntWithWildcardIsFine(t *testing.T) {
	checkSrc(t, `
func main() {
	x := 5
	match x {
		1 => {
		}
		2, 3 => {
		}
		_ => {
		}
	}
}
`)
}

func TestValueMatchIntMissingWildcardIsError(t *testing.T) {
	expectCheckErrors(t, `
func main() {
	x := 5
	match x {
		1 => {
		}
		2 => {
		}
	}
}
`, 1)
}

func TestValueMatchStringWithWildcardIsFine(t *testing.T) {
	checkSrc(t, `
func main() {
	s := "b"
	match s {
		"a" => {
		}
		"b", "c" => {
		}
		_ => {
		}
	}
}
`)
}

func TestValueMatchBoolWithWildcardIsFine(t *testing.T) {
	checkSrc(t, `
func main() {
	b := true
	match b {
		true => {
		}
		_ => {
		}
	}
}
`)
}

// TestValueMatchPatternWrongTypeIsError proves each arm's own pattern is
// checked for equality-comparability against the subject's own type, exactly
// like an ordinary == operand pair (checkEqualityOperands) - a string
// pattern against an int subject is a clean diagnostic, not silently
// accepted or a panic.
func TestValueMatchPatternWrongTypeIsError(t *testing.T) {
	expectCheckErrors(t, `
func main() {
	x := 5
	match x {
		"nope" => {
		}
		_ => {
		}
	}
}
`, 1)
}

// TestValueMatchVariablePatternIsFine proves a plain identifier referencing
// an already-declared variable/constant is a legal value pattern too, not
// just a bare literal - resolved as an ordinary value-expression reference
// (see resolve.go's resolvePattern), the same way Go's own switch accepts
// `case someVar:`.
func TestValueMatchVariablePatternIsFine(t *testing.T) {
	checkSrc(t, `
func main() {
	target := 3
	x := 5
	match x {
		target => {
		}
		_ => {
		}
	}
}
`)
}

func TestValueMatchDuplicateLiteralArmIsError(t *testing.T) {
	expectCheckErrors(t, `
func main() {
	x := 5
	match x {
		1 => {
		}
		1 => {
		}
		_ => {
		}
	}
}
`, 1)
}

// TestValueMatchDuplicateLiteralAcrossMultiPatternArmIsError proves the
// duplicate-literal check also looks across a single arm's own several
// comma-separated patterns, not just across different arms.
func TestValueMatchDuplicateLiteralAcrossMultiPatternArmIsError(t *testing.T) {
	expectCheckErrors(t, `
func main() {
	x := 5
	match x {
		1, 2, 1 => {
		}
		_ => {
		}
	}
}
`, 1)
}

// TestValueMatchOnFloatIsError proves f32/f64 stay deliberately excluded
// from value-match subjects (float equality is a footgun this language
// already avoids leaning into elsewhere - see DECISIONS.md).
func TestValueMatchOnFloatIsError(t *testing.T) {
	expectCheckErrors(t, `
func main() {
	x := 1.5
	match x {
		_ => {
		}
	}
}
`, 1)
}

// TestValueMatchOnStructIsError proves a struct subject is rejected too,
// same as a float - only scalar leaf types make sense to switch on.
func TestValueMatchOnStructIsError(t *testing.T) {
	expectCheckErrors(t, `
struct Point {
	x int
}
func main() {
	p := Point{1}
	match p {
		_ => {
		}
	}
}
`, 1)
}

// TestEnumMatchArmWithMultiplePatternsIsError proves an enum-match arm stays
// restricted to exactly one variant pattern this round - binding several
// differently-shaped variant patterns into one shared arm body is a real,
// separate, deliberately deferred feature (see DECISIONS.md).
func TestEnumMatchArmWithMultiplePatternsIsError(t *testing.T) {
	expectCheckErrors(t, `
enum Shape {
	Circle(f64),
	Point
}
func main() {
	s := Shape.Point
	match s {
		Shape.Circle(r), Shape.Point => {
		}
		_ => {
		}
	}
}
`, 1)
}

// TestValueMatchAsLastStatementSatisfiesMissingReturn mirrors
// TestMatchAsLastStatementSatisfiesMissingReturn one construct over - a
// value-match, since sema.Check already guarantees it carries a wildcard
// arm, terminates iff every arm's own body does (see matchStmtTerminates'
// own updated doc comment).
func TestValueMatchAsLastStatementSatisfiesMissingReturn(t *testing.T) {
	checkSrc(t, `
func classify(x int) int {
	match x {
		1, 2, 3 => {
			return 1
		}
		_ => {
			return 0
		}
	}
}
func main() {}
`)
}

func TestValueMatchMissingReturnWhenNotEveryArmReturns(t *testing.T) {
	expectCheckErrors(t, `
func classify(x int) int {
	match x {
		1 => {
			return 1
		}
		_ => {
		}
	}
}
func main() {}
`, 1) // missing return: the wildcard arm doesn't itself return
}
