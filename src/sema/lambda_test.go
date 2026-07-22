package sema

import (
	"testing"

	"llvm_lang/src/ast"
	"llvm_lang/src/enums"
	"llvm_lang/src/lexer"
	"llvm_lang/src/parser"
)

// firstNodeOfKind scans tree's own flat node array for the first node of
// kind, in declaration order - a small test-only helper, since a FuncLit's
// exact NodeIndex isn't otherwise convenient to pin down from source text
// alone the way a top-level declaration's positional index is.
func firstNodeOfKind(t *testing.T, tree *ast.Tree, kind enums.NodeKind) ast.NodeIndex {
	t.Helper()
	for idx := 1; idx < len(tree.Nodes); idx++ {
		if tree.Nodes[idx].Kind == kind {
			return ast.NodeIndex(idx)
		}
	}
	t.Fatalf("no %s node found in tree", kind)
	return ast.InvalidNode
}

// --- basic type-checking ---

func TestFuncLitIsTypedAsTypeFunc(t *testing.T) {
	src := "func f() {\n\tvar fn func(int, int) int = func(x int, y int) int {\n\t\treturn x + y\n\t}\n}\n"
	tree, info := checkSrc(t, src)
	lit := firstNodeOfKind(t, tree, enums.NodeKinds.FuncLit)
	got := info.Types[lit]
	if got.Kind != TypeFunc {
		t.Fatalf("Types[lit] = %v, want TypeFunc", got)
	}
	if len(got.Params) != 2 || got.Params[0].Kind != TypeI32 || got.Params[1].Kind != TypeI32 {
		t.Errorf("Params = %v, want [int, int]", got.Params)
	}
	if got.Return == nil || got.Return.Kind != TypeI32 {
		t.Errorf("Return = %v, want int", got.Return)
	}
}

func TestFuncLitAssignableToMatchingFuncTypedVar(t *testing.T) {
	checkSrc(t, "func f() {\n\tvar fn func(int) int = func(x int) int {\n\t\treturn x\n\t}\n}\n")
}

func TestFuncLitWrongReturnTypeIsAssignError(t *testing.T) {
	src := "func f() {\n\tvar fn func(int) bool = func(x int) int {\n\t\treturn x\n\t}\n}\n"
	expectCheckErrors(t, src, 1)
}

func TestFuncLitShortVarDeclInfersFuncType(t *testing.T) {
	src := "func f() {\n\tfn := func(x int) int {\n\t\treturn x\n\t}\n\tvar r int = fn(1)\n}\n"
	checkSrc(t, src)
}

func TestImmediatelyInvokedFuncLitTypeChecks(t *testing.T) {
	src := "func f() {\n\tvar r int = (func() int {\n\t\treturn 42\n\t})()\n}\n"
	checkSrc(t, src)
}

func TestFuncLitPassedAsArgument(t *testing.T) {
	src := "func apply(fn func(int) int, x int) int {\n\treturn fn(x)\n}\n" +
		"func f() {\n\tvar r int = apply(func(x int) int {\n\t\treturn x * 2\n\t}, 5)\n}\n"
	checkSrc(t, src)
}

func TestFuncLitReturnedFromFunc(t *testing.T) {
	src := "func getInc() func(int) int {\n\treturn func(x int) int {\n\t\treturn x + 1\n\t}\n}\n"
	checkSrc(t, src)
}

func TestFuncLitMissingReturnIsError(t *testing.T) {
	src := "func f() {\n\tvar fn func() int = func() int {\n\t\tif true {\n\t\t\treturn 1\n\t\t}\n\t}\n}\n"
	expectCheckErrors(t, src, 1)
}

func TestFuncLitVoidNeedsNoReturn(t *testing.T) {
	src := "func f() {\n\tfn := func() {\n\t\tprint(1)\n\t}\n\tfn()\n}\n"
	checkSrc(t, src)
}

// --- capture analysis ---

func TestSimpleCaptureMarksSymbolCaptured(t *testing.T) {
	src := "func makeCounter() func() int {\n" +
		"\tcount := 0\n" +
		"\tincrement := func() int {\n" +
		"\t\tcount = count + 1\n" +
		"\t\treturn count\n" +
		"\t}\n" +
		"\treturn increment\n" +
		"}\n"
	tree, info := checkSrc(t, src)

	lit := firstNodeOfKind(t, tree, enums.NodeKinds.FuncLit)
	captures := info.Captures[lit]
	if len(captures) != 1 {
		t.Fatalf("Captures[lit] = %v, want exactly 1 captured symbol", captures)
	}
	if captures[0].Name != "count" {
		t.Errorf("captured symbol name = %q, want %q", captures[0].Name, "count")
	}
	if !captures[0].Captured {
		t.Errorf("count.Captured = false, want true")
	}
}

// TestUncapturedLocalIsNotMarked covers the negative case directly alongside
// the positive one above: a local never referenced by any FuncLit must stay
// completely unaffected (Captured stays false, no codegen heap-promotion).
func TestUncapturedLocalIsNotMarked(t *testing.T) {
	src := "func f() {\n" +
		"\tuntouched := 5\n" +
		"\tfn := func() int {\n" +
		"\t\treturn 1\n" +
		"\t}\n" +
		"\tvar r int = fn()\n" +
		"\tvar s int = untouched\n" +
		"}\n"
	tree, info := checkSrc(t, src)
	lit := firstNodeOfKind(t, tree, enums.NodeKinds.FuncLit)
	if captures := info.Captures[lit]; len(captures) != 0 {
		t.Errorf("Captures[lit] = %v, want none - the literal never references any enclosing local", captures)
	}

	fDecl := tree.Children(tree.Root)[0]
	body := tree.Child(fDecl, 4)
	untouchedDecl := tree.Child(body, 0) // untouched := 5
	untouchedSym := info.Refs[tree.Child(untouchedDecl, 0)]
	if untouchedSym.Captured {
		t.Errorf("untouched.Captured = true, want false - it's never referenced by any FuncLit")
	}
}

// TestParamCaptureMarksSymbolCaptured covers capturing a *parameter*, not
// just a local var - the two symbol kinds capture.go's analyzeFuncLitCaptures
// treats identically (SymVar, SymParam).
func TestParamCaptureMarksSymbolCaptured(t *testing.T) {
	src := "func makeAdder(base int) func(int) int {\n" +
		"\treturn func(x int) int {\n" +
		"\t\treturn base + x\n" +
		"\t}\n" +
		"}\n"
	tree, info := checkSrc(t, src)
	lit := firstNodeOfKind(t, tree, enums.NodeKinds.FuncLit)
	captures := info.Captures[lit]
	if len(captures) != 1 || captures[0].Name != "base" {
		t.Fatalf("Captures[lit] = %v, want exactly [base]", captures)
	}
	if !captures[0].Captured {
		t.Errorf("base.Captured = false, want true")
	}
}

// TestTwoLevelNestedCaptureRelaysThroughBothLambdas covers a variable
// captured across two enclosing function levels: outerFunc declares x;
// lambda1 (declared directly inside outerFunc) itself declares lambda2,
// which is the one that actually references x. Both lambda1 and lambda2 must
// end up with x in their own Captures list (see capture.go's own doc comment
// for why lambda1 needs it too, even though its own statements never mention
// x directly - it has to relay x's address into lambda2's own capture
// context at codegen time).
func TestTwoLevelNestedCaptureRelaysThroughBothLambdas(t *testing.T) {
	src := "func outerFunc() func() func() int {\n" +
		"\tx := 10\n" +
		"\tlambda1 := func() func() int {\n" +
		"\t\tlambda2 := func() int {\n" +
		"\t\t\treturn x\n" +
		"\t\t}\n" +
		"\t\treturn lambda2\n" +
		"\t}\n" +
		"\treturn lambda1\n" +
		"}\n"
	tree, info := checkSrc(t, src)

	var lits []ast.NodeIndex
	for idx := 1; idx < len(tree.Nodes); idx++ {
		if tree.Nodes[idx].Kind == enums.NodeKinds.FuncLit {
			lits = append(lits, ast.NodeIndex(idx))
		}
	}
	if len(lits) != 2 {
		t.Fatalf("found %d FuncLit nodes, want 2", len(lits))
	}
	// lits[0] is lambda1 (appears first in source), lits[1] is lambda2.
	for i, lit := range lits {
		captures := info.Captures[lit]
		if len(captures) != 1 || captures[0].Name != "x" {
			t.Fatalf("lambda %d: Captures = %v, want exactly [x]", i+1, captures)
		}
	}
}

// TestCapturingThisInsideLambdaIsRejected covers the explicit, documented
// restriction that a lambda referencing an enclosing method's `this` is
// rejected outright (see LANGUAGE.md's "Lambdas" section and capture.go's
// analyzeFuncLitCaptures) - unlike an ordinary var/param, `this` is never
// captured/heap-promoted. This diagnostic is reported by Resolve's own tail
// pass (computeCaptures), not Check, so this test drives Resolve directly
// rather than going through checkSrc/expectCheckErrors (which both assert
// Resolve itself found nothing).
func TestCapturingThisInsideLambdaIsRejected(t *testing.T) {
	src := "struct Point {\n" +
		"\tx int\n" +
		"}\n" +
		"func (Point) getX() func() int {\n" +
		"\treturn func() int {\n" +
		"\t\treturn this.x\n" +
		"\t}\n" +
		"}\n"
	tree, pdiags := parser.ParseFile(lexer.NewFile("t.ll", src))
	if pdiags.HasErrors() {
		t.Fatalf("unexpected parse errors for %q: %v", src, pdiags.All())
	}
	_, rdiags := Resolve(tree)
	if rdiags.ErrorCount() != 1 {
		t.Fatalf("Resolve ErrorCount = %d, want 1: %v", rdiags.ErrorCount(), rdiags.All())
	}
}
