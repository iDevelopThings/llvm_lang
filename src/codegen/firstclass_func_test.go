package codegen

import "testing"

// TestFuncStoredInVarAndCalledIndirectly covers the simplest first-class-
// function case: a bare function name assigned into a function-typed local,
// then called through that variable - an indirect call (see CODEGEN.md's
// "First-class functions" section and genIndirectCall in expr.go), not the
// zero-overhead direct `call` a plain `add(...)` compiles to.
func TestFuncStoredInVarAndCalledIndirectly(t *testing.T) {
	jm := compileAndJIT(t, `
func add(x int, y int) int {
	return x + y
}

func callThroughVar(a int, b int) int {
	fn := add
	return fn(a, b)
}
`)

	if got := jm.runInt32(t, "callThroughVar", 3, 4); got != 7 {
		t.Errorf("callThroughVar(3, 4) = %d, want 7", got)
	}
}

// TestFuncPassedAsArgumentAndCalledInside covers a function value passed as
// an ordinary call argument (a function-typed parameter) and called from
// inside the callee - the same indirect-call machinery, this time through a
// parameter rather than a local variable.
func TestFuncPassedAsArgumentAndCalledInside(t *testing.T) {
	jm := compileAndJIT(t, `
func inc(x int) int {
	return x + 1
}

func double(x int) int {
	return x * 2
}

func apply(fn func(int) int, x int) int {
	return fn(x)
}

func applyInc(x int) int {
	return apply(inc, x)
}

func applyDouble(x int) int {
	return apply(double, x)
}
`)

	if got := jm.runInt32(t, "applyInc", 5); got != 6 {
		t.Errorf("applyInc(5) = %d, want 6", got)
	}
	if got := jm.runInt32(t, "applyDouble", 5); got != 10 {
		t.Errorf("applyDouble(5) = %d, want 10", got)
	}
}

// TestFuncReturnedFromFuncAndCalled covers a function returned as a value
// from another function, then called after the fact. The choice of which
// function to return is embedded as a source-level bool literal (matching
// this suite's established convention - see compileAndJIT's own doc
// comment) rather than passed as a real bool argument through the raw
// syscall test harness, which no existing test in this package does.
func TestFuncReturnedFromFuncAndCalled(t *testing.T) {
	jm := compileAndJIT(t, `
func inc(x int) int {
	return x + 1
}

func dec(x int) int {
	return x - 1
}

func pick(useInc bool) func(int) int {
	if useInc {
		return inc
	}
	return dec
}

func runInc(x int) int {
	fn := pick(true)
	return fn(x)
}

func runDec(x int) int {
	fn := pick(false)
	return fn(x)
}
`)

	if got := jm.runInt32(t, "runInc", 10); got != 11 {
		t.Errorf("runInc(10) = %d, want 11", got)
	}
	if got := jm.runInt32(t, "runDec", 10); got != 9 {
		t.Errorf("runDec(10) = %d, want 9", got)
	}
}

// TestChainedCallOnFuncReturningFunc covers calling a function-returning
// call's own result immediately (`getAdder()(...)`), without an
// intermediate variable - the same indirect-call path, just with the
// callee itself being another CallExpr rather than a plain Ident.
func TestChainedCallOnFuncReturningFunc(t *testing.T) {
	jm := compileAndJIT(t, `
func addFive(x int) int {
	return x + 5
}

func getAdder() func(int) int {
	return addFive
}

func chained(x int) int {
	return getAdder()(x)
}
`)

	if got := jm.runInt32(t, "chained", 10); got != 15 {
		t.Errorf("chained(10) = %d, want 15", got)
	}
}

// TestDirectCallStillCompilesToPlainCall is a regression guard for the
// zero-overhead requirement: a statically-known function name called
// directly (`add(...)`) must still compile without ever touching the
// fat-pointer/indirect-call machinery - this is really just an ordinary
// direct-call test (already covered elsewhere, e.g. control_flow_test.go's
// TestRecursion), included here as an explicit regression guard living
// alongside the new indirect-call tests, so a future change to
// genCallExpr's dispatch that broke direct calls would be caught in the
// same file as the feature that risked it.
func TestDirectCallStillCompilesToPlainCall(t *testing.T) {
	jm := compileAndJIT(t, `
func add(x int, y int) int {
	return x + y
}

func callDirect(a int, b int) int {
	return add(a, b)
}
`)

	if got := jm.runInt32(t, "callDirect", 3, 4); got != 7 {
		t.Errorf("callDirect(3, 4) = %d, want 7", got)
	}
}

// TestFuncValueAssignedThenReassigned covers a function-typed local
// reassigned to a different function value partway through, verifying each
// call sees whichever function was most recently stored.
func TestFuncValueAssignedThenReassigned(t *testing.T) {
	jm := compileAndJIT(t, `
func inc(x int) int {
	return x + 1
}

func dec(x int) int {
	return x - 1
}

func reassign(x int) int {
	fn := inc
	first := fn(x)
	fn = dec
	second := fn(x)
	return first + second
}
`)

	if got := jm.runInt32(t, "reassign", 10); got != 20 {
		t.Errorf("reassign(10) = %d, want 20 (11 + 9)", got)
	}
}
