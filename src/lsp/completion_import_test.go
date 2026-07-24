package lsp

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"llvm_lang/src/lexer"
	"llvm_lang/src/loader"

	"github.com/spf13/afero"
	protocol "github.com/tliron/glsp/protocol_3_16"
)

// unimportedWorkspace builds a *Workspace over a real (in-memory) afero.Fs
// with root set, so PackageIndex has something to discover - unlike
// singleFileWorkspace, which never sets fs/root at all.
func unimportedWorkspace(t *testing.T, appSrc, stringsSrc string) (w *Workspace, path string) {
	t.Helper()
	sep := string(filepath.Separator)
	root := filepath.Join(sep, "ws")
	fs := afero.NewMemMapFs()
	path = filepath.Join(root, "app", "main.llx")
	if err := afero.WriteFile(fs, path, []byte(appSrc), 0o644); err != nil {
		t.Fatalf("writing app/main.llx: %v", err)
	}
	if err := afero.WriteFile(fs, filepath.Join(root, "std", "strings", "s.llx"), []byte(stringsSrc), 0o644); err != nil {
		t.Fatalf("writing std/strings/s.llx: %v", err)
	}

	prog, err := loader.LoadProgram(fs, filepath.Join(root, "app"))
	if err != nil {
		t.Fatalf("LoadProgram: %v", err)
	}
	result := analyzeProgram(prog, 1)
	fa, ok := result[path]
	if !ok {
		t.Fatalf("%s not found in analysis result: %v", path, result)
	}

	w = &Workspace{fs: fs, analysis: map[string]*FileAnalysis{path: fa}}
	w.SetRoot(root)
	return w, path
}

const stringsPkgSrc = `func Join(a int, b int) int {
	return a + b
}
func helper() int {
	return 0
}
`

// TestCompletion_UnimportedPackageMember_SkipsHopelesslyBrokenCandidateFile
// covers a real crash risk caught by review: parser.ParseFile returns a nil
// *ast.Tree once a file's own parse bails out after too many errors (see
// parser.Run/maxErrors) - exportedPackageDecls must skip such a file rather
// than call DeclSymbols on a nil tree and crash the whole server on what
// should just be "this one candidate has nothing to offer".
func TestCompletion_UnimportedPackageMember_SkipsHopelesslyBrokenCandidateFile(t *testing.T) {
	sep := string(filepath.Separator)
	root := filepath.Join(sep, "ws")
	fs := afero.NewMemMapFs()
	appSrc := `func f() int {
	strings.
	return 0
}
`
	appPath := filepath.Join(root, "app", "main.llx")
	if err := afero.WriteFile(fs, appPath, []byte(appSrc), 0o644); err != nil {
		t.Fatalf("writing app/main.llx: %v", err)
	}
	brokenSrc := strings.Repeat("$ ", 25)
	if err := afero.WriteFile(fs, filepath.Join(root, "std", "strings", "s.llx"), []byte(brokenSrc), 0o644); err != nil {
		t.Fatalf("writing std/strings/s.llx: %v", err)
	}
	prog, err := loader.LoadProgram(fs, filepath.Join(root, "app"))
	if err != nil {
		t.Fatalf("LoadProgram: %v", err)
	}
	result := analyzeProgram(prog, 1)
	fa, ok := result[appPath]
	if !ok {
		t.Fatalf("%s not found in analysis result: %v", appPath, result)
	}
	w := &Workspace{fs: fs, analysis: map[string]*FileAnalysis{appPath: fa}}
	w.SetRoot(root)

	offset := strings.Index(fa.Tree.File.Src, "strings.\n") + len("strings.")
	pos := byteOffsetToPosition(fa.Tree.File, lexer.Pos(offset))

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Completion panicked on a hopelessly broken candidate file: %v", r)
		}
	}()
	items := w.Completion(appPath, pos)
	if len(items) != 0 {
		t.Errorf("items = %+v, want none (the only candidate file is unparseable)", items)
	}
}

func TestCompletion_UnimportedPackageMember(t *testing.T) {
	w, path := unimportedWorkspace(t, `func f() int {
	strings.
	return 0
}
`, stringsPkgSrc)

	fa, _ := w.Analysis(path)
	offset := strings.Index(fa.Tree.File.Src, "strings.\n") + len("strings.")
	pos := byteOffsetToPosition(fa.Tree.File, lexer.Pos(offset))

	items := w.Completion(path, pos)
	labels := completionLabels(items)
	if got, want := labels, []string{"Join"}; !slices.Equal(got, want) {
		t.Fatalf("completion labels at strings.<cursor> = %v, want %v (only the exported func - helper is unexported)", got, want)
	}

	item := items[0]
	if len(item.AdditionalTextEdits) != 1 {
		t.Fatalf("len(AdditionalTextEdits) = %d, want 1", len(item.AdditionalTextEdits))
	}
	edit := item.AdditionalTextEdits[0]
	wantText := "import \"../std/strings\"\n\n"
	if edit.NewText != wantText {
		t.Errorf("AdditionalTextEdits[0].NewText = %q, want %q", edit.NewText, wantText)
	}
	if edit.Range.Start != (protocol.Position{Line: 0, Character: 0}) {
		t.Errorf("AdditionalTextEdits[0].Range.Start = %+v, want {0,0} (no existing imports)", edit.Range.Start)
	}
}

func TestCompletion_UnimportedPackageMember_InsertsAfterExistingImports(t *testing.T) {
	sep := string(filepath.Separator)
	root := filepath.Join(sep, "ws")
	fs := afero.NewMemMapFs()
	appSrc := `import "../other"

func f() int {
	strings.
	return 0
}
`
	appPath := filepath.Join(root, "app", "main.llx")
	if err := afero.WriteFile(fs, appPath, []byte(appSrc), 0o644); err != nil {
		t.Fatalf("writing app/main.llx: %v", err)
	}
	if err := afero.WriteFile(fs, filepath.Join(root, "other", "o.llx"), []byte("func Noop() {}\n"), 0o644); err != nil {
		t.Fatalf("writing other/o.llx: %v", err)
	}
	if err := afero.WriteFile(fs, filepath.Join(root, "std", "strings", "s.llx"), []byte(stringsPkgSrc), 0o644); err != nil {
		t.Fatalf("writing std/strings/s.llx: %v", err)
	}
	prog, err := loader.LoadProgram(fs, filepath.Join(root, "app"))
	if err != nil {
		t.Fatalf("LoadProgram: %v", err)
	}
	result := analyzeProgram(prog, 1)
	fa, ok := result[appPath]
	if !ok {
		t.Fatalf("%s not found in analysis result: %v", appPath, result)
	}
	w := &Workspace{fs: fs, analysis: map[string]*FileAnalysis{appPath: fa}}
	w.SetRoot(root)
	path := appPath

	offset := strings.Index(fa.Tree.File.Src, "strings.\n") + len("strings.")
	pos := byteOffsetToPosition(fa.Tree.File, lexer.Pos(offset))

	items := w.Completion(path, pos)
	if len(items) != 1 || items[0].Label != "Join" {
		t.Fatalf("items = %+v, want exactly one Join completion", items)
	}
	edit := items[0].AdditionalTextEdits[0]
	if !strings.HasPrefix(edit.NewText, "\nimport \"../std/strings\"") {
		t.Errorf("NewText = %q, want it to start with a newline then the import line", edit.NewText)
	}
	if edit.Range.Start.Line != 0 {
		t.Errorf("edit inserted at line %d, want line 0 (right after the sole existing import)", edit.Range.Start.Line)
	}
}

func TestCompletion_UnimportedPackageName_OfferedAtIdentifierPosition(t *testing.T) {
	w, path := unimportedWorkspace(t, `func f() int {
	x := 1
	return x
}
`, stringsPkgSrc)

	fa, _ := w.Analysis(path)
	offset := strings.Index(fa.Tree.File.Src, "return x")
	pos := byteOffsetToPosition(fa.Tree.File, lexer.Pos(offset))

	items := w.Completion(path, pos)
	var found *protocol.CompletionItem
	for i := range items {
		if items[i].Label == "strings" {
			found = &items[i]
		}
	}
	if found == nil {
		t.Fatalf("completion items %v missing the not-yet-imported \"strings\" package candidate", completionLabels(items))
	}
	if *found.Kind != protocol.CompletionItemKindModule {
		t.Errorf("strings candidate Kind = %v, want CompletionItemKindModule", *found.Kind)
	}
	if len(found.AdditionalTextEdits) != 1 {
		t.Fatalf("len(AdditionalTextEdits) = %d, want 1", len(found.AdditionalTextEdits))
	}
}

func TestCompletion_UnimportedPackageName_NotOfferedOnceAlreadyImported(t *testing.T) {
	w, path := unimportedWorkspace(t, `import "../std/strings"

func f() int {
	x := 1
	return x
}
`, stringsPkgSrc)

	fa, _ := w.Analysis(path)
	offset := strings.Index(fa.Tree.File.Src, "return x")
	pos := byteOffsetToPosition(fa.Tree.File, lexer.Pos(offset))

	items := w.Completion(path, pos)
	for _, it := range items {
		if it.Label == "strings" && len(it.AdditionalTextEdits) != 0 {
			t.Errorf("strings appeared with an import-edit even though it's already imported: %+v", it)
		}
	}
}
