package sema

import (
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
	tree, pdiags := parser.ParseFile(lexer.NewFile("t.ll", src))
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
	body := tree.Child(fn, 4)
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

func TestDynamicArrayIsRejected(t *testing.T) {
	expectCheckErrors(t, "var a []int\n", 1)
}

func TestArraySizeMustBeConstantLiteral(t *testing.T) {
	src := "var n int = 3\nvar a [n]int\n"
	expectCheckErrors(t, src, 1)
}

func TestArraySizeMustBePositive(t *testing.T) {
	expectCheckErrors(t, "var a [0]int\n", 1)
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
	body := tree.Child(fn, 4)
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

func TestMemberExprFieldAccessResolvesAndTypes(t *testing.T) {
	src := pointSrc + "func f() {\n\tp := Point{1, 2}\n\tvar a int = p.x\n}\n"
	tree, info := checkSrc(t, src)
	fn := tree.Children(tree.Root)[1]
	body := tree.Child(fn, 4)
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
	"func (Point) move(dx int, dy int) {\n" +
	"\tthis.x = this.x + dx\n" +
	"\tthis.y = this.y + dy\n" +
	"}\n"

func TestThisTypedAsReceiverStruct(t *testing.T) {
	tree, info := checkSrc(t, pointWithMoveSrc)
	methodDecl := tree.Children(tree.Root)[1]
	body := tree.Child(methodDecl, 4)
	assign := tree.Child(body, 0)
	member := tree.Child(assign, 0)   // this.x
	thisExpr := tree.Child(member, 0) // this

	got := info.Types[thisExpr]
	if got.Kind != TypeStruct || got.Struct.Symbol.Name != "Point" {
		t.Errorf("Types[this] = %v, want struct Point", got)
	}
}

func TestMethodCallOk(t *testing.T) {
	src := pointWithMoveSrc + "func f() {\n\tp := Point{1, 2}\n\tp.move(1, 1)\n}\n"
	checkSrc(t, src)
}

func TestMethodCallArgCountMismatch(t *testing.T) {
	src := pointWithMoveSrc + "func f() {\n\tp := Point{1, 2}\n\tp.move(1)\n}\n"
	expectCheckErrors(t, src, 1)
}

func TestMethodCallArgTypeMismatch(t *testing.T) {
	src := pointWithMoveSrc + "func f() {\n\tp := Point{1, 2}\n\tp.move(1, \"x\")\n}\n"
	expectCheckErrors(t, src, 1)
}

func TestCallingFieldAsMethodIsError(t *testing.T) {
	src := pointWithMoveSrc + "func f() {\n\tp := Point{1, 2}\n\tp.x()\n}\n"
	expectCheckErrors(t, src, 1)
}

func TestAccessingMethodAsFieldIsError(t *testing.T) {
	src := pointWithMoveSrc + "func f() {\n\tp := Point{1, 2}\n\tvar a int = p.move\n}\n"
	expectCheckErrors(t, src, 1)
}

func TestIndexingIntoNonStructMemberIsError(t *testing.T) {
	src := "func f() {\n\tvar a int = 1\n\tvar b int = a.x\n}\n"
	expectCheckErrors(t, src, 1)
}
