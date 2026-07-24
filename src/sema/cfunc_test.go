package sema

import "testing"

// --- `cfunc(T1, T2) R` - bare C function pointer types (see LANGUAGE.md's
// "External functions (FFI)" section): a distinct TypeKind from TypeFunc,
// FFI-safe by construction, lowering to a bare function pointer with no
// closure context at all. ---

// TestCFuncTypeParamIsLegalOnExtern covers the type's whole point: unlike
// an ordinary func type (TestExternFuncFuncTypeParamIsError, extern_test.go),
// a cfunc type crosses an extern func signature cleanly.
func TestCFuncTypeParamIsLegalOnExtern(t *testing.T) {
	src := "extern func register(cb cfunc(int) int) int\n"
	checkSrc(t, src)
}

// TestCFuncTypeReturnIsLegalOnExtern mirrors the param case for the return
// position.
func TestCFuncTypeReturnIsLegalOnExtern(t *testing.T) {
	src := "extern func getHandler() cfunc(int) int\n"
	checkSrc(t, src)
}

// TestExternFuncFuncTypeParamStillRejected re-covers (see extern_test.go's
// own TestExternFuncFuncTypeParamIsError) that an ordinary language func
// type is still rejected on an extern signature after this feature - cfunc
// must never be folded into, or silently widen, that existing rule.
func TestExternFuncFuncTypeParamStillRejected(t *testing.T) {
	src := "extern func Foo(cb func(int) int) int\n"
	expectCheckErrors(t, src, 1)
}

// TestCFuncTypeNonFFISafeParamIsError covers checkCFuncElemType: a cfunc
// type's own parameter must itself be FFI-safe, exactly like an extern
// func's - `string`'s fat {ptr,i32} struct is rejected.
func TestCFuncTypeNonFFISafeParamIsError(t *testing.T) {
	src := "var f cfunc(string) int\n"
	expectCheckErrors(t, src, 1)
}

// TestCFuncTypeNonFFISafeReturnIsError mirrors the param case for the
// return position.
func TestCFuncTypeNonFFISafeReturnIsError(t *testing.T) {
	src := "var f cfunc(int) string\n"
	expectCheckErrors(t, src, 1)
}

// TestCFuncTypeNestedCFuncParamIsLegal covers cfuncIsFFISafe's own
// recursion: a cfunc type is itself FFI-safe (just a bare pointer at the
// ABI level), so a cfunc-typed parameter of a cfunc type is legal.
func TestCFuncTypeNestedCFuncParamIsLegal(t *testing.T) {
	src := "var f cfunc(cfunc(int) int) int\n"
	checkSrc(t, src)
}

// TestCFuncTypeStructParamIsLegal covers a struct made entirely of
// FFI-safe fields crossing a cfunc signature, mirroring
// TestExternFuncPODStructParamIsLegal (extern_test.go).
func TestCFuncTypeStructParamIsLegal(t *testing.T) {
	src := "struct Point {\n" +
		"\tx int\n" +
		"\ty int\n" +
		"}\n" +
		"var f cfunc(Point) Point\n"
	checkSrc(t, src)
}

// TestCFuncAsStructFieldIsLegal covers LANGUAGE.md's documented claim that
// a cfunc type is FFI-safe "recursively as a struct field" - isFFISafeStructField
// must accept TypeCFunc (via cfuncIsFFISafe), not fall through to
// isFFISafeScalar which deliberately omits it.
func TestCFuncAsStructFieldIsLegal(t *testing.T) {
	src := "struct Handlers {\n" +
		"\tonClick cfunc(int) int\n" +
		"}\n" +
		"extern func register(h Handlers) int\n"
	checkSrc(t, src)
}

// TestCFuncAsStructFieldWithNonFFISafeElemIsError covers the recursive
// walk: a cfunc field whose own signature isn't FFI-safe still rejects the
// enclosing struct on an extern signature.
func TestCFuncAsStructFieldWithNonFFISafeElemIsError(t *testing.T) {
	src := "struct Handlers {\n" +
		"\tonClick cfunc(string) int\n" +
		"}\n" +
		"extern func register(h Handlers) int\n"
	expectCheckErrors(t, src, 2) // cfunc param + enclosing extern param
}

// --- func -> cfunc conversion (checkAssignable's own special case,
// checkFuncToCFuncConversion) ---

// TestTopLevelFuncToCFuncConversionIsLegal covers the happy path: a direct
// reference to a top-level FuncDecl with a matching signature converts to a
// cfunc-typed var with no diagnostics.
func TestTopLevelFuncToCFuncConversionIsLegal(t *testing.T) {
	src := "func add(x int, y int) int {\n" +
		"\treturn x + y\n" +
		"}\n" +
		"func main() int {\n" +
		"\tvar cb cfunc(int, int) int = add\n" +
		"\treturn cb(1, 2)\n" +
		"}\n"
	tree, info := checkSrc(t, src)
	mainFn := tree.Children(tree.Root)[1]
	body := tree.FuncBody(mainFn)
	varDecl := tree.Children(body)[0]
	init := tree.Child(varDecl, 2) // bare `add`
	if got := info.Types[init]; got.Kind != TypeCFunc {
		t.Fatalf("Types[init] = %v, want TypeCFunc", got)
	}
}

// TestExternFuncToCFuncConversionIsLegal mirrors the top-level case for an
// ExternFuncDecl source - the other legal shape checkFuncToCFuncConversion
// accepts.
func TestExternFuncToCFuncConversionIsLegal(t *testing.T) {
	src := "extern func abs(x i32) i32\n" +
		"func main() int {\n" +
		"\tvar cb cfunc(i32) i32 = abs\n" +
		"\treturn cb(-5)\n" +
		"}\n"
	checkSrc(t, src)
}

// TestFuncLitToCFuncConversionIsError covers the round's explicit "no
// trampoline" rule: a function literal has no fixed address a bare cfunc
// pointer could ever hold, so it's a compile error rather than silently
// falling back to some other representation.
func TestFuncLitToCFuncConversionIsError(t *testing.T) {
	src := "func main() int {\n" +
		"\tvar cb cfunc(int) int = func(x int) int { return x }\n" +
		"\treturn cb(1)\n" +
		"}\n"
	expectCheckErrors(t, src, 1)
}

// TestStoredFuncValueToCFuncConversionIsError covers the other captures
// case: a func-typed variable/parameter (not a direct top-level reference)
// is rejected too, even though its own Type is TypeFunc and its signature
// matches - only a fixed, direct declaration reference is ever eligible
// (cfuncSourceSymbol).
func TestStoredFuncValueToCFuncConversionIsError(t *testing.T) {
	src := "func add(x int, y int) int {\n" +
		"\treturn x + y\n" +
		"}\n" +
		"func main() int {\n" +
		"\tf := add\n" +
		"\tvar cb cfunc(int, int) int = f\n" +
		"\treturn cb(1, 2)\n" +
		"}\n"
	expectCheckErrors(t, src, 1)
}

// TestMismatchedSignatureFuncToCFuncConversionIsError covers checkAssignable
// never even attempting the cfunc special case when the signatures don't
// structurally match (sameFuncShape) - it falls through to the ordinary
// Type.Equal mismatch diagnostic instead.
func TestMismatchedSignatureFuncToCFuncConversionIsError(t *testing.T) {
	src := "func add(x int, y int) int {\n" +
		"\treturn x + y\n" +
		"}\n" +
		"func main() int {\n" +
		"\tvar cb cfunc(int) int = add\n" +
		"\treturn cb(1)\n" +
		"}\n"
	expectCheckErrors(t, src, 1)
}

// TestOrdinaryFuncStructByValueToCFuncConversionIsError covers
// checkFuncToCFuncConversion's own ABI guard: an ordinary (non-extern)
// FuncDecl's real signature uses this compiler's internal, uncoerced
// struct-passing convention, which would silently disagree with a cfunc
// call site's C-ABI struct coercion - so this specific shape is rejected
// even though the source is otherwise a legal direct top-level reference.
func TestOrdinaryFuncStructByValueToCFuncConversionIsError(t *testing.T) {
	src := "struct Point {\n" +
		"\tx int\n" +
		"\ty int\n" +
		"}\n" +
		"func identity(p Point) Point {\n" +
		"\treturn p\n" +
		"}\n" +
		"func main() int {\n" +
		"\tvar cb cfunc(Point) Point = identity\n" +
		"\treturn 0\n" +
		"}\n"
	expectCheckErrors(t, src, 1)
}

// TestExternFuncStructByValueToCFuncConversionIsLegal is the positive
// counterpart: an ExternFuncDecl source is exempt from the struct-shape
// guard above, since its own real LLVM signature is already built with the
// identical C-ABI coercion a cfunc call site applies.
func TestExternFuncStructByValueToCFuncConversionIsLegal(t *testing.T) {
	src := "struct Point {\n" +
		"\tx int\n" +
		"\ty int\n" +
		"}\n" +
		"extern func identity(p Point) Point\n" +
		"func main() int {\n" +
		"\tvar cb cfunc(Point) Point = identity\n" +
		"\treturn 0\n" +
		"}\n"
	checkSrc(t, src)
}

// TestFuncToCFuncConversionAsArgument covers the identical conversion
// happening through an ordinary call argument, not just a var decl
// initializer - checkAssignable is shared by both positions, so this is a
// regression test against that sharing ever being lost.
func TestFuncToCFuncConversionAsArgument(t *testing.T) {
	src := "func add(x int, y int) int {\n" +
		"\treturn x + y\n" +
		"}\n" +
		"func callThrough(cb cfunc(int, int) int, a int, b int) int {\n" +
		"\treturn cb(a, b)\n" +
		"}\n" +
		"func main() int {\n" +
		"\treturn callThrough(add, 1, 2)\n" +
		"}\n"
	checkSrc(t, src)
}

// TestCFuncTypeString covers Type.String's own cfunc rendering
// ("cfunc(...)", distinct from "func(...)") - checkAssignable's own
// diagnostics (e.g. TestMismatchedSignatureFuncToCFuncConversionIsError
// above) depend on this to name the type correctly.
func TestCFuncTypeString(t *testing.T) {
	got := Type{
		Kind:   TypeCFunc,
		Params: []Type{i32Type},
		Return: &i32Type,
	}.String()
	want := "cfunc(int) int"
	if got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

// TestCFuncTypeNotComparable covers checkEqualityOperands never defining
// `==` for two cfunc-typed operands - the same as two ordinary func-typed
// operands (neither's Kind appears in that switch's own case list at all,
// so both fall through to its generic "operator not defined" diagnostic).
func TestCFuncTypeNotComparable(t *testing.T) {
	src := "func identity(x int) int {\n" +
		"\treturn x\n" +
		"}\n" +
		"func main() int {\n" +
		"\tvar a cfunc(int) int = identity\n" +
		"\tvar b cfunc(int) int = identity\n" +
		"\tif a == b {\n" +
		"\t\treturn 1\n" +
		"\t}\n" +
		"\treturn 0\n" +
		"}\n"
	expectCheckErrors(t, src, 1)
}

// TestCFuncTypeNotPrintable covers typeIsPrintable explicitly rejecting
// TypeCFunc (print's own call site, checkPrintCall) - both typeIsComparable
// and typeIsPrintable default to true for any unlisted Kind, so omitting
// TypeCFunc from either's own case list would silently accept it instead of
// rejecting it, the same as TypeFunc.
func TestCFuncTypeNotPrintable(t *testing.T) {
	src := "func identity(x int) int {\n" +
		"\treturn x\n" +
		"}\n" +
		"func main() int {\n" +
		"\tvar cb cfunc(int) int = identity\n" +
		"\tprint(cb)\n" +
		"\treturn 0\n" +
		"}\n"
	expectCheckErrors(t, src, 1)
}
