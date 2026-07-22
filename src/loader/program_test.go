package loader

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/afero"
)

// join builds an absolute (real-OS-separator) path the same way
// loader_test.go's existing tests do, so program_test.go's fixtures follow
// the exact same convention.
func join(elem ...string) string {
	return filepath.Join(append([]string{string(filepath.Separator)}, elem...)...)
}

// TestLoadProgram_RelativePathResolution covers import-path resolution
// relative to *each importing file's own directory*, not the entry
// package's directory: root/main.llx imports "./sub/util" (resolved against
// root/), and root/sub/util/pkg.llx itself imports "../../other" (resolved
// against root/sub/util/, landing back at root/other - two directories up
// from sub/util, not from root).
func TestLoadProgram_RelativePathResolution(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeFiles(t, fs, map[string]string{
		join("root", "main.llx"):               `import "./sub/util"`,
		join("root", "sub", "util", "pkg.llx"): `import "../../other"`,
		join("root", "other", "pkg.llx"):       `func Noop() {}`,
	})

	prog, err := LoadProgram(fs, join("root"))
	if err != nil {
		t.Fatalf("LoadProgram: %v", err)
	}

	if prog.Entry.Dir != join("root") {
		t.Errorf("entry dir = %q, want %q", prog.Entry.Dir, join("root"))
	}
	if len(prog.Entry.Files) != 1 || len(prog.Entry.Files[0].Imports) != 1 {
		t.Fatalf("entry package files/imports = %+v, want exactly one file with one import", prog.Entry.Files)
	}

	util := prog.Entry.Files[0].Imports[0].Package
	if util.Dir != join("root", "sub", "util") {
		t.Errorf("util package dir = %q, want %q", util.Dir, join("root", "sub", "util"))
	}
	if util.Name != "util" {
		t.Errorf("util package name = %q, want %q", util.Name, "util")
	}

	if len(util.Files) != 1 || len(util.Files[0].Imports) != 1 {
		t.Fatalf("util package files/imports = %+v, want exactly one file with one import", util.Files)
	}
	other := util.Files[0].Imports[0].Package
	if other.Dir != join("root", "other") {
		t.Errorf("other package dir = %q, want %q (resolved relative to sub/util, not root)", other.Dir, join("root", "other"))
	}
}

// TestLoadProgram_DiamondDependencyDedup covers two different packages
// (a, b) both importing the same third package (common) - it must be
// loaded exactly once, not once per import edge: the two Import.Package
// values must be the exact same *Package instance, and Program.Order must
// list it only once.
func TestLoadProgram_DiamondDependencyDedup(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeFiles(t, fs, map[string]string{
		join("root", "main.llx"):          "import \"./a\"\nimport \"./b\"",
		join("root", "a", "pkg.llx"):      `import "../common"`,
		join("root", "b", "pkg.llx"):      `import "../common"`,
		join("root", "common", "pkg.llx"): `func Noop() {}`,
	})

	prog, err := LoadProgram(fs, join("root"))
	if err != nil {
		t.Fatalf("LoadProgram: %v", err)
	}

	entryFile := prog.Entry.Files[0]
	if len(entryFile.Imports) != 2 {
		t.Fatalf("entry file imports = %+v, want 2 (a and b)", entryFile.Imports)
	}
	aPkg := entryFile.Imports[0].Package
	bPkg := entryFile.Imports[1].Package

	commonFromA := aPkg.Files[0].Imports[0].Package
	commonFromB := bPkg.Files[0].Imports[0].Package
	if commonFromA != commonFromB {
		t.Errorf("common package loaded twice: a's common = %p, b's common = %p, want the identical instance", commonFromA, commonFromB)
	}

	commonCount := 0
	for _, pkg := range prog.Order {
		if pkg.Name == "common" {
			commonCount++
		}
	}
	if commonCount != 1 {
		t.Errorf("Program.Order lists %d packages named \"common\", want exactly 1 (deduped by directory)", commonCount)
	}

	// Dependency order: common must precede both a and b, which must
	// precede the entry package itself.
	idx := make(map[string]int, len(prog.Order))
	for i, pkg := range prog.Order {
		idx[pkg.Dir] = i
	}
	if idx[commonFromA.Dir] >= idx[aPkg.Dir] {
		t.Errorf("common (%d) must be ordered before a (%d)", idx[commonFromA.Dir], idx[aPkg.Dir])
	}
	if idx[aPkg.Dir] >= idx[prog.Entry.Dir] {
		t.Errorf("a (%d) must be ordered before the entry package (%d)", idx[aPkg.Dir], idx[prog.Entry.Dir])
	}
}

// TestLoadProgram_ImportCycleRejected covers a real cycle (a imports b, b
// imports a, transitively back to a) - must be rejected with a clear error
// naming the cycle, not an infinite loop or a stack overflow.
func TestLoadProgram_ImportCycleRejected(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeFiles(t, fs, map[string]string{
		join("root", "a", "pkg.llx"): `import "../b"`,
		join("root", "b", "pkg.llx"): `import "../a"`,
	})

	_, err := LoadProgram(fs, join("root", "a"))
	if err == nil {
		t.Fatal("LoadProgram with an import cycle succeeded, want an error")
	}
	if !strings.Contains(err.Error(), "import cycle") {
		t.Errorf("error = %q, want it to mention \"import cycle\"", err.Error())
	}
	if !strings.Contains(err.Error(), "a") || !strings.Contains(err.Error(), "b") {
		t.Errorf("error = %q, want it to name both packages in the cycle", err.Error())
	}
}

// TestLoadProgram_SinglePackageNoImports covers the plain (pre-imports)
// case still working identically through LoadProgram: a package with no
// import declarations at all resolves to exactly one Package with no
// dependencies.
func TestLoadProgram_SinglePackageNoImports(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeFiles(t, fs, map[string]string{
		join("root", "main.llx"): `func main() {}`,
	})

	prog, err := LoadProgram(fs, join("root"))
	if err != nil {
		t.Fatalf("LoadProgram: %v", err)
	}
	if len(prog.Order) != 1 {
		t.Fatalf("Program.Order = %+v, want exactly 1 package", prog.Order)
	}
	if len(prog.Entry.Files[0].Imports) != 0 {
		t.Errorf("entry file imports = %+v, want none", prog.Entry.Files[0].Imports)
	}
}
