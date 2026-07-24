package sema

import "testing"

// Unsigned integer widths (u8/u16/u32/u64) mirror the signed ones exactly
// except for signedness - see LANGUAGE.md's Types section. These tests cover
// the type-rule half (valid names, no-implicit-mixing, untyped adaptation,
// explicit conversions); codegen/unsigned_test.go covers lowering.

func TestUnsignedTypeNamesAreValid(t *testing.T) {
	checkSrc(t, "var a u8 = 1\nvar b u16 = 1\nvar c u32 = 1\nvar d u64 = 1\n")
}

func TestUnsignedTopOfRangeLiteralsTypecheck(t *testing.T) {
	// sema itself never range-checks literals (that's codegen) - it only needs
	// to accept the type; the range check is proven in codegen/unsigned_test.go.
	checkSrc(t, "var a u8 = 255\nvar b u16 = 65535\nvar c u32 = 4294967295\nvar d u64 = 18446744073709551615\n")
}

// --- no implicit mixing (same rule as the existing i32/i64 case) ---

func TestSignedAndUnsignedSameWidthCannotMix(t *testing.T) {
	// An i32 and a u32 must never silently interoperate, exactly like an i32
	// and an i64 - see resolveNumericOperands (the concrete-operand path
	// requires Type.Equal, and TypeI32 != TypeU32 as Kinds).
	src := "func f() {\n\tvar a i32 = 1\n\tvar b u32 = 1\n\tvar c i32 = a + b\n}\n"
	expectCheckErrors(t, src, 1)
}

func TestDifferentUnsignedWidthsCannotMix(t *testing.T) {
	src := "func f() {\n\tvar a u8 = 1\n\tvar b u32 = 1\n\tvar c u32 = a + b\n}\n"
	expectCheckErrors(t, src, 1)
}

func TestSignedAndUnsignedSameWidthComparisonIsError(t *testing.T) {
	src := "func f() {\n\tvar a i32 = 1\n\tvar b u32 = 1\n\tif a > b {\n\t}\n}\n"
	expectCheckErrors(t, src, 1)
}

// --- untyped-constant adaptation to an unsigned context ---

func TestUntypedIntAdaptsToUnsignedWidths(t *testing.T) {
	checkSrc(t, "var a u8 = 5\nvar b u16 = 5\nvar c u32 = 5\nvar d u64 = 5\n")
}

func TestUntypedIntOperandAdaptsToConcreteUnsignedWidthInBinaryExpr(t *testing.T) {
	src := "func f() {\n\tvar a u32 = 1\n\tvar b u32 = a + 5\n}\n"
	checkSrc(t, src)
}

func TestUntypedFloatCannotAdaptToUnsigned(t *testing.T) {
	expectCheckErrors(t, "var a u32 = 5.5\n", 1)
}

func TestUnsignedTypeRecordedOnNode(t *testing.T) {
	tree, info := checkSrc(t, "var a u16 = 5\n")
	decl := tree.Children(tree.Root)[0]
	init := tree.Child(decl, 2)
	if got := info.Types[init]; got.Kind != TypeU16 {
		t.Errorf("Types[init] = %v, want u16", got)
	}
}

// --- explicit numeric conversions to/from unsigned ---

func TestConversionToUnsignedWidths(t *testing.T) {
	src := "func f() {\n\tvar a i32 = 1\n\tvar b u8 = u8(a)\n\tvar c u32 = u32(a)\n\tvar d u64 = u64(a)\n}\n"
	checkSrc(t, src)
}

func TestConversionBetweenUnsignedAndSignedRequiresExplicit(t *testing.T) {
	// Without the conversion this is a width/sign mismatch; with it, fine.
	checkSrc(t, "func f() {\n\tvar a u32 = 1\n\tvar b i32 = i32(a)\n}\n")
	expectCheckErrors(t, "func f() {\n\tvar a u32 = 1\n\tvar b i32 = a\n}\n", 1)
}

func TestConversionBetweenUnsignedAndFloat(t *testing.T) {
	src := "func f() {\n\tvar a u32 = 1\n\tvar b f64 = f64(a)\n\tvar c u32 = u32(b)\n}\n"
	checkSrc(t, src)
}

func TestSameUnsignedTypeConversionIsFine(t *testing.T) {
	checkSrc(t, "func f() {\n\tvar a u32 = 1\n\tvar b u32 = u32(a)\n}\n")
}

// --- unsigned is a legal value-match subject and print target ---

func TestUnsignedValueMatchSubjectIsAllowed(t *testing.T) {
	src := "func f() {\n\tvar a u8 = 1\n\tmatch a {\n\t\t1 => {}\n\t\t_ => {}\n\t}\n}\n"
	checkSrc(t, src)
}

func TestPrintAcceptsUnsigned(t *testing.T) {
	checkSrc(t, "func f() {\n\tvar a u64 = 1\n\tprint(a)\n}\n")
}
