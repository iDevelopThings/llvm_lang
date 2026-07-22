package codegen

import "testing"

// TestGlobalsAndMain covers global var declarations plus a real main entry
// point: main is the actual LLVM function named "main", returning a real
// i32 exit code computed from two top-level globals.
func TestGlobalsAndMain(t *testing.T) {
	jm := compileAndJIT(t, `
var a int = 5
var b int = 10

func main() int {
	c := a + b
	return c
}
`)
	if got := jm.runInt32(t, "main"); got != 15 {
		t.Errorf("main() = %d, want 15", got)
	}
}

// TestGlobalConstantFolding covers arithmetic constant folding (int) and
// negative literals in a global initializer - still folded entirely at
// compile time (constfold.go's constExpr), unchanged by the non-constant
// global initializer feature below (see TestGlobalNonConstantInitializerFromFunctionCall
// and friends) - a regression check that the existing compile-time path
// keeps working exactly as before.
func TestGlobalConstantFolding(t *testing.T) {
	jm := compileAndJIT(t, `
var x int = 2 + 3 * 4
var y int = -7
var z bool = 1 < 2 && 2 < 3

func main() int {
	r := x + y
	if z {
		r = r + 100
	}
	return r
}
`)
	// x = 2 + 12 = 14, y = -7, r = 7, z true -> r = 107
	if got := jm.runInt32(t, "main"); got != 107 {
		t.Errorf("main() = %d, want 107", got)
	}
}

// TestGlobalNonConstantInitializerFromFunctionCall covers the feature
// documented in LANGUAGE.md/CODEGEN.md's "Global var initializers" sections:
// a top-level var's initializer can now be an arbitrary expression, not just
// a compile-time constant - here, a call to a free function. The call must
// actually run before main (via the synthesized init function registered
// into @llvm.global_ctors - see globalinit.go), not just type-check.
func TestGlobalNonConstantInitializerFromFunctionCall(t *testing.T) {
	jm := compileAndJIT(t, `
func computeX() int {
	return 21 * 2
}

var x int = computeX()

func main() int {
	return x
}
`)
	if got := jm.runInt32(t, "main"); got != 42 {
		t.Errorf("main() = %d, want 42", got)
	}
}

// TestGlobalNonConstantInitializerReferencingAnotherGlobal covers a
// non-constant global initializer that reads another, already
// (compile-time-constant-)initialized global - the exact "reference to
// another variable" case constExpr always used to reject outright.
func TestGlobalNonConstantInitializerReferencingAnotherGlobal(t *testing.T) {
	jm := compileAndJIT(t, `
var base int = 10
var derived int = base + 5

func main() int {
	return derived
}
`)
	if got := jm.runInt32(t, "main"); got != 15 {
		t.Errorf("main() = %d, want 15", got)
	}
}

// TestGlobalNonConstantInitializersRunInDeclarationOrder covers this round's
// deliberately scoped ordering rule (CODEGEN.md's "Global var initializers"
// section / DECISIONS.md): every non-constant global's initializer runs in
// source declaration order, not a full dependency-graph topological sort - a
// global's initializer referencing another global declared *later* in the
// same file sees only that other global's zero value, not whatever its own
// initializer would eventually compute. `second` is declared (and so
// initialized) before `third`, which reads it once it's already 2; `first`
// reads `third` before `third` has run at all, so it observes 0, not 3.
func TestGlobalNonConstantInitializersRunInDeclarationOrder(t *testing.T) {
	jm := compileAndJIT(t, `
func identity(v int) int {
	return v
}

var first int = identity(third)
var second int = identity(1) + 1
var third int = identity(second) + 1

func main() int {
	return first*100 + second*10 + third
}
`)
	// first reads third before third's own initializer has run -> 0.
	// second = 1 + 1 = 2. third = second(2) + 1 = 3.
	if got := jm.runInt32(t, "main"); got != 23 {
		t.Errorf("main() = %d, want 23 (first=0, second=2, third=3)", got)
	}
}

// TestGlobalDynamicArrayLiteralInitializer covers a dynamic-array (slice)
// composite literal as a global initializer - constExpr has always rejected
// this outright (a slice literal needs a real runtime heap allocation via the
// arena - see LANGUAGE.md's "Dynamic arrays" section - which no compile-time
// constant can ever provide), but it's exactly the kind of non-constant
// initializer the synthesized init function now handles like any other real
// expression.
func TestGlobalDynamicArrayLiteralInitializer(t *testing.T) {
	jm := compileAndJIT(t, `
var s []int = []int{1, 2, 3}

func sumAndLen() int {
	return s[0] + s[1] + s[2] + len(s)
}
`)
	if got := jm.runInt32(t, "sumAndLen"); got != 9 {
		t.Errorf("sumAndLen() = %d, want 9 (1+2+3+3)", got)
	}
}

// TestGlobalNonConstantInitializerCodegenStillErrorsOnGenuineConstantError
// covers the one class of codegen-only diagnostic that survives this round:
// isConstFoldable structurally recognizes `5 / 0` as constant-*shaped*
// (both operands are literals), so it's still routed through constExpr, not
// silently deferred to the init function as if it depended on something
// non-constant - and constExpr itself still rejects a literal division by
// zero as a real error, exactly as it always has.
func TestGlobalNonConstantInitializerCodegenStillErrorsOnGenuineConstantError(t *testing.T) {
	gdiags := compileSrcExpectCodegenError(t, `
var x int = 5 / 0

func main() {
}
`)
	if gdiags.ErrorCount() != 1 {
		t.Fatalf("ErrorCount = %d, want 1: %v", gdiags.ErrorCount(), gdiags.All())
	}
	found := false
	for _, d := range gdiags.All() {
		if d.Msg == "division by zero in constant expression" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a diagnostic \"division by zero in constant expression\", got: %v", gdiags.All())
	}
}

// TestMainWithoutReturnGetsExitCodeZero covers the fallback-terminator
// policy for main specifically: a void main still needs a real `ret i32 0`
// at the LLVM level (see func.go's emitFallbackTerminator), never
// `unreachable`.
func TestMainWithoutReturnGetsExitCodeZero(t *testing.T) {
	jm := compileAndJIT(t, `
func main() {
	a := 1
	b := 2
	c := a + b
	c = c + 1
}
`)
	if got := jm.runInt32(t, "main"); got != 0 {
		t.Errorf("main() = %d, want 0", got)
	}
}
