// TypeId/TypeIdOf/TypeByName/AnyNew/AnySet builtin coverage - the type
// registry closing out the Any/reflection effort any_test.go already covers
// for AnyKind/AnyName/AnyAs/AnyFields/AnyLen/AnyIndex. Minimal, targeted
// additions rather than a parallel full sweep: TypeId[T]/AnySet[T] share
// AnyAs[T]'s own explicit-type-argument shape (so a Definition test on their
// own type argument is the one capability worth its own coverage - every
// other capability's handling of a predeclared builtin call is already
// proven generically by any_test.go), plus one semantic-tokens/folding smoke
// test and one malformed-source variant per src/lsp/doc.go's own standing
// convention.
package lsp

import (
	"strings"
	"testing"

	"llvm_lang/src/lexer"
)

// TestTypeRegistry_Definition_TypeIdTypeArgLandsOnDecl mirrors
// TestAny_Definition_BoxedStructFieldLandsOnDecl for TypeId[Point]'s own
// type argument - the identical no-Decl-Generic call shape as AnyAs[T].
func TestTypeRegistry_Definition_TypeIdTypeArgLandsOnDecl(t *testing.T) {
	src := `struct Point {
	X int
}

func f() int {
	return TypeId[Point]()
}
`
	w, path := singleFileWorkspace(t, src)
	fa, _ := w.Analysis(path)

	offset := strings.Index(fa.Tree.File.Src, "TypeId[Point]") + len("TypeId[")
	pos := byteOffsetToPosition(fa.Tree.File, lexer.Pos(offset))

	loc := w.Definition(path, pos)
	if loc == nil {
		t.Fatal("Definition returned nil for TypeId[Point]'s own type argument")
	}
	wantOffset := strings.Index(fa.Tree.File.Src, "struct Point") + len("struct ")
	wantPos := byteOffsetToPosition(fa.Tree.File, lexer.Pos(wantOffset))
	if loc.Range.Start != wantPos {
		t.Errorf("Definition landed at %+v, want Point's own declaration at %+v", loc.Range.Start, wantPos)
	}
}

// TestTypeRegistry_Hover_TypeIdOfArgShowsItsOwnType covers hovering an
// ordinary value argument passed to TypeIdOf - proving the new builtin
// doesn't disturb ordinary hover resolution for its own argument.
func TestTypeRegistry_Hover_TypeIdOfArgShowsItsOwnType(t *testing.T) {
	src := `func f(x int) int {
	return TypeIdOf(x)
}
`
	w, path := singleFileWorkspace(t, src)
	fa, _ := w.Analysis(path)

	offset := strings.Index(fa.Tree.File.Src, "TypeIdOf(x)") + len("TypeIdOf(")
	pos := byteOffsetToPosition(fa.Tree.File, lexer.Pos(offset))

	text := hoverText(t, w.Hover(path, pos))
	if !strings.Contains(text, "int") {
		t.Errorf("Hover(x) = %q, want it to contain %q", text, "int")
	}
}

// TestTypeRegistry_SemanticTokensAndFolding_NoCrash covers a fixture
// exercising all five builtins together - TypeId[T]/TypeIdOf/TypeByName/
// AnyNew/AnySet[T] - proving none of them confuse semantic tokens or
// folding, the same shape TestAny_SemanticTokens_NoCrashAndCoversBuiltinCalls
// already establishes for the earlier Any builtins.
func TestTypeRegistry_SemanticTokensAndFolding_NoCrash(t *testing.T) {
	src := `struct Point {
	X int
}

func f() bool {
	id := TypeId[Point]()
	p := Point{X: 1}
	other := TypeIdOf(p)
	ids := TypeByName("Point")
	a, ok := AnyNew(id)
	if !ok {
		return false
	}
	for name, v := range AnyFields(a) {
		AnySet[int](v, 5)
	}
	return id == other && len(ids) == 1
}
`
	w, path := singleFileWorkspace(t, src)
	toks := w.SemanticTokens(path)
	if toks == nil || len(toks.Data) == 0 {
		t.Fatal("SemanticTokens returned no data for a file using TypeId/TypeIdOf/TypeByName/AnyNew/AnySet")
	}
	if folds := w.FoldingRanges(path); len(folds) == 0 {
		t.Fatal("FoldingRanges returned none for a function using the type registry builtins")
	}
}

// TestTypeRegistry_MalformedSource_NoCrash covers a missing type argument
// (TypeId takes none, AnySet needs one), a wrong argument count, and a
// mid-typing TypeByName( call with no closing paren - the same broken/
// incomplete-source shapes any_test.go's own TestAny_MalformedAnySource_NoCrash
// covers for the earlier builtins.
func TestTypeRegistry_MalformedSource_NoCrash(t *testing.T) {
	src := `func Bad() int {
	id := TypeId()
	return id
}

func BadSet(a Any) bool {
	return AnySet(a, 5)
}

func Typing(name string) int {
	ids := TypeByName(name
`
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("malformed type-registry source panicked: %v", r)
		}
	}()

	w, path := singleFileWorkspace(t, src)
	fa, _ := w.Analysis(path)

	offset := strings.Index(fa.Tree.File.Src, "TypeByName(name") + len("TypeByName(")
	pos := byteOffsetToPosition(fa.Tree.File, lexer.Pos(offset))
	w.Hover(path, pos)
	w.Completion(path, pos)
	w.DocumentSymbols(path)
	w.FoldingRanges(path)
	w.SemanticTokens(path)

	badOffset := strings.Index(fa.Tree.File.Src, "AnySet(a, 5)")
	badPos := byteOffsetToPosition(fa.Tree.File, lexer.Pos(badOffset))
	w.Hover(path, badPos)
	w.Definition(path, badPos)
	w.References(path, badPos, true)
	w.DocumentHighlight(path, badPos)
}
