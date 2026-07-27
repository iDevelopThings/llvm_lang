package main

import (
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"time"

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

// runTest implements plain `-test <path>`: a single explicit package
// target, zero discovered Test* funcs is a hard usage error. See
// runTestAll for `-test -all`'s directory-tree counterpart, which shares
// this same per-package flow via runOnePackageTest but treats zero tests as
// a silent skip instead.
func runTest(path string, optimize bool, output string, emitLLVM bool, linkLibs, linkDirs []string, stderr io.Writer) int {
	base := afero.NewOsFs()
	opts := loaderOptionsFunc()
	code, _ := runOnePackageTest(base, path, opts, optimize, output, emitLLVM, linkLibs, linkDirs, stderr, true)
	return code
}

// runTestAll implements `-test -all`: recursively discovers every package
// directory under path (loader.DiscoverPackages - the same package-boundary
// walk src/lsp already uses for not-yet-imported-package completion) and
// runs runOnePackageTest against each one independently, aggregating
// pass/fail counts rather than compiling the whole tree as one program. A
// package with zero Test* funcs is silently skipped; a package that fails
// to load or run counts as a failure and the walk continues rather than
// aborting on the first one.
func runTestAll(path string, optimize, emitLLVM bool, linkLibs, linkDirs []string, stderr io.Writer) int {
	start := time.Now()
	base := afero.NewOsFs()
	opts := loaderOptionsFunc()

	var dirs []string
	for cand := range loader.DiscoverPackages(base, path) {
		dirs = append(dirs, cand.Dir)
	}
	sort.Strings(dirs)
	if len(dirs) == 0 {
		fmt.Fprintf(stderr, "llvmc: -test -all: no packages found under %s\n", path)
		return exitUsage
	}

	var ran, failed int
	for _, dir := range dirs {
		code, didRun := runOnePackageTest(base, dir, opts, optimize, "", emitLLVM, linkLibs, linkDirs, stderr, false)
		if !didRun {
			continue // zero Test* funcs in this package - silently skipped
		}
		ran++
		label := "PASS"
		if code != 0 {
			failed++
			label = "FAIL"
		}
		fmt.Fprintf(stderr, "=== PKG %s: %s\n", dir, label)
	}

	fmt.Fprintf(stderr, "=== SUMMARY: %d package(s) run, %d failed, took %s\n", ran, failed, formatDuration(time.Since(start)))
	if failed > 0 {
		fmt.Fprintln(stderr, "FAIL")
		return exitCompile
	}
	fmt.Fprintln(stderr, "PASS")
	return 0
}

// formatDuration renders d with the same ns/us/ms/s unit-picking and
// two-decimal precision as std/time.FormattedDuration (see time.llx) - the
// language-level function every per-test/per-suite duration already printed
// above runTestAll's own summary line uses - so the aggregate total reads
// consistently with them rather than in a different style.
func formatDuration(d time.Duration) string {
	seconds := d.Seconds()
	unit := "s"
	scaled := seconds
	switch {
	case seconds < 0.000001:
		unit = "ns"
		scaled = seconds * 1e9
	case seconds < 0.001:
		unit = "us"
		scaled = seconds * 1e6
	case seconds < 1.0:
		unit = "ms"
		scaled = seconds * 1e3
	}
	return fmt.Sprintf("%.2f%s", scaled, unit)
}

// runOnePackageTest loads pkgPath in TestMode, discovers its own Test* funcs
// (discoverTests), overlays a synthesized driver, and runs the ordinary
// compile/JIT/AOT finish path - the single-package body shared by runTest
// and runTestAll.
//
// zeroTestsIsError is the only real behavioral difference between the two
// callers: a single explicit target treats zero discovered tests as a hard
// usage error (ran=false, code=exitUsage, message printed here); a tree
// walk wants to silently skip such a package instead (ran=false, no
// message).
func runOnePackageTest(fs afero.Fs, pkgPath string, opts loader.Options, optimize bool, output string, emitLLVM bool, linkLibs, linkDirs []string, stderr io.Writer, zeroTestsIsError bool) (code int, ran bool) {
	opts.TestMode = true
	prog, err := loader.LoadProgramWithOptions(fs, pkgPath, opts)
	if err != nil {
		fmt.Fprintf(stderr, "llvmc: %v\n", err)
		return exitUsage, false
	}

	tests := discoverTests(prog.Entry)
	if len(tests) == 0 {
		if zeroTestsIsError {
			fmt.Fprintln(stderr, "llvmc: -test: no Test* functions found (want func TestXxx(t *test.Runner))")
			return exitUsage, false
		}
		return 0, false
	}

	driver := synthesizeTestDriver(tests)
	overlay := afero.NewMemMapFs()
	ofs := afero.NewCopyOnWriteFs(fs, overlay)
	driverPath := filepath.Join(prog.Entry.Dir, testDriverFileName)
	if err := afero.WriteFile(ofs, driverPath, []byte(driver), 0o644); err != nil {
		fmt.Fprintf(stderr, "llvmc: -test: writing driver overlay: %v\n", err)
		return exitCompile, false
	}

	prog2, err := loader.LoadProgramWithOptions(ofs, pkgPath, opts)
	if err != nil {
		fmt.Fprintf(stderr, "llvmc: %v\n", err)
		return exitUsage, false
	}
	return finish(compiler.CompileProgram(prog2, optimize), stderr, output, emitLLVM, linkLibs, linkDirs), true
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
