package sema

import (
	"testing"

	"llvm_lang/src/lexer"
	"llvm_lang/src/parser"
)

// --- struct destructors and the non-copyable rule they impose (see
// LANGUAGE.md's "Destructors" section) ---

const fileHandleSrc = "struct Box {\n" +
	"\tv int\n\n" +
	"\tconstructor(x int) {\n" +
	"\t\tthis.v = x\n" +
	"\t}\n" +
	"}\n" +
	"struct FileHandle {\n" +
	"\traw *Box\n\n" +
	"\tconstructor(v int) {\n" +
	"\t\tthis.raw = new Box(v)\n" +
	"\t}\n" +
	"\tdestructor() {\n" +
	"\t\tdelete this.raw\n" +
	"\t}\n" +
	"}\n"

// TestDestructorBodyAssignsThis covers a destructor's body resolving/typing
// exactly like inside an ordinary method - `this`, a field access, `delete`.
func TestDestructorBodyAssignsThis(t *testing.T) {
	checkSrc(t, fileHandleSrc)
}

// TestDestructorDuplicateIsError covers rejecting two destructors on the
// same struct - a structural error raised right at struct-declaration time
// (declareDestructor, resolve.go), not a call-time one (there's no call
// site for a destructor at all) - so, like duplicate-arity constructors,
// this is a Resolve-phase diagnostic, not a Check-phase one.
func TestDestructorDuplicateIsError(t *testing.T) {
	src := "struct Point {\n" +
		"\tx int\n\n" +
		"\tdestructor() {\n\t\tthis.x = 0\n\t}\n" +
		"\tdestructor() {\n\t\tthis.x = 1\n\t}\n" +
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

// TestDestructorMustTakeNoParametersIsError covers rejecting a destructor
// declared with a non-empty parameter list - the grammar (parseDestructorDecl)
// accepts the same shape a constructor's own paramList would, so this is
// what actually enforces "always zero parameters" (checkDestructorDecl).
func TestDestructorMustTakeNoParametersIsError(t *testing.T) {
	src := "struct Point {\n" +
		"\tx int\n\n" +
		"\tdestructor(v int) {\n\t\tthis.x = v\n\t}\n" +
		"}\n"
	expectCheckErrors(t, src, 1)
}

// TestDestructorCannotReturnAValue mirrors
// TestConstructorCannotReturnAValue: a destructor has no declared return
// type, so a bare `return` is fine but `return expr` is rejected exactly
// like inside any other void function/method.
func TestDestructorCannotReturnAValue(t *testing.T) {
	src := "struct Point {\n" +
		"\tx int\n\n" +
		"\tdestructor() {\n" +
		"\t\treturn 5\n" +
		"\t}\n" +
		"}\n"
	expectCheckErrors(t, src, 1)
}

// TestNonCopyableShortVarDeclCopyIsError covers the core `b := a` case: `a`
// is constructed fresh (legal on its own), then copying it into `b` is
// rejected.
func TestNonCopyableShortVarDeclCopyIsError(t *testing.T) {
	src := fileHandleSrc + "func f() {\n\ta := FileHandle(1)\n\tb := a\n}\n"
	expectCheckErrors(t, src, 1)
}

// TestNonCopyableAssignmentCopyIsError covers the `b = a` (existing plain
// assignment, not `:=`) case.
func TestNonCopyableAssignmentCopyIsError(t *testing.T) {
	src := fileHandleSrc + "func f() {\n\ta := FileHandle(1)\n\tvar b FileHandle\n\tb = a\n}\n"
	expectCheckErrors(t, src, 1)
}

// TestNonCopyableByValueParameterIsError covers rejecting a non-copyable
// type passed by value as a function argument.
func TestNonCopyableByValueParameterIsError(t *testing.T) {
	src := fileHandleSrc + "func consume(f FileHandle) {\n}\nfunc g() {\n\ta := FileHandle(1)\n\tconsume(a)\n}\n"
	expectCheckErrors(t, src, 1)
}

// TestNonCopyableByValueParameterAllowsFreshConstruction covers that a
// function argument follows the same fresh-construction exception a var
// decl/assignment/composite-literal element does: passing a freshly
// constructed value directly as the argument is legal - the callee's own
// parameter becomes its one and only owner (see checkNoIllegalCopy's own
// doc comment for why this is sound with no extra machinery, unlike
// returning one by value).
func TestNonCopyableByValueParameterAllowsFreshConstruction(t *testing.T) {
	src := fileHandleSrc + "func consume(f FileHandle) {\n}\nfunc g() {\n\tconsume(FileHandle(1))\n}\n"
	checkSrc(t, src)
}

// TestNonCopyableByValueReturnIsError covers rejecting a non-copyable type
// returned by value from a function.
func TestNonCopyableByValueReturnIsError(t *testing.T) {
	src := fileHandleSrc + "func make() FileHandle {\n\treturn FileHandle(1)\n}\n"
	expectCheckErrors(t, src, 1)
}

// TestNonCopyableFreshConstructionVarDeclIsLegal covers the one deliberate
// exception: constructing a fresh instance via a constructor call, and
// separately via a composite literal, both remain completely legal for a
// non-copyable type - it's creating the one instance, not duplicating an
// existing one.
func TestNonCopyableFreshConstructionVarDeclIsLegal(t *testing.T) {
	checkSrc(t, fileHandleSrc+"func f() {\n\ta := FileHandle(1)\n\tprint(a.raw)\n}\n")
	checkSrc(t, fileHandleSrc+"func g() {\n\tb := FileHandle{nil}\n\tprint(b.raw)\n}\n")
}

// TestNonCopyableNewIsLegal covers `new FileHandle(...)` remaining
// completely legal - `new` always produces a pointer, which is freely
// copyable regardless of its pointee's own copyability (the whole point of
// the pointer-field-plus-manual-delete escape hatch).
func TestNonCopyableNewIsLegal(t *testing.T) {
	checkSrc(t, fileHandleSrc+"func f() {\n\tp := new FileHandle(1)\n\tq := p\n\tdelete q\n}\n")
}

// TestNonCopyableMethodCallStillWorks covers calling a method on a
// non-copyable value remaining completely unaffected - a method receiver is
// already always an implicit pointer, never a copy, so this was true before
// this feature and stays true.
func TestNonCopyableMethodCallStillWorks(t *testing.T) {
	src := fileHandleSrc +
		"func (FileHandle) reset() {\n\tdelete this.raw\n\tthis.raw = new Box(0)\n}\n" +
		"func f() {\n\ta := FileHandle(1)\n\ta.reset()\n}\n"
	checkSrc(t, src)
}

// TestStructEmbeddingNonCopyableFieldIsNonCopyable covers the transitive
// propagation rule: a struct containing a field whose own type is
// non-copyable becomes non-copyable itself, even though it declares no
// destructor of its own.
func TestStructEmbeddingNonCopyableFieldIsNonCopyable(t *testing.T) {
	src := fileHandleSrc +
		"struct Wrapper {\n\tf FileHandle\n}\n" +
		"func f() {\n\tw := Wrapper{FileHandle(1)}\n\tw2 := w\n\tprint(w2.f.raw)\n}\n"
	expectCheckErrors(t, src, 1)
}

// TestFixedArrayOfNonCopyableTypeIsNonCopyable covers a fixed-size array
// `[N]T` being non-copyable whenever T is - the same propagation rule
// StructInfo.Copyable applies to an embedded field, one level down.
func TestFixedArrayOfNonCopyableTypeIsNonCopyable(t *testing.T) {
	src := fileHandleSrc +
		"func f() {\n" +
		"\ta := [2]FileHandle{FileHandle(1), FileHandle(2)}\n" +
		"\tb := a\n" +
		"\tprint(b[0].raw)\n" +
		"}\n"
	expectCheckErrors(t, src, 1)
}

// TestFixedArrayOfNonCopyableTypeFreshConstructionIsLegal covers that
// building a fixed array of a non-copyable element type - each element its
// own fresh construction - remains legal, mirroring
// TestNonCopyableFreshConstructionVarDeclIsLegal one level up.
func TestFixedArrayOfNonCopyableTypeFreshConstructionIsLegal(t *testing.T) {
	src := fileHandleSrc +
		"func f() {\n" +
		"\ta := [2]FileHandle{FileHandle(1), FileHandle(2)}\n" +
		"\tprint(a[0].raw)\n" +
		"}\n"
	checkSrc(t, src)
}

// TestDynamicArrayOfNonCopyableElementTypeIsError covers LANGUAGE.md's
// explicitly-out-of-scope case: a dynamic array (`[]T`) whose element type
// is non-copyable is rejected with a clear diagnostic, rather than silently
// mishandled (make/append/growth all copy element bytes around with no
// destructor-cascading concept at all).
func TestDynamicArrayOfNonCopyableElementTypeIsError(t *testing.T) {
	src := fileHandleSrc + "var s []FileHandle\n"
	expectCheckErrors(t, src, 1)
}

// TestPlainStructWithoutDestructorRemainsCopyable is the regression case:
// an ordinary struct with no destructor anywhere in its field chain is
// completely unaffected by this feature - a plain `b := a` copy remains
// perfectly legal, same as before.
func TestPlainStructWithoutDestructorRemainsCopyable(t *testing.T) {
	checkSrc(t, pointSrc+"func f() {\n\ta := Point{1, 2}\n\tb := a\n\tprint(b.x)\n}\n")
}
