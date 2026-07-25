package main

import (
	"os/exec"
	"strings"
	"testing"
)

// collectionsDemoExampleWant is
// examples/collections_demo/collections_demo.llx's own expected stdout (see
// that file's inline comments): std/collections's SlotMap[T] instantiated at
// both int and a struct type, exercising insert/get/remove, a freed slot's
// generation bump, and reuse by a later Insert.
const collectionsDemoExampleWant = "10\n" +
	"20\n" +
	"0\n" +
	"false\n" +
	"30\n" +
	"1\n" +
	"sword\n" +
	"4.500000\n" +
	"false\n" +
	"true"

// TestBinary_CollectionsDemoExample runs examples/collections_demo through
// the real llvmc binary (JIT) - what actually exercises std/collections's
// SlotMap[T], including two independent generic instantiations of it side by
// side.
func TestBinary_CollectionsDemoExample(t *testing.T) {
	cmd := exec.Command(llvmcPath, "../../examples/collections_demo/collections_demo.llx")
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			t.Fatalf("running llvmc: %v, stderr:\n%s", err, ee.Stderr)
		}
		t.Fatalf("running llvmc: %v", err)
	}

	normalized := strings.ReplaceAll(string(out), "\r\n", "\n")
	normalized = strings.TrimRight(normalized, "\n")
	if normalized != collectionsDemoExampleWant {
		t.Errorf("stdout = %q, want %q", normalized, collectionsDemoExampleWant)
	}
}

// TestBinary_AOT_CollectionsDemo AOT-compiles examples/collections_demo and
// confirms identical output to the JIT variant above.
func TestBinary_AOT_CollectionsDemo(t *testing.T) {
	exePath := aotCompile(t, "../../examples/collections_demo/collections_demo.llx")

	out, err := exec.Command(exePath).Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			t.Fatalf("%s exited %v, stderr:\n%s", exePath, err, ee.Stderr)
		}
		t.Fatalf("running %s: %v", exePath, err)
	}

	normalized := strings.ReplaceAll(string(out), "\r\n", "\n")
	normalized = strings.TrimRight(normalized, "\n")
	if normalized != collectionsDemoExampleWant {
		t.Errorf("stdout = %q, want %q", normalized, collectionsDemoExampleWant)
	}
}
