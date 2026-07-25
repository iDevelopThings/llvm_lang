package lsp

import (
	"llvm_lang/src/ast"
	"llvm_lang/src/sema"

	protocol "github.com/tliron/glsp/protocol_3_16"
)

// References answers a textDocument/references request: every occurrence of
// whatever symbol resolves at pos, across every file this Workspace has
// analyzed as part of the *same* recompute as path's own current analysis
// (see FileAnalysis.Generation's own doc comment for why cross-generation
// Symbol pointers are never safely comparable) - optionally including the
// declaration's own occurrence, per includeDeclaration (LSP's own
// ReferenceContext.IncludeDeclaration), identified via
// sema.Symbol.DeclaringNameNode.
//
// Scoped to the current recompute's own file set, which - same limitation
// as Definition/diagnostics (see Workspace.OpenOrChange's own doc comment)
// - means a downstream package that imports this one but hasn't itself been
// opened/analyzed yet contributes no results, even if it references this
// symbol.
func (w *Workspace) References(path string, pos protocol.Position, includeDeclaration bool) []protocol.Location {
	fa, n, ok := w.resolveNode(path, pos)
	if !ok {
		return nil
	}
	sym, ok := fa.Info.Refs[n]
	if !ok || sym == nil {
		return nil
	}

	// A monomorphized generic's own template (a free func/struct, or a
	// struct method - see Symbol.GenericInstances) and each of its
	// instantiations (Sum[int], Sum[f64], ...) are, from a user's point of
	// view, all "the same" Sum - clicking any one of them (the
	// declaration, or one call site) must find every other. declSym is
	// whichever of the pair is the real declaring Symbol, the one
	// DeclaringNameNode/Tree actually make sense against - an
	// instantiation's own Decl points at a synthetic clone (see
	// ast.Tree.CloneSubtree), already excluded from every scan below by
	// the RootAncestor check.
	targets := map[*sema.Symbol]bool{sym: true}
	declSym := sym
	if sym.GenericTemplate != nil {
		declSym = sym.GenericTemplate
		targets[declSym] = true
	}
	for inst := range declSym.GenericInstances() {
		targets[inst] = true
	}

	var declNameNode ast.NodeIndex
	if declSym.Tree != nil {
		declNameNode = declSym.DeclaringNameNode(declSym.Tree)
	}

	var locs []protocol.Location
	for _, other := range w.analysisSnapshot() {
		if other.Info == nil || other.Generation != fa.Generation {
			continue
		}
		for refNode, refSym := range other.Info.Refs {
			if !targets[refSym] {
				continue
			}
			if other.Tree.RootAncestor(refNode) != other.Tree.Root {
				// A monomorphized-generic instantiation's own clone (see
				// ast.Tree.CloneSubtree) - same symbol, but not a real
				// occurrence in this file's own source text, just an
				// internal copy Check re-resolved. Its own call site (a
				// real, visible occurrence) is a separate Refs entry,
				// reported normally.
				continue
			}
			if !includeDeclaration && other.Tree == declSym.Tree && refNode == declNameNode {
				continue
			}
			locs = append(locs, protocol.Location{
				URI:   URIFromPath(other.Tree.File.Name),
				Range: spanToRange(other.Tree.File, other.Tree.SpanOf(refNode)),
			})
		}
	}
	return locs
}
