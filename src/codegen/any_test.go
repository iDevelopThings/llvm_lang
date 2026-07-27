package codegen

import "testing"

// --- boxing + AnyAs round trip ---

// TestAnyBoxAndAsRoundTripPrimitive JIT-executes a full box -> AnyAs round
// trip for a primitive, proving the real numeric value survives (not merely
// that it compiles).
func TestAnyBoxAndAsRoundTripPrimitive(t *testing.T) {
	jm := compileAndJIT(t, `
func roundtrip() int {
	a := Any(42)
	v, ok := AnyAs[int](a)
	if !ok {
		return -1
	}
	return v
}
`)
	if got := jm.runInt32(t, "roundtrip"); got != 42 {
		t.Errorf("roundtrip() = %d, want 42", got)
	}
}

// TestAnyAsMismatchedKindReturnsFalseNotCrash proves a mismatched AnyAs[T]
// returns a real, well-defined (zero value, false) - never a crash or a
// garbage value read out of bounds (see genAnyAsCall's own doc comment: the
// mismatched branch never loads from the boxed data at all).
func TestAnyAsMismatchedKindReturnsFalseNotCrash(t *testing.T) {
	jm := compileAndJIT(t, `
func mismatchOk() bool {
	a := Any(42)
	_, ok := AnyAs[f64](a)
	return ok
}
func mismatchZero() int {
	a := Any(true)
	v, _ := AnyAs[int](a)
	return v
}
`)
	if got := jm.runBool(t, "mismatchOk"); got {
		t.Errorf("mismatchOk() = %v, want false", got)
	}
	if got := jm.runInt32(t, "mismatchZero"); got != 0 {
		t.Errorf("mismatchZero() = %d, want 0 (zero value on mismatch)", got)
	}
}

func TestAnyIntoAnyReboxRoundTrip(t *testing.T) {
	jm := compileAndJIT(t, `
func f() int {
	a := Any(7)
	b := Any(a)
	v, ok := AnyAs[int](b)
	if !ok {
		return -1
	}
	return v
}
`)
	if got := jm.runInt32(t, "f"); got != 7 {
		t.Errorf("f() = %d, want 7", got)
	}
}

func TestAnyBoxPointerRoundTrip(t *testing.T) {
	jm := compileAndJIT(t, `
func f() int {
	x := 99
	p := &x
	a := Any(p)
	v, ok := AnyAs[*int](a)
	if !ok {
		return -1
	}
	return *v
}
`)
	if got := jm.runInt32(t, "f"); got != 99 {
		t.Errorf("f() = %d, want 99", got)
	}
}

// --- map boxing (metadata-only - see LANGUAGE.md's "Any" section) ---

// TestAnyBoxMapRoundTrip JIT-executes box -> AnyAs for a map, proving the
// real map value (its own live table, not a copy) survives the round trip -
// inserting through the round-tripped handle is visible through the
// original variable, matching a map's own reference-type semantics.
func TestAnyBoxMapRoundTrip(t *testing.T) {
	jm := compileAndJIT(t, `
func f() int {
	m := make(map[string]int)
	m["a"] = 1
	a := Any(m)
	v, ok := AnyAs[map[string]int](a)
	if !ok {
		return -1
	}
	v["b"] = 2
	return m["a"] + m["b"]
}
`)
	if got := jm.runInt32(t, "f"); got != 3 {
		t.Errorf("f() = %d, want 3", got)
	}
}

// TestAnyBoxMapKindOnlyMatchIsAKnownImprecision documents, rather than
// merely asserting, that AnyAs[T] for T a map type only ever checks
// sema.TypeKind - every map shares the one interned TypeMap descriptor
// (unlike a struct/array, which also compares descriptor pointer identity -
// see genAnyAsCall) - so a boxed map[string]int spuriously reports ok=true
// against AnyAs[map[int]bool] too. Accepted, documented gap for this round
// (see DECISIONS.md), not a regression to fix here.
func TestAnyBoxMapKindOnlyMatchIsAKnownImprecision(t *testing.T) {
	jm := compileAndJIT(t, `
func f() bool {
	m := make(map[string]int)
	a := Any(m)
	_, ok := AnyAs[map[int]bool](a)
	return ok
}
`)
	if got := jm.runBool(t, "f"); !got {
		t.Errorf("f() = %v, want true (documented kind-only imprecision for maps)", got)
	}
}

// TestAnyBoxNilMapRoundTrip proves boxing is genuinely value-agnostic - a
// nil (never-`make`'d) map's control-block pointer is boxed exactly like any
// other map's, since genAnyBox never inspects what a map value points to.
func TestAnyBoxNilMapRoundTrip(t *testing.T) {
	jm := compileAndJIT(t, `
func f() int {
	var m map[string]int
	a := Any(m)
	v, ok := AnyAs[map[string]int](a)
	if !ok {
		return -1
	}
	return len(v)
}
`)
	if got := jm.runInt32(t, "f"); got != 0 {
		t.Errorf("f() = %d, want 0", got)
	}
}

// TestAnyFieldsOnBoxedMapYieldsZeroIterations proves AnyFields' existing
// non-struct precedent (a boxed int already yields zero fields) also holds
// for a boxed map - its descriptor has fieldCount 0/fieldsPtr null exactly
// like every other non-struct kind.
func TestAnyFieldsOnBoxedMapYieldsZeroIterations(t *testing.T) {
	jm := compileAndJIT(t, `
func f() int {
	m := make(map[string]int)
	m["a"] = 1
	a := Any(m)
	count := 0
	for name, v := range AnyFields(a) {
		count = count + 1
	}
	return count
}
`)
	if got := jm.runInt32(t, "f"); got != 0 {
		t.Errorf("f() = %d, want 0", got)
	}
}

// TestAnyBoxStructWithMapFieldRoundTrip mirrors
// TestAnyFieldsStructWithArrayFieldReflectable for a map field - walked via
// AnyFields, then round-tripped back to a real map through AnyAs.
func TestAnyBoxStructWithMapFieldRoundTrip(t *testing.T) {
	jm := compileAndJIT(t, `
struct Bag {
	Items map[string]int
}
func f() int {
	b := Bag{Items: make(map[string]int)}
	b.Items["x"] = 5
	a := Any(b)
	sum := 0
	for name, v := range AnyFields(a) {
		if name == "Items" {
			mv, ok := AnyAs[map[string]int](v)
			if ok {
				sum = sum + mv["x"]
			}
		}
	}
	return sum
}
`)
	if got := jm.runInt32(t, "f"); got != 5 {
		t.Errorf("f() = %d, want 5", got)
	}
}

// TestAnyBoxArrayOfStructsWithMapFieldThreeLevelNesting proves the
// recursive boxability composes through three levels (array -> struct ->
// map), not just the two levels every other Any test exercises - each level
// goes through the same isNestedBoxableIntoAny/typeDescriptorFor recursion,
// so there's nothing depth-specific to special-case, but this is the first
// test to actually prove it rather than assume it.
func TestAnyBoxArrayOfStructsWithMapFieldThreeLevelNesting(t *testing.T) {
	jm := compileAndJIT(t, `
struct Bag {
	Items map[string]int
}
func f() int {
	b0 := Bag{Items: make(map[string]int)}
	b0.Items["x"] = 5
	b1 := Bag{Items: make(map[string]int)}
	b1.Items["x"] = 7
	bags := [2]Bag{b0, b1}
	a := Any(bags)
	e, ok := AnyIndex(a, 1)
	if !ok {
		return -1
	}
	sum := 0
	for name, v := range AnyFields(e) {
		if name == "Items" {
			mv, mok := AnyAs[map[string]int](v)
			if mok {
				sum = sum + mv["x"]
			}
		}
	}
	return sum
}
`)
	if got := jm.runInt32(t, "f"); got != 7 {
		t.Errorf("f() = %d, want 7", got)
	}
}

// --- AnyKind/AnyName ---

// TestAnyKindConsistentAcrossSameKind proves AnyKind is a stable, kind-
// keyed value (same for two different int values, different across kinds)
// without hardcoding sema's own TypeKind wire values into this test.
func TestAnyKindConsistentAcrossSameKind(t *testing.T) {
	jm := compileAndJIT(t, `
func sameKind() bool {
	return AnyKind(Any(5)) == AnyKind(Any(6))
}
func differentKind() bool {
	return AnyKind(Any(5)) == AnyKind(Any(true))
}
`)
	if got := jm.runBool(t, "sameKind"); !got {
		t.Errorf("AnyKind(Any(5)) == AnyKind(Any(6)) = %v, want true", got)
	}
	if got := jm.runBool(t, "differentKind"); got {
		t.Errorf("AnyKind(Any(5)) == AnyKind(Any(true)) = %v, want false", got)
	}
}

// TestAnyKindAndNameForMap proves a boxed map doesn't crash AnyKind/AnyName
// and reports a stable kind distinct from a non-map, plus a non-empty
// AnyName - "map" (anyPrimitiveDisplayName's fallback, since TypeMap has no
// Display() column), not asserted verbatim here since the exact string isn't
// this round's contract.
func TestAnyKindAndNameForMap(t *testing.T) {
	jm := compileAndJIT(t, `
func sameKind() bool {
	a := make(map[string]int)
	b := make(map[int]bool)
	return AnyKind(Any(a)) == AnyKind(Any(b))
}
func differentFromInt() bool {
	m := make(map[string]int)
	return AnyKind(Any(m)) != AnyKind(Any(5))
}
func nameNonEmpty() bool {
	m := make(map[string]int)
	return len(AnyName(Any(m))) > 0
}
`)
	if got := jm.runBool(t, "sameKind"); !got {
		t.Errorf("AnyKind matches across two different map shapes = %v, want true", got)
	}
	if got := jm.runBool(t, "differentFromInt"); !got {
		t.Errorf("AnyKind(map) != AnyKind(int) = %v, want true", got)
	}
	if got := jm.runBool(t, "nameNonEmpty"); !got {
		t.Errorf("AnyName(boxed map) non-empty = %v, want true", got)
	}
}

// --- AnyFields: byte-correctness of a boxed struct's own fields ---

// TestAnyFieldsStructRoundTripByteCorrect JIT-executes box -> AnyFields ->
// AnyAs for a real 2-field struct, proving each field's own real value
// survives the full round trip - not just that it compiles or that the
// field count is right.
func TestAnyFieldsStructRoundTripByteCorrect(t *testing.T) {
	jm := compileAndJIT(t, `
struct Point {
	X int
	Y int
}

func f() int {
	p := Point{X: 3, Y: 4}
	a := Any(p)
	sum := 0
	for name, v := range AnyFields(a) {
		fv, ok := AnyAs[int](v)
		if ok {
			if name == "X" {
				sum = sum + fv * 100
			}
			if name == "Y" {
				sum = sum + fv
			}
		}
	}
	return sum
}
`)
	if got := jm.runInt32(t, "f"); got != 304 {
		t.Errorf("f() = %d, want 304 (X=3 *100 + Y=4)", got)
	}
}

// TestAnyFieldsNestedStructRecursiveBoxing proves a nested struct field is
// itself recursively boxed - AnyAs[Point] on a field's own Any value (not
// just AnyAs[int] on a leaf scalar) must round-trip correctly too.
func TestAnyFieldsNestedStructRecursiveBoxing(t *testing.T) {
	jm := compileAndJIT(t, `
struct Point {
	X int
	Y int
}
struct Line {
	Start Point
	End   Point
}

func f() int {
	l := Line{Start: Point{X: 1, Y: 2}, End: Point{X: 5, Y: 6}}
	a := Any(l)
	sum := 0
	for name, v := range AnyFields(a) {
		pv, ok := AnyAs[Point](v)
		if ok {
			if name == "Start" {
				sum = sum + pv.X * 1000 + pv.Y * 100
			}
			if name == "End" {
				sum = sum + pv.X * 10 + pv.Y
			}
		}
	}
	return sum
}
`)
	// Start: X=1,Y=2 -> 1000+200=1200; End: X=5,Y=6 -> 50+6=56; total 1256.
	if got := jm.runInt32(t, "f"); got != 1256 {
		t.Errorf("f() = %d, want 1256", got)
	}
}

// TestAnyFieldsDirectSelfNesting proves a field value can be range'd via
// AnyFields directly (for n2, v2 := range AnyFields(v)), not just passed
// through AnyAs[T] first - a field's own Any value is statically typed Any,
// so this needs no special support, just confirming it.
func TestAnyFieldsDirectSelfNesting(t *testing.T) {
	jm := compileAndJIT(t, `
struct Point {
	X int
	Y int
}
struct Line {
	Start Point
	End   Point
}

func f() int {
	l := Line{Start: Point{X: 1, Y: 2}, End: Point{X: 5, Y: 6}}
	a := Any(l)
	sum := 0
	for _, outer := range AnyFields(a) {
		for _, inner := range AnyFields(outer) {
			v, ok := AnyAs[int](inner)
			if ok {
				sum = sum + v
			}
		}
	}
	return sum
}
`)
	// 1+2+5+6 = 14.
	if got := jm.runInt32(t, "f"); got != 14 {
		t.Errorf("f() = %d, want 14", got)
	}
}

func TestAnyFieldsCountMatchesDeclaredFields(t *testing.T) {
	jm := compileAndJIT(t, `
struct Point {
	X int
	Y int
}

func f() int {
	p := Point{X: 1, Y: 2}
	a := Any(p)
	count := 0
	for name, v := range AnyFields(a) {
		count = count + 1
	}
	return count
}
`)
	if got := jm.runInt32(t, "f"); got != 2 {
		t.Errorf("f() = %d, want 2", got)
	}
}

// TestVariadicAnyCollectImplicitlyBoxesJIT is a real, JIT-executed proof
// that collecting a raw int/string/bool/untyped-float literal into a
// ...Any parameter (no explicit Any(x) at the call site - see sema's
// checkVariadicCallArgs) produces correctly boxed, correctly discriminated
// values, not just that it compiles. The untyped float literal (3.5)
// specifically exercises retypeUntyped's own codegen-visible effect - the
// exact spot an earlier bug in this same feature (an untyped literal never
// retyped, reaching genNumberLit still untyped) actually panicked in.
func TestVariadicAnyCollectImplicitlyBoxesJIT(t *testing.T) {
	jm := compileAndJIT(t, `
func Log(args ...Any) int {
	intSum := 0
	floatSum := f64(0)
	for _, a := range args {
		iv, ok := AnyAs[int](a)
		if ok {
			intSum = intSum + iv
		}
		fv, fok := AnyAs[f64](a)
		if fok {
			floatSum = floatSum + fv
		}
	}
	return len(args)*100 + intSum + int(floatSum)
}

func f() int {
	return Log(5, 7, "bob", true, 3.5)
}
`)
	// len(args)=5, intSum(5+7)=12, floatSum(3.5)->int(3.5)=3 -> 5*100+12+3=515.
	if got := jm.runInt32(t, "f"); got != 515 {
		t.Errorf("f() = %d, want 515", got)
	}
}

// --- AnyLen/AnyIndex: boxed array reflection ---

func TestAnyLenFixedArray(t *testing.T) {
	jm := compileAndJIT(t, `
func one() int {
	s := [1]int{7}
	a := Any(s)
	return AnyLen(a)
}
func three() int {
	s := [3]int{10, 20, 30}
	a := Any(s)
	return AnyLen(a)
}
`)
	if got := jm.runInt32(t, "one"); got != 1 {
		t.Errorf("one() = %d, want 1", got)
	}
	if got := jm.runInt32(t, "three"); got != 3 {
		t.Errorf("three() = %d, want 3", got)
	}
}

func TestAnyLenDynamicArray(t *testing.T) {
	jm := compileAndJIT(t, `
func four() int {
	s := []int{10, 20, 30, 40}
	a := Any(s)
	return AnyLen(a)
}
func empty() int {
	s := make([]int, 0)
	a := Any(s)
	return AnyLen(a)
}
`)
	if got := jm.runInt32(t, "four"); got != 4 {
		t.Errorf("four() = %d, want 4", got)
	}
	if got := jm.runInt32(t, "empty"); got != 0 {
		t.Errorf("empty() = %d, want 0 (zero-length dynamic array)", got)
	}
}

// TestAnyLenNonArrayReturnsZero proves AnyLen's own permissive "wrong kind
// at runtime is a harmless zero" behavior (checkAnyLenCall/genAnyLenCall),
// not a crash, for both a boxed scalar and a boxed struct.
func TestAnyLenNonArrayReturnsZero(t *testing.T) {
	jm := compileAndJIT(t, `
struct Point {
	X int
}
func viaInt() int {
	a := Any(42)
	return AnyLen(a)
}
func viaStruct() int {
	p := Point{X: 1}
	a := Any(p)
	return AnyLen(a)
}
`)
	if got := jm.runInt32(t, "viaInt"); got != 0 {
		t.Errorf("viaInt() = %d, want 0", got)
	}
	if got := jm.runInt32(t, "viaStruct"); got != 0 {
		t.Errorf("viaStruct() = %d, want 0", got)
	}
}

// TestAnyIndexFixedArrayRoundTrip covers first/middle/last in-bounds reads
// for a fixed array, proving the returned Any's own AnyAs[int] round trip is
// byte-correct, not just that AnyIndex reports ok=true.
func TestAnyIndexFixedArrayRoundTrip(t *testing.T) {
	jm := compileAndJIT(t, `
func f(i int) int {
	s := [3]int{10, 20, 30}
	a := Any(s)
	v, ok := AnyIndex(a, i)
	if !ok {
		return -1
	}
	iv, iok := AnyAs[int](v)
	if !iok {
		return -2
	}
	return iv
}
`)
	for i, want := range map[int32]int32{0: 10, 1: 20, 2: 30} {
		if got := jm.runInt32(t, "f", i); got != want {
			t.Errorf("f(%d) = %d, want %d", i, got, want)
		}
	}
}

// TestAnyIndexDynamicArrayRoundTrip mirrors the fixed-array case above for a
// dynamic array.
func TestAnyIndexDynamicArrayRoundTrip(t *testing.T) {
	jm := compileAndJIT(t, `
func f(i int) int {
	s := []int{10, 20, 30}
	a := Any(s)
	v, ok := AnyIndex(a, i)
	if !ok {
		return -1
	}
	iv, iok := AnyAs[int](v)
	if !iok {
		return -2
	}
	return iv
}
`)
	for i, want := range map[int32]int32{0: 10, 1: 20, 2: 30} {
		if got := jm.runInt32(t, "f", i); got != want {
			t.Errorf("f(%d) = %d, want %d", i, got, want)
		}
	}
}

// TestAnyIndexOutOfBoundsReturnsFalse covers a negative index, an index
// exactly at the length, and one well past it, for both array kinds -
// AnyIndex must never crash or read out of bounds, only report ok=false.
func TestAnyIndexOutOfBoundsReturnsFalse(t *testing.T) {
	jm := compileAndJIT(t, `
func fixedAt(i int) bool {
	s := [3]int{1, 2, 3}
	a := Any(s)
	_, ok := AnyIndex(a, i)
	return ok
}
func dynAt(i int) bool {
	s := []int{1, 2, 3}
	a := Any(s)
	_, ok := AnyIndex(a, i)
	return ok
}
`)
	for _, i := range []int32{-1, 3, 100} {
		if got := jm.runBool(t, "fixedAt", i); got {
			t.Errorf("fixedAt(%d) = true, want false", i)
		}
		if got := jm.runBool(t, "dynAt", i); got {
			t.Errorf("dynAt(%d) = true, want false", i)
		}
	}
}

// TestAnyIndexNonArrayReturnsFalse proves a wrong-kind `a` reports ok=false
// rather than crashing (there's no descriptor-level array shape to index
// into at all).
func TestAnyIndexNonArrayReturnsFalse(t *testing.T) {
	jm := compileAndJIT(t, `
func f() bool {
	a := Any(42)
	_, ok := AnyIndex(a, 0)
	return ok
}
`)
	if got := jm.runBool(t, "f"); got {
		t.Errorf("f() = true, want false")
	}
}

// TestAnyFieldsStructWithArrayFieldReflectable exercises the array-boxing/
// interning path end to end through a struct field: AnyFields walks to the
// array field's own Any value, which AnyLen/AnyIndex then reflect over
// exactly like a directly-boxed array.
func TestAnyFieldsStructWithArrayFieldReflectable(t *testing.T) {
	jm := compileAndJIT(t, `
struct Bag {
	Items []int
}
func f() int {
	b := Bag{Items: []int{5, 6, 7}}
	a := Any(b)
	sum := 0
	for name, v := range AnyFields(a) {
		if name == "Items" {
			sum = sum + AnyLen(v)
			e, ok := AnyIndex(v, 1)
			if ok {
				ev, eok := AnyAs[int](e)
				if eok {
					sum = sum + ev
				}
			}
		}
	}
	return sum
}
`)
	// len(Items)=3, Items[1]=6 -> 3+6=9.
	if got := jm.runInt32(t, "f"); got != 9 {
		t.Errorf("f() = %d, want 9", got)
	}
}

// TestAnyIndexArrayOfStructsFieldsWalk proves AnyIndex's returned Any for a
// struct-typed array element is itself fully reflectable via AnyFields, not
// just AnyAs[T] for a scalar element.
func TestAnyIndexArrayOfStructsFieldsWalk(t *testing.T) {
	jm := compileAndJIT(t, `
struct Point {
	X int
	Y int
}
func f() int {
	pts := [2]Point{Point{1, 2}, Point{3, 4}}
	a := Any(pts)
	e, ok := AnyIndex(a, 1)
	if !ok {
		return -1
	}
	sum := 0
	for name, v := range AnyFields(e) {
		fv, fok := AnyAs[int](v)
		if fok {
			sum = sum + fv
		}
	}
	return sum
}
`)
	// pts[1] = {X:3, Y:4} -> 3+4=7.
	if got := jm.runInt32(t, "f"); got != 7 {
		t.Errorf("f() = %d, want 7", got)
	}
}

// --- enum boxing (see LANGUAGE.md's "Any" section) ---

// TestAnyBoxEnumUnitVariantKindAndName proves a boxed unit variant's own
// AnyName is the variant's own name, and AnyFields yields zero iterations
// (no associated data to walk) - same "nothing to walk" precedent every
// other field-less kind already establishes.
func TestAnyBoxEnumUnitVariantKindAndName(t *testing.T) {
	jm := compileAndJIT(t, `
enum Shape {
	Point,
	Circle(f64),
	Triangle { base f64, height f64 }
}
func f() int {
	a := Any(Shape.Point)
	ok := 0
	if AnyName(a) == "Point" {
		ok = ok + 1
	}
	count := 0
	for name, v := range AnyFields(a) {
		count = count + 1
	}
	if count == 0 {
		ok = ok + 1
	}
	return ok
}
`)
	if got := jm.runInt32(t, "f"); got != 2 {
		t.Errorf("f() = %d, want 2", got)
	}
}

// TestAnyBoxEnumTupleVariantFieldsPositional proves a boxed tuple variant's
// own AnyFields yields positional "0"/"1"/... names, and each one's own real
// value round-trips through AnyAs.
func TestAnyBoxEnumTupleVariantFieldsPositional(t *testing.T) {
	jm := compileAndJIT(t, `
enum Shape {
	Point,
	Circle(f64),
	Triangle { base f64, height f64 }
}
func f() int {
	a := Any(Shape.Circle(5.0))
	ok := 0
	if AnyName(a) == "Circle" {
		ok = ok + 1
	}
	for name, v := range AnyFields(a) {
		if name == "0" {
			fv, fok := AnyAs[f64](v)
			if fok && fv == 5.0 {
				ok = ok + 1
			}
		}
	}
	return ok
}
`)
	if got := jm.runInt32(t, "f"); got != 2 {
		t.Errorf("f() = %d, want 2", got)
	}
}

// TestAnyBoxEnumStructVariantFieldsNamed proves a boxed struct variant's own
// AnyFields yields its real declared field names, each one's own real value
// round-tripping through AnyAs.
func TestAnyBoxEnumStructVariantFieldsNamed(t *testing.T) {
	jm := compileAndJIT(t, `
enum Shape {
	Point,
	Circle(f64),
	Triangle { base f64, height f64 }
}
func f() int {
	a := Any(Shape.Triangle{base: 3.0, height: 4.0})
	ok := 0
	if AnyName(a) == "Triangle" {
		ok = ok + 1
	}
	sum := 0.0
	for name, v := range AnyFields(a) {
		fv, fok := AnyAs[f64](v)
		if fok {
			if name == "base" {
				sum = sum + fv
			}
			if name == "height" {
				sum = sum + fv
			}
		}
	}
	if sum == 7.0 {
		ok = ok + 1
	}
	return ok
}
`)
	if got := jm.runInt32(t, "f"); got != 2 {
		t.Errorf("f() = %d, want 2", got)
	}
}

// TestAnyBoxEnumMixedWidthTupleFieldsByteCorrect proves field offsets are
// read correctly for a tuple variant whose associated data isn't all one
// uniform width (every other enum-reflection test uses only f64 payloads,
// which a wrong constFieldOffset computation could still pass by
// coincidence - a mixed i8/f64/i32/string tuple can't).
func TestAnyBoxEnumMixedWidthTupleFieldsByteCorrect(t *testing.T) {
	jm := compileAndJIT(t, `
enum Tagged {
	Tup(i8, f64, i32, string)
}
func f() int {
	a := Any(Tagged.Tup(5, 2.5, 7, "hi"))
	ok := 0
	for name, v := range AnyFields(a) {
		if name == "0" {
			fv, fok := AnyAs[i8](v)
			if fok && fv == 5 {
				ok = ok + 1
			}
		}
		if name == "1" {
			fv, fok := AnyAs[f64](v)
			if fok && fv == 2.5 {
				ok = ok + 1
			}
		}
		if name == "2" {
			fv, fok := AnyAs[i32](v)
			if fok && fv == 7 {
				ok = ok + 1
			}
		}
		if name == "3" {
			fv, fok := AnyAs[string](v)
			if fok && fv == "hi" {
				ok = ok + 1
			}
		}
	}
	return ok
}
`)
	if got := jm.runInt32(t, "f"); got != 4 {
		t.Errorf("f() = %d, want 4", got)
	}
}

// TestAnyKindForEnumIsStableAcrossVariants proves AnyKind reports the same
// TypeEnum-kind ordinal for every variant of an enum (AnyKind never
// distinguishes which specific enum/variant, same as it already can't for
// two different structs - AnyName is what fills that gap).
func TestAnyKindForEnumIsStableAcrossVariants(t *testing.T) {
	jm := compileAndJIT(t, `
enum Shape {
	Point,
	Circle(f64)
}
func f() bool {
	a := Any(Shape.Point)
	b := Any(Shape.Circle(1.0))
	return AnyKind(a) == AnyKind(b)
}
`)
	if got := jm.runBool(t, "f"); !got {
		t.Errorf("f() = %v, want true", got)
	}
}

// TestAnyBoxStructWithEnumFieldDirectFieldsIsDocumentedLimitation proves the
// documented current-limitations.md gap: calling AnyFields directly on a
// struct's own enum-typed field (bypassing AnyAs[EnumType] first) reports
// the enum's own type name with zero fields, not the real active variant's
// data - since that field's descriptor is built once for the whole struct
// type, with no specific value's discriminant in hand. Locks in the
// current, intentionally-scoped behavior so a future real fix updates this
// test deliberately rather than silently changing it.
func TestAnyBoxStructWithEnumFieldDirectFieldsIsDocumentedLimitation(t *testing.T) {
	jm := compileAndJIT(t, `
enum Shape {
	Point,
	Circle(f64)
}
struct Bag {
	Item Shape
}
func f() int {
	b := Bag{Item: Shape.Circle(9.0)}
	a := Any(b)
	fieldCount := 0
	nameOk := false
	for name, v := range AnyFields(a) {
		if name == "Item" {
			nameOk = AnyName(v) == "Shape"
			for fname, fv := range AnyFields(v) {
				fieldCount = fieldCount + 1
			}
		}
	}
	if !nameOk {
		return -1
	}
	return fieldCount
}
`)
	if got := jm.runInt32(t, "f"); got != 0 {
		t.Errorf("f() = %d, want 0 (documented limitation - see docs/current-limitations.md; -1 means AnyName(v) wasn't \"Shape\")", got)
	}
}

// TestAnyBoxEnumTwoDifferentActiveVariantsReflectIndependently is the single
// most important behavioral proof of this feature: two boxed values of the
// SAME enum type, each holding a different active variant, must each report
// their OWN AnyName/AnyFields - not the other's, and not always the
// first-declared variant regardless of which is genuinely active. A broken
// runtime discriminant switch (genEnumAnyDescriptor) could easily always
// select one fixed variant and still pass every single-value test above.
func TestAnyBoxEnumTwoDifferentActiveVariantsReflectIndependently(t *testing.T) {
	jm := compileAndJIT(t, `
enum Shape {
	Point,
	Circle(f64),
	Triangle { base f64, height f64 }
}
func f() int {
	a := Any(Shape.Circle(5.0))
	b := Any(Shape.Triangle{base: 3.0, height: 4.0})

	ok := 0
	if AnyName(a) == "Circle" {
		ok = ok + 1
	}
	if AnyName(b) == "Triangle" {
		ok = ok + 1
	}

	aCount := 0
	for name, v := range AnyFields(a) {
		aCount = aCount + 1
	}
	if aCount == 1 {
		ok = ok + 1
	}

	bCount := 0
	for name, v := range AnyFields(b) {
		bCount = bCount + 1
	}
	if bCount == 2 {
		ok = ok + 1
	}

	return ok
}
`)
	if got := jm.runInt32(t, "f"); got != 4 {
		t.Errorf("f() = %d, want 4", got)
	}
}

// TestAnyAsEnumRoundTrip proves AnyAs[EnumType] round-trips a boxed enum
// value's real active variant and data, not just that the kind matches.
func TestAnyAsEnumRoundTrip(t *testing.T) {
	jm := compileAndJIT(t, `
enum Shape {
	Point,
	Circle(f64)
}
func f() int {
	a := Any(Shape.Circle(7.0))
	v, ok := AnyAs[Shape](a)
	if !ok {
		return -1
	}
	match v {
		Shape.Circle(r) => {
			if r == 7.0 {
				return 1
			}
		}
		_ => {}
	}
	return 0
}
`)
	if got := jm.runInt32(t, "f"); got != 1 {
		t.Errorf("f() = %d, want 1", got)
	}
}

// TestAnyAsEnumMismatchedEnumTypeReturnsFalse proves AnyAs[T]'s identity
// check (genAnyAsCall's own TypeEnum case) tells two DIFFERENT enum types
// apart, even though both are single-unit-variant (structurally identical
// shape) - each variant descriptor is interned by *sema.EnumVariant pointer
// identity, never shared across declarations, so this isn't just checking
// "some enum was boxed here" the way a map's own kind-only check does.
func TestAnyAsEnumMismatchedEnumTypeReturnsFalse(t *testing.T) {
	jm := compileAndJIT(t, `
enum Shape {
	Point
}
enum Color {
	Red
}
func f() bool {
	a := Any(Shape.Point)
	_, ok := AnyAs[Color](a)
	return ok
}
`)
	if got := jm.runBool(t, "f"); got {
		t.Errorf("f() = %v, want false (different enum types)", got)
	}
}

// TestAnyBoxStructWithEnumFieldRoundTrip proves a struct's own enum-typed
// field composes correctly through the containing struct's boxing - mirrors
// TestAnyBoxStructWithMapFieldRoundTrip's own template. The field's own
// descriptor in the struct's static field table is enumNestedDescriptor's
// variant-agnostic placeholder (built at struct-descriptor time, with no
// runtime value in hand to pick a real variant), but AnyAs[Shape] still
// round-trips the field's own real bytes correctly - see DECISIONS.md for
// why.
func TestAnyBoxStructWithEnumFieldRoundTrip(t *testing.T) {
	jm := compileAndJIT(t, `
enum Shape {
	Point,
	Circle(f64)
}
struct Bag {
	Item Shape
}
func f() int {
	b := Bag{Item: Shape.Circle(9.0)}
	a := Any(b)
	ok := 0
	for name, v := range AnyFields(a) {
		if name == "Item" {
			sv, sok := AnyAs[Shape](v)
			if sok {
				match sv {
					Shape.Circle(r) => {
						if r == 9.0 {
							ok = ok + 1
						}
					}
					_ => {}
				}
			}
		}
	}
	return ok
}
`)
	if got := jm.runInt32(t, "f"); got != 1 {
		t.Errorf("f() = %d, want 1", got)
	}
}

// TestAnyIndexArrayOfEnumsRoundTrip mirrors
// TestAnyIndexArrayOfStructsFieldsWalk for an array of enums - proves the
// nested (array-element) enum descriptor path composes correctly too, not
// just the struct-field one above.
func TestAnyIndexArrayOfEnumsRoundTrip(t *testing.T) {
	jm := compileAndJIT(t, `
enum Shape {
	Point,
	Circle(f64)
}
func f() int {
	shapes := [2]Shape{Shape.Point, Shape.Circle(6.0)}
	a := Any(shapes)
	e, ok := AnyIndex(a, 1)
	if !ok {
		return -1
	}
	sv, sok := AnyAs[Shape](e)
	if !sok {
		return -2
	}
	match sv {
		Shape.Circle(r) => {
			if r == 6.0 {
				return 1
			}
		}
		_ => {}
	}
	return 0
}
`)
	if got := jm.runInt32(t, "f"); got != 1 {
		t.Errorf("f() = %d, want 1", got)
	}
}

// TestVariadicGenericFuncInstantiatedAtAnyJIT is a real, JIT-executed proof
// that a generic function's own variadic parameter, explicitly instantiated
// at T=Any, gets the same implicit boxing an ordinary (non-generic) ...Any
// parameter does - the monomorphized signature genCallArgValues/
// checkVariadicCallArgs see is indistinguishable from a hand-written one.
func TestVariadicGenericFuncInstantiatedAtAnyJIT(t *testing.T) {
	jm := compileAndJIT(t, `
func Log[T](args ...T) int {
	sum := 0
	for _, a := range args {
		iv, ok := AnyAs[int](a)
		if ok {
			sum = sum + iv
		}
	}
	return len(args)*100 + sum
}

func f() int {
	return Log[Any](5, 7, "bob")
}
`)
	// len(args)=3, sum(5+7)=12 -> 3*100+12=312.
	if got := jm.runInt32(t, "f"); got != 312 {
		t.Errorf("f() = %d, want 312", got)
	}
}
