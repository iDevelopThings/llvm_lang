package lsp

import (
	"strings"
	"testing"

	"llvm_lang/src/lexer"
)

// genericRefsFixture is shared by every test below: a global touched from
// inside a generic function's own body, instantiated twice with different
// type arguments - the exact shape the reported bug needed (a clone of the
// body per instantiation, each carrying its own copy of every reference the
// real body makes).
const genericRefsFixture = `var counter int = 0

func Bump[T](x T) T {
	counter = counter + 1
	return x
}

func main() int {
	Bump(1)
	Bump(1.5)
	return counter
}
`

// TestReferences_GenericFunc_NoDuplicateLocationsAcrossInstantiations is the
// regression test for a real reported bug: References iterated Info.Refs
// directly, which now contains one phantom entry per instantiation's own
// clone (same source span as the template's real occurrence) - touching a
// global from inside a generic function showed duplicate reference
// locations, once per instantiation. counter has exactly 4 real
// occurrences in genericRefsFixture's own source text (the declaration,
// Bump's own assignment target and its read on the right-hand side, and
// main's own `return counter`) - regardless of how many times Bump gets
// instantiated.
func TestReferences_GenericFunc_NoDuplicateLocationsAcrossInstantiations(t *testing.T) {
	w, path := singleFileWorkspace(t, genericRefsFixture)
	fa, _ := w.Analysis(path)

	declOffset := strings.Index(fa.Tree.File.Src, "counter int")
	pos := byteOffsetToPosition(fa.Tree.File, lexer.Pos(declOffset))

	locs := w.References(path, pos, true)
	if len(locs) != 4 {
		t.Fatalf("len(locs) = %d, want 4 (declaration + Bump's own target+read + main's return, no clone duplicates): %+v", len(locs), locs)
	}
}

// TestReferences_GenericFunc_DeclarationFindsEveryInstantiation is the
// regression test for the other half of the same reported bug: clicking a
// generic function's own declaration only returned its own name - never
// any inferred call site, since each instantiation resolves the callee to
// a *different* specialized Symbol (Bump[int] vs Bump[f64]), not the
// template's own Symbol.
func TestReferences_GenericFunc_DeclarationFindsEveryInstantiation(t *testing.T) {
	w, path := singleFileWorkspace(t, genericRefsFixture)
	fa, _ := w.Analysis(path)

	declOffset := strings.Index(fa.Tree.File.Src, "Bump[T]")
	pos := byteOffsetToPosition(fa.Tree.File, lexer.Pos(declOffset))

	locs := w.References(path, pos, true)
	if len(locs) != 3 {
		t.Fatalf("len(locs) = %d, want 3 (the declaration + both Bump(1)/Bump(1.5) call sites): %+v", len(locs), locs)
	}
}

// TestReferences_GenericFunc_CallSiteFindsSiblingInstantiations covers the
// reverse direction: clicking one specific instantiation's own call site
// must still find the declaration and every *other* instantiation's call
// site, not just occurrences sharing its own type argument.
func TestReferences_GenericFunc_CallSiteFindsSiblingInstantiations(t *testing.T) {
	w, path := singleFileWorkspace(t, genericRefsFixture)
	fa, _ := w.Analysis(path)

	callOffset := strings.Index(fa.Tree.File.Src, "Bump(1)")
	pos := byteOffsetToPosition(fa.Tree.File, lexer.Pos(callOffset))

	locs := w.References(path, pos, true)
	if len(locs) != 3 {
		t.Fatalf("len(locs) = %d, want 3 (declaration + Bump(1) + Bump(1.5)): %+v", len(locs), locs)
	}

	withoutDecl := w.References(path, pos, false)
	if len(withoutDecl) != 2 {
		t.Errorf("len(withoutDecl) = %d, want 2 (both call sites, declaration excluded): %+v", len(withoutDecl), withoutDecl)
	}
}

// genericMethodRefsFixture instantiates Box[T]'s own Get method against two
// *different* receiver struct instantiations (Box[int] and Box[f64]) -
// each gets its own separate per-struct method template (see
// GenericMethod.templates), so unifying them needs the declaring method
// Symbol's own GenericMethod field, not just GenericInfo/Generic (which
// only ever covers a free func/struct template, never a struct's own
// method - see Symbol.GenericMethod's own doc comment for why the two
// can't share one mechanism).
const genericMethodRefsFixture = `struct Box[T] {
	value T
}
func (Box[T]) Get() T {
	return this.value
}
func f() int {
	a := Box[int]{1}
	b := Box[f64]{2.5}
	print(a.Get())
	print(b.Get())
	return 0
}
`

// TestReferences_GenericStructMethod_DeclarationFindsEveryInstantiation is
// the regression test for a real bug caught by review: a generic struct's
// own method never unified across instantiations at all (not even within
// one), since GenericMethod.Sym (the method's real, Refs-anchored
// declaring Symbol) never had Generic set, and each receiver
// instantiation's own synthetic per-method template Symbol
// (GenericMethod.templateFor's own mt.Symbol) was a distinct, unconnected
// object - GenericTemplate pointed at that synthetic Symbol instead of the
// real declaration, so neither direction ever found the other.
func TestReferences_GenericStructMethod_DeclarationFindsEveryInstantiation(t *testing.T) {
	w, path := singleFileWorkspace(t, genericMethodRefsFixture)
	fa, _ := w.Analysis(path)

	declOffset := strings.Index(fa.Tree.File.Src, "Get() T")
	pos := byteOffsetToPosition(fa.Tree.File, lexer.Pos(declOffset))

	locs := w.References(path, pos, true)
	if len(locs) != 3 {
		t.Fatalf("len(locs) = %d, want 3 (the declaration + a.Get() + b.Get(), two different receiver instantiations): %+v", len(locs), locs)
	}
}

// TestReferences_GenericStructMethod_CallSiteFindsDeclarationAndSibling
// covers the reverse direction of the same bug: clicking one instantiated
// call site must still find the real declaration and the *other* receiver
// instantiation's own call site.
func TestReferences_GenericStructMethod_CallSiteFindsDeclarationAndSibling(t *testing.T) {
	w, path := singleFileWorkspace(t, genericMethodRefsFixture)
	fa, _ := w.Analysis(path)

	callOffset := strings.Index(fa.Tree.File.Src, "a.Get()") + len("a.")
	pos := byteOffsetToPosition(fa.Tree.File, lexer.Pos(callOffset))

	locs := w.References(path, pos, true)
	if len(locs) != 3 {
		t.Fatalf("len(locs) = %d, want 3 (declaration + a.Get() + b.Get()): %+v", len(locs), locs)
	}
}
