package codegen

import (
	"testing"

	"tinygo.org/x/go-llvm"
)

// compileAndJITOptimized is compileAndJIT plus a real run of this project's
// own optimization pipeline (mirroring src/compiler's finishPipeline/
// optimizationPipeline = "default<O2>") BEFORE handing the module to LLJIT -
// every other feature's own codegen-package tests JIT-execute raw,
// unoptimized IR directly (compileAndJIT never runs any pass), which is fine
// for every ordinary construct but not for a coroutine: llvm.coro.* intrinsics
// are only ever lowered into real code by the optimization pipeline's own
// coroutine-splitting/cleanup passes (see CODEGEN.md's "Coroutines" section
// and BLOCKERS.md's former entry on this) - JIT-executing unoptimized
// coroutine IR fails LLVM instruction selection outright ("Cannot select:
// intrinsic %llvm.coro.destroy"), confirmed directly while building this
// test file. Every coroutine test in this file must use this helper, never
// plain compileAndJIT.
func compileAndJITOptimized(t *testing.T, src string) *jitModule {
	t.Helper()
	mod := compileSrc(t, src)
	initJIT()

	triple := llvm.DefaultTargetTriple()
	target, err := llvm.GetTargetFromTriple(triple)
	if err != nil {
		mod.Dispose()
		t.Fatalf("GetTargetFromTriple: %v", err)
	}
	tm := target.CreateTargetMachine(triple, "", "", llvm.CodeGenLevelDefault, llvm.RelocDefault, llvm.CodeModelDefault)
	defer tm.Dispose()

	pbo := llvm.NewPassBuilderOptions()
	if err := mod.LLVM.RunPasses("default<O2>", tm, pbo); err != nil {
		pbo.Dispose()
		mod.Dispose()
		t.Fatalf("RunPasses: %v", err)
	}
	pbo.Dispose()

	ir := mod.LLVM.String()

	jit, err := llvm.NewLLJIT(llvm.NewLLJITBuilder())
	if err != nil {
		t.Fatalf("NewLLJIT: %v", err)
	}
	if err := bindMinGWMainThunk(jit); err != nil {
		mod.Dispose()
		jit.Dispose()
		t.Fatalf("bindMinGWMainThunk: %v", err)
	}

	tsctx := llvm.NewThreadSafeContextFromContext(mod.Ctx)
	tsm := llvm.NewThreadSafeModule(mod.LLVM, tsctx)
	if err := jit.AddLLVMIRModule(jit.MainJITDylib(), tsm); err != nil {
		jit.Dispose()
		t.Fatalf("AddLLVMIRModule: %v", err)
	}

	t.Cleanup(func() {
		if err := jit.Dispose(); err != nil {
			t.Errorf("LLJIT.Dispose: %v", err)
		}
	})
	return &jitModule{
		mod: mod,
		jit: jit,
		ir:  ir,
	}
}

// coroDestructorMatrixSrc declares one async func with two await points and
// a distinct, dedicated-counter-owning local declared in each of its three
// segments (before the first await, between the two awaits, after the
// second await/before normal completion) - the shared fixture for this
// file's exhaustive per-suspend-point destructor matrix (see AGENTS.md's
// Review process section: this is the single most important thing to get
// right for this feature). Three separate struct types (not one tagged
// type) so each destructor increments its own global independently, with no
// need for field access in the test source itself.
const coroDestructorMatrixSrc = `
struct ResA {
	constructor() {}
	destructor() { callsA = callsA + 1 }
}
struct ResB {
	constructor() {}
	destructor() { callsB = callsB + 1 }
}
struct ResC {
	constructor() {}
	destructor() { callsC = callsC + 1 }
}

var callsA int = 0
var callsB int = 0
var callsC int = 0

async func Coro() {
	a := ResA()
	await
	b := ResB()
	await
	c := ResC()
}

func getA() int { return callsA }
func getB() int { return callsB }
func getC() int { return callsC }

func destroyBeforeFirstAwait() {
	h := Coro()
	delete h
}

func destroyAfterFirstResume() {
	h := Coro()
	resume(h)
	delete h
}

func destroyAfterCompletion() {
	h := Coro()
	resume(h)
	resume(h)
	delete h
}

func scopeExitBeforeFirstAwait() {
	h := Coro()
}

func scopeExitAfterFirstResume() {
	h := Coro()
	resume(h)
}

func scopeExitAfterCompletion() {
	h := Coro()
	resume(h)
	resume(h)
}

func doubleDeleteIsSafe() {
	h := Coro()
	delete h
	delete h
}

func resumeAfterDoneIsSafe() bool {
	h := Coro()
	resume(h)
	resume(h)
	stillMore := resume(h)
	delete h
	return stillMore
}

func doneAfterDeleteIsSafe() bool {
	h := Coro()
	delete h
	return done(h)
}

func resumeAfterDeleteIsSafe() bool {
	h := Coro()
	delete h
	return resume(h)
}

// deleteInsideIfThenScopeExit exercises genIfStmt's own snapshot/restore
// destructor discipline (snapshotDestructors/restoreDestructors, stmt.go)
// against an explicit delete inside just one branch: the "then" branch's
// own delete removes h's entry from Generator.destructors, but genIfStmt
// unconditionally restores the PRE-IF snapshot afterward (which still has
// h's entry) - so the automatic scope-exit cleanup at this function's own
// end must still be safe even though h's slot is already null by then.
func deleteInsideIfThenScopeExit(cond bool) {
	h := Coro()
	if cond {
		delete h
	}
}
`

// checkCounters asserts callsA/callsB/callsC (via getA/getB/getC) match
// wantA/wantB/wantC exactly - both "no leak" (a destructor that should have
// fired but didn't) and "no double-destruct" (any count above 1) fail this.
func checkCounters(t *testing.T, jm *jitModule, label string, wantA, wantB, wantC int32) {
	t.Helper()
	if got := jm.runInt32(t, "getA"); got != wantA {
		t.Errorf("%s: callsA = %d, want %d", label, got, wantA)
	}
	if got := jm.runInt32(t, "getB"); got != wantB {
		t.Errorf("%s: callsB = %d, want %d", label, got, wantB)
	}
	if got := jm.runInt32(t, "getC"); got != wantC {
		t.Errorf("%s: callsC = %d, want %d", label, got, wantC)
	}
}

// TestCoroDestroy_BeforeFirstAwait: destroying (via explicit `delete`) a
// coroutine still suspended at its very first await must destruct exactly
// `a` (live at that point) - never `b`/`c` (never constructed yet).
func TestCoroDestroy_BeforeFirstAwait(t *testing.T) {
	jm := compileAndJITOptimized(t, coroDestructorMatrixSrc)
	jm.runInt32(t, "destroyBeforeFirstAwait")
	checkCounters(t, jm, "destroyBeforeFirstAwait", 1, 0, 0)
}

// TestCoroDestroy_AfterFirstResume: destroying after one resume (suspended
// at the second await) must destruct `a` AND `b`, never `c`.
func TestCoroDestroy_AfterFirstResume(t *testing.T) {
	jm := compileAndJITOptimized(t, coroDestructorMatrixSrc)
	jm.runInt32(t, "destroyAfterFirstResume")
	checkCounters(t, jm, "destroyAfterFirstResume", 1, 1, 0)
}

// TestCoroDestroy_AfterCompletion: two resumes runs the coroutine to normal
// completion - all three locals must already be destructed via normal
// completion's own unwind (not via the explicit `delete` that follows, which
// must be a safe no-op against the frame only).
func TestCoroDestroy_AfterCompletion(t *testing.T) {
	jm := compileAndJITOptimized(t, coroDestructorMatrixSrc)
	jm.runInt32(t, "destroyAfterCompletion")
	checkCounters(t, jm, "destroyAfterCompletion", 1, 1, 1)
}

// TestCoroScopeExit_BeforeFirstAwait: falling out of scope WITHOUT an
// explicit `delete` must trigger the identical automatic cleanup - the
// destructor-stack mechanism (pushDestructorEntry/unwindDestructorsTo) is
// what's actually new here, driving llvm.coro.destroy via
// coroDestroyLocalFn.
func TestCoroScopeExit_BeforeFirstAwait(t *testing.T) {
	jm := compileAndJITOptimized(t, coroDestructorMatrixSrc)
	jm.runInt32(t, "scopeExitBeforeFirstAwait")
	checkCounters(t, jm, "scopeExitBeforeFirstAwait", 1, 0, 0)
}

func TestCoroScopeExit_AfterFirstResume(t *testing.T) {
	jm := compileAndJITOptimized(t, coroDestructorMatrixSrc)
	jm.runInt32(t, "scopeExitAfterFirstResume")
	checkCounters(t, jm, "scopeExitAfterFirstResume", 1, 1, 0)
}

// TestCoroScopeExit_AfterCompletion: scope-exit on an already-normally-
// completed (but not yet deleted) handle must not double-destruct anything -
// it only frees the already-idle frame.
func TestCoroScopeExit_AfterCompletion(t *testing.T) {
	jm := compileAndJITOptimized(t, coroDestructorMatrixSrc)
	jm.runInt32(t, "scopeExitAfterCompletion")
	checkCounters(t, jm, "scopeExitAfterCompletion", 1, 1, 1)
}

// TestCoroDoubleDeleteIsSafe: a second explicit `delete` on an already-
// deleted (nulled) handle must be a defined no-op, never a crash/UB.
func TestCoroDoubleDeleteIsSafe(t *testing.T) {
	jm := compileAndJITOptimized(t, coroDestructorMatrixSrc)
	jm.runInt32(t, "doubleDeleteIsSafe")
	checkCounters(t, jm, "doubleDeleteIsSafe", 1, 0, 0)
}

// TestCoroResumeAfterDoneIsSafe: resume(h) against an already-normally-
// completed handle must be a defined, safe no-op reporting false (never
// resuming past a coroutine's own final suspend point for real).
func TestCoroResumeAfterDoneIsSafe(t *testing.T) {
	jm := compileAndJITOptimized(t, coroDestructorMatrixSrc)
	if got := jm.runBool(t, "resumeAfterDoneIsSafe"); got {
		t.Errorf("resumeAfterDoneIsSafe() = true, want false")
	}
	checkCounters(t, jm, "resumeAfterDoneIsSafe", 1, 1, 1)
}

// TestCoroDoneAfterDeleteIsSafe: done(h) against an already-`delete`d
// (nulled) handle must be a defined, safe read reporting true.
func TestCoroDoneAfterDeleteIsSafe(t *testing.T) {
	jm := compileAndJITOptimized(t, coroDestructorMatrixSrc)
	if got := jm.runBool(t, "doneAfterDeleteIsSafe"); !got {
		t.Errorf("doneAfterDeleteIsSafe() = false, want true")
	}
}

// TestCoroResumeAfterDeleteIsSafe: resume(h) against an already-`delete`d
// (nulled) handle must be a defined, safe no-op reporting false.
func TestCoroResumeAfterDeleteIsSafe(t *testing.T) {
	jm := compileAndJITOptimized(t, coroDestructorMatrixSrc)
	if got := jm.runBool(t, "resumeAfterDeleteIsSafe"); got {
		t.Errorf("resumeAfterDeleteIsSafe() = true, want false")
	}
}

// TestCoroDeleteInsideIfThenScopeExitIsSafe covers genIfStmt's own
// snapshot/restore destructor discipline against an explicit delete inside
// just one branch (see deleteInsideIfThenScopeExit's own doc comment) -
// both with cond true (the delete actually runs, then the function's own
// automatic scope-exit cleanup must not double-destroy/crash against the
// now-null handle) and cond false (the delete never runs, so ordinary
// automatic scope-exit cleanup must still destruct the still-suspended
// coroutine's own live local).
func TestCoroDeleteInsideIfThenScopeExitIsSafe(t *testing.T) {
	jm := compileAndJITOptimized(t, coroDestructorMatrixSrc)

	jm.runInt32(t, "deleteInsideIfThenScopeExit", 1)
	checkCounters(t, jm, "deleteInsideIfThenScopeExit(true)", 1, 0, 0)
}

func TestCoroDeleteInsideIfElseScopeExitIsSafe(t *testing.T) {
	jm := compileAndJITOptimized(t, coroDestructorMatrixSrc)

	jm.runInt32(t, "deleteInsideIfThenScopeExit", 0)
	checkCounters(t, jm, "deleteInsideIfThenScopeExit(false)", 1, 0, 0)
}

// TestCoroDeleteInsideMatchArmScopeExitIsSafe is the genMatchStmt/
// genValueMatchStmt counterpart to TestCoroDeleteInsideIfThenScopeExitIsSafe -
// both share the identical snapshotDestructors/restoreDestructors discipline
// (see enum.go), so the same guard that fixed the if-stmt case must cover
// this consumer too, not just the one construct the bug happened to be
// found through.
func TestCoroDeleteInsideMatchArmScopeExitIsSafe(t *testing.T) {
	jm := compileAndJITOptimized(t, coroDestructorMatrixSrc+`
func deleteInsideMatchArm(cond int) {
	h := Coro()
	match cond {
		1 => { delete h }
		_ => {}
	}
}
`)
	jm.runInt32(t, "deleteInsideMatchArm", 1)
	checkCounters(t, jm, "deleteInsideMatchArm(1)", 1, 0, 0)
}

func TestCoroDeleteInsideMatchWildcardScopeExitIsSafe(t *testing.T) {
	jm := compileAndJITOptimized(t, coroDestructorMatrixSrc+`
func deleteInsideMatchArm(cond int) {
	h := Coro()
	match cond {
		1 => { delete h }
		_ => {}
	}
}
`)
	jm.runInt32(t, "deleteInsideMatchArm", 0)
	checkCounters(t, jm, "deleteInsideMatchArm(0)", 1, 0, 0)
}

// TestCoroZeroAwaits: an async func with no await at all completes
// synchronously on the very first call - done(h) must already be true, and
// its one local must already be destructed, with no resume/delete needed.
func TestCoroZeroAwaits(t *testing.T) {
	jm := compileAndJITOptimized(t, `
struct ResA {
	constructor() {}
	destructor() { callsA = callsA + 1 }
}
var callsA int = 0

async func Instant() {
	a := ResA()
}

func getA() int { return callsA }

func run() bool {
	h := Instant()
	ok := done(h)
	delete h
	return ok
}
`)
	if got := jm.runBool(t, "run"); !got {
		t.Errorf("run() = false, want true (zero-await coroutine must be done() immediately)")
	}
	if got := jm.runInt32(t, "getA"); got != 1 {
		t.Errorf("TestCoroZeroAwaits: callsA = %d, want 1", got)
	}
}

// TestCoroResumeReportsMoreWork proves resume(h)'s own bool result tracks
// "suspended again" vs "just finished" correctly across a real multi-await
// coroutine, using getA/getB/getC as an independent progress signal.
func TestCoroResumeReportsMoreWork(t *testing.T) {
	jm := compileAndJITOptimized(t, coroDestructorMatrixSrc+`
func driveAndReport() int {
	h := Coro()
	r1 := resume(h)
	r2 := resume(h)
	n := 0
	if r1 { n = n + 1 }
	if r2 { n = n + 10 }
	delete h
	return n
}
`)
	// r1 (resuming from the first await into the second) must report true
	// (more work: the coroutine hasn't reached its own final suspend yet);
	// r2 (resuming from the second await through to normal completion) must
	// report false (it just finished).
	if got := jm.runInt32(t, "driveAndReport"); got != 1 {
		t.Errorf("driveAndReport() = %d, want 1 (r1=true, r2=false)", got)
	}
}

// TestCoroNestedHandleAsLocalDestructsInOrder covers holding a coroutine
// handle (Inner) as an ordinary local INSIDE another coroutine's own body
// (Outer), live across Outer's own await, then explicitly `delete`d from
// within Outer - never exercised by coroDestructorMatrixSrc, whose locals
// are all plain structs. order encodes destruction sequence as digits (`inner`'s
// own live resource must destruct before Outer's own, matching declaration
// order within each frame).
func TestCoroNestedHandleAsLocalDestructsInOrder(t *testing.T) {
	jm := compileAndJITOptimized(t, `
struct Res {
	id int
	constructor(v int) { this.id = v }
	destructor() { order = order * 10 + this.id }
}
var order int = 0

async func Inner() {
	r := Res(2)
	await
}

async func Outer() {
	outerRes := Res(1)
	inner := Inner()
	await
	delete inner
}

func getOrder() int { return order }

func run() {
	h := Outer()
	resume(h)
	delete h
}
`)
	jm.runInt32(t, "run")
	if got := jm.runInt32(t, "getOrder"); got != 21 {
		t.Errorf("order = %d, want 21 (inner's Res(2) destructs first, then Outer's own Res(1))", got)
	}
}

// TestCoroutineTypedVarWithNoAsyncFuncAnywhere covers programUsesCoroutines'
// own second trigger (coroutine.go): a program with a `coroutine`-typed
// declaration but ZERO `async func`s anywhere still needs coro.destroylocal
// set up, since the var's own scope-exit still reaches destructorFuncFor's
// TypeCoroutine case. h can only ever be nil here (no async func exists to
// call), so this also proves a nil coroutine handle's own scope-exit cleanup
// is a safe no-op even when setupCoroutines had nothing else to do.
func TestCoroutineTypedVarWithNoAsyncFuncAnywhere(t *testing.T) {
	jm := compileAndJITOptimized(t, `
func run() int {
	var h coroutine
	return 1
}
`)
	if got := jm.runInt32(t, "run"); got != 1 {
		t.Errorf("run() = %d, want 1", got)
	}
}

// --- result(h): a non-void async function's own declared result value ---
//
// See LANGUAGE.md's "Coroutines" section and CODEGEN.md's own "Coroutines"
// section for the coro.promise-based mechanism: genCoroPrologue allocates a
// promise slot for a non-void async function, every `return expr` inside its
// body stores into it, and genResultCall reads it back through
// llvm.coro.promise, guarded by a real done(h) branch.

// TestCoroResult_IntReturnValue: the simplest case - one await, then a real
// int return, read back via result(h) once the handle is done.
func TestCoroResult_IntReturnValue(t *testing.T) {
	jm := compileAndJITOptimized(t, `
async func ComputeAnswer() int {
	await
	return 42
}

func run() int {
	h := ComputeAnswer()
	resume(h)
	v := result(h)
	delete h
	return v
}
`)
	if got := jm.runInt32(t, "run"); got != 42 {
		t.Errorf("run() = %d, want 42", got)
	}
}

// TestCoroResult_GenericAsyncFuncExplicitTypeArg proves a generic async
// function's own explicit-instantiation call (`Foo[int](x)`) round-trips a
// real result value end to end, not just type-checks - both the missing
// TypeCoroutine wrap in checkGenericCall and calleeIsAsyncFunc's own
// Ident-only assumption (breaking isFreshConstruction for this callee
// shape) were real bugs found while building the type registry feature.
func TestCoroResult_GenericAsyncFuncExplicitTypeArg(t *testing.T) {
	jm := compileAndJITOptimized(t, `
async func Echo[T](x T) T {
	await
	return x
}

func run() int {
	h := Echo[int](99)
	resume(h)
	v := result(h)
	delete h
	return v
}
`)
	if got := jm.runInt32(t, "run"); got != 99 {
		t.Errorf("run() = %d, want 99", got)
	}
}

// TestCoroResult_BeforeDoneIsZeroValue proves result(h) called before the
// coroutine is actually done returns int's zero value, not garbage - then,
// against the SAME handle, driving it to completion and calling result(h)
// again returns the real value (see genResultCall's own done(h)-guarded
// branch, never a blind load).
func TestCoroResult_BeforeDoneIsZeroValue(t *testing.T) {
	jm := compileAndJITOptimized(t, `
async func ComputeAnswer() int {
	await
	return 42
}

func run() int {
	h := ComputeAnswer()
	before := result(h)
	resume(h)
	after := result(h)
	delete h
	return before*10000 + after
}
`)
	got := jm.runInt32(t, "run")
	before, after := got/10000, got%10000
	if before != 0 {
		t.Errorf("result(h) before done = %d, want 0", before)
	}
	if after != 42 {
		t.Errorf("result(h) after done = %d, want 42", after)
	}
}

// TestCoroResult_AfterDeleteIsZeroValueNotCrash proves result(h) on an
// already-deleted handle is a safe, defined no-op returning T's zero value -
// genDoneCall reports a nil handle as done, so result(h) must check for nil
// BEFORE trusting that "done" answer, or it would read through the freed
// frame via coro.promise against a null handle.
func TestCoroResult_AfterDeleteIsZeroValueNotCrash(t *testing.T) {
	jm := compileAndJITOptimized(t, `
async func ComputeAnswer() int {
	await
	return 42
}

func run() int {
	h := ComputeAnswer()
	resume(h)
	delete h
	return result(h)
}
`)
	if got := jm.runInt32(t, "run"); got != 0 {
		t.Errorf("result(h) after delete = %d, want 0", got)
	}
}

// TestCoroResult_MultiAwaitReadsAfterLastSuspend proves the returned value is
// read correctly after the LAST of several suspend/resume cycles, not just a
// coroutine that returns immediately with no await at all.
func TestCoroResult_MultiAwaitReadsAfterLastSuspend(t *testing.T) {
	jm := compileAndJITOptimized(t, `
async func Multi() int {
	await
	await
	await
	return 7
}

func run() int {
	h := Multi()
	resume(h)
	resume(h)
	resume(h)
	v := result(h)
	delete h
	return v
}
`)
	if got := jm.runInt32(t, "run"); got != 7 {
		t.Errorf("run() = %d, want 7", got)
	}
}

// TestCoroResult_StringReturnValue proves a non-scalar (fat-pointer) result
// type round-trips through the promise slot correctly.
func TestCoroResult_StringReturnValue(t *testing.T) {
	jm := compileAndJITOptimized(t, `
async func Greeting() string {
	await
	return "hello coroutine"
}

func run() bool {
	h := Greeting()
	resume(h)
	v := result(h)
	delete h
	return v == "hello coroutine"
}
`)
	if got := jm.runBool(t, "run"); !got {
		t.Errorf("run() = false, want true")
	}
}

// TestCoroResult_StructReturnValue proves a struct-typed result works too -
// exercising genFuncCall's own isAsync guard against its unrelated
// extern-struct-return coercion path (see genFuncCall's own doc comment).
func TestCoroResult_StructReturnValue(t *testing.T) {
	jm := compileAndJITOptimized(t, `
struct Point {
	x int
	y int
}

async func MakePoint() Point {
	await
	return Point{x: 3, y: 4}
}

func run() bool {
	h := MakePoint()
	resume(h)
	p := result(h)
	delete h
	return p.x == 3 && p.y == 4
}
`)
	if got := jm.runBool(t, "run"); !got {
		t.Errorf("run() = false, want true")
	}
}

// TestCoroResult_HandleCallItselfStillWorksForNonVoidAsync proves calling a
// non-void async function still produces a usable coroutine handle through
// the ordinary direct-call path (genFuncCall) - not just result(h) itself -
// since entry.retType is no longer forced void for async at codegen time.
func TestCoroResult_HandleCallItselfStillWorksForNonVoidAsync(t *testing.T) {
	jm := compileAndJITOptimized(t, `
async func ComputeAnswer() int {
	await
	return 42
}

func run() bool {
	h := ComputeAnswer()
	ok := !done(h)
	resume(h)
	stillOk := done(h)
	delete h
	return ok && stillOk
}
`)
	if got := jm.runBool(t, "run"); !got {
		t.Errorf("run() = false, want true")
	}
}
