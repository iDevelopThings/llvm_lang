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
	tree, diags := parser.ParseFile(file, false)
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

// TestFoldRanges_TrailingCommentsDoNotMerge is a regression test for a real
// reported bug: a trailing `// comment` on each of several consecutive
// statement lines (examples/constructors/constructors.llx's main() body is
// the real-world shape this was found in) produced one bogus FoldComment
// region spanning the whole run of statements, silently folding away real
// code. The synthetic ASI semicolon after each statement splits what would
// be a single blank-line whitespace run into two single-newline halves,
// defeating a newline-counting heuristic - trailing comments must never
// join a comment-fold run at all, regardless of surrounding blank lines.
func TestFoldRanges_TrailingCommentsDoNotMerge(t *testing.T) {
	src := `struct Point {
	x int
}

func main() int {
	a := Point(5)   // calls the one-arg constructor, a.x == 5
	b := Point()    // calls the zero-arg constructor, b.x == 99
	c := Point{1}   // composite literal, completely unaffected/unchanged

	print(a.x)   // 5
	print(b.x)   // 99
	print(c.x)   // 1

	return a.x + b.x + c.x   // 5 + 99 + 1 = 105
}
`
	tree := parseTree(t, src)
	folds := tree.FoldRanges()

	for _, f := range folds {
		if f.Kind == ast.FoldComment {
			startLine := tree.File.Position(f.Span.Start).Line - 1
			endLine := tree.File.Position(f.Span.End).Line - 1
			t.Errorf("unexpected FoldComment range [%d,%d] - trailing comments must never fold", startLine, endLine)
		}
	}
}

// TestFoldRanges_StandaloneCommentRunBreaksOnTrailingComment checks that two
// separate runs of standalone comment lines, split by a statement line that
// itself carries a trailing comment, produce two independent FoldComment
// ranges rather than merging into one (or vanishing entirely).
func TestFoldRanges_StandaloneCommentRunBreaksOnTrailingComment(t *testing.T) {
	src := `func main() int {
	// step one
	// step two
	x := 1 // trailing note
	// step three
	// step four
}
`
	tree := parseTree(t, src)
	folds := tree.FoldRanges()

	var comments []ast.FoldRange
	for _, f := range folds {
		if f.Kind == ast.FoldComment {
			comments = append(comments, f)
		}
	}

	wantLines := []struct{ start, end int }{
		{1, 2},
		{4, 5},
	}
	if len(comments) != len(wantLines) {
		t.Fatalf("len(comment folds) = %d, want %d - folds: %+v", len(comments), len(wantLines), folds)
	}
	for i, want := range wantLines {
		startLine := tree.File.Position(comments[i].Span.Start).Line - 1
		endLine := tree.File.Position(comments[i].Span.End).Line - 1
		if startLine != want.start || endLine != want.end {
			t.Errorf("comments[%d] = [%d,%d], want [%d,%d]", i, startLine, endLine, want.start, want.end)
		}
	}
}
