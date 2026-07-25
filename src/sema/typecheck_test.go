package sema

import (
	"strings"
	"testing"

	"llvm_lang/src/ast"
	"llvm_lang/src/diag"
	"llvm_lang/src/lexer"
	"llvm_lang/src/parser"
)

// checkSrc parses, resolves, and type-checks src, failing the test if
// parsing or resolving produced a diagnostic (those aren't what's under
// test here) or if Check itself reported any error.
func checkSrc(t *testing.T, src string) (*ast.Tree, *Info) {
	t.Helper()
	tree, info, cdiags := checkSrcAllowErrors(t, src)
	if cdiags.HasErrors() {
		t.Fatalf("unexpected check errors for %q: %v", src, cdiags.All())
	}
	return tree, info
}

// checkSrcAllowErrors is checkSrc without asserting Check found nothing -
// for tests asserting a specific error count instead.
func checkSrcAllowErrors(t *testing.T, src string) (*ast.Tree, *Info, *diag.Bag) {
	t.Helper()
	tree, pdiags := parser.ParseFile(lexer.NewFile("t.ll", src), false)
	if pdiags.HasErrors() {
		t.Fatalf("unexpected parse errors for %q: %v", src, pdiags.All())
	}
	info, rdiags := Resolve(tree)
	if rdiags.HasErrors() {
		t.Fatalf("unexpected resolve errors for %q: %v", src, rdiags.All())
	}
	cdiags := Check(tree, info)
	return tree, info, cdiags
}

func expectCheckErrors(t *testing.T, src string, want int) *diag.Bag {
	t.Helper()
	_, _, cdiags := checkSrcAllowErrors(t, src)
	if cdiags.ErrorCount() != want {
		t.Fatalf("ErrorCount = %d, want %d: %v", cdiags.ErrorCount(), want, cdiags.All())
	}
	return cdiags
}

// --- literals & basic var decls ---

func TestIntLiteralType(t *testing.T) {
	tree, info := checkSrc(t, "var a int = 5\n")
	decl := tree.Children(tree.Root)[0]
	init := tree.Child(decl, 2)
	if got := info.Types[init]; got.Kind != TypeInt {
		t.Errorf("Types[init] = %v, want int", got)
	}
}

func TestFloatLiteralRejected(t *testing.T) {
	expectCheckErrors(t, "var a int = 1.5\n", 1)
}

func TestVarDeclMissingTypeAndInit(t *testing.T) {
	expectCheckErrors(t, "var a\n", 1)
}

func TestVarDeclTypeInferredFromInit(t *testing.T) {
	// A var with no declared type takes its initializer's type - there's
	// no expression node for the omitted annotation itself, so check it
	// through a later reference instead.
	src := "var a = 5\nfunc f() {\n\tb := a\n}\n"
	tree, info := checkSrc(t, src)
	fn := tree.Children(tree.Root)[1]
	body := tree.Child(fn, 5)
	short := tree.Child(body, 0)
	bType := info.Types[tree.Child(short, 1)]
	if bType.Kind != TypeInt {
		t.Errorf("b's inferred type = %v, want int", bType)
	}
}

func TestVarDeclTypeMismatch(t *testing.T) {
	expectCheckErrors(t, "var a int = \"x\"\n", 1)
}

func TestForwardReferenceTopLevelVarsTypeCheck(t *testing.T) {
	checkSrc(t, "var a int = b\nvar b int = 5\n")
}

func TestSelfReferentialVarDeclIsACycleError(t *testing.T) {
	expectCheckErrors(t, "var a int = a\n", 1)
}

// --- binary / unary operators ---

func TestIntArithmeticProducesInt(t *testing.T) {
	tree, info := checkSrc(t, "var a int = 1 + 2 * 3\n")
	decl := tree.Children(tree.Root)[0]
	init := tree.Child(decl, 2)
	if got := info.Types[init]; got.Kind != TypeInt {
		t.Errorf("Types[init] = %v, want int", got)
	}
}

func TestStringConcatenation(t *testing.T) {
	tree, info := checkSrc(t, "var a string = \"a\" + \"b\"\n")
	decl := tree.Children(tree.Root)[0]
	init := tree.Child(decl, 2)
	if got := info.Types[init]; got.Kind != TypeString {
		t.Errorf("Types[init] = %v, want string", got)
	}
}

func TestMismatchedArithmeticOperandsIsError(t *testing.T) {
	expectCheckErrors(t, "var a int = 1 + \"x\"\n", 1)
}

func TestSubtractionRejectsStrings(t *testing.T) {
	expectCheckErrors(t, "var a string = \"a\" - \"b\"\n", 1)
}

func TestComparisonProducesBool(t *testing.T) {
	tree, info := checkSrc(t, "var b bool = 1 < 2\n")
	decl := tree.Children(tree.Root)[0]
	init := tree.Child(decl, 2)
	if got := info.Types[init]; got.Kind != TypeBool {
		t.Errorf("Types[init] = %v, want bool", got)
	}
}

func TestEqualityAcrossDifferentTypesIsError(t *testing.T) {
	expectCheckErrors(t, "var b bool = 1 == \"x\"\n", 1)
}

func TestEqualityWorksForBool(t *testing.T) {
	checkSrc(t, "var b bool = true == false\n")
}

func TestEqualityWorksForSameStructType(t *testing.T) {
	src := pointSrc + "var b bool = Point{1, 2} == Point{1, 2}\n"
	checkSrc(t, src)
}

func TestEqualityWorksForSameArrayType(t *testing.T) {
	checkSrc(t, "var b bool = [2]int{1, 2} == [2]int{1, 2}\n")
}

func TestEqualityAcrossDifferentStructTypesIsError(t *testing.T) {
	src := pointSrc + "struct Other {\n\tx int\n\ty int\n}\n" +
		"var b bool = Point{1, 2} == Other{1, 2}\n"
	expectCheckErrors(t, src, 1)
}

func TestEqualityAcrossDifferentArraySizesIsError(t *testing.T) {
	expectCheckErrors(t, "var b bool = [2]int{1, 2} == [3]int{1, 2, 3}\n", 1)
}

func TestEqualityAcrossDifferentArrayElementTypesIsError(t *testing.T) {
	src := "var b bool = [1]int{1} == [1]bool{true}\n"
	expectCheckErrors(t, src, 1)
}

// --- struct/array equality nested-comparability gate (typeIsComparable) ---
//
// checkEqualityOperands's struct/array branch used to accept == between two
// same-typed structs/arrays via a bare whole-type Type.Equal check alone,
// never validating that every recursively-nested field/element Kind was
// actually something codegen's genValueEqual could lower - a struct field of
// TypeMap/TypeFunc reached genValueEqual's own unsupported-type panic, and a
// dynamic-array field silently compared as always-equal (genValueEqual's
// TypeArray case loops to t.Size, which a dynamic array never sets). See
// DECISIONS.md's dated entry for the full root-cause writeup.

func TestStructWithDynamicArrayFieldEqualityRejected(t *testing.T) {
	src := "struct S {\n\ts []int\n}\n" +
		"func f() {\n\ta := S{make([]int, 3)}\n\tb := S{make([]int, 3)}\n\tif a == b {\n\t}\n}\n"
	expectCheckErrors(t, src, 1)
}

func TestStructWithMapFieldEqualityRejected(t *testing.T) {
	src := "struct S {\n\tm map[string]int\n}\n" +
		"func f() {\n\ta := S{make(map[string]int)}\n\tb := S{make(map[string]int)}\n\tif a == b {\n\t}\n}\n"
	expectCheckErrors(t, src, 1)
}

func TestStructWithFuncFieldEqualityRejected(t *testing.T) {
	src := "func double(x int) int {\n\treturn x * 2\n}\n" +
		"struct Callback {\n\tfn func(int) int\n}\n" +
		"func f() {\n\ta := Callback{double}\n\tb := Callback{double}\n\tif a == b {\n\t}\n}\n"
	expectCheckErrors(t, src, 1)
}

func TestArrayOfDynamicArrayStructFieldEqualityRejected(t *testing.T) {
	// A dynamic array nested two levels deep (fixed array of a struct that
	// itself has a dynamic-array field) must still be caught - the
	// recursion has to actually walk through both levels, not just the
	// struct's own direct fields.
	src := "struct S {\n\ts []int\n}\n" +
		"func f() {\n\ta := [1]S{S{make([]int, 1)}}\n\tb := [1]S{S{make([]int, 1)}}\n\tif a == b {\n\t}\n}\n"
	expectCheckErrors(t, src, 1)
}

// A pointer field is still comparable - typeIsComparable's rejection is
// specific to a dynamic array/function type/map, not pointers in general.
func TestStructWithPointerFieldEqualityStillOk(t *testing.T) {
	src := "struct Box {\n\tp *int\n}\n" +
		"func f() {\n\ta := 1\n\tb := Box{&a}\n\tc := Box{&a}\n\tif b == c {\n\t}\n}\n"
	checkSrc(t, src)
}

func TestOrderingWorksForStrings(t *testing.T) {
	tree, info := checkSrc(t, "var b bool = \"a\" < \"b\"\n")
	decl := tree.Children(tree.Root)[0]
	init := tree.Child(decl, 2)
	if got := info.Types[init]; got.Kind != TypeBool {
		t.Errorf("Types[init] = %v, want bool", got)
	}
}

func TestOrderingRequiresIntOrString(t *testing.T) {
	expectCheckErrors(t, "var b bool = true < false\n", 1)
	expectCheckErrors(t, "var b bool = 1 < \"x\"\n", 1)
}

func TestLogicalOperatorsRequireBool(t *testing.T) {
	checkSrc(t, "var b bool = true && false\n")
	expectCheckErrors(t, "var b bool = 1 && true\n", 1)
}

func TestUnaryMinusRequiresInt(t *testing.T) {
	checkSrc(t, "var a int = -5\n")
	expectCheckErrors(t, "var a bool = -true\n", 1)
}

func TestUnaryNotRequiresBool(t *testing.T) {
	checkSrc(t, "var a bool = !true\n")
	expectCheckErrors(t, "var a bool = !5\n", 1)
}

// --- if/for conditions ---

func TestIfConditionMustBeBool(t *testing.T) {
	checkSrc(t, "func f() {\n\tif true {\n\t}\n}\n")
	expectCheckErrors(t, "func f() {\n\tif 1 {\n\t}\n}\n", 1)
}

func TestForConditionMustBeBool(t *testing.T) {
	checkSrc(t, "func f() {\n\tfor true {\n\t}\n}\n")
	expectCheckErrors(t, "func f() {\n\tfor 1 {\n\t}\n}\n", 1)
}

func TestForThreeClauseConditionMustBeBool(t *testing.T) {
	checkSrc(t, "func f() {\n\tfor i := 0; i < 10; i++ {\n\t}\n}\n")
	expectCheckErrors(t, "func f() {\n\tfor i := 0; i; i++ {\n\t}\n}\n", 1)
}

// --- return statements ---

func TestReturnTypeMustMatchDeclared(t *testing.T) {
	checkSrc(t, "func f() int {\n\treturn 1\n}\n")
	expectCheckErrors(t, "func f() int {\n\treturn \"x\"\n}\n", 1)
}

func TestBareReturnRequiresNoDeclaredReturnType(t *testing.T) {
	checkSrc(t, "func f() {\n\treturn\n}\n")
	expectCheckErrors(t, "func f() int {\n\treturn\n}\n", 1)
}

func TestReturnValueInVoidFunctionIsError(t *testing.T) {
	expectCheckErrors(t, "func f() {\n\treturn 1\n}\n", 1)
}

// --- main's return type ---

func TestMainWithNoDeclaredReturnTypeIsFine(t *testing.T) {
	checkSrc(t, "func main() {\n}\n")
}

func TestMainReturningIntIsFine(t *testing.T) {
	checkSrc(t, "func main() int {\n\treturn 0\n}\n")
}

func TestMainReturningOtherTypeIsError(t *testing.T) {
	expectCheckErrors(t, "func main() f64 {\n\treturn 1.5\n}\n", 1)
}

func TestMainReturningStringIsError(t *testing.T) {
	expectCheckErrors(t, "func main() string {\n\treturn \"x\"\n}\n", 1)
}

func TestNonMainFunctionMayReturnAnyType(t *testing.T) {
	// The restriction is specific to the real entry point - an ordinary
	// function named anything else may declare any return type.
	checkSrc(t, "func f() f64 {\n\treturn 1.5\n}\n")
}

// --- break / continue placement ---

func TestBreakOutsideLoopIsError(t *testing.T) {
	expectCheckErrors(t, "func f() {\n\tbreak\n}\n", 1)
}

func TestContinueOutsideLoopIsError(t *testing.T) {
	expectCheckErrors(t, "func f() {\n\tcontinue\n}\n", 1)
}

func TestBreakInsideForLoopIsFine(t *testing.T) {
	checkSrc(t, "func f() {\n\tfor {\n\t\tbreak\n\t}\n}\n")
}

func TestContinueInsideForLoopIsFine(t *testing.T) {
	checkSrc(t, "func f() {\n\tfor {\n\t\tcontinue\n\t}\n}\n")
}

func TestBreakInsideIfInsideLoopIsFine(t *testing.T) {
	checkSrc(t, "func f() {\n\tfor {\n\t\tif true {\n\t\t\tbreak\n\t\t}\n\t}\n}\n")
}

func TestBreakAfterLoopEndsIsError(t *testing.T) {
	src := "func f() {\n\tfor {\n\t\tbreak\n\t}\n\tbreak\n}\n"
	expectCheckErrors(t, src, 1)
}

// --- missing return (terminating-statement flow analysis) ---

func TestMissingReturnIsError(t *testing.T) {
	expectCheckErrors(t, "func f() int {\n}\n", 1)
}

func TestReturnEndingBodySatisfiesTermination(t *testing.T) {
	checkSrc(t, "func f() int {\n\treturn 1\n}\n")
}

func TestVoidFunctionNeedsNoTerminationCheck(t *testing.T) {
	checkSrc(t, "func f() {\n\tvar a int = 1\n}\n")
}

func TestIfElseBothReturningSatisfiesTermination(t *testing.T) {
	src := "func f() int {\n\tif true {\n\t\treturn 1\n\t} else {\n\t\treturn 2\n\t}\n}\n"
	checkSrc(t, src)
}

func TestIfWithNoElseIsNeverTerminating(t *testing.T) {
	src := "func f() int {\n\tif true {\n\t\treturn 1\n\t}\n}\n"
	expectCheckErrors(t, src, 1)
}

func TestOneLineIfWithNoElseIsNeverTerminating(t *testing.T) {
	src := "func f() int {\n\tif true: return 1\n}\n"
	expectCheckErrors(t, src, 1)
}

func TestNestedIfElseFullyCoveringAllPathsSatisfiesTermination(t *testing.T) {
	src := "func f(x int) int {\n" +
		"\tif x > 0 {\n" +
		"\t\tif x > 10 {\n" +
		"\t\t\treturn 1\n" +
		"\t\t} else {\n" +
		"\t\t\treturn 2\n" +
		"\t\t}\n" +
		"\t} else {\n" +
		"\t\treturn 3\n" +
		"\t}\n" +
		"}\n"
	checkSrc(t, src)
}

func TestInfiniteForWithNoBreakSatisfiesTermination(t *testing.T) {
	checkSrc(t, "func f() int {\n\tfor {\n\t}\n}\n")
}

func TestInfiniteForWithOwnBreakIsNotTerminating(t *testing.T) {
	src := "func f() int {\n\tfor {\n\t\tbreak\n\t}\n}\n"
	expectCheckErrors(t, src, 1)
}

func TestForWithCondNeverTerminates(t *testing.T) {
	src := "func f() int {\n\tfor true {\n\t\treturn 1\n\t}\n}\n"
	expectCheckErrors(t, src, 1)
}

// TestForWithBreakOnlyInNestedLoopStillTerminates covers the subtlety the
// task calls out explicitly: an outer infinite `for {}`'s own termination
// isn't affected by a `break` that actually targets a *nested* loop.
func TestForWithBreakOnlyInNestedLoopStillTerminates(t *testing.T) {
	src := "func f() int {\n" +
		"\tfor {\n" +
		"\t\tfor {\n" +
		"\t\t\tbreak\n" +
		"\t\t}\n" +
		"\t}\n" +
		"}\n"
	checkSrc(t, src)
}

// --- calls ---

func TestCallArgCountMismatch(t *testing.T) {
	src := "func add(x int, y int) int {\n\treturn x + y\n}\n" +
		"func f() {\n\tadd(1)\n}\n"
	expectCheckErrors(t, src, 1)
}

func TestCallArgTypeMismatch(t *testing.T) {
	src := "func add(x int, y int) int {\n\treturn x + y\n}\n" +
		"func f() {\n\tadd(1, \"x\")\n}\n"
	expectCheckErrors(t, src, 1)
}

func TestCallArgsCheckedOk(t *testing.T) {
	src := "func add(x int, y int) int {\n\treturn x + y\n}\n" +
		"func f() {\n\tvar z int = add(1, 2)\n}\n"
	checkSrc(t, src)
}

func TestForwardCallToLaterFunc(t *testing.T) {
	checkSrc(t, "func a() { b() }\nfunc b() { }\n")
}

func TestCallToNonFunctionIsError(t *testing.T) {
	expectCheckErrors(t, "var a int = 1\nfunc f() {\n\ta()\n}\n", 1)
}

func TestPrintAcceptsSingleArgAnyType(t *testing.T) {
	checkSrc(t, "func f() {\n\tprint(1)\n}\n")
	checkSrc(t, "func f() {\n\tprint(\"x\")\n}\n")
}

func TestPrintWrongArgCountIsError(t *testing.T) {
	expectCheckErrors(t, "func f() {\n\tprint(1, 2)\n}\n", 1)
}

// --- print's printability gate (typeIsPrintable) ---
//
// checkPrintCall used to accept "exactly one argument, of any type" with
// zero restriction, while codegen's genPrintCall/genPrintValueBare only ever
// implemented a fixed set of Kinds and panicked on a bare function value, a
// map value, or either nested inside a struct field. See DECISIONS.md's
// dated entry for the full root-cause writeup, and typeIsPrintable's own doc
// comment for why this is a strictly larger allowlist than typeIsComparable
// (a dynamic array is printable but never comparable).

func TestPrintBareFuncValueRejected(t *testing.T) {
	src := "func double(x int) int {\n\treturn x * 2\n}\n" +
		"func f() {\n\tprint(double)\n}\n"
	expectCheckErrors(t, src, 1)
}

func TestPrintBareMapValueRejected(t *testing.T) {
	src := "func f() {\n\tm := make(map[string]int)\n\tprint(m)\n}\n"
	expectCheckErrors(t, src, 1)
}

func TestPrintStructWithFuncFieldRejected(t *testing.T) {
	src := "func double(x int) int {\n\treturn x * 2\n}\n" +
		"struct Callback {\n\tfn func(int) int\n}\n" +
		"func f() {\n\tcb := Callback{double}\n\tprint(cb)\n}\n"
	expectCheckErrors(t, src, 1)
}

func TestPrintStructWithMapFieldRejected(t *testing.T) {
	src := "struct S {\n\tm map[string]int\n}\n" +
		"func f() {\n\ts := S{make(map[string]int)}\n\tprint(s)\n}\n"
	expectCheckErrors(t, src, 1)
}

// A dynamic array is still printable - genPrintArrayValue already renders
// one correctly, an existing feature typeIsPrintable must not regress even
// though the exact same shape is now rejected for ==/!= (typeIsComparable).
func TestPrintDynamicArrayStillOk(t *testing.T) {
	checkSrc(t, "func f() {\n\ts := make([]int, 3)\n\tprint(s)\n}\n")
}

// A bare pointer is legally printable - it's already comparable, and is
// included in typeIsPrintable's own base set too.
func TestPrintBarePointerOk(t *testing.T) {
	checkSrc(t, "func f() {\n\ta := 1\n\tprint(&a)\n}\n")
}

// A pointer field inside a struct is likewise still printable.
func TestPrintStructWithPointerFieldOk(t *testing.T) {
	src := "struct Box {\n\tp *int\n}\n" +
		"func f() {\n\ta := 1\n\tb := Box{&a}\n\tprint(b)\n}\n"
	checkSrc(t, src)
}

func TestVoidCallUsedAsValueIsError(t *testing.T) {
	src := "func f() {\n}\nfunc g() {\n\tvar a int = f()\n}\n"
	expectCheckErrors(t, src, 1)
}

func TestVoidCallAsStatementIsFine(t *testing.T) {
	checkSrc(t, "func f() {\n}\nfunc g() {\n\tf()\n}\n")
}

// --- assignment / inc-dec ---

func TestAssignTypeMismatch(t *testing.T) {
	src := "func f() {\n\tvar a int = 1\n\ta = \"x\"\n}\n"
	expectCheckErrors(t, src, 1)
}

func TestAssignOk(t *testing.T) {
	checkSrc(t, "func f() {\n\tvar a int = 1\n\ta = 2\n}\n")
}

func TestAssignToFunctionNameIsError(t *testing.T) {
	src := "func add() int {\n\treturn 1\n}\nfunc f() {\n\tadd = 1\n}\n"
	expectCheckErrors(t, src, 1)
}

func TestCompoundAssignOk(t *testing.T) {
	checkSrc(t, "func f() {\n\tvar a int = 1\n\ta += 2\n}\n")
	checkSrc(t, "func f() {\n\tvar a string = \"x\"\n\ta += \"y\"\n}\n")
}

func TestCompoundAssignTypeMismatch(t *testing.T) {
	src := "func f() {\n\tvar a string = \"x\"\n\ta -= \"y\"\n}\n"
	expectCheckErrors(t, src, 1)
}

func TestIncDecRequiresInt(t *testing.T) {
	checkSrc(t, "func f() {\n\tvar a int = 1\n\ta++\n}\n")
	expectCheckErrors(t, "func f() {\n\tvar a bool = true\n\ta++\n}\n", 1)
}

// --- index expressions ---

func TestIndexRequiresArrayTarget(t *testing.T) {
	src := "func f() {\n\tvar a int = 1\n\tvar b int = a[0]\n}\n"
	expectCheckErrors(t, src, 1)
}

func TestIndexRequiresIntIndex(t *testing.T) {
	src := "func f() {\n\tvar a [3]int = [3]int{1, 2, 3}\n\tvar b int = a[\"x\"]\n}\n"
	expectCheckErrors(t, src, 1)
}

func TestIndexOk(t *testing.T) {
	src := "func f() {\n\tvar a [3]int = [3]int{1, 2, 3}\n\tvar b int = a[0]\n}\n"
	checkSrc(t, src)
}

func TestIndexAssignOk(t *testing.T) {
	src := "func f() {\n\tvar a [3]int = [3]int{1, 2, 3}\n\ta[0] = 9\n}\n"
	checkSrc(t, src)
}

// --- arrays ---

func TestDynamicArrayTypeChecksFine(t *testing.T) {
	checkSrc(t, "var a []int\n")
}

func TestDynamicArrayAsParamAndReturnType(t *testing.T) {
	checkSrc(t, "func f(a []int) []int {\n\treturn a\n}\n")
}

func TestDynamicArrayAsStructField(t *testing.T) {
	checkSrc(t, "struct S {\n\ta []int\n}\n")
}

// --- make/append/len ---

func TestMakeTwoArgOk(t *testing.T) {
	checkSrc(t, "func f() {\n\tvar a []int = make([]int, 3)\n}\n")
}

func TestMakeThreeArgOk(t *testing.T) {
	checkSrc(t, "func f() {\n\tvar a []int = make([]int, 3, 5)\n}\n")
}

func TestMakeWrongArgCount(t *testing.T) {
	expectCheckErrors(t, "func f() {\n\tvar a []int = make([]int)\n}\n", 1)
	expectCheckErrors(t, "func f() {\n\tvar a []int = make([]int, 1, 2, 3)\n}\n", 1)
}

func TestMakeRequiresDynamicArrayType(t *testing.T) {
	expectCheckErrors(t, "func f() {\n\tvar a int = make(int, 3)\n}\n", 1)
}

func TestMakeSizeMustBeInt(t *testing.T) {
	expectCheckErrors(t, "func f() {\n\tvar a []int = make([]int, \"x\")\n}\n", 1)
}

func TestMakeSizeNeedNotBeConstant(t *testing.T) {
	// Unlike [N]T's N, make's n/cap are ordinary runtime expressions - see
	// LANGUAGE.md's "Dynamic arrays" section.
	checkSrc(t, "func f(n int) {\n\tvar a []int = make([]int, n)\n}\n")
}

// TestMakeShadowedAsOrdinaryFunctionReportsDiagnostic covers a real, once-
// panicking bug: shadowing the predeclared `make` with an ordinary
// same-named function (legal - see scope.go's universeScope) and then
// calling it with make's own bespoke argument grammar still in play (the
// parser's isMakeCallee dispatches purely on the callee's lexical spelling -
// see parser/expr.go - so it forces the first "argument" through
// parseTypeExpr into an ArrayType node regardless of what `make` actually
// resolves to). isBuiltinCall correctly sees through the shadowing here and
// falls through to the ordinary-call path, which must now report a real
// diagnostic for that stray value-position ArrayType instead of silently
// type-checking (via checkAssignable's "already invalid, don't re-report"
// rule) into something codegen has no case for and panics on. See
// compiler's TestCompilePackage_ShadowedMakeCheckError for the same
// scenario asserted end-to-end (the pipeline must stop here, before
// codegen ever runs).
func TestMakeShadowedAsOrdinaryFunctionReportsDiagnostic(t *testing.T) {
	src := "" +
		"func make(a int, b int) int {\n" +
		"\treturn a + b\n" +
		"}\n" +
		"\n" +
		"func main() int {\n" +
		"\treturn make([]int, 2)\n" +
		"}\n"
	diags := expectCheckErrors(t, src, 1)
	found := false
	for _, d := range diags.All() {
		if strings.Contains(d.Msg, "array type used as a value") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a diagnostic mentioning \"array type used as a value\", got: %v", diags.All())
	}
}

func TestAppendOk(t *testing.T) {
	checkSrc(t, "func f() {\n\ta := make([]int, 0)\n\ta = append(a, 1)\n}\n")
}

func TestAppendRequiresDynamicArray(t *testing.T) {
	expectCheckErrors(t, "func f() {\n\tvar a [3]int = [3]int{1, 2, 3}\n\ta = append(a, 4)\n}\n", 1)
}

func TestAppendElementTypeMismatch(t *testing.T) {
	expectCheckErrors(t, "func f() {\n\ta := make([]int, 0)\n\ta = append(a, \"x\")\n}\n", 1)
}

func TestAppendWrongArgCount(t *testing.T) {
	expectCheckErrors(t, "func f() {\n\ta := make([]int, 0)\n\ta = append(a)\n}\n", 1)
}

func TestLenOnDynamicArray(t *testing.T) {
	checkSrc(t, "func f() {\n\ta := make([]int, 3)\n\tvar n int = len(a)\n}\n")
}

func TestLenOnFixedArray(t *testing.T) {
	checkSrc(t, "func f() {\n\ta := [3]int{1, 2, 3}\n\tvar n int = len(a)\n}\n")
}

func TestLenOnString(t *testing.T) {
	checkSrc(t, "func f() {\n\tvar n int = len(\"hello\")\n}\n")
}

func TestLenOnUnsupportedType(t *testing.T) {
	expectCheckErrors(t, "func f() {\n\tvar n int = len(5)\n}\n", 1)
}

// TestLenWrongArgCountIsError covers checkLenCall's own argument-count gate
// (distinct from TestLenOnUnsupportedType's "wrong type" branch) - both too
// few and too many arguments must report exactly one error each, still
// type-checking whatever arguments were actually given.
func TestLenWrongArgCountIsError(t *testing.T) {
	expectCheckErrors(t, "func f() {\n\tvar n int = len()\n}\n", 1)
	expectCheckErrors(t, "func f() {\n\tvar a int = 1\n\tvar b int = 2\n\tvar n int = len(a, b)\n}\n", 1)
}

// --- args ---

// TestArgsCallOk covers the happy path: args() takes no arguments and
// returns []string, assignable to a []string-typed var exactly like any
// other value-producing expression.
func TestArgsCallOk(t *testing.T) {
	checkSrc(t, "func f() {\n\tvar a []string = args()\n}\n")
}

// TestArgsCallableFromAnywhere covers checkArgsCall's own claim (see
// LANGUAGE.md's "The args() builtin" section) that args() is callable from
// any function, not just main - a plain helper function works exactly the
// same as main would.
func TestArgsCallableFromAnywhere(t *testing.T) {
	checkSrc(t, "func helper() int {\n\treturn len(args())\n}\n\nfunc main() int {\n\treturn helper()\n}\n")
}

// TestArgsRejectsArguments covers checkArgsCall's own argument-count gate:
// args takes no arguments at all, unlike make/append/len.
func TestArgsRejectsArguments(t *testing.T) {
	expectCheckErrors(t, "func f() {\n\tvar a []string = args(1)\n}\n", 1)
}

// TestArgsResultElementIsString covers the exact result type args() returns
// - []string specifically, not just "some dynamic array" - indexing it must
// type-check as a string, and assigning that to a non-string var must fail.
func TestArgsResultElementIsString(t *testing.T) {
	checkSrc(t, "func f() {\n\tvar s string = args()[0]\n}\n")
	expectCheckErrors(t, "func f() {\n\tvar n int = args()[0]\n}\n", 1)
}

// TestArgsShadowedAsOrdinaryFunctionStillTypeChecks covers shadowing (legal
// - see scope.go's universeScope) the predeclared args with an ordinary
// same-named function: isBuiltinCall must correctly see through the
// shadowing (args resolves to the user's own SymFunc, with a real Decl, not
// the predeclared no-Decl one) and fall through to the ordinary-call path,
// checking the call against the shadowing function's own declared signature
// instead of args' own zero-argument rule.
func TestArgsShadowedAsOrdinaryFunctionStillTypeChecks(t *testing.T) {
	checkSrc(t, "func args(x int) int {\n\treturn x\n}\n\nfunc main() int {\n\treturn args(5)\n}\n")
}

func TestSliceCompositeLitOk(t *testing.T) {
	checkSrc(t, "func f() {\n\tvar a []int = []int{1, 2, 3}\n}\n")
}

func TestSliceEqualityRejected(t *testing.T) {
	expectCheckErrors(t, "func f() {\n\ta := make([]int, 1)\n\tb := make([]int, 1)\n\teq := a == b\n}\n", 1)
}

func TestArraySizeMustBeConstantLiteral(t *testing.T) {
	src := "var n int = 3\nvar a [n]int\n"
	expectCheckErrors(t, src, 1)
}

func TestArraySizeMustBePositive(t *testing.T) {
	expectCheckErrors(t, "var a [0]int\n", 1)
}

// TestArraySizeRejectsFloatLiteral covers constArraySize's own
// float-vs-int-literal gate: `[3.5]int` is still a bare NumberLit shape (so
// it doesn't hit the "must be a constant literal" branch
// TestArraySizeMustBeConstantLiteral covers), but its kind is
// TypeUntypedFloat, not TypeUntypedInt - a distinct rejection from either of
// the two above.
func TestArraySizeRejectsFloatLiteral(t *testing.T) {
	expectCheckErrors(t, "var a [3.5]int\n", 1)
}

func TestArrayCompositeLitCountMismatch(t *testing.T) {
	expectCheckErrors(t, "var a [3]int = [3]int{1, 2}\n", 1)
}

func TestArrayCompositeLitElementTypeMismatch(t *testing.T) {
	expectCheckErrors(t, "var a [2]int = [2]int{1, \"x\"}\n", 1)
}

func TestArrayCompositeLitOk(t *testing.T) {
	checkSrc(t, "var a [3]int = [3]int{1, 2, 3}\n")
}

func TestArrayCompositeLitKeyedElementsRejected(t *testing.T) {
	expectCheckErrors(t, "var a [2]int = [2]int{0: 1, 1: 2}\n", 2)
}

// TestCompositeLitOnNonAggregateTypeIsError covers checkCompositeLit's own
// outer dispatch - a composite literal whose type resolves cleanly (no
// resolve-time error at all - `bool` is a perfectly good builtin type
// symbol) but isn't a struct or array, the one shape neither
// checkStructCompositeLit nor checkArrayCompositeLit ever gets a chance to
// reject themselves.
func TestCompositeLitOnNonAggregateTypeIsError(t *testing.T) {
	expectCheckErrors(t, "func f() {\n\ta := bool{true}\n}\n", 1)
}

// --- structs, composite literals, member access, methods ---

const pointSrc = "struct Point {\n\tx int\n\ty int\n}\n"

func TestStructPositionalCompositeLitOk(t *testing.T) {
	checkSrc(t, pointSrc+"var p Point = Point{1, 2}\n")
}

func TestStructPositionalCompositeLitCountMismatch(t *testing.T) {
	expectCheckErrors(t, pointSrc+"var p Point = Point{1}\n", 1)
}

func TestStructPositionalElementTypeMismatch(t *testing.T) {
	expectCheckErrors(t, pointSrc+"var p Point = Point{1, \"y\"}\n", 1)
}

func TestStructKeyedCompositeLitOk(t *testing.T) {
	checkSrc(t, pointSrc+"var p Point = Point{x: 1, y: 2}\n")
}

func TestStructKeyedCompositeLitResolvesFieldName(t *testing.T) {
	src := pointSrc + "func f() {\n\tp := Point{x: 1, y: 2}\n}\n"
	tree, info := checkSrc(t, src)
	fn := tree.Children(tree.Root)[1]
	body := tree.Child(fn, 5)
	short := tree.Child(body, 0)
	lit := tree.Child(short, 1)
	kv := tree.Child(lit, 1) // `x: 1`
	key := tree.Child(kv, 0)

	sym, ok := info.Refs[key]
	if !ok {
		t.Fatal("expected the keyed element's field name to resolve")
	}
	structInfo := info.Structs["Point"]
	if sym != structInfo.Fields["x"] {
		t.Errorf("key resolved to %+v, want Point's x field symbol", sym)
	}
}

func TestStructCompositeLitUnknownFieldIsError(t *testing.T) {
	expectCheckErrors(t, pointSrc+"var p Point = Point{z: 1}\n", 1)
}

func TestStructCompositeLitDuplicateKeyIsError(t *testing.T) {
	expectCheckErrors(t, pointSrc+"var p Point = Point{x: 1, x: 2}\n", 1)
}

func TestStructCompositeLitMixedFormsIsError(t *testing.T) {
	expectCheckErrors(t, pointSrc+"var p Point = Point{1, y: 2}\n", 1)
}

// TestEmptyStructCompositeLitOk covers the fix for a fully-empty composite
// literal (`T{}`): checkStructCompositeLit's own arity check
// (`len(elems) != len(fields)`) always misfired against it before, since
// `keyed` short-circuits false for zero elements and 0 != len(fields) for
// any struct with at least one field. `T{}` must be unconditionally valid,
// zero-filling every field - it vacuously satisfies both the positional and
// keyed interpretations.
func TestEmptyStructCompositeLitOk(t *testing.T) {
	checkSrc(t, pointSrc+"var p Point = Point{}\n")
}

// TestEmptyStructCompositeLitOkSingleField covers the same fix against a
// single-field struct - `len(fields) == 1`, so the old bug's arity check
// (`0 != 1`) would have misfired here too, same as the multi-field case.
func TestEmptyStructCompositeLitOkSingleField(t *testing.T) {
	src := "struct Solo {\n\tx int\n}\n" + "var s Solo = Solo{}\n"
	checkSrc(t, src)
}

// TestEmptyStructCompositeLitOkSamePackage is the same case reached through
// a local variable inside a function rather than a top-level var, and
// through `:=` rather than a declared-type var - confirms the fix isn't
// somehow specific to one particular composite-literal call site.
func TestEmptyStructCompositeLitOkSamePackage(t *testing.T) {
	src := pointSrc + "func f() {\n\tp := Point{}\n}\n"
	checkSrc(t, src)
}

func TestMemberExprFieldAccessResolvesAndTypes(t *testing.T) {
	src := pointSrc + "func f() {\n\tp := Point{1, 2}\n\tvar a int = p.x\n}\n"
	tree, info := checkSrc(t, src)
	fn := tree.Children(tree.Root)[1]
	body := tree.Child(fn, 5)
	varDecl := tree.Child(body, 1)
	member := tree.Child(varDecl, 2)

	sym, ok := info.Refs[member]
	if !ok {
		t.Fatal("expected MemberExpr's field name to resolve")
	}
	structInfo := info.Structs["Point"]
	if sym != structInfo.Fields["x"] {
		t.Errorf("member resolved to %+v, want Point's x field symbol", sym)
	}
	if got := info.Types[member]; got.Kind != TypeInt {
		t.Errorf("Types[member] = %v, want int", got)
	}
}

func TestMemberExprUnknownFieldIsError(t *testing.T) {
	src := pointSrc + "func f() {\n\tp := Point{1, 2}\n\tvar a int = p.z\n}\n"
	expectCheckErrors(t, src, 1)
}

func TestMemberExprFieldTypeMismatch(t *testing.T) {
	src := pointSrc + "func f() {\n\tp := Point{1, 2}\n\tvar a string = p.x\n}\n"
	expectCheckErrors(t, src, 1)
}

func TestMemberAssignToField(t *testing.T) {
	src := pointSrc + "func f() {\n\tp := Point{1, 2}\n\tp.x = 9\n}\n"
	checkSrc(t, src)
}

const pointWithMoveSrc = pointSrc +
	"func (Point) translate(dx int, dy int) {\n" +
	"\tthis.x = this.x + dx\n" +
	"\tthis.y = this.y + dy\n" +
	"}\n"

// TestThisTypedAsReceiverStruct asserts `this`'s own bare type is *Point
// (TypePointer wrapping the receiver struct), not the bare struct type -
// checkThisExpr, sema/typecheck.go - matching what `this` already, literally
// is at the codegen level (the receiver parameter itself, a real pointer, no
// alloca of its own - see CODEGEN.md's "Method receivers" section). This is
// what makes `return this`/`x := this`/passing `this` as a *T argument
// type-check at all.
func TestThisTypedAsReceiverStruct(t *testing.T) {
	tree, info := checkSrc(t, pointWithMoveSrc)
	methodDecl := tree.Children(tree.Root)[1]
	body := tree.Child(methodDecl, 5)
	assign := tree.Child(body, 0)
	member := tree.Child(assign, 0)   // this.x
	thisExpr := tree.Child(member, 0) // this

	got := info.Types[thisExpr]
	if got.Kind != TypePointer || got.Elem == nil || got.Elem.Kind != TypeStruct || got.Elem.Struct.Symbol.Name != "Point" {
		t.Errorf("Types[this] = %v, want *Point", got)
	}
}

// TestThisFieldAndMethodAccessStillWorkAsPointer is a regression test for
// checkThisExpr's TypePointer change above: resolveMember's generic
// TypePointer auto-deref (the same mechanism an ordinary *T-typed local
// already goes through) must keep making this.field/this.method() work
// completely unchanged.
func TestThisFieldAndMethodAccessStillWorkAsPointer(t *testing.T) {
	src := pointWithMoveSrc + "func f() {\n\tp := Point{1, 2}\n\tp.translate(1, 1)\n\tvar a int = p.x\n}\n"
	checkSrc(t, src)
}

// TestReturnThisTypeChecksAgainstPointerReturnType covers `return this` from
// a method declaring a `*T` return type - the concrete motivating case for
// checkThisExpr's TypePointer change: previously this was rejected
// ("cannot use Point as *Point in return statement") even though `this` is
// already, literally, a pointer at the codegen level.
func TestReturnThisTypeChecksAgainstPointerReturnType(t *testing.T) {
	src := pointSrc + "func (Point) self() *Point {\n\treturn this\n}\n"
	checkSrc(t, src)
}

// TestThisAssignableToPointerLocalVar covers `this` assigned into a plain
// `*T`-typed local (`x := this`) - a bare `this` used as an ordinary value,
// not through `.field`/`.method()`.
func TestThisAssignableToPointerLocalVar(t *testing.T) {
	src := pointSrc + "func (Point) self() {\n\tp := this\n\tvar q *Point = p\n}\n"
	checkSrc(t, src)
}

// TestThisPassableAsPointerArgument covers `this` passed directly as a
// `*T`-typed function argument.
func TestThisPassableAsPointerArgument(t *testing.T) {
	src := pointSrc +
		"func identity(p *Point) *Point {\n\treturn p\n}\n" +
		"func (Point) self() *Point {\n\treturn identity(this)\n}\n"
	checkSrc(t, src)
}

func TestMethodCallOk(t *testing.T) {
	src := pointWithMoveSrc + "func f() {\n\tp := Point{1, 2}\n\tp.translate(1, 1)\n}\n"
	checkSrc(t, src)
}

func TestMethodCallArgCountMismatch(t *testing.T) {
	src := pointWithMoveSrc + "func f() {\n\tp := Point{1, 2}\n\tp.translate(1)\n}\n"
	expectCheckErrors(t, src, 1)
}

func TestMethodCallArgTypeMismatch(t *testing.T) {
	src := pointWithMoveSrc + "func f() {\n\tp := Point{1, 2}\n\tp.translate(1, \"x\")\n}\n"
	expectCheckErrors(t, src, 1)
}

func TestCallingFieldAsMethodIsError(t *testing.T) {
	src := pointWithMoveSrc + "func f() {\n\tp := Point{1, 2}\n\tp.x()\n}\n"
	expectCheckErrors(t, src, 1)
}

func TestAccessingMethodAsFieldIsError(t *testing.T) {
	src := pointWithMoveSrc + "func f() {\n\tp := Point{1, 2}\n\tvar a int = p.translate\n}\n"
	expectCheckErrors(t, src, 1)
}

func TestIndexingIntoNonStructMemberIsError(t *testing.T) {
	src := "func f() {\n\tvar a int = 1\n\tvar b int = a.x\n}\n"
	expectCheckErrors(t, src, 1)
}
