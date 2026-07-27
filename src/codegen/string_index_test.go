package codegen

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// --- single-indexing a string (`s[i]` - see LANGUAGE.md's "Slicing"
// section): genAddr's new TypeString branch (expr.go) - a real byte read,
// bounds-checked exactly like an array element (genBoundsCheck, shared). ---

// TestStringIndexReadsRealBytesJIT covers the actual byte values read back,
// not just "it compiles" - a real ASCII string indexed at every position.
func TestStringIndexReadsRealBytesJIT(t *testing.T) {
	jm := compileAndJIT(t, `
func getByte(i int) int {
	s := "ABCDE"
	return i32(s[i])
}
`)
	want := []int32{'A', 'B', 'C', 'D', 'E'}
	for i, w := range want {
		if got := jm.runInt32(t, "getByte", int32(i)); got != w {
			t.Errorf("getByte(%d) = %d, want %d", i, got, w)
		}
	}
}

// TestStringIndexOutOfRangeTraps mirrors TestOutOfBoundsIndexTraps
// (bounds_test.go) for a string operand - same genBoundsCheck lowering,
// reached through genAddr's new TypeString branch instead of the fixed-array
// one. See that test's own doc comment for why this re-execs the test binary
// as a child process rather than crashing this one directly.
func TestStringIndexOutOfRangeTraps(t *testing.T) {
	const childEnv = "LLVM_LANG_STRING_INDEX_TRAP_CHILD"
	const idxEnv = "LLVM_LANG_STRING_INDEX_TRAP_INDEX"

	if os.Getenv(childEnv) == "1" {
		idx := os.Getenv(idxEnv)
		jm := compileAndJIT(t, fmt.Sprintf(`
func trap() int {
	s := "hello"
	return i32(s[%s])
}
`, idx))
		jm.runInt32(t, "trap")
		return
	}

	for _, idx := range []string{"-1", "5"} {
		t.Run("index_"+idx, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestStringIndexOutOfRangeTraps$")
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
			want := fmt.Sprintf("runtime error: index %s out of range [0:5)", idx)
			if !strings.Contains(string(out), want) {
				t.Errorf("index %s: expected trap message to contain %q, got:\n%s", idx, want, out)
			}
		})
	}
}
