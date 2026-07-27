package main

import (
	"os/exec"
	"strings"
	"testing"
)

// typeMatchExampleWant is examples/type_match/type_match.llx's own expected
// stdout (see that file's inline comments): one line per boxed value, each
// dispatched by the type inside the Any.
const typeMatchExampleWant = "int 7\n" +
	"string hello\n" +
	"Point(1, 2)\n" +
	"Size(3 x 4)\n" +
	"a shape\n" +
	"a shape\n" +
	"a pointer\n" +
	"something else"

// typeMatchExampleExit is the example's own documented exit code.
const typeMatchExampleExit = 42

// TestBinary_TypeMatchExample runs examples/type_match/type_match.llx end to
// end through the real binary - the worked demo for type matching over Any
// (see LANGUAGE.md's "match" section's "Type matching" subsection).
func TestBinary_TypeMatchExample(t *testing.T) {
	cmd := exec.Command(llvmcPath, "../../examples/type_match/type_match.llx")
	out, err := cmd.Output()
	assertTypeMatchRun(t, out, err)
}

// TestBinary_AOT_TypeMatch is the same program through the ahead-of-time
// path - the lowering is an ordinary switch either way, so nothing here
// should differ from the JIT run above.
func TestBinary_AOT_TypeMatch(t *testing.T) {
	exePath := aotCompile(t, "../../examples/type_match/type_match.llx")
	out, err := exec.Command(exePath).Output()
	assertTypeMatchRun(t, out, err)
}

func assertTypeMatchRun(t *testing.T, out []byte, err error) {
	t.Helper()
	if err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("running the type_match example: %v", err)
		}
		if code := ee.ExitCode(); code != typeMatchExampleExit {
			t.Fatalf("exit code = %d, want %d, stderr:\n%s", code, typeMatchExampleExit, ee.Stderr)
		}
	}

	normalized := strings.ReplaceAll(string(out), "\r\n", "\n")
	normalized = strings.TrimRight(normalized, "\n")
	if normalized != typeMatchExampleWant {
		t.Errorf("stdout = %q, want %q", normalized, typeMatchExampleWant)
	}
}
