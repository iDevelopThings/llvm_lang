package lsp

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/spf13/afero"
)

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
