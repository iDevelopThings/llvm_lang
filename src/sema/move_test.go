package sema

import (
	"testing"

	"llvm_lang/src/lexer"
	"llvm_lang/src/parser"
)

// expectErrorAnywhere is expectCheckErrors' own looser counterpart for a
// case whose error is expected to surface at the parse or resolve stage
// rather than Check's own - checkSrcAllowErrors fatals on those instead of
// returning them, since every other test in this package only ever expects
// a Check-phase diagnostic.
func expectErrorAnywhere(t *testing.T, src string) {
	t.Helper()
	tree, pdiags := parser.ParseFile(lexer.NewFile("t.ll", src))
	if pdiags.HasErrors() {
		return
	}
	info, rdiags := Resolve(tree)
	if rdiags.HasErrors() {
		return
	}
	if !Check(tree, info).HasErrors() {
		t.Fatalf("expected an error somewhere for %q, got none", src)
	}
}

// This file covers `move x` (see LANGUAGE.md's "Destructors" section's
// "move" subsection): the bare-identifier grammar shape is already covered
// at the parser layer (parser/move_test.go) - this covers move as the
// fresh-or-move exception at every checkNoIllegalCopy call site (including
// return, previously the one context allowing no exception at all),
// use-after-move flow tracking, the if/else and match convergence rules,
// and the "no moving a loop-external value from inside a loop" restriction.

// boxSrc is this file's own destructor-owning fixture struct - deliberately
// distinct from destructor_test.go's fileHandleSrc so this file's own
// constructor argument (an int, printed by the destructor) can double as a
// cheap "which Box is this" marker if a codegen-level test ever wants it.
const boxSrc = "struct Box {\n" +
	"\tv int\n\n" +
	"\tconstructor(x int) {\n" +
	"\t\tthis.v = x\n" +
	"\t}\n" +
	"\tdestructor() {\n" +
	"\t\tprint(this.v)\n" +
	"\t}\n" +
	"}\n"

const coroSrc = "async func Coro(v int) {\n\tawait\n}\n"

// --- valid: move accepted at every fresh-or-move call site, both for a
// destructor-owning struct and a coroutine handle ---

func TestMoveAcceptedAtVarDeclInit(t *testing.T) {
	checkSrc(t, boxSrc+"func f() {\n\ta := Box(1)\n\tb := move a\n\tprint(b.v)\n}\n")
}

func TestMoveAcceptedAtCompositeLitField(t *testing.T) {
	src := boxSrc +
		"struct Wrapper {\n\tb Box\n}\n" +
		"func f() {\n\ta := Box(1)\n\tw := Wrapper{move a}\n\tprint(w.b.v)\n}\n"
	checkSrc(t, src)
}

func TestMoveAcceptedAtArrayElement(t *testing.T) {
	src := boxSrc + "func f() {\n\ta := Box(1)\n\tarr := [1]Box{move a}\n\tprint(arr[0].v)\n}\n"
	checkSrc(t, src)
}

func TestMoveAcceptedAsFunctionArgument(t *testing.T) {
	src := boxSrc + "func consume(b Box) {\n\tprint(b.v)\n}\nfunc f() {\n\ta := Box(1)\n\tconsume(move a)\n}\n"
	checkSrc(t, src)
}

// TestMoveAcceptedAtReturn is the round's core unlock: a return statement
// used to allow no exception at all, even a fresh construction - `move x`
// now makes it legal.
func TestMoveAcceptedAtReturn(t *testing.T) {
	src := boxSrc + "func make() Box {\n\ta := Box(1)\n\treturn move a\n}\n"
	checkSrc(t, src)
}

func TestMoveAcceptedForCoroutineHandleAtVarDecl(t *testing.T) {
	src := coroSrc + "func f() {\n\th := Coro(1)\n\th2 := move h\n\tdelete h2\n}\n"
	checkSrc(t, src)
}

func TestMoveAcceptedForCoroutineHandleAsArgument(t *testing.T) {
	src := coroSrc + "func consume(h coroutine) {\n\tdelete h\n}\nfunc f() {\n\th := Coro(1)\n\tconsume(move h)\n}\n"
	checkSrc(t, src)
}

func TestMoveAcceptedForCoroutineHandleAtReturn(t *testing.T) {
	src := coroSrc + "func make() coroutine {\n\th := Coro(1)\n\treturn move h\n}\n"
	checkSrc(t, src)
}

// TestFactoryReturningFreshOrMovedValueUsableAtItsOwnCallSites is the round's
// own central reasoning check (see isFreshConstruction's CallExpr case): a
// function returning a fresh/moved non-copyable value type-checks as fresh
// at every one of ITS OWN call sites too - a var-decl init, a function
// argument, and nested inside another function's own return - with no
// annotation beyond makeBox's own successful compilation.
func TestFactoryReturningFreshOrMovedValueUsableAtItsOwnCallSites(t *testing.T) {
	src := boxSrc +
		"func makeBox(v int) Box {\n\treturn Box(v)\n}\n" +
		"func wrapper() Box {\n\treturn makeBox(5)\n}\n" +
		"func consume(b Box) {\n\tprint(b.v)\n}\n" +
		"func f() {\n" +
		"\tc := makeBox(1)\n" +
		"\tprint(c.v)\n" +
		"\tconsume(makeBox(2))\n" +
		"}\n"
	checkSrc(t, src)
}

// TestFactoryReturningMovedValueUsableAtItsOwnCallSites is the same check,
// one level over: makeBox itself returns via `move`, not a fresh
// construction directly - still just as fresh from any caller's own
// perspective.
func TestFactoryReturningMovedValueUsableAtItsOwnCallSites(t *testing.T) {
	src := boxSrc +
		"func makeBox(v int) Box {\n\ta := Box(v)\n\treturn move a\n}\n" +
		"func f() {\n\tc := makeBox(1)\n\tprint(c.v)\n}\n"
	checkSrc(t, src)
}

// --- valid: moving a copyable-typed value is a harmless no-op/plain read -
// no ownership to track, so no restriction applies (see checkMoveExpr's own
// doc comment for this deliberate choice) ---

func TestMoveOfCopyableTypeIsLegalPlainRead(t *testing.T) {
	checkSrc(t, "func f() {\n\ta := 5\n\tb := move a\n\tc := move a\n\tprint(b + c + a)\n}\n")
}

// --- invalid: any later reference to a moved-from value ---

func TestUseOfMovedValueReadIsError(t *testing.T) {
	src := boxSrc + "func f() {\n\ta := Box(1)\n\tb := move a\n\tprint(a.v)\n}\n"
	expectCheckErrors(t, src, 1)
}

func TestUseOfMovedValueSecondMoveIsError(t *testing.T) {
	src := boxSrc + "func f() {\n\ta := Box(1)\n\tb := move a\n\tc := move a\n}\n"
	expectCheckErrors(t, src, 1)
}

func TestUseOfMovedValueDeleteIsError(t *testing.T) {
	src := coroSrc + "func f() {\n\th := Coro(1)\n\th2 := move h\n\tdelete h\n\tdelete h2\n}\n"
	expectCheckErrors(t, src, 1)
}

func TestUseOfMovedValueAsAssignmentTargetIsError(t *testing.T) {
	src := boxSrc + "func f() {\n\ta := Box(1)\n\tb := move a\n\ta = Box(2)\n}\n"
	expectCheckErrors(t, src, 1)
}

// --- invalid/valid: if/else convergence ---

// TestMoveInOnlyOneIfBranchIsError covers the ambiguous-join case: moved on
// the `then` branch, not on the (implicit or explicit) `else`, both
// reachable afterward - "may already have been moved", rejected outright
// rather than reconciled (see DECISIONS.md's dated entry).
func TestMoveInOnlyOneIfBranchIsError(t *testing.T) {
	src := boxSrc + "func f(cond bool) {\n\ta := Box(1)\n\tif cond {\n\t\tb := move a\n\t\tprint(b.v)\n\t}\n\tprint(a.v)\n}\n"
	expectCheckErrors(t, src, 1)
}

func TestMoveInOneIfBranchOnlyWithNoUseAfterIsStillError(t *testing.T) {
	// Even with no explicit later read, falling off the end of f (an
	// implicit scope-exit) is exactly the ambiguous join this rule exists to
	// catch - see checkIfStmt's own merge, raised right at the if/else join,
	// not deferred to a later read that may not even exist textually.
	src := boxSrc + "func f(cond bool) {\n\ta := Box(1)\n\tif cond {\n\t\tb := move a\n\t\tprint(b.v)\n\t}\n}\n"
	expectCheckErrors(t, src, 1)
}

// TestMoveInBothIfBranchesIsFine is the accepted symmetric case - both
// branches move it, so the post-if state is unambiguous either way.
func TestMoveInBothIfBranchesIsFine(t *testing.T) {
	src := boxSrc + "func f(cond bool) {\n\ta := Box(1)\n\tif cond {\n\t\tb := move a\n\t\tprint(b.v)\n\t} else {\n\t\tc := move a\n\t\tprint(c.v)\n\t}\n}\n"
	checkSrc(t, src)
}

// TestMoveInOneIfBranchThatReturnsIsFine covers the other accepted case: the
// branch that moves it also unconditionally returns (or breaks/continues),
// so it never reaches the join at all - nothing to reconcile.
func TestMoveInOneIfBranchThatReturnsIsFine(t *testing.T) {
	src := boxSrc + "func f(cond bool) {\n\ta := Box(1)\n\tif cond {\n\t\tb := move a\n\t\tprint(b.v)\n\t\treturn\n\t}\n\tprint(a.v)\n}\n"
	checkSrc(t, src)
}

// TestMoveInLoopIfBranchThatBreaksIsStillError is the loop counterpart to
// TestMoveInOneIfBranchThatReturnsIsFine - but here it's still rejected: `a`
// is declared BEFORE the loop, and this project's chosen loop restriction
// (moveState.declLoopDepth) rejects moving any loop-external value inside a
// loop unconditionally, regardless of break placement - a deliberately
// simpler, stricter rule than a real per-iteration fixed point (see
// DECISIONS.md's dated entry).
func TestMoveInLoopIfBranchThatBreaksIsStillError(t *testing.T) {
	src := boxSrc + "func f(cond bool) {\n\ta := Box(1)\n\tfor {\n\t\tif cond {\n\t\t\tb := move a\n\t\t\tprint(b.v)\n\t\t\tbreak\n\t\t}\n\t\tbreak\n\t}\n}\n"
	expectCheckErrors(t, src, 1)
}

// --- invalid/valid: match convergence, one level over from if/else ---

func TestMoveInOnlyOneMatchArmIsError(t *testing.T) {
	src := boxSrc +
		"enum Shape {\n\tCircle,\n\tSquare\n}\n" +
		"func f(s Shape) {\n\ta := Box(1)\n\tmatch s {\n\t\tShape.Circle => {\n\t\t\tb := move a\n\t\t\tprint(b.v)\n\t\t}\n\t\tShape.Square => {\n\t\t}\n\t}\n\tprint(a.v)\n}\n"
	expectCheckErrors(t, src, 1)
}

func TestMoveInEveryMatchArmIsFine(t *testing.T) {
	src := boxSrc +
		"enum Shape {\n\tCircle,\n\tSquare\n}\n" +
		"func f(s Shape) {\n\ta := Box(1)\n\tmatch s {\n\t\tShape.Circle => {\n\t\t\tb := move a\n\t\t\tprint(b.v)\n\t\t}\n\t\tShape.Square => {\n\t\t\tc := move a\n\t\t\tprint(c.v)\n\t\t}\n\t}\n}\n"
	checkSrc(t, src)
}

// --- invalid: moving a value declared outside the current loop ---

// TestMoveInLoopOfOuterDeclaredValueIsError covers moving a variable
// declared before a loop, from inside the loop's own body - rejected
// outright regardless of break placement (see moveState.declLoopDepth's own
// doc comment): a later iteration could then move an already-moved value.
func TestMoveInLoopOfOuterDeclaredValueIsError(t *testing.T) {
	src := boxSrc + "func f() {\n\ta := Box(1)\n\tfor i := 0; i < 3; i++ {\n\t\tb := move a\n\t\tprint(b.v)\n\t\tbreak\n\t}\n}\n"
	expectCheckErrors(t, src, 1)
}

// TestMoveInLoopOfOwnLoopDeclaredValueIsFine covers the accepted
// counterpart: a value declared INSIDE the loop body is fresh every
// iteration, so moving it there is unrestricted.
func TestMoveInLoopOfOwnLoopDeclaredValueIsFine(t *testing.T) {
	src := boxSrc + "func f() {\n\tfor i := 0; i < 3; i++ {\n\t\ta := Box(i)\n\t\tb := move a\n\t\tprint(b.v)\n\t}\n}\n"
	checkSrc(t, src)
}

// --- invalid: move restricted to a bare identifier naming a local
// var/param - a struct field or array element is rejected at the parser
// layer (see parser/move_test.go); this just confirms the full pipeline
// (parse+resolve+check) still reports an error for each, end to end. ---

func TestMoveOfFieldIsError(t *testing.T) {
	src := boxSrc + "struct Wrapper {\n\tb Box\n}\nfunc f() {\n\tw := Wrapper{Box(1)}\n\tc := move w.b\n\tprint(c.v)\n}\n"
	expectErrorAnywhere(t, src)
}

func TestMoveOfArrayElementIsError(t *testing.T) {
	src := boxSrc + "func f() {\n\tarr := [1]Box{Box(1)}\n\tc := move arr[0]\n\tprint(c.v)\n}\n"
	expectErrorAnywhere(t, src)
}

// --- invalid: moving an undefined name ---

func TestMoveOfUndefinedNameIsError(t *testing.T) {
	src := "func f() {\n\tb := move doesNotExist\n\tprint(b)\n}\n"
	expectErrorAnywhere(t, src)
}

// --- invalid: moving a symbol captured by a lambda ---

func TestMoveOfCapturedValueIsError(t *testing.T) {
	src := boxSrc + "func f() {\n\ta := Box(1)\n\tcb := func() {\n\t\tprint(a.v)\n\t}\n\tb := move a\n\tprint(b.v)\n\tcb()\n}\n"
	expectCheckErrors(t, src, 1)
}

// --- invalid: moving a value into its own overwrite ---

func TestSelfMoveIntoOwnAssignmentTargetIsError(t *testing.T) {
	src := boxSrc + "func f() {\n\ta := Box(1)\n\ta = move a\n}\n"
	expectCheckErrors(t, src, 1)
}
