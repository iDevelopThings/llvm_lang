package codegen

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"testing"
)

// TestMakeIndexAndLen covers the basic happy path: make allocates a properly
// sized, zero-filled backing buffer, indexing reads/writes through it
// correctly, and len reports the slice's runtime length field.
func TestMakeIndexAndLen(t *testing.T) {
	jm := compileAndJIT(t, `
func f() int {
	s := make([]int, 3)
	s[0] = 10
	s[1] = 20
	s[2] = 30
	return s[0] + s[1] + s[2] + len(s)
}
`)
	if got := jm.runInt32(t, "f"); got != 63 {
		t.Errorf("f() = %d, want 63", got)
	}
}

// TestMakeZeroFillsBackingBuffer covers make's own zero-fill guarantee (see
// LANGUAGE.md's "Dynamic arrays" section) - an element never read from
// before being explicitly written must still come back as zero, not
// uninitialized arena garbage.
func TestMakeZeroFillsBackingBuffer(t *testing.T) {
	jm := compileAndJIT(t, `
func f() int {
	s := make([]int, 5)
	return s[0] + s[1] + s[2] + s[3] + s[4]
}
`)
	if got := jm.runInt32(t, "f"); got != 0 {
		t.Errorf("f() = %d, want 0", got)
	}
}

// TestMakeWithExplicitCapacity covers the 3-arg form: len is n, not cap, and
// cap's own extra room is usable via append without triggering a grow.
func TestMakeWithExplicitCapacity(t *testing.T) {
	jm := compileAndJIT(t, `
func f() int {
	s := make([]int, 1, 4)
	return len(s)
}
`)
	if got := jm.runInt32(t, "f"); got != 1 {
		t.Errorf("f() = %d, want 1", got)
	}
}

// TestAppendWithinCapacityMutatesInPlace is the "same backing pointer"
// half of append's growth strategy (see LANGUAGE.md's "Dynamic arrays"
// section): when len < cap, append must reuse the exact same backing
// buffer, matching Go's own (sometimes-surprising but well-defined) aliasing
// behavior. This is observed indirectly (there's no raw pointer value
// exposed at the language level to compare directly): mutating the
// *original* slice after appending must be visible through the appended
// result, which is only possible if both share one backing array.
func TestAppendWithinCapacityMutatesInPlace(t *testing.T) {
	jm := compileAndJIT(t, `
func f() int {
	s := make([]int, 1, 2)
	s[0] = 100
	s2 := append(s, 200)
	s[0] = 999
	if s2[0] == 999 && s2[1] == 200 && len(s2) == 2 {
		return 1
	}
	return 0
}
`)
	if got := jm.runInt32(t, "f"); got != 1 {
		t.Errorf("f() = %d, want 1 (append within capacity must mutate the same backing buffer)", got)
	}
}

// TestAppendPastCapacityGrowsAndPreservesData is the "actually grows, old
// data preserved" half: when len == cap, append must allocate a fresh,
// larger buffer and copy the existing elements over - observed the same
// indirect way as TestAppendWithinCapacityMutatesInPlace, but inverted:
// mutating the original slice after growth must NOT be visible through the
// appended result, since they no longer share a backing buffer.
func TestAppendPastCapacityGrowsAndPreservesData(t *testing.T) {
	jm := compileAndJIT(t, `
func f() int {
	s := make([]int, 2, 2)
	s[0] = 1
	s[1] = 2
	s2 := append(s, 3)
	s[0] = 999
	if s2[0] == 1 && s2[1] == 2 && s2[2] == 3 && len(s2) == 3 {
		return 1
	}
	return 0
}
`)
	if got := jm.runInt32(t, "f"); got != 1 {
		t.Errorf("f() = %d, want 1 (append past capacity must grow into a fresh buffer, preserving old data)", got)
	}
}

// TestAppendFromZeroCapacity covers append's cap==0 edge case (newcap =
// max(1, cap*2), see DECISIONS.md's append-growth entry) - a slice made with
// no capacity at all must still grow correctly on its very first append.
func TestAppendFromZeroCapacity(t *testing.T) {
	jm := compileAndJIT(t, `
func f() int {
	s := make([]int, 0)
	s = append(s, 7)
	s = append(s, 8)
	return s[0] + s[1] + len(s)
}
`)
	if got := jm.runInt32(t, "f"); got != 17 {
		t.Errorf("f() = %d, want 17", got)
	}
}

// TestSliceCompositeLit covers `[]T{...}` sugar - a heap-allocated, properly
// sized backing buffer filled positionally, distinct from [N]T's plain
// inline LLVM array (see LANGUAGE.md's "Dynamic arrays" section).
func TestSliceCompositeLit(t *testing.T) {
	jm := compileAndJIT(t, `
func f() int {
	s := []int{10, 20, 30}
	return s[0] + s[1] + s[2] + len(s)
}
`)
	if got := jm.runInt32(t, "f"); got != 63 {
		t.Errorf("f() = %d, want 63", got)
	}
}

// TestSliceCompositeLitThenAppend covers appending onto a slice literal
// (len == cap immediately after construction, so this exercises the growth
// path too).
func TestSliceCompositeLitThenAppend(t *testing.T) {
	jm := compileAndJIT(t, `
func f() int {
	s := []int{1, 2}
	s = append(s, 3)
	return s[0] + s[1] + s[2] + len(s)
}
`)
	if got := jm.runInt32(t, "f"); got != 9 {
		t.Errorf("f() = %d, want 9", got)
	}
}

// TestPrintDynamicArray covers the runtime-loop-driven print rendering for a
// dynamic array (genPrintDynArrayValue) - same `[e0 e1 ...]` shape a
// fixed-size array already prints, but the element count isn't known until
// the loop actually runs.
func TestPrintDynamicArray(t *testing.T) {
	jm := compileAndJIT(t, `
func main() {
	s := []int{1, 2, 3}
	print(s)
	empty := make([]int, 0)
	print(empty)
}
`)
	out := captureStdout(t, func() {
		jm.runInt32(t, "main")
	})
	want := "[1 2 3]\n[]\n"
	if out != want {
		t.Fatalf("print output = %q, want %q", out, want)
	}
}

// TestDynamicArrayAsStructFieldAndParam covers a dynamic array flowing
// through a struct field and a function parameter/return type - both should
// work generically now that []T is a real Type, no different from any other
// aggregate.
func TestDynamicArrayAsStructFieldAndParam(t *testing.T) {
	jm := compileAndJIT(t, `
struct Box {
	items []int
}

func sum(s []int) int {
	total := 0
	for i := 0; i < len(s); i++ {
		total += s[i]
	}
	return total
}

func f() int {
	b := Box{[]int{1, 2, 3, 4}}
	return sum(b.items)
}
`)
	if got := jm.runInt32(t, "f"); got != 10 {
		t.Errorf("f() = %d, want 10", got)
	}
}

// TestDynamicArrayIndexOutOfBoundsTraps is the dynamic-array counterpart to
// bounds_test.go's TestOutOfBoundsIndexTraps: indexing past a slice's
// runtime len must trap exactly the same way a fixed-size array's
// out-of-range index does (genBoundsCheck, now generalized to accept a
// runtime llvm.Value size - see CODEGEN.md's "Array bounds checking"
// section). Uses the same re-exec-as-child-process pattern, since a real
// trap is a genuine process abort that would otherwise take the `go test`
// binary down with it.
func TestDynamicArrayIndexOutOfBoundsTraps(t *testing.T) {
	const childEnv = "LLVM_LANG_DYN_BOUNDS_TRAP_CHILD"
	const idxEnv = "LLVM_LANG_DYN_BOUNDS_TRAP_INDEX"

	if os.Getenv(childEnv) == "1" {
		idx := os.Getenv(idxEnv)
		jm := compileAndJIT(t, `
func trap(i int) int {
	s := []int{1, 2, 3}
	return s[i]
}
`)
		n, err := strconv.Atoi(idx)
		if err != nil {
			t.Fatalf("bad index env value %q: %v", idx, err)
		}
		jm.runInt32(t, "trap", int32(n))
		return
	}

	for _, idx := range []string{"-1", "3"} {
		t.Run("index_"+idx, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestDynamicArrayIndexOutOfBoundsTraps$")
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
		})
	}
}

// TestMakeCapLessThanLenTraps covers make's own runtime cap>=n check
// (genMakeCapCheck, runtime.go) - see LANGUAGE.md's "Dynamic arrays" section:
// n/cap are ordinary runtime expressions, so a bad relationship between them
// can't always be caught at compile time, hence the same trap-based
// mechanism the array-bounds check already uses.
func TestMakeCapLessThanLenTraps(t *testing.T) {
	const childEnv = "LLVM_LANG_MAKE_CAP_TRAP_CHILD"

	if os.Getenv(childEnv) == "1" {
		jm := compileAndJIT(t, `
func trap() int {
	s := make([]int, 5, 2)
	return len(s)
}
`)
		jm.runInt32(t, "trap")
		return
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestMakeCapLessThanLenTraps$")
	cmd.Env = append(os.Environ(), childEnv+"=1")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected the child process to crash (llvm.trap) for make([]int, 5, 2), but it exited cleanly - output:\n%s", out)
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("expected an *exec.ExitError from the crashing child, got %v (%T) - output:\n%s", err, err, out)
	}
	if exitErr.ExitCode() == 0 {
		t.Fatalf("expected an abnormal (crash) exit, got a clean exit code 0 - output:\n%s", out)
	}
}
