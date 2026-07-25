// Generics coverage across every LSP capability - the actual root problem
// this whole round started from: nothing under src/lsp exercised generics
// at all, so a real user-visible regression (see semantictokens_test.go's
// own TestSemanticTokens_UnresolvedIdentifierGetsReadonlyFallback) went
// unnoticed. References/DocumentHighlight generics coverage lives in
// references_test.go/highlight_symbol_folding_test.go (the clone-
// duplication bug those fixed needed its own dedicated fixtures); Detail
// rendering's generics coverage lives in signature_test.go and
// sema/query_test.go. This file covers what isn't exercised anywhere else:
// Definition, DocumentSymbols, FoldingRanges, Hover and Completion
// specifically *inside* a generic template's own body (the tooling-pass
// fix's whole point), plus a broken/incomplete-source variant - per this
// project's own "invalid-path coverage, not just the happy path" standard.
package lsp

import (
	"slices"
	"strings"
	"testing"

	"llvm_lang/src/lexer"
	"llvm_lang/src/sema"

	protocol "github.com/tliron/glsp/protocol_3_16"
)

const genericStructFixture = `struct Box[T] {
	value T
}
func (Box[T]) Get() T {
	return this.value
}
func Sum[T](a T, b T) T {
	return a + b
}
func f() int {
	b := Box[int]{7}
	return b.Get()
}
`

func TestGenerics_Definition_InstantiatedCallSiteLandsOnRealDeclaration(t *testing.T) {
	w, path := singleFileWorkspace(t, genericStructFixture)
	fa, _ := w.Analysis(path)

	callOffset := strings.Index(fa.Tree.File.Src, "b.Get()") + len("b.")
	pos := byteOffsetToPosition(fa.Tree.File, lexer.Pos(callOffset))

	loc := w.Definition(path, pos)
	if loc == nil {
		t.Fatal("Definition returned nil for an instantiated method call site")
	}

	wantOffset := strings.Index(fa.Tree.File.Src, "Get()")
	wantPos := byteOffsetToPosition(fa.Tree.File, lexer.Pos(wantOffset))
	if loc.Range.Start != wantPos {
		t.Errorf("Definition landed at %+v, want the real declaration's own name at %+v", loc.Range.Start, wantPos)
	}
}

func TestGenerics_DocumentSymbols_ShowsTemplateAndMethods(t *testing.T) {
	w, path := singleFileWorkspace(t, genericStructFixture)

	syms := w.DocumentSymbols(path)
	var names []string
	for _, s := range syms {
		names = append(names, s.Name)
	}
	for _, want := range []string{"Box", "Get", "Sum", "f"} {
		if !slices.Contains(names, want) {
			t.Errorf("DocumentSymbols names %v missing %q (a generic template, or its method, silently dropped from the outline)", names, want)
		}
	}
}

func TestGenerics_FoldingRanges_TemplateBodyFolds(t *testing.T) {
	w, path := singleFileWorkspace(t, genericStructFixture)

	folds := w.FoldingRanges(path)
	if len(folds) == 0 {
		t.Fatal("FoldingRanges returned none - a generic struct/method/func body must still fold like any other")
	}
}

// TestGenerics_Hover_WorksInsideTemplateBody is the Hover-level regression
// test for the tooling-pass fix: a generic template's own body used to have
// no Info.Refs/Info.Types at all (only each instantiation's clone did), so
// hovering a parameter *inside* the template itself (not at a call site)
// returned nothing.
func TestGenerics_Hover_WorksInsideTemplateBody(t *testing.T) {
	w, path := singleFileWorkspace(t, genericStructFixture)
	fa, _ := w.Analysis(path)

	// The 'a' in Sum[T](a T, b T) T's own body: `return a + b`.
	offset := strings.Index(fa.Tree.File.Src, "return a + b") + len("return ")
	pos := byteOffsetToPosition(fa.Tree.File, lexer.Pos(offset))

	hover := w.Hover(path, pos)
	if hover == nil {
		t.Fatal("Hover returned nil for a parameter reference inside a generic template's own body")
	}
}

// TestGenerics_Hover_ExplicitInstantiationShowsSubstitutedSignature is the
// regression test for a real reported bug: hovering the bare name at an
// explicit-instantiation call site (NewBox[int](), not NewBox(x) with T
// inferred) showed the template's own unsubstituted "() Box[T]" - checkGenericCall
// only pointed the *whole* Name[T] IndexExpr's own Ref at the real
// specialization, never the bare name Ident nested inside it, so a hover
// landing on just "NewBox" (not the "[int]" part) still read genericRef's
// original template-pointing resolution.
func TestGenerics_Hover_ExplicitInstantiationShowsSubstitutedSignature(t *testing.T) {
	src := `struct Box[T] {
	v T
}
func NewBox[T]() Box[T] {
	return Box[T]{}
}
func main() int {
	b := NewBox[int]()
	print(b.v)
	return 0
}
`
	w, path := singleFileWorkspace(t, src)
	fa, _ := w.Analysis(path)

	offset := strings.Index(fa.Tree.File.Src, "NewBox[int]") + len("Ne")
	pos := byteOffsetToPosition(fa.Tree.File, lexer.Pos(offset))

	hover := w.Hover(path, pos)
	if hover == nil {
		t.Fatal("Hover returned nil at an explicit-instantiation call site")
	}
	content := hover.Contents.(protocol.MarkupContent)
	if !strings.Contains(content.Value, "() Box[int]") {
		t.Errorf("hover content = %q, want it to contain the substituted \"() Box[int]\", not the template's own \"() Box[T]\"", content.Value)
	}
}

// TestGenerics_Hover_FieldOfInstantiationShowsSubstitutedTypeAndLayout covers
// the struct-field hover feature (Size/Alignment/Offset, "in struct X") for
// a field belonging to an INSTANTIATED generic struct - the field's own
// Symbol.StructInfo must be the instantiation's own (Box[int]), not the
// template's (Box[T]), and its layout must reflect the substituted concrete
// type (int, size 4), not the template's unresolvable T.
func TestGenerics_Hover_FieldOfInstantiationShowsSubstitutedTypeAndLayout(t *testing.T) {
	src := `struct Box[T] {
	value T
}
func f() int {
	b := Box[int]{7}
	return b.value
}
`
	w, path := singleFileWorkspace(t, src)
	fa, _ := w.Analysis(path)

	offset := strings.LastIndex(fa.Tree.File.Src, "value")
	pos := byteOffsetToPosition(fa.Tree.File, lexer.Pos(offset))

	hover := w.Hover(path, pos)
	if hover == nil {
		t.Fatal("Hover returned nil at a generic instantiation's own field-access site")
	}
	content := hover.Contents.(protocol.MarkupContent)
	if !strings.Contains(content.Value, "field value int") {
		t.Errorf("hover content = %q, want the substituted \"field value int\", not the template's own \"field value T\"", content.Value)
	}
	if !strings.Contains(content.Value, "in struct `Box[int]`") {
		t.Errorf("hover content = %q, want it to name the instantiation \"Box[int]\", not the template \"Box[T]\"", content.Value)
	}
	if !strings.Contains(content.Value, "Size: 4\n") || !strings.Contains(content.Value, "Offset: 0") {
		t.Errorf("hover content = %q, want the substituted int's own Size: 4/Offset: 0", content.Value)
	}
}

// TestGenerics_Completion_IdentifierInsideTemplateBodySeesParams is the
// Completion-level regression test for the same tooling-pass fix: before
// it, identifier completion inside a generic body only ever saw
// package/universe scope - no params, no locals, since Info.Scopes had no
// entry for the template's own FuncDecl/Block nodes either.
func TestGenerics_Completion_IdentifierInsideTemplateBodySeesParams(t *testing.T) {
	w, path := singleFileWorkspace(t, genericStructFixture)
	fa, _ := w.Analysis(path)

	offset := strings.Index(fa.Tree.File.Src, "return a + b") + len("return ")
	pos := byteOffsetToPosition(fa.Tree.File, lexer.Pos(offset))

	items := w.Completion(path, pos)
	labels := completionLabels(items)
	for _, want := range []string{"a", "b"} {
		if !slices.Contains(labels, want) {
			t.Errorf("completion labels inside Sum[T]'s own body = %v, missing %q", labels, want)
		}
	}
}

// TestGenerics_Completion_ThisMemberInsideTemplateBodyListsFieldsAndMethods
// is the completion-level regression test for the same reported bug as
// TestReferences_GenericStructField_ThisAccessFindsFieldDeclaration:
// `this.<cursor>` inside a generic struct's own method body returned no
// suggestions at all - memberCompletions' own first check reads
// Info.Types[object], which a generic template's body never gets (only
// Check populates it, and templates never run Check), so it fell through
// all the way to the not-yet-imported-package fallback instead of listing
// this's own real fields/methods.
func TestGenerics_Completion_ThisMemberInsideTemplateBodyListsFieldsAndMethods(t *testing.T) {
	src := `struct Box[T] {
	value T
	other T
}
func (Box[T]) Get() T {
	return this.value
}
func (Box[T]) Set(v T) {
	this.`
	w, path := singleFileWorkspace(t, src)
	fa, _ := w.Analysis(path)
	pos := byteOffsetToPosition(fa.Tree.File, lexer.Pos(len(fa.Tree.File.Src)))

	items := w.Completion(path, pos)
	labels := completionLabels(items)
	for _, want := range []string{"value", "other", "Get"} {
		if !slices.Contains(labels, want) {
			t.Errorf("completion labels at this.<EOF> inside a generic method = %v, missing %q", labels, want)
		}
	}
}

// TestGenerics_SemanticTokens_ParameterInsideUninstantiatedTemplate is the
// assertion for the literal symptom this whole round started from: a
// never-instantiated generic function's body had no Refs at all, so every
// identifier in it fell back to variable+readonly - a wall of underlines
// under LSP4IJ's default color mapping. The parameter must classify as a
// real parameter token.
func TestGenerics_SemanticTokens_ParameterInsideUninstantiatedTemplate(t *testing.T) {
	src := `func Sum[T](a T, b T) T {
	return a + b
}
`
	w, path := singleFileWorkspace(t, src)
	fa, _ := w.Analysis(path)

	offset := strings.Index(src, "return a + b") + len("return ")
	tok, ok := semanticTokenAt(fa, lexer.Pos(offset))
	if !ok {
		t.Fatal("no semantic token emitted for 'a' inside the template body")
	}
	if tok.typeIdx != semTokParameter {
		t.Errorf("token type = %d, want semTokParameter (%d) - an uninstantiated template's own body fell back to the unresolved-identifier classification",
			tok.typeIdx, semTokParameter)
	}
}

// semanticTokenAt runs SemanticTokens' own collection passes over fa and
// returns whichever token starts at offset.
func semanticTokenAt(fa *FileAnalysis, offset lexer.Pos) (rawToken, bool) {
	reassigned := make(map[*sema.Symbol]bool)
	collectReassignedSymbols(fa.Tree, fa.Info, fa.Tree.Root, reassigned)

	covered := make(map[lexer.Pos]bool)
	var raw []rawToken
	collectNodeTokens(fa.Tree, fa.Info, fa.Tree.Root, reassigned, covered, &raw)

	want := byteOffsetToPosition(fa.Tree.File, offset)
	for _, tok := range raw {
		if uint32(tok.line) == want.Line && uint32(tok.char) == want.Character {
			return tok, true
		}
	}
	return rawToken{}, false
}

// TestGenerics_DanglingMemberAccessInsideGenericMethod_NoCrash is the
// broken/incomplete-source variant every capability above needs to survive
// (per this project's own invalid-path-coverage standard): `this.` with
// nothing typed after it yet, inside a generic method's own body - a
// dangling member access is itself a parse error by construction (see
// completion.go's own doc comment on why that's already handled at the
// frontend.RunProgram level), now compounded with the template-body gap
// this round's fix targets.
func TestGenerics_DanglingMemberAccessInsideGenericMethod_NoCrash(t *testing.T) {
	src := `struct Box[T] {
	value T
}
func (Box[T]) Get() T {
	this.
}
`
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("a dangling member access inside a generic method panicked: %v", r)
		}
	}()

	w, path := singleFileWorkspace(t, src)
	fa, _ := w.Analysis(path)
	offset := strings.Index(fa.Tree.File.Src, "this.\n") + len("this.")
	pos := byteOffsetToPosition(fa.Tree.File, lexer.Pos(offset))

	w.Hover(path, pos)
	w.Completion(path, pos)
	w.DocumentSymbols(path)
	w.FoldingRanges(path)
	w.SemanticTokens(path)
}
