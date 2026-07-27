package loader

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
)

// writeFiles populates an in-memory filesystem with the given path -> source
// text pairs, failing the test on any write error.
func writeFiles(t *testing.T, fs afero.Fs, files map[string]string) {
	t.Helper()
	for path, src := range files {
		if err := afero.WriteFile(fs, path, []byte(src), 0o644); err != nil {
			t.Fatalf("writing %s: %v", path, err)
		}
	}
}

func TestLoad_MultipleFilesSortedByName(t *testing.T) {
	dir := filepath.Join(string(filepath.Separator), "pkg")
	fs := afero.NewMemMapFs()
	writeFiles(t, fs, map[string]string{
		filepath.Join(dir, "c.llx"): "// c",
		filepath.Join(dir, "a.llx"): "// a",
		filepath.Join(dir, "b.llx"): "// b",
	})

	files, err := Load(fs, dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(files) != 3 {
		t.Fatalf("len(files) = %d, want 3", len(files))
	}
	want := []string{
		filepath.Join(dir, "a.llx"),
		filepath.Join(dir, "b.llx"),
		filepath.Join(dir, "c.llx"),
	}
	for i, f := range files {
		if f.Name != want[i] {
			t.Errorf("files[%d].Name = %q, want %q", i, f.Name, want[i])
		}
	}
	if files[0].Src != "// a" || files[1].Src != "// b" || files[2].Src != "// c" {
		t.Errorf("file contents did not round-trip: %+v", files)
	}
}

func TestLoad_FileAndDirectoryResolveIdentically(t *testing.T) {
	dir := filepath.Join(string(filepath.Separator), "pkg")
	fs := afero.NewMemMapFs()
	writeFiles(t, fs, map[string]string{
		filepath.Join(dir, "main.llx"): "// main",
		filepath.Join(dir, "util.llx"): "// util",
	})

	byDir, err := Load(fs, dir)
	if err != nil {
		t.Fatalf("Load(dir): %v", err)
	}
	byFile, err := Load(fs, filepath.Join(dir, "main.llx"))
	if err != nil {
		t.Fatalf("Load(file): %v", err)
	}

	if len(byDir) != len(byFile) {
		t.Fatalf("Load(dir) = %d files, Load(file) = %d files, want equal", len(byDir), len(byFile))
	}
	for i := range byDir {
		if byDir[i] != byFile[i] {
			t.Errorf("file[%d]: Load(dir) = %+v, Load(file) = %+v, want equal", i, byDir[i], byFile[i])
		}
	}
}

func TestLoad_NonRecursive(t *testing.T) {
	dir := filepath.Join(string(filepath.Separator), "pkg")
	fs := afero.NewMemMapFs()
	writeFiles(t, fs, map[string]string{
		filepath.Join(dir, "main.llx"):          "// main",
		filepath.Join(dir, "sub", "nested.llx"): "// nested - must NOT be picked up",
	})

	files, err := Load(fs, dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("len(files) = %d, want 1 (a subdirectory is not part of the package): %+v", len(files), files)
	}
	want := filepath.Join(dir, "main.llx")
	if files[0].Name != want {
		t.Errorf("files[0].Name = %q, want %q", files[0].Name, want)
	}
}

func TestLoad_IgnoresNonSourceFiles(t *testing.T) {
	dir := filepath.Join(string(filepath.Separator), "pkg")
	fs := afero.NewMemMapFs()
	writeFiles(t, fs, map[string]string{
		filepath.Join(dir, "main.llx"):  "// main",
		filepath.Join(dir, "README.md"): "# not a source file",
		filepath.Join(dir, "notes.txt"): "not a source file either",
	})

	files, err := Load(fs, dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("len(files) = %d, want 1: %+v", len(files), files)
	}
}

func TestLoad_SkipsStubsFile(t *testing.T) {
	dir := filepath.Join(string(filepath.Separator), "std")
	fs := afero.NewMemMapFs()
	writeFiles(t, fs, map[string]string{
		filepath.Join(dir, "stubs.llx"): "// language stubs - not a package member",
	})

	_, err := Load(fs, dir)
	if err == nil {
		t.Fatal("Load(std with only stubs.llx) succeeded, want no package members")
	}
}

func TestLoad_SkipsStubsFileAlongsideRealSources(t *testing.T) {
	dir := filepath.Join(string(filepath.Separator), "pkg")
	fs := afero.NewMemMapFs()
	writeFiles(t, fs, map[string]string{
		filepath.Join(dir, "main.llx"):  "// main",
		filepath.Join(dir, "stubs.llx"): "stub func args() []string",
	})

	files, err := Load(fs, dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(files) != 1 || filepath.Base(files[0].Name) != "main.llx" {
		t.Fatalf("files = %+v, want only main.llx", files)
	}
}

func TestLoad_SkipsStubsFileCaseInsensitiveBasename(t *testing.T) {
	dir := filepath.Join(string(filepath.Separator), "pkg")
	fs := afero.NewMemMapFs()
	writeFiles(t, fs, map[string]string{
		filepath.Join(dir, "main.llx"):   "// main",
		filepath.Join(dir, "STUBS.LLX"): "stub func args() []string",
	})

	files, err := Load(fs, dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(files) != 1 || filepath.Base(files[0].Name) != "main.llx" {
		t.Fatalf("files = %+v, want only main.llx (STUBS.LLX skipped via EqualFold)", files)
	}
}

func TestLoad_EmptyDirectoryErrors(t *testing.T) {
	dir := filepath.Join(string(filepath.Separator), "empty")
	fs := afero.NewMemMapFs()
	if err := fs.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	_, err := Load(fs, dir)
	if err == nil {
		t.Fatal("Load(empty dir) succeeded, want an error")
	}
}

func TestLoad_MissingPathErrors(t *testing.T) {
	fs := afero.NewMemMapFs()
	_, err := Load(fs, filepath.Join(string(filepath.Separator), "does-not-exist"))
	if err == nil {
		t.Fatal("Load(missing path) succeeded, want an error")
	}
}

// unreadableFs wraps an afero.Fs so that Open/OpenFile on one specific path
// fails, simulating a file that exists (and is listed by ReadDir) but can't
// actually be read - e.g. a permissions problem on a real filesystem.
type unreadableFs struct {
	afero.Fs
	badPath string
}

func (f *unreadableFs) Open(name string) (afero.File, error) {
	if name == f.badPath {
		return nil, os.ErrPermission
	}
	return f.Fs.Open(name)
}

func (f *unreadableFs) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	if name == f.badPath {
		return nil, os.ErrPermission
	}
	return f.Fs.OpenFile(name, flag, perm)
}

func TestLoad_UnreadableFileErrors(t *testing.T) {
	dir := filepath.Join(string(filepath.Separator), "pkg")
	badPath := filepath.Join(dir, "bad.llx")

	base := afero.NewMemMapFs()
	writeFiles(t, base, map[string]string{
		filepath.Join(dir, "good.llx"): "// good",
		badPath:                        "// bad",
	})
	fs := &unreadableFs{Fs: base, badPath: badPath}

	_, err := Load(fs, dir)
	if err == nil {
		t.Fatal("Load with an unreadable file succeeded, want an error")
	}
	if !errors.Is(err, os.ErrPermission) {
		t.Errorf("error = %v, want it to wrap os.ErrPermission", err)
	}
}
