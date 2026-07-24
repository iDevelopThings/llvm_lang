package frontend

import (
	"path/filepath"
	"testing"

	"llvm_lang/src/loader"

	"github.com/spf13/afero"
)

// loadProgram builds prog over an afero.MemMapFs (no real filesystem touched
// - see AGENTS.md's afero convention) from a two-package layout: an
// import-less "mathutils" package, and an "app" package importing it -
// mirroring src/compiler's own TestCompileProgram_Success fixture, since
// RunProgram drives the identical loader.Program shape CompileProgram does.
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

// TestRunProgram_Success covers a valid two-package program (identical
// fixture to src/compiler's own TestCompileProgram_Success, since both
// exercise the same RunProgram this package now shares with it): no errors,
// Infos/TreePackage populated for every tree.
func TestRunProgram_Success(t *testing.T) {
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

	res := RunProgram(prog)
	if res.HasErrors {
		t.Fatalf("HasErrors = true, want false; diags = %v", dumpDiags(res))
	}
	if len(res.Trees) != 2 {
		t.Errorf("len(Trees) = %d, want 2", len(res.Trees))
	}
	for _, tree := range res.Trees {
		if res.Infos[tree] == nil {
			t.Errorf("tree %q has no Info, want a real one", tree.File.Name)
		}
		if b := res.Diags[tree]; b == nil || b.Len() != 0 {
			t.Errorf("tree %q has diagnostics, want none: %v", tree.File.Name, dumpDiags(res))
		}
	}
	if res.TreePackage == nil {
		t.Error("TreePackage = nil, want populated (cross-package export enforcement needs it)")
	}
}

// TestRunProgram_ParseError covers a real syntax error in one package's own
// file - RunProgram still drives Resolve/Check to completion against
// whatever the parser recovered (sema tolerates a partially-malformed tree
// without panicking - see RunProgram's own doc comment), so src/lsp can
// still get usable Info for the parts of the program a parse error didn't
// touch. HasErrors still reports true.
func TestRunProgram_ParseError(t *testing.T) {
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

	res := RunProgram(prog)
	if !res.HasErrors {
		t.Fatal("HasErrors = false, want true on a parse error")
	}
	if res.Infos == nil {
		t.Error("Infos = nil, want populated best-effort even past a parse error")
	}
	if !anyDiagHasErrors(res) {
		t.Errorf("expected an error-severity diagnostic somewhere, got: %v", dumpDiags(res))
	}
}

// TestRunProgram_ResolveError covers a real sema.ResolveProgram failure - a
// reference to an undeclared name - which must still drive CheckProgram to
// completion and populate Infos (ResolveProgram never returns a nil infos
// map, error or not - see its own doc comment).
func TestRunProgram_ResolveError(t *testing.T) {
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

	res := RunProgram(prog)
	if !res.HasErrors {
		t.Fatal("HasErrors = false, want true on a resolve error")
	}
	if res.Infos == nil {
		t.Error("Infos = nil, want populated best-effort even past a resolve error")
	}
	if !anyDiagHasErrors(res) {
		t.Errorf("expected an error-severity diagnostic, got: %v", dumpDiags(res))
	}
}

// TestRunProgram_CheckError covers a real sema.CheckProgram (type-check)
// failure - assigning a string literal to an int-typed var - which must
// still populate Infos (Resolve itself succeeded; only Check found an
// error), matching src/lsp's own need to keep hover/definition working
// against the parts of a file that didn't error.
func TestRunProgram_CheckError(t *testing.T) {
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

	res := RunProgram(prog)
	if !res.HasErrors {
		t.Fatal("HasErrors = false, want true on a check error")
	}
	if res.Infos == nil {
		t.Error("Infos = nil, want populated - Resolve succeeded, only Check found an error")
	}
	if !anyDiagHasErrors(res) {
		t.Errorf("expected an error-severity diagnostic, got: %v", dumpDiags(res))
	}
}

func anyDiagHasErrors(res *Result) bool {
	for _, b := range res.Diags {
		if b != nil && b.HasErrors() {
			return true
		}
	}
	return false
}

func dumpDiags(res *Result) []string {
	var out []string
	for tree, b := range res.Diags {
		if b == nil {
			continue
		}
		for d := range b.Seq() {
			out = append(out, tree.File.Name+": "+d.Msg)
		}
	}
	return out
}
