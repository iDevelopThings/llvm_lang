package main

import (
	"os/exec"
	"strings"
	"testing"
)

// slicesDemoExampleWant is examples/slices_demo/slices_demo.llx's own
// expected stdout (see that file's inline comments): every std/slices
// function, plus its own empty-slice and single-element-Reverse boundary
// cases.
const slicesDemoExampleWant = "true\n" +
	"false\n" +
	"3\n" +
	"-1\n" +
	"[2 4 6 8 10]\n" +
	"[1 4 9 16 25]\n" +
	"[2 4]\n" +
	"15\n" +
	"[5 4 3 2 1]\n" +
	"false\n" +
	"-1\n" +
	"0\n" +
	"0\n" +
	"0\n" +
	"100\n" +
	"[42]"

// TestBinary_SlicesDemoExample runs examples/slices_demo through the real
// llvmc binary (JIT) - what actually exercises std/slices's generic
// algorithms (Contains/IndexOf/Reverse/Map/Filter/Reduce), including Map
// called with both a named function and a lambda as its callback argument.
func TestBinary_SlicesDemoExample(t *testing.T) {
	cmd := exec.Command(llvmcPath, "../../examples/slices_demo/slices_demo.llx")
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			t.Fatalf("running llvmc: %v, stderr:\n%s", err, ee.Stderr)
		}
		t.Fatalf("running llvmc: %v", err)
	}

	normalized := strings.ReplaceAll(string(out), "\r\n", "\n")
	normalized = strings.TrimRight(normalized, "\n")
	if normalized != slicesDemoExampleWant {
		t.Errorf("stdout = %q, want %q", normalized, slicesDemoExampleWant)
	}
}

// TestBinary_AOT_SlicesDemo AOT-compiles examples/slices_demo and confirms
// identical output to the JIT variant above.
func TestBinary_AOT_SlicesDemo(t *testing.T) {
	exePath := aotCompile(t, "../../examples/slices_demo/slices_demo.llx")

	out, err := exec.Command(exePath).Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			t.Fatalf("%s exited %v, stderr:\n%s", exePath, err, ee.Stderr)
		}
		t.Fatalf("running %s: %v", exePath, err)
	}

	normalized := strings.ReplaceAll(string(out), "\r\n", "\n")
	normalized = strings.TrimRight(normalized, "\n")
	if normalized != slicesDemoExampleWant {
		t.Errorf("stdout = %q, want %q", normalized, slicesDemoExampleWant)
	}
}
