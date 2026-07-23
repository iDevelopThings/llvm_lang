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

// TestThreeLevelNestedClosureCapture extends
// TestTwoLevelNestedClosureCapture one level further: outerFunc declares x;
// lambda1 declares lambda2, which itself declares lambda3, which is the one
// that actually reads x. Each intermediate lambda must relay x's address
// into the next one's own capture context even though neither lambda1's nor
// lambda2's own body ever mentions x directly - proving the relay chain
// (sema/capture.go's analyzeFuncLitCaptures walking straight through nested
// FuncLit subtrees, and codegen's own per-level relay - see CODEGEN.md's
// "Lambdas" section) generalizes past two levels rather than only happening
// to work for exactly two.
func TestThreeLevelNestedClosureCapture(t *testing.T) {
	jm := compileAndJIT(t, `
func outerFunc() func() func() func() int {
	x := 7
	lambda1 := func() func() func() int {
		lambda2 := func() func() int {
			lambda3 := func() int {
				return x
			}
			return lambda3
		}
		return lambda2
	}
	return lambda1
}

func runThreeLevel() int {
	lambda1 := outerFunc()
	lambda2 := lambda1()
	lambda3 := lambda2()
	return lambda3()
}
`)

	if got := jm.runInt32(t, "runThreeLevel"); got != 7 {
		t.Errorf("runThreeLevel() = %d, want 7", got)
	}
}

// TestCaptureFromConditionalBranchOnlyLambda covers a FuncLit that only
// lexically exists inside one arm of an if/else - the capture-promotion
// decision (marking the captured local as needing arena storage instead of
// an ordinary stack alloca) must not depend on which control-flow branch a
// FuncLit happens to sit in, since capture analysis walks the whole tree by
// node kind (computeCaptures, sema/capture.go), not by simulating control
// flow. Both branches assign a lambda that captures the same outer local x
// into the same func-typed variable, so the same JIT-executed function
// exercises whichever branch flag selects.
func TestCaptureFromConditionalBranchOnlyLambda(t *testing.T) {
	jm := compileAndJIT(t, `
func makeChooser(flag bool) func() int {
	x := 41
	var result func() int
	if flag {
		result = func() int {
			return x + 1
		}
	} else {
		result = func() int {
			return x + 2
		}
	}
	return result
}

func runChooser(flag bool) int {
	fn := makeChooser(flag)
	return fn()
}
`)

	if got := jm.runInt32(t, "runChooser", 1); got != 42 {
		t.Errorf("runChooser(true) = %d, want 42", got)
	}
	if got := jm.runInt32(t, "runChooser", 0); got != 43 {
		t.Errorf("runChooser(false) = %d, want 43", got)
	}
}

// TestMethodParamAndReceiverDerivedLocalCapturedThroughLambdaChain covers a
// method's own parameter and a receiver-derived local (a plain local var
// initialized from `this.<field>`, since `this` itself can never be
// captured directly - see TestCapturingThisInsideLambdaIsRejected,
// sema/lambda_test.go) each captured through a two-level lambda chain -
// confirming this goes through the identical capture/relay code path a free
// function's own parameter/local capture already does (see
// TestLambdaCapturesParameterByReference and
// TestTwoLevelNestedClosureCapture above), with no method-specific special
// casing anywhere in that path.
func TestMethodParamAndReceiverDerivedLocalCapturedThroughLambdaChain(t *testing.T) {
	jm := compileAndJIT(t, `
struct Box {
	v int
}

func (Box) makeNestedAdder(base int) func() func(int) int {
	local := this.v
	outer := func() func(int) int {
		return func(x int) int {
			return local + base + x
		}
	}
	return outer
}

func runBoxAdder(v int, base int, x int) int {
	b := Box{v}
	outer := b.makeNestedAdder(base)
	inner := outer()
	return inner(x)
}
`)

	if got := jm.runInt32(t, "runBoxAdder", 100, 10, 1); got != 111 {
		t.Errorf("runBoxAdder(100, 10, 1) = %d, want 111", got)
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
// TestForLoopCapturedHeaderVariableGetsPerIterationValue covers this
// project's Go 1.22+-style per-iteration for-loop variable semantics (see
// genForStmt, stmt.go, and LANGUAGE.md's "Lambdas" section): a closure
// created inside a for-loop's body, capturing the loop's OWN header
// variable (declared in its init clause), must see the value that variable
// held at the moment that particular closure was created - not whatever
// i++ mutates it to by the time the closure is actually called. Every
// closure is stashed into a slice first and only called afterward
// (folding all five results into one decimal number, sum = sum*10 +
// fns[j](), the same "avoid a JIT harness that can only call one named
// function" trick TestClosureCapturesByReferenceAcrossCalls above already
// uses) specifically so a call happening well after i has advanced past
// every value is exactly what's being exercised - the pre-fix, shared-slot
// behavior would fold every one of the five calls to 5 (Go's own classic
// closures-in-a-loop gotcha: 55555), not 01234 (1234).
func TestForLoopCapturedHeaderVariableGetsPerIterationValue(t *testing.T) {
	jm := compileAndJIT(t, `
func buildAndFold() int {
	fns := make([]func() int, 0)
	for i := 0; i < 5; i++ {
		fns = append(fns, func() int { return i })
	}
	sum := 0
	j := 0
	for j < len(fns) {
		sum = sum*10 + fns[j]()
		j++
	}
	return sum
}
`)

	if got := jm.runInt32(t, "buildAndFold"); got != 1234 {
		t.Errorf("buildAndFold() = %d, want 1234 (0,1,2,3,4 - each closure's own iteration value)", got)
	}
}

// TestForLoopBodyLocalCaptureStillFreshEachIteration is the regression
// counterpart to TestForLoopCapturedHeaderVariableGetsPerIterationValue: a
// fresh local declared directly in the loop's own body (`captured := i`)
// and captured instead of the header variable itself was already correct
// before this fix (its own ShortVarDecl sits inside bodyBB, so its
// arena_alloc call already re-executes fresh every dynamic iteration - see
// genForStmt's own doc comment) and must produce the exact same result,
// completely unaffected by the new per-iteration hand-off logic (this
// path never touches it at all: sym here is never the for-loop's own
// header symbol).
func TestForLoopBodyLocalCaptureStillFreshEachIteration(t *testing.T) {
	jm := compileAndJIT(t, `
func buildAndFoldBodyLocal() int {
	fns := make([]func() int, 0)
	for i := 0; i < 5; i++ {
		captured := i
		fns = append(fns, func() int { return captured })
	}
	sum := 0
	j := 0
	for j < len(fns) {
		sum = sum*10 + fns[j]()
		j++
	}
	return sum
}
`)

	if got := jm.runInt32(t, "buildAndFoldBodyLocal"); got != 1234 {
		t.Errorf("buildAndFoldBodyLocal() = %d, want 1234 (0,1,2,3,4)", got)
	}
}

// TestForLoopContinueStillPropagatesCapturedHeaderVariable covers `continue`
// interacting with the per-iteration hand-off (genForStmt's postBB-entry
// copy-back, which continueTarget routes every `continue` through): i=0 and
// i=1 each append a closure over their own iteration value; at i==2, the
// body itself reassigns i to 10 before continuing - that body-side mutation
// must still reach the post-clause's own i++ (making it 11, so 11<5 is
// false and the loop ends there), exactly as it would have before this fix.
// Only two closures ever get appended, over 0 and 1.
func TestForLoopContinueStillPropagatesCapturedHeaderVariable(t *testing.T) {
	jm := compileAndJIT(t, `
func buildAndFoldContinue() int {
	fns := make([]func() int, 0)
	for i := 0; i < 5; i++ {
		if i == 2 {
			i = 10
			continue
		}
		fns = append(fns, func() int { return i })
	}
	sum := 0
	j := 0
	for j < len(fns) {
		sum = sum*10 + fns[j]()
		j++
	}
	return sum
}
`)

	if got := jm.runInt32(t, "buildAndFoldContinue"); got != 1 {
		t.Errorf("buildAndFoldContinue() = %d, want 1 (closures over 0 and 1 only)", got)
	}
}

// TestForLoopBreakStillExitsCleanlyWithCapturedHeaderVariable covers
// `break` alongside the per-iteration hand-off: break branches straight to
// endBB, bypassing postBB (and so the copy-back hand-off) entirely - this
// must still exit cleanly with exactly the closures captured before the
// break (over 0, 1, and 2), no dangling/incorrect state.
func TestForLoopBreakStillExitsCleanlyWithCapturedHeaderVariable(t *testing.T) {
	jm := compileAndJIT(t, `
func buildAndFoldBreak() int {
	fns := make([]func() int, 0)
	for i := 0; i < 10; i++ {
		if i == 3 {
			break
		}
		fns = append(fns, func() int { return i })
	}
	sum := 0
	j := 0
	for j < len(fns) {
		sum = sum*10 + fns[j]()
		j++
	}
	return sum
}
`)

	if got := jm.runInt32(t, "buildAndFoldBreak"); got != 12 {
		t.Errorf("buildAndFoldBreak() = %d, want 12 (closures over 0, 1, 2)", got)
	}
}

// TestNestedForLoopsEachTrackOwnCapturedHeaderVariable covers an inner
// for-loop's own captured header variable nested inside an outer one - each
// level's per-iteration hand-off (loopVarSym/loopVarOrigAddr/loopVarType/
// loopVarEligible in genForStmt are plain call-local variables, not
// Generator fields) must track its own independent state via ordinary Go
// recursion, with zero cross-contamination between the two levels: every
// closure captures both i (outer) and j (inner), each at its own iteration.
func TestNestedForLoopsEachTrackOwnCapturedHeaderVariable(t *testing.T) {
	jm := compileAndJIT(t, `
func buildAndFoldNested() int {
	fns := make([]func() int, 0)
	for i := 0; i < 2; i++ {
		for j := 0; j < 3; j++ {
			fns = append(fns, func() int { return i*10 + j })
		}
	}
	sum := 0
	k := 0
	for k < len(fns) {
		sum = sum*100 + fns[k]()
		k++
	}
	return sum
}
`)

	if got := jm.runInt32(t, "buildAndFoldNested"); got != 102101112 {
		t.Errorf("buildAndFoldNested() = %d, want 102101112 (0,1,2,10,11,12)", got)
	}
}

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
