package lsp

import (
	"strings"

	"llvm_lang/src/ast"
	"llvm_lang/src/enums"
	"llvm_lang/src/lexer"

	protocol "github.com/tliron/glsp/protocol_3_16"
)

// FoldingRanges answers a textDocument/foldingRange request: every
// multi-line Block/StructDecl/EnumDecl body, a run of 2+ consecutive
// top-level import declarations, and a run of 2+ consecutive line comments -
// nil when path has no analysis yet.
func (w *Workspace) FoldingRanges(path string) []protocol.FoldingRange {
	fa, ok := w.Analysis(path)
	if !ok || fa.Tree == nil {
		return nil
	}

	var out []protocol.FoldingRange
	collectNodeFoldingRanges(fa.Tree, fa.Tree.Root, &out)
	collectImportFoldingRange(fa.Tree, &out)
	collectCommentFoldingRanges(fa.Tree.File.Name, fa.Tree.File.Src, &out)
	return out
}

// collectNodeFoldingRanges walks tree from n, folding every Block (a
// function/if/for/match/... body - see ast.Node's own doc comment for every
// kind that carries one) and every StructDecl/EnumDecl (their own brace
// body, not a separate Block child) that spans more than one line.
func collectNodeFoldingRanges(tree *ast.Tree, n ast.NodeIndex, out *[]protocol.FoldingRange) {
	if n == ast.InvalidNode {
		return
	}
	switch tree.Nodes[n].Kind {
	case enums.NodeKinds.Block,
		enums.NodeKinds.StructDecl,
		enums.NodeKinds.EnumDecl:
		appendFoldingRange(tree.File, tree.SpanOf(n), "", out)
	}
	for _, c := range tree.Children(n) {
		collectNodeFoldingRanges(tree, c, out)
	}
}

// collectImportFoldingRange folds the whole run of top-level import
// declarations as one region, when there are 2 or more - LANGUAGE.md's
// "Imports" section requires every ImportDecl to come first in the file, so
// the first and last one found already bound the whole contiguous run with
// no need to check adjacency.
func collectImportFoldingRange(tree *ast.Tree, out *[]protocol.FoldingRange) {
	var first, last ast.NodeIndex
	count := 0
	for decl := range tree.TopLevelDeclsOfKind(enums.NodeKinds.ImportDecl) {
		if count == 0 {
			first = decl
		}
		last = decl
		count++
	}
	if count < 2 {
		return
	}
	appendFoldingRange(tree.File, ast.Span{
		Start: tree.SpanOf(first).Start,
		End:   tree.SpanOf(last).End,
	}, protocol.FoldingRangeKindImports, out)
}

// collectCommentFoldingRanges re-lexes src (see SemanticTokens' own doc
// comment for why this is a fresh, throwaway File/Lexer - comments aren't
// represented as ast.Nodes at all) and folds every contiguous run of 2+ line
// comments as one "comment" region, stopping a run at a blank line (2+
// newlines in an intervening whitespace run) or a block comment. Walking the
// file's whole trivia arena as one flat, already-in-source-order slice
// (bounded by the EOF token's own LeadingTrivia.End(), the arena's final
// index) is simpler than re-deriving order from per-token trivia batches.
func collectCommentFoldingRanges(name, src string, out *[]protocol.FoldingRange) {
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
			appendFoldingRange(file, ast.Span{
				Start: run[0].Start,
				End:   run[len(run)-1].End,
			}, protocol.FoldingRangeKindComment, out)
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

// appendFoldingRange converts span to a FoldingRange, skipping it entirely
// if it doesn't actually cross a line boundary - folding a single-line span
// would hide nothing and isn't a real folding region. kind is one of the
// protocol.FoldingRangeKind* constants, or "" for an unkinded/generic range
// (a plain code block - only a comment/imports run gets a real kind).
func appendFoldingRange(file *lexer.File, span ast.Span, kind protocol.FoldingRangeKind, out *[]protocol.FoldingRange) {
	startLine := file.Position(span.Start).Line - 1 // 0-based
	endLine := file.Position(span.End).Line - 1
	if startLine >= endLine {
		return
	}
	fr := protocol.FoldingRange{
		StartLine: protocol.UInteger(startLine),
		EndLine:   protocol.UInteger(endLine),
	}
	if kind != "" {
		k := string(kind)
		fr.Kind = &k
	}
	*out = append(*out, fr)
}
