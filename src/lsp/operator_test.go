// Operator-overloading coverage across every major LSP capability - see
// src/lsp/doc.go's own note on the expected shape (generics landed with
// zero such coverage and a real bug went unnoticed for several rounds).
// Mirrors generics_test.go's own structure: one shared fixture, one test per
// capability, plus a broken/incomplete-source variant at the end.
package lsp

import (
	"slices"
	"strings"
	"testing"

	"llvm_lang/src/lexer"
	"llvm_lang/src/sema"

	protocol "github.com/tliron/glsp/protocol_3_16"
)

const vector2OperatorFixture = `struct Vector2 {
	x f64
	y f64

	operator *(scalar f64) Vector2 {
		return Vector2{this.x * scalar, this.y * scalar}
	}
}

func f() f64 {
	v := Vector2{1, 2}
	scaled := v * 2.0
	return scaled.x
}
`

// TestOperator_Hover_Declaration covers hovering the operator symbol itself
// within an `operator` block's own declaration (the OperatorDecl node's own
// Tok - see ast.Node's doc comment) - mirroring
// TestHover_ConstructorDoesNotRepeatItsOwnKindWord one construct over:
// declareOperator (resolve.go) records the overload's own Symbol directly
// on the OperatorDecl node, the same "container node IS its own declaring
// Info.Refs entry" shape a constructor/destructor already uses.
func TestOperator_Hover_Declaration(t *testing.T) {
	w, path := singleFileWorkspace(t, vector2OperatorFixture)
	fa, _ := w.Analysis(path)

	offset := strings.Index(fa.Tree.File.Src, "operator *(scalar f64)") + len("operator ")
	pos := byteOffsetToPosition(fa.Tree.File, lexer.Pos(offset))

	hover := w.Hover(path, pos)
	if hover == nil {
		t.Fatal("Hover returned nil for an operator overload's own declaration")
	}
	content, ok := hover.Contents.(protocol.MarkupContent)
	if !ok {
		t.Fatalf("hover.Contents = %T, want protocol.MarkupContent", hover.Contents)
	}
	if !strings.Contains(content.Value, "operator") || !strings.Contains(content.Value, "Vector2") {
		t.Errorf("hover content = %q, want it to mention both \"operator\" and \"Vector2\"", content.Value)
	}
}

// TestOperator_Hover_UseSite covers hovering the operator symbol at a
// resolved use site (`v * 2.0`) - checkBinaryExpr's own fallback path
// (typecheck.go) records the selected overload's Symbol directly on the
// whole BinaryExpr node's own Info.Refs entry, so Hover's existing
// Info.Refs-driven rendering picks it up with no LSP-layer changes needed
// at all.
func TestOperator_Hover_UseSite(t *testing.T) {
	w, path := singleFileWorkspace(t, vector2OperatorFixture)
	fa, _ := w.Analysis(path)

	offset := strings.Index(fa.Tree.File.Src, "v * 2.0") + len("v ")
	pos := byteOffsetToPosition(fa.Tree.File, lexer.Pos(offset))

	hover := w.Hover(path, pos)
	if hover == nil {
		t.Fatal("Hover returned nil at a `v * 2.0` use site resolved to an operator overload")
	}
	content, ok := hover.Contents.(protocol.MarkupContent)
	if !ok {
		t.Fatalf("hover.Contents = %T, want protocol.MarkupContent", hover.Contents)
	}
	if !strings.Contains(content.Value, "operator") {
		t.Errorf("hover content = %q, want it to mention \"operator\" (the resolved overload's own kind)", content.Value)
	}
}

// TestOperator_References_DeclarationFindsUseSiteAndExcludesItself covers
// find-references from an operator's own declaration: it must find the one
// real use site (`v * 2.0`), and - the specific regression this test
// guards - correctly exclude the declaration's own occurrence when
// includeDeclaration is false. That exclusion depends on
// Symbol.DeclaringNameNode (sema/scope.go) recognizing OperatorDecl as
// "IS its own declaring Info.Refs entry", the same case
// ConstructorDecl/DestructorDecl already have; before that case existed,
// DeclaringNameNode fell through to its default (Child(s.Decl, 0) - the
// operator's own ParamList, never a real Info.Refs key), so the
// declaration's own occurrence was never recognized as "the declaration" at
// all and leaked into the includeDeclaration=false result.
func TestOperator_References_DeclarationFindsUseSiteAndExcludesItself(t *testing.T) {
	w, path := singleFileWorkspace(t, vector2OperatorFixture)
	fa, _ := w.Analysis(path)

	declOffset := strings.Index(fa.Tree.File.Src, "operator *(scalar f64)") + len("operator ")
	pos := byteOffsetToPosition(fa.Tree.File, lexer.Pos(declOffset))

	withDecl := w.References(path, pos, true)
	if len(withDecl) != 2 {
		t.Fatalf("References(includeDeclaration=true) len = %d, want 2 (the declaration + the `v * 2.0` use site): %+v", len(withDecl), withDecl)
	}

	withoutDecl := w.References(path, pos, false)
	if len(withoutDecl) != 1 {
		t.Fatalf("References(includeDeclaration=false) len = %d, want 1 (just the use site, declaration excluded): %+v", len(withoutDecl), withoutDecl)
	}

	useOffset := strings.Index(fa.Tree.File.Src, "v * 2.0")
	usePos := byteOffsetToPosition(fa.Tree.File, lexer.Pos(useOffset))
	if withoutDecl[0].Range.Start.Line != usePos.Line {
		t.Errorf("References(includeDeclaration=false)[0] = %+v, want it on the `v * 2.0` use site's own line (%d)", withoutDecl[0], usePos.Line)
	}
}

// TestOperator_SemanticTokens_DeclarationAndUseSite covers both halves the
// coordinator's own review flagged: the operator symbol classifies via
// classifyNodeToken's existing lexeme-based switch (semTokOperator) - not a
// node-kind switch, so nothing about this feature needed to touch
// semantictokens.go at all - while the leading `operator` keyword itself
// (never captured as any node's own Tok - see ast.Node's OperatorDecl doc
// comment) still gets classified as semTokKeyword via
// collectLexicalExtras' own uncaptured-keyword re-lex fallback (mirroring
// TestSemanticTokens_UncapturedKeywords one keyword over - unlike
// semanticTokenAt, which only runs collectNodeTokens, this needs both
// passes to see an uncaptured keyword at all). The use site's own `*`
// classifies identically, proving this was never dependent on whether the
// token happens to sit inside a declaration or a use.
func TestOperator_SemanticTokens_DeclarationAndUseSite(t *testing.T) {
	w, path := singleFileWorkspace(t, vector2OperatorFixture)
	fa, _ := w.Analysis(path)
	src := fa.Tree.File.Src

	reassigned := make(map[*sema.Symbol]bool)
	collectReassignedSymbols(fa.Tree, fa.Info, fa.Tree.Root, reassigned)
	covered := make(map[lexer.Pos]bool)
	var raw []rawToken
	collectNodeTokens(fa.Tree, fa.Info, fa.Tree.Root, reassigned, covered, &raw)
	collectLexicalExtras(fa.Tree.File.Name, src, covered, &raw)

	tokenAt := func(offset int) (rawToken, bool) {
		want := byteOffsetToPosition(fa.Tree.File, lexer.Pos(offset))
		for _, tok := range raw {
			if uint32(tok.line) == want.Line && uint32(tok.char) == want.Character {
				return tok, true
			}
		}
		return rawToken{}, false
	}

	kwOffset := strings.Index(src, "operator *(scalar f64)")
	kwTok, ok := tokenAt(kwOffset)
	if !ok {
		t.Fatal("no semantic token emitted for the `operator` keyword itself")
	}
	if kwTok.typeIdx != semTokKeyword {
		t.Errorf("`operator` keyword token type = %d, want semTokKeyword (%d)", kwTok.typeIdx, semTokKeyword)
	}

	declOpOffset := kwOffset + len("operator ")
	declOpTok, ok := tokenAt(declOpOffset)
	if !ok {
		t.Fatal("no semantic token emitted for the declaration's own `*` symbol")
	}
	if declOpTok.typeIdx != semTokOperator {
		t.Errorf("declaration `*` token type = %d, want semTokOperator (%d)", declOpTok.typeIdx, semTokOperator)
	}

	useOpOffset := strings.Index(src, "v * 2.0") + len("v ")
	useOpTok, ok := tokenAt(useOpOffset)
	if !ok {
		t.Fatal("no semantic token emitted for the use site's own `*` symbol")
	}
	if useOpTok.typeIdx != semTokOperator {
		t.Errorf("use-site `*` token type = %d, want semTokOperator (%d)", useOpTok.typeIdx, semTokOperator)
	}
}

// TestOperator_Completion_ThisInsideOperatorBodyListsFieldsAndMethods
// covers `this.<cursor>` completion inside an operator body - resolved via
// resolveOperatorBody (resolve.go) setting the same fnScope.Receiver.
// StructInfo an ordinary constructor/method body already gets, so
// memberCompletions sees Vector2's real fields with no operator-specific
// completion.go changes needed.
func TestOperator_Completion_ThisInsideOperatorBodyListsFieldsAndMethods(t *testing.T) {
	src := `struct Vector2 {
	x f64
	y f64

	operator *(scalar f64) Vector2 {
		return Vector2{this.x * scalar, this.
	}
}
`
	w, path := singleFileWorkspace(t, src)
	fa, _ := w.Analysis(path)
	offset := strings.Index(fa.Tree.File.Src, "this.x * scalar, this.") + len("this.x * scalar, this.")
	pos := byteOffsetToPosition(fa.Tree.File, lexer.Pos(offset))

	items := w.Completion(path, pos)
	labels := completionLabels(items)
	for _, want := range []string{"x", "y"} {
		if !slices.Contains(labels, want) {
			t.Errorf("completion labels at this.<cursor> inside an operator body = %v, missing %q", labels, want)
		}
	}
}

// TestOperator_BrokenSource_NoCrash is the broken/incomplete-source variant
// every capability above needs to survive (src/lsp/doc.go's own standard):
// a struct declaring a syntactically malformed operator block (missing
// operator symbol entirely - see parser's own
// TestOperatorDeclMissingOperatorSymbolIsError) must not crash any
// capability, even though the whole package fails to fully check.
func TestOperator_BrokenSource_NoCrash(t *testing.T) {
	src := `struct Vector2 {
	x f64

	operator (scalar f64) Vector2 {
		return Vector2{this.x}
	}
}
func f() {
	v := Vector2{1}
	bad := v * 2.0
}
`
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("a malformed operator block panicked: %v", r)
		}
	}()

	w, path := singleFileWorkspace(t, src)
	fa, _ := w.Analysis(path)
	offset := strings.Index(fa.Tree.File.Src, "bad := v * 2.0") + len("bad := v ")
	pos := byteOffsetToPosition(fa.Tree.File, lexer.Pos(offset))

	w.Hover(path, pos)
	w.Completion(path, pos)
	w.References(path, pos, true)
	w.DocumentSymbols(path)
	w.FoldingRanges(path)
	w.SemanticTokens(path)
}
