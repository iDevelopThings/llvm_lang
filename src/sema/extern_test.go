package sema

import (
	"testing"

	"llvm_lang/src/lexer"
	"llvm_lang/src/parser"
)

// --- `extern func` FFI declarations (see LANGUAGE.md's "External functions
// (FFI)" section) ---

// TestExternFuncDeclResolves covers the basic resolve-phase shape: an
// ExternFuncDecl declares a SymFunc symbol into package scope, exactly like
// an ordinary free FuncDecl (see resolve.go's declareExternFunc) - a call to
// it resolves and type-checks with no diagnostics at all.
func TestExternFuncDeclResolves(t *testing.T) {
	src := "extern func abs(x i32) i32\n" +
		"func main() int {\n" +
		"\treturn abs(-5)\n" +
		"}\n"
	tree, info := resolveSrc(t, src)
	externDecl := tree.Children(tree.Root)[0]
	nameNode := tree.ExternFuncName(externDecl)
	sym, ok := info.Refs[nameNode]
	if !ok {
		t.Fatal("expected a Ref for the extern func's name")
	}
	if sym.Kind != SymFunc {
		t.Errorf("sym.Kind = %s, want SymFunc", sym.Kind)
	}
	if sym.Decl != externDecl {
		t.Errorf("sym.Decl = %v, want the ExternFuncDecl node itself (%v)", sym.Decl, externDecl)
	}
}

// TestExternFuncCallTypeChecks covers funcSigForCall/funcSigForDecl's own
// dispatch (typecheck.go) correctly reading an ExternFuncDecl's 3-child
// [name, paramList, returnType] shape rather than assuming FuncDecl's -
// argument/return types must check out exactly like an ordinary call.
func TestExternFuncCallTypeChecks(t *testing.T) {
	src := "extern func abs(x i32) i32\n" +
		"func main() int {\n" +
		"\ty := abs(-5) + 1\n" +
		"\treturn y\n" +
		"}\n"
	tree, info := checkSrc(t, src)
	fn := tree.Children(tree.Root)[1]
	body := tree.FuncBody(fn)
	shortVarDecl := tree.Children(body)[0]
	init := tree.Child(shortVarDecl, 1) // abs(-5) + 1
	if got := info.Types[init]; got.Kind != TypeInt {
		t.Errorf("Types[init] = %v, want int", got)
	}
}

// TestExternFuncBareValueReferenceTypeChecks covers typeOfSymbolValue's
// SymFunc case (a bare, uncalled reference to a free function is a
// first-class TypeFunc value - see LANGUAGE.md's "First-class functions"
// section) reading an ExternFuncDecl's signature through the same
// funcSigForDecl dispatch a direct call already goes through - this needed no
// code changes of its own (see funcSigForDecl's own doc comment), but is
// worth its own regression test since it's a second, independent call site
// that could have assumed FuncDecl's shape just as easily.
func TestExternFuncBareValueReferenceTypeChecks(t *testing.T) {
	src := "extern func abs(x i32) i32\n" +
		"func main() int {\n" +
		"\tf := abs\n" +
		"\treturn f(-5)\n" +
		"}\n"
	tree, info := checkSrc(t, src)
	fn := tree.Children(tree.Root)[1]
	body := tree.FuncBody(fn)
	shortVarDecl := tree.Children(body)[0]
	init := tree.Child(shortVarDecl, 1) // bare `abs`
	got := info.Types[init]
	if got.Kind != TypeFunc {
		t.Fatalf("Types[init] = %v, want TypeFunc", got)
	}
	if len(got.Params) != 1 || got.Params[0].Kind != TypeI32 {
		t.Errorf("Types[init].Params = %v, want [i32]", got.Params)
	}
	if got.Return == nil || got.Return.Kind != TypeI32 {
		t.Errorf("Types[init].Return = %v, want i32", got.Return)
	}
}

// TestExternFuncNoReturnType covers an extern func declaring no return type
// at all - a void C function, e.g. `extern func Foo(x i32)` - typing as
// TypeVoid exactly like an ordinary FuncDecl with no declared return type.
func TestExternFuncNoReturnType(t *testing.T) {
	src := "extern func Foo(x i32)\n" +
		"func main() int {\n" +
		"\tFoo(5)\n" +
		"\treturn 0\n" +
		"}\n"
	checkSrc(t, src)
}

// TestExternFuncPointerParamTypeChecks covers a pointer-typed parameter (the
// motivating QueryPerformanceCounter-style shape - see LANGUAGE.md's
// "External functions (FFI)" section) type-checking cleanly, argument
// included (`&x`, address-of a local).
func TestExternFuncPointerParamTypeChecks(t *testing.T) {
	src := "extern func QueryPerformanceCounter(counter *i64) bool\n" +
		"func main() int {\n" +
		"\tx := i64(0)\n" +
		"\tQueryPerformanceCounter(&x)\n" +
		"\treturn 0\n" +
		"}\n"
	checkSrc(t, src)
}

// --- FFI type-restriction diagnostics (a genuinely new rule this feature
// introduces - see LANGUAGE.md's "External functions (FFI)" section): every
// parameter type and the return type of an extern func must be a numeric
// type, bool, or a pointer type - string, a struct by value, a dynamic
// array, and a function type are all explicitly rejected. ---

func TestExternFuncStringParamIsError(t *testing.T) {
	src := "extern func Foo(s string) int\n"
	expectCheckErrors(t, src, 1)
}

func TestExternFuncStringReturnIsError(t *testing.T) {
	src := "extern func Foo(x i32) string\n"
	expectCheckErrors(t, src, 1)
}

func TestExternFuncDynamicArrayReturnIsError(t *testing.T) {
	src := "extern func Foo() []int\n"
	expectCheckErrors(t, src, 1)
}

func TestExternFuncFuncTypeParamIsError(t *testing.T) {
	src := "extern func Foo(cb func(int) int) int\n"
	expectCheckErrors(t, src, 1)
}

// TestExternFuncPointerToDisallowedElemIsLegal covers the recursive-but-
// unconditional pointer rule: a pointer type is always valid regardless of
// its own pointee type - even one otherwise disallowed here (string, in this
// case) - since a pointer is always just a raw address at the ABI level (see
// isValidExternType's own doc comment).
func TestExternFuncPointerToDisallowedElemIsLegal(t *testing.T) {
	src := "extern func Foo(p *string) int\n"
	checkSrc(t, src)
}

// --- Struct-by-value FFI safety (see LANGUAGE.md's "External functions
// (FFI)" section): a named struct type may cross an extern signature iff
// every field, recursively, is itself FFI-safe (isFFISafeType/
// isFFISafeStructField, typecheck.go). ---

// TestExternFuncPODStructParamIsLegal covers the base allowlist case: a
// struct made entirely of numeric fields crosses an extern signature (both
// as a parameter and a return type) cleanly.
func TestExternFuncPODStructParamIsLegal(t *testing.T) {
	src := "struct Point {\n" +
		"\tx int\n" +
		"\ty int\n" +
		"}\n" +
		"extern func Foo(p Point) Point\n"
	checkSrc(t, src)
}

// TestExternFuncStructWithStringFieldIsError covers propagation: a struct
// containing even one non-FFI-safe field (string's own {ptr,i32} fat
// struct) is rejected, the same as string crossing directly.
func TestExternFuncStructWithStringFieldIsError(t *testing.T) {
	src := "struct Bad {\n" +
		"\ts string\n" +
		"}\n" +
		"extern func Foo(b Bad) int\n"
	expectCheckErrors(t, src, 1)
}

// TestExternFuncStructWithDynamicArrayFieldIsError mirrors the string case
// for a dynamic-array field.
func TestExternFuncStructWithDynamicArrayFieldIsError(t *testing.T) {
	src := "struct Bad {\n" +
		"\txs []int\n" +
		"}\n" +
		"extern func Foo(b Bad) int\n"
	expectCheckErrors(t, src, 1)
}

// TestExternFuncStructWithFuncFieldIsError mirrors the string case for a
// function-typed field (a fat closure pointer, not a scalar/pointer C ABI
// shape).
func TestExternFuncStructWithFuncFieldIsError(t *testing.T) {
	src := "struct Bad {\n" +
		"\tcb func(int) int\n" +
		"}\n" +
		"extern func Foo(b Bad) int\n"
	expectCheckErrors(t, src, 1)
}

// TestExternFuncBareFixedArrayParamIsError covers the standalone-position
// restriction: `[N]T` is fine as a struct field (see
// TestExternFuncFixedArrayFieldIsLegal below) but never legal as a bare
// parameter/return itself - a real C array parameter decays to a pointer,
// a conversion this compiler doesn't perform implicitly.
func TestExternFuncBareFixedArrayParamIsError(t *testing.T) {
	src := "extern func Foo(a [4]int) int\n"
	expectCheckErrors(t, src, 1)
}

// TestExternFuncNestedFFISafeStructIsLegal covers recursion: a struct
// nesting another struct, itself made entirely of FFI-safe fields, is
// FFI-safe.
func TestExternFuncNestedFFISafeStructIsLegal(t *testing.T) {
	src := "struct Inner {\n" +
		"\tx int\n" +
		"}\n" +
		"struct Outer {\n" +
		"\tinner Inner\n" +
		"\ty int\n" +
		"}\n" +
		"extern func Foo(o Outer) int\n"
	checkSrc(t, src)
}

// TestExternFuncNestedNonFFISafeStructIsError covers recursion propagating a
// rejection through a nested struct exactly like a direct field would.
func TestExternFuncNestedNonFFISafeStructIsError(t *testing.T) {
	src := "struct Inner {\n" +
		"\ts string\n" +
		"}\n" +
		"struct Outer {\n" +
		"\tinner Inner\n" +
		"}\n" +
		"extern func Foo(o Outer) int\n"
	expectCheckErrors(t, src, 1)
}

// TestExternFuncFixedArrayFieldIsLegal covers the one case a bare `[N]T`
// param/return doesn't cover: `[N]T` as a struct *field* is FFI-safe when T
// is, since a real C struct may legally embed an array member.
func TestExternFuncFixedArrayFieldIsLegal(t *testing.T) {
	src := "struct Buf {\n" +
		"\tdata [4]i8\n" +
		"}\n" +
		"extern func Foo(b Buf) int\n"
	checkSrc(t, src)
}

// TestExternFuncFixedArrayOfDisallowedElemFieldIsError covers a fixed-array
// field itself propagating a non-FFI-safe element type's rejection.
func TestExternFuncFixedArrayOfDisallowedElemFieldIsError(t *testing.T) {
	src := "struct Bad {\n" +
		"\tdata [4]string\n" +
		"}\n" +
		"extern func Foo(b Bad) int\n"
	expectCheckErrors(t, src, 1)
}

// TestExternFuncNeverCalledStillChecked covers checkExternFuncDecl's own
// reason for existing (typecheck.go): a declared-but-never-called extern
// func - very plausibly one half of a matched pair, like this feature's own
// QueryPerformanceCounter/QueryPerformanceFrequency worked example - must
// still get its own type-restriction diagnostics from checkPackage's eager
// top-level pass, not only the first time some call site happens to
// reference it (or never, if none ever does).
func TestExternFuncNeverCalledStillChecked(t *testing.T) {
	src := "extern func Foo(s string) int\n" +
		"func main() int {\n" +
		"\treturn 0\n" +
		"}\n"
	expectCheckErrors(t, src, 1)
}

// TestExternFuncRedeclaredIsError covers declareLocal's already-generic
// redeclaration check (resolve.go) applying to an ExternFuncDecl with no
// extra logic of its own needed - two top-level declarations sharing the
// name "abs" (an ordinary func and an extern func) collide in package scope
// exactly like two ordinary funcs would.
func TestExternFuncRedeclaredIsError(t *testing.T) {
	src := "extern func abs(x i32) i32\n" +
		"func abs(x i32) i32 {\n" +
		"\treturn x\n" +
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
