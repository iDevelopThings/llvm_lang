package lsp

import (
	"runtime"
	"testing"
)

func TestURIPathRoundTrip(t *testing.T) {
	var path string
	if runtime.GOOS == "windows" {
		path = `C:\Users\dev\project\main.llx`
	} else {
		path = "/home/dev/project/main.llx"
	}

	uri := URIFromPath(path)
	got, err := PathFromURI(uri)
	if err != nil {
		t.Fatalf("PathFromURI(%q): %v", uri, err)
	}
	if got != path {
		t.Errorf("round trip mismatch: got %q, want %q (via uri %q)", got, path, uri)
	}
}

func TestPathFromURIRejectsNonFileScheme(t *testing.T) {
	if _, err := PathFromURI("http://example.com/foo.llx"); err == nil {
		t.Error("expected an error for a non-file:// URI, got nil")
	}
}
