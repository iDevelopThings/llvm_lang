package sema

import (
	"strings"
	"testing"

	"llvm_lang/src/diag"
	"llvm_lang/src/lexer"
	"llvm_lang/src/parser"
)

// checkNamedSrc is checkSrcAllowErrors with an explicit file name - stub
// decls are only legal when the basename is stubs.llx.
func checkNamedSrc(t *testing.T, name, src string) *diag.Bag {
	t.Helper()
	tree, pdiags := parser.ParseFile(lexer.NewFile(name, src), false)
	if pdiags.HasErrors() {
		t.Fatalf("unexpected parse errors for %q: %v", src, pdiags.All())
	}
	info, rdiags := Resolve(tree)
	if rdiags.HasErrors() {
		t.Fatalf("unexpected resolve errors for %q: %v", src, rdiags.All())
	}
	return Check(tree, info)
}

func TestStubFuncAllowedInStubsFile(t *testing.T) {
	src := "stub func args() []string\n" +
		"stub func print(x Any)\n" +
		"stub func AnyLen(a Any) int\n"
	diags := checkNamedSrc(t, "std/stubs.llx", src)
	if diags.HasErrors() {
		t.Fatalf("unexpected check errors: %v", diags.All())
	}
}

func TestStubFuncRejectedOutsideStubsFile(t *testing.T) {
	src := "stub func args() []string\n"
	diags := checkNamedSrc(t, "main.llx", src)
	if !diags.HasErrors() {
		t.Fatal("expected stub func outside stubs.llx to error")
	}
	msg := diags.All()[0].Msg
	if !strings.Contains(msg, "stubs.llx") {
		t.Errorf("error = %q, want mention of stubs.llx", msg)
	}
}

func TestStubFuncAllowsNonFFITypes(t *testing.T) {
	// string / Any / []string would all fail as extern; stubs must accept them.
	src := "stub func print(x Any)\n" +
		"stub func args() []string\n" +
		"stub func nameOf(a Any) string\n"
	diags := checkNamedSrc(t, "stubs.llx", src)
	if diags.HasErrors() {
		t.Fatalf("unexpected check errors: %v", diags.All())
	}
}

func TestStubFuncCallIsRejected(t *testing.T) {
	src := "stub func args() []string\n" +
		"func main() int {\n" +
		"\ta := args()\n" +
		"\treturn len(a)\n" +
		"}\n"
	diags := checkNamedSrc(t, "stubs.llx", src)
	if !diags.HasErrors() {
		t.Fatal("expected calling a stub func to error")
	}
	found := false
	for _, d := range diags.All() {
		if strings.Contains(d.Msg, "cannot be called") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("errors = %v, want a cannot-be-called diagnostic", diags.All())
	}
}

func TestStubFuncGenericParsesInStubsFile(t *testing.T) {
	src := "stub func AnyAs[T](a Any) (T, bool)\n" +
		"stub func append[T](s []T, elem T) []T\n"
	diags := checkNamedSrc(t, "stubs.llx", src)
	if diags.HasErrors() {
		t.Fatalf("unexpected check errors: %v", diags.All())
	}
}

// TestStubFuncTypeRegistryBuiltinsParse proves the actual std/stubs.llx
// declarations for the type registry builtins (TypeId/TypeIdOf/TypeByName/
// AnyNew/AnySet) are syntactically legal stub funcs - these were initially
// missed when the feature landed, leaving the JB plugin's stubs.llx-driven
// tooling with no signature for them at all.
func TestStubFuncTypeRegistryBuiltinsParse(t *testing.T) {
	src := "stub func TypeId[T]() int\n" +
		"stub func TypeIdOf(x Any) int\n" +
		"stub func TypeByName(name string) []int\n" +
		"stub func AnyNew(id int) (Any, bool)\n" +
		"stub func AnySet[T](field Any, value T) bool\n"
	diags := checkNamedSrc(t, "stubs.llx", src)
	if diags.HasErrors() {
		t.Fatalf("unexpected check errors: %v", diags.All())
	}
}

func TestStubFuncAllowedUnderCaseInsensitiveBasename(t *testing.T) {
	// Matches loader's EqualFold skip of stubs.llx on case-insensitive FS.
	src := "stub func args() []string\n"
	diags := checkNamedSrc(t, "std/STUBS.LLX", src)
	if diags.HasErrors() {
		t.Fatalf("unexpected check errors for STUBS.LLX: %v", diags.All())
	}
}
