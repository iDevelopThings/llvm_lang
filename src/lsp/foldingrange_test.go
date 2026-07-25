package lsp

import (
	"path/filepath"
	"testing"

	"llvm_lang/src/loader"

	"github.com/spf13/afero"
)

// singleFileWorkspace builds a *Workspace pre-seeded with a single,
// import-less file's analysis - no afero/real filesystem exposed to the
// caller (loader.LoadProgram itself still goes through afero.MemMapFs, per
// AGENTS.md's own convention, but that's an implementation detail here).
func singleFileWorkspace(t *testing.T, src string) (w *Workspace, path string) {
	t.Helper()
	fs := afero.NewMemMapFs()
	sep := string(filepath.Separator)
	dir := filepath.Join(sep, "prog")
	path = filepath.Join(dir, "main.llx")
	if err := afero.WriteFile(fs, path, []byte(src), 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
	// TestMode: true mirrors safeLoadProgram's own real production behavior
	// (workspace.go) - a tests{} block's contents must be visible to every
	// LSP capability exactly like ordinary code (see LANGUAGE.md's "tests{}"
	// section); no-op for a fixture with no such block.
	prog, err := loader.LoadProgramWithOptions(fs, dir, loader.Options{TestMode: true})
	if err != nil {
		t.Fatalf("LoadProgramWithOptions: %v", err)
	}
	result := analyzeProgram(prog, 1)
	fa, ok := result[path]
	if !ok {
		t.Fatalf("%s not found in analysis result: %v", path, result)
	}
	return &Workspace{analysis: map[string]*FileAnalysis{path: fa}}, path
}

// TestFoldingRanges_AdjacentTopLevelFuncs is the same regression scenario as
// src/ast/foldrange_test.go's identically-named test, but through the full
// Workspace.FoldingRanges -> protocol.FoldingRange path - the actual
// response an editor receives - to isolate whether a reported bug lives in
// the ast-level FoldRanges data itself or in this package's own protocol
// mapping.
func TestFoldingRanges_AdjacentTopLevelFuncs(t *testing.T) {
	src := `func Add(a int, b int) int {
	return a + b
}

func double(x int) int {
	return x * 2
}
`
	w, path := singleFileWorkspace(t, src)
	folds := w.FoldingRanges(path)

	wantLines := []struct{ start, end int }{
		{0, 2},
		{4, 6},
	}
	if len(folds) != len(wantLines) {
		t.Fatalf("len(folds) = %d, want %d - folds: %+v", len(folds), len(wantLines), folds)
	}
	for i, want := range wantLines {
		if int(folds[i].StartLine) != want.start || int(folds[i].EndLine) != want.end {
			t.Errorf("folds[%d] = [%d,%d], want [%d,%d]", i, folds[i].StartLine, folds[i].EndLine, want.start, want.end)
		}
	}
}
