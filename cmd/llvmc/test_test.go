package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"llvm_lang/src/loader"

	"github.com/spf13/afero"
)

// fakeTestRunnerSrc is a minimal stand-in for the real std/test/test.llx -
// just enough of Runner's own shape (NewRunner/Failed, both called by the
// synthesized driver itself - see synthesizeTestDriver - plus Assert, called
// by whichever testdata file's own TestXxx body happens to use it) for a
// -test in-process test to compile past its own "std:test" import without
// depending on the real, much larger standard library implementation.
const fakeTestRunnerSrc = `struct Runner {
	name string
}
func NewRunner(name string) *Runner {
	return &Runner{name}
}
func (Runner) Failed() bool {
	return false
}
func (Runner) Assert(cond bool, msg string) bool {
	return cond
}
`

// withFakeStdFS temporarily swaps loaderOptionsFunc for one returning a
// fake std root backed by stdFiles (path -> content, relative to "std/"
// itself, e.g. "test/test.llx") - so -test mode's own in-process tests
// (which run inside this go test binary, whose own os.Executable() has no
// real std/ sibling - see loader.StdlibFS) can resolve "std:test"
// deterministically instead of hitting the real per-machine lookup.
// Restored automatically via t.Cleanup.
func withFakeStdFS(t *testing.T, stdFiles map[string]string) {
	t.Helper()
	fs := afero.NewMemMapFs()
	for path, content := range stdFiles {
		if err := afero.WriteFile(fs, path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	orig := loaderOptionsFunc
	loaderOptionsFunc = func() loader.Options {
		return loader.Options{StdFS: fs}
	}
	t.Cleanup(func() { loaderOptionsFunc = orig })
}

func TestRun_TestMode_ExclusiveWithWatch(t *testing.T) {
	var stderr bytes.Buffer
	code := run([]string{"-watch", "-test", "../../examples/hello/hello.llx"}, &stderr)
	if code != exitUsage {
		t.Fatalf("exit = %d, want %d; stderr:\n%s", code, exitUsage, stderr.String())
	}
	if !strings.Contains(stderr.String(), "-watch and -test are mutually exclusive") {
		t.Fatalf("stderr = %q, want mutual-exclusion message", stderr.String())
	}
}

// TestRun_TestMode_ZeroTests and TestRun_TestMode_WrongSignatureIgnored both
// exit on the "no Test* functions found" path, which returns before the
// synthesized driver (the only place "std:test" ever gets imported) is even
// built - so unlike every other -test test below, neither needs
// withFakeStdFS at all.
func TestRun_TestMode_ZeroTests(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "empty.llx")
	if err := os.WriteFile(src, []byte("func helper() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var stderr bytes.Buffer
	code := run([]string{"-test", src}, &stderr)
	if code != exitUsage {
		t.Fatalf("exit = %d, want %d; stderr:\n%s", code, exitUsage, stderr.String())
	}
	if !strings.Contains(stderr.String(), "no Test* functions found") {
		t.Fatalf("stderr = %q, want zero-tests message", stderr.String())
	}
}

func TestRun_TestMode_WrongSignatureIgnored(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "wrong.llx")
	// Looks like a test name but wrong signature - must not be discovered.
	body := "func TestFoo(a int) {}\n"
	if err := os.WriteFile(src, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	var stderr bytes.Buffer
	code := run([]string{"-test", src}, &stderr)
	if code != exitUsage {
		t.Fatalf("exit = %d, want %d; stderr:\n%s", code, exitUsage, stderr.String())
	}
	if !strings.Contains(stderr.String(), "no Test* functions found") {
		t.Fatalf("stderr = %q, want zero-tests (wrong sig ignored)", stderr.String())
	}
}

func TestRun_TestMode_MainCollision(t *testing.T) {
	withFakeStdFS(t, map[string]string{"test/test.llx": fakeTestRunnerSrc})

	dir := filepath.Join("testdata", "testmode", "with_main")
	var stderr bytes.Buffer
	code := run([]string{"-test", dir}, &stderr)
	if code != exitCompile {
		t.Fatalf("exit = %d, want %d; stderr:\n%s", code, exitCompile, stderr.String())
	}
	errText := stderr.String()
	if !strings.Contains(errText, "main") {
		t.Fatalf("stderr = %q, want a main redeclaration diagnostic", errText)
	}
}

func TestBinary_TestMode_DemoFails(t *testing.T) {
	out, err := exec.Command(llvmcPath, "-test", "../../examples/test_demo").CombinedOutput()
	ee, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("want ExitError, got err=%v out:\n%s", err, out)
	}
	if ee.ExitCode() != 1 {
		t.Fatalf("exit = %d, want 1; out:\n%s", ee.ExitCode(), out)
	}
	got := string(out)
	if !strings.Contains(got, "--- PASS: TestAdd") {
		t.Errorf("missing PASS TestAdd; out:\n%s", got)
	}
	if !strings.Contains(got, "--- FAIL: TestDeliberateFailure") {
		t.Errorf("missing FAIL TestDeliberateFailure; out:\n%s", got)
	}
	if !strings.Contains(got, "FAIL: want unequal ints to fail") {
		t.Errorf("missing assert message; out:\n%s", got)
	}
	// The driver's own overall summary line is a bare "FAIL" on its own
	// line - checking for the substring alone would trivially pass just
	// from the per-test "--- FAIL: ..." line already asserted above.
	lines := strings.Split(strings.TrimRight(got, "\r\n"), "\n")
	if !slices.Contains(lines, "FAIL") {
		t.Errorf("missing overall FAIL summary line; out:\n%s", got)
	}
}

// TestBinary_TestMode_OverlayNeverTouchesRealDisk confirms the synthesized
// driver (afero.NewCopyOnWriteFs in runTest) never actually writes
// __llvmc_test_main.llx into the real example directory - the whole point
// of the copy-on-write overlay over a plain afero.NewOsFs() write.
func TestBinary_TestMode_OverlayNeverTouchesRealDisk(t *testing.T) {
	driverPath := filepath.Join("..", "..", "examples", "test_demo", testDriverFileName)
	if _, err := os.Stat(driverPath); err == nil {
		t.Fatalf("%s exists before running -test - stale from a prior manual run?", driverPath)
	}

	if _, err := exec.Command(llvmcPath, "-test", "../../examples/test_demo").CombinedOutput(); err == nil {
		t.Fatal("want the demo's deliberate failure to produce a non-zero exit")
	}

	if _, err := os.Stat(driverPath); !os.IsNotExist(err) {
		t.Fatalf("%s exists after running -test - the overlay leaked onto real disk (err=%v)", driverPath, err)
	}
}

func TestBinary_TestMode_AllPass(t *testing.T) {
	dir := filepath.Join("testdata", "testmode", "all_pass")
	out, err := exec.Command(llvmcPath, "-test", dir).CombinedOutput()
	if err != nil {
		t.Fatalf("llvmc -test: %v\nout:\n%s", err, out)
	}
	got := string(out)
	if !strings.Contains(got, "--- PASS: TestOne") {
		t.Errorf("missing PASS; out:\n%s", got)
	}
	lines := strings.Split(strings.TrimRight(got, "\r\n"), "\n")
	if !slices.Contains(lines, "PASS") {
		t.Errorf("missing overall PASS summary line; out:\n%s", got)
	}
}

func TestBinary_TestMode_AOT(t *testing.T) {
	dir := filepath.Join("testdata", "testmode", "all_pass")
	exe := filepath.Join(t.TempDir(), "suite.exe")
	buildOut, err := exec.Command(llvmcPath, "-test", "-o", exe, dir).CombinedOutput()
	if err != nil {
		t.Fatalf("llvmc -test -o: %v\nout:\n%s", err, buildOut)
	}
	runOut, err := exec.Command(exe).CombinedOutput()
	if err != nil {
		t.Fatalf("running suite.exe: %v\nout:\n%s", err, runOut)
	}
	if !strings.Contains(string(runOut), "--- PASS: TestOne") {
		t.Errorf("AOT suite stdout:\n%s", runOut)
	}
}

// TestBinary_TestMode_AssertCoverage exercises AssertSliceEqual's and
// AssertNil/AssertNotNil's actual failure/success paths, not just their
// happy path - two tests here deliberately fail so the length-mismatch and
// element-mismatch branches are proven to detect a real difference, and
// AssertNil/AssertNotNil are instantiated at a struct pointer, not just
// *int (see testdata/testmode/assert_coverage/assert_coverage.llx).
func TestBinary_TestMode_AssertCoverage(t *testing.T) {
	dir := filepath.Join("testdata", "testmode", "assert_coverage")
	out, err := exec.Command(llvmcPath, "-test", dir).CombinedOutput()
	ee, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("want ExitError (two deliberate failures), got err=%v out:\n%s", err, out)
	}
	if ee.ExitCode() != 1 {
		t.Fatalf("exit = %d, want 1; out:\n%s", ee.ExitCode(), out)
	}
	got := string(out)
	for _, want := range []string{
		"--- PASS: TestSliceEqualPass",
		"--- PASS: TestSliceEqualEmptyBothPass",
		"    FAIL: length mismatch should fail",
		"--- FAIL: TestSliceEqualLengthMismatchFails",
		"    FAIL: content mismatch should fail",
		"--- FAIL: TestSliceEqualContentMismatchFails",
		"--- PASS: TestNilPointerStruct",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q; out:\n%s", want, got)
		}
	}
}

// TestBinary_TestMode_StdTimeSelfImport is a regression test for a real
// crash: `-test std/time` used to fail with "Symbols not found:
// [QueryPerformanceFrequency.N, QueryPerformanceCounter.N]" - std/test's own
// Runner/Suite import std:time for their duration helpers, so testing
// std/time directly loaded that same package twice (once as the entry
// package, once via std:test's own std:time import) and codegen emitted a
// second, LLVM-renamed declaration of the same real extern symbol (see
// DECISIONS.md's dated entry, and declareExternFuncSignature's own
// NamedFunction reuse, func.go).
func TestBinary_TestMode_StdTimeSelfImport(t *testing.T) {
	dir := filepath.Join("..", "..", "std", "time")
	out, err := exec.Command(llvmcPath, "-test", dir).CombinedOutput()
	if err != nil {
		t.Fatalf("llvmc -test std/time: %v\nout:\n%s", err, out)
	}
	got := string(out)
	if strings.Contains(got, "Symbols not found") {
		t.Errorf("extern symbol collision regressed; out:\n%s", got)
	}
	lines := strings.Split(strings.TrimRight(got, "\r\n"), "\n")
	if !slices.Contains(lines, "PASS") {
		t.Errorf("missing overall PASS summary line; out:\n%s", got)
	}
}

// TestRun_TestMode_StdRootNotConfigured confirms a clean error (not a
// panic) when no standard library location is available at all - the
// synthesized driver's own "std:test" import (see synthesizeTestDriver)
// resolving via loaderOptionsFunc's real, unfaked implementation, which -
// like every in-process test in this file - finds no genuine std/ sibling
// next to this go-test-compiled binary (see loader.StdlibFS). Every other
// -test test in this file deliberately calls withFakeStdFS specifically to
// avoid hitting this path.
func TestRun_TestMode_StdRootNotConfigured(t *testing.T) {
	dir := filepath.Join("testdata", "testmode", "all_pass")
	var stderr bytes.Buffer
	code := run([]string{"-test", dir}, &stderr)
	if code != exitUsage {
		t.Fatalf("exit = %d, want %d; stderr:\n%s", code, exitUsage, stderr.String())
	}
	if !strings.Contains(stderr.String(), "no standard library location was configured") {
		t.Fatalf("stderr = %q, want a clean std-root-not-configured message", stderr.String())
	}
}

// TestBinary_TestMode_InlineTestsBlock covers a tests{} block living in the
// same file as ordinary package code (see LANGUAGE.md's "tests{}" section):
// -test discovers and runs TestAdd exactly as if it were a real top-level
// FuncDecl.
func TestBinary_TestMode_InlineTestsBlock(t *testing.T) {
	dir := filepath.Join("testdata", "testmode", "inline_block")
	out, err := exec.Command(llvmcPath, "-test", dir).CombinedOutput()
	if err != nil {
		t.Fatalf("llvmc -test: %v\nout:\n%s", err, out)
	}
	got := string(out)
	if !strings.Contains(got, "--- PASS: TestAdd") {
		t.Errorf("missing PASS TestAdd; out:\n%s", got)
	}
	lines := strings.Split(strings.TrimRight(got, "\r\n"), "\n")
	if !slices.Contains(lines, "PASS") {
		t.Errorf("missing overall PASS summary line; out:\n%s", got)
	}
}

// TestBinary_InlineTestsBlockInvisibleOutsideTestMode is the actual
// regression test for the leak tests{} exists to prevent: the exact same
// file compiled WITHOUT -test must never need "std:test" to resolve (there
// is no -test std/ overlay on this path at all) and must never emit a
// TestAdd symbol into the module.
func TestBinary_InlineTestsBlockInvisibleOutsideTestMode(t *testing.T) {
	src := filepath.Join("testdata", "testmode", "inline_block", "inline_block.llx")
	out, err := exec.Command(llvmcPath, "-emit-llvm", src).CombinedOutput()
	if err != nil {
		t.Fatalf("llvmc -emit-llvm (plain path): %v\nout:\n%s", err, out)
	}
	ir := string(out)
	if !strings.Contains(ir, "@add") {
		t.Fatalf("expected the ordinary @add function in the IR, got:\n%s", ir)
	}
	for _, absent := range []string{"TestAdd", "Runner", "std:test", "test.Runner"} {
		if strings.Contains(ir, absent) {
			t.Errorf("plain (non -test) IR mentions %q, want the tests{} block completely invisible:\n%s", absent, ir)
		}
	}
}

// TestBinary_TestMode_EmitLLVM confirms -test composes with -emit-llvm (the
// synthesized driver's own IR, not an execution) - claimed in CODEGEN.md/
// main.go's doc comment but, until now, never actually exercised.
func TestBinary_TestMode_EmitLLVM(t *testing.T) {
	dir := filepath.Join("testdata", "testmode", "all_pass")
	out, err := exec.Command(llvmcPath, "-test", "-emit-llvm", dir).CombinedOutput()
	if err != nil {
		t.Fatalf("llvmc -test -emit-llvm: %v\nout:\n%s", err, out)
	}
	ir := string(out)
	if !strings.Contains(ir, "define") {
		t.Fatalf("expected LLVM IR containing at least one define, got:\n%s", ir)
	}
	if !strings.Contains(ir, "@main") {
		t.Fatalf("expected the synthesized driver's own @main, got:\n%s", ir)
	}
}
