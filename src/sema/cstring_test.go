package sema

import "testing"

// --- `cstring` (see LANGUAGE.md's "External functions (FFI)" section): a
// predeclared builtin type, like string/bool, lowering to a raw pointer -
// only reachable via the two explicit cstring<->string conversions
// (checkConversionCall). ---

func TestCStringIsAPredeclaredType(t *testing.T) {
	src := "func f() {\n\tvar c cstring = cstring(\"hi\")\n}\n"
	checkSrc(t, src)
}

func TestCStringFromStringConversionTypeChecks(t *testing.T) {
	tree, info := checkSrc(t, "var c cstring = cstring(\"hi\")\n")
	decl := tree.Children(tree.Root)[0]
	call := tree.Child(decl, 2)
	if got := info.Types[call]; got.Kind != TypeCString {
		t.Errorf("Types[call] = %v, want cstring", got)
	}
}

func TestStringFromCStringConversionTypeChecks(t *testing.T) {
	src := "func f() {\n\tc := cstring(\"hi\")\n\ts := string(c)\n}\n"
	tree, info := checkSrc(t, src)
	fn := tree.Children(tree.Root)[0]
	body := tree.FuncBody(fn)
	sDecl := tree.Children(body)[1] // s := string(c)
	init := tree.Child(sDecl, 1)
	if got := info.Types[init]; got.Kind != TypeString {
		t.Errorf("Types[init] = %v, want string", got)
	}
}

// TestCStringExternParamTypeChecks covers isValidExternType's own new
// allowance - a cstring parameter/return may cross an extern func signature,
// unlike string.
func TestCStringExternParamTypeChecks(t *testing.T) {
	src := "extern func strlen(s cstring) i64\n" +
		"func f() {\n" +
		"\tstrlen(cstring(\"hi\"))\n" +
		"}\n"
	checkSrc(t, src)
}

func TestCStringExternReturnTypeChecks(t *testing.T) {
	checkSrc(t, "extern func getenv(name cstring) cstring\n")
}

// --- illegal usage ---

// TestCStringConversionOfNonStringIsError covers the conversion staying
// scoped to exactly string<->cstring - a numeric argument is neither a
// cstring<->string crossing nor numeric-to-numeric, so it falls to the
// existing "cannot convert" diagnostic.
func TestCStringConversionOfNonStringIsError(t *testing.T) {
	expectCheckErrors(t, "var c cstring = cstring(5)\n", 1)
}

func TestStringConversionOfNonCStringIsError(t *testing.T) {
	expectCheckErrors(t, "var s string = string(5)\n", 1)
}

// TestCStringIsNotComparable covers typeIsComparable's explicit rejection -
// `==`/`!=` has no defined lowering for a bare pointer with no length, and
// this must be caught in sema, not silently reach codegen.
func TestCStringIsNotComparable(t *testing.T) {
	src := "func f() {\n\ta := cstring(\"a\")\n\tb := cstring(\"b\")\n\ta == b\n}\n"
	expectCheckErrors(t, src, 1)
}

// TestCStringIsNotPrintable covers typeIsPrintable's explicit rejection -
// genPrintValueBare has no case for TypeCString and would otherwise panic.
func TestCStringIsNotPrintable(t *testing.T) {
	src := "func f() {\n\tc := cstring(\"hi\")\n\tprint(c)\n}\n"
	expectCheckErrors(t, src, 1)
}

// TestCStringPlusCStringIsError covers `+` staying string-only - cstring
// gets none of string's own operator support.
func TestCStringPlusCStringIsError(t *testing.T) {
	src := "func f() {\n\ta := cstring(\"a\")\n\tb := cstring(\"b\")\n\ta + b\n}\n"
	expectCheckErrors(t, src, 1)
}

// TestCStringLenIsError covers len() staying scoped to array/string/map -
// cstring has no length field to read.
func TestCStringLenIsError(t *testing.T) {
	src := "func f() {\n\tc := cstring(\"hi\")\n\tlen(c)\n}\n"
	expectCheckErrors(t, src, 1)
}

// TestCStringAssignableToItself covers cstring behaving like any other
// concrete scalar type for ordinary assignment/argument checking
// (checkAssignable's generic Type.Equal path) - no special-casing needed
// beyond the conversions themselves.
func TestCStringAssignableToItself(t *testing.T) {
	src := "func f() {\n\tvar a cstring = cstring(\"hi\")\n\tvar b cstring = a\n}\n"
	checkSrc(t, src)
}
