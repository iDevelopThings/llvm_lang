package loader

import (
	"testing"

	"llvm_lang/src/enums"

	"github.com/spf13/afero"
)

// This file covers loader.Options.TestMode's own scope rule: it splices a
// tests{} block's contents into ordinary top-level declarations only for
// files belonging to the entry package (see loadPackage's isEntry parameter),
// never a transitively-imported dependency's own tests{} blocks - matching
// cmd/llvmc/test.go's discoverTests, which already only scans prog.Entry.
// See src/parser/testsblock_test.go for the parser-level wrap/splice shape
// this all rests on.

// TestLoadProgramWithOptions_TestModeSplicesOnlyEntryPackage covers the core
// scoping rule: the entry package's own tests{} block is spliced (no
// TestBlockDecl node, its FuncDecl reachable as an ordinary top-level decl),
// but an imported dependency's own tests{} block stays wrapped - even though
// the whole program was loaded with TestMode: true.
func TestLoadProgramWithOptions_TestModeSplicesOnlyEntryPackage(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeFiles(t, fs, map[string]string{
		join("root", "main.llx"): "import \"./dep\"\n\nfunc main() int {\n\treturn 0\n}\n\n" +
			"tests {\n\tfunc TestEntry(t *test.Runner) {\n\t}\n}\n",
		join("root", "dep", "pkg.llx"): "func Helper() int {\n\treturn 1\n}\n\n" +
			"tests {\n\tfunc TestDep(t *test.Runner) {\n\t}\n}\n",
	})

	prog, err := LoadProgramWithOptions(fs, join("root"), Options{TestMode: true})
	if err != nil {
		t.Fatalf("LoadProgramWithOptions: %v", err)
	}

	entryTree := prog.Entry.Files[0].Tree
	var entryHasTestBlock, entryHasSplicedFunc bool
	for _, d := range entryTree.Children(entryTree.Root) {
		switch entryTree.Nodes[d].Kind {
		case enums.NodeKinds.TestBlockDecl:
			entryHasTestBlock = true
		case enums.NodeKinds.FuncDecl:
			if entryTree.Text(entryTree.FuncName(d)) == "TestEntry" {
				entryHasSplicedFunc = true
			}
		}
	}
	if entryHasTestBlock {
		t.Errorf("entry package still has a TestBlockDecl node, want it spliced away")
	}
	if !entryHasSplicedFunc {
		t.Errorf("entry package's TestEntry func not found as a spliced top-level decl")
	}

	depPkg := prog.Entry.Files[0].Imports[0].Package
	depTree := depPkg.Files[0].Tree
	var depHasTestBlock bool
	for _, d := range depTree.Children(depTree.Root) {
		if depTree.Nodes[d].Kind == enums.NodeKinds.TestBlockDecl {
			depHasTestBlock = true
		}
	}
	if !depHasTestBlock {
		t.Errorf("imported dependency's tests{} block was spliced too, want it left wrapped (TestMode only applies to the entry package)")
	}
}

// TestLoadProgramWithOptions_TestModeResolvesNestedStdTestImport covers the
// splice's own downstream effect: once TestMode splices a tests{} block's
// `import "std:test"` into an ordinary top-level ImportDecl, loader's own
// import scan (which only ever looks at tree.Children(tree.Root) - see
// loadPackage) picks it up and resolves it against the std: scheme exactly
// like any other import.
func TestLoadProgramWithOptions_TestModeResolvesNestedStdTestImport(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeFiles(t, fs, map[string]string{
		join("root", "main.llx"): "func main() int {\n\treturn 0\n}\n\n" +
			"tests {\n\timport \"std:test\"\n\n\tfunc TestFoo(t *test.Runner) {\n\t}\n}\n",
	})

	prog, err := LoadProgramWithOptions(fs, join("root"), Options{TestMode: true, StdFS: fakeStdTestFS(t)})
	if err != nil {
		t.Fatalf("LoadProgramWithOptions: %v", err)
	}

	entryFile := prog.Entry.Files[0]
	if len(entryFile.Imports) != 1 {
		t.Fatalf("entry file imports = %+v, want exactly 1 (the spliced std:test import)", entryFile.Imports)
	}
	if entryFile.Imports[0].LocalName != "test" {
		t.Errorf("LocalName = %q, want %q", entryFile.Imports[0].LocalName, "test")
	}
}

// TestLoadProgramWithOptions_TestModeFalseLeavesNestedImportUnresolved is the
// negative counterpart proving the actual leak this feature prevents: with
// TestMode left false (the default, every non -test build), a tests{}
// block's own `import "std:test"` is never even seen by loader's import
// scan - it stays buried inside the invisible TestBlockDecl wrapper, so a
// plain build never needs "std:test" to be available at all.
func TestLoadProgramWithOptions_TestModeFalseLeavesNestedImportUnresolved(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeFiles(t, fs, map[string]string{
		join("root", "main.llx"): "func main() int {\n\treturn 0\n}\n\n" +
			"tests {\n\timport \"std:test\"\n\n\tfunc TestFoo(t *test.Runner) {\n\t}\n}\n",
	})

	// No StdFS supplied at all - if the nested import were ever resolved,
	// this would fail with "no standard library location was configured".
	prog, err := LoadProgramWithOptions(fs, join("root"), Options{})
	if err != nil {
		t.Fatalf("LoadProgramWithOptions: %v", err)
	}
	if len(prog.Entry.Files[0].Imports) != 0 {
		t.Errorf("entry file imports = %+v, want none (tests{}'s own import must stay invisible outside test mode)", prog.Entry.Files[0].Imports)
	}
}

// fakeStdTestFS stands in for the compiler's own bundled std/test package
// (see fakeStdFS in scheme_test.go for the identical pattern against
// std:mathutil).
func fakeStdTestFS(t *testing.T) afero.Fs {
	t.Helper()
	fs := afero.NewMemMapFs()
	writeFiles(t, fs, map[string]string{
		"test/test.llx": "struct Runner {\n\tname string\n}\n",
	})
	return fs
}
