package lsp

import (
	"path/filepath"
	"testing"

	"llvm_lang/src/loader"

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

// TestAnalyzeProgram_ParseError covers a real syntax error - every file
// still gets its own parse diagnostics back, but Info stays nil throughout
// (a structurally broken tree is never safe to hand to Resolve/Check).
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
		if fa.Info != nil {
			t.Errorf("%s: Info != nil, want nil on a parse error", path)
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
// to an undeclared name) - Info must stay nil (mirroring sema.CheckProgram's
// own "assumes Resolve succeeded" precondition), but every file still gets
// its own diagnostics.
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
		if fa.Info != nil {
			t.Errorf("%s: Info != nil, want nil on a resolve error", path)
		}
		if fa.Diags != nil && fa.Diags.HasErrors() {
			sawError = true
		}
	}
	if !sawError {
		t.Error("expected an error-severity diagnostic somewhere, got none")
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
