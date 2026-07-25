package lsp

import "testing"

// TestResolveStructFields_UnanalyzedDeclaringTreeReturnsNotOk covers the
// graceful-degradation path a real hover depends on: a struct whose
// declaring file's Info isn't in this Workspace's own analysis snapshot
// (e.g. a cross-package struct the workspace hasn't analyzed, or hasn't
// caught up with yet) must report ok=false, not panic or return a
// misleadingly-empty field list.
func TestResolveStructFields_UnanalyzedDeclaringTreeReturnsNotOk(t *testing.T) {
	w, path := singleFileWorkspace(t, `struct Point {
	x int
}
`)
	fa, _ := w.Analysis(path)
	si := fa.Info.Structs["Point"]
	if si == nil {
		t.Fatal(`Info.Structs["Point"] = nil, want the real StructInfo`)
	}

	other := newTestWorkspace()
	if _, ok := other.resolveStructFields(si); ok {
		t.Error("resolveStructFields returned ok=true against a workspace with no analysis for the struct's own declaring tree, want false")
	}
}
