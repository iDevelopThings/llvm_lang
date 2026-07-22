package codegen

import (
	"errors"
	"os"
	"os/exec"
	"testing"
)

// TestSliceDynamicArrayAliasing covers the core, defining property of a
// slice expression on a dynamic array (`s[a:b]` - see LANGUAGE.md's
// "Slicing" section): the result is a fresh header sharing the exact same
// backing memory as s, not a copy - a write through either one must be
// visible through the other, in both directions.
func TestSliceDynamicArrayAliasing(t *testing.T) {
	jm := compileAndJIT(t, `
func f() int {
	s := []int{10, 20, 30, 40, 50}
	mid := s[1:4]
	if len(mid) != 3 {
		return 0
	}
	if mid[0] != 20 || mid[1] != 30 || mid[2] != 40 {
		return 0
	}
	mid[0] = 99
	if s[1] != 99 {
		return 0
	}
	s[2] = 777
	if mid[1] != 777 {
		return 0
	}
	return 1
}
`)
	if got := jm.runInt32(t, "f"); got != 1 {
		t.Errorf("f() = %d, want 1 (a dynamic array slice must alias its original's backing memory both ways)", got)
	}
}

// TestSliceDynamicArrayOmittedBounds covers all four slice-expression forms
// on a dynamic array (`s[a:b]`, `s[:b]`, `s[a:]`, `s[:]`) - the omitted-low
// default (0) and omitted-high default (len(s), not cap(s) - see
// LANGUAGE.md's "Slicing" section for why this direction matters).
func TestSliceDynamicArrayOmittedBounds(t *testing.T) {
	jm := compileAndJIT(t, `
func f() int {
	s := []int{1, 2, 3, 4, 5}
	a := s[1:3]
	b := s[:3]
	c := s[1:]
	d := s[:]
	if len(a) != 2 || a[0] != 2 || a[1] != 3 {
		return 0
	}
	if len(b) != 3 || b[0] != 1 || b[2] != 3 {
		return 0
	}
	if len(c) != 4 || c[0] != 2 || c[3] != 5 {
		return 0
	}
	if len(d) != 5 || d[0] != 1 || d[4] != 5 {
		return 0
	}
	return 1
}
`)
	if got := jm.runInt32(t, "f"); got != 1 {
		t.Errorf("f() = %d, want 1", got)
	}
}

// TestSliceDynamicArrayReslicePastLenWithinCap covers the one Go-slicing
// subtlety this feature deliberately preserves: a reslice's high bound is
// checked against cap(s), not len(s), so it's legal to extend a slice past
// its current length into spare capacity - the exact idiom Go's own
// slice-growth code relies on.
func TestSliceDynamicArrayReslicePastLenWithinCap(t *testing.T) {
	jm := compileAndJIT(t, `
func f() int {
	s := make([]int, 2, 5)
	s[0] = 1
	s[1] = 2
	grown := s[0:4]
	if len(grown) != 4 {
		return 0
	}
	grown[3] = 40
	return grown[3]
}
`)
	if got := jm.runInt32(t, "f"); got != 40 {
		t.Errorf("f() = %d, want 40 (reslicing past len but within cap must be legal)", got)
	}
}

// TestSliceStringBasic covers `str[a:b]`/`str[a:]`/`str[:b]` on a string -
// a fresh {ptr, len} value sharing str's own backing bytes, printed to
// verify the actual content (there's no int-returning way to observe a
// string's identity/content directly through runInt32).
func TestSliceStringBasic(t *testing.T) {
	jm := compileAndJIT(t, `
func main() {
	str := "hello world"
	print(str[6:])
	print(str[:5])
	print(str[6:11])
	print(str[:])
}
`)
	out := captureStdout(t, func() {
		jm.runInt32(t, "main")
	})
	want := "world\nhello\nworld\nhello world\n"
	if out != want {
		t.Fatalf("print output = %q, want %q", out, want)
	}
}

// TestSliceStringAliasingWithOriginalUnaffected covers that slicing a string
// never mutates anything - strings are immutable in this language (see
// LANGUAGE.md's "string representation" section) - the "sharing" here is
// read-only sharing of the same backing bytes, not a mutation channel the
// way a dynamic array's slice is.
func TestSliceStringAliasingWithOriginalUnaffected(t *testing.T) {
	jm := compileAndJIT(t, `
func f() bool {
	full := "hello world"
	part := full[0:5]
	return part == "hello" && full == "hello world"
}
`)
	if got := jm.runBool(t, "f"); got != true {
		t.Errorf("f() = %v, want true", got)
	}
}

// TestSliceFixedArrayAliasing covers slicing a fixed-size array (`arr[a:b]`)
// - it must produce a real []int that shares the fixed array's own storage
// (see LANGUAGE.md's "Slicing" section: this requires arr to be
// addressable, which sema already enforces - see slice_test.go in sema),
// exactly like the dynamic-array case above.
func TestSliceFixedArrayAliasing(t *testing.T) {
	jm := compileAndJIT(t, `
func f() int {
	arr := [5]int{1, 2, 3, 4, 5}
	view := arr[1:3]
	if len(view) != 2 || view[0] != 2 || view[1] != 3 {
		return 0
	}
	view[0] = 100
	if arr[1] != 100 {
		return 0
	}
	arr[2] = 200
	if view[1] != 200 {
		return 0
	}
	return 1
}
`)
	if got := jm.runInt32(t, "f"); got != 1 {
		t.Errorf("f() = %d, want 1 (slicing a fixed array must alias its own storage both ways)", got)
	}
}

// TestSliceLenAndAppendOnSlicedValue covers that a sliced value is an
// ordinary dynamic-array value afterward - len and append work unchanged,
// with no special-casing needed in either (see LANGUAGE.md's "Slicing"
// section).
func TestSliceLenAndAppendOnSlicedValue(t *testing.T) {
	jm := compileAndJIT(t, `
func f() int {
	s := []int{1, 2, 3, 4, 5}
	mid := s[1:3]
	mid = append(mid, 999)
	if len(mid) != 3 {
		return 0
	}
	if mid[0] != 2 || mid[1] != 3 || mid[2] != 999 {
		return 0
	}
	return 1
}
`)
	if got := jm.runInt32(t, "f"); got != 1 {
		t.Errorf("f() = %d, want 1", got)
	}
}

// TestLanguageSpecSliceExample mirrors the exact example from this project's
// own slicing design writeup end to end - all three operand types combined
// in one program, exercising aliasing for each.
func TestLanguageSpecSliceExample(t *testing.T) {
	jm := compileAndJIT(t, `
func f() int {
	s := []int{10, 20, 30, 40, 50}
	mid := s[1:4]
	mid[0] = 99

	str := "hello world"
	tail := str[6:]
	head := str[:5]

	arr := [5]int{1, 2, 3, 4, 5}
	view := arr[1:3]
	view[0] = 100

	if s[1] != 99 {
		return -1
	}
	if tail != "world" || head != "hello" {
		return -2
	}
	if arr[1] != 100 {
		return -3
	}
	return len(mid) + len(view)
}
`)
	if got := jm.runInt32(t, "f"); got != 5 {
		t.Errorf("f() = %d, want 5", got)
	}
}

// TestSliceRangeCheckTraps covers the runtime range check (genSliceRangeCheck,
// expr.go: 0 <= low <= high <= cap-or-len-or-N) actually trapping on an
// out-of-range slice expression - the slicing counterpart to
// bounds_test.go's TestOutOfBoundsIndexTraps. Uses the same re-exec-as-a-
// child-process pattern: a real trap is a genuine process abort that would
// otherwise take the `go test` binary down with it.
func TestSliceRangeCheckTraps(t *testing.T) {
	const childEnv = "LLVM_LANG_SLICE_TRAP_CHILD"
	const srcEnv = "LLVM_LANG_SLICE_TRAP_SRC"

	cases := []struct {
		name string
		src  string
	}{
		{
			name: "dynamic_array_high_past_cap",
			src: `
func trap() int {
	s := make([]int, 2, 2)
	bad := s[0:3]
	return len(bad)
}
`,
		},
		{
			name: "dynamic_array_negative_low",
			src: `
func trap() int {
	s := []int{1, 2, 3}
	bad := s[-1:2]
	return len(bad)
}
`,
		},
		{
			name: "dynamic_array_low_greater_than_high",
			src: `
func trap() int {
	s := []int{1, 2, 3}
	bad := s[2:1]
	return len(bad)
}
`,
		},
		{
			name: "string_high_past_len",
			src: `
func trap() int {
	s := "abc"
	bad := s[0:4]
	return len(bad)
}
`,
		},
		{
			name: "fixed_array_high_past_n",
			src: `
func trap() int {
	arr := [3]int{1, 2, 3}
	bad := arr[0:4]
	return len(bad)
}
`,
		},
	}

	if os.Getenv(childEnv) == "1" {
		jm := compileAndJIT(t, os.Getenv(srcEnv))
		// If the range check works, this call never returns - the process
		// aborts inside the JIT-compiled trap block. Reaching the line after
		// it (a clean process exit) is exactly the failure this test is
		// designed to catch, from the parent's side.
		jm.runInt32(t, "trap")
		return
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestSliceRangeCheckTraps$")
			cmd.Env = append(os.Environ(),
				childEnv+"=1",
				srcEnv+"="+tc.src,
			)
			out, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("%s: expected the child process to crash (llvm.trap), but it exited cleanly - output:\n%s", tc.name, out)
			}
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) {
				t.Fatalf("%s: expected an *exec.ExitError from the crashing child, got %v (%T) - output:\n%s", tc.name, err, err, out)
			}
			if exitErr.ExitCode() == 0 {
				t.Fatalf("%s: expected an abnormal (crash) exit, got a clean exit code 0 - output:\n%s", tc.name, out)
			}
		})
	}
}
