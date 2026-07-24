package codegen

import "testing"

// This file covers `move x`'s own lowering (see LANGUAGE.md's "Destructors"
// section's "move" subsection, CODEGEN.md's corresponding section):
// genMoveExpr's removeDestructorEntry at the move site, and the
// reassignment-leak fix (genAssignInto, stmt.go) for both a destructor-
// owning struct and a coroutine handle. Every test here verifies exactly
// how many times a destructor fires, via a global counter/accumulator -
// never more, matching destructor_test.go's own standard (see its own doc
// comment).
//
// A coroutine handle moved INTO an async function's own parameter is
// mechanically identical to TestMoveOfStructAsFunctionArgumentDestructsOnce
// below (pushDestructorEntry is pushed for a parameter the same way
// regardless of whether the enclosing function is async - see genFuncBody,
// func.go) - not written separately here as its own test, since it would
// exercise no new codegen path. Moving a handle OUT of a coroutine's own
// body via `return` is not expressible at all this round: an async
// function is void-only (checkFuncDecl rejects any declared return type),
// so there is no way for its body to hand anything back to its own caller.

const moveResourceSrc = `
struct Resource {
	id int
	constructor(v int) {
		this.id = v
	}
	destructor() {
		calls = calls + 1
	}
}
var calls int = 0
`

// TestMoveBetweenLocalsFiresDestructorOnceAtNewOwner proves a moved value
// destructs exactly once, at the NEW binding's own scope exit - never at
// the old binding's own original declaration position (which move's own
// removeDestructorEntry must have cleared), and never twice.
func TestMoveBetweenLocalsFiresDestructorOnceAtNewOwner(t *testing.T) {
	jm := compileAndJIT(t, moveResourceSrc+`
func f() {
	a := Resource(1)
	b := move a
}
func run() int {
	f()
	return calls
}
`)
	if got := jm.runInt32(t, "run"); got != 1 {
		t.Errorf("run() = %d, want 1 (b destructs exactly once at f's own scope exit; a's own original entry must not also fire)", got)
	}
}

// TestMoveIntoCompositeLitFieldDoesNotAlsoDestructTheOldLocal covers the
// composite-literal-field call site: LANGUAGE.md's own "Known limitation"
// means a struct field holding a destructor-owning value by value is never
// itself auto-destructed at ITS OWN containing struct's scope exit (Wrapper
// declares no destructor()) - so `calls` never increments here at all. What
// this actually proves is that `a`'s own move correctly removed its
// destructor-stack entry: if it hadn't, a's own original declaring block
// would still fire an extra, incorrect destructor call once THIS function
// returns, and calls would come back 1, not 0.
func TestMoveIntoCompositeLitFieldDoesNotAlsoDestructTheOldLocal(t *testing.T) {
	jm := compileAndJIT(t, moveResourceSrc+`
struct Wrapper {
	r Resource
}
func f() int {
	a := Resource(1)
	w := Wrapper{move a}
	return w.r.id
}
func run() int {
	id := f()
	return id*100 + calls
}
`)
	if got := jm.runInt32(t, "run"); got != 100 {
		t.Errorf("run() = %d, want 100 (w.r.id == 1 proves the value moved correctly; calls == 0 proves a's own entry was removed, not left to fire a second time)", got)
	}
}

// TestMoveIntoArrayElementDoesNotAlsoDestructTheOldLocal is the identical
// check for an array element - a fixed-size array's own type never
// declares a destructor of its own either (see pushDestructorEntry's own
// struct/enum-only gate), so the same "never auto-destructed, but also
// never double-destructed" reasoning applies verbatim.
func TestMoveIntoArrayElementDoesNotAlsoDestructTheOldLocal(t *testing.T) {
	jm := compileAndJIT(t, moveResourceSrc+`
func f() int {
	a := Resource(1)
	arr := [1]Resource{move a}
	return arr[0].id
}
func run() int {
	id := f()
	return id*100 + calls
}
`)
	if got := jm.runInt32(t, "run"); got != 100 {
		t.Errorf("run() = %d, want 100 (arr[0].id == 1 proves the value moved correctly; calls == 0 proves a's own entry was removed)", got)
	}
}

// TestMoveOfStructAsFunctionArgumentDestructsOnce covers the argument call
// site: unlike a composite-literal field or array element, a function
// PARAMETER is a plain local as far as pushDestructorEntry is concerned, so
// it destructs normally at the callee's own scope exit - exactly once.
func TestMoveOfStructAsFunctionArgumentDestructsOnce(t *testing.T) {
	jm := compileAndJIT(t, moveResourceSrc+`
func consume(r Resource) {
}
func f() {
	a := Resource(1)
	consume(move a)
}
func run() int {
	f()
	return calls
}
`)
	if got := jm.runInt32(t, "run"); got != 1 {
		t.Errorf("run() = %d, want 1 (consume's own parameter destructs exactly once; a's own entry must not also fire)", got)
	}
}

// TestMoveOfStructAtReturnDestructsOnceAtCallerOwnScopeExit covers the
// round's own central unlock: a return statement used to allow no
// non-copyable exception at all, even a fresh construction - `move a` now
// makes it legal, and the caller's own fresh binding owns the result
// exactly like any other freshly-returned value.
func TestMoveOfStructAtReturnDestructsOnceAtCallerOwnScopeExit(t *testing.T) {
	jm := compileAndJIT(t, moveResourceSrc+`
func make() Resource {
	a := Resource(1)
	return move a
}
func useIt() {
	c := make()
	print(c.id)
}
func run() int {
	useIt()
	return calls
}
`)
	if got := jm.runInt32(t, "run"); got != 1 {
		t.Errorf("run() = %d, want 1 (c destructs exactly once at useIt's own scope exit)", got)
	}
}

// TestReassignmentDestructsOldStructValueBeforeOverwrite is the
// reassignment-leak fix's own regression test: `f := Res(1); f = Res(2)`
// used to silently leak Res(1) - genAssignInto (stmt.go) now destructs
// whatever f currently holds before storing the new value. order encodes
// both the SEQUENCE and the COUNT: each destructor appends its own id as a
// base-10 digit, so "12" is only reachable if Res(1) destructs exactly
// once, before Res(2) does, which itself destructs exactly once (a leaked
// Res(1) would read back "2"; a double-destructed Res(2) would read back
// "112" or "122").
func TestReassignmentDestructsOldStructValueBeforeOverwrite(t *testing.T) {
	jm := compileAndJIT(t, `
struct Resource {
	id int
	constructor(v int) {
		this.id = v
	}
	destructor() {
		order = order*10 + this.id
	}
}
var order int = 0
func f() {
	r := Resource(1)
	r = Resource(2)
}
func run() int {
	f()
	return order
}
`)
	if got := jm.runInt32(t, "run"); got != 12 {
		t.Errorf("run() = %d, want 12 (Resource(1) destructs once, then Resource(2) destructs once at f's own scope exit)", got)
	}
}

// TestReassignmentDestructsOldCoroutineHandleBeforeOverwrite is the
// identical reassignment-leak fix, one type kind over: `h := Coro(1); h =
// Coro(2)` used to leak the first coroutine's own frame (and its own live
// Resource) entirely - genAssignInto now destructs h's old handle
// (destructorFuncFor's TypeCoroutine case, the same coroDestroyLocalFn
// wrapper pushDestructorEntry already uses) before storing the new one.
// Same order-encoding proof as the struct case: "12" is only reachable if
// each coroutine's own live Resource destructs exactly once, in sequence.
func TestReassignmentDestructsOldCoroutineHandleBeforeOverwrite(t *testing.T) {
	jm := compileAndJITOptimized(t, `
struct Resource {
	id int
	constructor(v int) {
		this.id = v
	}
	destructor() {
		order = order*10 + this.id
	}
}
var order int = 0
async func Coro(v int) {
	r := Resource(v)
	await
}
func f() {
	h := Coro(1)
	h = Coro(2)
	delete h
}
func run() int {
	f()
	return order
}
`)
	if got := jm.runInt32(t, "run"); got != 12 {
		t.Errorf("run() = %d, want 12 (the first coroutine's own live Resource(1) must destruct before Resource(2)'s own coroutine is built, then Resource(2) destructs via the explicit delete)", got)
	}
}

// TestMoveInBothIfBranchesRunsCorrectlyAtRuntime is sema's own
// TestMoveInBothIfBranchesIsFine, proven at runtime: `move x` in both a
// then and else branch type-checks (mergeMovedAcrossPaths sees it moved on
// every reachable path), but that only proves compile-time soundness - this
// confirms the actual generated code destructs exactly once regardless of
// which branch runs, for BOTH possible cond values.
func TestMoveInBothIfBranchesRunsCorrectlyAtRuntime(t *testing.T) {
	src := moveResourceSrc + `
func run(cond bool) int {
	r := Resource(1)
	if cond {
		b := move r
		print(b.id)
	} else {
		c := move r
		print(c.id)
	}
	return calls
}
`
	if got := compileAndJIT(t, src).runInt32(t, "run", 1); got != 1 {
		t.Errorf("run(true) = %d, want 1", got)
	}
	if got := compileAndJIT(t, src).runInt32(t, "run", 0); got != 1 {
		t.Errorf("run(false) = %d, want 1", got)
	}
}

// TestMoveOfLoopDeclaredValueAcrossIterations proves a value declared fresh
// INSIDE a loop body, moved within that same iteration, is safe across many
// iterations - each iteration's own local is a distinct instance (matching
// how any other per-iteration local already works), so moving it away each
// time must never leak or double-destruct across iterations.
func TestMoveOfLoopDeclaredValueAcrossIterations(t *testing.T) {
	jm := compileAndJIT(t, moveResourceSrc+`
func run() int {
	i := 0
	for i < 5 {
		r := Resource(i)
		dest := move r
		i = i + 1
	}
	return calls
}
`)
	if got := jm.runInt32(t, "run"); got != 5 {
		t.Errorf("run() = %d, want 5 (one destructor call per loop iteration, no leak or double-destruct)", got)
	}
}

// TestReassignmentRHSReadsOldValueBeforeDestructing proves genAssignInto's
// own evaluate-then-destruct ordering for a non-composite-literal RHS: `f =
// Resource(f.id + 1)` must read f's OLD id (1) before destructing it, not
// after - destructing first would read back a wrong/corrupted id once the
// old Resource's own destructor has already run against it.
func TestReassignmentRHSReadsOldValueBeforeDestructing(t *testing.T) {
	jm := compileAndJIT(t, `
struct Resource {
	id int
	constructor(v int) {
		this.id = v
	}
	destructor() {
		order = order*10 + this.id
	}
}
var order int = 0
func run() int {
	f := Resource(1)
	f = Resource(f.id + 1)
	return order
}
`)
	if got := jm.runInt32(t, "run"); got != 1 {
		t.Errorf("run() = %d, want 1 (only the old Resource(1) destructs here; Resource(2) is still live, destructing at run's own scope exit - which this test doesn't observe since it returns before that)", got)
	}
}

// TestMoveInEveryMatchArmRunsCorrectlyAtRuntime is
// TestMoveInBothIfBranchesRunsCorrectlyAtRuntime's own match-statement
// counterpart - genMatchStmt (enum.go) applies the identical
// mergeBranchDestructors reconciliation genIfStmt does, generalized from two
// branches to N arms. Moving r (declared before the match) in every arm
// must destruct it exactly once, via whichever arm's own fresh binding
// actually ran - never via a resurrected entry for r itself once the match
// is done.
func TestMoveInEveryMatchArmRunsCorrectlyAtRuntime(t *testing.T) {
	src := moveResourceSrc + `
enum Shape {
	Circle,
	Square
}
func run(isCircle bool) int {
	r := Resource(1)
	s := Shape.Square
	if isCircle {
		s = Shape.Circle
	}
	match s {
		Shape.Circle => {
			b := move r
			print(b.id)
		}
		Shape.Square => {
			c := move r
			print(c.id)
		}
	}
	return calls
}
`
	if got := compileAndJIT(t, src).runInt32(t, "run", 1); got != 1 {
		t.Errorf("run(true) = %d, want 1", got)
	}
	if got := compileAndJIT(t, src).runInt32(t, "run", 0); got != 1 {
		t.Errorf("run(false) = %d, want 1", got)
	}
}

// TestMoveInsideIfInsideLoopThenBreakDestructsCorrectly directly covers the
// coordinator's own follow-up concern: does genIfStmt's own
// mergeBranchDestructors fix compose correctly with an ENCLOSING loop's own
// break/continue destructorBase (a destructorScope, recomputed against the
// current stack - see unwindDestructorsToScope)? r is declared inside the
// loop body (fresh every iteration, so moving it is unrestricted - see
// moveState.declLoopDepth), moved in both arms of a nested if, one of which
// breaks - the loop's own destructorBase must still correctly see nothing
// left to unwind at the break (r was already handed off to b/c, whichever
// ran) or at each normal iteration's own fall-through.
func TestMoveInsideIfInsideLoopThenBreakDestructsCorrectly(t *testing.T) {
	jm := compileAndJIT(t, moveResourceSrc+`
func run() int {
	i := 0
	for i < 5 {
		r := Resource(i)
		if i == 2 {
			b := move r
			print(b.id)
			break
		} else {
			c := move r
			print(c.id)
		}
		i = i + 1
	}
	return calls
}
`)
	// i=0,1: else-branch moves r into c, falls through, loop continues.
	// i=2: then-branch moves r into b, then breaks - one destructor call per
	// iteration either way, three total, no leak and no double-destruct.
	if got := jm.runInt32(t, "run"); got != 3 {
		t.Errorf("run() = %d, want 3 (one destructor call per iteration - i=0,1 via else, i=2 via then-then-break)", got)
	}
}
