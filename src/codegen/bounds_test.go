package codegen

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestArrayIndexValidRangeStillWorks is the "no false positive" half of
// bounds checking (see genBoundsCheck, expr.go): every in-range index into a
// fixed-size array must still load/store exactly as before - the new
// runtime check must never itself reject a legitimate access.
func TestArrayIndexValidRangeStillWorks(t *testing.T) {
	jm := compileAndJIT(t, `
func arrayGet(i int) int {
	a := [5]int{10, 20, 30, 40, 50}
	return a[i]
}

func arraySet(i int, v int) int {
	a := [5]int{10, 20, 30, 40, 50}
	a[i] = v
	return a[i]
}
`)

	want := []int32{10, 20, 30, 40, 50}
	for i, w := range want {
		if got := jm.runInt32(t, "arrayGet", int32(i)); got != w {
			t.Errorf("arrayGet(%d) = %d, want %d", i, got, w)
		}
	}
	if got := jm.runInt32(t, "arraySet", 2, 99); got != 99 {
		t.Errorf("arraySet(2, 99) = %d, want 99", got)
	}
}

// TestOutOfBoundsIndexTraps is the "actually traps" half of bounds checking.
// JIT-executing a genuinely out-of-range index is *supposed* to crash the
// process (llvm.trap + unreachable - see genBoundsCheck, expr.go) - doing
// that directly in this test's own goroutine would take the whole `go test`
// binary down with it, so this instead re-execs the test binary itself as a
// child process (the same GO_WANT_HELPER_PROCESS pattern os/exec's own test
// suite uses), asserts the *child* crashes (an abnormal, non-zero/signal
// exit - never a clean os.Exit(0) or a normal test failure), and lets the
// parent process (the actual `go test` run) observe that from the outside,
// unharmed either way.
func TestOutOfBoundsIndexTraps(t *testing.T) {
	const childEnv = "LLVM_LANG_BOUNDS_TRAP_CHILD"
	const idxEnv = "LLVM_LANG_BOUNDS_TRAP_INDEX"

	if os.Getenv(childEnv) == "1" {
		idx := os.Getenv(idxEnv)
		jm := compileAndJIT(t, fmt.Sprintf(`
func trap() int {
	a := [5]int{1, 2, 3, 4, 5}
	return a[%s]
}
`, idx))
		// If bounds checking works, this call never returns - the process
		// aborts inside the JIT-compiled trap block. Reaching the line after
		// it (a clean process exit) is exactly the failure this test is
		// designed to catch, from the parent's side.
		jm.runInt32(t, "trap")
		return
	}

	for _, idx := range []string{"-1", "5"} {
		t.Run("index_"+idx, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestOutOfBoundsIndexTraps$")
			cmd.Env = append(os.Environ(),
				childEnv+"=1",
				idxEnv+"="+idx,
			)
			out, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("index %s: expected the child process to crash (llvm.trap), but it exited cleanly - output:\n%s", idx, out)
			}
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) {
				t.Fatalf("index %s: expected an *exec.ExitError from the crashing child, got %v (%T) - output:\n%s", idx, err, err, out)
			}
			if exitErr.ExitCode() == 0 {
				t.Fatalf("index %s: expected an abnormal (crash) exit, got a clean exit code 0 - output:\n%s", idx, out)
			}
			// See CODEGEN.md's "Runtime trap diagnostics" section: an
			// informative message (the actual index/size involved) must
			// print before the abort, not just a bare crash.
			want := fmt.Sprintf("runtime error: index %s out of range [0:5)", idx)
			if !strings.Contains(string(out), want) {
				t.Errorf("index %s: expected trap message to contain %q, got:\n%s", idx, want, out)
			}
		})
	}
}
