package codegen

import (
	"strings"
	"testing"
)

// --- collect form: proves the variadic parameter is an ordinary []T inside
// the function body, and that call-site collection actually builds a real
// backing buffer, not just "compiles" ---

// TestVariadicCollectSumsRealCollectedValues JIT-executes a variadic
// function that ranges over its own `...int` parameter exactly like an
// ordinary []int (len/range - see LANGUAGE.md's "Variadic parameters"
// section), called with zero, one, and several collected arguments, and
// asserts the real, numeric result each time - not merely that it compiles.
func TestVariadicCollectSumsRealCollectedValues(t *testing.T) {
	jm := compileAndJIT(t, `
func Sum(nums ...int) int {
	total := 0
	for i := range nums {
		total = total + nums[i]
	}
	return total
}

func callZero() int { return Sum() }
func callOne() int { return Sum(5) }
func callSeveral() int { return Sum(1, 2, 3, 4) }
`)
	if got := jm.runInt32(t, "callZero"); got != 0 {
		t.Errorf("Sum() = %d, want 0", got)
	}
	if got := jm.runInt32(t, "callOne"); got != 5 {
		t.Errorf("Sum(5) = %d, want 5", got)
	}
	if got := jm.runInt32(t, "callSeveral"); got != 10 {
		t.Errorf("Sum(1, 2, 3, 4) = %d, want 10", got)
	}
}

// TestVariadicCollectWithFixedLeadingParam covers a variadic parameter
// combined with fixed leading parameters (`func F(scale int, nums ...int)`),
// collecting only the trailing arguments.
func TestVariadicCollectWithFixedLeadingParam(t *testing.T) {
	jm := compileAndJIT(t, `
func ScaledSum(scale int, nums ...int) int {
	total := 0
	for i := range nums {
		total = total + nums[i]
	}
	return total * scale
}

func f() int {
	return ScaledSum(10, 1, 2, 3)
}
`)
	if got := jm.runInt32(t, "f"); got != 60 {
		t.Errorf("ScaledSum(10, 1, 2, 3) = %d, want 60", got)
	}
}

// --- spread form: forwards an existing []T directly, no re-collection ---

// TestVariadicSpreadPassesExistingSliceThrough JIT-executes a spread call,
// proving the callee receives the exact same element values an equivalent
// collect call would (built here via make + indexed writes, so this doesn't
// depend on slice-literal codegen to prove the point).
func TestVariadicSpreadPassesExistingSliceThrough(t *testing.T) {
	jm := compileAndJIT(t, `
func Sum(nums ...int) int {
	total := 0
	for i := range nums {
		total = total + nums[i]
	}
	return total
}

func f() int {
	nums := make([]int, 3)
	nums[0] = 10
	nums[1] = 20
	nums[2] = 30
	return Sum(nums...)
}
`)
	if got := jm.runInt32(t, "f"); got != 60 {
		t.Errorf("Sum(nums...) = %d, want 60", got)
	}
}

// TestVariadicSpreadEmptySlice covers the empty-spread edge: spreading a
// zero-length []int must behave exactly like an empty collect call.
func TestVariadicSpreadEmptySlice(t *testing.T) {
	jm := compileAndJIT(t, `
func Sum(nums ...int) int {
	total := 0
	for i := range nums {
		total = total + nums[i]
	}
	return total
}

func f() int {
	nums := make([]int, 0)
	return Sum(nums...)
}
`)
	if got := jm.runInt32(t, "f"); got != 0 {
		t.Errorf("Sum(nums...) with an empty slice = %d, want 0", got)
	}
}

// --- a method with a variadic last parameter ---

func TestVariadicMethodRealResult(t *testing.T) {
	jm := compileAndJIT(t, `
struct Accumulator {
	base int
}

func (Accumulator) AddAll(nums ...int) int {
	total := this.base
	for i := range nums {
		total = total + nums[i]
	}
	return total
}

func f() int {
	a := Accumulator{100}
	return a.AddAll(1, 2, 3)
}
`)
	if got := jm.runInt32(t, "f"); got != 106 {
		t.Errorf("a.AddAll(1, 2, 3) = %d, want 106", got)
	}
}

// --- a generic function with a variadic last parameter ---

// TestVariadicGenericFuncRealResult JIT-executes `func F[T](items ...T)`
// instantiated at two different concrete types from two different call
// sites, proving monomorphization and variadic collection compose correctly.
func TestVariadicGenericFuncRealResult(t *testing.T) {
	jm := compileAndJIT(t, `
func First[T](items ...T) T {
	return items[0]
}

func callInt() int { return First(7, 8, 9) }
func callBool() bool { return First(true, false) }
`)
	if got := jm.runInt32(t, "callInt"); got != 7 {
		t.Errorf("First(7, 8, 9) = %d, want 7", got)
	}
	if got := jm.runBool(t, "callBool"); got != true {
		t.Errorf("First(true, false) = %v, want true", got)
	}
}

// --- confirms the "no function-body codegen changes" design claim ---

// TestVariadicFuncDeclaresIdenticalSignatureToPlainSliceParam is the direct
// proof for AGENTS.md's own review-process concern: a variadic function's
// own declared LLVM signature/body must be indistinguishable from an
// ordinary function whose last parameter happens to be []T - only the call
// site should need new codegen. Two functions, differing only in
// `...int` vs `[]int`, must emit the exact same `define` line shape (aside
// from their own names) - if the variadic one needed any real declaration-
// level special-casing, this would show up as a divergence here.
func TestVariadicFuncDeclaresIdenticalSignatureToPlainSliceParam(t *testing.T) {
	jm := compileAndJIT(t, `
func Variadic(scale int, nums ...int) int {
	return scale
}

func Plain(scale int, nums []int) int {
	return scale
}
`)
	variadicSig := defineLineSignature(t, jm.ir, "Variadic")
	plainSig := defineLineSignature(t, jm.ir, "Plain")
	if variadicSig != plainSig {
		t.Fatalf("variadic func's own declared signature = %q, plain []int-param func's = %q - want identical parameter shapes", variadicSig, plainSig)
	}
}

// defineLineSignature extracts fnName's own `define ...(...)` parameter-list
// text out of ir, for comparing two functions' declared signatures
// independent of their own names.
func defineLineSignature(t *testing.T, ir, fnName string) string {
	t.Helper()
	marker := "@" + fnName + "("
	i := strings.Index(ir, marker)
	if i < 0 {
		t.Fatalf("no `define` for %s found in IR:\n%s", fnName, ir)
	}
	rest := ir[i+len(marker):]
	j := strings.Index(rest, ")")
	if j < 0 {
		t.Fatalf("unterminated parameter list for %s in IR:\n%s", fnName, ir)
	}
	return rest[:j]
}

// --- a real string result, not just numeric ---

// TestVariadicStringConcatenationRealResult proves the variadic feature
// isn't string-special-cased in the other direction either: a variadic
// []string parameter concatenated inside the body and printed, asserting
// the exact captured output - the "real string result" case AGENTS.md's own
// review process calls for, not just a numeric one.
func TestVariadicStringConcatenationRealResult(t *testing.T) {
	jm := compileAndJIT(t, `
func Concat(parts ...string) string {
	result := ""
	for i := range parts {
		result = result + parts[i]
	}
	return result
}

func main() {
	print(Concat("a", "b", "c"))
}
`)
	out := captureStdout(t, func() {
		jm.runInt32(t, "main")
	})
	want := "abc\n"
	if out != want {
		t.Fatalf("print(Concat(\"a\", \"b\", \"c\")) captured stdout = %q, want %q", out, want)
	}
}
