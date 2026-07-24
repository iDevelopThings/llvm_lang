package sema

import (
	"testing"

	"llvm_lang/src/ast"
	"llvm_lang/src/enums"
)

// This file covers `async func Name(params) { ... }` / bare `await` (see
// LANGUAGE.md's "Coroutines" section): the async marker's own legality
// (top-level FuncDecl only, never a method - void-only this round, no
// declared return type), await's legality (only inside an async function's
// own body, at any nesting depth), calling an async func producing a
// TypeCoroutine handle (a real, storable, non-copyable value - unlike
// TypeGenerator), and the resume/done builtins.

// --- declaring an async func ---

func TestAsyncFuncDeclIsFine(t *testing.T) {
	checkSrc(t, "async func Coro() {\n\tawait\n}\n")
}

func TestAsyncFuncMethodReceiverIsError(t *testing.T) {
	src := "struct Point {\n\tx int\n}\n\nasync func (Point) Move() {\n\tawait\n}\n"
	expectCheckErrors(t, src, 1)
}

func TestAsyncFuncDeclaredReturnTypeIsError(t *testing.T) {
	// Async functions are void-only this round (see LANGUAGE.md's
	// "Coroutines" section for why) - a declared return type is a clean
	// diagnostic, not a silent miscompile.
	src := "async func Coro() int {\n\tawait\n\treturn 1\n}\n"
	expectCheckErrors(t, src, 1)
}

func TestAsyncFuncMissingReturnNotRequired(t *testing.T) {
	// Void-only, exactly like an ordinary void function's body - no
	// "missing return" diagnostic just because the body doesn't explicitly
	// return.
	checkSrc(t, "async func Coro() {\n\tawait\n}\n")
}

// --- await's legality ---

func TestAwaitOutsideAsyncFuncIsError(t *testing.T) {
	src := "func f() {\n\tawait\n}\n"
	expectCheckErrors(t, src, 1)
}

func TestAwaitInsideGeneratorIsError(t *testing.T) {
	// A generator function (yield T) is not an async function - await has
	// no special meaning there.
	src := "func Gen() yield int {\n\tawait\n\tyield 1\n}\n"
	expectCheckErrors(t, src, 1)
}

func TestAwaitInsideAsyncFuncNestedAnyDepthIsFine(t *testing.T) {
	checkSrc(t, "async func Coro() {\n\tif true {\n\t\tfor {\n\t\t\tawait\n\t\t\tbreak\n\t\t}\n\t}\n}\n")
}

func TestYieldInsideAsyncFuncIsError(t *testing.T) {
	// yield's own legality (checkYieldStmt) is untouched by isAsync - an
	// async function is not a generator, so yield is still rejected there
	// exactly like inside any other ordinary function.
	src := "async func Coro() {\n\tyield 1\n}\n"
	expectCheckErrors(t, src, 1)
}

// --- calling an async func produces a coroutine handle ---

func TestCallingAsyncFuncProducesCoroutineType(t *testing.T) {
	tree, info := checkSrc(t, "async func Coro() {\n\tawait\n}\n\nfunc use() {\n\th := Coro()\n\tdelete h\n}\n")
	// Locate the ShortVarDecl `h := Coro()` inside use's own body and check
	// its declared type came back as TypeCoroutine.
	var found bool
	for idx := ast.NodeIndex(1); int(idx) < len(tree.Nodes); idx++ {
		if tree.Nodes[idx].Kind != enums.NodeKinds.ShortVarDecl {
			continue
		}
		if t2, ok := info.Types[idx]; ok && t2.Kind == TypeCoroutine {
			found = true
		}
	}
	if !found {
		t.Fatal("no ShortVarDecl with a TypeCoroutine type found")
	}
}

// TestCoroutineHandleIsNonCopyable proves a coroutine handle can't be
// aliased (`h2 := h`) - only a fresh call result (`h := Coro()`) is legal,
// the same non-copyable rule a destructor-owning struct already gets.
func TestCoroutineHandleIsNonCopyable(t *testing.T) {
	src := "async func Coro() {\n\tawait\n}\n\nfunc use() {\n\th := Coro()\n\th2 := h\n\tdelete h\n\tdelete h2\n}\n"
	expectCheckErrors(t, src, 1)
}

// --- delete on a coroutine handle ---

func TestDeleteCoroutineHandleIsFine(t *testing.T) {
	checkSrc(t, "async func Coro() {\n\tawait\n}\n\nfunc use() {\n\th := Coro()\n\tdelete h\n}\n")
}

func TestDeleteNonPointerNonCoroutineIsError(t *testing.T) {
	src := "func use() {\n\tx := 5\n\tdelete x\n}\n"
	expectCheckErrors(t, src, 1)
}

// --- resume/done builtins ---

func TestResumeCallOnCoroutineHandleIsFine(t *testing.T) {
	tree, info := checkSrc(t, "async func Coro() {\n\tawait\n}\n\nfunc use() bool {\n\th := Coro()\n\tr := resume(h)\n\tdelete h\n\treturn r\n}\n")
	_ = tree
	_ = info
}

func TestDoneCallOnCoroutineHandleIsFine(t *testing.T) {
	checkSrc(t, "async func Coro() {\n\tawait\n}\n\nfunc use() bool {\n\th := Coro()\n\td := done(h)\n\tdelete h\n\treturn d\n}\n")
}

func TestResumeCallOnNonCoroutineIsError(t *testing.T) {
	src := "func use() bool {\n\tx := 5\n\treturn resume(x)\n}\n"
	expectCheckErrors(t, src, 1)
}

func TestDoneCallOnNonCoroutineIsError(t *testing.T) {
	src := "func use() bool {\n\tx := 5\n\treturn done(x)\n}\n"
	expectCheckErrors(t, src, 1)
}

func TestResumeCallWrongArgCountIsError(t *testing.T) {
	src := "async func Coro() {\n\tawait\n}\n\nfunc use() {\n\th := Coro()\n\tresume(h, h)\n\tdelete h\n}\n"
	expectCheckErrors(t, src, 1)
}

// TestPassCoroutineHandleAsArgIsError proves an existing handle can't be
// passed by value either - not just aliased via `:=` (see
// TestCoroutineHandleIsNonCopyable) - since checkNoIllegalCopy's fresh-value
// exception only covers a fresh call/literal at the argument site itself,
// never a reference to an already-existing one.
func TestPassCoroutineHandleAsArgIsError(t *testing.T) {
	src := "async func Coro() {\n\tawait\n}\n\nfunc take(h int) {}\n\nfunc use() {\n\th := Coro()\n\ttake(h)\n\tdelete h\n}\n"
	expectCheckErrors(t, src, 1)
}
