package codegen

import "testing"

// This file covers generator functions end to end, JIT-executed (see
// LANGUAGE.md's "Generator functions" section): the push/callback lowering
// (a generator's own implicit trailing callback parameter, `yield`'s
// indirect call through it), consuming one via `for [v] := range Gen(args)
// { ... }` (zero/one-binding forms), break/continue proved with concrete
// values (stopping early vs. skipping one element), an outer accumulator
// local correctly captured and mutated across every callback invocation, and
// a local declared fresh inside the consuming loop's own body behaving
// correctly across iterations.

func TestGeneratorZeroBindingCountsIterations(t *testing.T) {
	jm := compileAndJIT(t, `
func Range(a int, b int) yield int {
	for i := a; i < b; i = i + 1 {
		yield i
	}
}

func countIterations() int {
	count := 0
	for range Range(0, 5) {
		count = count + 1
	}
	return count
}
`)
	if got := jm.runInt32(t, "countIterations"); got != 5 {
		t.Errorf("countIterations() = %d, want 5", got)
	}
}

func TestGeneratorOneBindingSumsValues(t *testing.T) {
	jm := compileAndJIT(t, `
func Range(a int, b int) yield int {
	for i := a; i < b; i = i + 1 {
		yield i
	}
}

func sumRange() int {
	sum := 0
	for v := range Range(1, 5) {
		sum = sum + v
	}
	return sum
}
`)
	if got := jm.runInt32(t, "sumRange"); got != 10 {
		t.Errorf("sumRange() = %d, want 10 (1+2+3+4)", got)
	}
}

// TestGeneratorBreakStopsEarly proves break inside a generator-consuming
// range-for genuinely stops the generator early - a concrete value proof
// (sum only includes 1..4, never reaching 9, which a "break does nothing"
// bug would let through).
func TestGeneratorBreakStopsEarly(t *testing.T) {
	jm := compileAndJIT(t, `
func Range(a int, b int) yield int {
	for i := a; i < b; i = i + 1 {
		yield i
	}
}

func sumUntilFive() int {
	sum := 0
	for v := range Range(1, 10) {
		if v == 5 {
			break
		}
		sum = sum + v
	}
	return sum
}
`)
	if got := jm.runInt32(t, "sumUntilFive"); got != 10 {
		t.Errorf("sumUntilFive() = %d, want 10 (1+2+3+4, stopping before 5)", got)
	}
}

// TestGeneratorContinueSkipsOne proves continue inside a generator-consuming
// range-for skips exactly one element and keeps going - a concrete value
// proof distinguishing it from break (every OTHER element still
// contributes, unlike break's own early stop).
func TestGeneratorContinueSkipsOne(t *testing.T) {
	jm := compileAndJIT(t, `
func Range(a int, b int) yield int {
	for i := a; i < b; i = i + 1 {
		yield i
	}
}

func sumSkippingThree() int {
	sum := 0
	for v := range Range(1, 6) {
		if v == 3 {
			continue
		}
		sum = sum + v
	}
	return sum
}
`)
	if got := jm.runInt32(t, "sumSkippingThree"); got != 12 {
		t.Errorf("sumSkippingThree() = %d, want 12 (1+2+4+5, skipping 3)", got)
	}
}

// TestGeneratorInternalFilterAndEarlyExit covers a generator with more
// interesting internal logic: Evens only ever yields an even number (an
// `if`-guarded yield inside its own body), and FirstNPositive stops
// yielding itself via a bare `return` once it's produced enough values.
func TestGeneratorInternalFilterAndEarlyExit(t *testing.T) {
	jm := compileAndJIT(t, `
func Evens(limit int) yield int {
	i := 0
	for i < limit {
		if i % 2 == 0 {
			yield i
		}
		i = i + 1
	}
}

func FirstNPositive(n int) yield int {
	count := 0
	i := 1
	for {
		if count == n {
			return
		}
		yield i
		count = count + 1
		i = i + 1
	}
}

func sumEvens() int {
	sum := 0
	for v := range Evens(10) {
		sum = sum + v
	}
	return sum
}

func sumFirstThree() int {
	sum := 0
	for v := range FirstNPositive(3) {
		sum = sum + v
	}
	return sum
}
`)
	if got := jm.runInt32(t, "sumEvens"); got != 20 {
		t.Errorf("sumEvens() = %d, want 20 (0+2+4+6+8)", got)
	}
	if got := jm.runInt32(t, "sumFirstThree"); got != 6 {
		t.Errorf("sumFirstThree() = %d, want 6 (1+2+3)", got)
	}
}

// TestGeneratorConsumingLoopCapturesOuterLocals proves the closure-capture
// machinery reused from lambdas works correctly here: a read-only outer
// local (multiplier) and an outer accumulator (scaled, both read AND
// written across every callback invocation) both need their real,
// consistent storage relayed through the synthesized callback's own ctxPtr.
func TestGeneratorConsumingLoopCapturesOuterLocals(t *testing.T) {
	jm := compileAndJIT(t, `
func Range(a int, b int) yield int {
	for i := a; i < b; i = i + 1 {
		yield i
	}
}

func scaledSum() int {
	multiplier := 3
	scaled := 0
	for v := range Range(1, 4) {
		scaled = scaled + v*multiplier
	}
	return scaled
}
`)
	if got := jm.runInt32(t, "scaledSum"); got != 18 {
		t.Errorf("scaledSum() = %d, want 18 ((1+2+3)*3)", got)
	}
}

// TestGeneratorConsumingLoopBodyLocalIsFreshPerIteration proves a local
// declared directly inside the consuming loop's own body gets fresh storage
// every callback invocation, not stale/shared state left over from a prior
// call.
func TestGeneratorConsumingLoopBodyLocalIsFreshPerIteration(t *testing.T) {
	jm := compileAndJIT(t, `
func Range(a int, b int) yield int {
	for i := a; i < b; i = i + 1 {
		yield i
	}
}

func doubledSum() int {
	total := 0
	for v := range Range(1, 4) {
		local := v * 2
		total = total + local
	}
	return total
}
`)
	if got := jm.runInt32(t, "doubledSum"); got != 12 {
		t.Errorf("doubledSum() = %d, want 12 ((1+2+3)*2)", got)
	}
}
