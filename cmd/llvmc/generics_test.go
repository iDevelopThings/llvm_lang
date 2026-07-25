package main

import (
	"os/exec"
	"strings"
	"testing"
)

// genericsExampleWant is examples/generics/generics.llx's own expected stdout
// (see that file's inline comments): inferred single- and multi-parameter
// calls, a generic method on a non-generic struct, and a generational-handle
// slot map instantiated with two different element types.
const genericsExampleWant = "3\n" +
	"3.750000\n" +
	"abcd\n" +
	"3\n" +
	"hi\n" +
	"player\n" +
	"42\n" +
	"10\n" +
	"20\n" +
	"1\n" +
	"30\n" +
	"100\n" +
	"50"

// TestBinary_GenericsExample runs examples/generics/generics.llx end to end
// through the real binary - the worked dogfooding demo for monomorphized
// generics (see LANGUAGE.md's "Generics" section).
func TestBinary_GenericsExample(t *testing.T) {
	cmd := exec.Command(llvmcPath, "../../examples/generics/generics.llx")
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
	if normalized != genericsExampleWant {
		t.Errorf("stdout = %q, want %q", normalized, genericsExampleWant)
	}
}

// TestBinary_AOT_Generics is the same program through the ahead-of-time path
// - specializations are ordinary functions in the emitted object file, so
// nothing here should differ from the JIT run above.
func TestBinary_AOT_Generics(t *testing.T) {
	exePath := aotCompile(t, "../../examples/generics/generics.llx")

	out, err := exec.Command(exePath).Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			t.Fatalf("%s exited %v, stderr:\n%s", exePath, err, ee.Stderr)
		}
		t.Fatalf("running %s: %v", exePath, err)
	}

	normalized := strings.ReplaceAll(string(out), "\r\n", "\n")
	normalized = strings.TrimRight(normalized, "\n")
	if normalized != genericsExampleWant {
		t.Errorf("stdout = %q, want %q", normalized, genericsExampleWant)
	}
}
