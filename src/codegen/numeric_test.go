package codegen

import (
	"strings"
	"testing"
)

// TestIntWidthArithmetic covers arithmetic across every new signed integer
// width (i8/i16/i64), not just the pre-existing i32 - the same LLVM
// instructions (CreateAdd/CreateSub/...) generalize directly across widths,
// so this mostly proves genBinaryExpr/llvmType actually wire up the correct
// LLVM type per width rather than silently defaulting everything to i32.
// i8/i16 results are asserted via a bool-returning function (comparing
// against the expected value inside the language itself), the same pattern
// this package's string tests already use, since a value narrower than i32
// has no dedicated syscall-return helper; i64 uses runInt64 directly (a
// value that doesn't fit in 32 bits proves it isn't silently truncated
// anywhere along the way).
func TestIntWidthArithmetic(t *testing.T) {
	jm := compileAndJIT(t, `
func addI8() bool {
	var a i8 = 100
	var b i8 = 20
	return a + b == 120
}

func subI8() bool {
	var a i8 = -100
	var b i8 = 20
	return a - b == -120
}

func addI16() bool {
	var a i16 = 30000
	var b i16 = 1000
	return a + b == 31000
}

func addI64() i64 {
	var a i64 = 4000000000
	var b i64 = 4000000000
	return a + b
}

func mulI64() i64 {
	var a i64 = 3000000000
	var b i64 = 2
	return a * b
}
`)
	if !jm.runBool(t, "addI8") {
		t.Error("addI8() = false, want true")
	}
	if !jm.runBool(t, "subI8") {
		t.Error("subI8() = false, want true")
	}
	if !jm.runBool(t, "addI16") {
		t.Error("addI16() = false, want true")
	}
	if got := jm.runInt64(t, "addI64"); got != 8000000000 {
		t.Errorf("addI64() = %d, want 8000000000", got)
	}
	if got := jm.runInt64(t, "mulI64"); got != 6000000000 {
		t.Errorf("mulI64() = %d, want 6000000000", got)
	}
}

// TestFloatArithmetic covers +, -, *, / on f64 (and f32, promoted through a
// var of that type) - asserted via bool-returning functions, since a float
// return/argument can't safely round-trip through this test harness's raw
// syscall.SyscallN calling convention (float results come back in an XMM
// register on this ABI, which SyscallN never reads - see AGENTS.md/
// codegen_test.go's own doc comments for the same reasoning already applied
// to string-typed results).
func TestFloatArithmetic(t *testing.T) {
	jm := compileAndJIT(t, `
func addF64() bool {
	var a f64 = 1.5
	var b f64 = 2.25
	return a + b == 3.75
}

func subF64() bool {
	var a f64 = 5.5
	var b f64 = 1.5
	return a - b == 4.0
}

func mulF64() bool {
	var a f64 = 2.5
	var b f64 = 4.0
	return a * b == 10.0
}

func divF64() bool {
	var a f64 = 9.0
	var b f64 = 2.0
	return a / b == 4.5
}

func addF32() bool {
	var a f32 = 1.5
	var b f32 = 2.5
	return a + b == 4.0
}

func negF64() bool {
	var a f64 = 3.5
	return -a == -3.5
}
`)
	tests := []string{"addF64", "subF64", "mulF64", "divF64", "addF32", "negF64"}
	for _, name := range tests {
		if !jm.runBool(t, name) {
			t.Errorf("%s() = false, want true", name)
		}
	}
}

// TestFloatComparisons covers `== != < <= > >=` on floats, lowered via the
// ordered FCmp predicates (see genFloatOrder/genBinaryExpr) - false whenever
// either operand is NaN would be the alternative wrong behavior an
// unordered predicate could produce, but there's no NaN literal syntax in
// this language to test that edge directly; this instead just covers the
// ordinary true/false cases for every operator.
func TestFloatComparisons(t *testing.T) {
	jm := compileAndJIT(t, `
func lt() bool {
	var a f64 = 1.5
	var b f64 = 2.5
	return a < b
}

func notLt() bool {
	var a f64 = 2.5
	var b f64 = 1.5
	return a < b
}

func le() bool {
	var a f64 = 2.5
	var b f64 = 2.5
	return a <= b
}

func gt() bool {
	var a f64 = 3.5
	var b f64 = 2.5
	return a > b
}

func ge() bool {
	var a f64 = 2.5
	var b f64 = 2.5
	return a >= b
}

func eq() bool {
	var a f64 = 2.5
	var b f64 = 2.5
	return a == b
}

func notEq() bool {
	var a f64 = 2.5
	var b f64 = 3.5
	return a != b
}
`)
	tests := []struct {
		name string
		want bool
	}{
		{"lt", true},
		{"notLt", false},
		{"le", true},
		{"gt", true},
		{"ge", true},
		{"eq", true},
		{"notEq", true},
	}
	for _, tc := range tests {
		if got := jm.runBool(t, tc.name); got != tc.want {
			t.Errorf("%s() = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestIntComparisons covers `< <= > >=` on plain integers (genIntOrder) -
// TestFloatComparisons above already covers all four operators for f64, but
// none of the four have ever actually been JIT-executed and asserted for an
// integer operand: every other test in this package that uses `<=`/`>`/`>=`
// on an int does so only incidentally inside a loop condition or bounds
// check, never as the directly-asserted result of the comparison itself.
func TestIntComparisons(t *testing.T) {
	jm := compileAndJIT(t, `
func lt() bool {
	var a int = 1
	var b int = 2
	return a < b
}

func notLt() bool {
	var a int = 2
	var b int = 1
	return a < b
}

func le() bool {
	var a int = 2
	var b int = 2
	return a <= b
}

func notLe() bool {
	var a int = 3
	var b int = 2
	return a <= b
}

func gt() bool {
	var a int = 3
	var b int = 2
	return a > b
}

func notGt() bool {
	var a int = 2
	var b int = 2
	return a > b
}

func ge() bool {
	var a int = 2
	var b int = 2
	return a >= b
}

func notGe() bool {
	var a int = 1
	var b int = 2
	return a >= b
}
`)
	tests := []struct {
		name string
		want bool
	}{
		{"lt", true},
		{"notLt", false},
		{"le", true},
		{"notLe", false},
		{"gt", true},
		{"notGt", false},
		{"ge", true},
		{"notGe", false},
	}
	for _, tc := range tests {
		if got := jm.runBool(t, tc.name); got != tc.want {
			t.Errorf("%s() = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestIntWidthIncDecAndCompoundAssign covers ++/--/+=/-=/*=//= generalized
// beyond plain i32 - a narrower width (i16) and a float (f64), both of which
// used to be impossible (the old codegen hard-coded i32 for ++/-- and never
// considered float instructions for the compound-assignment operators at
// all).
func TestIntWidthIncDecAndCompoundAssign(t *testing.T) {
	jm := compileAndJIT(t, `
func incI16() bool {
	var a i16 = 41
	a++
	return a == 42
}

func decI16() bool {
	var a i16 = 43
	a--
	return a == 42
}

func incF64() bool {
	var a f64 = 41.5
	a++
	return a == 42.5
}

func compoundF64() bool {
	var a f64 = 2.0
	a += 1.5
	a *= 2.0
	a -= 1.0
	a /= 2.0
	return a == 3.0
}
`)
	tests := []string{"incI16", "decI16", "incF64", "compoundF64"}
	for _, name := range tests {
		if !jm.runBool(t, name) {
			t.Errorf("%s() = false, want true", name)
		}
	}
}

// TestExplicitConversions covers T(x) numeric conversions end to end -
// widening (sext/fpext), narrowing (trunc/fptrunc), int<->float
// (sitofp/fptosi), and the same-type passthrough case (i32(someI32) should
// just be the value itself, not a pointless instruction - checked via IR
// inspection below).
func TestExplicitConversions(t *testing.T) {
	jm := compileAndJIT(t, `
func widenI32ToI64() bool {
	var a i32 = 1000000
	b := i64(a) * i64(3000)
	return b == 3000000000
}

func narrowI64ToI32() bool {
	var a i64 = 4000000005
	b := i32(a % 100)
	return b == 5
}

func narrowI32ToI8() bool {
	var a i32 = 300
	b := i8(a)
	return b == 44
}

func intToFloat() bool {
	var a i32 = 5
	b := f64(a)
	return b == 5.0
}

func floatToInt() bool {
	var a f64 = 5.9
	b := i32(a)
	return b == 5
}

func floatToIntNegative() bool {
	var a f64 = -5.9
	b := i32(a)
	return b == -5
}

func widenF32ToF64() bool {
	var a f32 = 2.5
	b := f64(a)
	return b == 2.5
}

func narrowF64ToF32() bool {
	var a f64 = 2.5
	b := f32(a)
	return b == 2.5
}

func literalConversion() bool {
	return i64(5) == 5
}

func sameTypePassthrough(x int) int {
	return int(x)
}
`)
	tests := []string{
		"widenI32ToI64",
		"narrowI64ToI32",
		"narrowI32ToI8",
		"intToFloat",
		"floatToInt",
		"floatToIntNegative",
		"widenF32ToF64",
		"narrowF64ToF32",
		"literalConversion",
	}
	for _, name := range tests {
		if !jm.runBool(t, name) {
			t.Errorf("%s() = false, want true", name)
		}
	}
	if got := jm.runInt32(t, "sameTypePassthrough", 7); got != 7 {
		t.Errorf("sameTypePassthrough(7) = %d, want 7", got)
	}
}

// TestGlobalConstantFoldingWidensCorrectly covers constfold.go's generalized
// (no-longer-i32-only) constant folding: a top-level `var`'s initializer
// using i64/f64/i8 must fold to a constant of the *correct* LLVM width, not
// a mismatched i32 the module verifier would reject (the exact regression
// risk generalizing constUnaryExpr/constBinaryExpr away from their old
// int32-only assumption introduced - see constfold.go's own doc comments).
func TestGlobalConstantFoldingWidensCorrectly(t *testing.T) {
	jm := compileAndJIT(t, `
var bigSum i64 = 4000000000 + 4000000000
var negI8 i8 = -100 - 20
var floatProduct f64 = 2.5 * 4.0

func bigSumOk() bool {
	return bigSum == 8000000000
}

func negI8Ok() bool {
	return negI8 == -120
}

func floatProductOk() bool {
	return floatProduct == 10.0
}
`)
	tests := []string{"bigSumOk", "negI8Ok", "floatProductOk"}
	for _, name := range tests {
		if !jm.runBool(t, name) {
			t.Errorf("%s() = false, want true", name)
		}
	}
}

// TestSameTypeConversionIsAPassthrough asserts int(x) where x is already int
// generates no sext/trunc/sitofp/fptosi instruction at all - just the bare
// value, per AGENTS.md's "Explicit conversions" section.
func TestSameTypeConversionIsAPassthrough(t *testing.T) {
	jm := compileAndJIT(t, `
func f(x int) int {
	return int(x)
}
`)
	ir := jm.ir
	for _, unwanted := range []string{"sext", "trunc", "sitofp", "fptosi", "fpext", "fptrunc"} {
		if strings.Contains(ir, unwanted) {
			t.Errorf("same-type conversion int(x) emitted a %q instruction, want a bare passthrough\n%s", unwanted, ir)
		}
	}
}
