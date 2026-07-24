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
// reaching them needs a real re-lex) and folds every contiguous run of 2+
// standalone line comments (see isStandaloneComment) as one region.
// Contiguity is decided by comparing consecutive standalone comments' own
// line numbers, not by counting blank-line newlines in between - automatic
// semicolon insertion splits a statement's trailing blank-line whitespace
// across two trivia chunks, which defeats a newline-counting heuristic.
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
	lastLine := 0
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
			if !isStandaloneComment(file, tr) {
				continue
			}
			line := file.Position(tr.Start).Line
			if len(run) > 0 && line != lastLine+1 {
				flush()
			}
			run = append(run, tr)
			lastLine = line
		case lexer.TriviaKinds.BlockComment:
			flush()
		}
	}
	flush()
}

// isStandaloneComment reports whether tr sits alone on its own source line -
// only leading whitespace precedes it - as opposed to trailing real code
// (e.g. `x := 1 // note`).
func isStandaloneComment(file *lexer.File, tr lexer.Trivia) bool {
	pos := file.Position(tr.Start)
	prefix := file.Line(pos.Line)[:pos.Column-1]
	return strings.TrimSpace(prefix) == ""
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
