package sema

import "testing"

// This file covers this round's Go-style multi-return values feature (see
// LANGUAGE.md's "Go-style multi-return values" section): the new
// TypeMultiReturn Type, the two consuming positions that are allowed to see
// it (a matching multi-value return, the sole right-hand side of a matching
// multi-target `:=`/`=`), and every explicitly out-of-scope case rejected
// with a clean diagnostic rather than a panic.

// --- the declared signature itself ---

func TestMultiReturnFuncSignature(t *testing.T) {
	src := "func divide(a int, b int) (int, bool) {\n" +
		"\tif b == 0 {\n\t\treturn 0, false\n\t}\n" +
		"\treturn a / b, true\n" +
		"}\n"
	tree, info := checkSrc(t, src)
	decl := tree.Children(tree.Root)[0]
	retNode := tree.FuncReturnType(decl)
	got := info.Types[retNode]
	if got.Kind != TypeMultiReturn {
		t.Fatalf("Types[retNode] = %v, want TypeMultiReturn", got)
	}
	if len(got.Params) != 2 || got.Params[0].Kind != TypeI32 || got.Params[1].Kind != TypeBool {
		t.Errorf("Params = %v, want [int, bool]", got.Params)
	}
}

func TestMultiReturnTypeStringsAsParenList(t *testing.T) {
	src := "func f() (int, bool) { return 0, true }\n"
	tree, info := checkSrc(t, src)
	decl := tree.Children(tree.Root)[0]
	got := info.Types[tree.FuncReturnType(decl)]
	if got.String() != "(int, bool)" {
		t.Errorf("String() = %q, want %q", got.String(), "(int, bool)")
	}
}

// A parenthesized return type needs at least 2 component types - the
// grammar itself accepts any count (see parser.parseFuncDeclReturnType), so
// this is sema's own job to reject. Each still produces one further
// cascading diagnostic on top of the "at least 2" one itself - a declared
// 1-type "multi-return" still counts as hasReturn/TypeMultiReturn for the
// rest of this pass, so a plain `return 0` no longer matches it (a second,
// genuinely distinct complaint, not a duplicate of the first) and an empty
// one's empty body is (correctly) still "missing return" - both real,
// individually-meaningful diagnostics, not one root cause reported twice.
func TestMultiReturnTypeRequiresAtLeastTwoTypes(t *testing.T) {
	expectCheckErrors(t, "func f() (int) { return 0 }\n", 2)
}

func TestMultiReturnTypeRequiresAtLeastTwoTypesEmpty(t *testing.T) {
	expectCheckErrors(t, "func f() () { }\n", 2)
}

// main declaring a multi-return type falls out of the existing
// checkMainReturnType check with no special-casing at all - it just isn't
// TypeVoid or TypeInt, so it hits the same "must return either nothing or
// int" diagnostic every other non-int/non-void return type already does.
func TestMultiReturnMainRejected(t *testing.T) {
	expectCheckErrors(t, "func main() (int, bool) { return 0, true }\n", 1)
}

// --- return statement matching ---

func TestMultiValueReturnOk(t *testing.T) {
	checkSrc(t, "func f() (int, bool) {\n\treturn 1, true\n}\n")
}

func TestMultiValueReturnWrongCount(t *testing.T) {
	expectCheckErrors(t, "func f() (int, bool) {\n\treturn 1, true, false\n}\n", 1)
}

func TestMultiValueReturnTooFewValues(t *testing.T) {
	expectCheckErrors(t, "func f() (int, bool) {\n\treturn 1\n}\n", 1)
}

func TestMultiValueReturnTypeMismatch(t *testing.T) {
	expectCheckErrors(t, "func f() (int, bool) {\n\treturn true, 1\n}\n", 2)
}

func TestMultiValueReturnFromSingleReturnFuncRejected(t *testing.T) {
	expectCheckErrors(t, "func f() int {\n\treturn 1, 2\n}\n", 1)
}

func TestMultiValueReturnFromVoidFuncRejected(t *testing.T) {
	expectCheckErrors(t, "func f() {\n\treturn 1, 2\n}\n", 1)
}

// --- destructuring: := ---

func TestDestructureShortVarDeclOk(t *testing.T) {
	src := "func divide(a int, b int) (int, bool) {\n" +
		"\tif b == 0 {\n\t\treturn 0, false\n\t}\n" +
		"\treturn a / b, true\n" +
		"}\n" +
		"func f() {\n\tq, ok := divide(10, 2)\n\tprint(q)\n\tprint(ok)\n}\n"
	checkSrc(t, src)
}

// Each destructured name gets its own individually-typed Symbol - proven by
// reading each one's own Type back via info.Types, keyed by its own Ident
// node (see checkMultiShortVarDeclNode).
func TestDestructureShortVarDeclNamesGetIndividualTypes(t *testing.T) {
	src := "func f() (int, bool) {\n\treturn 1, true\n}\n" +
		"func g() {\n\ta, b := f()\n\tprint(a)\n\tprint(b)\n}\n"
	tree, info := checkSrc(t, src)
	gDecl := tree.Children(tree.Root)[1]
	body := tree.FuncBody(gDecl)
	multiDecl := tree.Child(body, 0)
	names := tree.MultiShortVarDeclNames(multiDecl)
	if len(names) != 2 {
		t.Fatalf("MultiShortVarDeclNames = %v, want 2 names", names)
	}
	if got := info.Types[names[0]]; got.Kind != TypeI32 {
		t.Errorf("Types[a] = %v, want int", got)
	}
	if got := info.Types[names[1]]; got.Kind != TypeBool {
		t.Errorf("Types[b] = %v, want bool", got)
	}
}

func TestDestructureShortVarDeclWrongTargetCount(t *testing.T) {
	src := "func f() (int, bool) {\n\treturn 1, true\n}\n" +
		"func g() {\n\ta, b, c := f()\n}\n"
	expectCheckErrors(t, src, 1)
}

func TestDestructureShortVarDeclFromSingleReturnRejected(t *testing.T) {
	src := "func f() int {\n\treturn 1\n}\n" +
		"func g() {\n\ta, b := f()\n}\n"
	expectCheckErrors(t, src, 1)
}

func TestDestructureShortVarDeclFromNonCallRejected(t *testing.T) {
	// The explicitly out-of-scope Go-style parallel multi-assign
	// (`a, b := 1, 2`) can't even reach this check on a real parenthesized
	// value list (the parser only ever accepts one expression here - see
	// parser's own TestParallelMultiAssignRejectedCleanly), but a single
	// non-call expression on the right (e.g. a parenthesized one) must still
	// be rejected cleanly by sema, not just by accident of the grammar.
	src := "func g() {\n\ta, b := (1)\n}\n"
	expectCheckErrors(t, src, 1)
}

// --- destructuring: = ---

func TestDestructureAssignStmtOk(t *testing.T) {
	src := "func f() (int, bool) {\n\treturn 1, true\n}\n" +
		"func g() {\n\tvar a int\n\tvar b bool\n\ta, b = f()\n\tprint(a)\n\tprint(b)\n}\n"
	checkSrc(t, src)
}

// Proves the assignment form isn't special-cased to only plain identifiers -
// a MemberExpr and an IndexExpr target both work, exactly like AssignStmt's
// own single target already allows.
func TestDestructureAssignStmtNonIdentTargets(t *testing.T) {
	src := "struct Point {\n\tx int\n\ty bool\n}\n" +
		"func f() (int, bool) {\n\treturn 1, true\n}\n" +
		"func g() {\n\tp := Point{0, false}\n\tarr := [2]int{0, 0}\n\tp.x, arr[0] = f()\n}\n"
	// arr[0]'s own declared type is int; f()'s second component is bool -
	// this is deliberately a type mismatch on the second target, to prove
	// per-target assignability is actually checked (see below for the
	// well-typed matching version).
	expectCheckErrors(t, src, 1)
}

func TestDestructureAssignStmtNonIdentTargetsWellTyped(t *testing.T) {
	src := "struct Point {\n\tx int\n\ty bool\n}\n" +
		"func f() (int, bool) {\n\treturn 1, true\n}\n" +
		"func g() {\n\tp := Point{0, false}\n\tarr := [2]int{0, 0}\n\tarr[0], p.y = f()\n}\n"
	checkSrc(t, src)
}

func TestDestructureAssignStmtWrongTargetCount(t *testing.T) {
	src := "func f() (int, bool) {\n\treturn 1, true\n}\n" +
		"func g() {\n\tvar a int\n\tvar b bool\n\tvar c bool\n\ta, b, c = f()\n}\n"
	expectCheckErrors(t, src, 1)
}

// --- explicitly out of scope: a multi-return call used as a single value ---

func TestMultiReturnCallAsSingleShortVarDeclRejected(t *testing.T) {
	src := "func f() (int, bool) {\n\treturn 1, true\n}\n" +
		"func g() {\n\tx := f()\n}\n"
	expectCheckErrors(t, src, 1)
}

func TestMultiReturnCallAsArgumentRejected(t *testing.T) {
	src := "func f() (int, bool) {\n\treturn 1, true\n}\n" +
		"func g() {\n\tprint(f())\n}\n"
	expectCheckErrors(t, src, 1)
}

func TestMultiReturnCallAsSingleAssignTargetRejected(t *testing.T) {
	src := "func f() (int, bool) {\n\treturn 1, true\n}\n" +
		"func g() {\n\ta := 1\n\ta = f()\n}\n"
	expectCheckErrors(t, src, 1)
}
