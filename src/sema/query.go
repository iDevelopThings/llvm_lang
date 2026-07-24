package sema

import (
	"iter"

	"llvm_lang/src/ast"
)

// EnclosingScope returns the nearest Scope that owns n - walking n up
// through tree.Parent until a node with an Info.Scopes entry is found.
// Info.Scopes is only populated on scope-introducing nodes (Block,
// FuncDecl, ...), not every node, so most callers (an LSP asking "what's
// visible at this cursor") need this walk rather than a direct map lookup.
// Returns nil if n has no scope-owning ancestor at all (n itself is
// InvalidNode, or somehow outside tree).
func (i *Info) EnclosingScope(tree *ast.Tree, n ast.NodeIndex) *Scope {
	for n != ast.InvalidNode {
		if s, ok := i.Scopes[n]; ok {
			return s
		}
		n = tree.Parent(n)
	}
	return nil
}

// Visible yields every symbol reachable from s: s's own names, then each
// enclosing scope's in turn, out to the universe scope - a nearer scope's
// symbol shadows an outer scope's same-named one, matching Lookup's own
// resolution order. This single walk already covers locals, params, the
// method receiver, package-level decls, and file-scoped import bindings,
// since they're all just entries at different levels of the same parent
// chain.
func (s *Scope) Visible() iter.Seq[*Symbol] {
	return func(yield func(*Symbol) bool) {
		seen := make(map[string]bool)
		for sc := s; sc != nil; sc = sc.Parent {
			for name, sym := range sc.names {
				if seen[name] {
					continue
				}
				seen[name] = true
				if !yield(sym) {
					return
				}
			}
		}
	}
}

// Local yields only s's own directly-declared symbols, without walking to
// any enclosing scope - the enumeration counterpart to LookupLocal, for a
// caller that already has a specific scope in hand (e.g. an already-
// imported package's own top-level Scope, via Symbol.Package.Scope) and
// wants exactly its own names, not everything reachable from within it.
func (s *Scope) Local() iter.Seq[*Symbol] {
	return func(yield func(*Symbol) bool) {
		for _, sym := range s.names {
			if !yield(sym) {
				return
			}
		}
	}
}
