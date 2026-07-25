package lsp

import (
	"path/filepath"
	"testing"

	"llvm_lang/src/loader"
	"llvm_lang/src/sema"

	"github.com/spf13/afero"
)

// loadProgram builds prog over an afero.MemMapFs (no real filesystem touched
// - see AGENTS.md's afero convention) from a two-package layout: an
// import-less "mathutils" package, and an "app" package importing it.
func loadProgram(t *testing.T, mathutilsSrc, appSrc string) *loader.Program {
	t.Helper()
	fs := afero.NewMemMapFs()
	sep := string(filepath.Separator)
	writeFile := func(path, src string) {
		if err := afero.WriteFile(fs, path, []byte(src), 0o644); err != nil {
			t.Fatalf("writing %s: %v", path, err)
		}
	}
	writeFile(filepath.Join(sep, "prog", "mathutils", "add.llx"), mathutilsSrc)
	writeFile(filepath.Join(sep, "prog", "app", "main.llx"), appSrc)

	prog, err := loader.LoadProgram(fs, filepath.Join(sep, "prog", "app"))
	if err != nil {
		t.Fatalf("LoadProgram: %v", err)
	}
	return prog
}

// TestAnalyzeProgram_Success covers a valid two-package program: every file
// gets back a real Info (hover/definition/semantic-tokens all need one) and
// no diagnostics.
func TestAnalyzeProgram_Success(t *testing.T) {
	prog := loadProgram(t, `
func Add(a int, b int) int {
	return a + b
}
`, `
import "../mathutils"

func main() int {
	return mathutils.Add(2, 3)
}
`)

	out := analyzeProgram(prog, 1)
	if len(out) != 2 {
		t.Fatalf("len(out) = %d, want 2", len(out))
	}
	for path, fa := range out {
		if fa.Info == nil {
			t.Errorf("%s: Info = nil, want a real one", path)
		}
		if fa.Diags == nil || fa.Diags.HasErrors() {
			t.Errorf("%s: has error diagnostics, want none", path)
		}
	}
}

// TestAnalyzeProgram_GenericTemplateBodyGetsToolingInfo is the analyzeProgram
// -level regression test for a real reported bug: a generic declaration's
// own body (never resolved by the real pipeline - only each instantiation
// is) had no Info.Refs entries at all, which made every identifier inside
// it fall back to a plain "variable" semantic-token classification (see
// semantictokens_test.go's own regression test for the visible symptom this
// caused). sema.ResolveTemplatesForTooling (called from analyzeProgram)
// must enrich Info in place so ordinary Info.Refs[n] lookups - the same
// ones hover/completion/semantic-tokens already use - see real data here
// too, even for a generic never instantiated anywhere in this program.
func TestAnalyzeProgram_GenericTemplateBodyGetsToolingInfo(t *testing.T) {
	sep := string(filepath.Separator)
	dir := filepath.Join(sep, "prog")
	path := filepath.Join(dir, "main.llx")
	fs := afero.NewMemMapFs()
	src := `func Sum[T](a T, b T) T {
	return a + b
}
`
	if err := afero.WriteFile(fs, path, []byte(src), 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
	prog, err := loader.LoadProgram(fs, dir)
	if err != nil {
		t.Fatalf("LoadProgram: %v", err)
	}

	out := analyzeProgram(prog, 1)
	fa, ok := out[path]
	if !ok || fa.Info == nil {
		t.Fatalf("%s not found or Info nil in analysis result: %+v", path, out)
	}

	funcDecl := fa.Tree.Children(fa.Tree.Root)[0]
	aIdent := fa.Tree.FindIdentByText(funcDecl, "a")

	sym, ok := fa.Info.Refs[aIdent]
	if !ok || sym == nil {
		t.Fatal("the generic template body's own 'a' identifier has no Refs entry - the tooling pass did not run")
	}
	if sym.Kind != sema.SymParam {
		t.Errorf("'a' resolved to Kind %v, want SymParam", sym.Kind)
	}
}

// TestAnalyzeProgram_ParseError covers a real syntax error - every file
// still gets its own parse diagnostics back, and Info is now still
// populated best-effort (sema tolerates a partially-malformed tree without
// panicking - see frontend.RunProgram's own doc comment) rather than going
// nil for the whole package, so features like completion keep working
// against everything a parse error didn't directly touch.
func TestAnalyzeProgram_ParseError(t *testing.T) {
	prog := loadProgram(t, `
func Add(a int, b int) int {
	return a + b
}
`, `
import "../mathutils"

func main() int {
	return mathutils.Add(2, 3
}
`)

	out := analyzeProgram(prog, 1)
	if len(out) != 2 {
		t.Fatalf("len(out) = %d, want 2", len(out))
	}
	sawError := false
	for path, fa := range out {
		if fa.Info == nil {
			t.Errorf("%s: Info = nil, want populated best-effort even past a parse error", path)
		}
		if fa.Diags != nil && fa.Diags.HasErrors() {
			sawError = true
		}
	}
	if !sawError {
		t.Error("expected an error-severity diagnostic somewhere, got none")
	}
}

// TestAnalyzeProgram_ResolveError covers a real resolve failure (a reference
// to an undeclared name) - Info must still be populated best-effort (see
// TestAnalyzeProgram_ParseError), and every file still gets its own
// diagnostics.
func TestAnalyzeProgram_ResolveError(t *testing.T) {
	prog := loadProgram(t, `
func Add(a int, b int) int {
	return a + b
}
`, `
import "../mathutils"

func main() int {
	return mathutils.DoesNotExist(2, 3)
}
`)

	out := analyzeProgram(prog, 1)
	sawError := false
	for path, fa := range out {
		if fa.Info == nil {
			t.Errorf("%s: Info = nil, want populated best-effort even past a resolve error", path)
		}
		if fa.Diags != nil && fa.Diags.HasErrors() {
			sawError = true
		}
	}
	if !sawError {
		t.Error("expected an error-severity diagnostic somewhere, got none")
	}
}

// TestAnalyzeProgram_DanglingMemberAccessKeepsSiblingInfo is the concrete
// completion-blocking case this fix targets: `f.` with nothing typed after
// it yet (the most common completion trigger position) is itself a parse
// error by construction (parseMemberExpr's own expectIdent call). A sibling
// declaration in the SAME file, untouched by that error, must still get a
// real, usable Info - not just "some file in the package still has Info."
func TestAnalyzeProgram_DanglingMemberAccessKeepsSiblingInfo(t *testing.T) {
	prog := loadProgram(t, `
func Add(a int, b int) int {
	return a + b
}
`, `
import "../mathutils"

struct Foo {
	x int
}

func broken(f Foo) int {
	f.
	return f.x
}

func clean() int {
	return mathutils.Add(1, 2)
}
`)

	out := analyzeProgram(prog, 1)
	var appFile *FileAnalysis
	for path, fa := range out {
		if filepath.Base(path) == "main.llx" {
			appFile = fa
		}
	}
	if appFile == nil {
		t.Fatal("app/main.llx not found in analyzeProgram output")
	}
	if appFile.Info == nil {
		t.Fatal("main.llx: Info = nil, want populated despite the dangling `f.` parse error")
	}
	if !appFile.Diags.HasErrors() {
		t.Error("main.llx: expected the dangling `f.` to still produce an error diagnostic")
	}
}

// TestAnalyzeProgram_CheckError covers a real type-check failure - Info must
// still be populated (Resolve succeeded; only Check found an error), so
// hover/definition keep working against the parts of the file that didn't
// error.
func TestAnalyzeProgram_CheckError(t *testing.T) {
	prog := loadProgram(t, `
func Add(a int, b int) int {
	return a + b
}
`, `
import "../mathutils"

func main() int {
	var a int = "oops"
	return a
}
`)

	out := analyzeProgram(prog, 1)
	sawError := false
	for path, fa := range out {
		if fa.Info == nil {
			t.Errorf("%s: Info = nil, want populated - Resolve succeeded, only Check found an error", path)
		}
		if fa.Diags != nil && fa.Diags.HasErrors() {
			sawError = true
		}
	}
	if !sawError {
		t.Error("expected an error-severity diagnostic somewhere, got none")
	}
}
