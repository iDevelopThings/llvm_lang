package lsp

import (
	"llvm_lang/src/ast"

	protocol "github.com/tliron/glsp/protocol_3_16"
)

// FoldingRanges answers a textDocument/foldingRange request: path's own
// foldable regions (ast.Tree.FoldRanges), translated into LSP's wire
// format. nil when path has no analysis yet.
func (w *Workspace) FoldingRanges(path string) []protocol.FoldingRange {
	fa, ok := w.Analysis(path)
	if !ok || fa.Tree == nil {
		return nil
	}

	folds := fa.Tree.FoldRanges()
	if len(folds) == 0 {
		return nil
	}
	out := make([]protocol.FoldingRange, len(folds))
	for i, f := range folds {
		rng := spanToRange(fa.Tree.File, f.Span)
		out[i] = protocol.FoldingRange{
			StartLine: rng.Start.Line,
			EndLine:   rng.End.Line,
		}
		if kind := toProtocolFoldKind(f.Kind); kind != "" {
			k := string(kind)
			out[i].Kind = &k
		}
	}
	return out
}

func toProtocolFoldKind(kind ast.FoldKind) protocol.FoldingRangeKind {
	switch kind {
	case ast.FoldComment:
		return protocol.FoldingRangeKindComment
	case ast.FoldImports:
		return protocol.FoldingRangeKindImports
	default: // ast.FoldRegion
		return ""
	}
}
