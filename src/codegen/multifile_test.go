package codegen

import (
	"testing"

	"llvm_lang/src/ast"
	"llvm_lang/src/lexer"
	"llvm_lang/src/parser"
	"llvm_lang/src/sema"

	"tinygo.org/x/go-llvm"
)

// compilePackageSrc is compileSrc generalized to multiple files, compiled
// together as one package (sema.ResolvePackage/CheckPackage ->
// GeneratePackage) - see LANGUAGE.md's "Multi-file packages" section. Fails
// the test if any stage reports a diagnostic in any file.
func compilePackageSrc(t *testing.T, files [][2]string) *Module {
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

	infos, rdiags := sema.ResolvePackage(trees)
	for i, tree := range trees {
		if b := rdiags[tree]; b.HasErrors() {
			t.Fatalf("unexpected resolve errors in %s: %v", files[i][0], b.All())
		}
	}
	cdiags := sema.CheckPackage(trees, infos)
	for i, tree := range trees {
		if b := cdiags[tree]; b.HasErrors() {
			t.Fatalf("unexpected check errors in %s: %v", files[i][0], b.All())
		}
	}

	mod, gdiags := GeneratePackage(trees, infos, "test")
	for i, tree := range trees {
		if b := gdiags[tree]; b.HasErrors() {
			t.Fatalf("unexpected codegen errors in %s: %v", files[i][0], b.All())
		}
	}
	if err := llvm.VerifyModule(mod.LLVM, llvm.ReturnStatusAction); err != nil {
		t.Fatalf("module verification failed: %v\n%s", err, mod.LLVM.String())
	}
	return mod
}

// compilePackageAndJIT is compilePackageSrc handed to a live ExecutionEngine
// - see compileAndJIT's own doc comment for the disposal ordering this
// mirrors exactly (an engine takes ownership of its module; a later
// mod.Dispose() would double-free it).
func compilePackageAndJIT(t *testing.T, files [][2]string) *jitModule {
	t.Helper()
	mod := compilePackageSrc(t, files)
	initJIT()

	engine, err := llvm.NewExecutionEngine(mod.LLVM)
	if err != nil {
		t.Fatalf("NewExecutionEngine: %v", err)
	}
	t.Cleanup(func() {
		engine.Dispose()
		mod.Ctx.Dispose()
	})
	return &jitModule{
		mod:    mod,
		engine: engine,
	}
}

// TestMultiFile_FunctionCallAcrossFiles JIT-executes a package split across
// two files - main (in "main.llx") calls a function declared in "helper.llx"
// - and asserts on the real, JIT-computed result, the same rigor this
// package's existing single-file tests already apply.
func TestMultiFile_FunctionCallAcrossFiles(t *testing.T) {
	jm := compilePackageAndJIT(t, [][2]string{
		{"main.llx", `
func main() int {
	return double(21)
}
`},
		{"helper.llx", `
func double(x int) int {
	return x * 2
}
`},
	})
	if got := jm.runInt32(t, "main"); got != 42 {
		t.Errorf("main() = %d, want 42", got)
	}
}

// TestMultiFile_StructAndMethodAcrossFiles covers a struct declared in one
// file, its method declared in a second, and both used from a third file's
// main - one shared LLVM Module, not per-file modules (see CODEGEN.md's
// multi-file section).
func TestMultiFile_StructAndMethodAcrossFiles(t *testing.T) {
	jm := compilePackageAndJIT(t, [][2]string{
		{"point.llx", `
struct Point {
	x int
	y int
}
`},
		{"methods.llx", `
func (Point) move(dx int, dy int) {
	this.x = this.x + dx
	this.y = this.y + dy
}
`},
		{"main.llx", `
func main() int {
	p := Point{1, 2}
	p.move(10, 20)
	return p.x + p.y
}
`},
	})
	if got := jm.runInt32(t, "main"); got != 33 {
		t.Errorf("main() = %d, want 33", got)
	}
}

// TestMultiFile_GlobalVarAcrossFiles covers a package-level `var` declared
// in one file and used from main in another.
func TestMultiFile_GlobalVarAcrossFiles(t *testing.T) {
	jm := compilePackageAndJIT(t, [][2]string{
		{"globals.llx", `
var base int = 100
`},
		{"main.llx", `
func main() int {
	return base + 1
}
`},
	})
	if got := jm.runInt32(t, "main"); got != 101 {
		t.Errorf("main() = %d, want 101", got)
	}
}

// TestMultiFile_DeclarationOrderDoesNotMatter proves the exact same package
// JIT-executes to the same result regardless of which order its files are
// compiled in - main.llx (the caller) sorts *before* helper.llx (the
// callee) alphabetically and is passed first here, demonstrating this isn't
// silently working only because of file-sort order (see
// sema/multifile_test.go's identical point one layer down).
func TestMultiFile_DeclarationOrderDoesNotMatter(t *testing.T) {
	jm := compilePackageAndJIT(t, [][2]string{
		{"main.llx", `
func main() int {
	return triple(4)
}
`},
		{"zzz_helper.llx", `
func triple(x int) int {
	return x * 3
}
`},
	})
	if got := jm.runInt32(t, "main"); got != 12 {
		t.Errorf("main() = %d, want 12", got)
	}
}
