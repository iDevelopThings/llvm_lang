package sema

import (
	"testing"
)

// --- `yield` legality ---

// TestYieldOutsideMatchExpressionIsError covers checkYieldStmt's own
// c.matchExprStack-empty rejection - the stack-shaped counterpart to
// checkBreakOrContinue's "break outside a loop" (see LANGUAGE.md's "match"
// section's "match as an expression" subsection).
func TestYieldOutsideMatchExpressionIsError(t *testing.T) {
	expectCheckErrors(t, `
func f() {
	yield 5
}
`, 1)
}

// TestYieldInsideStatementModeMatchArmIsStillOutsideAnyMatchExpression
// covers a `yield` inside an ordinary statement-position match's own arm
// body - checkMatchStmt never pushes a matchExprCheckCtx frame at all (only
// checkMatchExprStmt does), so this is still "yield outside a match
// expression", not accidentally legal just because it's textually nested
// inside SOME match.
func TestYieldInsideStatementModeMatchArmIsStillOutsideAnyMatchExpression(t *testing.T) {
	expectCheckErrors(t, `
func f(x int) {
	match x {
		1 => {
			yield 5
		}
		_ => {
		}
	}
}
`, 1)
}

// --- mandatory yield-on-every-path ---

// TestMatchExprBlockArmMissingYieldIsError covers checkMatchExprArmBody's
// own "every reachable path must yield" rule: an `if` with no `else` can
// never terminate (mirroring isTerminatingStmt's own identical treatment),
// so falling off the end of the block without a final yield is a clean
// diagnostic, never silently accepted.
func TestMatchExprBlockArmMissingYieldIsError(t *testing.T) {
	expectCheckErrors(t, `
func f(x int, special bool) {
	y := match x {
		1 => {
			if special {
				yield "one-special"
			}
		}
		_ => "other"
	}
}
`, 1)
}

// TestMatchExprBlockArmYieldOnEveryIfBranchIsFine is
// TestMatchExprBlockArmMissingYieldIsError's positive counterpart: an if
// with BOTH branches yielding (an else present) does satisfy "every
// reachable path yields", mirroring isTerminatingStmt's own if/else rule.
func TestMatchExprBlockArmYieldOnEveryIfBranchIsFine(t *testing.T) {
	checkSrc(t, `
func f(x int, special bool) {
	y := match x {
		1 => {
			if special {
				yield "one-special"
			} else {
				yield "one"
			}
		}
		_ => "other"
	}
	print(y)
}
`)
}

// TestMatchExprBlockArmYieldAfterIfWithNoElseIsFine covers the
// LANGUAGE.md motivating example directly: an if with no else, followed by
// an unconditional yield - the if itself never terminates, but the block's
// own LAST statement (the trailing yield) does, which is all
// mustYieldEveryPath's own Block case requires (mirroring isTerminatingStmt's
// identical last-statement-only rule).
func TestMatchExprBlockArmYieldAfterIfWithNoElseIsFine(t *testing.T) {
	checkSrc(t, `
func f(x string, special bool) {
	y := match x {
		"s" => {
			if special {
				yield "small-but-special"
			}
			yield "small"
		}
		_ => "other"
	}
	print(y)
}
`)
}

// TestMatchExprArmEndingInReturnNeedsNoYield covers mustYieldEveryPath's own
// ReturnStmt case (identical to isTerminatingStmt's) - an arm whose only
// path exits via a bare `return` never needs its own yield, since control
// never comes back to consume the match expression's own result along that
// path at all (exactly like an `if` branch ending in `return` needs no
// further statement after it).
func TestMatchExprArmEndingInReturnNeedsNoYield(t *testing.T) {
	checkSrc(t, `
func f(x int) string {
	y := match x {
		1 => {
			return "early"
		}
		_ => "other"
	}
	return y
}
`)
}

// TestMatchExprNoArmEverYieldsIsError covers checkMatchExprStmt's own
// "no arm ever yields at all" guard - every arm here exits via return, so
// frame.resultTypeSet never becomes true; this must be its own clean
// diagnostic (not a silently invalid/zero type leaking into Info.Types -
// see AGENTS.md's review-process section on exactly this failure mode).
func TestMatchExprNoArmEverYieldsIsError(t *testing.T) {
	expectCheckErrors(t, `
func f(x int) string {
	y := match x {
		1 => {
			return "one"
		}
		_ => {
			return "other"
		}
	}
	return y
}
`, 1)
}

// --- result-type unification across arms ---

// TestMatchExprResultTypeUnifiesAcrossArms confirms the whole match
// expression's own Info.Types entry is the unified result type once every
// arm is checked (checkMatchExprStmt's own tail).
func TestMatchExprResultTypeUnifiesAcrossArms(t *testing.T) {
	tree, info := checkSrc(t, `
func f(x int) {
	y := match x {
		1, 2 => "low"
		_ => {
			yield "other"
		}
	}
	print(y)
}
`)
	fn := tree.Children(tree.Root)[0]
	body := tree.Child(fn, 4)
	short := tree.Child(body, 0)
	matchNode := tree.Child(short, 1)
	if got := info.Types[matchNode]; got.Kind != TypeString {
		t.Errorf("Info.Types[matchExpr] = %v, want string", got)
	}
}

// TestMatchExprIncompatibleYieldTypesIsError covers checkYieldStmt's own
// cross-arm unification failure: the first yield fixes the result type
// (string here); a later yield of an incompatible type (int) must be a
// clean diagnostic naming both types, not a panic or a silently wrong type.
func TestMatchExprIncompatibleYieldTypesIsError(t *testing.T) {
	expectCheckErrors(t, `
func f(x int) {
	y := match x {
		1 => "one"
		2 => {
			yield 2
		}
		_ => "other"
	}
	print(y)
}
`, 1)
}

// TestMatchExprUntypedYieldDefaults covers defaultIfUntyped's own
// integration with checkYieldStmt: an untyped-int yield (`yield 1`, no
// further type context anywhere) still defaults to i32 exactly like any
// other context that never supplies one.
func TestMatchExprUntypedYieldDefaults(t *testing.T) {
	tree, info := checkSrc(t, `
func f(x int) {
	y := match x {
		1 => 10
		_ => 20
	}
	print(y)
}
`)
	fn := tree.Children(tree.Root)[0]
	body := tree.Child(fn, 4)
	short := tree.Child(body, 0)
	matchNode := tree.Child(short, 1)
	if got := info.Types[matchNode]; got.Kind != TypeI32 {
		t.Errorf("Info.Types[matchExpr] = %v, want i32 (untyped-int default)", got)
	}
}

// --- nested match-expression frame isolation ---

// TestNestedMatchExprYieldTargetsItsOwnFrame covers checker.matchExprStack's
// own stacking (see its doc comment): a `yield match other {...}` pushes a
// SECOND frame for the inner match - the inner match's own yields (int)
// must never be checked against the outer match's own result type (string),
// and vice versa. If frame push/pop were wrong (e.g. sharing one frame,
// or popping the wrong one), this would either spuriously error or - worse -
// silently unify two unrelated types.
func TestNestedMatchExprYieldTargetsItsOwnFrame(t *testing.T) {
	tree, info := checkSrc(t, `
func f(x int, y int) {
	outer := match x {
		1 => {
			yield match y {
				1 => 10
				_ => 20
			}
		}
		_ => {
			yield 99
		}
	}
	print(outer)
}
`)
	fn := tree.Children(tree.Root)[0]
	body := tree.Child(fn, 4)
	short := tree.Child(body, 0)
	outerMatch := tree.Child(short, 1)
	if got := info.Types[outerMatch]; got.Kind != TypeI32 {
		t.Errorf("Info.Types[outerMatch] = %v, want i32", got)
	}
}

// TestNestedMatchExprOuterStringInnerIntNeverCrossUnifies is the negative
// mirror of TestNestedMatchExprYieldTargetsItsOwnFrame: if frame isolation
// were broken (inner yields checked against the outer's own frame), this
// would spuriously report a type-mismatch error between the outer's own
// "other"/"low" (string) arms and the inner match's own int yields - it must
// not, since they're two completely independent match expressions.
func TestNestedMatchExprOuterStringInnerIntNeverCrossUnifies(t *testing.T) {
	checkSrc(t, `
func f(x int, y int) {
	outer := match x {
		1 => {
			inner := match y {
				1 => 10
				_ => 20
			}
			print(inner)
			yield "used-inner-as-a-plain-local"
		}
		_ => "other"
	}
	print(outer)
}
`)
}

// --- composition with the existing enum/value-match machinery ---

// TestMatchExprEnumExhaustivenessStillEnforced confirms an expression-mode
// enum match still requires full coverage or a wildcard - checkMatchDispatch
// reuses checkEnumMatchStmt's entire exhaustiveness logic unchanged, so this
// composes for free (see LANGUAGE.md's "match" section).
func TestMatchExprEnumExhaustivenessStillEnforced(t *testing.T) {
	expectCheckErrors(t, `
enum Shape {
	Circle,
	Square
}
func f(s Shape) {
	y := match s {
		Shape.Circle => "circle"
	}
	print(y)
}
`, 1)
}

// TestMatchExprEnumFullCoverageNoWildcardIsFine is the positive mirror -
// every variant covered, no wildcard needed, exactly like the statement
// form already allows.
func TestMatchExprEnumFullCoverageNoWildcardIsFine(t *testing.T) {
	checkSrc(t, `
enum Shape {
	Circle,
	Square
}
func f(s Shape) {
	y := match s {
		Shape.Circle => "circle"
		Shape.Square => {
			yield "square"
		}
	}
	print(y)
}
`)
}

// TestMatchExprValueMatchWildcardStillMandatory confirms an expression-mode
// value-match still requires a mandatory wildcard `_` arm - the same
// deliberately-stricter-than-Go rule the statement form already enforces
// (checkValueMatchStmt, reused unchanged via checkMatchDispatch).
func TestMatchExprValueMatchWildcardStillMandatory(t *testing.T) {
	expectCheckErrors(t, `
func f(x int) {
	y := match x {
		1 => "one"
		2 => "two"
	}
	print(y)
}
`, 1)
}

// --- regression: the statement form is completely unaffected ---

// TestBareMatchStatementUnaffectedByMatchExpression is the sema-level
// regression companion to the parser's own
// TestBareMatchStmtStillStatementMode: an ordinary statement-position match
// (side-effecting arm bodies, no yield anywhere, no wildcard needed as long
// as every variant is covered) must still check cleanly exactly as it did
// before this round - checkMatchStmt is now a thin wrapper around
// checkMatchDispatch, but its own observable behavior is unchanged.
func TestBareMatchStatementUnaffectedByMatchExpression(t *testing.T) {
	checkSrc(t, `
enum Shape {
	Circle,
	Square
}
func f(s Shape) {
	match s {
		Shape.Circle => {
			print(1)
		}
		Shape.Square => {
			print(2)
		}
	}
}
`)
}
