package lsp

import (
	"strings"
	"testing"

	"llvm_lang/src/lexer"

	protocol "github.com/tliron/glsp/protocol_3_16"
)

// appAnalysis is a small shared fixture: a two-package program (mirroring
// loadProgram's own precedent) whose "app" file exercises a struct (for
// DocumentSymbols), a reassigned local (for DocumentHighlight's Read/Write
// split), and multi-line bodies/a doc comment (for FoldingRanges) all at
// once - returned alongside a *Workspace pre-seeded with the analysis (no
// afero/real filesystem involved - Workspace.analysis is populated
// directly, same package).
func appAnalysis(t *testing.T) (w *Workspace, path string, fa *FileAnalysis) {
	t.Helper()
	prog := loadProgram(t, `
func Add(a int, b int) int {
	return a + b
}
`, `
import "../mathutils"

// Point is a 2D coordinate.
// See LANGUAGE.md's "Structs" section.
struct Point {
	x int
	y int
}

func main() int {
	total := 0
	total = total + 1
	return mathutils.Add(total, 1)
}
`)
	result := analyzeProgram(prog, 1)
	for p, f := range result {
		if strings.Contains(p, "app") {
			path, fa = p, f
		}
	}
	if fa == nil {
		t.Fatal("app/main.llx not found in analysis result")
	}
	return &Workspace{analysis: map[string]*FileAnalysis{path: fa}}, path, fa
}

func TestDocumentSymbols(t *testing.T) {
	w, path, _ := appAnalysis(t)

	syms := w.DocumentSymbols(path)
	if len(syms) != 2 {
		t.Fatalf("len(syms) = %d, want 2 (Point, main)", len(syms))
	}
	if syms[0].Name != "Point" || len(syms[0].Children) != 2 {
		t.Errorf("syms[0] = %q with %d children, want \"Point\" with 2 (x, y)", syms[0].Name, len(syms[0].Children))
	}
	if syms[1].Name != "main" {
		t.Errorf("syms[1].Name = %q, want \"main\"", syms[1].Name)
	}
}

func TestDocumentHighlight_ReadWrite(t *testing.T) {
	w, path, fa := appAnalysis(t)

	declOffset := strings.Index(fa.Tree.File.Src, "total := 0")
	pos := byteOffsetToPosition(fa.Tree.File, lexer.Pos(declOffset))

	highlights := w.DocumentHighlight(path, pos)
	if len(highlights) != 4 {
		t.Fatalf("len(highlights) = %d, want 4 (decl, reassignment target, its own read, and the Add(total, ...) read)", len(highlights))
	}

	if writes, reads := highlightKinds(highlights); writes != 2 || reads != 2 {
		t.Errorf("writes=%d reads=%d, want writes=2 (decl + reassignment target) reads=2", writes, reads)
	}
}

// TestDocumentHighlight_GenericFunc_NoDuplicatesAcrossInstantiations is the
// DocumentHighlight-side regression test for the same reported bug as
// references_test.go's identically-shaped ones: a monomorphized generic's
// own instantiations each carry a clone of its body, and DocumentHighlight
// iterated Info.Refs directly - counter showed one duplicate highlight
// range per instantiation of Bump, on top of its real occurrences.
func TestDocumentHighlight_GenericFunc_NoDuplicatesAcrossInstantiations(t *testing.T) {
	w, path := singleFileWorkspace(t, genericRefsFixture)
	fa, _ := w.Analysis(path)

	declOffset := strings.Index(fa.Tree.File.Src, "counter int")
	pos := byteOffsetToPosition(fa.Tree.File, lexer.Pos(declOffset))

	highlights := w.DocumentHighlight(path, pos)
	if len(highlights) != 4 {
		t.Fatalf("len(highlights) = %d, want 4 (declaration, Bump's own target+read, main's return - no clone duplicates): %+v", len(highlights), highlights)
	}

	if writes, reads := highlightKinds(highlights); writes != 2 || reads != 2 {
		t.Errorf("writes=%d reads=%d, want writes=2 (decl + Bump's own assignment target) reads=2", writes, reads)
	}
}

// highlightKinds counts h's own Read/Write split, the two kinds
// DocumentHighlight ever emits.
func highlightKinds(highlights []protocol.DocumentHighlight) (writes, reads int) {
	for _, h := range highlights {
		switch *h.Kind {
		case protocol.DocumentHighlightKindWrite:
			writes++
		case protocol.DocumentHighlightKindRead:
			reads++
		}
	}
	return writes, reads
}

// TestDocumentHighlight_GenericFunc_DeclarationHighlightsEveryInstantiation
// is the DocumentHighlight half of the unification References already got:
// each instantiation resolves its callee to a *different* specialized
// Symbol, so a bare pointer compare against the template's own Symbol
// highlighted only the declaration itself.
func TestDocumentHighlight_GenericFunc_DeclarationHighlightsEveryInstantiation(t *testing.T) {
	w, path := singleFileWorkspace(t, genericRefsFixture)
	fa, _ := w.Analysis(path)

	declOffset := strings.Index(fa.Tree.File.Src, "Bump[T]")
	pos := byteOffsetToPosition(fa.Tree.File, lexer.Pos(declOffset))

	highlights := w.DocumentHighlight(path, pos)
	if len(highlights) != 3 {
		t.Fatalf("len(highlights) = %d, want 3 (the declaration + both Bump(1)/Bump(1.5) call sites): %+v", len(highlights), highlights)
	}
	if writes, reads := highlightKinds(highlights); writes != 1 || reads != 2 {
		t.Errorf("writes=%d reads=%d, want writes=1 (the declaration) reads=2 (both call sites)", writes, reads)
	}
}

// TestDocumentHighlight_GenericFunc_CallSiteHighlightsSiblings covers the
// reverse direction: one instantiation's own call site must still highlight
// the declaration and every sibling instantiation.
func TestDocumentHighlight_GenericFunc_CallSiteHighlightsSiblings(t *testing.T) {
	w, path := singleFileWorkspace(t, genericRefsFixture)
	fa, _ := w.Analysis(path)

	callOffset := strings.Index(fa.Tree.File.Src, "Bump(1)")
	pos := byteOffsetToPosition(fa.Tree.File, lexer.Pos(callOffset))

	highlights := w.DocumentHighlight(path, pos)
	if len(highlights) != 3 {
		t.Fatalf("len(highlights) = %d, want 3 (declaration + Bump(1) + Bump(1.5)): %+v", len(highlights), highlights)
	}
}

// TestDocumentHighlight_GenericStructMethod_DeclarationHighlightsEveryInstantiation
// is the harder half of the same fix: a generic struct's own method gets a
// separate per-receiver-instantiation template Symbol, unified only through
// Symbol.GenericMethod (see sema.Symbol.GenericFamily).
func TestDocumentHighlight_GenericStructMethod_DeclarationHighlightsEveryInstantiation(t *testing.T) {
	w, path := singleFileWorkspace(t, genericMethodRefsFixture)
	fa, _ := w.Analysis(path)

	declOffset := strings.Index(fa.Tree.File.Src, "Get() T")
	pos := byteOffsetToPosition(fa.Tree.File, lexer.Pos(declOffset))

	highlights := w.DocumentHighlight(path, pos)
	if len(highlights) != 3 {
		t.Fatalf("len(highlights) = %d, want 3 (the declaration + a.Get() + b.Get(), two different receiver instantiations): %+v", len(highlights), highlights)
	}
}

// TestDocumentHighlight_GenericStructMethod_CallSiteHighlightsDeclarationAndSibling
// covers that same method case from one instantiated call site.
func TestDocumentHighlight_GenericStructMethod_CallSiteHighlightsDeclarationAndSibling(t *testing.T) {
	w, path := singleFileWorkspace(t, genericMethodRefsFixture)
	fa, _ := w.Analysis(path)

	callOffset := strings.Index(fa.Tree.File.Src, "a.Get()") + len("a.")
	pos := byteOffsetToPosition(fa.Tree.File, lexer.Pos(callOffset))

	highlights := w.DocumentHighlight(path, pos)
	if len(highlights) != 3 {
		t.Fatalf("len(highlights) = %d, want 3 (declaration + a.Get() + b.Get()): %+v", len(highlights), highlights)
	}
}

func TestFoldingRanges(t *testing.T) {
	w, path, _ := appAnalysis(t)

	folds := w.FoldingRanges(path)

	var sawComment, sawFuncBody bool
	for _, f := range folds {
		if f.Kind != nil && *f.Kind == "comment" {
			sawComment = true
		}
		if f.StartLine != f.EndLine {
			sawFuncBody = true
		}
	}
	if !sawComment {
		t.Error("no comment folding range found, want one for the doc comment above Point")
	}
	if !sawFuncBody {
		t.Error("no multi-line folding range found, want at least main()'s own body")
	}
}
