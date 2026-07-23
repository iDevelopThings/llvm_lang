package codegen

import "testing"

// TestStructEqualityEveryFieldKind covers a real gap genValueEqual (expr.go)
// used to have: its switch only ever implemented sema.TypeInt (an alias for
// TypeI32)/TypeBool directly, plus TypeString/TypeStruct/TypeArray recursion
// - a struct field of any other Kind (i8/i16/i64/f32/f64/a pointer) panicked
// at codegen time even though checkEqualityOperands (sema/typecheck.go) had
// already accepted the comparison via plain Type.Equal on the whole struct
// type, which never itself validates that every recursive field Kind is one
// genValueEqual actually implements. Each function below builds its own
// struct values entirely in source (no parameters) - the JIT test harness's
// runBool only ever passes plain int32 arguments, and threading real
// i8/i16/i64/f32/f64/pointer values through it would need machinery this
// test doesn't otherwise need.
func TestStructEqualityEveryFieldKind(t *testing.T) {
	jm := compileAndJIT(t, `
struct Wide {
	a i8
	b i16
	c i64
	d f32
	e f64
	p *int
}

func wideEqualSamePointer() bool {
	x := 5
	a := Wide{1, 2, 3000000000, 1.5, 2.5, &x}
	b := Wide{1, 2, 3000000000, 1.5, 2.5, &x}
	return a == b
}

func wideNotEqualDifferentI8() bool {
	x := 5
	a := Wide{1, 2, 3000000000, 1.5, 2.5, &x}
	b := Wide{9, 2, 3000000000, 1.5, 2.5, &x}
	return a == b
}

func wideNotEqualDifferentI16() bool {
	x := 5
	a := Wide{1, 2, 3000000000, 1.5, 2.5, &x}
	b := Wide{1, 99, 3000000000, 1.5, 2.5, &x}
	return a == b
}

func wideNotEqualDifferentI64() bool {
	x := 5
	a := Wide{1, 2, 3000000000, 1.5, 2.5, &x}
	b := Wide{1, 2, 3000000001, 1.5, 2.5, &x}
	return a == b
}

func wideNotEqualDifferentF32() bool {
	x := 5
	a := Wide{1, 2, 3000000000, 1.5, 2.5, &x}
	b := Wide{1, 2, 3000000000, 9.5, 2.5, &x}
	return a == b
}

func wideNotEqualDifferentF64() bool {
	x := 5
	a := Wide{1, 2, 3000000000, 1.5, 2.5, &x}
	b := Wide{1, 2, 3000000000, 1.5, 9.5, &x}
	return a == b
}

func wideNotEqualDifferentPointer() bool {
	x := 5
	y := 5
	a := Wide{1, 2, 3000000000, 1.5, 2.5, &x}
	b := Wide{1, 2, 3000000000, 1.5, 2.5, &y}
	return a == b
}

func wideNotEqualUsesBangEqual() bool {
	x := 5
	a := Wide{1, 2, 3000000000, 1.5, 2.5, &x}
	b := Wide{9, 2, 3000000000, 1.5, 2.5, &x}
	return a != b
}
`)

	if !jm.runBool(t, "wideEqualSamePointer") {
		t.Error("wideEqualSamePointer() = false, want true (every field, including i8/i16/i64/f32/f64/pointer, matches)")
	}
	if jm.runBool(t, "wideNotEqualDifferentI8") {
		t.Error("wideNotEqualDifferentI8() = true, want false (i8 field differs)")
	}
	if jm.runBool(t, "wideNotEqualDifferentI16") {
		t.Error("wideNotEqualDifferentI16() = true, want false (i16 field differs)")
	}
	if jm.runBool(t, "wideNotEqualDifferentI64") {
		t.Error("wideNotEqualDifferentI64() = true, want false (i64 field differs)")
	}
	if jm.runBool(t, "wideNotEqualDifferentF32") {
		t.Error("wideNotEqualDifferentF32() = true, want false (f32 field differs)")
	}
	if jm.runBool(t, "wideNotEqualDifferentF64") {
		t.Error("wideNotEqualDifferentF64() = true, want false (f64 field differs)")
	}
	if jm.runBool(t, "wideNotEqualDifferentPointer") {
		t.Error("wideNotEqualDifferentPointer() = true, want false (pointer fields hold different addresses, even though both point to a value 5)")
	}
	if !jm.runBool(t, "wideNotEqualUsesBangEqual") {
		t.Error("wideNotEqualUsesBangEqual() = false, want true (!= correctly reports true when a field differs)")
	}
}
