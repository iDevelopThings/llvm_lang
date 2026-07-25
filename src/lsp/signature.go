package lsp

import (
	"strings"

	"llvm_lang/src/ast"
	"llvm_lang/src/sema"
)

// symbolDetail renders a compact, single-line signature/summary for sym -
// a function's own parameter names+types and return type, or a struct's
// own field list - for CompletionItem.Detail and hover's own extra detail
// line. Type-first: reads each param/field/return type's own checked Type
// from the declaring file's Info when available, which is what makes an
// instantiated generic's own substituted types render correctly (e.g.
// "(v Entity) int" for a Box[Entity] method, not the template's own
// "(v T) T" - see sema.Info.Types' own doc comment on how an
// instantiation's clone gets its own, separately-checked Types entries).
// Falls back to sym's own exact source text (ast.Tree.SourceText) per
// piece wherever a Type isn't available - an unchecked generic template
// (this project's sema.ResolveTemplateForTooling deliberately only
// resolves Refs/Scopes, never Types - see its own doc comment), or no
// Info at all yet.
//
// "" for any symbol kind this doesn't know how to render (unchanged
// behavior for everything but SymFunc and SymStruct).
func symbolDetail(w *Workspace, sym *sema.Symbol) string {
	if sym == nil || sym.Tree == nil || sym.Decl == ast.InvalidNode {
		return ""
	}
	var info *sema.Info
	if fa, ok := w.Analysis(sym.Tree.File.Name); ok {
		info = fa.Info
	}

	switch sym.Kind {
	case sema.SymFunc:
		// Covers a free function and a method alike (a method's own
		// FuncDecl just carries a receiver clause too - see FuncReceiver)
		// - both share the identical param-list/return-type shape.
		// Deliberately NOT SymConstructor/SymDestructor: those are a
		// different node kind (ConstructorDecl/DestructorDecl) with their
		// own distinct accessors (ConstructorParamList, no return type at
		// all), not reachable through FuncParamList/FuncReturnType.
		return funcSignatureDetail(sym.Tree, info, sym.Decl)
	case sema.SymStruct:
		return structFieldsDetail(sym.Tree, info, sym.Decl)
	default:
		return ""
	}
}

func funcSignatureDetail(tree *ast.Tree, info *sema.Info, decl ast.NodeIndex) string {
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

func structFieldsDetail(tree *ast.Tree, info *sema.Info, decl ast.NodeIndex) string {
	var fields []string
	for _, f := range tree.StructFields(decl) {
		name := tree.Text(tree.Child(f, 0))
		fields = append(fields, name+" "+typeOrSourceText(tree, info, tree.Child(f, 1)))
	}
	return "{ " + strings.Join(fields, ", ") + " }"
}

// typeOrSourceText renders typeNode's own checked Type when info has a
// valid one recorded, else its exact source text.
func typeOrSourceText(tree *ast.Tree, info *sema.Info, typeNode ast.NodeIndex) string {
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
