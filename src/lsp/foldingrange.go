package lsp

import (
	"llvm_lang/src/ast"

	protocol "github.com/tliron/glsp/protocol_3_16"
)

// FoldingRanges answers a textDocument/foldingRange request: path's own
// foldable regions (ast.Tree.FoldRanges), translated into LSP's wire
// format. nil when path has no analysis yet.
//
// StartCharacter/EndCharacter are both spec-optional, but omitting them is
// a real client-side footgun: LSP4IJ's own fallback for a missing
// endCharacter (document.getLineEndOffset(endLine), then scanning forward
// for the next literal `}`) starts one character past this project's own
// already-correct closing brace, so it can walk straight past it into an
// unrelated later `}` - visually folding far more than intended. Always
// sending the exact character position sidesteps that guesswork entirely.
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
		startChar, endChar := rng.Start.Character, rng.End.Character
		out[i] = protocol.FoldingRange{
			StartLine:      rng.Start.Line,
			StartCharacter: &startChar,
			EndLine:        rng.End.Line,
			EndCharacter:   &endChar,
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
