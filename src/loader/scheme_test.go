package loader

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/afero"
)

// fakeStdFS builds a tiny afero.Fs standing in for a real std/ directory
// (see StdlibFS) - the loader-level scheme tests below inject this via
// LoadProgramWithOptions rather than depending on StdlibFS/os.Executable,
// which only make sense against the real running binary, never a `go test`
// process.
func fakeStdFS(t *testing.T) afero.Fs {
	t.Helper()
	fs := afero.NewMemMapFs()
	writeFiles(t, fs, map[string]string{
		filepath.Join("mathutil", "mathutil.llx"): "func Sqrt(x f64) f64 {\n\treturn x\n}\n",
	})
	return fs
}

// TestLoadProgram_StdSchemeUnavailableByDefault covers LoadProgram's own
// documented contract: with no Options supplied, "std:" isn't backed by
// anything (same as the still-unimplemented "lib:") - a caller must go
// through LoadProgramWithOptions and supply a real StdFS (StdlibFS, for a
// real compiler binary) to make it available at all.
func TestLoadProgram_StdSchemeUnavailableByDefault(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeFiles(t, fs, map[string]string{
		join("proj", "main.llx"): "import \"std:mathutil\"\nfunc main() int {\n\treturn 0\n}\n",
	})

	_, err := LoadProgram(fs, join("proj"))
	if err == nil {
		t.Fatal("LoadProgram (no Options) with a std: import succeeded, want an error")
	}
	if !strings.Contains(err.Error(), "no standard library location was configured") {
		t.Errorf("error = %q, want it to say no standard library location was configured", err.Error())
	}
}

// TestLoadProgramWithOptions_StdSchemeResolvesAgainstSuppliedFS is the
// end-to-end proof this feature exists for: once a caller supplies a real
// std root (StdFS), a project living anywhere on disk - here, an
// afero.MemMapFs directory wholly unrelated to the "std root" filesystem
// itself - can `import "std:mathutil"` and reach it, independent of the
// importing file's own location (see DECISIONS.md's "std:/lib: import
// schemes" entry).
func TestLoadProgramWithOptions_StdSchemeResolvesAgainstSuppliedFS(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeFiles(t, fs, map[string]string{
		join("proj", "main.llx"): "import \"std:mathutil\"\nfunc main() int {\n\treturn 0\n}\n",
	})

	prog, err := LoadProgramWithOptions(fs, join("proj"), Options{StdFS: fakeStdFS(t)})
	if err != nil {
		t.Fatalf("LoadProgramWithOptions: %v", err)
	}

	entryFile := prog.Entry.Files[0]
	if len(entryFile.Imports) != 1 {
		t.Fatalf("entry file imports = %+v, want exactly 1", entryFile.Imports)
	}
	imp := entryFile.Imports[0]
	if imp.LocalName != "mathutil" {
		t.Errorf("LocalName = %q, want %q", imp.LocalName, "mathutil")
	}
	if imp.Package.Name != "mathutil" {
		t.Errorf("Package.Name = %q, want %q", imp.Package.Name, "mathutil")
	}
	if len(imp.Package.Files) != 1 || !strings.Contains(imp.Package.Files[0].Tree.File.Src, "func Sqrt(") {
		t.Errorf("std:mathutil's own resolved files = %+v, want the fake mathutil.llx source (func Sqrt)", imp.Package.Files)
	}
}

// TestLoadProgramWithOptions_StdSchemeIgnoresImportingFileLocation proves
// the whole point of a scheme-qualified import: two files at completely
// different depths, both importing "std:mathutil", must resolve to the
// exact same loaded package (deduped, not loaded twice) - a plain relative
// import could never do this, since it's anchored to each file's own
// directory.
func TestLoadProgramWithOptions_StdSchemeIgnoresImportingFileLocation(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeFiles(t, fs, map[string]string{
		join("proj", "main.llx"):                     "import \"./deep/a/b/c\"\nimport \"std:mathutil\"\nfunc main() int {\n\treturn 0\n}\n",
		join("proj", "deep", "a", "b", "c", "c.llx"): "import \"std:mathutil\"\nfunc F() int {\n\treturn 0\n}\n",
	})

	prog, err := LoadProgramWithOptions(fs, join("proj"), Options{StdFS: fakeStdFS(t)})
	if err != nil {
		t.Fatalf("LoadProgramWithOptions: %v", err)
	}

	mainStd := prog.Entry.Files[0].Imports[1].Package
	deepPkg := prog.Entry.Files[0].Imports[0].Package
	deepStd := deepPkg.Files[0].Imports[0].Package

	if mainStd != deepStd {
		t.Errorf("std:mathutil resolved to two different *Package instances (%p vs %p) depending on the importing file's own depth, want the identical instance", mainStd, deepStd)
	}
}

// TestLoadProgramWithOptions_LibSchemeReportsNotImplementedYet covers the
// "lib:" scheme this project reserves for future third-party package
// support (see DECISIONS.md) but doesn't back with anything yet -
// importing under it must fail with a clear "not implemented" diagnostic,
// distinct from a genuinely unknown/typo'd scheme, and unaffected by
// whether a std root was supplied.
func TestLoadProgramWithOptions_LibSchemeReportsNotImplementedYet(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeFiles(t, fs, map[string]string{
		join("proj", "main.llx"): "import \"lib:somepkg\"\nfunc main() int {\n\treturn 0\n}\n",
	})

	_, err := LoadProgramWithOptions(fs, join("proj"), Options{StdFS: fakeStdFS(t)})
	if err == nil {
		t.Fatal("LoadProgramWithOptions with a lib: import succeeded, want an error")
	}
	if !strings.Contains(err.Error(), "aren't implemented yet") {
		t.Errorf("error = %q, want it to say third-party imports aren't implemented yet", err.Error())
	}
}

// TestLoadProgramWithOptions_UnknownSchemeRejected covers a genuinely
// unknown scheme name (a typo, or a made-up one) - must fail with a clear
// "unknown import scheme" diagnostic, not silently misresolve as a
// relative path or panic.
func TestLoadProgramWithOptions_UnknownSchemeRejected(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeFiles(t, fs, map[string]string{
		join("proj", "main.llx"): "import \"vendor:somepkg\"\nfunc main() int {\n\treturn 0\n}\n",
	})

	_, err := LoadProgramWithOptions(fs, join("proj"), Options{StdFS: fakeStdFS(t)})
	if err == nil {
		t.Fatal("LoadProgramWithOptions with an unknown scheme succeeded, want an error")
	}
	if !strings.Contains(err.Error(), "unknown import scheme") {
		t.Errorf("error = %q, want it to say the scheme is unknown", err.Error())
	}
}

// TestLoadProgramWithOptions_BareColonSchemeRejected covers the boundary
// case of an EMPTY scheme name (a bare leading colon, ":foo") - splitScheme
// still reports this as scheme-qualified (empty string, "foo"), which must
// fail as "unknown import scheme" like any other unregistered name, not
// panic on an empty map key or silently fall back to a relative path.
func TestLoadProgramWithOptions_BareColonSchemeRejected(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeFiles(t, fs, map[string]string{
		join("proj", "main.llx"): "import \":foo\"\nfunc main() int {\n\treturn 0\n}\n",
	})

	_, err := LoadProgramWithOptions(fs, join("proj"), Options{StdFS: fakeStdFS(t)})
	if err == nil {
		t.Fatal("LoadProgramWithOptions with a bare-colon scheme succeeded, want an error")
	}
	if !strings.Contains(err.Error(), "unknown import scheme") {
		t.Errorf("error = %q, want it to say the scheme is unknown", err.Error())
	}
}

// TestLoadProgramWithOptions_StdSchemeWithEmptyPathErrors covers "std:"
// with nothing after the colon - resolves to the std root's own top-level
// directory, which (like this project's real std/) has no .llx files
// directly in it, only subdirectories - must fail the same clear way Load
// already does for an empty package directory, not silently succeed with
// zero files or panic.
func TestLoadProgramWithOptions_StdSchemeWithEmptyPathErrors(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeFiles(t, fs, map[string]string{
		join("proj", "main.llx"): "import \"std:\"\nfunc main() int {\n\treturn 0\n}\n",
	})

	_, err := LoadProgramWithOptions(fs, join("proj"), Options{StdFS: fakeStdFS(t)})
	if err == nil {
		t.Fatal("LoadProgramWithOptions with std: (empty path) succeeded, want an error")
	}
}

// TestLoadProgramWithOptions_StdSchemeWithNonexistentPackageErrors covers
// the invalid path: "std:doesnotexist" must fail the same clear way any
// other missing package directory does (Load's own "no .llx files found"
// error), not silently resolve to an empty package or panic.
func TestLoadProgramWithOptions_StdSchemeWithNonexistentPackageErrors(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeFiles(t, fs, map[string]string{
		join("proj", "main.llx"): "import \"std:doesnotexist\"\nfunc main() int {\n\treturn 0\n}\n",
	})

	_, err := LoadProgramWithOptions(fs, join("proj"), Options{StdFS: fakeStdFS(t)})
	if err == nil {
		t.Fatal("LoadProgramWithOptions with std:doesnotexist succeeded, want an error")
	}
}

// TestStdlibFSNextTo_FindsRealSiblingDirectory covers StdlibFS' own real
// logic (stdlibFSNextTo) against a genuine temp directory rather than
// afero.NewMemMapFs - StdlibFS' whole job is locating a real directory on
// the real OS filesystem next to a real executable path, so unlike every
// other loader test, this one legitimately needs t.TempDir() instead of an
// afero fake (there is no in-memory equivalent of "is there really a std/
// directory sitting on disk here").
func TestStdlibFSNextTo_FindsRealSiblingDirectory(t *testing.T) {
	dir := t.TempDir()
	stdDir := filepath.Join(dir, "std", "mathutil")
	if err := os.MkdirAll(stdDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stdDir, "mathutil.llx"), []byte("func Sqrt(x f64) f64 {\n\treturn x\n}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	fs, err := stdlibFSNextTo(filepath.Join(dir, "llvmc.exe"))
	if err != nil {
		t.Fatalf("stdlibFSNextTo: %v", err)
	}

	files, err := Load(fs, "mathutil")
	if err != nil {
		t.Fatalf("Load(fs, \"mathutil\"): %v", err)
	}
	if len(files) != 1 || !strings.Contains(files[0].Src, "func Sqrt(") {
		t.Errorf("Load(fs, \"mathutil\") = %+v, want the real mathutil.llx source", files)
	}
}

// TestStdlibFSNextTo_MissingDirectoryErrors covers the invalid path: no
// std/ directory next to the given executable path must report a clear
// error, not a nil afero.Fs a caller could then use to silently read
// nothing.
func TestStdlibFSNextTo_MissingDirectoryErrors(t *testing.T) {
	dir := t.TempDir()

	_, err := stdlibFSNextTo(filepath.Join(dir, "llvmc.exe"))
	if err == nil {
		t.Fatal("stdlibFSNextTo with no std/ sibling succeeded, want an error")
	}
}

// TestSplitScheme covers splitScheme's own edge cases directly: a colon
// appearing before any '/' is a scheme prefix; a colon appearing after one
// (or not at all) is never mistaken for one, since an ordinary relative
// path could otherwise never legally contain a colon at all.
func TestSplitScheme(t *testing.T) {
	cases := []struct {
		path       string
		wantScheme string
		wantRest   string
		wantOk     bool
	}{
		{"std:mathutil", "std", "mathutil", true},
		{"std:collections/slotmap", "std", "collections/slotmap", true},
		{"./mathutils", "", "", false},
		{"../shared/util", "", "", false},
		{"mathutils", "", "", false},
		{"foo/bar:baz", "", "", false}, // colon after the first '/' - not a scheme
	}
	for _, c := range cases {
		scheme, rest, ok := splitScheme(c.path)
		if ok != c.wantOk || scheme != c.wantScheme || rest != c.wantRest {
			t.Errorf("splitScheme(%q) = (%q, %q, %v), want (%q, %q, %v)",
				c.path, scheme, rest, ok, c.wantScheme, c.wantRest, c.wantOk)
		}
	}
}

// TestImportLocalName_StripsSchemePrefix covers importLocalName's own
// scheme-aware behavior: "std:mathutil"'s local binding name must be
// "mathutil", not the literal, unstripped "std:mathutil" a naive
// path.Base (which only understands '/') would otherwise return.
func TestImportLocalName_StripsSchemePrefix(t *testing.T) {
	cases := map[string]string{
		"std:mathutil":            "mathutil",
		"std:collections/slotmap": "slotmap",
		"./mathutils":             "mathutils",
		"../shared/util":          "util",
	}
	for path, want := range cases {
		if got := importLocalName(path); got != want {
			t.Errorf("importLocalName(%q) = %q, want %q", path, got, want)
		}
	}
}
