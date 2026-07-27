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
