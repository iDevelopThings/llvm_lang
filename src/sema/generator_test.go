package sema

import (
	"testing"

	"llvm_lang/src/ast"
	"llvm_lang/src/enums"
)

// findSoleRangeForStmt locates the sole RangeForStmt anywhere in tree - the
// generator-capture test's own counterpart to rangefor_test.go's
// rangeForBindings, which returns the bindings rather than the node itself.
func findSoleRangeForStmt(t *testing.T, tree *ast.Tree) ast.NodeIndex {
	t.Helper()
	for idx := 1; idx < len(tree.Nodes); idx++ {
		n := ast.NodeIndex(idx)
		if tree.Nodes[n].Kind == enums.NodeKinds.RangeForStmt {
			return n
		}
	}
	t.Fatal("no RangeForStmt found in tree")
	return ast.InvalidNode
}

// This file covers `func Name(params) yield T { ... }` generator functions
// (see LANGUAGE.md's "Generator functions" section): the return-type marker's
// own legality (top-level FuncDecl only, never a method), `yield expr`'s
// generalized legality (match expression OR generator, never neither),
// `return`'s restricted legality inside a generator's own body (bare only),
// consuming one via `for [v] := range Gen(args) { ... }` (zero/one-binding
// only, direct calls only), and the explicit out-of-scope restrictions this
// round doesn't build real support for (nested generator composition, a
// generator value used anywhere but a range-for's own subject).

// --- declaring a generator ---

func TestGeneratorFuncDeclIsFine(t *testing.T) {
	checkSrc(t, "func Range(a int, b int) yield int {\n\tfor i := a; i < b; i = i + 1 {\n\t\tyield i\n\t}\n}\n")
}

func TestGeneratorMethodReceiverIsError(t *testing.T) {
	src := "struct Point {\n\tx int\n}\n\nfunc (Point) Values() yield int {\n\tyield this.x\n}\n"
	expectCheckErrors(t, src, 1)
}

// TestGeneratorMissingReturnNotRequired proves a generator's own body ending
// without a `return` needs no "missing return" diagnostic - treated exactly
// like an ordinary void function's body (checkFuncDecl's hasReturn stays
// false for a generator).
func TestGeneratorMissingReturnNotRequired(t *testing.T) {
	checkSrc(t, "func Range(a int, b int) yield int {\n\tfor i := a; i < b; i = i + 1 {\n\t\tyield i\n\t}\n}\n")
}

// --- return inside a generator's own body ---

func TestGeneratorBareReturnIsFine(t *testing.T) {
	checkSrc(t, "func Range(a int, b int) yield int {\n\tyield a\n\treturn\n}\n")
}

func TestGeneratorReturnWithValueIsError(t *testing.T) {
	src := "func Range(a int, b int) yield int {\n\tyield a\n\treturn a\n}\n"
	expectCheckErrors(t, src, 1)
}

// --- yield's generalized legality ---

func TestYieldOutsideMatchOrGeneratorIsError(t *testing.T) {
	src := "func f(x int) {\n\tyield x\n}\n"
	expectCheckErrors(t, src, 1)
}

func TestYieldInsideGeneratorTypeMismatchIsError(t *testing.T) {
	src := "func Range(a int, b int) yield int {\n\tyield true\n}\n"
	expectCheckErrors(t, src, 1)
}

func TestYieldInsideMatchExprTakesPriorityOverGenerator(t *testing.T) {
	// A yield inside a still-innermost match-expression arm always targets
	// that match, never the enclosing generator - even though the enclosing
	// function is itself a generator, this should type-check as an ordinary
	// match expression yielding a bool, completely unrelated to Range's own
	// declared int element type.
	src := "func Range(a int, b int) yield int {\n" +
		"\tb := match a {\n" +
		"\t\t1 => { yield true }\n" +
		"\t\t_ => { yield false }\n" +
		"\t}\n" +
		"\tyield a\n" +
		"}\n"
	checkSrc(t, src)
}

// --- consuming a generator via range-for ---

func TestGeneratorRangeForOneBindingIsFine(t *testing.T) {
	tree, info := checkSrc(t, "func Range(a int, b int) yield int {\n\tyield a\n}\n\nfunc f() {\n\tfor v := range Range(1, 10) {\n\t}\n}\n")
	key, value := rangeForBindings(t, tree)
	if value != 0 {
		t.Fatalf("value slot = %d, want InvalidNode (generator has no key/index binding)", value)
	}
	if got := info.Types[key]; got.Kind != TypeI32 {
		t.Errorf("bound name type = %v, want int (the yielded element type)", got)
	}
}

func TestGeneratorRangeForZeroBindingIsFine(t *testing.T) {
	checkSrc(t, "func Range(a int, b int) yield int {\n\tyield a\n}\n\nfunc f() {\n\tfor range Range(1, 10) {\n\t}\n}\n")
}

func TestGeneratorRangeForTwoBindingIsError(t *testing.T) {
	src := "func Range(a int, b int) yield int {\n\tyield a\n}\n\nfunc f() {\n\tfor k, v := range Range(1, 10) {\n\t}\n}\n"
	expectCheckErrors(t, src, 1)
}

func TestGeneratorRangeForIndirectCallIsError(t *testing.T) {
	// Ranging over a stored function value (rather than calling the
	// generator directly by name) is rejected - codegen's own lowering needs
	// the callee's real FuncDecl-based signature to append the synthesized
	// callback argument to.
	src := "func Range(a int, b int) yield int {\n\tyield a\n}\n\nfunc f() {\n\tg := Range\n\tfor v := range g(1, 10) {\n\t}\n}\n"
	expectCheckErrors(t, src, 1)
}

// --- a generator value used anywhere but a range-for's own subject ---

func TestGeneratorValueStoredInVariableIsError(t *testing.T) {
	src := "func Range(a int, b int) yield int {\n\tyield a\n}\n\nfunc f() {\n\tx := Range(1, 10)\n}\n"
	expectCheckErrors(t, src, 1)
}

func TestGeneratorValuePassedAsArgumentIsError(t *testing.T) {
	src := "func Range(a int, b int) yield int {\n\tyield a\n}\n\nfunc useGen(g int) {\n}\n\nfunc f() {\n\tuseGen(Range(1, 10))\n}\n"
	expectCheckErrors(t, src, 1)
}

func TestGeneratorValueReturnedIsError(t *testing.T) {
	src := "func Range(a int, b int) yield int {\n\tyield a\n}\n\nfunc f() int {\n\treturn Range(1, 10)\n}\n"
	expectCheckErrors(t, src, 1)
}

func TestGeneratorValueAsBareStatementIsError(t *testing.T) {
	src := "func Range(a int, b int) yield int {\n\tyield a\n}\n\nfunc f() {\n\tRange(1, 10)\n}\n"
	expectCheckErrors(t, src, 1)
}

// --- explicitly out of scope this round ---

func TestNestedGeneratorCompositionIsError(t *testing.T) {
	src := "func Inner(a int, b int) yield int {\n\tyield a\n}\n\nfunc Outer(a int, b int) yield int {\n\tfor v := range Inner(a, b) {\n\t\tyield v\n\t}\n}\n"
	expectCheckErrors(t, src, 1)
}

// TestReturnInsideGeneratorConsumingRangeForIsError proves a `return` reached
// directly inside a generator-consuming range-for's own body is rejected -
// that body becomes a genuinely separate callback function at codegen time,
// which has no way to make the real enclosing function return early (true
// non-local return isn't implemented this round).
func TestReturnInsideGeneratorConsumingRangeForIsError(t *testing.T) {
	src := "func Range(a int, b int) yield int {\n\tyield a\n}\n\nfunc f() int {\n\tfor v := range Range(1, 10) {\n\t\treturn v\n\t}\n\treturn 0\n}\n"
	expectCheckErrors(t, src, 1)
}

// TestReturnInsideFuncLitInsideGeneratorRangeForIsFine proves the return
// restriction above is scoped correctly: a return inside a FuncLit nested
// inside the range-for's own body targets that lambda's own frame, not the
// enclosing real function, so it's completely unaffected.
func TestReturnInsideFuncLitInsideGeneratorRangeForIsFine(t *testing.T) {
	src := "func Range(a int, b int) yield int {\n\tyield a\n}\n\nfunc f() {\n\tfor v := range Range(1, 10) {\n\t\tadd := func(x int) int {\n\t\t\treturn x + 1\n\t\t}\n\t\tprint(add(v))\n\t}\n}\n"
	checkSrc(t, src)
}

// --- break/continue inside a generator-consuming range-for ---

func TestBreakContinueInsideGeneratorRangeForIsFine(t *testing.T) {
	checkSrc(t, "func Range(a int, b int) yield int {\n\tyield a\n}\n\nfunc f() {\n\tfor v := range Range(1, 10) {\n\t\tif v == 5 {\n\t\t\tbreak\n\t\t}\n\t\tcontinue\n\t}\n}\n")
}

// --- capture analysis reuse ---

// TestGeneratorRangeForCapturesOuterLocal proves an outer local referenced
// inside a generator-consuming range-for's own body is marked Captured -
// the same closure-capture machinery a FuncLit already uses (see
// capture.go's analyzeGeneratorRangeCaptures).
func TestGeneratorRangeForCapturesOuterLocal(t *testing.T) {
	src := "func Range(a int, b int) yield int {\n\tyield a\n}\n\nfunc f() {\n\tsum := 0\n\tfor v := range Range(1, 10) {\n\t\tsum = sum + v\n\t}\n\tprint(sum)\n}\n"
	tree, info := checkSrc(t, src)

	rangeForNode := findSoleRangeForStmt(t, tree)
	captures := info.Captures[rangeForNode]
	if len(captures) != 1 || captures[0].Name != "sum" {
		t.Fatalf("Captures[rangeForNode] = %v, want exactly [sum]", captures)
	}
	if !captures[0].Captured {
		t.Errorf("sum.Captured = false, want true")
	}
}
