package sema

import (
	"testing"

	"llvm_lang/src/ast"
	"llvm_lang/src/lexer"
	"llvm_lang/src/parser"
)

// checkPackageSrcs parses every (name, src) pair in files and resolves/
// type-checks them together as one package (ResolvePackage/CheckPackage),
// failing the test if parsing itself produced a diagnostic in any file (a
// parse error means the test source is broken, not the multi-file plumbing
// under test) or if resolving/checking reported any error. files is given in
// the exact order it should be passed to ResolvePackage/CheckPackage - tests
// that care about order-independence pass a deliberately "wrong" order (the
// file that *uses* a name before the file that *declares* it) and still
// expect success.
func checkPackageSrcs(t *testing.T, files [][2]string) ([]*ast.Tree, map[*ast.Tree]*Info) {
	t.Helper()
	trees := make([]*ast.Tree, len(files))
	for i, f := range files {
		name, src := f[0], f[1]
		tree, pdiags := parser.ParseFile(lexer.NewFile(name, src))
		if pdiags.HasErrors() {
			t.Fatalf("unexpected parse errors in %s: %v", name, pdiags.All())
		}
		trees[i] = tree
	}

	infos, rdiags := ResolvePackage(trees)
	for i, tree := range trees {
		if b := rdiags[tree]; b.HasErrors() {
			t.Fatalf("unexpected resolve errors in %s: %v", files[i][0], b.All())
		}
	}

	cdiags := CheckPackage(trees, infos)
	for i, tree := range trees {
		if b := cdiags[tree]; b.HasErrors() {
			t.Fatalf("unexpected check errors in %s: %v", files[i][0], b.All())
		}
	}
	return trees, infos
}

// TestMultiFile_FunctionCallAcrossFiles covers a function declared in one
// file called from another - the core multi-file guarantee (see
// LANGUAGE.md's "Multi-file packages" section). "a.llx" (the caller) sorts
// alphabetically *before* "b.llx" (the callee) and is passed to
// ResolvePackage/CheckPackage in that same order, proving this isn't working
// by accident of file-processing order - see TestMultiFile_DeclarationOrder
// AcrossFilesDoesNotMatter below for the same point made explicitly.
func TestMultiFile_FunctionCallAcrossFiles(t *testing.T) {
	checkPackageSrcs(t, [][2]string{
		{"a.llx", "func a() int { return b() + 1 }\n"},
		{"b.llx", "func b() int { return 41 }\n"},
	})
}

// TestMultiFile_StructAcrossFiles covers a struct declared in one file used
// (as a variable's type, and via a composite literal) in another.
func TestMultiFile_StructAcrossFiles(t *testing.T) {
	checkPackageSrcs(t, [][2]string{
		{"user.llx", "func makePoint() Point { return Point{1, 2} }\n"},
		{"point.llx", "struct Point {\n\tx int\n\ty int\n}\n"},
	})
}

// TestMultiFile_MethodAcrossFiles covers a method declared (with its
// receiver struct declared in a *third* file, for good measure) being called
// from yet another file.
func TestMultiFile_MethodAcrossFiles(t *testing.T) {
	checkPackageSrcs(t, [][2]string{
		{"caller.llx", "func use() int {\n\tp := Point{1, 2}\n\tp.move(3, 4)\n\treturn p.x\n}\n"},
		{"methods.llx", "func (Point) move(dx int, dy int) {\n\tthis.x = this.x + dx\n\tthis.y = this.y + dy\n}\n"},
		{"point.llx", "struct Point {\n\tx int\n\ty int\n}\n"},
	})
}

// TestMultiFile_GlobalVarAcrossFiles covers a package-level `var` declared
// in one file, read from another.
func TestMultiFile_GlobalVarAcrossFiles(t *testing.T) {
	checkPackageSrcs(t, [][2]string{
		{"a.llx", "func useGlobal() int { return total + 1 }\n"},
		{"b.llx", "var total int = 41\n"},
	})
}

// TestMultiFile_DeclarationOrderAcrossFilesDoesNotMatter asserts the exact
// same package resolves/type-checks identically regardless of which order
// its files are handed to ResolvePackage/CheckPackage in - proving cross-
// file forward references aren't secretly relying on processing order (see
// LANGUAGE.md's "Multi-file packages" section: declaration order must never
// matter, matching the existing same-file guarantee one level up).
func TestMultiFile_DeclarationOrderAcrossFilesDoesNotMatter(t *testing.T) {
	files := [][2]string{
		{"a.llx", "func a() int { return b() + 1 }\n"},
		{"b.llx", "func b() int { return 41 }\n"},
	}
	reversed := [][2]string{files[1], files[0]}

	checkPackageSrcs(t, files)
	checkPackageSrcs(t, reversed)
}

// TestMultiFile_RedeclarationAcrossFilesIsAnError covers the same top-level
// name declared twice in two different files - this must still be reported
// as a redeclaration, exactly like two declarations of the same name within
// one file already are.
func TestMultiFile_RedeclarationAcrossFilesIsAnError(t *testing.T) {
	files := [][2]string{
		{"a.llx", "func dup() int { return 1 }\n"},
		{"b.llx", "func dup() int { return 2 }\n"},
	}
	trees := make([]*ast.Tree, len(files))
	for i, f := range files {
		tree, pdiags := parser.ParseFile(lexer.NewFile(f[0], f[1]))
		if pdiags.HasErrors() {
			t.Fatalf("unexpected parse errors in %s: %v", f[0], pdiags.All())
		}
		trees[i] = tree
	}

	_, rdiags := ResolvePackage(trees)
	total := 0
	for _, tree := range trees {
		total += rdiags[tree].ErrorCount()
	}
	if total != 1 {
		t.Fatalf("total resolve ErrorCount across files = %d, want 1", total)
	}
}
