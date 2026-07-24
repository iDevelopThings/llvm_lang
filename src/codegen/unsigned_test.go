package codegen

import (
	"strings"
	"testing"
)

// Unsigned widths lower to the same LLVM iN types as their signed
// counterparts; only division/remainder/ordered-comparison/extension/
// int<->float conversion and print formatting branch on signedness (see
// CODEGEN.md's Types section). Each case below picks values where a signed
// interpretation would give a visibly different (wrong) result, so a
// regression to the signed instruction would actually fail the test.

func TestUnsignedWidthArithmetic(t *testing.T) {
	jm := compileAndJIT(t, `
func addU8() bool {
	var a u8 = 200
	var b u8 = 50
	return a + b == 250
}

func addU16() bool {
	var a u16 = 60000
	var b u16 = 5000
	return a + b == 65000
}

func mulU32() bool {
	var a u32 = 100000
	var b u32 = 40000
	return a * b == 4000000000
}

func addU64() u64 {
	var a u64 = 10000000000000000000
	var b u64 = 8000000000000000000
	return a + b
}
`)
	if !jm.runBool(t, "addU8") {
		t.Error("addU8() = false, want true")
	}
	if !jm.runBool(t, "addU16") {
		t.Error("addU16() = false, want true")
	}
	if !jm.runBool(t, "mulU32") {
		t.Error("mulU32() = false, want true (4000000000 exceeds i32 max - proves u32, not i32)")
	}
	if got := uint64(jm.runInt64(t, "addU64")); got != 18000000000000000000 {
		t.Errorf("addU64() = %d, want 18000000000000000000", got)
	}
}

// TestUnsignedDivisionAndRemainder proves genArithOp/genBinaryExpr select
// udiv/urem: the dividends exceed the signed max for their width, so an sdiv/
// srem would treat them as negative and produce a different result.
func TestUnsignedDivisionAndRemainder(t *testing.T) {
	jm := compileAndJIT(t, `
func divU32() bool {
	var a u32 = 3000000000
	var b u32 = 3
	return a / b == 1000000000
}

func remU32() bool {
	var a u32 = 3000000001
	var b u32 = 2
	return a % b == 1
}

func divU64() u64 {
	var a u64 = 18000000000000000000
	var b u64 = 2
	return a / b
}
`)
	if !jm.runBool(t, "divU32") {
		t.Error("divU32() = false, want true (unsigned udiv, not sdiv)")
	}
	if !jm.runBool(t, "remU32") {
		t.Error("remU32() = false, want true (unsigned urem, not srem)")
	}
	if got := uint64(jm.runInt64(t, "divU64")); got != 9000000000000000000 {
		t.Errorf("divU64() = %d, want 9000000000000000000", got)
	}
}

// TestUnsignedComparison proves genIntOrder uses the unsigned predicates: a
// u32 above the signed max compares as a large positive value, not negative.
func TestUnsignedComparison(t *testing.T) {
	jm := compileAndJIT(t, `
func gtU32() bool {
	var a u32 = 3000000000
	var b u32 = 5
	return a > b
}

func ltU32() bool {
	var a u32 = 3000000000
	var b u32 = 5
	return b < a
}
`)
	if !jm.runBool(t, "gtU32") {
		t.Error("gtU32() = false, want true (unsigned compare - 3000000000 > 5)")
	}
	if !jm.runBool(t, "ltU32") {
		t.Error("ltU32() = false, want true")
	}
}

// TestUnsignedConversions covers genConversion's unsigned branches:
// zero-extension when widening an unsigned source, and uitofp/fptoui for the
// int<->float crossings.
func TestUnsignedConversions(t *testing.T) {
	jm := compileAndJIT(t, `
func zextU8ToU32() bool {
	var a u8 = 200
	return u32(a) == 200
}

func uToFloat() bool {
	var a u32 = 4000000000
	return f64(a) == 4000000000.0
}

func floatToU() bool {
	var a f64 = 4000000000.0
	return u32(a) == 4000000000
}

func uToSignedWiderZeroExtends() bool {
	var a u8 = 255
	return i32(a) == 255
}
`)
	for _, name := range []string{"zextU8ToU32", "uToFloat", "floatToU", "uToSignedWiderZeroExtends"} {
		if !jm.runBool(t, name) {
			t.Errorf("%s() = false, want true", name)
		}
	}
}

// TestUnsignedTopOfRangeLiterals proves a literal at the exact maximum of each
// width compiles (ParseUint's 0..2^bits-1 range) and round-trips unchanged.
func TestUnsignedTopOfRangeLiterals(t *testing.T) {
	jm := compileAndJIT(t, `
func maxU8() bool {
	var a u8 = 255
	return a == 255
}

func maxU16() bool {
	var a u16 = 65535
	return a == 65535
}

func maxU32() u64 {
	var a u32 = 4294967295
	return u64(a)
}

func maxU64() u64 {
	var a u64 = 18446744073709551615
	return a
}
`)
	if !jm.runBool(t, "maxU8") {
		t.Error("maxU8() = false, want true")
	}
	if !jm.runBool(t, "maxU16") {
		t.Error("maxU16() = false, want true")
	}
	if got := uint64(jm.runInt64(t, "maxU32")); got != 4294967295 {
		t.Errorf("maxU32() = %d, want 4294967295", got)
	}
	if got := uint64(jm.runInt64(t, "maxU64")); got != 18446744073709551615 {
		t.Errorf("maxU64() = %d, want 18446744073709551615", got)
	}
}

// TestUnsignedVariableNegationWraps proves negating a runtime unsigned value
// is legal and wraps two's-complement (matching Go), unlike a constant.
func TestUnsignedVariableNegationWraps(t *testing.T) {
	jm := compileAndJIT(t, `
func negU32() bool {
	var x u32 = 5
	var y u32 = -x
	return y == 4294967291
}
`)
	if !jm.runBool(t, "negU32") {
		t.Error("negU32() = false, want true (-5 wraps to 4294967291 for u32)")
	}
}

// TestUnsignedMapKey covers genMapHash/genMapKeyEqual's unsigned cases - a
// map keyed on an unsigned type must insert and look up correctly.
func TestUnsignedMapKey(t *testing.T) {
	jm := compileAndJIT(t, `
func lookup() int {
	m := make(map[u32]int)
	var k u32 = 3000000000
	m[k] = 7
	return m[k]
}
`)
	if got := jm.runInt32(t, "lookup"); got != 7 {
		t.Errorf("lookup() = %d, want 7", got)
	}
}

// TestGlobalUnsignedConstantFolding covers constUnsignedBinaryExpr: a global
// initializer folded at compile time must divide unsigned (ZExtValue), not via
// the signed path a dividend above the signed max would misread.
func TestGlobalUnsignedConstantFolding(t *testing.T) {
	jm := compileAndJIT(t, `
var divided u32 = 3000000000 / 3

func getDivided() u64 {
	return u64(divided)
}
`)
	if got := uint64(jm.runInt64(t, "getDivided")); got != 1000000000 {
		t.Errorf("getDivided() = %d, want 1000000000 (unsigned fold)", got)
	}
}

// --- invalid paths ---

// TestUnsignedLiteralOutOfRangeIsCodegenError covers genUIntLit's ParseUint
// range check: a literal one past the top of each width is rejected.
func TestUnsignedLiteralOutOfRangeIsCodegenError(t *testing.T) {
	cases := []struct {
		name string
		src  string
	}{
		{"u8", "var x u8 = 256\nfunc main() {}\n"},
		{"u16", "var x u16 = 65536\nfunc main() {}\n"},
		{"u32", "var x u32 = 4294967296\nfunc main() {}\n"},
		{"u64", "var x u64 = 18446744073709551616\nfunc main() {}\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gdiags := compileSrcExpectCodegenError(t, c.src)
			if gdiags.ErrorCount() != 1 {
				t.Fatalf("ErrorCount = %d, want 1: %v", gdiags.ErrorCount(), gdiags.All())
			}
			if msg := gdiags.All()[0].Msg; !strings.Contains(msg, "out of range") {
				t.Fatalf("message = %q, want it to mention out of range", msg)
			}
		})
	}
}

// TestNegativeLiteralIntoUnsignedIsCodegenError covers both the constfold
// (global) and the runtime (local) negation-of-an-unsigned-constant paths.
func TestNegativeLiteralIntoUnsignedIsCodegenError(t *testing.T) {
	t.Run("global", func(t *testing.T) {
		gdiags := compileSrcExpectCodegenError(t, "var x u8 = -1\nfunc main() {}\n")
		if gdiags.ErrorCount() != 1 {
			t.Fatalf("ErrorCount = %d, want 1: %v", gdiags.ErrorCount(), gdiags.All())
		}
		if msg := gdiags.All()[0].Msg; !strings.Contains(msg, "negation of unsigned constant") {
			t.Fatalf("message = %q, want it to mention negation of unsigned constant", msg)
		}
	})
	t.Run("local", func(t *testing.T) {
		gdiags := compileSrcExpectCodegenError(t, "func main() {\n\tvar x u8 = -1\n\tprint(x)\n}\n")
		if gdiags.ErrorCount() != 1 {
			t.Fatalf("ErrorCount = %d, want 1: %v", gdiags.ErrorCount(), gdiags.All())
		}
		if msg := gdiags.All()[0].Msg; !strings.Contains(msg, "negation of unsigned constant") {
			t.Fatalf("message = %q, want it to mention negation of unsigned constant", msg)
		}
	})
}
