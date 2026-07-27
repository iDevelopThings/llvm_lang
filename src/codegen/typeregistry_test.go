package codegen

import "testing"

// --- TypeId[T]/TypeIdOf: same id for the same concrete type ---

func TestTypeIdAndTypeIdOfSamePrimitiveMatch(t *testing.T) {
	jm := compileAndJIT(t, `
func f() bool {
	a := TypeId[int]()
	x := 5
	b := TypeIdOf(x)
	return a == b
}
`)
	if got := jm.runBool(t, "f"); !got {
		t.Errorf("f() = %v, want true (TypeId[int]() == TypeIdOf(5))", got)
	}
}

func TestTypeIdAndTypeIdOfSameStructMatch(t *testing.T) {
	jm := compileAndJIT(t, `
struct Point {
	X int
}
func f() bool {
	a := TypeId[Point]()
	p := Point{X: 1}
	b := TypeIdOf(p)
	return a == b
}
`)
	if got := jm.runBool(t, "f"); !got {
		t.Errorf("f() = %v, want true (TypeId[Point]() == TypeIdOf(p))", got)
	}
}

func TestTypeIdAndTypeIdOfSameArrayMatch(t *testing.T) {
	jm := compileAndJIT(t, `
func f() bool {
	a := TypeId[[]int]()
	s := []int{1, 2, 3}
	b := TypeIdOf(s)
	return a == b
}
`)
	if got := jm.runBool(t, "f"); !got {
		t.Errorf("f() = %v, want true (TypeId[[]int]() == TypeIdOf(s))", got)
	}
}

// --- TypeId[T]: distinct types intern to distinct ids, the same type interns once ---

func TestTypeIdDifferentStructsGetDifferentIds(t *testing.T) {
	jm := compileAndJIT(t, `
struct Point {
	X int
}
struct Other {
	Y int
}
func f() bool {
	a := TypeId[Point]()
	b := TypeId[Other]()
	return a != b
}
`)
	if got := jm.runBool(t, "f"); !got {
		t.Errorf("f() = %v, want true (Point and Other get different ids)", got)
	}
}

// TestTypeIdSameStructFromTwoCallSitesGetsSameId proves a struct's own id is
// interned (assigned once, in setupTypeRegistry), not freshly minted per
// call site - g calls TypeId[Point]() from a different function entirely.
func TestTypeIdSameStructFromTwoCallSitesGetsSameId(t *testing.T) {
	jm := compileAndJIT(t, `
struct Point {
	X int
}
func g() int {
	return TypeId[Point]()
}
func f() bool {
	a := TypeId[Point]()
	b := g()
	return a == b
}
`)
	if got := jm.runBool(t, "f"); !got {
		t.Errorf("f() = %v, want true (same struct, same id across call sites)", got)
	}
}

// TestTypeIdOfEnumSameIdRegardlessOfActiveVariant proves TypeId[T]/TypeIdOf
// on an enum-typed value is deliberately NOT variant-specific (unlike
// AnyName's own "active variant" behavior) - two boxed values of the same
// enum holding different variants both report the identical id, matching
// TypeId[Shape]() itself.
func TestTypeIdOfEnumSameIdRegardlessOfActiveVariant(t *testing.T) {
	jm := compileAndJIT(t, `
enum Shape {
	Circle(f64),
	Point
}
func f() bool {
	a := Any(Shape.Circle(2.0))
	b := Any(Shape.Point)
	v1, ok1 := AnyAs[Shape](a)
	v2, ok2 := AnyAs[Shape](b)
	if !ok1 || !ok2 {
		return false
	}
	id1 := TypeIdOf(v1)
	id2 := TypeIdOf(v2)
	id3 := TypeId[Shape]()
	return id1 == id2 && id2 == id3
}
`)
	if got := jm.runBool(t, "f"); !got {
		t.Errorf("f() = %v, want true (Circle/Point variants share Shape's own id)", got)
	}
}

// --- TypeByName ---

func TestTypeByNameFindsStructMatchingItsOwnId(t *testing.T) {
	jm := compileAndJIT(t, `
struct Point {
	X int
}
func f() bool {
	want := TypeId[Point]()
	ids := TypeByName("Point")
	if len(ids) != 1 {
		return false
	}
	return ids[0] == want
}
`)
	if got := jm.runBool(t, "f"); !got {
		t.Errorf("f() = %v, want true (TypeByName(\"Point\") == [TypeId[Point]()])", got)
	}
}

func TestTypeByNameNoMatchReturnsEmpty(t *testing.T) {
	jm := compileAndJIT(t, `
func f() bool {
	ids := TypeByName("NoSuchType")
	return len(ids) == 0
}
`)
	if got := jm.runBool(t, "f"); !got {
		t.Errorf("f() = %v, want true (no match -> empty []int, not a crash)", got)
	}
}

// TestTypeByNameTwoDifferentPackagesSameNameReturnsBothIds covers the reason
// TypeByName returns every match rather than one (see LANGUAGE.md's "Type
// registry" section): this language has no package-qualified naming, so two
// distinct packages can legally each declare their own "Point" - "app"
// doesn't import either package at all (TypeByName is a runtime string
// lookup, no static reference needed), proving both still get registered
// purely by being loaded into the same program (see setupTypeRegistry's own
// doc comment: every tree GeneratePackage receives, not just the entry
// package's).
func TestTypeByNameTwoDifferentPackagesSameNameReturnsBothIds(t *testing.T) {
	jm := compileProgramAndJIT(t, []programPackage{
		{
			key: "pkgA",
			files: []packageFile{
				{"pkgA/point.llx", "struct Point {\n\tX int\n}\n"},
			},
		},
		{
			key: "pkgB",
			files: []packageFile{
				{"pkgB/point.llx", "struct Point {\n\tY int\n}\n"},
			},
		},
		{
			key: "app",
			files: []packageFile{
				{"app/main.llx", `
func main() int {
	ids := TypeByName("Point")
	if len(ids) != 2 {
		return -1
	}
	if ids[0] == ids[1] {
		return -2
	}
	return 0
}
`},
			},
		},
	})
	if got := jm.runInt32(t, "main"); got != 0 {
		t.Errorf("main() = %d, want 0 (both pkgA.Point and pkgB.Point found, distinct ids)", got)
	}
}

// --- AnyNew ---

// TestAnyNewStructZeroFieldsViaAnyFields proves AnyNew hands back a genuinely
// zero-valued struct - every field read back byte-correct, not garbage - by
// walking every field via AnyFields rather than trusting a single top-level
// comparison.
func TestAnyNewStructZeroFieldsViaAnyFields(t *testing.T) {
	jm := compileAndJIT(t, `
struct Point {
	X int
	Y int
}
func f() bool {
	id := TypeId[Point]()
	a, ok := AnyNew(id)
	if !ok {
		return false
	}
	allZero := true
	for name, v := range AnyFields(a) {
		fv, fok := AnyAs[int](v)
		if !fok || fv != 0 {
			allZero = false
		}
	}
	return allZero
}
`)
	if got := jm.runBool(t, "f"); !got {
		t.Errorf("f() = %v, want true (every field of a fresh AnyNew(Point) is zero)", got)
	}
}

// TestAnyNewMapProducesNilMap proves AnyNew's own zero-fill for a map
// produces exactly the same nil-map state `make`-less `var m map[K]V`
// already produces - a map's runtime value is a single opaque pointer (see
// LANGUAGE.md's "Any" section), so all-zero-bytes is a null pointer, this
// language's own existing legitimate nil map.
func TestAnyNewMapProducesNilMap(t *testing.T) {
	jm := compileAndJIT(t, `
func f() bool {
	id := TypeId[map[string]int]()
	a, ok := AnyNew(id)
	if !ok {
		return false
	}
	m, ok2 := AnyAs[map[string]int](a)
	if !ok2 {
		return false
	}
	return len(m) == 0
}
`)
	if got := jm.runBool(t, "f"); !got {
		t.Errorf("f() = %v, want true (AnyNew map -> legitimate nil map, len 0)", got)
	}
}

// TestAnyNewDynamicArrayProducesZeroLengthSlice mirrors
// TestAnyNewMapProducesNilMap for a dynamic array - all-zero-bytes is
// exactly the {null, 0, 0} header an ordinary `var s []T` already produces.
func TestAnyNewDynamicArrayProducesZeroLengthSlice(t *testing.T) {
	jm := compileAndJIT(t, `
func f() bool {
	id := TypeId[[]int]()
	a, ok := AnyNew(id)
	if !ok {
		return false
	}
	s, ok2 := AnyAs[[]int](a)
	if !ok2 {
		return false
	}
	return len(s) == 0
}
`)
	if got := jm.runBool(t, "f"); !got {
		t.Errorf("f() = %v, want true (AnyNew []int -> zero-length slice)", got)
	}
}

// TestAnyNewOutOfRangeIdReturnsFalse proves a negative or past-the-end id
// never crashes - mirroring AnyIndex's own bounds-checking precedent.
func TestAnyNewOutOfRangeIdReturnsFalse(t *testing.T) {
	jm := compileAndJIT(t, `
func tooLarge() bool {
	_, ok := AnyNew(999999)
	return ok
}
func negative() bool {
	_, ok := AnyNew(-1)
	return ok
}
`)
	if got := jm.runBool(t, "tooLarge"); got {
		t.Errorf("tooLarge() = %v, want false", got)
	}
	if got := jm.runBool(t, "negative"); got {
		t.Errorf("negative() = %v, want false", got)
	}
}

// TestAnyNewEnumIdRejected proves AnyNew rejects a valid, in-range id that
// happens to name an enum - the explicit scope exclusion (see BLOCKERS.md's
// "An enum's zero value..." entry) - never silently constructing the known-
// broken zero-payload state.
func TestAnyNewEnumIdRejected(t *testing.T) {
	jm := compileAndJIT(t, `
enum Shape {
	Circle(f64),
	Point
}
func f() bool {
	id := TypeId[Shape]()
	_, ok := AnyNew(id)
	return ok
}
`)
	if got := jm.runBool(t, "f"); got {
		t.Errorf("f() = %v, want false (an enum's own id is never constructible via AnyNew)", got)
	}
}

// --- AnySet[T] ---

// TestAnySetWriteVisibleThroughOriginalStructVariable is the single most
// important behavioral proof of the mutation half of this feature: a
// struct field's own Any (from AnyFields) shares the parent Any's own live
// arena storage (see genRangeForAnyFields's doc comment), so a write through
// AnySet is visible when the SAME boxed value is read back afterward - not
// through the pre-Any(p) stack variable p itself, which Any(p)'s own
// copy-on-box semantics (genAnyBox) already makes impossible to reach this
// way; "the original variable" here is the boxed `a`, re-read via AnyAs.
func TestAnySetWriteVisibleThroughOriginalStructVariable(t *testing.T) {
	jm := compileAndJIT(t, `
struct Point {
	X int
	Y int
}
func f() int {
	p := Point{X: 1, Y: 2}
	a := Any(p)
	for name, v := range AnyFields(a) {
		if name == "X" {
			AnySet[int](v, 100)
		}
	}
	after, ok := AnyAs[Point](a)
	if !ok {
		return -1
	}
	return after.X
}
`)
	if got := jm.runInt32(t, "f"); got != 100 {
		t.Errorf("f() = %d, want 100 (AnySet's write visible through a)", got)
	}
}

// TestAnySetMismatchedTypeLeavesValueUnchanged proves a failed AnySet[T]
// (wrong T against the field's real boxed kind) writes nothing at all - the
// field's own real value survives, not just that the return value is false.
func TestAnySetMismatchedTypeLeavesValueUnchanged(t *testing.T) {
	jm := compileAndJIT(t, `
struct Point {
	X int
	Y int
}
func f() bool {
	p := Point{X: 1, Y: 2}
	a := Any(p)
	ok := true
	for name, v := range AnyFields(a) {
		if name == "X" {
			ok = AnySet[f64](v, 9.0)
		}
	}
	after, aok := AnyAs[Point](a)
	if !aok {
		return false
	}
	return !ok && after.X == 1
}
`)
	if got := jm.runBool(t, "f"); !got {
		t.Errorf("f() = %v, want true (mismatched AnySet returns false, X still 1)", got)
	}
}

// TestAnyNewNonCopyableStructRejected proves AnyNew also rejects a non-
// copyable struct's own id, not just an enum's - TypeId[Res]() is itself
// legal (isBoxableIntoAny doesn't check copyability, only structural
// boxability - see checkTypeIdCall), but AnyNew constructing a fresh
// instance of it would let a later AnyAs[Res] copy it back out, exactly the
// implicit copy this language otherwise never allows for a non-copyable
// type - the same class of risk BLOCKERS.md's enum-zero-value entry flags,
// generalized here to structs (see registryConstructible).
func TestAnyNewNonCopyableStructRejected(t *testing.T) {
	jm := compileAndJIT(t, `
struct Res {
	x int
	destructor() {
		this.x = 0
	}
}
func f() bool {
	id := TypeId[Res]()
	_, ok := AnyNew(id)
	return ok
}
`)
	if got := jm.runBool(t, "f"); got {
		t.Errorf("f() = %v, want false (a non-copyable struct's own id is never constructible via AnyNew)", got)
	}
}

// TestAnyNewAnyIdRejected proves AnyNew rejects TypeId[Any]()'s own id -
// TypeAny is legally boxable (isBoxableIntoAny's no-op re-box rule), so it
// has a real descriptor and TypeId[Any]() succeeds, but a zero-filled Any is
// {data: nil, desc: nil}, unlike every other primitive's valid all-zero-bytes
// zero value. Every reflection builtin (AnyKind/AnyAs/...) loads unconditionally
// off the descriptor pointer, so constructing one would crash on first use.
func TestAnyNewAnyIdRejected(t *testing.T) {
	jm := compileAndJIT(t, `
func f() bool {
	id := TypeId[Any]()
	_, ok := AnyNew(id)
	return ok
}
`)
	if got := jm.runBool(t, "f"); got {
		t.Errorf("f() = %v, want false (Any's own id is never constructible via AnyNew)", got)
	}
}
