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

	writes, reads := 0, 0
	for _, h := range highlights {
		switch *h.Kind {
		case protocol.DocumentHighlightKindWrite:
			writes++
		case protocol.DocumentHighlightKindRead:
			reads++
		}
	}
	if writes != 2 || reads != 2 {
		t.Errorf("writes=%d reads=%d, want writes=2 (decl + reassignment target) reads=2", writes, reads)
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
