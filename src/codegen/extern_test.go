package codegen

import (
	"strings"
	"testing"
)

// --- `extern func` FFI declarations (see LANGUAGE.md's "External functions
// (FFI)" section) ---

// TestExternFuncDirectCallJIT covers the full pipeline end to end: an extern
// func bound to libc's own `abs` (always linked, deterministic, and already
// loaded into this test process - no timing dependency the way this
// feature's own real-world motivating case, QueryPerformanceCounter, would
// have) is called directly from main, its result used in an ordinary
// expression (not just as a bare statement) and returned as main's own exit
// code - proving isDirectFuncCall/genFuncCall's g.funcs-keyed dispatch
// genuinely needs no changes at all for an ExternFuncDecl-backed symbol (see
// declareExternFuncSignature's own doc comment): main's result comes back
// through the exact same syscall-return path an ordinary direct call would.
func TestExternFuncDirectCallJIT(t *testing.T) {
	jm := compileAndJIT(t, `
extern func abs(x i32) i32

func main() int {
	y := abs(-5) + 1
	return y
}
`)
	if got := jm.runInt32(t, "main"); got != 6 {
		t.Errorf("main() = %d, want 6 (abs(-5) + 1)", got)
	}
}

// TestExternFuncBareStatementResultIgnored covers calling an extern func as
// a bare ExprStmt, discarding its result - the exact shape this feature's own
// worked example (examples/scope_timer) uses for QueryPerformanceCounter -
// proving codegen doesn't choke on a non-void CallExpr whose value is never
// consumed.
func TestExternFuncBareStatementResultIgnored(t *testing.T) {
	jm := compileAndJIT(t, `
extern func abs(x i32) i32

func main() int {
	abs(-5)
	return 7
}
`)
	if got := jm.runInt32(t, "main"); got != 7 {
		t.Errorf("main() = %d, want 7", got)
	}
}

// TestExternFuncVoidCallJIT covers an extern func declaring no return type at
// all (a void C function) - lowered exactly like an ordinary void
// FuncDecl/method call.
func TestExternFuncVoidCallJIT(t *testing.T) {
	jm := compileAndJIT(t, `
extern func abs(x i32)

func main() int {
	abs(-5)
	return 3
}
`)
	if got := jm.runInt32(t, "main"); got != 3 {
		t.Errorf("main() = %d, want 3", got)
	}
}

// TestExternFuncPointerArgJIT covers the motivating pointer-argument shape
// this feature exists for (see LANGUAGE.md's "External functions (FFI)"
// section and examples/scope_timer): an extern func taking a pointer
// parameter, called with `&local` - address-of a local variable, an ordinary,
// already-legal expression this feature adds no new handling for of its own.
// Declares (and actually calls) the exact QueryPerformanceCounter shape the
// real worked example uses; the JIT's already-registered process-symbol
// generator (see cmd/llvmc/main.go's bindMinGWMainThunk, mirrored by this
// package's own test helper of the same name) resolves it against the real
// kernel32.dll export loaded into this test process on Windows, so this is a
// genuine end-to-end call, not merely a "does it compile" check.
func TestExternFuncPointerArgJIT(t *testing.T) {
	jm := compileAndJIT(t, `
extern func QueryPerformanceCounter(counter *i64) bool

func main() int {
	x := i64(0)
	QueryPerformanceCounter(&x)
	return 9
}
`)
	if got := jm.runInt32(t, "main"); got != 9 {
		t.Errorf("main() = %d, want 9", got)
	}
}

// TestExternFuncDeclaredWithDefaultLinkage covers declareExternFuncSignature
// declaring its LLVM function with default linkage (not private) - unlike
// this package's own internal never-called-directly helpers (e.g.
// llvm_lang.arena_alloc in runtime.go), an extern func must resolve as a
// genuine external symbol the JIT's process-symbol generator can bind
// against a real DLL export, which a private-linkage declaration never
// would.
func TestExternFuncDeclaredWithDefaultLinkage(t *testing.T) {
	jm := compileAndJIT(t, `
extern func abs(x i32) i32

func main() int {
	return abs(-1)
}
`)
	// A private-linkage declaration would read `declare private i32 @abs(...)`
	// (see e.g. this same module's own llvm_lang.arena_alloc, which does carry
	// that keyword) - asserting the exact, keyword-free declaration line is a
	// tighter check than merely searching the whole module for the word
	// "private" (which legitimately appears elsewhere, on this package's own
	// internal helpers/globals).
	if !strings.Contains(jm.ir, "declare i32 @abs(i32)") {
		t.Errorf("expected a plain default-linkage `declare i32 @abs(i32)`, got:\n%s", jm.ir)
	}
}
