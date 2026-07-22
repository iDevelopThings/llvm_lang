package codegen

import "testing"

// TestIfForArithmetic covers if/else, all three for-loop forms' shared
// lowering, and a mix of eager binary operators, JIT-executed and checked
// against a value computed independently in this comment:
//
//	total=0
//	i=0 even -> total = add(total, 0)      = 0
//	i=1 odd  -> total += 1*2               = 2
//	i=2 even -> total = add(total, 2)      = 4
//	i=3 odd  -> total += 3*2               = 10
//	i=4 even -> total = add(total, 4)      = 14
//	i=5 odd  -> total += 5*2               = 24
//	i=6 even -> total = add(total, 6)      = 30
//	i=7 odd  -> total += 7*2               = 44
//	i=8 even -> total = add(total, 8)      = 52
//	i=9 odd  -> total += 9*2               = 70
func TestIfForArithmetic(t *testing.T) {
	jm := compileAndJIT(t, `
func add(x int, y int) int {
	return x + y
}

func main() int {
	total := 0
	for i := 0; i < 10; i++ {
		if i % 2 == 0 {
			total = add(total, i)
		} else {
			total += i * 2
		}
	}
	return total
}
`)

	if got := jm.runInt32(t, "main"); got != 70 {
		t.Errorf("main() = %d, want 70", got)
	}
}

// TestRecursion covers a recursive function call, JIT-executed directly
// (not through main) with an explicit argument.
func TestRecursion(t *testing.T) {
	jm := compileAndJIT(t, `
func fib(n int) int {
	if n < 2 {
		return n
	}
	return fib(n - 1) + fib(n - 2)
}
`)

	if got := jm.runInt32(t, "fib", 10); got != 55 {
		t.Errorf("fib(10) = %d, want 55", got)
	}
}

// TestBreakContinue covers break/continue branch-target tracking through a
// single loop:
//
//	i=0 even -> continue
//	i=1 odd  -> total += 1 = 1
//	i=2 even -> continue
//	i=3 odd  -> total += 3 = 4
//	i=4 even -> continue
//	i=5      -> break
func TestBreakContinue(t *testing.T) {
	jm := compileAndJIT(t, `
func sumSkip(n int) int {
	total := 0
	for i := 0; i < n; i++ {
		if i == 5 {
			break
		}
		if i % 2 == 0 {
			continue
		}
		total += i
	}
	return total
}
`)

	if got := jm.runInt32(t, "sumSkip", 10); got != 4 {
		t.Errorf("sumSkip(10) = %d, want 4", got)
	}
}

// TestNestedLoopsBreakOnlyInnermost covers break/continue tracking through
// *nested* loops - break/continue must target the innermost loop, not
// whichever loop started the loopStack.
//
//	outer i=0: inner j=0,1 (break at j==2) -> total += 0+1 = 1
//	outer i=1: inner j=0,1 (break at j==2) -> total += 0+1 = 1 (running 2)
//	outer i=2: inner j=0,1 (break at j==2) -> total += 0+1 = 1 (running 3)
func TestNestedLoopsBreakOnlyInnermost(t *testing.T) {
	jm := compileAndJIT(t, `
func nested() int {
	total := 0
	for i := 0; i < 3; i++ {
		for j := 0; j < 5; j++ {
			if j == 2 {
				break
			}
			total += j
		}
	}
	return total
}
`)

	if got := jm.runInt32(t, "nested"); got != 3 {
		t.Errorf("nested() = %d, want 3", got)
	}
}

// Note: break/continue outside a loop used to be caught only here, at
// codegen (see BLOCKERS.md's codegen-phase entry #6) - Resolve/Check didn't
// validate loop placement at all. sema.Check now does (see
// checkBreakOrContinue in sema/typecheck.go and
// TestBreakOutsideLoopIsError/TestContinueOutsideLoopIsError in
// sema/typecheck_test.go), so a program like this never reaches codegen at
// all anymore - genBreakStmt/genContinueStmt's own loopStack-empty case is
// now an unreachable-on-a-valid-tree panic instead (see stmt.go), matching
// how every other already-invalid-input case in this package is handled.

// TestInfiniteForAsLastStatementVerifies covers the interaction between
// sema's new "missing return" flow analysis (an infinite `for {}` with no
// break that targets it directly is a terminating statement - see
// isTerminatingStmt in sema/typecheck.go) and codegen: a non-void function
// ending in one is now legal per sema (it's never checked for a `return`
// falling off the end), so codegen must still produce a verifiably valid
// module for it. It's deliberately not JIT-*executed* here - the loop really
// is infinite - only compiled and verified (see compileSrc), which already
// runs llvm.VerifyModule.
func TestInfiniteForAsLastStatementVerifies(t *testing.T) {
	compileSrc(t, `
func f() int {
	for {
	}
}
`)
}

// TestBareReturnInVoidFunction covers genReturnStmt's own bare-`return`
// branch (no value) - every other test in this package that reaches
// genReturnStmt does so via `return <expr>`; a plain `return` with nothing
// after it, ending a void function early, has never actually been
// JIT-executed here before. Distinct from TestMainWithoutReturnGetsExitCodeZero
// (globals_test.go), which covers falling off main's end without ANY return
// statement at all, not an explicit bare one.
func TestBareReturnInVoidFunction(t *testing.T) {
	jm := compileAndJIT(t, `
var reached int = 0

func setIfPositive(x int) {
	if x <= 0 {
		return
	}
	reached = 1
}

func run(x int) int {
	setIfPositive(x)
	return reached
}
`)
	if got := jm.runInt32(t, "run", 0); got != 0 {
		t.Errorf("run(0) = %d, want 0 (bare return should skip the rest of the function)", got)
	}
	if got := jm.runInt32(t, "run", 5); got != 1 {
		t.Errorf("run(5) = %d, want 1", got)
	}
}

// TestBareReturnInMain covers genReturnStmt's main-specific bare-return
// branch: main declares no return type here (a bare `return` is only legal
// at all in a function declaring none - see sema's
// TestBareReturnRequiresNoDeclaredReturnType), but main is still special:
// it must produce the real `ret i32 0` exit code every main needs, not
// `ret void` like any other bare-return void function (see
// TestBareReturnInVoidFunction above, which exercises that `ret void`
// branch for a genuinely non-main function).
func TestBareReturnInMain(t *testing.T) {
	jm := compileAndJIT(t, `
func main() {
	return
}
`)
	if got := jm.runInt32(t, "main"); got != 0 {
		t.Errorf("main() = %d, want 0", got)
	}
}

// TestShortCircuitSkipsRightOperand proves `&&`/`||` really short-circuit
// (genShortCircuit's basic-block branching), not an eager bitwise AND/OR:
// sideEffect's visible side effect (incrementing a global) must not happen
// when the left operand alone already decides the result, verified through
// a real JIT-executed return value (a global counter read back afterward),
// not by trying to capture the process's console output.
func TestShortCircuitSkipsRightOperand(t *testing.T) {
	jm := compileAndJIT(t, `
var calls int = 0

func sideEffect() bool {
	calls = calls + 1
	return true
}

func callCount() int {
	return calls
}

func shortCircuitAnd() bool {
	f := false
	return f && sideEffect()
}

func shortCircuitOr() bool {
	tr := true
	return tr || sideEffect()
}
`)

	if got := jm.runBool(t, "shortCircuitAnd"); got != false {
		t.Fatalf("shortCircuitAnd() = %v, want false", got)
	}
	if got := jm.runInt32(t, "callCount"); got != 0 {
		t.Errorf("callCount() after && = %d, want 0 (sideEffect must not have run)", got)
	}

	if got := jm.runBool(t, "shortCircuitOr"); got != true {
		t.Fatalf("shortCircuitOr() = %v, want true", got)
	}
	if got := jm.runInt32(t, "callCount"); got != 0 {
		t.Errorf("callCount() after || = %d, want 0 (sideEffect must not have run)", got)
	}
}
