package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"llvm_lang/src/loader"
)

// TestCompileAndRun_Success drives compileAndRun in-process against a valid
// program, asserting a clean exit (no diagnostics printed, exit code 0 for a
// void main).
func TestCompileAndRun_Success(t *testing.T) {
	var stderr bytes.Buffer
	code := compileAndRun("t.llx", `
func main() {
	print("hi")
}
`, &stderr, false)

	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want empty", stderr.String())
	}
}

// TestCompileAndRun_MainReturnsInt covers `func main() int { ... }` -
// main's own returned int value must come back as compileAndRun's result,
// unchanged (see the package doc comment's exit-code convention).
func TestCompileAndRun_MainReturnsInt(t *testing.T) {
	var stderr bytes.Buffer
	code := compileAndRun("t.llx", `
func main() int {
	return 2 + 3
}
`, &stderr, false)

	if code != 5 {
		t.Errorf("exit code = %d, want 5", code)
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want empty", stderr.String())
	}
}

// TestCompileAndRun_ParseError covers the failure path for a syntax error:
// compileAndRun must stop at the parser stage (never reach sema/codegen),
// return the compile-error exit code, and print a diagnostic mentioning the
// bad source line - all without panicking.
func TestCompileAndRun_ParseError(t *testing.T) {
	var stderr bytes.Buffer
	code := compileAndRun("t.llx", `
func main() {
	print(
}
`, &stderr, false)

	if code != exitCompile {
		t.Errorf("exit code = %d, want %d", code, exitCompile)
	}
	if stderr.Len() == 0 {
		t.Error("expected a diagnostic on stderr, got none")
	}
}

// TestCompileAndRun_TypeError covers the failure path for a sema.Check type
// error (parsing succeeds, type-checking doesn't): still a clean non-zero
// exit plus a diagnostic, never a panic, and codegen must never run (there's
// nothing that would even be JIT-executable here).
func TestCompileAndRun_TypeError(t *testing.T) {
	var stderr bytes.Buffer
	code := compileAndRun("t.llx", `
func main() {
	var a int = "oops"
	print(a)
}
`, &stderr, false)

	if code != exitCompile {
		t.Errorf("exit code = %d, want %d", code, exitCompile)
	}
	got := stderr.String()
	if !strings.Contains(got, "t.llx:") {
		t.Errorf("stderr = %q, want it to mention the source file/position", got)
	}
}

// TestRun_UsageErrors covers run's own argument/file-handling, separate from
// compileAndRun's pipeline: no argument, and an unreadable path.
func TestRun_UsageErrors(t *testing.T) {
	var stderr bytes.Buffer
	if code := run(nil, &stderr); code != exitUsage {
		t.Errorf("run(nil) exit code = %d, want %d", code, exitUsage)
	}
	if stderr.Len() == 0 {
		t.Error("expected a usage message on stderr, got none")
	}

	stderr.Reset()
	if code := run([]string{"does-not-exist.llx"}, &stderr); code != exitUsage {
		t.Errorf("run(missing file) exit code = %d, want %d", code, exitUsage)
	}
	if stderr.Len() == 0 {
		t.Error("expected a file-read error on stderr, got none")
	}
}

// --- Black-box tests driving the actual built binary as a real, separate
// process via os/exec - this is the only way to see llvmc's JIT-executed
// print output land on a real, capturable stdout (see cmd/llvmc's package
// doc comment, and BLOCKERS.md's codegen-phase entry 7, for why capturing
// that same output *within* this test's own process isn't reliable on
// Windows). TestMain builds the binary once and shares its path with every
// test in this file.

var llvmcPath string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "llvmc-test")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)

	llvmcPath = filepath.Join(dir, "llvmc.exe")
	build := exec.Command("go", "build", "-tags=llvm22", "-o", llvmcPath, ".")
	build.Dir = "."
	if out, err := build.CombinedOutput(); err != nil {
		panic("building llvmc for tests: " + err.Error() + "\n" + string(out))
	}

	os.Exit(m.Run())
}

// TestBinary_Success shells out to the real llvmc binary against the
// repo's own examples/hello/hello.llx and checks its real stdout and exit code.
func TestBinary_Success(t *testing.T) {
	out, err := exec.Command(llvmcPath, "../../examples/hello/hello.llx").Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			t.Fatalf("llvmc exited %v, stderr:\n%s", err, ee.Stderr)
		}
		t.Fatalf("running llvmc: %v", err)
	}

	if got := strings.TrimRight(string(out), "\r\n"); got != "Hello, World!" {
		t.Errorf("stdout = %q, want %q", got, "Hello, World!")
	}
}

// TestBinary_EmitLLVM covers the -emit-llvm flag end to end. The source
// below concatenates two string literals at runtime ("a + b") rather than
// printing a literal directly - a plain literal (e.g. examples/hello/hello.llx's
// "Hello, World!") would still show up in -emit-llvm's output as a global
// constant even without ever running anything, since codegen always embeds
// every string literal it sees as module-level data (see AGENTS.md's
// "string representation" section) - that would make "the printed text is
// absent" a meaningless assertion. The *concatenated* result, by contrast,
// only ever exists at runtime (genStringConcat builds it via a real memcpy
// into an arena buffer, src/codegen/runtime.go) - it can never appear as one
// contiguous token anywhere in the static IR text. So its absence here is a
// real signal that main was never actually executed, not just an artifact of
// which literals happen to appear in the module.
func TestBinary_EmitLLVM(t *testing.T) {
	src := `
func main() {
	a := "NOPE_"
	b := "RUNTIME_EXEC_MARKER"
	c := a + b
	print(c)
}
`
	path := filepath.Join(t.TempDir(), "concat.llx")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatalf("writing temp source file: %v", err)
	}

	out, err := exec.Command(llvmcPath, "-emit-llvm", path).Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			t.Fatalf("llvmc exited %v, stderr:\n%s", err, ee.Stderr)
		}
		t.Fatalf("running llvmc: %v", err)
	}

	got := string(out)
	if !strings.Contains(got, "define") {
		t.Errorf("stdout = %q, want it to contain a LLVM \"define\"", got)
	}
	if !strings.Contains(got, "@main") {
		t.Errorf("stdout = %q, want it to contain the \"@main\" function", got)
	}
	if strings.Contains(got, "NOPE_RUNTIME_EXEC_MARKER") {
		t.Errorf("stdout = %q, want it to never contain the runtime-concatenated printed text - -emit-llvm must not JIT-execute anything", got)
	}
}

// TestBinary_MainExitCode covers examples/features/features.llx end to end: its
// three print calls' real stdout, and its `func main() int` return value
// coming back as the child process's own exit code.
func TestBinary_MainExitCode(t *testing.T) {
	cmd := exec.Command(llvmcPath, "../../examples/features/features.llx")
	out, err := cmd.Output()

	if err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("running llvmc: %v", err)
		}
		if ee.ExitCode() != 30 {
			t.Fatalf("exit code = %d, want 30, stderr:\n%s", ee.ExitCode(), ee.Stderr)
		}
	} else {
		t.Fatal("expected llvmc to exit 30, got exit 0")
	}

	normalized := strings.ReplaceAll(string(out), "\r\n", "\n")
	normalized = strings.TrimRight(normalized, "\n")
	if normalized != "10\n30\n100" {
		t.Errorf("stdout = %q, want the lines 10, 30, 100", string(out))
	}
}

// TestBinary_Failure covers the failure path through the real binary:
// examples/error/error.llx must exit non-zero and print a diagnostic to stderr,
// never a Go panic/stack trace.
func TestBinary_Failure(t *testing.T) {
	cmd := exec.Command(llvmcPath, "../../examples/error/error.llx")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()

	ee, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected llvmc to exit non-zero with an *exec.ExitError, got: %v", err)
	}
	if ee.ExitCode() != exitCompile {
		t.Errorf("exit code = %d, want %d", ee.ExitCode(), exitCompile)
	}

	got := stderr.String()
	if strings.Contains(got, "panic:") {
		t.Errorf("stderr contains a Go panic, want a clean diagnostic:\n%s", got)
	}
	if !strings.Contains(got, "error.llx:") {
		t.Errorf("stderr = %q, want it to mention error.llx's position", got)
	}
}

// TestRun_MissingMainFunction covers a module with no `main` at all - the
// pipeline succeeds all the way through codegen/verification, but there's
// nothing to JIT-execute. Must fail cleanly (a diagnostic-shaped message,
// exitCompile), never panic.
func TestRun_MissingMainFunction(t *testing.T) {
	var stderr bytes.Buffer
	code := compileAndRun("t.llx", `
func add(x int, y int) int {
	return x + y
}
`, &stderr, false)

	if code != exitCompile {
		t.Errorf("exit code = %d, want %d", code, exitCompile)
	}
	if !strings.Contains(stderr.String(), "no main function") {
		t.Errorf("stderr = %q, want it to mention the missing main function", stderr.String())
	}
}

// --- Multi-file package tests (see LANGUAGE.md's "Multi-file packages"
// section) - loader.Load resolves a directory (or a file within one) to
// every .llx file directly inside it, compiled together as one package.

// TestCompileAndRunPackage_MultipleFiles drives compileAndRunPackage
// in-process against two files declared directly (no real filesystem
// involved) - a function call and a struct/method pair, each declared in one
// file and used from another.
func TestCompileAndRunPackage_MultipleFiles(t *testing.T) {
	var stderr bytes.Buffer
	code := compileAndRunPackage([]loader.SourceFile{
		{Name: "main.llx", Src: `
func main() int {
	p := Point{1, 2}
	p.move(10, 20)
	return double(p.x) + p.y
}
`},
		{Name: "point.llx", Src: `
struct Point {
	x int
	y int
}

func (Point) move(dx int, dy int) {
	this.x = this.x + dx
	this.y = this.y + dy
}
`},
		{Name: "math.llx", Src: `
func double(x int) int {
	return x * 2
}
`},
	}, &stderr, false)

	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want empty", stderr.String())
	}
	// p.x = 11, doubled = 22; p.y = 22; total = 44.
	if code != 44 {
		t.Errorf("exit code = %d, want 44", code)
	}
}

// TestBinary_MultiFileDirectory shells out to the real llvmc binary against
// examples/multifile (three files: shapes.llx, util.llx, main.llx - see that
// directory's own doc comments) two ways - the directory itself, and one of
// its files - asserting both resolve to the identical package (same real
// stdout, same exit code), exactly like LANGUAGE.md's "Multi-file packages"
// section promises.
func TestBinary_MultiFileDirectory(t *testing.T) {
	const wantStdout = "12\n48\n30"
	const wantExit = 30

	runAndCheck := func(t *testing.T, path string) {
		t.Helper()
		cmd := exec.Command(llvmcPath, path)
		out, err := cmd.Output()
		if err != nil {
			ee, ok := err.(*exec.ExitError)
			if !ok {
				t.Fatalf("running llvmc: %v", err)
			}
			if ee.ExitCode() != wantExit {
				t.Fatalf("exit code = %d, want %d, stderr:\n%s", ee.ExitCode(), wantExit, ee.Stderr)
			}
		} else {
			t.Fatalf("expected llvmc to exit %d, got exit 0", wantExit)
		}

		got := strings.ReplaceAll(string(out), "\r\n", "\n")
		got = strings.TrimRight(got, "\n")
		if got != wantStdout {
			t.Errorf("stdout = %q, want %q", got, wantStdout)
		}
	}

	t.Run("directory", func(t *testing.T) { runAndCheck(t, "../../examples/multifile") })
	t.Run("file", func(t *testing.T) { runAndCheck(t, "../../examples/multifile/main.llx") })
}
