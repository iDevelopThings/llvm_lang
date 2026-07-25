package lsp

import (
	"path/filepath"
	"slices"
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
// equivalent regression case for a real loader crash: an unclosed call with
// a dangling member access inside a destructor body, followed by a further
// top-level declaration, drives the parser past its own maxErrors bailout.
// parser.Run now recovers into the parser's own partial tree rather than a
// nil *ast.Tree (see parser.go), so this is no longer even an error - just
// an ordinary analysis carrying the real parse diagnostics, same as any
// other malformed-but-non-panicking source.
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
	if err != nil {
		t.Fatalf("OpenOrChange: %v, want no error - a broken parse now degrades to diagnostics, not a panic/error", err)
	}
	fa := out[path]
	if fa == nil || fa.Diags == nil || !fa.Diags.HasErrors() {
		t.Fatalf("OpenOrChange result for %s = %+v, want a populated FileAnalysis carrying real parse errors", path, fa)
	}
}

// TestWorkspace_OpenOrChange_RecoversForNextEdit proves a maxErrors-bailout
// edit doesn't wedge the Workspace: a following well-formed edit on the same
// path must still analyze normally, exactly like a real editor session
// recovering after the user finishes typing past the malformed intermediate
// state.
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
`); err != nil {
		t.Fatalf("OpenOrChange on the malformed edit: %v, want no error", err)
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

func TestWorkspace_PackageIndex_DiscoversUnderRoot(t *testing.T) {
	sep := string(filepath.Separator)
	root := filepath.Join(sep, "ws")
	fs := afero.NewMemMapFs()
	afero.WriteFile(fs, filepath.Join(root, "app", "main.llx"), []byte("// main"), 0o644)
	afero.WriteFile(fs, filepath.Join(root, "std", "strings", "s.llx"), []byte("// strings"), 0o644)

	w := &Workspace{fs: fs}
	w.SetRoot(root)

	var names []string
	for _, c := range w.PackageIndex() {
		names = append(names, c.Name)
	}
	slices.Sort(names)
	if got, want := names, []string{"app", "strings"}; !slices.Equal(got, want) {
		t.Errorf("PackageIndex names = %v, want %v", got, want)
	}
}

func TestWorkspace_PackageIndex_EmptyWithoutRoot(t *testing.T) {
	w := &Workspace{fs: afero.NewMemMapFs()}
	if got := w.PackageIndex(); got != nil {
		t.Errorf("PackageIndex() with no root set = %v, want nil", got)
	}
}

func TestWorkspace_PackageIndex_CachedAfterFirstCall(t *testing.T) {
	sep := string(filepath.Separator)
	root := filepath.Join(sep, "ws")
	fs := afero.NewMemMapFs()
	afero.WriteFile(fs, filepath.Join(root, "app", "main.llx"), []byte("// main"), 0o644)

	w := &Workspace{fs: fs}
	w.SetRoot(root)

	first := w.PackageIndex()
	afero.WriteFile(fs, filepath.Join(root, "extra", "e.llx"), []byte("// extra"), 0o644)
	second := w.PackageIndex()

	if len(second) != len(first) {
		t.Errorf("PackageIndex() after a later disk change = %d entries, want %d (cached, not re-scanned - a documented v1 limitation)", len(second), len(first))
	}
}
