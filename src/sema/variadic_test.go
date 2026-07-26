package sema

import "testing"

// --- valid: the variadic parameter's own effective type ---

// TestVariadicParamIsDynamicArrayType covers the core representation
// decision (LANGUAGE.md's "Variadic parameters" section): `parts ...string`
// has the exact same effective Type as an ordinary `parts []string`
// parameter - an ordinary dynamic array, nothing special about it once past
// the declaration itself.
func TestVariadicParamIsDynamicArrayType(t *testing.T) {
	tree, info := checkSrc(t, "func Join(sep string, parts ...string) string {\n\treturn sep\n}\n")
	decl := tree.Children(tree.Root)[0]
	paramList := tree.FuncParamList(decl)
	partsParam := tree.Children(paramList)[1]

	got := info.Types[partsParam]
	if got.Kind != TypeArray || !got.Dynamic {
		t.Fatalf("Types[partsParam] = %v, want a dynamic []string array", got)
	}
	if got.Elem == nil || got.Elem.Kind != TypeString {
		t.Errorf("Types[partsParam].Elem = %v, want string", got.Elem)
	}
}

// --- valid: collect form ---

func TestVariadicCallCollectZeroArgs(t *testing.T) {
	src := "func Join(sep string, parts ...string) string {\n\treturn sep\n}\n" +
		"func f() {\n\tJoin(\",\")\n}\n"
	checkSrc(t, src)
}

func TestVariadicCallCollectOneArg(t *testing.T) {
	src := "func Join(sep string, parts ...string) string {\n\treturn sep\n}\n" +
		"func f() {\n\tJoin(\",\", \"a\")\n}\n"
	checkSrc(t, src)
}

func TestVariadicCallCollectSeveralArgs(t *testing.T) {
	src := "func Join(sep string, parts ...string) string {\n\treturn sep\n}\n" +
		"func f() {\n\tJoin(\",\", \"a\", \"b\", \"c\")\n}\n"
	checkSrc(t, src)
}

// TestVariadicNonStringElemType proves collect isn't string-special-cased -
// an int-elemented variadic parameter collects individually-checked int
// arguments exactly the same way.
func TestVariadicNonStringElemType(t *testing.T) {
	src := "func Sum(nums ...int) int {\n\treturn 0\n}\n" +
		"func f() {\n\tSum(1, 2, 3)\n}\n"
	checkSrc(t, src)
}

func TestVariadicSoleParameterNoFixedLeading(t *testing.T) {
	src := "func Sum(nums ...int) int {\n\treturn 0\n}\n" +
		"func f() {\n\tSum()\n\tSum(1)\n\tSum(1, 2)\n}\n"
	checkSrc(t, src)
}

// --- valid: spread form ---

func TestVariadicCallSpreadMatchingSliceType(t *testing.T) {
	src := "func Join(sep string, parts ...string) string {\n\treturn sep\n}\n" +
		"func f() {\n\tall := []string{\"a\", \"b\"}\n\tJoin(\",\", all...)\n}\n"
	checkSrc(t, src)
}

func TestVariadicCallSpreadSoleArgument(t *testing.T) {
	src := "func Sum(nums ...int) int {\n\treturn 0\n}\n" +
		"func f() {\n\tall := []int{1, 2, 3}\n\tSum(all...)\n}\n"
	checkSrc(t, src)
}

// --- valid: combinations the task explicitly calls out ---

// TestVariadicMethod covers a method whose last parameter is variadic -
// nothing about call-site collection/spread should differ from a free
// function once the receiver is resolved.
func TestVariadicMethod(t *testing.T) {
	src := "struct Logger {\n\tprefix string\n}\n" +
		"func (Logger) Log(parts ...string) int {\n\treturn 0\n}\n" +
		"func f() {\n\tl := Logger{\"x\"}\n\tl.Log(\"a\", \"b\")\n}\n"
	checkSrc(t, src)
}

// TestVariadicGenericFunc covers a generic function whose last parameter is
// variadic (`func F[T](items ...T)`) - type inference must solve T from
// every collected argument (or a spread argument's own element type), the
// same way an ordinary generic parameter already infers.
func TestVariadicGenericFunc(t *testing.T) {
	src := "func First[T](items ...T) T {\n\treturn items[0]\n}\n" +
		"func f() {\n\tvar a int = First(1, 2, 3)\n\tvar b string = First(\"x\", \"y\")\n}\n"
	checkSrc(t, src)
}

func TestVariadicGenericFuncSpread(t *testing.T) {
	src := "func First[T](items ...T) T {\n\treturn items[0]\n}\n" +
		"func f() {\n\tnums := []int{1, 2, 3}\n\tvar a int = First(nums...)\n}\n"
	checkSrc(t, src)
}

func TestVariadicGenericFuncZeroArgsInferenceFails(t *testing.T) {
	// Zero collected arguments (and no spread) gives inference nothing to
	// solve T from - same "cannot infer" diagnostic an ordinary generic
	// parameter with no arguments at all already gets.
	src := "func First[T](items ...T) T {\n\treturn items[0]\n}\n" +
		"func f() {\n\tFirst()\n}\n"
	expectCheckErrors(t, src, 1)
}

// --- invalid: spread on a non-variadic function ---

func TestSpreadOnNonVariadicFunctionIsError(t *testing.T) {
	src := "func add(x int, y int) int {\n\treturn x + y\n}\n" +
		"func f() {\n\tnums := []int{1, 2}\n\tadd(nums...)\n}\n"
	expectCheckErrors(t, src, 1)
}

func TestSpreadOnBuiltinIsError(t *testing.T) {
	src := "func f() {\n\ts := []int{1, 2}\n\tprint(s...)\n}\n"
	expectCheckErrors(t, src, 1)
}

func TestSpreadOnConstructorIsError(t *testing.T) {
	src := "struct Point {\n\tx int\n\ty int\n\n\tconstructor(a int, b int) {\n\t\tthis.x = a\n\t\tthis.y = b\n\t}\n}\n" +
		"func f() {\n\targs := []int{1, 2}\n\tPoint(args...)\n}\n"
	expectCheckErrors(t, src, 1)
}

// --- invalid: spread with a mismatched type ---

func TestSpreadWrongElementSliceTypeIsError(t *testing.T) {
	// []int spread against a []string variadic parameter - no implicit
	// conversion, same "must be EXACTLY []T" rule as everywhere else.
	src := "func Join(sep string, parts ...string) string {\n\treturn sep\n}\n" +
		"func f() {\n\tnums := []int{1, 2}\n\tJoin(\",\", nums...)\n}\n"
	expectCheckErrors(t, src, 1)
}

func TestSpreadNonSliceValueIsError(t *testing.T) {
	// A bare string spread - not a []string at all - against a variadic
	// []string parameter.
	src := "func Join(sep string, parts ...string) string {\n\treturn sep\n}\n" +
		"func f() {\n\ts := \"x\"\n\tJoin(\",\", s...)\n}\n"
	expectCheckErrors(t, src, 1)
}

// --- invalid: collected element doesn't match T ---

func TestVariadicCollectedElementTypeMismatchIsError(t *testing.T) {
	src := "func Join(sep string, parts ...string) string {\n\treturn sep\n}\n" +
		"func f() {\n\tJoin(\",\", \"a\", 5)\n}\n"
	expectCheckErrors(t, src, 1)
}

// --- invalid: too few fixed arguments ---

func TestVariadicTooFewFixedArgumentsIsError(t *testing.T) {
	src := "func Join(sep string, parts ...string) string {\n\treturn sep\n}\n" +
		"func f() {\n\tJoin()\n}\n"
	expectCheckErrors(t, src, 1)
}

// --- invalid: a variadic function referenced as a bare value ---

func TestVariadicFuncAsBareValueIsError(t *testing.T) {
	src := "func Sum(nums ...int) int {\n\treturn 0\n}\n" +
		"func f() {\n\tvar fn func([]int) int = Sum\n}\n"
	expectCheckErrors(t, src, 1)
}

func TestVariadicFuncShortVarDeclAsBareValueIsError(t *testing.T) {
	src := "func Sum(nums ...int) int {\n\treturn 0\n}\n" +
		"func f() {\n\tfn := Sum\n}\n"
	expectCheckErrors(t, src, 1)
}

// --- an extern func's own FFI type restriction already rejects []T, so a
// variadic extern func parameter needs no dedicated diagnostic of its own ---

func TestExternFuncVariadicParamIsRejectedByFFIRestriction(t *testing.T) {
	src := "extern func sumAll(n int, args ...int) int\n"
	expectCheckErrors(t, src, 1)
}
