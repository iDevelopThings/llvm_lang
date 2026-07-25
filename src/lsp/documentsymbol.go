package lsp

import (
	"llvm_lang/src/ast"
	"llvm_lang/src/lexer"

	protocol "github.com/tliron/glsp/protocol_3_16"
)

// DocumentSymbols answers a textDocument/documentSymbol request: path's own
// hierarchical outline (ast.Tree.DeclSymbols), translated into LSP's wire
// format - including each declaration's own signature/field-list summary as
// DocumentSymbol.Detail, rendered from source rather than resolved types
// (see ast.DeclSymbol.Detail). nil when path has no analysis yet.
func (w *Workspace) DocumentSymbols(path string) []protocol.DocumentSymbol {
	fa, ok := w.Analysis(path)
	if !ok || fa.Tree == nil {
		return nil
	}
	return toProtocolSymbols(fa.Tree.File, fa.Tree.DeclSymbols())
}

func toProtocolSymbols(file *lexer.File, syms []ast.DeclSymbol) []protocol.DocumentSymbol {
	if len(syms) == 0 {
		return nil
	}
	out := make([]protocol.DocumentSymbol, len(syms))
	for i, s := range syms {
		out[i] = protocol.DocumentSymbol{
			Name:           s.Name,
			Kind:           toProtocolSymbolKind(s.Kind),
			Range:          spanToRange(file, s.Span),
			SelectionRange: spanToRange(file, s.NameSpan),
			Children:       toProtocolSymbols(file, s.Children),
		}
		if s.Detail != "" {
			detail := s.Detail
			out[i].Detail = &detail
		}
	}
	return out
}

func toProtocolSymbolKind(kind ast.SymbolOutlineKind) protocol.SymbolKind {
	switch kind {
	case ast.SymbolOutlineFunction:
		return protocol.SymbolKindFunction
	case ast.SymbolOutlineMethod:
		return protocol.SymbolKindMethod
	case ast.SymbolOutlineStruct:
		return protocol.SymbolKindStruct
	case ast.SymbolOutlineEnum:
		return protocol.SymbolKindEnum
	case ast.SymbolOutlineField:
		return protocol.SymbolKindField
	case ast.SymbolOutlineConstructor:
		return protocol.SymbolKindConstructor
	case ast.SymbolOutlineDestructor:
		// LSP's SymbolKind has no dedicated destructor kind - Method is the
		// closer of the two special-method kinds it does have.
		return protocol.SymbolKindMethod
	case ast.SymbolOutlineEnumMember:
		return protocol.SymbolKindEnumMember
	default: // SymbolOutlineVariable
		return protocol.SymbolKindVariable
	}
}
