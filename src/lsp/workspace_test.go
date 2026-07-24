package lsp

import (
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
)

// newTestWorkspace returns a Workspace rooted at a fresh afero.MemMapFs (no
// real filesystem touched - see AGENTS.md's afero convention), bypassing
// NewWorkspace's own real-OS-backed overlay so a test can write fixture
// files directly onto the same afero.Fs OpenOrChange reads from.
func newTestWorkspace() *Workspace {
	return &Workspace{
		fs:       afero.NewMemMapFs(),
		analysis: make(map[string]*FileAnalysis),
	}
}

// TestWorkspace_OpenOrChange_Success covers the ordinary case still working:
// a valid single-file package produces a real analysis with no error.
func TestWorkspace_OpenOrChange_Success(t *testing.T) {
	w := newTestWorkspace()
	path := filepath.Join(string(filepath.Separator), "prog", "main.llx")

	out, err := w.OpenOrChange(path, `
func main() int {
	return 1
}
`)
	if err != nil {
		t.Fatalf("OpenOrChange: %v", err)
	}
	if out[path] == nil || out[path].Info == nil {
		t.Fatalf("OpenOrChange result for %s = %+v, want a populated FileAnalysis", path, out[path])
	}
}

// TestWorkspace_OpenOrChange_MalformedDestructorSurvives is the didOpen-
// equivalent regression case for a loader crash: an unclosed call with a
// dangling member access inside a destructor body, followed by a further
// top-level declaration, drives the parser past its own maxErrors bailout,
// leaving ParseFile's result at a nil *ast.Tree that loader.loadPackage then
// dereferences. OpenOrChange must survive this as a returned error via
// safeLoadProgram, not a panic that takes the whole server down.
func TestWorkspace_OpenOrChange_MalformedDestructorSurvives(t *testing.T) {
	w := newTestWorkspace()
	path := filepath.Join(string(filepath.Separator), "prog", "main.llx")

	src := `struct S {
	id int
	destructor() {
		print(this.
	}
}
func other() int {
	return 1
}
`
	out, err := w.OpenOrChange(path, src)
	if err == nil {
		t.Fatalf("OpenOrChange result = %+v, want a non-nil error (malformed source hits the parser's maxErrors bailout)", out)
	}
}

// TestWorkspace_OpenOrChange_RecoversForNextEdit proves a crash-inducing edit
// doesn't wedge the Workspace: a following well-formed edit on the same path
// must still analyze normally, exactly like a real editor session recovering
// after the user finishes typing past the malformed intermediate state.
func TestWorkspace_OpenOrChange_RecoversForNextEdit(t *testing.T) {
	w := newTestWorkspace()
	path := filepath.Join(string(filepath.Separator), "prog", "main.llx")

	if _, err := w.OpenOrChange(path, `struct S {
	id int
	destructor() {
		print(this.
	}
}
func other() int {
	return 1
}
`); err == nil {
		t.Fatal("expected the malformed edit to return an error")
	}

	out, err := w.OpenOrChange(path, `
func main() int {
	return 1
}
`)
	if err != nil {
		t.Fatalf("OpenOrChange after recovery: %v", err)
	}
	if out[path] == nil || out[path].Info == nil {
		t.Fatalf("OpenOrChange result for %s = %+v, want a populated FileAnalysis after recovery", path, out[path])
	}
}
