package sema

import (
	"testing"

	"llvm_lang/src/lexer"
	"llvm_lang/src/parser"
)

// --- struct constructors (see LANGUAGE.md's "Constructors" section) ---

const pointWithCtorsSrc = "struct Point {\n" +
	"\tx int\n\n" +
	"\tconstructor() {\n\t\tthis.x = 99\n\t}\n" +
	"\tconstructor(v int) {\n\t\tthis.x = v\n\t}\n" +
	"}\n"

// TestConstructorOverloadResolutionByArgCount covers the core positive case:
// a zero-arg and a one-arg constructor on the same struct, each selected by
// the call's own argument count.
func TestConstructorOverloadResolutionByArgCount(t *testing.T) {
	checkSrc(t, pointWithCtorsSrc+"func f() int {\n\ta := Point(5)\n\tb := Point()\n\treturn a.x + b.x\n}\n")
}

// TestConstructorCallRecordsSelectedConstructorSymbol asserts the specific
// constructor a call resolved to is recorded directly onto the callee's own
// Info.Refs entry (not just "this names a struct with some constructor") -
// codegen needs to know exactly which one was selected (see
// checkConstructorCall, typecheck.go).
func TestConstructorCallRecordsSelectedConstructorSymbol(t *testing.T) {
	tree, info := checkSrc(t, pointWithCtorsSrc+"func f() int {\n\ta := Point(5)\n\treturn a.x\n}\n")
	fn := tree.Children(tree.Root)[1]
	body := tree.Child(fn, 5)
	short := tree.Child(body, 0)
	call := tree.Child(short, 1)
	callee := tree.Child(call, 0)

	sym, ok := info.Refs[callee]
	if !ok {
		t.Fatal("expected the constructor call's callee to resolve")
	}
	if sym.Kind != SymConstructor {
		t.Fatalf("callee resolved to kind %s, want constructor", sym.Kind)
	}
	structInfo := info.Structs["Point"]
	if want := structInfo.Constructors[1]; sym != want {
		t.Errorf("callee resolved to %+v, want Point's 1-arg constructor %+v", sym, want)
	}
}

// TestConstructorDuplicateArityIsError covers rejecting two constructors
// sharing the same parameter count on one struct - a structural error
// raised right at struct-declaration time (declareConstructor, resolve.go),
// not a call-time one (see StructInfo.Constructors' own doc comment) - so,
// unlike this file's other error cases, it's a Resolve-phase diagnostic, not
// a Check-phase one, and needs its own direct Resolve call rather than
// expectCheckErrors (which requires zero *resolve* errors before it will
// even run Check).
func TestConstructorDuplicateArityIsError(t *testing.T) {
	src := "struct Point {\n" +
		"\tx int\n\n" +
		"\tconstructor(a int) {\n\t\tthis.x = a\n\t}\n" +
		"\tconstructor(b int) {\n\t\tthis.x = b\n\t}\n" +
		"}\n"
	tree, pdiags := parser.ParseFile(lexer.NewFile("t.ll", src))
	if pdiags.HasErrors() {
		t.Fatalf("unexpected parse errors: %v", pdiags.All())
	}
	_, rdiags := Resolve(tree)
	if rdiags.ErrorCount() != 1 {
		t.Fatalf("ErrorCount = %d, want 1: %v", rdiags.ErrorCount(), rdiags.All())
	}
}

// TestConstructorCallWrongArityIsError covers calling a struct's
// constructor(s) with an argument count that matches none of them.
func TestConstructorCallWrongArityIsError(t *testing.T) {
	expectCheckErrors(t, pointWithCtorsSrc+"func f() {\n\tPoint(1, 2)\n}\n", 1)
}

// TestConstructorCallArgumentTypeMismatchIsError covers ordinary argument
// type-checking against the selected constructor's declared parameter type,
// exactly like an ordinary function call.
func TestConstructorCallArgumentTypeMismatchIsError(t *testing.T) {
	expectCheckErrors(t, pointWithCtorsSrc+"func f() {\n\tPoint(\"x\")\n}\n", 1)
}

// TestConstructorCallOnZeroConstructorStructIsError is the regression case:
// a struct with no constructors at all called like a function must remain
// exactly as illegal as before this feature (falling through unclaimed to
// checkConversionCall's own struct-target rejection - see its doc comment).
// This mirrors the pre-existing TestConversionToNonNumericTypeIsError
// (numeric_test.go), asserted again here under this feature's own test file
// for visibility.
func TestConstructorCallOnZeroConstructorStructIsError(t *testing.T) {
	expectCheckErrors(t, pointSrc+"func f() {\n\tPoint(1)\n}\n", 1)
}

// TestConstructorCallZeroArgsOnZeroConstructorStructNamesTheRealProblem
// covers a struct with no constructors called with the WRONG argument count
// (zero, not the one TestConstructorCallOnZeroConstructorStructIsError
// covers) - checkConversionCall must reject this before ever reaching its
// own argument-count check, since a struct is never a valid conversion
// target at any arity: "supply exactly one argument" would be misleading,
// since one argument wouldn't make Point(1) legal either.
func TestConstructorCallZeroArgsOnZeroConstructorStructNamesTheRealProblem(t *testing.T) {
	diags := expectCheckErrors(t, pointSrc+"func f() {\n\tPoint()\n}\n", 1)
	wantDiag(t, diags.All()[0].Msg, "Point has no constructor")
}

// TestNewCallOnZeroConstructorStructReportsBothLayers covers `new Point()`
// wrapping the same zero-constructor struct call: the inner conversion
// rejection (checkConversionCall) and checkNewExpr's own "new requires a
// constructor call or composite literal" both fire - two diagnostics, not
// one - and neither is the misleading argument-count wording.
func TestNewCallOnZeroConstructorStructReportsBothLayers(t *testing.T) {
	diags := expectCheckErrors(t, pointSrc+"func f() {\n\tnew Point()\n}\n", 2)
	wantDiagAmong(t, diags.All(), "Point has no constructor")
	wantDiagAmong(t, diags.All(), "new requires a struct constructor call or composite literal")
}

// TestConstructorCompositeLitRegression asserts `Point{...}` composite
// literals - both positional and keyed - remain completely unaffected by a
// struct also declaring constructors: both construction paths coexist.
func TestConstructorCompositeLitRegression(t *testing.T) {
	checkSrc(t, pointWithCtorsSrc+"var a Point = Point{1}\n")
	checkSrc(t, pointWithCtorsSrc+"var b Point = Point{x: 1}\n")
}

// TestConstructorBodyAssignsThis covers `this` inside a constructor body
// resolving/typing exactly like inside an ordinary method.
func TestConstructorBodyAssignsThis(t *testing.T) {
	checkSrc(t, pointWithCtorsSrc)
}

// TestConstructorCannotReturnAValue covers a constructor's implicit "no
// declared return type" rule: a bare `return` is fine (early exit), but
// `return expr` is rejected exactly like inside any other void
// function/method.
func TestConstructorCannotReturnAValue(t *testing.T) {
	src := "struct Point {\n" +
		"\tx int\n\n" +
		"\tconstructor(v int) {\n" +
		"\t\tthis.x = v\n" +
		"\t\treturn 5\n" +
		"\t}\n" +
		"}\n"
	expectCheckErrors(t, src, 1)
}

// TestConstructorBareReturnIsFine covers the early-exit case: a bare
// `return` inside a constructor body is legal, same as inside any other
// void function/method.
func TestConstructorBareReturnIsFine(t *testing.T) {
	src := "struct Point {\n" +
		"\tx int\n\n" +
		"\tconstructor(v int) {\n" +
		"\t\tif v > 0 {\n" +
		"\t\t\tthis.x = v\n" +
		"\t\t\treturn\n" +
		"\t\t}\n" +
		"\t\tthis.x = 0\n" +
		"\t}\n" +
		"}\n"
	checkSrc(t, src)
}
