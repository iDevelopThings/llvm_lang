package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

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

func TestRun_TestMode_ZeroTests(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "empty.llx")
	if err := os.WriteFile(src, []byte("func helper() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Place a fake std/test so import discovery isn't the failure mode.
	stdTest := filepath.Join(dir, "std", "test")
	if err := os.MkdirAll(stdTest, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stdTest, "test.llx"), []byte("struct Runner {}\n"), 0o644); err != nil {
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
	stdTest := filepath.Join(dir, "std", "test")
	if err := os.MkdirAll(stdTest, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stdTest, "test.llx"), []byte("struct Runner {}\n"), 0o644); err != nil {
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

// TestRun_TestMode_StdTestNotFound confirms a clean error (not a panic) when
// std/test genuinely can't be found above the entry package - stdTestImportPath's
// own not-found return path, untested until now (every other -test test
// deliberately places a fake std/test to avoid hitting this path at all).
func TestRun_TestMode_StdTestNotFound(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "main.llx")
	// A syntactically valid TestXxx signature referencing a package named
	// "test" that doesn't actually exist anywhere above dir - so discovery
	// itself can't even resolve the import, exactly the case
	// stdTestImportPath's walk-up-and-fail path exists for.
	body := "func TestFoo(t *test.Runner) {}\n"
	if err := os.WriteFile(src, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	var stderr bytes.Buffer
	code := run([]string{"-test", src}, &stderr)
	if code != exitUsage {
		t.Fatalf("exit = %d, want %d; stderr:\n%s", code, exitUsage, stderr.String())
	}
	if !strings.Contains(stderr.String(), "could not find std/test") {
		t.Fatalf("stderr = %q, want a clean std/test-not-found message", stderr.String())
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
