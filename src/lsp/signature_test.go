package lsp

import (
	"strings"
	"testing"

	"llvm_lang/src/lexer"
)

func TestSymbolDetail_FuncSignature(t *testing.T) {
	w, path := singleFileWorkspace(t, `func Insert(v int, n int) int {
	return v + n
}
`)
	fa, _ := w.Analysis(path)
	nameOffset := strings.Index(fa.Tree.File.Src, "Insert")
	pos := byteOffsetToPosition(fa.Tree.File, lexer.Pos(nameOffset))

	fa2, n, ok := w.resolveNode(path, pos)
	if !ok {
		t.Fatal("resolveNode failed")
	}
	sym := fa2.Info.Refs[n]
	if sym == nil {
		t.Fatal("no symbol resolved for Insert")
	}

	got := symbolDetail(w, sym)
	want := "(v int, n int) int"
	if got != want {
		t.Errorf("symbolDetail = %q, want %q", got, want)
	}
}

func TestSymbolDetail_StructFields(t *testing.T) {
	w, path := singleFileWorkspace(t, `struct Point {
	x int
	y int
}

func f() int {
	p := Point{1, 2}
	return p.x
}
`)
	fa, _ := w.Analysis(path)
	nameOffset := strings.Index(fa.Tree.File.Src, "struct Point") + len("struct ")
	pos := byteOffsetToPosition(fa.Tree.File, lexer.Pos(nameOffset))

	fa2, n, ok := w.resolveNode(path, pos)
	if !ok {
		t.Fatal("resolveNode failed")
	}
	sym := fa2.Info.Refs[n]
	if sym == nil {
		t.Fatal("no symbol resolved for Point")
	}

	got := symbolDetail(w, sym)
	want := "{ x int, y int }"
	if got != want {
		t.Errorf("symbolDetail = %q, want %q", got, want)
	}
}

// TestSymbolDetail_GenericMethod_ShowsInstantiatedTypes covers the
// Type-first half of symbolDetail's own contract: an instantiated generic
// method's own Detail must show the *substituted* concrete types (its own
// instantiation clone gets separately-checked Info.Types entries), not the
// template's own literal "T" spelling.
func TestSymbolDetail_GenericMethod_ShowsInstantiatedTypes(t *testing.T) {
	w, path := singleFileWorkspace(t, `struct Box[T] {
	value T
}
func (Box[T]) Get() T {
	return this.value
}
func f() int {
	b := Box[int]{7}
	return b.Get()
}
`)
	fa, _ := w.Analysis(path)
	callOffset := strings.Index(fa.Tree.File.Src, "b.Get()") + len("b.")
	pos := byteOffsetToPosition(fa.Tree.File, lexer.Pos(callOffset))

	fa2, n, ok := w.resolveNode(path, pos)
	if !ok {
		t.Fatal("resolveNode failed")
	}
	sym := fa2.Info.Refs[n]
	if sym == nil {
		t.Fatal("no symbol resolved for Get")
	}

	got := symbolDetail(w, sym)
	want := "() int"
	if got != want {
		t.Errorf("symbolDetail = %q, want %q (the instantiated Box[int]'s own substituted return type)", got, want)
	}
}

// TestSymbolDetail_UnresolvedGenericTemplate_FallsBackToSourceText covers
// the raw-text fallback: a generic template never gets checked
// (Info.Types), so its own Detail must still show something useful -
// exactly what was written, type parameter names included.
func TestSymbolDetail_UnresolvedGenericTemplate_FallsBackToSourceText(t *testing.T) {
	w, path := singleFileWorkspace(t, `func Sum[T](a T, b T) T {
	return a + b
}
`)
	fa, _ := w.Analysis(path)
	nameOffset := strings.Index(fa.Tree.File.Src, "Sum[T]")
	pos := byteOffsetToPosition(fa.Tree.File, lexer.Pos(nameOffset))

	fa2, n, ok := w.resolveNode(path, pos)
	if !ok {
		t.Fatal("resolveNode failed")
	}
	sym := fa2.Info.Refs[n]
	if sym == nil {
		t.Fatal("no symbol resolved for Sum")
	}

	got := symbolDetail(w, sym)
	want := "(a T, b T) T"
	if got != want {
		t.Errorf("symbolDetail = %q, want %q (raw source fallback for an unchecked template)", got, want)
	}
}
