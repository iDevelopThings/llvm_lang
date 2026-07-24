// Package ast_test (an external test package, not package ast) so it can
// import src/parser to build a real Tree from real source - src/ast itself
// can never import src/parser (parser already imports ast, so that would be
// a cycle), but a hand-built tree risks silently sidestepping the actual
// bug a real parse would hit (span/adjacency details only the real grammar
// produces).
package ast_test

import (
	"testing"

	"llvm_lang/src/ast"
	"llvm_lang/src/lexer"
	"llvm_lang/src/parser"
)

func parseTree(t *testing.T, src string) *ast.Tree {
	t.Helper()
	file := lexer.NewFile("t.llx", src)
	tree, diags := parser.ParseFile(file)
	if diags.HasErrors() {
		t.Fatalf("unexpected parse errors: %v", diags.Sorted())
	}
	return tree
}

// TestFoldRanges_AdjacentTopLevelFuncs is a regression test for a real
// reported bug: two adjacent top-level function declarations produced one
// merged folding range spanning from the first function's own opening line
// through the second's closing brace, instead of two independent ranges.
func TestFoldRanges_AdjacentTopLevelFuncs(t *testing.T) {
	src := `func Add(a int, b int) int {
	return a + b
}

func double(x int) int {
	return x * 2
}
`
	tree := parseTree(t, src)
	folds := tree.FoldRanges()

	wantLines := []struct{ start, end int }{
		{0, 2}, // func Add's own Block: line 0 "{" through line 2 "}"
		{4, 6}, // func double's own Block: line 4 "{" through line 6 "}"
	}
	if len(folds) != len(wantLines) {
		t.Fatalf("len(folds) = %d, want %d - folds: %+v", len(folds), len(wantLines), folds)
	}
	for i, want := range wantLines {
		startLine := tree.File.Position(folds[i].Span.Start).Line - 1
		endLine := tree.File.Position(folds[i].Span.End).Line - 1
		if startLine != want.start || endLine != want.end {
			t.Errorf("folds[%d] = [%d,%d], want [%d,%d]", i, startLine, endLine, want.start, want.end)
		}
	}
}

// TestFoldRanges_StructThenMethod is a regression test for the same bug
// reported against a struct: struct Point{...}'s own fold shouldn't extend
// into the unrelated method declared after it.
func TestFoldRanges_StructThenMethod(t *testing.T) {
	src := `struct Point {
	X int
	Y int
}

func (Point) Scale(factor int) {
	this.X = this.X * factor
	this.Y = this.Y * factor
}
`
	tree := parseTree(t, src)
	folds := tree.FoldRanges()

	wantLines := []struct{ start, end int }{
		{0, 3}, // struct Point's own body: line 0 through line 3
		{5, 8}, // Scale's own Block: line 5 through line 8
	}
	if len(folds) != len(wantLines) {
		t.Fatalf("len(folds) = %d, want %d - folds: %+v", len(folds), len(wantLines), folds)
	}
	for i, want := range wantLines {
		startLine := tree.File.Position(folds[i].Span.Start).Line - 1
		endLine := tree.File.Position(folds[i].Span.End).Line - 1
		if startLine != want.start || endLine != want.end {
			t.Errorf("folds[%d] = [%d,%d], want [%d,%d]", i, startLine, endLine, want.start, want.end)
		}
	}
}
