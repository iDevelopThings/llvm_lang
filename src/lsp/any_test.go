// Any-type and AnyKind/AnyName/AnyAs/AnyFields/AnyLen/AnyIndex builtin
// coverage across every LSP capability - see src/lsp/doc.go's own standing
// convention and variadic_test.go for the identical shape this file follows.
package lsp

import (
	"slices"
	"strings"
	"testing"

	"llvm_lang/src/lexer"
)

const anyFixture = `struct Point {
	X int
	Y int
}

func describe(a Any) int {
	k := AnyKind(a)
	n := AnyName(a)
	v, ok := AnyAs[int](a)
	if ok {
		return v
	}
	return 0
}

func describeArray(a Any) int {
	count := AnyLen(a)
	e, ok := AnyIndex(a, 0)
	if ok {
		v, vok := AnyAs[int](e)
		if vok {
			return count + v
		}
	}
	return count
}

func main() int {
	p := Point{X: 1, Y: 2}
	boxed := Any(p)
	sum := 0
	for name, field := range AnyFields(boxed) {
		fv, ok := AnyAs[int](field)
		if ok {
			sum = sum + fv
		}
	}
	s := []int{1, 2, 3}
	return describe(Any(5)) + sum + describeArray(Any(s))
}
`

// TestAny_Hover_BoxedVarShowsAnyType covers hovering a reference to a
// variable declared from Any(x) - its rendered type must read "Any", not
// the boxed source type. Hovers the reference inside AnyFields(boxed), not
// the `boxed :=` declaration site itself - a pre-existing, Any-independent
// gap in this package means only a *reference*'s own Info.Types entry
// renders a "type:" line in hover, never the declaring name (confirmed with
// a plain `x := 5` too - worth a separate follow-up, out of scope here).
func TestAny_Hover_BoxedVarShowsAnyType(t *testing.T) {
	w, path := singleFileWorkspace(t, anyFixture)
	fa, _ := w.Analysis(path)

	offset := strings.Index(fa.Tree.File.Src, "AnyFields(boxed)") + len("AnyFields(")
	pos := byteOffsetToPosition(fa.Tree.File, lexer.Pos(offset))

	text := hoverText(t, w.Hover(path, pos))
	if !strings.Contains(text, "Any") {
		t.Errorf("Hover(boxed) = %q, want it to contain %q", text, "Any")
	}
}

// TestAny_Hover_ParamShowsAnyType covers hovering an ordinary `a Any`
// parameter reference inside a function body.
func TestAny_Hover_ParamShowsAnyType(t *testing.T) {
	w, path := singleFileWorkspace(t, anyFixture)
	fa, _ := w.Analysis(path)

	offset := strings.Index(fa.Tree.File.Src, "AnyKind(a)") + len("AnyKind(")
	pos := byteOffsetToPosition(fa.Tree.File, lexer.Pos(offset))

	text := hoverText(t, w.Hover(path, pos))
	if !strings.Contains(text, "Any") {
		t.Errorf("Hover(a) = %q, want it to contain %q", text, "Any")
	}
}

// TestAny_Hover_ArrayParamShowsAnyType covers hovering the `a Any` parameter
// inside describeArray - the AnyLen/AnyIndex-using sibling of describe
// above, proving the new builtins don't confuse ordinary hover resolution.
func TestAny_Hover_ArrayParamShowsAnyType(t *testing.T) {
	w, path := singleFileWorkspace(t, anyFixture)
	fa, _ := w.Analysis(path)

	offset := strings.Index(fa.Tree.File.Src, "AnyLen(a)") + len("AnyLen(")
	pos := byteOffsetToPosition(fa.Tree.File, lexer.Pos(offset))

	text := hoverText(t, w.Hover(path, pos))
	if !strings.Contains(text, "Any") {
		t.Errorf("Hover(a) = %q, want it to contain %q", text, "Any")
	}
}

// TestAny_Definition_BoxedStructFieldLandsOnDecl covers go-to-definition
// from AnyAs[Point]'s own type argument back to the struct's declaration -
// proving the type-argument position resolves exactly like an ordinary type
// reference elsewhere.
func TestAny_Definition_BoxedStructFieldLandsOnDecl(t *testing.T) {
	src := `struct Point {
	X int
}

func f(a Any) int {
	v, ok := AnyAs[Point](a)
	if ok {
		return v.X
	}
	return 0
}
`
	w, path := singleFileWorkspace(t, src)
	fa, _ := w.Analysis(path)

	offset := strings.Index(fa.Tree.File.Src, "AnyAs[Point]") + len("AnyAs[")
	pos := byteOffsetToPosition(fa.Tree.File, lexer.Pos(offset))

	loc := w.Definition(path, pos)
	if loc == nil {
		t.Fatal("Definition returned nil for AnyAs[Point]'s own type argument")
	}
	wantOffset := strings.Index(fa.Tree.File.Src, "struct Point") + len("struct ")
	wantPos := byteOffsetToPosition(fa.Tree.File, lexer.Pos(wantOffset))
	if loc.Range.Start != wantPos {
		t.Errorf("Definition landed at %+v, want Point's own declaration at %+v", loc.Range.Start, wantPos)
	}
}

// TestAny_Definition_BoxedEnumTypeArgLandsOnDecl mirrors
// TestAny_Definition_BoxedStructFieldLandsOnDecl for an enum type argument -
// enums are boxable into Any now (see LANGUAGE.md's "Any" section), so
// AnyAs[Shape]'s own type argument must resolve exactly like a struct's does.
func TestAny_Definition_BoxedEnumTypeArgLandsOnDecl(t *testing.T) {
	src := `enum Shape {
	Point,
	Circle(f64)
}

func f(a Any) int {
	v, ok := AnyAs[Shape](a)
	if ok {
		return AnyKind(a)
	}
	return 0
}
`
	w, path := singleFileWorkspace(t, src)
	fa, _ := w.Analysis(path)

	offset := strings.Index(fa.Tree.File.Src, "AnyAs[Shape]") + len("AnyAs[")
	pos := byteOffsetToPosition(fa.Tree.File, lexer.Pos(offset))

	loc := w.Definition(path, pos)
	if loc == nil {
		t.Fatal("Definition returned nil for AnyAs[Shape]'s own type argument")
	}
	wantOffset := strings.Index(fa.Tree.File.Src, "enum Shape") + len("enum ")
	wantPos := byteOffsetToPosition(fa.Tree.File, lexer.Pos(wantOffset))
	if loc.Range.Start != wantPos {
		t.Errorf("Definition landed at %+v, want Shape's own declaration at %+v", loc.Range.Start, wantPos)
	}
}

// TestAny_References_ParamFoundAtEveryUse covers References for the `a Any`
// parameter, expecting a hit at every one of its own uses in describe's body.
func TestAny_References_ParamFoundAtEveryUse(t *testing.T) {
	w, path := singleFileWorkspace(t, anyFixture)
	fa, _ := w.Analysis(path)

	offset := strings.Index(fa.Tree.File.Src, "AnyKind(a)") + len("AnyKind(")
	pos := byteOffsetToPosition(fa.Tree.File, lexer.Pos(offset))

	refs := w.References(path, pos, true)
	if len(refs) < 3 {
		t.Errorf("References(a) returned %d locations, want at least 3 (decl + 3 uses)", len(refs))
	}
}

// TestAny_DocumentHighlight_ParamHighlightsEveryUse mirrors the References
// test above for DocumentHighlight.
func TestAny_DocumentHighlight_ParamHighlightsEveryUse(t *testing.T) {
	w, path := singleFileWorkspace(t, anyFixture)
	fa, _ := w.Analysis(path)

	offset := strings.Index(fa.Tree.File.Src, "AnyKind(a)") + len("AnyKind(")
	pos := byteOffsetToPosition(fa.Tree.File, lexer.Pos(offset))

	highlights := w.DocumentHighlight(path, pos)
	if len(highlights) < 3 {
		t.Errorf("DocumentHighlight(a) returned %d ranges, want at least 3", len(highlights))
	}
}

// TestAny_Completion_BoxedVarVisibleInsideBody covers ordinary scope
// resolution for a local bound from Any(...)/AnyFields - both must be
// visible in completion after their own declaration point.
func TestAny_Completion_BoxedVarVisibleInsideBody(t *testing.T) {
	w, path := singleFileWorkspace(t, anyFixture)
	fa, _ := w.Analysis(path)

	offset := strings.Index(fa.Tree.File.Src, "return describe")
	pos := byteOffsetToPosition(fa.Tree.File, lexer.Pos(offset))

	items := w.Completion(path, pos)
	labels := completionLabels(items)
	for _, want := range []string{"boxed", "sum"} {
		if !slices.Contains(labels, want) {
			t.Errorf("completion labels = %v, missing %q", labels, want)
		}
	}
}

// TestAny_DocumentSymbolsAndFolding_FuncsUsingAny covers document symbols and
// folding for both functions in the fixture - AnyKind/AnyName/AnyAs/AnyFields
// calls must not confuse either pass.
func TestAny_DocumentSymbolsAndFolding_FuncsUsingAny(t *testing.T) {
	w, path := singleFileWorkspace(t, anyFixture)

	syms := w.DocumentSymbols(path)
	var names []string
	for _, s := range syms {
		names = append(names, s.Name)
	}
	for _, want := range []string{"Point", "describe", "describeArray", "main"} {
		if !slices.Contains(names, want) {
			t.Errorf("DocumentSymbols names %v missing %q", names, want)
		}
	}

	folds := w.FoldingRanges(path)
	if len(folds) == 0 {
		t.Fatal("FoldingRanges returned none - a function using Any must still fold like any other")
	}
}

// TestAny_SemanticTokens_NoCrashAndCoversBuiltinCalls covers semantic tokens
// over the whole fixture - AnyKind/AnyName/AnyAs/AnyFields are all builtin
// calls (Decl == ast.InvalidNode, see universeScope) the same shape as len/
// make/append already are, so this mostly proves that shape isn't
// mishandled for a brand new set of names.
func TestAny_SemanticTokens_NoCrashAndCoversBuiltinCalls(t *testing.T) {
	w, path := singleFileWorkspace(t, anyFixture)
	toks := w.SemanticTokens(path)
	if toks == nil || len(toks.Data) == 0 {
		t.Fatal("SemanticTokens returned no data for a file using Any/AnyKind/AnyName/AnyAs/AnyFields")
	}
}

// broken/incomplete-source variant every capability above needs to survive:
// AnyAs with no type argument (a real, rejected shape), AnyIndex with a
// missing second argument, plus a mid-typing `Any(` call with no closing
// paren yet.
func TestAny_MalformedAnySource_NoCrash(t *testing.T) {
	src := `func Bad(a Any) int {
	v, ok := AnyAs(a)
	if ok {
		return v
	}
	return 0
}

func BadIndex(a Any) int {
	v, ok := AnyIndex(a)
	return AnyLen(v)
}

func Typing(x int) int {
	a := Any(x
`
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("malformed Any source panicked: %v", r)
		}
	}()

	w, path := singleFileWorkspace(t, src)
	fa, _ := w.Analysis(path)
	offset := strings.Index(fa.Tree.File.Src, "Any(x") + len("Any(")
	pos := byteOffsetToPosition(fa.Tree.File, lexer.Pos(offset))

	w.Hover(path, pos)
	w.Completion(path, pos)
	w.DocumentSymbols(path)
	w.FoldingRanges(path)
	w.SemanticTokens(path)

	badOffset := strings.Index(fa.Tree.File.Src, "AnyAs(a)")
	badPos := byteOffsetToPosition(fa.Tree.File, lexer.Pos(badOffset))
	w.Hover(path, badPos)
	w.Definition(path, badPos)
	w.References(path, badPos, true)
	w.DocumentHighlight(path, badPos)
}
