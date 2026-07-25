package sema

import (
	"iter"
	"strings"

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

// FuncSignatureText renders decl's own parameter list and return type as a
// compact "(name Type, ...) Return" string - Type-first via info.Types
// (nil-safe: a nil info, or one missing an entry - an unchecked generic
// template, see ResolveTemplateForTooling's own doc comment on why it
// never populates Types - falls back to decl's own exact source text for
// that one piece). Reflects an instantiated generic's own substituted
// types, since an instantiation's clone gets its own, separately-checked
// Types entries distinct from the template's.
func FuncSignatureText(tree *ast.Tree, info *Info, decl ast.NodeIndex) string {
	var params []string
	for _, p := range tree.Children(tree.FuncParamList(decl)) {
		name := tree.Text(tree.Child(p, 0))
		params = append(params, name+" "+typeOrSourceText(tree, info, tree.Child(p, 1)))
	}
	sig := "(" + strings.Join(params, ", ") + ")"
	if ret := tree.FuncReturnType(decl); ret != ast.InvalidNode {
		sig += " " + typeOrSourceText(tree, info, ret)
	}
	return sig
}

// StructFieldsText renders decl's own fields, in declaration order, as a
// compact "{ name Type, ... }" summary - see FuncSignatureText for the
// same Type-first-with-source-fallback reasoning.
func StructFieldsText(tree *ast.Tree, info *Info, decl ast.NodeIndex) string {
	var fields []string
	for _, f := range tree.StructFields(decl) {
		name := tree.Text(tree.Child(f, 0))
		fields = append(fields, name+" "+typeOrSourceText(tree, info, tree.Child(f, 1)))
	}
	return "{ " + strings.Join(fields, ", ") + " }"
}

// typeOrSourceText renders typeNode's own checked Type when info has a
// valid one recorded, else its exact source text.
func typeOrSourceText(tree *ast.Tree, info *Info, typeNode ast.NodeIndex) string {
	if typeNode == ast.InvalidNode {
		return ""
	}
	if info != nil {
		if t, ok := info.Types[typeNode]; ok && !t.IsInvalid() {
			return t.String()
		}
	}
	return tree.SourceText(typeNode)
}
