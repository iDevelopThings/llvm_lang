package codegen

import (
	"strings"
	"testing"
)

// --- `cstring` (see LANGUAGE.md's "External functions (FFI)" section):
// lowers to a raw `ptr`, only reachable via the two explicit conversions
// `cstring(s)`/`string(cs)` (genConversion, expr.go). Round-tripped here
// through real libc `strlen`/`strcmp` externs - already proven resolvable
// under JIT by this package's own extern_test.go. ---

// TestCStringFromLiteralSkipsArenaCopyJIT covers the literal fast path
// (genStringToCString): a string-literal argument reuses constStringValue's
// own already-NUL-terminated global directly, with no arena allocation.
// strlen on the result must still report the correct length.
func TestCStringFromLiteralSkipsArenaCopyJIT(t *testing.T) {
	jm := compileAndJIT(t, `
extern func strlen(s cstring) i64

func main() int {
	c := cstring("hello")
	return i32(strlen(c))
}
`)
	if got := jm.runInt32(t, "main"); got != 5 {
		t.Errorf("main() = %d, want 5", got)
	}
}

// TestCStringFromNonLiteralArenaCopiesAndNULTerminatesJIT covers the
// general path (a concatenation result, not a literal): genStringToCString
// must arena-copy the real bytes and append a NUL of its own, since a
// language string's {ptr, i32} carries no such guarantee.
func TestCStringFromNonLiteralArenaCopiesAndNULTerminatesJIT(t *testing.T) {
	jm := compileAndJIT(t, `
extern func strlen(s cstring) i64

func main() int {
	s := "hel" + "lo"
	c := cstring(s)
	return i32(strlen(c))
}
`)
	if got := jm.runInt32(t, "main"); got != 5 {
		t.Errorf("main() = %d, want 5", got)
	}
}

// TestCStringToStringRoundTripLengthJIT covers genCStringToString: strlen
// finds the real length, the bytes are arena-copied into a real language
// string, and len() on the result reads that copied length back correctly.
func TestCStringToStringRoundTripLengthJIT(t *testing.T) {
	jm := compileAndJIT(t, `
func main() int {
	c := cstring("hello")
	s := string(c)
	return len(s)
}
`)
	if got := jm.runInt32(t, "main"); got != 5 {
		t.Errorf("main() = %d, want 5", got)
	}
}

// TestCStringToStringRoundTripContentJIT covers the round-tripped string's
// actual bytes, not just its length, matching the original via `==` -
// exercising genStringEqual against a language string genuinely built by
// genCStringToString's own memcpy, not just an aliased pointer.
func TestCStringToStringRoundTripContentJIT(t *testing.T) {
	jm := compileAndJIT(t, `
func main() int {
	c := cstring("hello")
	s := string(c)
	if s == "hello" {
		return 1
	}
	return 0
}
`)
	if got := jm.runInt32(t, "main"); got != 1 {
		t.Errorf("main() = %d, want 1", got)
	}
}

// TestCStringExternRoundTripStrcmpJIT is the fuller end-to-end shape: one
// cstring built via the literal fast path, the other via the general
// arena-copy path (so they're genuinely two distinct buffers, not the same
// deduped literal global - see constStringValue's own dedup), compared for
// real byte-for-byte equality through a real libc strcmp call.
func TestCStringExternRoundTripStrcmpJIT(t *testing.T) {
	jm := compileAndJIT(t, `
extern func strcmp(a cstring, b cstring) i32

func main() int {
	s := "ab" + "c"
	a := cstring(s)
	b := cstring("abc")
	if strcmp(a, b) == 0 {
		return 1
	}
	return 0
}
`)
	if got := jm.runInt32(t, "main"); got != 1 {
		t.Errorf("main() = %d, want 1", got)
	}
}

// TestCStringExternRoundTripStrcmpMismatchJIT is
// TestCStringExternRoundTripStrcmpJIT's negative counterpart - genuinely
// different content must compare unequal, not just "not crash".
func TestCStringExternRoundTripStrcmpMismatchJIT(t *testing.T) {
	jm := compileAndJIT(t, `
extern func strcmp(a cstring, b cstring) i32

func main() int {
	a := cstring("abc")
	b := cstring("abd")
	if strcmp(a, b) == 0 {
		return 1
	}
	return 0
}
`)
	if got := jm.runInt32(t, "main"); got != 0 {
		t.Errorf("main() = %d, want 0", got)
	}
}

// TestCStringLLVMTypeIsPtr covers llvmType(TypeCString) lowering to a plain
// `ptr`, not string's own {ptr, i32} - asserted directly against the
// generated extern declaration's IR text.
func TestCStringLLVMTypeIsPtr(t *testing.T) {
	jm := compileAndJIT(t, `
extern func strlen(s cstring) i64

func main() int {
	return i32(strlen(cstring("hi")))
}
`)
	if !strings.Contains(jm.ir, "declare i64 @strlen(ptr)") {
		t.Errorf("expected `declare i64 @strlen(ptr)`, got:\n%s", jm.ir)
	}
}

// TestCStringNullCheckAndPointerConversionRoundTripJIT covers the two new
// cstring/*u8 interop gaps together (see LANGUAGE.md's "The cstring type"):
// an extern returning *u8 that may be null, a null check on the result
// (checkNilEquality's new TypeCString gate applies once retyped, but the
// interesting new path is the pointer itself - *u8 == nil already worked),
// and on the non-null path cstring(p) (checkConversionCall's new *u8 ->
// cstring reinterpret) followed by string(...) - the actual `getenv` idiom
// this feature exists for, both the found and not-found cases.
func TestCStringNullCheckAndPointerConversionRoundTripJIT(t *testing.T) {
	jm := compileAndJIT(t, `
extern func getenv(name cstring) *u8

func lookupPath() int {
	p := getenv(cstring("PATH"))
	if p == nil {
		return -1
	}
	s := string(cstring(p))
	if len(s) > 0 {
		return 1
	}
	return 0
}

func lookupMissing() int {
	p := getenv(cstring("LLVM_LANG_DEFINITELY_UNSET_XYZ_VAR"))
	if p == nil {
		return 1
	}
	return 0
}
`)
	if got := jm.runInt32(t, "lookupPath"); got != 1 {
		t.Errorf("lookupPath() = %d, want 1 (PATH should be set and non-empty)", got)
	}
	if got := jm.runInt32(t, "lookupMissing"); got != 1 {
		t.Errorf("lookupMissing() = %d, want 1 (getenv should return null)", got)
	}
}

// TestCStringEqualsNilJIT covers checkNilEquality's new TypeCString gate at
// runtime, not just type-checking: a real cstring value compares != nil, and
// one built from a genuinely null pointer (getenv on a missing var, via the
// *u8 -> cstring reinterpret) compares == nil - proving genBinaryExpr's
// existing default ICmp path needs no changes for this, as analyzed.
func TestCStringEqualsNilJIT(t *testing.T) {
	jm := compileAndJIT(t, `
extern func getenv(name cstring) *u8

func nonNullIsNotNil() int {
	c := cstring("hi")
	if c == nil {
		return 0
	}
	return 1
}

func nullFromMissingEnvIsNil() int {
	p := getenv(cstring("LLVM_LANG_DEFINITELY_UNSET_XYZ_VAR"))
	c := cstring(p)
	if c == nil {
		return 1
	}
	return 0
}
`)
	if got := jm.runInt32(t, "nonNullIsNotNil"); got != 1 {
		t.Errorf("nonNullIsNotNil() = %d, want 1", got)
	}
	if got := jm.runInt32(t, "nullFromMissingEnvIsNil"); got != 1 {
		t.Errorf("nullFromMissingEnvIsNil() = %d, want 1", got)
	}
}

// TestCStringConversionSharesUserDeclaredStrlenJIT covers strlenExtern's
// (runtime.go) whole reason for existing: a program that both declares its
// own `extern func strlen` AND uses `string(cs)` (genCStringToString, which
// also needs a "strlen") must resolve both to the exact same declaration,
// not two colliding ones (LLVM would silently rename the second to
// "strlen.1", which the JIT can never find - see this test failing that way
// before strlenExtern existed).
func TestCStringConversionSharesUserDeclaredStrlenJIT(t *testing.T) {
	jm := compileAndJIT(t, `
extern func strlen(s cstring) i64

func main() int {
	c := cstring("hello")
	n := strlen(c)
	s := string(c)
	return i32(n) + len(s)
}
`)
	if got := jm.runInt32(t, "main"); got != 10 {
		t.Errorf("main() = %d, want 10 (5 + 5)", got)
	}
	if got := strings.Count(jm.ir, "declare i64 @strlen(ptr)"); got != 1 {
		t.Errorf("expected exactly 1 @strlen declaration, got %d:\n%s", got, jm.ir)
	}
}
