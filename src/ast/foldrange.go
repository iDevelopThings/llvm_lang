package ast

import (
	"strings"

	"llvm_lang/src/enums"
	"llvm_lang/src/lexer"
)

// FoldKind classifies a FoldRange - mirrors LSP's own FoldingRangeKind
// concept generically (comment/imports/plain region), for the same reason
// SymbolOutlineKind mirrors LSP's SymbolKind - so a consumer other than an
// LSP server could use this too.
type FoldKind int

const (
	FoldRegion FoldKind = iota // a plain code block - no special kind
	FoldComment
	FoldImports
)

// FoldRange is one foldable region: its full extent (Span) and kind.
type FoldRange struct {
	Span Span
	Kind FoldKind
}

// FoldRanges returns every foldable region in t that spans more than one
// line: a Block (a function/if/for/match/... body - see Node's own doc
// comment for every kind that carries one) or a StructDecl/EnumDecl's own
// brace body; a run of 2+ consecutive top-level import declarations
// (LANGUAGE.md's "Imports" section requires every ImportDecl to come first
// in the file, so the first and last one found already bound the whole
// contiguous run with no need to check adjacency); and a run of 2+
// consecutive line comments (see collectCommentFoldRanges).
func (t *Tree) FoldRanges() []FoldRange {
	var out []FoldRange
	t.collectNodeFoldRanges(t.Root, &out)
	t.collectImportFoldRange(&out)
	collectCommentFoldRanges(t.File.Name, t.File.Src, &out)
	return out
}

func (t *Tree) collectNodeFoldRanges(n NodeIndex, out *[]FoldRange) {
	if n == InvalidNode {
		return
	}
	switch t.Nodes[n].Kind {
	case enums.NodeKinds.Block,
		enums.NodeKinds.StructDecl,
		enums.NodeKinds.EnumDecl:
		appendFoldRange(t.File, t.SpanOf(n), FoldRegion, out)
	}
	for _, c := range t.Children(n) {
		t.collectNodeFoldRanges(c, out)
	}
}

func (t *Tree) collectImportFoldRange(out *[]FoldRange) {
	var first, last NodeIndex
	count := 0
	for decl := range t.TopLevelDeclsOfKind(enums.NodeKinds.ImportDecl) {
		if count == 0 {
			first = decl
		}
		last = decl
		count++
	}
	if count < 2 {
		return
	}
	appendFoldRange(t.File, Span{
		Start: t.SpanOf(first).Start,
		End:   t.SpanOf(last).End,
	}, FoldImports, out)
}

// collectCommentFoldRanges re-lexes src (a fresh, throwaway File/Lexer,
// deliberately not t.File - comments aren't represented as Nodes at all, so
// reaching them needs a real re-lex, and using a second File instead of
// t's own can't perturb its already-built trivia arena/line table) and
// folds every contiguous run of 2+ line comments as one region, stopping a
// run at a blank line (2+ newlines in an intervening whitespace run) or a
// block comment. Walking the file's whole trivia arena as one flat,
// already-in-source-order slice (bounded by the EOF token's own
// LeadingTrivia.End(), the arena's final index) is simpler than
// re-deriving order from per-token trivia batches.
func collectCommentFoldRanges(name, src string, out *[]FoldRange) {
	file := lexer.NewFile(name, src)
	lx := lexer.New(file)
	var lastTrivia lexer.Range
	for tok := range lx.All() {
		lastTrivia = tok.LeadingTrivia
		if tok.Lexeme == enums.Lexemes.EOF {
			break
		}
	}

	var run []lexer.Trivia
	flush := func() {
		if len(run) >= 2 {
			appendFoldRange(file, Span{
				Start: run[0].Start,
				End:   run[len(run)-1].End,
			}, FoldComment, out)
		}
		run = nil
	}
	for _, tr := range file.Trivia(lexer.Range{Start: 0, Count: lastTrivia.End()}) {
		switch tr.Kind {
		case lexer.TriviaKinds.LineComment:
			run = append(run, tr)
		case lexer.TriviaKinds.BlockComment:
			flush()
		case lexer.TriviaKinds.Whitespace:
			if strings.Count(file.TriviaText(tr), "\n") >= 2 {
				flush()
			}
		}
	}
	flush()
}

// appendFoldRange converts span to a FoldRange, skipping it entirely if it
// doesn't actually cross a line boundary - folding a single-line span would
// hide nothing and isn't a real folding region.
func appendFoldRange(file *lexer.File, span Span, kind FoldKind, out *[]FoldRange) {
	startLine := file.Position(span.Start).Line
	endLine := file.Position(span.End).Line
	if startLine >= endLine {
		return
	}
	*out = append(*out, FoldRange{Span: span, Kind: kind})
}
