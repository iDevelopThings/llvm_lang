package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveLibraryArtifactPrefersDLLOverStatic(t *testing.T) {
	dir := t.TempDir()
	mustWriteEmpty(t, filepath.Join(dir, "foo.dll"))
	mustWriteEmpty(t, filepath.Join(dir, "libfoo.a"))

	got, err := resolveLibraryArtifact("foo", []string{dir})
	if err != nil {
		t.Fatalf("resolveLibraryArtifact: %v", err)
	}
	if got.Kind != libraryShared {
		t.Fatalf("Kind = %v, want libraryShared", got.Kind)
	}
	if got.Path != filepath.Join(dir, "foo.dll") {
		t.Fatalf("Path = %q, want foo.dll", got.Path)
	}
}

func TestResolveLibraryArtifactFindsStaticWhenNoDLL(t *testing.T) {
	dir := t.TempDir()
	mustWriteEmpty(t, filepath.Join(dir, "libaddone.a"))

	got, err := resolveLibraryArtifact("addone", []string{dir})
	if err != nil {
		t.Fatalf("resolveLibraryArtifact: %v", err)
	}
	if got.Kind != libraryStatic {
		t.Fatalf("Kind = %v, want libraryStatic", got.Kind)
	}
	if got.Path != filepath.Join(dir, "libaddone.a") {
		t.Fatalf("Path = %q, want libaddone.a", got.Path)
	}
}

func TestResolveLibraryArtifactRejectsImportLibOnly(t *testing.T) {
	dir := t.TempDir()
	mustWriteEmpty(t, filepath.Join(dir, "libfoo.dll.a"))

	_, err := resolveLibraryArtifact("foo", []string{dir})
	if err == nil {
		t.Fatal("expected error for import-lib-only directory")
	}
	if !strings.Contains(err.Error(), "import lib") {
		t.Fatalf("err = %v, want it to mention import lib", err)
	}
}

func TestResolveLibraryArtifactLiteralPath(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "custom.dll")
	mustWriteEmpty(t, p)

	got, err := resolveLibraryArtifact(p, nil)
	if err != nil {
		t.Fatalf("resolveLibraryArtifact: %v", err)
	}
	if got.Kind != libraryShared || got.Path != p {
		t.Fatalf("got %+v, want shared %q", got, p)
	}
}

// TestResolveLibraryArtifactBareFilenameUsesDirs covers the regression where
// a bare "-l libfoo.a" (extension, no directory separator) was treated as a
// literal CWD-relative path and never searched under -L.
func TestResolveLibraryArtifactBareFilenameUsesDirs(t *testing.T) {
	dir := t.TempDir()
	want := filepath.Join(dir, "libfoo.a")
	mustWriteEmpty(t, want)

	got, err := resolveLibraryArtifact("libfoo.a", []string{dir})
	if err != nil {
		t.Fatalf("resolveLibraryArtifact: %v", err)
	}
	if got.Kind != libraryStatic || got.Path != want {
		t.Fatalf("got %+v, want static %q", got, want)
	}
}

func TestResolveLibraryArtifactBareFilenameMissingUsesDirs(t *testing.T) {
	dir := t.TempDir()
	_, err := resolveLibraryArtifact("libfoo.a", []string{dir})
	if err == nil {
		t.Fatal("expected missing bare-filename error")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("err = %v, want not found", err)
	}
}

func TestResolveLibraryArtifactMissing(t *testing.T) {
	dir := t.TempDir()
	_, err := resolveLibraryArtifact("missing", []string{dir})
	if err == nil {
		t.Fatal("expected missing-library error")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("err = %v, want not found", err)
	}
}

func TestResolveLibraryArtifactsOrder(t *testing.T) {
	dir := t.TempDir()
	mustWriteEmpty(t, filepath.Join(dir, "a.dll"))
	mustWriteEmpty(t, filepath.Join(dir, "libb.a"))

	got, err := resolveLibraryArtifacts([]string{"a", "b"}, []string{dir})
	if err != nil {
		t.Fatalf("resolveLibraryArtifacts: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].Kind != libraryShared || got[1].Kind != libraryStatic {
		t.Fatalf("kinds = %v, %v", got[0].Kind, got[1].Kind)
	}
}

func mustWriteEmpty(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}
