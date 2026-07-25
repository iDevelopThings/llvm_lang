// tests{} coverage across every major LSP capability (see src/lsp/doc.go's
// own standing convention). safeLoadProgram (workspace.go) always loads with
// TestMode: true, so a developer editing a tests{} block gets full IDE
// support - its contents are spliced into ordinary top-level declarations
// before sema ever runs (see LANGUAGE.md's "tests{}" section and
// DECISIONS.md), indistinguishable from code written outside any block.
package lsp

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"llvm_lang/src/lexer"
	"llvm_lang/src/loader"

	"github.com/spf13/afero"
)

// testsBlockFixture exercises a tests{} block sharing a file with ordinary
// code: a real `import "std:test"` (so hover/completion/references inside
// the block behave exactly like ordinary code, not a stubbed-out shape) and
// a local reassigned inside the test func, for DocumentHighlight's own
// Read/Write split.
const testsBlockFixture = `func add(a int, b int) int {
	return a + b
}

tests {
	import "std:test"

	func TestAdd(t *test.Runner) {
		total := add(1, 1)
		total = total + 1
		print(total)
	}
}

func main() int {
	return add(1, 1)
}
`

// testsBlockWorkspace is singleFileWorkspace's own counterpart with a fake
// std root wired in (see cmd/llvmc/test_test.go's withFakeStdFS for the
// identical pattern) - needed since this fixture's own tests{} block
// contains a real `import "std:test"`.
func testsBlockWorkspace(t *testing.T, src string) (w *Workspace, path string) {
	t.Helper()
	fs := afero.NewMemMapFs()
	sep := string(filepath.Separator)
	dir := filepath.Join(sep, "prog")
	path = filepath.Join(dir, "main.llx")
	if err := afero.WriteFile(fs, path, []byte(src), 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}

	stdFS := afero.NewMemMapFs()
	runnerSrc := "struct Runner {\n\tname string\n}\n"
	if err := afero.WriteFile(stdFS, filepath.Join("test", "test.llx"), []byte(runnerSrc), 0o644); err != nil {
		t.Fatalf("writing fake std/test: %v", err)
	}

	prog, err := loader.LoadProgramWithOptions(fs, dir, loader.Options{TestMode: true, StdFS: stdFS})
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

// TestTestsBlock_SemanticTokens_TestsKeyword covers the one real risk this
// feature's mechanism creates for semantic tokens: in test mode, the `tests`
// keyword's own token is never captured as any node's Tok at all (no
// TestBlockDecl is ever built - see parser.parseTestsBlock), unlike every
// other keyword collectNodeTokens normally classifies from a node. It must
// still be classified as a keyword via collectLexicalExtras' own
// not-yet-covered re-lex pass - the same fallback that already rescues
// else/import/map.
func TestTestsBlock_SemanticTokens_TestsKeyword(t *testing.T) {
	w, path := testsBlockWorkspace(t, testsBlockFixture)

	toks := w.SemanticTokens(path)
	if toks == nil {
		t.Fatal("SemanticTokens returned nil")
	}

	fa, _ := w.Analysis(path)
	lines := strings.Split(fa.Tree.File.Src, "\n")
	kindAt := func(line, char, length int) string {
		return lines[line][char : char+length]
	}

	line, char := 0, 0
	found := false
	for i := 0; i+4 < len(toks.Data); i += 5 {
		deltaLine, deltaChar, length, typeIdx := int(toks.Data[i]), int(toks.Data[i+1]), int(toks.Data[i+2]), int(toks.Data[i+3])
		if deltaLine == 0 {
			char += deltaChar
		} else {
			line += deltaLine
			char = deltaChar
		}
		if typeIdx == semTokKeyword && kindAt(line, char, length) == "tests" {
			found = true
		}
	}
	if !found {
		t.Error("no Keyword token found for \"tests\"")
	}
}

// TestTestsBlock_DocumentSymbols_ShowsSplicedFunc covers the actual splice:
// TestAdd must appear alongside add/main as an ordinary top-level symbol,
// not buried inside (or entirely absent because of) an invisible wrapper.
func TestTestsBlock_DocumentSymbols_ShowsSplicedFunc(t *testing.T) {
	w, path := testsBlockWorkspace(t, testsBlockFixture)

	syms := w.DocumentSymbols(path)
	var names []string
	for _, s := range syms {
		names = append(names, s.Name)
	}
	for _, want := range []string{"add", "TestAdd", "main"} {
		if !slices.Contains(names, want) {
			t.Errorf("DocumentSymbols names %v missing %q", names, want)
		}
	}
}

// TestTestsBlock_Hover_FuncDeclAndCallSite covers hover both on TestAdd's own
// declaration and on its call to the outer add - ordinary cross-scope
// resolution reaching into spliced content.
func TestTestsBlock_Hover_FuncDeclAndCallSite(t *testing.T) {
	w, path := testsBlockWorkspace(t, testsBlockFixture)
	fa, _ := w.Analysis(path)

	declOffset := strings.Index(fa.Tree.File.Src, "TestAdd")
	declPos := byteOffsetToPosition(fa.Tree.File, lexer.Pos(declOffset))
	if hover := w.Hover(path, declPos); hover == nil {
		t.Error("Hover(TestAdd decl) returned nil")
	}

	callOffset := strings.Index(fa.Tree.File.Src, "add(1, 1)")
	callPos := byteOffsetToPosition(fa.Tree.File, lexer.Pos(callOffset))
	if hover := w.Hover(path, callPos); hover == nil {
		t.Error("Hover(add call site inside tests{}) returned nil")
	}
}

// TestTestsBlock_Completion_SeesLocalAndOuterFunc covers scope resolution
// inside a spliced FuncDecl's own body: both its own local (total) and the
// outer, sibling add function must be visible.
func TestTestsBlock_Completion_SeesLocalAndOuterFunc(t *testing.T) {
	w, path := testsBlockWorkspace(t, testsBlockFixture)
	fa, _ := w.Analysis(path)

	offset := strings.Index(fa.Tree.File.Src, "print(total)")
	pos := byteOffsetToPosition(fa.Tree.File, lexer.Pos(offset))

	labels := completionLabels(w.Completion(path, pos))
	for _, want := range []string{"total", "add"} {
		if !slices.Contains(labels, want) {
			t.Errorf("completion labels inside TestAdd's own body = %v, missing %q", labels, want)
		}
	}
}

// TestTestsBlock_References_OuterFuncCalledFromInsideBlock covers
// References reaching from an outer declaration into a spliced call site,
// and back: add is declared once and called twice (once in main, once
// inside the tests{} block).
func TestTestsBlock_References_OuterFuncCalledFromInsideBlock(t *testing.T) {
	w, path := testsBlockWorkspace(t, testsBlockFixture)
	fa, _ := w.Analysis(path)

	declPos := byteOffsetToPosition(fa.Tree.File, lexer.Pos(strings.Index(fa.Tree.File.Src, "add(a int")))
	locs := w.References(path, declPos, true)
	if len(locs) != 3 {
		t.Errorf("len(locs) = %d, want 3 (add's own decl + its call inside tests{} + its call in main): %+v", len(locs), locs)
	}
}

// TestTestsBlock_DocumentHighlight_ReassignedLocalInsideBlock covers the
// Read/Write split for a local declared and reassigned entirely inside a
// spliced FuncDecl's own body: declaration + reassignment target are writes,
// the reassignment's own right-hand `total + 1` plus `print(total)` are reads.
func TestTestsBlock_DocumentHighlight_ReassignedLocalInsideBlock(t *testing.T) {
	w, path := testsBlockWorkspace(t, testsBlockFixture)
	fa, _ := w.Analysis(path)

	pos := byteOffsetToPosition(fa.Tree.File, lexer.Pos(strings.Index(fa.Tree.File.Src, "total := add")))
	highlights := w.DocumentHighlight(path, pos)
	if len(highlights) != 4 {
		t.Fatalf("len(highlights) = %d, want 4 (declaration + reassignment target + its own RHS read + print(total) read): %+v", len(highlights), highlights)
	}
	if writes, reads := highlightKinds(highlights); writes != 2 || reads != 2 {
		t.Errorf("writes=%d reads=%d, want writes=2 (declaration + reassignment) reads=2", writes, reads)
	}
}

// TestTestsBlock_FoldingRanges_BodyFolds covers a spliced FuncDecl's own
// multi-line body folding like any other.
func TestTestsBlock_FoldingRanges_BodyFolds(t *testing.T) {
	w, path := testsBlockWorkspace(t, testsBlockFixture)

	folds := w.FoldingRanges(path)
	if len(folds) == 0 {
		t.Fatal("FoldingRanges returned none - a spliced tests{} func body must still fold like any other")
	}
}

// TestTestsBlock_NoDiagnosticsForValidUsage confirms a well-formed tests{}
// block produces zero diagnostics once TestMode splices its `import
// "std:test"` and TestAdd normally - the actual "no leak" proof at the LSP
// layer: this only holds because TestMode resolves the import against the
// fake std root, exactly like a real llvmc-lsp resolves it against the real
// one (see workspace.go's lspStdFS).
func TestTestsBlock_NoDiagnosticsForValidUsage(t *testing.T) {
	w, path := testsBlockWorkspace(t, testsBlockFixture)
	fa, _ := w.Analysis(path)
	if fa.Diags != nil && fa.Diags.HasErrors() {
		t.Errorf("unexpected diagnostics: %v", fa.Diags.All())
	}
}

// TestTestsBlock_NestedBlockRejected_NoCrash is the broken/incomplete-source
// variant every capability above needs to survive, per this project's
// invalid-path-coverage standard: a nested tests{} block is a parse error
// (see parser.parseTestsBlock), and every LSP capability must still respond
// without panicking against the resulting partial tree.
func TestTestsBlock_NestedBlockRejected_NoCrash(t *testing.T) {
	src := `tests {
	tests {
		func TestInner(t *test.Runner) {
		}
	}
}

func main() int {
	return 0
}
`
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("a nested tests{} block panicked: %v", r)
		}
	}()

	w, path := testsBlockWorkspace(t, src)
	fa, _ := w.Analysis(path)
	if fa.Diags == nil || !fa.Diags.HasErrors() {
		t.Error("expected a parse diagnostic for the nested tests{} block")
	}

	pos := byteOffsetToPosition(fa.Tree.File, lexer.Pos(strings.Index(fa.Tree.File.Src, "TestInner")))
	w.Hover(path, pos)
	w.Completion(path, pos)
	w.DocumentSymbols(path)
	w.FoldingRanges(path)
	w.SemanticTokens(path)
	w.DocumentHighlight(path, pos)
	w.References(path, pos, true)
}
