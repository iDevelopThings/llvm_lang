package codegen

import "testing"

// TestDestructorDoesNotFireWhileStillInScope covers the "not before" half of
// the normal-scope-end trigger: a value read from inside the local's own
// declaring block, before it falls off the end, must never observe the
// destructor having already run.
func TestDestructorDoesNotFireWhileStillInScope(t *testing.T) {
	jm := compileAndJIT(t, `
struct Resource {
	constructor() {
	}
	destructor() {
		calls = calls + 1
	}
}

var calls int = 0

func useIt() int {
	r := Resource()
	return calls
}
`)
	if got := jm.runInt32(t, "useIt"); got != 0 {
		t.Errorf("useIt() = %d, want 0 (destructor must not have fired yet while r is still in scope)", got)
	}
}

// TestDestructorFiresExactlyOnceAtNormalScopeEnd covers the "exactly once"
// half: falling off the end of the local's own declaring block fires its
// destructor exactly once - a separate JIT module from
// TestDestructorDoesNotFireWhileStillInScope's, so the shared `calls` global
// only ever gets exactly one call's worth of side effects to observe here.
func TestDestructorFiresExactlyOnceAtNormalScopeEnd(t *testing.T) {
	jm := compileAndJIT(t, `
struct Resource {
	constructor() {
	}
	destructor() {
		calls = calls + 1
	}
}

var calls int = 0

func useIt() {
	r := Resource()
}

func afterUse() int {
	useIt()
	return calls
}
`)
	if got := jm.runInt32(t, "afterUse"); got != 1 {
		t.Errorf("afterUse() = %d, want 1 (destructor must fire exactly once after useIt returns)", got)
	}
}

// destructorEarlyReturnSrc is shared by
// TestDestructorFiresOnEarlyReturn/TestDestructorFiresOnFallThroughReturn -
// two separate JIT modules (each gets its own fresh `calls` global) so
// each test's single call is the only thing ever incrementing it, rather
// than two calls sharing one module's global state and potentially masking
// a real bug behind a coincidental total (see the destructor stack's own
// if/else save-restore fix, stmt.go's genIfStmt, for exactly the kind of
// bug a shared-state test setup here could have hidden).
const destructorEarlyReturnSrc = `
struct Resource {
	constructor() {
	}
	destructor() {
		calls = calls + 1
	}
}

var calls int = 0

func useIt(early bool) int {
	r := Resource()
	if early {
		return 1
	}
	return 2
}

func run(early bool) int {
	useIt(early)
	return calls
}
`

// TestDestructorFiresOnEarlyReturn covers the second exit-path trigger: an
// early `return` inside the local's own declaring block (here, inside an
// if's own then-branch, nested one level deeper than the block that
// declares the local) still fires its destructor, exactly once.
func TestDestructorFiresOnEarlyReturn(t *testing.T) {
	jm := compileAndJIT(t, destructorEarlyReturnSrc)
	if got := jm.runInt32(t, "run", 1); got != 1 {
		t.Errorf("run(true) = %d, want 1 (destructor must fire on the early return)", got)
	}
}

// TestDestructorFiresOnFallThroughReturn covers the sibling case in the same
// program: the *other* branch (falling through the if to the trailing
// `return 2`) must independently still fire its own destructor call too -
// this is the real proof genIfStmt's then/else destructor-stack save/restore
// is correct, not just that *some* path fires a call: before that fix, only
// whichever branch happened to be generated first ever got a real destructor
// call in the emitted IR at all.
func TestDestructorFiresOnFallThroughReturn(t *testing.T) {
	jm := compileAndJIT(t, destructorEarlyReturnSrc)
	if got := jm.runInt32(t, "run", 0); got != 1 {
		t.Errorf("run(false) = %d, want 1 (the fall-through return path must fire its own destructor call too)", got)
	}
}

// TestDestructorFiresOnBreak covers the third exit-path trigger: a `break`
// exiting an enclosing loop from within the local's own declaring block
// fires its destructor before actually branching out of the loop.
func TestDestructorFiresOnBreak(t *testing.T) {
	jm := compileAndJIT(t, `
struct Resource {
	constructor() {
	}
	destructor() {
		calls = calls + 1
	}
}

var calls int = 0

func loopWithBreak() int {
	for i := 0; i < 5; i++ {
		r := Resource()
		if i == 2 {
			break
		}
	}
	return calls
}
`)
	// i=0: Resource() falls off the end of the body normally (1 call).
	// i=1: same (2 calls).
	// i=2: break fires the destructor for this iteration's r too (3 calls),
	// then exits the loop - iterations 3 and 4 never run at all.
	if got := jm.runInt32(t, "loopWithBreak"); got != 3 {
		t.Errorf("loopWithBreak() = %d, want 3 (fall-through at i=0,1 plus break at i=2)", got)
	}
}

// TestDestructorFiresOnContinue covers the third exit-path trigger's other
// half: a `continue` exiting the current iteration fires the destructor for
// everything declared so far that iteration, exactly like a break would,
// but the loop itself keeps running afterward.
func TestDestructorFiresOnContinue(t *testing.T) {
	jm := compileAndJIT(t, `
struct Resource {
	constructor() {
	}
	destructor() {
		calls = calls + 1
	}
}

var calls int = 0

func loopWithContinue() int {
	for i := 0; i < 4; i++ {
		r := Resource()
		if i == 1 {
			continue
		}
	}
	return calls
}
`)
	// Every one of the 4 iterations declares r exactly once - whether it
	// falls off the end (i=0,2,3) or hits continue (i=1), the destructor
	// fires exactly once per iteration either way, so all 4 iterations
	// still count once each.
	if got := jm.runInt32(t, "loopWithContinue"); got != 4 {
		t.Errorf("loopWithContinue() = %d, want 4 (one destructor call per iteration, fall-through or continue alike)", got)
	}
}

// TestDestructorReverseDeclarationOrder is the real ordering proof: three
// locals declared in one block destruct in reverse declaration order, not
// declaration order or some other arbitrary order - verified by having each
// destructor append its own id into a shared "order" global as a base-10
// digit, so the exact sequence of calls is directly observable, not just
// the count.
func TestDestructorReverseDeclarationOrder(t *testing.T) {
	jm := compileAndJIT(t, `
struct Resource {
	id int

	constructor(v int) {
		this.id = v
	}
	destructor() {
		order = order * 10 + this.id
	}
}

var order int = 0

func declareThree() int {
	a := Resource(1)
	b := Resource(2)
	c := Resource(3)
	return 0
}

func checkOrder() int {
	declareThree()
	return order
}
`)
	if got := jm.runInt32(t, "checkOrder"); got != 321 {
		t.Errorf("checkOrder() = %d, want 321 (c destructs first, then b, then a - reverse declaration order)", got)
	}
}

// TestDeleteCallsDestructorThenFrees covers `delete p`'s own destructor-
// then-free ordering (see LANGUAGE.md's "Pointers"/"Destructors" sections):
// the destructor must run *before* the free, while the pointee's memory is
// still valid - proven by having the destructor read a field of `this` and
// stash it into a global, then asserting that stashed value is exactly the
// constructed one (a use-after-free would typically read back zeroed or
// garbage memory instead, not reliably the original value).
func TestDeleteCallsDestructorThenFrees(t *testing.T) {
	jm := compileAndJIT(t, `
struct Resource {
	id int

	constructor(v int) {
		this.id = v
	}
	destructor() {
		lastSeenId = this.id
	}
}

var lastSeenId int = 0

func useDelete() int {
	p := new Resource(77)
	delete p
	return lastSeenId
}
`)
	if got := jm.runInt32(t, "useDelete"); got != 77 {
		t.Errorf("useDelete() = %d, want 77 (destructor must read the still-valid pointee before free runs)", got)
	}
}

// TestDeleteWithoutDestructorStillFrees is the regression case: `delete` on
// a pointer whose pointee type declares no destructor at all behaves
// exactly as it did before this feature - a plain free, no crash.
func TestDeleteWithoutDestructorStillFrees(t *testing.T) {
	jm := compileAndJIT(t, `
struct Point {
	x int
}

func f() int {
	p := new Point{5}
	sum := p.x
	delete p
	return sum
}
`)
	if got := jm.runInt32(t, "f"); got != 5 {
		t.Errorf("f() = %d, want 5", got)
	}
}

// TestDestructorFiresOnParameter covers the "parameter" half of "a plain
// local variable/parameter of a type with its own destructor()" - a
// by-value parameter fires its destructor at its own function's scope exit,
// exactly like a local would.
func TestDestructorFiresOnParameter(t *testing.T) {
	jm := compileAndJIT(t, `
struct Resource {
	constructor() {
	}
	destructor() {
		calls = calls + 1
	}
}

var calls int = 0

func consume(r Resource) int {
	return 1
}

func run() int {
	consume(Resource())
	return calls
}
`)
	if got := jm.runInt32(t, "run"); got != 1 {
		t.Errorf("run() = %d, want 1 (the parameter's own destructor must fire once consume returns)", got)
	}
}

// TestForLoopInitDeclaredDestructorFiresAtLoopExit is the regression test for
// the bug fixed alongside this test: a for-loop's init clause (`for r :=
// Resource(1); ...`) declares r in a scope that closes the moment the loop
// itself exits (see sema/resolve.go's own doc comment on this exact `for`
// scoping rule) - r's destructor must fire right there, at the loop's own
// exit, not get deferred all the way out to whatever statement follows the
// `for` in its enclosing block. Before the fix, genForStmt captured its
// destructor base *after* generating the init statement, so nothing ever
// unwound r's own entry back down at the loop's real exit point - it just
// sat on the flat function-scoped stack until `d`/`print(200)` had already
// run, producing the wrong order (100, 100, 200, 9, 3) instead of the
// correct one asserted here.
func TestForLoopInitDeclaredDestructorFiresAtLoopExit(t *testing.T) {
	jm := compileAndJIT(t, `
struct Resource {
	id int
	constructor(v int) {
		this.id = v
	}
	destructor() {
		print(this.id)
	}
}
func main() {
	for r := Resource(1); r.id < 3; r.id = r.id + 1 {
		print(100)
	}
	d := Resource(9)
	print(200)
}
`)
	out := captureStdout(t, func() {
		jm.runInt32(t, "main")
	})
	want := "100\n100\n3\n200\n9\n"
	if out != want {
		t.Fatalf("captured stdout = %q, want %q (r must destruct right at the loop's own exit, before d/print(200))", out, want)
	}
}

// TestForLoopInitDeclaredDestructorFiresOnBreak covers the same init-clause
// scoping rule via the loop's other real exit path - a `break` from inside
// the body, rather than the condition going false - confirming the fix
// covers both: endBB is the single unwind point common to both a natural
// condition-false fall-through and a break (break already branches to
// endBB), so both should behave identically.
func TestForLoopInitDeclaredDestructorFiresOnBreak(t *testing.T) {
	jm := compileAndJIT(t, `
struct Resource {
	id int
	constructor(v int) {
		this.id = v
	}
	destructor() {
		print(this.id)
	}
}
func main() {
	for r := Resource(1); r.id < 100; r.id = r.id + 1 {
		if r.id == 2 {
			break
		}
	}
	print(200)
}
`)
	out := captureStdout(t, func() {
		jm.runInt32(t, "main")
	})
	want := "2\n200\n"
	if out != want {
		t.Fatalf("captured stdout = %q, want %q (r must destruct exactly once, at the break's landing point, before print(200))", out, want)
	}
}

// TestForLoopInitDeclaredContinueDoesNotDestruct proves `continue` leaves the
// init-clause local completely alone: it never leaves the loop's own scope
// at all, so r must survive across iterations (never re-constructed, its
// mutated id carried forward) and destruct exactly once, at the loop's real
// exit, with its final value - not once per `continue`.
func TestForLoopInitDeclaredContinueDoesNotDestruct(t *testing.T) {
	jm := compileAndJIT(t, `
struct Resource {
	id int
	constructor(v int) {
		this.id = v
	}
	destructor() {
		print(this.id)
	}
}
func main() {
	for r := Resource(0); r.id < 3; r.id = r.id + 1 {
		if r.id == 1 {
			continue
		}
		print(r.id)
	}
	print(200)
}
`)
	out := captureStdout(t, func() {
		jm.runInt32(t, "main")
	})
	want := "0\n2\n3\n200\n"
	if out != want {
		t.Fatalf("captured stdout = %q, want %q (continue must not destruct r; it survives across iterations and destructs once, at the end, with its final value)", out, want)
	}
}

// TestForLoopPostDeclaredDestructorFiresPerIteration covers a for-loop's post
// clause declaring its own destructor-owning local (`for ...; ...; x :=
// Resource(i) {}` - legal since post is parsed via parseSimpleStmt, same as
// init). Unlike the loop body, which goes through genBlock and so already
// unwinds its own locals on every fall-through, post used to be generated via
// a bare genStmt with nothing unwinding its pushed entry in between - x's
// entries just accumulated on the flat stack across iterations, and only the
// loop's own final endBB unwind ever called a destructor, once, against
// whatever address the last iteration happened to leave behind. x must
// instead destruct once per iteration, right after that iteration's post
// runs, before the condition is re-checked.
func TestForLoopPostDeclaredDestructorFiresPerIteration(t *testing.T) {
	jm := compileAndJIT(t, `
struct Resource {
	id int
	constructor(v int) {
		this.id = v
	}
	destructor() {
		print(this.id)
	}
}
func main() {
	for i := 0; i < 3; x := Resource(i) {
		i = i + 1
	}
	print(200)
}
`)
	out := captureStdout(t, func() {
		jm.runInt32(t, "main")
	})
	want := "1\n2\n3\n200\n"
	if out != want {
		t.Fatalf("captured stdout = %q, want %q (x must destruct once per iteration, right after each post runs)", out, want)
	}
}

// TestForLoopPostDeclaredDestructorFiresOnContinue covers the post-clause fix
// against a `continue` (loopCtx.continueTarget is postBB, so continue skips
// straight to the post-clause without ever reaching the loop's own body-end)
// - x must still destruct exactly once per iteration even on the iteration
// that continued past its own print(100).
func TestForLoopPostDeclaredDestructorFiresOnContinue(t *testing.T) {
	jm := compileAndJIT(t, `
struct Resource {
	id int
	constructor(v int) {
		this.id = v
	}
	destructor() {
		print(this.id)
	}
}
func main() {
	for i := 0; i < 3; x := Resource(i) {
		i = i + 1
		if i == 2 {
			continue
		}
		print(100)
	}
	print(200)
}
`)
	out := captureStdout(t, func() {
		jm.runInt32(t, "main")
	})
	want := "100\n1\n2\n100\n3\n200\n"
	if out != want {
		t.Fatalf("captured stdout = %q, want %q (x must destruct once per iteration even when continue skips the rest of the body)", out, want)
	}
}
