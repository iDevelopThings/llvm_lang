package sema

import "testing"

// --- single-indexing a string (`s[i]` - see LANGUAGE.md's "Slicing"
// section): a single byte, typed u8, read-only - not an lvalue, not
// addressable. ---

// TestStringIndexType covers `s[i]` typing to u8.
func TestStringIndexType(t *testing.T) {
	tree, info := checkSrc(t, "func f() u8 {\n\ts := \"hello\"\n\treturn s[0]\n}\n")
	fn := tree.Children(tree.Root)[0]
	body := tree.Child(fn, 5)
	ret := tree.Child(body, 1)
	idxExpr := tree.Child(ret, 0)
	if got := info.Types[idxExpr]; got.Kind != TypeU8 {
		t.Fatalf("s[0] type = %v, want u8", got)
	}
}

// TestStringIndexUntypedIntAdapts covers an untyped int literal index
// retyping to i32, the same rule the existing array-index case already
// follows.
func TestStringIndexUntypedIntAdapts(t *testing.T) {
	tree, info := checkSrc(t, "func f() u8 {\n\ts := \"hello\"\n\treturn s[0]\n}\n")
	fn := tree.Children(tree.Root)[0]
	body := tree.Child(fn, 5)
	ret := tree.Child(body, 1)
	idxExpr := tree.Child(ret, 0)
	idxNode := tree.Child(idxExpr, 1)
	if got := info.Types[idxNode]; got.Kind != TypeI32 {
		t.Fatalf("index literal type = %v, want i32", got)
	}
}

// TestStringIndexNonIntIsError covers rejecting a non-int index, same as the
// existing array-index case.
func TestStringIndexNonIntIsError(t *testing.T) {
	expectCheckErrors(t, "func f() u8 {\n\ts := \"hello\"\n\treturn s[\"a\"]\n}\n", 1)
}

// TestStringIndexAssignIsError covers `s[i] = x` rejected with the specific
// "strings are immutable" diagnostic, not a generic assignment error.
func TestStringIndexAssignIsError(t *testing.T) {
	diags := expectCheckErrors(t, "func f() {\n\ts := \"hello\"\n\ts[0] = 65\n}\n", 1)
	wantDiag(t, diags.All()[0].Msg, "cannot assign to a string byte - strings are immutable")
}

// TestStringIndexAddressIsError covers `&s[i]` rejected with the specific
// "strings are immutable" diagnostic, not the generic "cannot take the
// address of this expression" fallback.
func TestStringIndexAddressIsError(t *testing.T) {
	diags := expectCheckErrors(t, "func f() *u8 {\n\ts := \"hello\"\n\treturn &s[0]\n}\n", 1)
	wantDiag(t, diags.All()[0].Msg, "cannot take the address of a string byte - strings are immutable")
}
