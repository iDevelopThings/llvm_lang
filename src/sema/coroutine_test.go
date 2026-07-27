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

// TestAsyncFuncDeclaredReturnTypeIsFine proves an async function can now
// declare a real result type (see LANGUAGE.md's "Coroutines" section) -
// read back later via result(h).
func TestAsyncFuncDeclaredReturnTypeIsFine(t *testing.T) {
	src := "async func Coro() int {\n\tawait\n\treturn 1\n}\n"
	checkSrc(t, src)
}

// TestAsyncFuncMissingReturnIsError mirrors an ordinary function's own
// "missing return" check (isTerminatingStmt) - await is a plain statement,
// not a branch, so a body ending in one still needs a return on every path.
func TestAsyncFuncMissingReturnIsError(t *testing.T) {
	src := "async func Coro() int {\n\tawait\n}\n"
	expectCheckErrors(t, src, 1)
}

// TestAsyncFuncReturnWrongTypeIsError proves a non-void async function's
// `return expr` is checked against its declared return type via the exact
// same machinery an ordinary function's return already uses.
func TestAsyncFuncReturnWrongTypeIsError(t *testing.T) {
	src := "async func Coro() int {\n\tawait\n\treturn \"nope\"\n}\n"
	expectCheckErrors(t, src, 1)
}

// TestAsyncFuncAndGeneratorReturnTypeIsError proves async combined with a
// `yield T` return type - a wholly different construct - is still rejected,
// now that a declared return type alone no longer is.
func TestAsyncFuncAndGeneratorReturnTypeIsError(t *testing.T) {
	src := "async func Coro() yield int {\n\tawait\n\tyield 1\n}\n"
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

// --- result builtin ---

// TestResultCallOnNonVoidCoroutineIsFine proves result(h)'s own return type
// is h's resolved TypeCoroutine.Elem - int here - with no type argument.
func TestResultCallOnNonVoidCoroutineIsFine(t *testing.T) {
	src := "async func Coro() int {\n\tawait\n\treturn 1\n}\n\nfunc use() int {\n\th := Coro()\n\tresume(h)\n\tv := result(h)\n\tdelete h\n\treturn v\n}\n"
	checkSrc(t, src)
}

// TestResultCallOnVoidCoroutineIsError proves result(h) against an ordinary
// (still void, no declared return type) coroutine is a clean diagnostic, not
// a silently-void result.
func TestResultCallOnVoidCoroutineIsError(t *testing.T) {
	src := "async func Coro() {\n\tawait\n}\n\nfunc use() {\n\th := Coro()\n\tresult(h)\n\tdelete h\n}\n"
	expectCheckErrors(t, src, 1)
}

func TestResultCallOnNonCoroutineIsError(t *testing.T) {
	src := "func use() int {\n\tx := 5\n\treturn result(x)\n}\n"
	expectCheckErrors(t, src, 1)
}

func TestResultCallWrongArgCountIsError(t *testing.T) {
	src := "async func Coro() int {\n\tawait\n\treturn 1\n}\n\nfunc use() {\n\th := Coro()\n\tresult(h, h)\n\tdelete h\n}\n"
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

// --- the `coroutine` type keyword - a real, spellable name for TypeCoroutine
// (see typeFromSymbol's "coroutine" case), usable anywhere an ordinary type
// name is legal: var decl, struct field, function param ---

func TestCoroutineTypeVarDeclIsFine(t *testing.T) {
	checkSrc(t, "func f() {\n\tvar h coroutine\n\tdelete h\n}\n")
}

func TestCoroutineTypeStructFieldIsFine(t *testing.T) {
	checkSrc(t, "struct Entry {\n\th coroutine\n}\n")
}

func TestCoroutineTypeFuncParamIsFine(t *testing.T) {
	src := "async func Coro() {\n\tawait\n}\n\nfunc take(h coroutine) {\n\tdelete h\n}\n\nfunc use() {\n\ttake(Coro())\n}\n"
	checkSrc(t, src)
}

// TestPassExistingHandleToCoroutineParamIsError mirrors
// TestPassCoroutineHandleAsArgIsError, now against a real `coroutine`-typed
// param rather than an unrelated `int` one - the non-copyable rejection is
// identical either way, keyed off Kind alone.
func TestPassExistingHandleToCoroutineParamIsError(t *testing.T) {
	src := "async func Coro() {\n\tawait\n}\n\nfunc take(h coroutine) {\n\tdelete h\n}\n\nfunc use() {\n\th := Coro()\n\ttake(h)\n\tdelete h\n}\n"
	expectCheckErrors(t, src, 1)
}

// TestAssignExistingHandleToCoroutineFieldIsError proves the non-copyable
// rule still fires for a `coroutine`-typed struct field, not just a param -
// only a fresh call result may fill it (see checkNoIllegalCopy's fresh-
// construction exception), never an existing named handle.
func TestAssignExistingHandleToCoroutineFieldIsError(t *testing.T) {
	src := "struct Entry {\n\th coroutine\n}\n\nasync func Coro() {\n\tawait\n}\n\nfunc use() {\n\th := Coro()\n\te := Entry{}\n\te.h = h\n\tdelete h\n}\n"
	expectCheckErrors(t, src, 1)
}

// TestAssignFreshHandleToCoroutineFieldIsFine is the accepted counterpart -
// `entry.h = Coro()` fills the field with a brand-new handle, exactly as
// fresh as a composite-literal element or a function argument.
func TestAssignFreshHandleToCoroutineFieldIsFine(t *testing.T) {
	src := "struct Entry {\n\th coroutine\n}\n\nasync func Coro() {\n\tawait\n}\n\nfunc use() {\n\te := Entry{}\n\te.h = Coro()\n}\n"
	checkSrc(t, src)
}

// TestDynamicArrayOfCoroutineFieldStructIsError proves a struct containing a
// `coroutine` field is rejected as a dynamic-array element exactly like any
// other destructor-owning/non-copyable struct already is (see
// TestDynamicArrayOfNonCopyableElementTypeIsError) - the coroutine field
// alone (no explicit destructor()) is enough to make Entry non-copyable.
func TestDynamicArrayOfCoroutineFieldStructIsError(t *testing.T) {
	src := "struct Entry {\n\th coroutine\n}\n\nvar arr []Entry\n"
	expectCheckErrors(t, src, 1)
}

// TestFixedArrayOfCoroutineFieldStructFreshConstructionIsFine proves a
// fixed-size array of a coroutine-containing struct still works via the
// existing fresh-construction exception - each element its own composite
// literal, each field its own fresh async call.
func TestFixedArrayOfCoroutineFieldStructFreshConstructionIsFine(t *testing.T) {
	src := "struct Entry {\n\th coroutine\n}\n\nasync func Coro() {\n\tawait\n}\n\nfunc f() {\n" +
		"\ta := [2]Entry{Entry{Coro()}, Entry{Coro()}}\n" +
		"\tdelete a[0].h\n\tdelete a[1].h\n" +
		"}\n"
	checkSrc(t, src)
}

// TestCoroutineFieldOmittedInCompositeLitIsNilHandle proves an omitted
// `coroutine` field in a composite literal is a safe, defined nil handle -
// `delete`/resume/done on it are already safe no-ops on a nil handle (see
// LANGUAGE.md's "Coroutines" section), so a zero-initialized Entry compiles
// and its field can be deleted with no async call ever having run.
func TestCoroutineFieldOmittedInCompositeLitIsNilHandle(t *testing.T) {
	src := "struct Entry {\n\th coroutine\n}\n\nfunc f() {\n\te := Entry{}\n\tdelete e.h\n}\n"
	checkSrc(t, src)
}

// TestNonVoidCoroutineWidensToBareCoroutineVarDecl/FuncParam/StructField prove
// a result-returning coroutine's own handle still fits a bare `coroutine`-
// typed slot - driving one by hand (resume/done/delete) never needs to know
// its result type, only result(h) does. Without this, a non-void async
// function couldn't be used with std/scheduler's own Entry.Handle field at
// all (Type.Equal now distinguishes TypeCoroutine by its own Elem).
func TestNonVoidCoroutineWidensToBareCoroutineVarDecl(t *testing.T) {
	src := "async func Coro() int {\n\tawait\n\treturn 1\n}\n\nfunc f() {\n\tvar h coroutine = Coro()\n\tdelete h\n}\n"
	checkSrc(t, src)
}

func TestNonVoidCoroutineWidensToBareCoroutineFuncParam(t *testing.T) {
	src := "async func Coro() int {\n\tawait\n\treturn 1\n}\n\nfunc take(h coroutine) {\n\tdelete h\n}\n\nfunc use() {\n\ttake(Coro())\n}\n"
	checkSrc(t, src)
}

func TestNonVoidCoroutineWidensToBareCoroutineStructField(t *testing.T) {
	src := "struct Entry {\n\th coroutine\n}\n\nasync func Coro() int {\n\tawait\n\treturn 1\n}\n\nfunc use() {\n\te := Entry{}\n\te.h = Coro()\n}\n"
	checkSrc(t, src)
}

// TestBareCoroutineCannotWidenToNonVoidCoroutine proves the widening is
// one-directional - a variable/field declared with a real result type still
// requires an exact Elem match, since result(h) against it needs a real,
// specific type to read back.
func TestBareCoroutineCannotWidenToNonVoidCoroutine(t *testing.T) {
	src := "async func Coro() int {\n\tawait\n\treturn 1\n}\n\nfunc take(h coroutine) {\n\tvar v coroutine = h\n\tdelete v\n}\n"
	expectCheckErrors(t, src, 1)
}

// TestAsyncFuncNonCopyableResultTypeIsError proves a destructor-owning
// result type is rejected at declaration time - the frame's own teardown
// never runs a destructor on whatever was left in an unread promise slot.
func TestAsyncFuncNonCopyableResultTypeIsError(t *testing.T) {
	src := "struct Res {\n\tid int\n\tdestructor() {}\n}\n\nasync func Make() Res {\n\tawait\n\treturn Res{1}\n}\n"
	expectCheckErrors(t, src, 1)
}

// TestAsyncFuncReferencedAsBareValueIsError proves referencing an async
// function as a first-class value (not calling it) is a clean diagnostic -
// calling one doesn't produce its own result the way an ordinary call does,
// so it could never mean the same thing through a plain func(...) value.
func TestAsyncFuncReferencedAsBareValueIsError(t *testing.T) {
	src := "async func Coro() int {\n\tawait\n\treturn 1\n}\n\nfunc f() {\n\tg := Coro\n\tprint(g)\n}\n"
	expectCheckErrors(t, src, 1)
}

func TestVoidAsyncFuncReferencedAsBareValueIsError(t *testing.T) {
	src := "async func Coro() {\n\tawait\n}\n\nfunc f() {\n\tg := Coro\n\tprint(g)\n}\n"
	expectCheckErrors(t, src, 1)
}
