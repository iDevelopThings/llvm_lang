package codegen

import "testing"

// TestClosureCapturesByReferenceAcrossCalls is this feature's headline case
// (see LANGUAGE.md's "Lambdas" section): a lambda closing over a local
// variable of its declaring function, mutating it across multiple separate
// calls to the returned closure - proving real by-reference capture (a
// persisting, shared storage location), not a per-construction snapshot.
// Everything lives inside the compiled source itself (runCounter calls the
// closure three times and folds the three results into one decimal number)
// since this suite's JIT harness calls a function by name via a raw
// syscall - see compileAndJIT's own doc comment - not by invoking an
// anonymous, dynamically-named lambda function directly from Go.
func TestClosureCapturesByReferenceAcrossCalls(t *testing.T) {
	jm := compileAndJIT(t, `
func makeCounter() func() int {
	count := 0
	increment := func() int {
		count = count + 1
		return count
	}
	return increment
}

func runCounter() int {
	next := makeCounter()
	a := next()
	b := next()
	c := next()
	return a*100 + b*10 + c
}
`)

	if got := jm.runInt32(t, "runCounter"); got != 123 {
		t.Errorf("runCounter() = %d, want 123 (1, 2, 3 across three calls)", got)
	}
}

// TestTwoIndependentClosuresDoNotShareState covers that two separate calls to
// makeCounter each get their own, independent captured storage - proving
// each closure's capture-context allocation is genuinely per-invocation, not
// accidentally shared module-global state.
func TestTwoIndependentClosuresDoNotShareState(t *testing.T) {
	jm := compileAndJIT(t, `
func makeCounter() func() int {
	count := 0
	increment := func() int {
		count = count + 1
		return count
	}
	return increment
}

func runTwoCounters() int {
	first := makeCounter()
	second := makeCounter()
	a := first()
	a = first()
	b := second()
	return a*10 + b
}
`)

	if got := jm.runInt32(t, "runTwoCounters"); got != 21 {
		t.Errorf("runTwoCounters() = %d, want 21 (first reaches 2, second reaches 1)", got)
	}
}

// TestLambdaCapturesParameterByReference covers capturing a *parameter* (not
// just a local var) - makeAdder's own `base` parameter, read (never written)
// by the returned lambda.
func TestLambdaCapturesParameterByReference(t *testing.T) {
	jm := compileAndJIT(t, `
func makeAdder(base int) func(int) int {
	return func(x int) int {
		return base + x
	}
}

func runAdder(base int, x int) int {
	add := makeAdder(base)
	return add(x)
}
`)

	if got := jm.runInt32(t, "runAdder", 10, 5); got != 15 {
		t.Errorf("runAdder(10, 5) = %d, want 15", got)
	}
}

// TestLambdaPassedAsArgumentAndCalledInside covers a lambda literal passed
// directly as a call argument (a function-typed parameter) and invoked from
// inside the callee - the same shape TestFuncPassedAsArgumentAndCalledInside
// (firstclass_func_test.go) already covers for a bare function reference,
// now for a genuine literal instead.
func TestLambdaPassedAsArgumentAndCalledInside(t *testing.T) {
	jm := compileAndJIT(t, `
func applyLambda(fn func(int) int, x int) int {
	return fn(x)
}

func testPassLambda(x int) int {
	return applyLambda(func(v int) int {
		return v * 3
	}, x)
}
`)

	if got := jm.runInt32(t, "testPassLambda", 4); got != 12 {
		t.Errorf("testPassLambda(4) = %d, want 12", got)
	}
}

// TestImmediatelyInvokedFuncLit covers calling a function-literal expression
// directly, with no intermediate variable at all - the parenthesized-IIFE
// shape (see LANGUAGE.md's "Lambdas" section).
func TestImmediatelyInvokedFuncLit(t *testing.T) {
	jm := compileAndJIT(t, `
func testIIFE() int {
	return (func() int {
		return 42
	})()
}
`)

	if got := jm.runInt32(t, "testIIFE"); got != 42 {
		t.Errorf("testIIFE() = %d, want 42", got)
	}
}

// TestTwoLevelNestedClosureCapture covers a variable captured across two
// enclosing function levels (see sema/lambda_test.go's
// TestTwoLevelNestedCaptureRelaysThroughBothLambdas for the sema-level half
// of this same scenario): outerFunc declares x; lambda1 (declared directly
// inside outerFunc) itself declares and returns lambda2, which is the one
// that actually reads x. This exercises the relay codegen path directly -
// lambda1's own generated function must forward x's address into lambda2's
// own capture context, even though lambda1's own body never reads x itself.
func TestTwoLevelNestedClosureCapture(t *testing.T) {
	jm := compileAndJIT(t, `
func outerFunc() func() func() int {
	x := 99
	lambda1 := func() func() int {
		lambda2 := func() int {
			return x
		}
		return lambda2
	}
	return lambda1
}

func runNested() int {
	lambda1 := outerFunc()
	lambda2 := lambda1()
	return lambda2()
}
`)

	if got := jm.runInt32(t, "runNested"); got != 99 {
		t.Errorf("runNested() = %d, want 99", got)
	}
}

// TestUniformAbiAcrossPlainFunctionAndLambda is the test that would catch a
// silent miscompilation if the thunk/uniform-calling-convention fix
// (CODEGEN.md's "Lambdas" section) were wrong or missing: a single
// func(int, int) int-typed variable holds first a plain free-function
// reference, then a genuine lambda, calling it indirectly both times through
// the exact same variable. A free function's real underlying signature has
// no ctxPtr parameter at all, while a genuine lambda's real underlying
// function does - if genIndirectCall's own uniform "always pass ctxPtr
// first" calling convention weren't matched by a real, uniform-ABI thunk on
// the free-function side (genFuncThunk), this would either fail LLVM's
// module verifier (a mismatched call-site/callee signature) or - worse -
// silently corrupt the stack/registers at runtime instead of crashing
// cleanly.
func TestUniformAbiAcrossPlainFunctionAndLambda(t *testing.T) {
	jm := compileAndJIT(t, `
func add(x int, y int) int {
	return x + y
}

func testUniformAbi(x int, y int) int {
	var fn func(int, int) int
	fn = add
	a := fn(x, y)
	fn = func(p int, q int) int {
		return p * q
	}
	b := fn(x, y)
	return a + b
}
`)

	if got := jm.runInt32(t, "testUniformAbi", 3, 4); got != 19 {
		t.Errorf("testUniformAbi(3, 4) = %d, want 19 (7 + 12)", got)
	}
}
