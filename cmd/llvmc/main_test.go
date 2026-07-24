package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"llvm_lang/src/loader"

	"github.com/spf13/afero"
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

// TestBinary_EmitLLVMWithNonConstantGlobalNeverExecutes covers -emit-llvm
// against a non-constant global initializer (see CODEGEN.md's "Global var
// initializers" section): its real initializer now runs inside a
// synthesized init function registered into @llvm.global_ctors, but
// -emit-llvm must still only ever print IR text and exit 0, exactly as
// before this feature existed - never actually running the synthesized init
// function (or main). Mirrors TestBinary_EmitLLVM's own marker-string trick
// exactly (see its doc comment for why a runtime-only string concatenation,
// not a plain literal or a small int a JIT-independent LLVM constant-fold
// could just as easily produce, is the only reliable "this never actually
// ran" signal) - the concatenation happens inside a *global*'s own
// initializer this time, specifically exercising buildGlobalInitFn's
// lowering rather than an ordinary function body's.
func TestBinary_EmitLLVMWithNonConstantGlobalNeverExecutes(t *testing.T) {
	src := `
func buildGreeting() string {
	a := "NOPE_"
	b := "RUNTIME_EXEC_MARKER"
	return a + b
}

var greeting string = buildGreeting()

func main() {
	print(greeting)
}
`
	path := filepath.Join(t.TempDir(), "global_concat.llx")
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
	if !strings.Contains(got, "@llvm.global_ctors") {
		t.Errorf("stdout = %q, want it to contain the @llvm.global_ctors array", got)
	}
	if !strings.Contains(got, "llvm_lang.global_init") {
		t.Errorf("stdout = %q, want it to contain the synthesized llvm_lang.global_init function", got)
	}
	if strings.Contains(got, "NOPE_RUNTIME_EXEC_MARKER") {
		t.Errorf("stdout = %q, want it to never contain the runtime-concatenated printed text - -emit-llvm must not execute the synthesized global-init function", got)
	}
}

// TestBinary_GlobalInitExample runs examples/global_init/global_init.llx end
// to end via the real binary: computeStart()'s result, a reference to
// another global, and a `new`-heap-allocated struct must all be genuinely
// evaluated by the synthesized init function before main runs (see
// CODEGEN.md's "Global var initializers" section) - real stdout output plus
// the exit code, not just a clean compile.
func TestBinary_GlobalInitExample(t *testing.T) {
	cmd := exec.Command(llvmcPath, "../../examples/global_init/global_init.llx")
	out, err := cmd.Output()

	if err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("running llvmc: %v", err)
		}
		if ee.ExitCode() != 52 {
			t.Fatalf("exit code = %d, want 52, stderr:\n%s", ee.ExitCode(), ee.Stderr)
		}
	} else {
		t.Fatal("expected llvmc to exit 52, got exit 0")
	}

	normalized := strings.ReplaceAll(string(out), "\r\n", "\n")
	normalized = strings.TrimRight(normalized, "\n")
	if normalized != "42\n42\n52" {
		t.Errorf("stdout = %q, want the lines 42, 42, 52", string(out))
	}
}

// TestBinary_MultiReturnExample runs examples/multireturn/multireturn.llx end
// to end via the real binary - the worked dogfooding demo for this round's
// Go-style multi-return values feature (see LANGUAGE.md's own section of the
// same name): divide's and find's `(T, bool)` results, each destructured via
// both supported forms (`:=` for a fresh pair, `=` reusing the same two
// names for a second call), real stdout output confirming both the
// found/ok and not-found/division-by-zero paths.
func TestBinary_MultiReturnExample(t *testing.T) {
	cmd := exec.Command(llvmcPath, "../../examples/multireturn/multireturn.llx")
	out, err := cmd.Output()
	if err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("running llvmc: %v", err)
		}
		t.Fatalf("llvmc exited %v, stderr:\n%s", err, ee.Stderr)
	}

	normalized := strings.ReplaceAll(string(out), "\r\n", "\n")
	normalized = strings.TrimRight(normalized, "\n")
	want := "5\ndivision by zero\n2\nnot found"
	if normalized != want {
		t.Errorf("stdout = %q, want %q", normalized, want)
	}
}

// multiAssignWant is the expected stdout for
// examples/multi_assign/multi_assign.llx - shared by both the JIT
// (TestBinary_MultiAssignExample) and AOT (TestBinary_AOT_MultiAssign)
// end-to-end runs below, so both stay byte-identical to each other and to
// the source file's own inline comments.
const multiAssignWant = "1\n2\n1\n2\n2\n1\n5\nhi"

// TestBinary_MultiAssignExample runs examples/multi_assign/multi_assign.llx
// end to end via the real binary - the worked dogfooding demo for this
// round's general Go-style parallel multi-assignment (see LANGUAGE.md's
// "Go-style multi-return values" section): plain parallel init (`a, b := 1,
// 2`), the classic swap idiom (`a, b = b, a` - the concrete proof this
// feature's own evaluate-all-then-assign-all ordering actually matters), and
// mixed-type positions (`x, s := 5, "hi"`).
func TestBinary_MultiAssignExample(t *testing.T) {
	cmd := exec.Command(llvmcPath, "../../examples/multi_assign/multi_assign.llx")
	out, err := cmd.Output()
	if err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("running llvmc: %v", err)
		}
		t.Fatalf("llvmc exited %v, stderr:\n%s", err, ee.Stderr)
	}

	normalized := strings.ReplaceAll(string(out), "\r\n", "\n")
	normalized = strings.TrimRight(normalized, "\n")
	if normalized != multiAssignWant {
		t.Errorf("stdout = %q, want %q", normalized, multiAssignWant)
	}
}

// TestBinary_EnumsExample runs examples/enums/enums.llx end to end via the
// real binary - the worked dogfooding demo for this round's Rust-style enums
// plus `match` feature (see LANGUAGE.md's "Enums"/"match" sections): a
// Shape enum's own Area() method dispatching via match, ==/!= across every
// combination (same-variant-same-data, same-variant-different-data,
// different-variant), print() of each variant, and a recursive List enum
// summed via recursive match - proving the pointer-based self-reference
// genuinely works. Its own `main() int` returns the list's own sum (6),
// which becomes the exit code, exactly like every other int-returning
// example already does.
func TestBinary_EnumsExample(t *testing.T) {
	cmd := exec.Command(llvmcPath, "../../examples/enums/enums.llx")
	out, err := cmd.Output()

	if err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("running llvmc: %v", err)
		}
		if ee.ExitCode() != 6 {
			t.Fatalf("exit code = %d, want 6, stderr:\n%s", ee.ExitCode(), ee.Stderr)
		}
	} else {
		t.Fatal("expected llvmc to exit 6, got exit 0")
	}

	normalized := strings.ReplaceAll(string(out), "\r\n", "\n")
	normalized = strings.TrimRight(normalized, "\n")
	want := "12.566360\n" +
		"12.000000\n" +
		"0.000000\n" +
		"Circle(2.000000)\n" +
		"Rectangle(3.000000 4.000000)\n" +
		"Point\n" +
		"true\n" +
		"false\n" +
		"false\n" +
		"false\n" +
		"6"
	if normalized != want {
		t.Errorf("stdout = %q, want %q", normalized, want)
	}
}

// matchValuesWant is the expected stdout for examples/match_values/match_values.llx
// (see that file's own inline comments) - shared by the JIT and AOT tests
// below, exactly like every other worked-example test pair in this file.
const matchValuesWant = "1\n" +
	"1\n" +
	"2\n" +
	"0\n" +
	"small\n" +
	"medium-or-large\n" +
	"medium-or-large\n" +
	"unknown\n" +
	"1\n" +
	"0"

// TestBinary_MatchValuesExample runs examples/match_values/match_values.llx
// end to end via the real binary - the worked dogfooding demo for this
// round's generalization of `match` into a general Go-`switch`-style value
// dispatcher (see LANGUAGE.md's "match" section's plain-value-pattern
// extension): an int value-match with a multi-value arm, a string
// value-match, a bool value-match, and the mandatory wildcard `_` arm every
// value-match requires. Its own `main() int` returns 5 (see that file's own
// inline comment breaking down exactly how), which becomes the exit code.
func TestBinary_MatchValuesExample(t *testing.T) {
	cmd := exec.Command(llvmcPath, "../../examples/match_values/match_values.llx")
	out, err := cmd.Output()

	if err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("running llvmc: %v", err)
		}
		if ee.ExitCode() != 5 {
			t.Fatalf("exit code = %d, want 5, stderr:\n%s", ee.ExitCode(), ee.Stderr)
		}
	} else {
		t.Fatal("expected llvmc to exit 5, got exit 0")
	}

	normalized := strings.ReplaceAll(string(out), "\r\n", "\n")
	normalized = strings.TrimRight(normalized, "\n")
	if normalized != matchValuesWant {
		t.Errorf("stdout = %q, want %q", normalized, matchValuesWant)
	}
}

// matchExprWant is the expected stdout for examples/match_expr/match_expr.llx
// (see that file's own inline comments) - shared by the JIT and AOT tests
// below, exactly like every other worked-example test pair in this file.
const matchExprWant = "small\n" +
	"small-but-special\n" +
	"medium-or-large\n" +
	"medium-or-large\n" +
	"unknown\n" +
	"low\n" +
	"mid\n" +
	"high\n" +
	"yes\n" +
	"large"

// TestBinary_MatchExprExample runs examples/match_expr/match_expr.llx end to
// end via the real binary - the worked dogfooding demo for this round's
// `match` as an expression (see LANGUAGE.md's "match" section's "match as an
// expression" subsection): a bare-expression arm and a block arm (with its
// own nested if/multiple yields) coexisting in the same match, used as a
// `:=` right-hand side, a bare `return`, and a function-call argument, over
// both a value-match subject (string/int/bool) and an enum-match subject.
// Its own `main() int` returns 0.
func TestBinary_MatchExprExample(t *testing.T) {
	cmd := exec.Command(llvmcPath, "../../examples/match_expr/match_expr.llx")
	out, err := cmd.Output()
	if err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("running llvmc: %v", err)
		}
		t.Fatalf("llvmc exited %v, stderr:\n%s", err, ee.Stderr)
	}

	normalized := strings.ReplaceAll(string(out), "\r\n", "\n")
	normalized = strings.TrimRight(normalized, "\n")
	if normalized != matchExprWant {
		t.Errorf("stdout = %q, want %q", normalized, matchExprWant)
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

// coroutinesExampleWant is examples/coroutines/coroutines.llx's own expected
// stdout (see that file's inline comments) - shared by the JIT and AOT
// variants below.
const coroutinesExampleWant = "100\n200\n300\n3\n2\n1\n100\n200\n2\n1"

// TestBinary_CoroutinesExample runs examples/coroutines/coroutines.llx
// through the real llvmc binary (JIT) - true suspend/resume coroutines (see
// LANGUAGE.md's "Coroutines" section): resuming to normal completion (every
// segment's own print, then every destructor firing in reverse declaration
// order via the coroutine's own final suspend) and an early `delete` on a
// still-suspended handle destructing exactly what's live at that point.
func TestBinary_CoroutinesExample(t *testing.T) {
	cmd := exec.Command(llvmcPath, "../../examples/coroutines/coroutines.llx")
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			t.Fatalf("running llvmc: %v, stderr:\n%s", err, ee.Stderr)
		}
		t.Fatalf("running llvmc: %v", err)
	}

	normalized := strings.ReplaceAll(string(out), "\r\n", "\n")
	normalized = strings.TrimRight(normalized, "\n")
	if normalized != coroutinesExampleWant {
		t.Errorf("stdout = %q, want %q", normalized, coroutinesExampleWant)
	}
}

// TestBinary_AOT_Coroutines AOT-compiles examples/coroutines/coroutines.llx
// and confirms identical output to the JIT variant above - see
// TestBinary_AOT_HelloWorld's own doc comment for why this matters (a
// genuinely standalone, deployable binary, no llvmc/JIT in the loop).
func TestBinary_AOT_Coroutines(t *testing.T) {
	exePath := aotCompile(t, "../../examples/coroutines/coroutines.llx")

	out, err := exec.Command(exePath).Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			t.Fatalf("%s exited %v, stderr:\n%s", exePath, err, ee.Stderr)
		}
		t.Fatalf("running %s: %v", exePath, err)
	}

	normalized := strings.ReplaceAll(string(out), "\r\n", "\n")
	normalized = strings.TrimRight(normalized, "\n")
	if normalized != coroutinesExampleWant {
		t.Errorf("stdout = %q, want %q", normalized, coroutinesExampleWant)
	}
}

// TestBinary_NoOptAsyncIsCleanError covers the -no-opt/async restriction
// (see CODEGEN.md's "Coroutines" section and src/compiler's
// checkNoOptAsyncRestriction): llvm.coro.* intrinsics are only ever lowered
// by the optimization pipeline, so -no-opt against a program declaring an
// async function must be a clean, immediate compile-time diagnostic - never
// a crash (confirmed directly, before this restriction existed, as a real
// "LLVM ERROR: Cannot select: intrinsic %llvm.coro.destroy" fatal abort) and
// never silently wrong output.
func TestBinary_NoOptAsyncIsCleanError(t *testing.T) {
	cmd := exec.Command(llvmcPath, "-no-opt", "../../examples/coroutines/coroutines.llx")
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
	if strings.Contains(got, "panic:") || strings.Contains(got, "LLVM ERROR") {
		t.Errorf("stderr contains a crash, want a clean diagnostic:\n%s", got)
	}
	if !strings.Contains(got, "-no-opt") {
		t.Errorf("stderr = %q, want it to mention -no-opt", got)
	}
}

// TestBinary_NoOptCoroutineTypeWithNoAsyncFuncIsCleanError covers the other
// checkNoOptAsyncRestriction trigger: a `coroutine`-typed declaration with
// NO async func anywhere in the program still needs setupCoroutines' own
// intrinsics (see codegen.programUsesCoroutines) - -no-opt against one must
// be a clean diagnostic here too, not the same fatal
// "LLVM ERROR: Cannot select: intrinsic %llvm.coro.destroy" abort confirmed
// directly before this specific trigger was added to the restriction.
func TestBinary_NoOptCoroutineTypeWithNoAsyncFuncIsCleanError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "main.llx")
	src := `func main() int {
	var h coroutine
	return 0
}
`
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(llvmcPath, "-no-opt", path)
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
	if strings.Contains(got, "panic:") || strings.Contains(got, "LLVM ERROR") {
		t.Errorf("stderr contains a crash, want a clean diagnostic:\n%s", got)
	}
	if !strings.Contains(got, "-no-opt") {
		t.Errorf("stderr = %q, want it to mention -no-opt", got)
	}
}

// schedulerDemoExampleWant is examples/scheduler_demo/scheduler_demo.llx's
// own expected stdout (see that file's inline comments): the countdown
// itself (3, 2, 1), driven entirely by std/scheduler's Schedule/Tick, then
// the number of 0.5s ticks it took to finish.
const schedulerDemoExampleWant = "3\n2\n1\n4"

// TestBinary_SchedulerDemoExample runs examples/scheduler_demo through the
// real llvmc binary (JIT) - std/scheduler's own Unity-`StartCoroutine`-style
// API (see LANGUAGE.md's "Standard library" section) on top of the
// `coroutine` type keyword, imported via a genuine `import` path (unlike
// examples/coroutines, which has no imports at all).
func TestBinary_SchedulerDemoExample(t *testing.T) {
	cmd := exec.Command(llvmcPath, "../../examples/scheduler_demo/scheduler_demo.llx")
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			t.Fatalf("running llvmc: %v, stderr:\n%s", err, ee.Stderr)
		}
		t.Fatalf("running llvmc: %v", err)
	}

	normalized := strings.ReplaceAll(string(out), "\r\n", "\n")
	normalized = strings.TrimRight(normalized, "\n")
	if normalized != schedulerDemoExampleWant {
		t.Errorf("stdout = %q, want %q", normalized, schedulerDemoExampleWant)
	}
}

// TestBinary_AOT_SchedulerDemo AOT-compiles examples/scheduler_demo and
// confirms identical output to the JIT variant above.
func TestBinary_AOT_SchedulerDemo(t *testing.T) {
	exePath := aotCompile(t, "../../examples/scheduler_demo/scheduler_demo.llx")

	out, err := exec.Command(exePath).Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			t.Fatalf("%s exited %v, stderr:\n%s", exePath, err, ee.Stderr)
		}
		t.Fatalf("running %s: %v", exePath, err)
	}

	normalized := strings.ReplaceAll(string(out), "\r\n", "\n")
	normalized = strings.TrimRight(normalized, "\n")
	if normalized != schedulerDemoExampleWant {
		t.Errorf("stdout = %q, want %q", normalized, schedulerDemoExampleWant)
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

// --- -o (AOT compilation to a native executable) tests. Every "run the
// resulting binary directly" test below invokes the AOT-compiled .exe as a
// completely separate process, with llvmcPath nowhere in that particular
// exec.Command call - the real acceptance test for this feature: a
// genuinely standalone, deployable binary, not just "the pipeline emitted an
// object file".

// aotCompile shells out to the real llvmc binary with -o against srcPath,
// producing a fresh .exe in t.TempDir() and returning its path - failing the
// test immediately if llvmc itself reports a non-zero exit (a compile or
// link failure), since every test using this helper expects the AOT
// compilation step itself to succeed.
func aotCompile(t *testing.T, srcPath string) string {
	t.Helper()
	outPath := filepath.Join(t.TempDir(), "aot_out.exe")
	cmd := exec.Command(llvmcPath, "-o", outPath, srcPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("llvmc -o %s %s failed: %v\n%s", outPath, srcPath, err, out)
	}
	return outPath
}

// TestBinary_AOT_HelloWorld is the primary acceptance test for -o: compile
// examples/hello to a real .exe, then run that .exe directly (no llvmc, no
// Go/LLVM toolchain in the loop at all) and confirm it prints "Hello,
// World!" and exits 0 - a genuinely standalone, deployable binary.
func TestBinary_AOT_HelloWorld(t *testing.T) {
	exePath := aotCompile(t, "../../examples/hello/hello.llx")

	out, err := exec.Command(exePath).Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			t.Fatalf("%s exited %v, stderr:\n%s", exePath, err, ee.Stderr)
		}
		t.Fatalf("running %s: %v", exePath, err)
	}
	if got := strings.TrimRight(string(out), "\r\n"); got != "Hello, World!" {
		t.Errorf("stdout = %q, want %q", got, "Hello, World!")
	}
}

// TestBinary_AOT_Features AOT-compiles examples/features and confirms
// identical output/exit code to its JIT-executed behavior (see
// TestBinary_MainExitCode) - proving -o doesn't change ordinary program
// behavior, only how the result is produced/run.
func TestBinary_AOT_Features(t *testing.T) {
	exePath := aotCompile(t, "../../examples/features/features.llx")

	cmd := exec.Command(exePath)
	out, err := cmd.Output()
	if err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("running %s: %v", exePath, err)
		}
		if ee.ExitCode() != 30 {
			t.Fatalf("exit code = %d, want 30, stderr:\n%s", ee.ExitCode(), ee.Stderr)
		}
	} else {
		t.Fatal("expected the AOT binary to exit 30, got exit 0")
	}

	normalized := strings.ReplaceAll(string(out), "\r\n", "\n")
	normalized = strings.TrimRight(normalized, "\n")
	if normalized != "10\n30\n100" {
		t.Errorf("stdout = %q, want the lines 10, 30, 100", string(out))
	}
}

// TestBinary_AOT_MultiReturn AOT-compiles examples/multireturn and confirms
// identical output to its JIT-executed behavior (TestBinary_MultiReturnExample) -
// proving the multi-return aggregate-struct-return ABI (see CODEGEN.md's
// "Go-style multi-return values" section) round-trips correctly through a
// real, standalone linked executable, not just under JIT execution.
func TestBinary_AOT_MultiReturn(t *testing.T) {
	exePath := aotCompile(t, "../../examples/multireturn/multireturn.llx")

	out, err := exec.Command(exePath).Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			t.Fatalf("%s exited %v, stderr:\n%s", exePath, err, ee.Stderr)
		}
		t.Fatalf("running %s: %v", exePath, err)
	}

	normalized := strings.ReplaceAll(string(out), "\r\n", "\n")
	normalized = strings.TrimRight(normalized, "\n")
	want := "5\ndivision by zero\n2\nnot found"
	if normalized != want {
		t.Errorf("stdout = %q, want %q", normalized, want)
	}
}

// TestBinary_AOT_MultiAssign AOT-compiles examples/multi_assign and confirms
// identical output to its JIT-executed behavior
// (TestBinary_MultiAssignExample) - proving the swap idiom's own
// evaluate-then-store codegen ordering round-trips correctly through a real,
// standalone linked executable too, not just under JIT execution.
func TestBinary_AOT_MultiAssign(t *testing.T) {
	exePath := aotCompile(t, "../../examples/multi_assign/multi_assign.llx")

	out, err := exec.Command(exePath).Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			t.Fatalf("%s exited %v, stderr:\n%s", exePath, err, ee.Stderr)
		}
		t.Fatalf("running %s: %v", exePath, err)
	}

	normalized := strings.ReplaceAll(string(out), "\r\n", "\n")
	normalized = strings.TrimRight(normalized, "\n")
	if normalized != multiAssignWant {
		t.Errorf("stdout = %q, want %q", normalized, multiAssignWant)
	}
}

// TestBinary_AOT_Enums AOT-compiles examples/enums and confirms identical
// output/exit code to its JIT-executed behavior (TestBinary_EnumsExample) -
// proving the enum tagged-union representation, its real discriminant-switch
// `==`/print/`match` codegen, and the arena-allocated variant payload all
// round-trip correctly through a real, standalone linked executable, not
// just under JIT execution (the payload in particular must genuinely
// survive independent of any JIT-specific memory management).
func TestBinary_AOT_Enums(t *testing.T) {
	exePath := aotCompile(t, "../../examples/enums/enums.llx")

	cmd := exec.Command(exePath)
	out, err := cmd.Output()
	if err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("running %s: %v", exePath, err)
		}
		if ee.ExitCode() != 6 {
			t.Fatalf("%s exit code = %d, want 6, stderr:\n%s", exePath, ee.ExitCode(), ee.Stderr)
		}
	} else {
		t.Fatalf("expected %s to exit 6, got exit 0", exePath)
	}

	normalized := strings.ReplaceAll(string(out), "\r\n", "\n")
	normalized = strings.TrimRight(normalized, "\n")
	want := "12.566360\n" +
		"12.000000\n" +
		"0.000000\n" +
		"Circle(2.000000)\n" +
		"Rectangle(3.000000 4.000000)\n" +
		"Point\n" +
		"true\n" +
		"false\n" +
		"false\n" +
		"false\n" +
		"6"
	if normalized != want {
		t.Errorf("stdout = %q, want %q", normalized, want)
	}
}

// TestBinary_AOT_MatchValues AOT-compiles examples/match_values and confirms
// identical output/exit code to its JIT-executed behavior
// (TestBinary_MatchValuesExample) - proving genValueMatchStmt's own
// runtime-comparison-chain lowering (as opposed to genMatchStmt's own LLVM
// `switch` for an enum discriminant - see CODEGEN.md's "match codegen"
// section) round-trips correctly through a real, standalone linked
// executable, not just under JIT execution.
func TestBinary_AOT_MatchValues(t *testing.T) {
	exePath := aotCompile(t, "../../examples/match_values/match_values.llx")

	cmd := exec.Command(exePath)
	out, err := cmd.Output()
	if err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("running %s: %v", exePath, err)
		}
		if ee.ExitCode() != 5 {
			t.Fatalf("%s exit code = %d, want 5, stderr:\n%s", exePath, ee.ExitCode(), ee.Stderr)
		}
	} else {
		t.Fatalf("expected %s to exit 5, got exit 0", exePath)
	}

	normalized := strings.ReplaceAll(string(out), "\r\n", "\n")
	normalized = strings.TrimRight(normalized, "\n")
	if normalized != matchValuesWant {
		t.Errorf("stdout = %q, want %q", normalized, matchValuesWant)
	}
}

// TestBinary_AOT_MatchExpr AOT-compiles examples/match_expr and confirms
// identical output/exit code to its JIT-executed behavior
// (TestBinary_MatchExprExample) - proving genMatchExpr's own phi-based
// value-producing lowering (as opposed to genMatchStmt's own plain
// unreachable-or-fall-through merge point for the statement form - see
// CODEGEN.md's "match codegen" section) round-trips correctly through a
// real, standalone linked executable, not just under JIT execution.
func TestBinary_AOT_MatchExpr(t *testing.T) {
	exePath := aotCompile(t, "../../examples/match_expr/match_expr.llx")

	cmd := exec.Command(exePath)
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			t.Fatalf("%s exited %v, stderr:\n%s", exePath, err, ee.Stderr)
		}
		t.Fatalf("running %s: %v", exePath, err)
	}

	normalized := strings.ReplaceAll(string(out), "\r\n", "\n")
	normalized = strings.TrimRight(normalized, "\n")
	if normalized != matchExprWant {
		t.Errorf("stdout = %q, want %q", normalized, matchExprWant)
	}
}

// TestBinary_AOT_ExternFuncScopeTimer AOT-compiles examples/scope_timer -
// exercising the `extern func` FFI feature (LANGUAGE.md's "External
// functions (FFI)" section) - and confirms the resulting standalone .exe
// runs correctly on its own, proving extern-bound Win32 API calls
// (QueryPerformanceCounter/QueryPerformanceFrequency, kernel32.dll exports)
// link and resolve correctly through gcc's own linker at build time, not
// just the JIT's runtime process-symbol generator (see CODEGEN.md's
// "External functions (FFI)" section: these are two genuinely different
// resolution mechanisms, and this is the one test that actually exercises
// the link-time path). The elapsed tick counts scope_timer itself prints
// are inherently non-deterministic (real wall-clock-derived values), so this
// only asserts the exe ran to completion, exited cleanly, and printed the
// label/shape of output a successful run always produces - not exact tick
// counts.
func TestBinary_AOT_ExternFuncScopeTimer(t *testing.T) {
	exePath := aotCompile(t, "../../examples/scope_timer/scope_timer.llx")

	out, err := exec.Command(exePath).Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			t.Fatalf("%s exited %v, stderr:\n%s", exePath, err, ee.Stderr)
		}
		t.Fatalf("running %s: %v", exePath, err)
	}

	got := string(out)
	if !strings.Contains(got, "slowWork") {
		t.Errorf("stdout = %q, want it to contain the ScopeTimer destructor's \"slowWork\" label - extern QueryPerformanceCounter/Frequency calls must have resolved and run", got)
	}
	lines := strings.Split(strings.TrimRight(strings.ReplaceAll(got, "\r\n", "\n"), "\n"), "\n")
	if len(lines) != 4 {
		t.Errorf("stdout = %q, want exactly 4 lines (label, elapsed ticks, frequency, slowWork's own result)", got)
	}
}

// TestBinary_AOT_Args writes a small scratch program calling the args()
// builtin (see LANGUAGE.md's "The args() builtin" section), AOT-compiles it,
// and runs the resulting .exe directly with real command-line arguments -
// confirming args() sees the real OS argv (argv[0] the exe's own path,
// argv[1:] the trailing arguments) when actually AOT-compiled and invoked as
// a standalone process, distinct from (and a stronger guarantee than) the
// JIT-execution path's own documented empty-slice fallback (see
// TestArgsCallUnderJITReturnsEmptySlice, src/codegen/args_test.go).
func TestBinary_AOT_Args(t *testing.T) {
	src := `
func main() int {
	a := args()
	print(len(a))
	i := 0
	for i < len(a) {
		print(a[i])
		i++
	}
	return 0
}
`
	srcPath := filepath.Join(t.TempDir(), "args_prog.llx")
	if err := os.WriteFile(srcPath, []byte(src), 0o644); err != nil {
		t.Fatalf("writing temp source file: %v", err)
	}
	exePath := aotCompile(t, srcPath)

	out, err := exec.Command(exePath, "foo", "bar baz").Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			t.Fatalf("%s exited %v, stderr:\n%s", exePath, err, ee.Stderr)
		}
		t.Fatalf("running %s: %v", exePath, err)
	}

	normalized := strings.ReplaceAll(string(out), "\r\n", "\n")
	normalized = strings.TrimRight(normalized, "\n")
	lines := strings.Split(normalized, "\n")
	if len(lines) != 4 {
		t.Fatalf("stdout = %q, want 4 lines (len, then 3 argv elements)", string(out))
	}
	if lines[0] != "3" {
		t.Errorf("len(args()) = %q, want \"3\" (exe path + 2 trailing args)", lines[0])
	}
	if lines[1] != exePath {
		t.Errorf("args()[0] = %q, want the exe's own path %q", lines[1], exePath)
	}
	if lines[2] != "foo" {
		t.Errorf("args()[1] = %q, want %q", lines[2], "foo")
	}
	if lines[3] != "bar baz" {
		t.Errorf("args()[2] = %q, want %q", lines[3], "bar baz")
	}
}

// TestRun_EmitLLVMAndOutputMutuallyExclusive covers run's own usage-error
// check: -emit-llvm and -o together is a usage error (exitUsage), not a
// silent "one wins" - see the package doc comment's exit-code writeup.
func TestRun_EmitLLVMAndOutputMutuallyExclusive(t *testing.T) {
	var stderr bytes.Buffer
	code := run([]string{"-emit-llvm", "-o", filepath.Join(t.TempDir(), "out.exe"), "../../examples/hello/hello.llx"}, &stderr)
	if code != exitUsage {
		t.Errorf("exit code = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(stderr.String(), "mutually exclusive") {
		t.Errorf("stderr = %q, want it to mention -emit-llvm/-o being mutually exclusive", stderr.String())
	}
}

// TestRun_LinkFlagsWithEmitLLVM covers -l/-L with -emit-llvm: those flags
// only feed AOT linking or JIT library generators, so using them when only
// dumping IR is a usage error.
func TestRun_LinkFlagsWithEmitLLVM(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"-l with -emit-llvm", []string{"-emit-llvm", "-l", "m", "../../examples/hello/hello.llx"}},
		{"-L with -emit-llvm", []string{"-emit-llvm", "-L", ".", "../../examples/hello/hello.llx"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stderr bytes.Buffer
			code := run(tc.args, &stderr)
			if code != exitUsage {
				t.Errorf("exit code = %d, want %d, stderr:\n%s", code, exitUsage, stderr.String())
			}
			if !strings.Contains(stderr.String(), "-l and -L cannot be used with -emit-llvm") {
				t.Errorf("stderr = %q, want it to mention -l/-L vs -emit-llvm", stderr.String())
			}
		})
	}
}

// TestBinary_JIT_LinkLibStatic JIT-runs a program that calls into a tiny
// static C library via -L/-l - the acceptance path for third-party .a under
// LLJIT (NewStaticLibrarySearchGeneratorForPath).
func TestBinary_JIT_LinkLibStatic(t *testing.T) {
	dir := t.TempDir()
	cPath := filepath.Join(dir, "addone.c")
	if err := os.WriteFile(cPath, []byte("int add_one(int x) { return x + 1; }\n"), 0o644); err != nil {
		t.Fatalf("writing C source: %v", err)
	}
	objPath := filepath.Join(dir, "addone.o")
	if out, err := exec.Command("gcc", "-c", cPath, "-o", objPath).CombinedOutput(); err != nil {
		t.Fatalf("compiling C object: %v\n%s", err, out)
	}
	libPath := filepath.Join(dir, "libaddone.a")
	if out, err := exec.Command("ar", "rcs", libPath, objPath).CombinedOutput(); err != nil {
		t.Fatalf("creating static lib: %v\n%s", err, out)
	}

	srcPath := filepath.Join(dir, "main.llx")
	src := "extern func add_one(x i32) i32\n" +
		"func main() int {\n" +
		"\treturn add_one(41)\n" +
		"}\n"
	if err := os.WriteFile(srcPath, []byte(src), 0o644); err != nil {
		t.Fatalf("writing llx source: %v", err)
	}

	cmd := exec.Command(llvmcPath, "-L", dir, "-l", "addone", srcPath)
	if err := cmd.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			if ee.ExitCode() != 42 {
				t.Fatalf("llvmc exit code = %d, want 42\nstderr:\n%s", ee.ExitCode(), ee.Stderr)
			}
			return
		}
		t.Fatalf("running llvmc: %v", err)
	}
	t.Fatal("llvmc exited 0, want 42")
}

// TestBinary_JIT_LinkLibDLL is TestBinary_JIT_LinkLibStatic's shared-library
// counterpart (NewDynamicLibrarySearchGeneratorForPath).
func TestBinary_JIT_LinkLibDLL(t *testing.T) {
	dir := t.TempDir()
	cPath := filepath.Join(dir, "addone.c")
	if err := os.WriteFile(cPath, []byte("int add_one(int x) { return x + 1; }\n"), 0o644); err != nil {
		t.Fatalf("writing C source: %v", err)
	}
	dllPath := filepath.Join(dir, "addone.dll")
	if out, err := exec.Command("gcc", "-shared", "-o", dllPath, cPath).CombinedOutput(); err != nil {
		t.Fatalf("compiling DLL: %v\n%s", err, out)
	}

	srcPath := filepath.Join(dir, "main.llx")
	src := "extern func add_one(x i32) i32\n" +
		"func main() int {\n" +
		"\treturn add_one(41)\n" +
		"}\n"
	if err := os.WriteFile(srcPath, []byte(src), 0o644); err != nil {
		t.Fatalf("writing llx source: %v", err)
	}

	cmd := exec.Command(llvmcPath, "-L", dir, "-l", "addone", srcPath)
	if err := cmd.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			if ee.ExitCode() != 42 {
				t.Fatalf("llvmc exit code = %d, want 42\nstderr:\n%s", ee.ExitCode(), ee.Stderr)
			}
			return
		}
		t.Fatalf("running llvmc: %v", err)
	}
	t.Fatal("llvmc exited 0, want 42")
}

// TestRun_JIT_MissingLibrary covers -l resolution failure under JIT: clear
// compile-error exit, not a panic, with the missing lib named on stderr.
func TestRun_JIT_MissingLibrary(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "main.llx")
	src := "extern func add_one(x i32) i32\n" +
		"func main() int {\n" +
		"\treturn add_one(41)\n" +
		"}\n"
	if err := os.WriteFile(srcPath, []byte(src), 0o644); err != nil {
		t.Fatalf("writing llx source: %v", err)
	}

	var stderr bytes.Buffer
	code := run([]string{"-L", dir, "-l", "addone", srcPath}, &stderr)
	if code != exitCompile {
		t.Errorf("exit code = %d, want %d, stderr:\n%s", code, exitCompile, stderr.String())
	}
	got := stderr.String()
	if !strings.Contains(got, "addone") || !strings.Contains(got, "not found") {
		t.Errorf("stderr = %q, want missing-library diagnostic naming addone", got)
	}
}

// TestBinary_AOT_LinkLib builds a tiny static C library, AOT-compiles a
// program that calls it via extern func, and links with -L/-l - the
// acceptance path for third-party C libs that are not on mingw's default
// import-lib set.
func TestBinary_AOT_LinkLib(t *testing.T) {
	dir := t.TempDir()
	cPath := filepath.Join(dir, "addone.c")
	if err := os.WriteFile(cPath, []byte("int add_one(int x) { return x + 1; }\n"), 0o644); err != nil {
		t.Fatalf("writing C source: %v", err)
	}
	objPath := filepath.Join(dir, "addone.o")
	if out, err := exec.Command("gcc", "-c", cPath, "-o", objPath).CombinedOutput(); err != nil {
		t.Fatalf("compiling C object: %v\n%s", err, out)
	}
	libPath := filepath.Join(dir, "libaddone.a")
	if out, err := exec.Command("ar", "rcs", libPath, objPath).CombinedOutput(); err != nil {
		t.Fatalf("creating static lib: %v\n%s", err, out)
	}

	srcPath := filepath.Join(dir, "main.llx")
	src := "extern func add_one(x i32) i32\n" +
		"func main() int {\n" +
		"\treturn add_one(41)\n" +
		"}\n"
	if err := os.WriteFile(srcPath, []byte(src), 0o644); err != nil {
		t.Fatalf("writing llx source: %v", err)
	}

	exePath := filepath.Join(dir, "out.exe")
	cmd := exec.Command(llvmcPath, "-o", exePath, "-L", dir, "-l", "addone", srcPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("llvmc -o with -L/-l: %v\n%s", err, out)
	}

	run := exec.Command(exePath)
	if err := run.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			if ee.ExitCode() != 42 {
				t.Fatalf("exe exit code = %d, want 42", ee.ExitCode())
			}
			return
		}
		t.Fatalf("running exe: %v", err)
	}
	t.Fatal("exe exited 0, want 42")
}

// TestBinary_AOT_LinkLibStructByValue is struct-by-value FFI's real
// end-to-end proof (see LANGUAGE.md's "External functions (FFI)" section
// and DECISIONS.md's dated entry): a tiny static C library takes and
// returns `struct point { int x; int y; }` by value - the exact real C ABI
// shape sema's isFFISafeType now allows crossing an extern signature -
// linked the same -L/-l way TestBinary_AOT_LinkLib already proves for a
// scalar param. If LLVM's own Win64 ABI lowering (driven by the module's
// pinned DataLayout/triple - see finishPipeline) didn't handle aggregate
// pass/return correctly for a real C call, this would link fine but return
// the wrong value (or crash) at run time, not fail to compile - exactly why
// this needs a real run, not just an IR-shape assertion.
func TestBinary_AOT_LinkLibStructByValue(t *testing.T) {
	dir := t.TempDir()
	cPath := filepath.Join(dir, "point.c")
	cSrc := "struct point { int x; int y; };\n" +
		"struct point make_point(int x, int y) {\n" +
		"\tstruct point p;\n" +
		"\tp.x = x;\n" +
		"\tp.y = y;\n" +
		"\treturn p;\n" +
		"}\n" +
		"int point_sum(struct point p) {\n" +
		"\treturn p.x + p.y;\n" +
		"}\n"
	if err := os.WriteFile(cPath, []byte(cSrc), 0o644); err != nil {
		t.Fatalf("writing C source: %v", err)
	}
	objPath := filepath.Join(dir, "point.o")
	if out, err := exec.Command("gcc", "-c", cPath, "-o", objPath).CombinedOutput(); err != nil {
		t.Fatalf("compiling C object: %v\n%s", err, out)
	}
	libPath := filepath.Join(dir, "libpoint.a")
	if out, err := exec.Command("ar", "rcs", libPath, objPath).CombinedOutput(); err != nil {
		t.Fatalf("creating static lib: %v\n%s", err, out)
	}

	srcPath := filepath.Join(dir, "main.llx")
	src := "struct Point {\n" +
		"\tx int\n" +
		"\ty int\n" +
		"}\n" +
		"extern func make_point(x int, y int) Point\n" +
		"extern func point_sum(p Point) int\n" +
		"func main() int {\n" +
		"\tp := make_point(19, 23)\n" +
		"\treturn point_sum(p)\n" +
		"}\n"
	if err := os.WriteFile(srcPath, []byte(src), 0o644); err != nil {
		t.Fatalf("writing llx source: %v", err)
	}

	exePath := filepath.Join(dir, "out.exe")
	cmd := exec.Command(llvmcPath, "-o", exePath, "-L", dir, "-l", "point", srcPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("llvmc -o with -L/-l: %v\n%s", err, out)
	}

	run := exec.Command(exePath)
	if err := run.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			if ee.ExitCode() != 42 {
				t.Fatalf("exe exit code = %d, want 42 (19+23)", ee.ExitCode())
			}
			return
		}
		t.Fatalf("running exe: %v", err)
	}
	t.Fatal("exe exited 0, want 42")
}

// TestBinary_AOT_LinkLibLargeStructByValue is
// TestBinary_AOT_LinkLibStructByValue's counterpart for the "otherwise pass
// by reference" half of the Windows x64 aggregate ABI (see ffi.go's
// externReturnType and this test's own struct: 3 ints = 12 bytes, neither
// 8 nor any other power-of-two size that qualifies for integer coercion) -
// a real gcc-compiled callee expecting a hidden sret return slot and an
// indirect (pointer) parameter, exactly what declareExternFuncSignature now
// emits for this shape.
func TestBinary_AOT_LinkLibLargeStructByValue(t *testing.T) {
	dir := t.TempDir()
	cPath := filepath.Join(dir, "triple.c")
	cSrc := "struct triple { int x; int y; int z; };\n" +
		"struct triple make_triple(int x, int y, int z) {\n" +
		"\tstruct triple t;\n" +
		"\tt.x = x;\n" +
		"\tt.y = y;\n" +
		"\tt.z = z;\n" +
		"\treturn t;\n" +
		"}\n" +
		"int triple_sum(struct triple t) {\n" +
		"\treturn t.x + t.y + t.z;\n" +
		"}\n"
	if err := os.WriteFile(cPath, []byte(cSrc), 0o644); err != nil {
		t.Fatalf("writing C source: %v", err)
	}
	objPath := filepath.Join(dir, "triple.o")
	if out, err := exec.Command("gcc", "-c", cPath, "-o", objPath).CombinedOutput(); err != nil {
		t.Fatalf("compiling C object: %v\n%s", err, out)
	}
	libPath := filepath.Join(dir, "libtriple.a")
	if out, err := exec.Command("ar", "rcs", libPath, objPath).CombinedOutput(); err != nil {
		t.Fatalf("creating static lib: %v\n%s", err, out)
	}

	srcPath := filepath.Join(dir, "main.llx")
	src := "struct Triple {\n" +
		"\tx int\n" +
		"\ty int\n" +
		"\tz int\n" +
		"}\n" +
		"extern func make_triple(x int, y int, z int) Triple\n" +
		"extern func triple_sum(t Triple) int\n" +
		"func main() int {\n" +
		"\tt := make_triple(10, 15, 17)\n" +
		"\treturn triple_sum(t)\n" +
		"}\n"
	if err := os.WriteFile(srcPath, []byte(src), 0o644); err != nil {
		t.Fatalf("writing llx source: %v", err)
	}

	exePath := filepath.Join(dir, "out.exe")
	cmd := exec.Command(llvmcPath, "-o", exePath, "-L", dir, "-l", "triple", srcPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("llvmc -o with -L/-l: %v\n%s", err, out)
	}

	run := exec.Command(exePath)
	if err := run.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			if ee.ExitCode() != 42 {
				t.Fatalf("exe exit code = %d, want 42 (10+15+17)", ee.ExitCode())
			}
			return
		}
		t.Fatalf("running exe: %v", err)
	}
	t.Fatal("exe exited 0, want 42")
}

// TestBinary_AOT_LinkLibCFuncCallback is cfunc's real end-to-end proof (see
// LANGUAGE.md's "External functions (FFI)" section): a tiny static C
// library takes a real C function pointer and calls it itself, exactly the
// motivating "pass a callback to a C API" shape cfunc exists for - linked
// the same -L/-l way TestBinary_AOT_LinkLib already proves for a plain
// scalar param. The language side passes a top-level func by name
// (converted to cfunc via checkFuncToCFuncConversion, sema/typecheck.go) -
// if genCFuncCall's bare-pointer, no-ctxPtr calling convention (codegen/
// expr.go) didn't genuinely match a real C function pointer's own calling
// convention, this would link fine but crash or return garbage at run time,
// not fail to compile.
func TestBinary_AOT_LinkLibCFuncCallback(t *testing.T) {
	dir := t.TempDir()
	cPath := filepath.Join(dir, "apply.c")
	cSrc := "int apply_callback(int (*cb)(int), int x) {\n" +
		"\treturn cb(x);\n" +
		"}\n"
	if err := os.WriteFile(cPath, []byte(cSrc), 0o644); err != nil {
		t.Fatalf("writing C source: %v", err)
	}
	objPath := filepath.Join(dir, "apply.o")
	if out, err := exec.Command("gcc", "-c", cPath, "-o", objPath).CombinedOutput(); err != nil {
		t.Fatalf("compiling C object: %v\n%s", err, out)
	}
	libPath := filepath.Join(dir, "libapply.a")
	if out, err := exec.Command("ar", "rcs", libPath, objPath).CombinedOutput(); err != nil {
		t.Fatalf("creating static lib: %v\n%s", err, out)
	}

	srcPath := filepath.Join(dir, "main.llx")
	src := "extern func apply_callback(cb cfunc(int) int, x int) int\n" +
		"func double(x int) int {\n" +
		"\treturn x * 2\n" +
		"}\n" +
		"func main() int {\n" +
		"\treturn apply_callback(double, 21)\n" +
		"}\n"
	if err := os.WriteFile(srcPath, []byte(src), 0o644); err != nil {
		t.Fatalf("writing llx source: %v", err)
	}

	exePath := filepath.Join(dir, "out.exe")
	cmd := exec.Command(llvmcPath, "-o", exePath, "-L", dir, "-l", "apply", srcPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("llvmc -o with -L/-l: %v\n%s", err, out)
	}

	run := exec.Command(exePath)
	if err := run.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			if ee.ExitCode() != 42 {
				t.Fatalf("exe exit code = %d, want 42 (double(21))", ee.ExitCode())
			}
			return
		}
		t.Fatalf("running exe: %v", err)
	}
	t.Fatal("exe exited 0, want 42")
}

// TestBinary_AOT_LinkLibCFuncStructField is the regression for cfunc-as-
// struct-field FFI (LANGUAGE.md): Handlers{onClick cfunc(int) int} must be
// accepted by isFFISafeStructField and sized by abiSizeAlign as an 8-byte
// pointer (coerced to i64 on Win64), then round-trip through a real gcc-
// linked C struct containing a function pointer. Without both fixes this
// either failed at sema or panicked in abiSizeAlign.
func TestBinary_AOT_LinkLibCFuncStructField(t *testing.T) {
	dir := t.TempDir()
	cPath := filepath.Join(dir, "handlers.c")
	cSrc := "typedef struct {\n" +
		"\tint (*onClick)(int);\n" +
		"} Handlers;\n" +
		"int register_handlers(Handlers h, int x) {\n" +
		"\treturn h.onClick(x);\n" +
		"}\n"
	if err := os.WriteFile(cPath, []byte(cSrc), 0o644); err != nil {
		t.Fatalf("writing C source: %v", err)
	}
	objPath := filepath.Join(dir, "handlers.o")
	if out, err := exec.Command("gcc", "-c", cPath, "-o", objPath).CombinedOutput(); err != nil {
		t.Fatalf("compiling C object: %v\n%s", err, out)
	}
	libPath := filepath.Join(dir, "libhandlers.a")
	if out, err := exec.Command("ar", "rcs", libPath, objPath).CombinedOutput(); err != nil {
		t.Fatalf("creating static lib: %v\n%s", err, out)
	}

	srcPath := filepath.Join(dir, "main.llx")
	src := "struct Handlers {\n" +
		"\tonClick cfunc(int) int\n" +
		"}\n" +
		"extern func register_handlers(h Handlers, x int) int\n" +
		"func double(x int) int {\n" +
		"\treturn x * 2\n" +
		"}\n" +
		"func main() int {\n" +
		"\th := Handlers{double}\n" +
		"\treturn register_handlers(h, 21)\n" +
		"}\n"
	if err := os.WriteFile(srcPath, []byte(src), 0o644); err != nil {
		t.Fatalf("writing llx source: %v", err)
	}

	exePath := filepath.Join(dir, "out.exe")
	cmd := exec.Command(llvmcPath, "-o", exePath, "-L", dir, "-l", "handlers", srcPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("llvmc -o with -L/-l: %v\n%s", err, out)
	}

	run := exec.Command(exePath)
	if err := run.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			if ee.ExitCode() != 42 {
				t.Fatalf("exe exit code = %d, want 42 (double(21))", ee.ExitCode())
			}
			return
		}
		t.Fatalf("running exe: %v", err)
	}
	t.Fatal("exe exited 0, want 42")
}

// TestRun_OutputLinkFailureIsCompileError covers -o's own link-failure exit
// code (see the package doc comment): an output path inside a directory that
// doesn't exist makes gcc's own link step fail - this must surface as
// exitCompile with gcc's own error text on stderr, never a panic.
func TestRun_OutputLinkFailureIsCompileError(t *testing.T) {
	var stderr bytes.Buffer
	badOutput := filepath.Join(t.TempDir(), "does-not-exist", "out.exe")
	code := run([]string{"-o", badOutput, "../../examples/hello/hello.llx"}, &stderr)
	if code != exitCompile {
		t.Errorf("exit code = %d, want %d, stderr:\n%s", code, exitCompile, stderr.String())
	}
	if stderr.Len() == 0 {
		t.Error("expected a link-failure message on stderr, got none")
	}
}

// TestRun_OutputMissingGCCIsCompileError covers -o's link step when gcc
// isn't available on PATH at all (see AGENTS.md's "Compiling" section for
// why mingw64's gcc/g++ must normally be on PATH) - a real coverage gap a
// code review flagged: the actual behavior was traced by hand and found
// already correct (a clear, diagnostic-shaped message naming the missing
// "gcc" executable and the linking step, not a confusing crash/panic), so
// this backs that finding with a real test rather than leaving it
// unverified. Temporarily overrides PATH to a fresh, empty directory - t.Setenv
// restores the real PATH automatically once this test ends, exactly like
// every other env-var-scoped test in this file already relies on.
func TestRun_OutputMissingGCCIsCompileError(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	var stderr bytes.Buffer
	outPath := filepath.Join(t.TempDir(), "out.exe")
	code := run([]string{"-o", outPath, "../../examples/hello/hello.llx"}, &stderr)

	if code != exitCompile {
		t.Errorf("exit code = %d, want %d, stderr:\n%s", code, exitCompile, stderr.String())
	}
	got := stderr.String()
	if !strings.Contains(got, `"gcc"`) {
		t.Errorf("stderr = %q, want it to mention the missing \"gcc\" executable", got)
	}
	if !strings.Contains(got, "linking") {
		t.Errorf("stderr = %q, want it to mention the linking step", got)
	}
	if strings.Contains(got, "panic:") {
		t.Errorf("stderr = %q, want no Go panic - a missing gcc must be a clean diagnostic", got)
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
	p.translate(10, 20)
	return double(p.x) + p.y
}
`},
		{Name: "point.llx", Src: `
struct Point {
	x int
	y int
}

func (Point) translate(dx int, dy int) {
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

// --- Cross-package import tests (see LANGUAGE.md's "Imports" section) -
// loader.LoadProgram discovers/parses/dedups/cycle-checks the whole
// transitive import graph, and compileAndRunProgram drives it through the
// rest of the pipeline as one shared Module.

// TestBinary_ImportsExample shells out to the real llvmc binary against
// examples/imports/app - a real two-package program (app imports the
// sibling mathutils package - see that directory's own doc comments) - both
// as a directory and as one of its files, exactly like
// TestBinary_MultiFileDirectory does one level down.
func TestBinary_ImportsExample(t *testing.T) {
	const wantStdout = "30\n15"
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

	t.Run("directory", func(t *testing.T) { runAndCheck(t, "../../examples/imports/app") })
	t.Run("file", func(t *testing.T) { runAndCheck(t, "../../examples/imports/app/main.llx") })
}

// TestCompileAndRunProgram_CrossPackageImports drives compileAndRunProgram
// in-process (via loader.LoadProgram over an afero.MemMapFs, so no real
// filesystem is involved) against a two-package program - proving the
// cmd/llvmc <-> loader <-> src/compiler <-> sema bridging
// (compileAndRunProgram, compiler.CompileProgram, its shared finishPipeline
// tail) works end to end, not just the individually-tested pieces.
func TestCompileAndRunProgram_CrossPackageImports(t *testing.T) {
	fs := afero.NewMemMapFs()
	sep := string(filepath.Separator)
	writeFile := func(path, src string) {
		if err := afero.WriteFile(fs, path, []byte(src), 0o644); err != nil {
			t.Fatalf("writing %s: %v", path, err)
		}
	}
	writeFile(filepath.Join(sep, "prog", "mathutils", "add.llx"), `
func Add(a int, b int) int {
	return a + b
}
`)
	writeFile(filepath.Join(sep, "prog", "app", "main.llx"), `
import "../mathutils"

func main() int {
	return mathutils.Add(2, 3)
}
`)

	prog, err := loader.LoadProgram(fs, filepath.Join(sep, "prog", "app"))
	if err != nil {
		t.Fatalf("LoadProgram: %v", err)
	}

	var stderr bytes.Buffer
	code := compileAndRunProgram(prog, &stderr, false)
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want empty", stderr.String())
	}
	if code != 5 {
		t.Errorf("exit code = %d, want 5", code)
	}
}

// TestCompileAndRunProgram_UnexportedNameIsCompileError covers the failure
// path: a cross-package reference to an unexported name must stop the
// pipeline with exitCompile and a diagnostic mentioning it, never panic or
// silently succeed.
func TestCompileAndRunProgram_UnexportedNameIsCompileError(t *testing.T) {
	fs := afero.NewMemMapFs()
	sep := string(filepath.Separator)
	writeFile := func(path, src string) {
		if err := afero.WriteFile(fs, path, []byte(src), 0o644); err != nil {
			t.Fatalf("writing %s: %v", path, err)
		}
	}
	writeFile(filepath.Join(sep, "prog", "mathutils", "add.llx"), `
func add(a int, b int) int {
	return a + b
}
`)
	writeFile(filepath.Join(sep, "prog", "app", "main.llx"), `
import "../mathutils"

func main() int {
	return mathutils.add(2, 3)
}
`)

	prog, err := loader.LoadProgram(fs, filepath.Join(sep, "prog", "app"))
	if err != nil {
		t.Fatalf("LoadProgram: %v", err)
	}

	var stderr bytes.Buffer
	code := compileAndRunProgram(prog, &stderr, false)
	if code != exitCompile {
		t.Errorf("exit code = %d, want %d", code, exitCompile)
	}
	if !strings.Contains(stderr.String(), "not exported") {
		t.Errorf("stderr = %q, want it to mention the unexported name", stderr.String())
	}
}
