// symbolDetail's own rendering rules are covered by sema/query_test.go
// (FuncSignatureText/StructFieldsText, including the generic-instantiation
// and unchecked-template cases) - what's left here is only what's actually
// lsp-specific: resolving the *declaring* file's Info out of the workspace
// snapshot, and the kinds this function deliberately renders nothing for.
package lsp

import (
	"strings"
	"testing"

	"llvm_lang/src/ast"
	"llvm_lang/src/lexer"
	"llvm_lang/src/sema"
)

// symbolAt resolves the symbol at needle's own offset in path's source.
func symbolAt(t *testing.T, w *Workspace, path, needle string) *sema.Symbol {
	t.Helper()
	fa, _ := w.Analysis(path)
	pos := byteOffsetToPosition(fa.Tree.File, lexer.Pos(strings.Index(fa.Tree.File.Src, needle)))
	fa2, n, ok := w.resolveNode(path, pos)
	if !ok {
		t.Fatalf("resolveNode failed at %q", needle)
	}
	sym := fa2.Info.Refs[n]
	if sym == nil {
		t.Fatalf("no symbol resolved at %q", needle)
	}
	return sym
}

// TestSymbolDetail_UsesDeclaringFilesInfo covers the workspace plumbing:
// the Type-aware rendering only happens when the *declaring* file's Info is
// found in the snapshot symbolDetail is handed - an empty snapshot must
// still render (falling back to source text), never panic.
func TestSymbolDetail_UsesDeclaringFilesInfo(t *testing.T) {
	w, path := singleFileWorkspace(t, `func Insert(v int, n int) int {
	return v + n
}
`)
	sym := symbolAt(t, w, path, "Insert")

	if got, want := symbolDetail(w.infoForTree(sym.Tree), sym), "(v int, n int) int"; got != want {
		t.Errorf("symbolDetail = %q, want %q", got, want)
	}
	if got, want := symbolDetail(nil, sym), "(v int, n int) int"; got != want {
		t.Errorf("symbolDetail with no declaring Info = %q, want %q (the source-text fallback)", got, want)
	}
}

// TestSymbolDetail_UnrenderableKinds covers the default branch: a symbol
// kind with no signature/field-list shape to render (a local, a parameter,
// an enum) renders nothing at all rather than something misleading.
func TestSymbolDetail_UnrenderableKinds(t *testing.T) {
	w, path := singleFileWorkspace(t, `enum Shape {
	Circle
	Square
}

func f(n int) int {
	total := 0
	return total + n
}
`)
	for _, needle := range []string{"Shape", "n int", "total := 0"} {
		sym := symbolAt(t, w, path, needle)
		if got := symbolDetail(w.infoForTree(sym.Tree), sym); got != "" {
			t.Errorf("symbolDetail(%s, kind %v) = %q, want \"\"", needle, sym.Kind, got)
		}
	}
}

// TestSymbolDetail_NilAndUndeclaredSymbols covers the guard clause - a nil
// symbol, and one with no declaration node to read a signature from.
func TestSymbolDetail_NilAndUndeclaredSymbols(t *testing.T) {
	if got := symbolDetail(nil, nil); got != "" {
		t.Errorf("symbolDetail(nil) = %q, want \"\"", got)
	}
	sym := &sema.Symbol{
		Kind: sema.SymFunc,
		Decl: ast.InvalidNode,
	}
	if got := symbolDetail(nil, sym); got != "" {
		t.Errorf("symbolDetail(symbol with no Tree/Decl) = %q, want \"\"", got)
	}
}
