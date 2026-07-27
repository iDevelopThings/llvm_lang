// Type-matching (`match anyValue { v int => ... }`) coverage across every
// LSP capability - see src/lsp/doc.go's own standing convention and
// any_test.go for the identical shape this file follows.
package lsp

import (
	"slices"
	"strings"
	"testing"

	"llvm_lang/src/diag"
	"llvm_lang/src/lexer"
)

const typeMatchFixture = `struct Point {
	X int
	Y int
}

enum Shape {
	Circle(f64)
	Square
}

func describe(a Any) int {
	match a {
		v int => {
			return v
		}
		p Point => {
			return p.X + p.Y
		}
		s Shape => {
			return 1
		}
		*Point => {
			return 2
		}
		_ => {
			return 0
		}
	}
}

func describeExpr(a Any) int {
	return match a {
		v int => v
		_ => 0
	}
}

func main() int {
	p := Point{X: 1, Y: 2}
	return describe(Any(p)) + describeExpr(Any(3))
}
`

// TestTypeMatch_Hover_BindingShowsNarrowedType is the capability that
// matters most for this feature: hovering a type-match binding must render
// the narrowed type, not Any.
func TestTypeMatch_Hover_BindingShowsNarrowedType(t *testing.T) {
	w, path := singleFileWorkspace(t, typeMatchFixture)
	fa, _ := w.Analysis(path)

	offset := strings.Index(fa.Tree.File.Src, "return v\n") + len("return ")
	pos := byteOffsetToPosition(fa.Tree.File, lexer.Pos(offset))

	text := hoverText(t, w.Hover(path, pos))
	if !strings.Contains(text, "int") {
		t.Errorf("Hover(v) = %q, want it to mention the narrowed type int", text)
	}
}

// TestTypeMatch_Hover_StructBindingShowsStructType covers the same for a
// struct-typed arm, where the narrowed type is a real declared type.
func TestTypeMatch_Hover_StructBindingShowsStructType(t *testing.T) {
	w, path := singleFileWorkspace(t, typeMatchFixture)
	fa, _ := w.Analysis(path)

	offset := strings.Index(fa.Tree.File.Src, "return p.X") + len("return ")
	pos := byteOffsetToPosition(fa.Tree.File, lexer.Pos(offset))

	text := hoverText(t, w.Hover(path, pos))
	if !strings.Contains(text, "Point") {
		t.Errorf("Hover(p) = %q, want it to mention Point", text)
	}
}

// TestTypeMatch_Definition_ArmTypeLandsOnDecl covers go-to-definition on an
// arm's own named type - it's a real type reference, exactly like one in a
// parameter list.
func TestTypeMatch_Definition_ArmTypeLandsOnDecl(t *testing.T) {
	w, path := singleFileWorkspace(t, typeMatchFixture)
	fa, _ := w.Analysis(path)

	offset := strings.Index(fa.Tree.File.Src, "p Point =>") + len("p ")
	pos := byteOffsetToPosition(fa.Tree.File, lexer.Pos(offset))

	loc := w.Definition(path, pos)
	if loc == nil {
		t.Fatal("Definition returned nil for a type-match arm's own named type")
	}
	wantOffset := strings.Index(fa.Tree.File.Src, "struct Point") + len("struct ")
	wantPos := byteOffsetToPosition(fa.Tree.File, lexer.Pos(wantOffset))
	if loc.Range.Start != wantPos {
		t.Errorf("Definition landed at %+v, want Point's own declaration at %+v", loc.Range.Start, wantPos)
	}
}

// TestTypeMatch_Definition_BareArmTypeLandsOnDecl covers the binding-less
// arm form, whose pattern IS the type node.
func TestTypeMatch_Definition_BareArmTypeLandsOnDecl(t *testing.T) {
	w, path := singleFileWorkspace(t, typeMatchFixture)
	fa, _ := w.Analysis(path)

	offset := strings.Index(fa.Tree.File.Src, "*Point =>") + len("*")
	pos := byteOffsetToPosition(fa.Tree.File, lexer.Pos(offset))

	loc := w.Definition(path, pos)
	if loc == nil {
		t.Fatal("Definition returned nil for a bare `*Point` arm's own type")
	}
	wantOffset := strings.Index(fa.Tree.File.Src, "struct Point") + len("struct ")
	wantPos := byteOffsetToPosition(fa.Tree.File, lexer.Pos(wantOffset))
	if loc.Range.Start != wantPos {
		t.Errorf("Definition landed at %+v, want Point's own declaration at %+v", loc.Range.Start, wantPos)
	}
}

// TestTypeMatch_Completion_ArmBodySeesNarrowedMembers covers completion
// inside an arm body: the binding is in scope, and completing off it offers
// the narrowed type's own members.
func TestTypeMatch_Completion_ArmBodySeesNarrowedMembers(t *testing.T) {
	src := `struct Point {
	X int
	Y int
}

func describe(a Any) int {
	match a {
		p Point => {
			return p.
		}
		_ => {
			return 0
		}
	}
}
`
	w, path := singleFileWorkspace(t, src)
	fa, _ := w.Analysis(path)

	offset := strings.Index(fa.Tree.File.Src, "return p.") + len("return p.")
	pos := byteOffsetToPosition(fa.Tree.File, lexer.Pos(offset))

	labels := completionLabels(w.Completion(path, pos))
	for _, want := range []string{"X", "Y"} {
		if !slices.Contains(labels, want) {
			t.Errorf("completion labels = %v, missing %q (the narrowed type's own field)", labels, want)
		}
	}
}

// TestTypeMatch_Completion_BindingVisibleInArmBody covers the binding itself
// appearing in ordinary scope completion inside its own arm.
func TestTypeMatch_Completion_BindingVisibleInArmBody(t *testing.T) {
	w, path := singleFileWorkspace(t, typeMatchFixture)
	fa, _ := w.Analysis(path)

	offset := strings.Index(fa.Tree.File.Src, "return v\n")
	pos := byteOffsetToPosition(fa.Tree.File, lexer.Pos(offset))

	labels := completionLabels(w.Completion(path, pos))
	if !slices.Contains(labels, "v") {
		t.Errorf("completion labels = %v, missing the arm binding %q", labels, "v")
	}
}

// TestTypeMatch_ReferencesAndHighlight_Binding covers References and
// DocumentHighlight for an arm binding used inside its own body.
func TestTypeMatch_ReferencesAndHighlight_Binding(t *testing.T) {
	w, path := singleFileWorkspace(t, typeMatchFixture)
	fa, _ := w.Analysis(path)

	offset := strings.Index(fa.Tree.File.Src, "return p.X") + len("return ")
	pos := byteOffsetToPosition(fa.Tree.File, lexer.Pos(offset))

	if refs := w.References(path, pos, true); len(refs) < 2 {
		t.Errorf("References(p) returned %d locations, want at least 2 (binding + use)", len(refs))
	}
	if highlights := w.DocumentHighlight(path, pos); len(highlights) < 2 {
		t.Errorf("DocumentHighlight(p) returned %d ranges, want at least 2", len(highlights))
	}
}

// TestTypeMatch_SymbolsFoldingAndTokens covers document symbols, folding and
// semantic tokens over a file full of type matches.
func TestTypeMatch_SymbolsFoldingAndTokens(t *testing.T) {
	w, path := singleFileWorkspace(t, typeMatchFixture)

	var names []string
	for _, s := range w.DocumentSymbols(path) {
		names = append(names, s.Name)
	}
	for _, want := range []string{"Point", "Shape", "describe", "describeExpr", "main"} {
		if !slices.Contains(names, want) {
			t.Errorf("DocumentSymbols names %v missing %q", names, want)
		}
	}

	if folds := w.FoldingRanges(path); len(folds) == 0 {
		t.Error("FoldingRanges returned none for a file of type matches")
	}
	if toks := w.SemanticTokens(path); toks == nil || len(toks.Data) == 0 {
		t.Error("SemanticTokens returned no data for a file of type matches")
	}
}

// TestTypeMatch_Diagnostics_MissingWildcard proves sema's own type-match
// diagnostics surface through the LSP.
func TestTypeMatch_Diagnostics_MissingWildcard(t *testing.T) {
	src := `func describe(a Any) int {
	match a {
		v int => {
			return v
		}
	}
}
`
	w, path := singleFileWorkspace(t, src)
	fa, _ := w.Analysis(path)
	if fa.Diags == nil || !fa.Diags.HasErrors() {
		t.Fatal("no diagnostics for a type match missing its wildcard arm")
	}
	found := slices.ContainsFunc(fa.Diags.All(), func(d diag.Diagnostic) bool {
		return strings.Contains(d.Msg, "wildcard")
	})
	if !found {
		t.Errorf("Diagnostics = %+v, want one about the missing wildcard arm", fa.Diags.All())
	}
}

// TestTypeMatch_MalformedSource_NoCrash is the broken/incomplete-source
// variant every capability must survive: a half-typed arm with no type after
// the binding, an arm with no body, and an unterminated match.
func TestTypeMatch_MalformedSource_NoCrash(t *testing.T) {
	src := `struct Point {
	X int
}

func Bad(a Any) int {
	match a {
		v => {
			return 0
		}
		p Point =>
		_ => {
	}
}

func Typing(a Any) int {
	match a {
		v Po
`
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("malformed type-match source panicked: %v", r)
		}
	}()

	w, path := singleFileWorkspace(t, src)
	fa, _ := w.Analysis(path)

	offset := strings.Index(fa.Tree.File.Src, "v Po") + len("v P")
	pos := byteOffsetToPosition(fa.Tree.File, lexer.Pos(offset))

	w.Hover(path, pos)
	w.Definition(path, pos)
	w.Completion(path, pos)
	w.References(path, pos, true)
	w.DocumentHighlight(path, pos)
	w.DocumentSymbols(path)
	w.FoldingRanges(path)
	w.SemanticTokens(path)
}
