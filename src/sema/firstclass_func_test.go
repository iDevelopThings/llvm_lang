package sema

import "testing"

// --- bare function name references (first-class function values) ---

func TestBareFuncNameIsTypedAsTypeFunc(t *testing.T) {
	src := "func add(x int, y int) int {\n\treturn x + y\n}\n" +
		"func f() {\n\tvar fn func(int, int) int = add\n}\n"
	tree, info := checkSrc(t, src)
	fDecl := tree.Children(tree.Root)[1]
	body := tree.Child(fDecl, 4)
	varDecl := tree.Child(body, 0)
	init := tree.Child(varDecl, 2) // the bare `add` reference

	got := info.Types[init]
	if got.Kind != TypeFunc {
		t.Fatalf("Types[init] = %v, want TypeFunc", got)
	}
	if len(got.Params) != 2 || got.Params[0].Kind != TypeI32 || got.Params[1].Kind != TypeI32 {
		t.Errorf("Params = %v, want [int, int]", got.Params)
	}
	if got.Return == nil || got.Return.Kind != TypeI32 {
		t.Errorf("Return = %v, want int", got.Return)
	}
}

func TestFuncValueAssignableToMatchingFuncTypedVar(t *testing.T) {
	src := "func add(x int, y int) int {\n\treturn x + y\n}\n" +
		"func f() {\n\tvar fn func(int, int) int = add\n}\n"
	checkSrc(t, src)
}

func TestFuncValueShortVarDeclInfersFuncType(t *testing.T) {
	src := "func add(x int, y int) int {\n\treturn x + y\n}\n" +
		"func f() {\n\tfn := add\n\tvar r int = fn(1, 2)\n}\n"
	checkSrc(t, src)
}

func TestFuncValueWrongSignatureIsAssignError(t *testing.T) {
	// add returns int; fn is declared func(int, int) bool - mismatched
	// return type, must be rejected same as any other assignability check.
	src := "func add(x int, y int) int {\n\treturn x + y\n}\n" +
		"func f() {\n\tvar fn func(int, int) bool = add\n}\n"
	expectCheckErrors(t, src, 1)
}

func TestFuncValueWrongParamCountIsAssignError(t *testing.T) {
	src := "func add(x int, y int) int {\n\treturn x + y\n}\n" +
		"func f() {\n\tvar fn func(int) int = add\n}\n"
	expectCheckErrors(t, src, 1)
}

func TestFuncTypedParamAssignable(t *testing.T) {
	src := "func inc(x int) int {\n\treturn x + 1\n}\n" +
		"func apply(fn func(int) int, x int) int {\n\treturn fn(x)\n}\n" +
		"func f() {\n\tvar r int = apply(inc, 5)\n}\n"
	checkSrc(t, src)
}

func TestFuncReturnedFromFuncIsUsable(t *testing.T) {
	src := "func inc(x int) int {\n\treturn x + 1\n}\n" +
		"func getInc() func(int) int {\n\treturn inc\n}\n" +
		"func f() {\n\tfn := getInc()\n\tvar r int = fn(5)\n}\n"
	checkSrc(t, src)
}

// --- calling through a function-typed value (indirect calls) ---

func TestCallThroughFuncTypedVarOk(t *testing.T) {
	src := "func add(x int, y int) int {\n\treturn x + y\n}\n" +
		"func f() {\n\tfn := add\n\tvar r int = fn(1, 2)\n}\n"
	checkSrc(t, src)
}

func TestCallThroughFuncTypedVarArgCountMismatch(t *testing.T) {
	src := "func add(x int, y int) int {\n\treturn x + y\n}\n" +
		"func f() {\n\tfn := add\n\tfn(1)\n}\n"
	expectCheckErrors(t, src, 1)
}

func TestCallThroughFuncTypedVarArgTypeMismatch(t *testing.T) {
	src := "func add(x int, y int) int {\n\treturn x + y\n}\n" +
		"func f() {\n\tfn := add\n\tfn(1, \"x\")\n}\n"
	expectCheckErrors(t, src, 1)
}

func TestCallThroughFuncTypedParamOk(t *testing.T) {
	src := "func apply(fn func(int) int, x int) int {\n\treturn fn(x)\n}\n"
	checkSrc(t, src)
}

func TestIndirectCalleeGetsInfoTypesEntry(t *testing.T) {
	// Unlike a direct call's callee (which deliberately gets no info.Types
	// entry - see funcSigForCall), an indirect call's callee is a real value
	// expression and must get one, since codegen needs it to actually
	// evaluate the function value before calling through it.
	src := "func add(x int, y int) int {\n\treturn x + y\n}\n" +
		"func f() {\n\tfn := add\n\tfn(1, 2)\n}\n"
	tree, info := checkSrc(t, src)
	fDecl := tree.Children(tree.Root)[1]
	body := tree.Child(fDecl, 4)
	exprStmt := tree.Child(body, 1) // fn(1, 2)
	call := tree.Child(exprStmt, 0)
	callee := tree.Child(call, 0) // fn

	got, ok := info.Types[callee]
	if !ok {
		t.Fatal("expected an info.Types entry for the indirect call's callee")
	}
	if got.Kind != TypeFunc {
		t.Errorf("Types[callee] = %v, want TypeFunc", got)
	}
}

// --- what's still rejected ---

func TestBareFuncNameUsedAsWrongTypeIsError(t *testing.T) {
	src := "func add(x int, y int) int {\n\treturn x + y\n}\n" +
		"func f() {\n\tvar a int = add\n}\n"
	expectCheckErrors(t, src, 1)
}

func TestBarePrintReferenceIsStillError(t *testing.T) {
	// print has no real declaration (it's predeclared - see universeScope),
	// so it has no signature to build a TypeFunc from; referencing it bare
	// remains an error, same as before this round.
	src := "func f() {\n\tvar a func(int) int = print\n}\n"
	expectCheckErrors(t, src, 1)
}

func TestBareMethodReferenceIsStillError(t *testing.T) {
	// Method values remain explicitly out of scope this round (see
	// LANGUAGE.md) - only a MemberExpr *call* (`p.move()`) is legal; `p.move`
	// alone must still be rejected exactly as before.
	src := pointWithMoveSrc + "func f() {\n\tp := Point{1, 2}\n\tvar a int = p.move\n}\n"
	expectCheckErrors(t, src, 1)
}

func TestFuncValueCannotBeReassigned(t *testing.T) {
	// A bare function name has nowhere to be assigned *to* - there's no
	// storage location backing it, only a fixed declaration.
	src := "func add(x int, y int) int {\n\treturn x + y\n}\n" +
		"func other(x int, y int) int {\n\treturn x - y\n}\n" +
		"func f() {\n\tadd = other\n}\n"
	expectCheckErrors(t, src, 1)
}

func TestCallingNonFuncTypedVarIsError(t *testing.T) {
	src := "func f() {\n\tvar a int = 1\n\ta(1, 2)\n}\n"
	expectCheckErrors(t, src, 1)
}

// --- structural equality of function types ---

func TestFuncTypesEqualByStructure(t *testing.T) {
	tree, info := checkSrc(t, "var a func(int, int) int\nvar b func(int, int) int\n")
	declA := tree.Children(tree.Root)[0]
	declB := tree.Children(tree.Root)[1]
	ta := info.Types[declA]
	tb := info.Types[declB]
	if !ta.Equal(tb) {
		t.Errorf("structurally identical func types should be Equal: %v vs %v", ta, tb)
	}
}

func TestFuncTypesUnequalByParamType(t *testing.T) {
	tree, info := checkSrc(t, "var a func(int) int\nvar b func(bool) int\n")
	declA := tree.Children(tree.Root)[0]
	declB := tree.Children(tree.Root)[1]
	ta := info.Types[declA]
	tb := info.Types[declB]
	if ta.Equal(tb) {
		t.Errorf("func types with different param types should not be Equal: %v vs %v", ta, tb)
	}
}

func TestVoidFuncTypeStringOmitsReturn(t *testing.T) {
	tree, info := checkSrc(t, "var a func(int)\n")
	decl := tree.Children(tree.Root)[0]
	got := info.Types[decl].String()
	if got != "func(int)" {
		t.Errorf("String() = %q, want %q", got, "func(int)")
	}
}
