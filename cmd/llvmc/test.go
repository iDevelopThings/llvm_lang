package main

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"llvm_lang/src/compiler"
	"llvm_lang/src/loader"

	"github.com/spf13/afero"
)

// Synthetic driver filename written only into the afero overlay (never the
// user's real tree). Leading underscores keep it out of typical hand-written
// package layouts.
const testDriverFileName = "__llvmc_test_main.llx"

type discoveredTest struct {
	name string
}

// runTest loads the entry package, discovers Test* funcs, overlays a
// synthesized main driver, and runs the normal compile/JIT/AOT finish path.
func runTest(path string, optimize bool, output string, emitLLVM bool, linkLibs, linkDirs []string, stderr io.Writer) int {
	base := afero.NewOsFs()
	opts := loaderOptionsFunc()
	opts.TestMode = true
	prog, err := loader.LoadProgramWithOptions(base, path, opts)
	if err != nil {
		fmt.Fprintf(stderr, "llvmc: %v\n", err)
		return exitUsage
	}

	tests := discoverTests(prog.Entry)
	if len(tests) == 0 {
		fmt.Fprintln(stderr, "llvmc: -test: no Test* functions found (want func TestXxx(t *test.Runner))")
		return exitUsage
	}

	driver := synthesizeTestDriver(tests)
	overlay := afero.NewMemMapFs()
	fs := afero.NewCopyOnWriteFs(base, overlay)
	driverPath := filepath.Join(prog.Entry.Dir, testDriverFileName)
	if err := afero.WriteFile(fs, driverPath, []byte(driver), 0o644); err != nil {
		fmt.Fprintf(stderr, "llvmc: -test: writing driver overlay: %v\n", err)
		return exitCompile
	}

	prog2, err := loader.LoadProgramWithOptions(fs, path, opts)
	if err != nil {
		fmt.Fprintf(stderr, "llvmc: %v\n", err)
		return exitUsage
	}
	return finish(compiler.CompileProgram(prog2, optimize), stderr, output, emitLLVM, linkLibs, linkDirs)
}

// discoverTests scans only the entry package (imported packages' Test* are
// ignored - the synthesized driver can only call same-package names), via
// the shared ast.Tree.TestFuncs convention (see src/ast/discovery.go) so a
// second consumer of the same TestXxx(t *test.Runner) convention - e.g. an
// LSP "run test" code lens - has a helper to call instead of its own copy.
func discoverTests(pkg *loader.Package) []discoveredTest {
	var out []discoveredTest
	for _, f := range pkg.Files {
		if f.Tree == nil {
			continue
		}
		for tf := range f.Tree.TestFuncs("test", "Runner") {
			out = append(out, discoveredTest{name: tf.Name})
		}
	}
	return out
}

// synthesizeTestDriver builds the driver program's own source: a fixed
// "std:test" import (this compiler's own bundled test.Runner - see
// DECISIONS.md's "std:/lib: import schemes" entry, resolved independent of
// where the entry package itself lives, unlike the relative-path walk this
// used to need) plus one call per discovered TestXxx.
func synthesizeTestDriver(tests []discoveredTest) string {
	var b strings.Builder
	b.WriteString("import \"std:test\"\n\n")
	b.WriteString("func main() int {\n")
	b.WriteString("    failed := 0\n\n")
	b.WriteString("    suite := test.NewSuite()\n")

	for i, t := range tests {
		r := fmt.Sprintf("r%d", i)
		fmt.Fprintf(&b, "    %s := test.NewRunner(%q)\n", r, t.name)
		fmt.Fprintf(&b, "    %s(%s)\n", t.name, r)
		fmt.Fprintf(&b, "    if %s.Failed() {\n", r)
		fmt.Fprintf(&b, "        print(\"--- FAIL: %s\" + \" (\" + %s.DurationStr() + \")\")\n", t.name, r)
		b.WriteString("        failed = failed + 1\n")
		b.WriteString("    } else {\n")
		fmt.Fprintf(&b, "        print(\"--- PASS: %s\" + \" (\" + %s.DurationStr() + \")\")\n", t.name, r)
		b.WriteString("    }\n\n")
	}

	b.WriteString("    print(\"=== TESTS DONE: \" + suite.DurationStr())\n")

	b.WriteString("    if failed > 0 {\n")
	b.WriteString("        print(\"FAIL\")\n")
	b.WriteString("        return 1\n")
	b.WriteString("    }\n")
	b.WriteString("    print(\"PASS\")\n")
	b.WriteString("    return 0\n")
	b.WriteString("}\n")
	return b.String()
}
