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
// negative literals in a global initializer - the only kind of initializer
// this package accepts (see AGENTS.md's codegen section / genGlobalVarDecl).
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

// TestGlobalNonConstantInitializerIsCodegenError covers the decision
// documented in AGENTS.md/BLOCKERS.md: a top-level var's initializer must be
// a compile-time constant. sema itself has no opinion on this (calling a
// function to initialize a global type-checks fine), so this is a
// codegen-level diagnostic, not a parse/sema one.
func TestGlobalNonConstantInitializerIsCodegenError(t *testing.T) {
	gdiags := compileSrcExpectCodegenError(t, `
func f() int {
	return 5
}

var x int = f()

func main() {
}
`)
	if gdiags.ErrorCount() != 1 {
		t.Fatalf("ErrorCount = %d, want 1: %v", gdiags.ErrorCount(), gdiags.All())
	}
}

// TestGlobalDynamicArrayLiteralInitializerIsCodegenError covers
// constfold.go's constCompositeLit rejecting a dynamic-array composite
// literal (`[]T{...}`) as a global initializer: unlike a fixed-size array
// literal (a plain LLVM ConstArray, no allocation needed), a slice literal
// always needs a real runtime heap allocation (the arena - see
// LANGUAGE.md's "Dynamic arrays" section), which a global initializer can
// never provide (see this test's TestGlobalNonConstantInitializerIsCodegenError
// sibling above for the general rule this is a specific instance of). sema
// itself type-checks a dynamic-array-typed global fine (see
// sema.TestDynamicArrayTypeChecksFine); this is purely a codegen-level
// restriction.
func TestGlobalDynamicArrayLiteralInitializerIsCodegenError(t *testing.T) {
	gdiags := compileSrcExpectCodegenError(t, `
var s []int = []int{1, 2, 3}

func main() {
}
`)
	if gdiags.ErrorCount() != 1 {
		t.Fatalf("ErrorCount = %d, want 1: %v", gdiags.ErrorCount(), gdiags.All())
	}
	found := false
	for _, d := range gdiags.All() {
		if d.Msg == "global initializer must be a compile-time constant expression" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a diagnostic \"global initializer must be a compile-time constant expression\", got: %v", gdiags.All())
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
