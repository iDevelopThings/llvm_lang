package codegen

import (
	"syscall"
	"testing"

	"llvm_lang/src/ast"
	"llvm_lang/src/lexer"
	"llvm_lang/src/parser"
	"llvm_lang/src/sema"

	"tinygo.org/x/go-llvm"
)

// packageFile is one (name, src) pair belonging to a particular
// sema.PackageUnit - see compileProgramSrc.
type packageFile struct {
	name string
	src  string
}

// programPackage describes one package's input to compileProgramSrc: its
// own files, plus - for each file, by index into files - the imports it
// declares (localName, targetKey, where targetKey matches some other
// programPackage's own key in the same compileProgramSrc call).
type programPackage struct {
	key   string
	files []packageFile
	// imports maps a file index (into files) to that file's own import
	// bindings.
	imports map[int][]sema.FileImport
}

// compileProgramSrc drives a whole multi-package program (see LANGUAGE.md's
// "Imports" section) through the full pipeline - parse every package's every
// file, sema.ResolveProgram -> sema.CheckProgram, then codegen.GeneratePackage
// across every package's trees flattened into one shared Module (see
// CODEGEN.md's "Multi-file packages" section for why one shared Module needs
// no package-boundary awareness, extended one level up unchanged) - failing
// the test if any stage reports a diagnostic anywhere.
func compileProgramSrc(t *testing.T, pkgs []programPackage) *Module {
	t.Helper()

	units := make([]*sema.PackageUnit, len(pkgs))
	var allTrees []*ast.Tree

	for i, pkg := range pkgs {
		trees := make([]*ast.Tree, len(pkg.files))
		for j, f := range pkg.files {
			tree, pdiags := parser.ParseFile(lexer.NewFile(f.name, f.src), false)
			if pdiags.HasErrors() {
				t.Fatalf("unexpected parse errors in %s: %v", f.name, pdiags.All())
			}
			trees[j] = tree
		}
		allTrees = append(allTrees, trees...)

		var fileImports map[*ast.Tree][]sema.FileImport
		if len(pkg.imports) > 0 {
			fileImports = make(map[*ast.Tree][]sema.FileImport, len(pkg.imports))
			for fileIdx, imps := range pkg.imports {
				fileImports[trees[fileIdx]] = imps
			}
		}

		units[i] = &sema.PackageUnit{
			Key:         pkg.key,
			Name:        pkg.key,
			Trees:       trees,
			FileImports: fileImports,
		}
	}

	infos, rdiags, _, treePackage := sema.ResolveProgram(units)
	for _, tree := range allTrees {
		if b := rdiags[tree]; b.HasErrors() {
			t.Fatalf("unexpected resolve errors in %s: %v", tree.File.Name, b.All())
		}
	}

	cdiags := sema.CheckProgram(allTrees, infos, treePackage)
	for _, tree := range allTrees {
		if b := cdiags[tree]; b.HasErrors() {
			t.Fatalf("unexpected check errors in %s: %v", tree.File.Name, b.All())
		}
	}

	mod, gdiags := GeneratePackage(allTrees, infos, "test")
	for _, tree := range allTrees {
		if b := gdiags[tree]; b.HasErrors() {
			t.Fatalf("unexpected codegen errors in %s: %v", tree.File.Name, b.All())
		}
	}
	if err := llvm.VerifyModule(mod.LLVM, llvm.ReturnStatusAction); err != nil {
		t.Fatalf("module verification failed: %v\n%s", err, mod.LLVM.String())
	}
	return mod
}

// compileProgramAndJIT is compileProgramSrc handed to a single LLJIT
// instance, mirroring compileAndJIT/compilePackageAndJIT's own ownership/
// disposal ordering and global_init handling exactly (see compileAndJIT's
// doc comment).
func compileProgramAndJIT(t *testing.T, pkgs []programPackage) *jitModule {
	t.Helper()
	mod := compileProgramSrc(t, pkgs)
	ir := mod.LLVM.String()
	initJIT()

	jit, err := llvm.NewLLJIT(llvm.NewLLJITBuilder())
	if err != nil {
		t.Fatalf("NewLLJIT: %v", err)
	}

	if err := bindMinGWMainThunk(jit); err != nil {
		// mod isn't wrapped/handed to jit yet at this point (that happens
		// below, via AddLLVMIRModule) - still fully owned here.
		mod.Dispose()
		jit.Dispose()
		t.Fatalf("bindMinGWMainThunk: %v", err)
	}

	tsctx := llvm.NewThreadSafeContextFromContext(mod.Ctx)
	tsm := llvm.NewThreadSafeModule(mod.LLVM, tsctx)
	if err := jit.AddLLVMIRModule(jit.MainJITDylib(), tsm); err != nil {
		jit.Dispose()
		t.Fatalf("AddLLVMIRModule: %v", err)
	}

	if initAddr, err := jit.Lookup("llvm_lang.global_init"); err == nil {
		syscall.SyscallN(uintptr(initAddr))
	}

	t.Cleanup(func() {
		if err := jit.Dispose(); err != nil {
			t.Errorf("LLJIT.Dispose: %v", err)
		}
	})
	return &jitModule{
		mod: mod,
		jit: jit,
		ir:  ir,
	}
}

// TestImports_CrossPackageFunctionCall JIT-executes a real two-package
// program - an importing "app" package calling an exported free function
// from an imported "mathutils" package - and asserts on the real,
// JIT-computed result, the same rigor this package's existing single/multi-
// file tests already apply.
func TestImports_CrossPackageFunctionCall(t *testing.T) {
	jm := compileProgramAndJIT(t, []programPackage{
		{
			key: "mathutils",
			files: []packageFile{
				{"mathutils/add.llx", `
func Add(a int, b int) int {
	return a + b
}
`},
			},
		},
		{
			key: "app",
			files: []packageFile{
				{"app/main.llx", `
import "./mathutils"

func main() int {
	return mathutils.Add(1, 2)
}
`},
			},
			imports: map[int][]sema.FileImport{
				0: {{LocalName: "mathutils", TargetKey: "mathutils"}},
			},
		},
	})
	if got := jm.runInt32(t, "main"); got != 3 {
		t.Errorf("main() = %d, want 3", got)
	}
}

// TestImports_CrossPackageStructAndMethod covers a struct type and its
// method, both declared in an imported package, constructed and called from
// the importing package - proving struct layouts/method entries declared in
// one package are visible in the same shared Module exactly like a same-
// package cross-file reference already is (see CODEGEN.md's "Multi-file
// packages" section, extended one level up).
func TestImports_CrossPackageStructAndMethod(t *testing.T) {
	jm := compileProgramAndJIT(t, []programPackage{
		{
			key: "shapes",
			files: []packageFile{
				{"shapes/point.llx", `
struct Point {
	X int
	Y int
}

func (Point) Move(dx int, dy int) {
	this.X = this.X + dx
	this.Y = this.Y + dy
}
`},
			},
		},
		{
			key: "app",
			files: []packageFile{
				{"app/main.llx", `
import "./shapes"

func main() int {
	p := shapes.Point{1, 2}
	p.Move(10, 20)
	return p.X + p.Y
}
`},
			},
			imports: map[int][]sema.FileImport{
				0: {{LocalName: "shapes", TargetKey: "shapes"}},
			},
		},
	})
	if got := jm.runInt32(t, "main"); got != 33 {
		t.Errorf("main() = %d, want 33", got)
	}
}

// TestImports_DiamondDependency covers three packages - "app" imports both
// "a" and "b", each of which itself imports a shared "common" package -
// proving common's declarations are only ever lowered once into the shared
// Module (a real duplicate-symbol/duplicate-definition problem would fail
// LLVM's own module verifier, which compileProgramSrc already checks) and
// are usable from both importers.
func TestImports_DiamondDependency(t *testing.T) {
	jm := compileProgramAndJIT(t, []programPackage{
		{
			key: "common",
			files: []packageFile{
				{"common/base.llx", `
func Base() int {
	return 10
}
`},
			},
		},
		{
			key: "a",
			files: []packageFile{
				{"a/a.llx", `
import "../common"

func FromA() int {
	return common.Base() + 1
}
`},
			},
			imports: map[int][]sema.FileImport{
				0: {{LocalName: "common", TargetKey: "common"}},
			},
		},
		{
			key: "b",
			files: []packageFile{
				{"b/b.llx", `
import "../common"

func FromB() int {
	return common.Base() + 2
}
`},
			},
			imports: map[int][]sema.FileImport{
				0: {{LocalName: "common", TargetKey: "common"}},
			},
		},
		{
			key: "app",
			files: []packageFile{
				{"app/main.llx", `
import "./a"
import "./b"

func main() int {
	return a.FromA() + b.FromB()
}
`},
			},
			imports: map[int][]sema.FileImport{
				0: {
					{LocalName: "a", TargetKey: "a"},
					{LocalName: "b", TargetKey: "b"},
				},
			},
		},
	})
	// FromA = 10+1 = 11, FromB = 10+2 = 12, total = 23.
	if got := jm.runInt32(t, "main"); got != 23 {
		t.Errorf("main() = %d, want 23", got)
	}
}

// TestImports_NewCrossPackageConstructorCall covers `new pkg.Point(...)` -
// heap-allocating a struct via its constructor, both declared in an imported
// package - proving auto-deref (`p.X`, no explicit `(*p).X` needed) and
// export gating still compose correctly with `new` once a package boundary
// is involved, not just the same-package case pointer_test.go's
// TestNewConstructorCallHeapAllocates already covers.
func TestImports_NewCrossPackageConstructorCall(t *testing.T) {
	jm := compileProgramAndJIT(t, []programPackage{
		{
			key: "shapes",
			files: []packageFile{
				{"shapes/point.llx", `
struct Point {
	X int
	Y int

	constructor(x int, y int) {
		this.X = x
		this.Y = y
	}
}
`},
			},
		},
		{
			key: "app",
			files: []packageFile{
				{"app/main.llx", `
import "./shapes"

func main() int {
	p := new shapes.Point(3, 4)
	p.X = p.X + 100
	return p.X + p.Y
}
`},
			},
			imports: map[int][]sema.FileImport{
				0: {{LocalName: "shapes", TargetKey: "shapes"}},
			},
		},
	})
	if got := jm.runInt32(t, "main"); got != 107 {
		t.Errorf("main() = %d, want 107 (103 + 4)", got)
	}
}

// TestImports_ClosureReturnedAcrossPackageBoundary covers a lambda created
// inside an imported package's own exported function, returned across the
// package boundary, and called from the importing package - proving the
// closure's capture-context machinery (the arena-backed relay codegen.md
// documents) doesn't need any package-boundary-specific handling: the
// returned func(int) int value is just an ordinary fat pointer either way,
// same as TestImports_CrossPackageFunctionCall already proves for a plain
// (non-closure) function value.
func TestImports_ClosureReturnedAcrossPackageBoundary(t *testing.T) {
	jm := compileProgramAndJIT(t, []programPackage{
		{
			key: "counters",
			files: []packageFile{
				{"counters/counter.llx", `
func MakeAdder(base int) func(int) int {
	return func(x int) int {
		return base + x
	}
}
`},
			},
		},
		{
			key: "app",
			files: []packageFile{
				{"app/main.llx", `
import "./counters"

func main() int {
	add10 := counters.MakeAdder(10)
	return add10(5)
}
`},
			},
			imports: map[int][]sema.FileImport{
				0: {{LocalName: "counters", TargetKey: "counters"}},
			},
		},
	})
	if got := jm.runInt32(t, "main"); got != 15 {
		t.Errorf("main() = %d, want 15", got)
	}
}
