package lsp

import (
	"llvm_lang/src/ast"
	"llvm_lang/src/sema"
)

// symbolDetail renders a compact, single-line signature/summary for sym -
// a function's own parameter names+types and return type, a struct's own
// field list, or a single field's own type - for CompletionItem.Detail and
// hover's own extra detail line. Type-first: reads each param/field/return
// type's own checked Type from the declaring file's Info when available,
// which is what makes an instantiated generic's own substituted types
// render correctly (e.g. "(v Entity) int" for a Box[Entity] method, not the
// template's own "(v T) T" - see sema.FuncSignatureText's own doc comment
// for why).
//
// info is sym's own declaring file's Info (Workspace.declaringInfos'/
// infoForTree's lookup, done by the caller) - the Info to render against is
// the *declaring* file's, which needn't be the file the request came from.
//
// "" for any symbol kind this doesn't know how to render (unchanged
// behavior for everything but SymFunc/SymStruct/SymField).
func symbolDetail(info *sema.Info, sym *sema.Symbol) string {
	if sym == nil || sym.Tree == nil || sym.Decl == ast.InvalidNode {
		return ""
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
		return sema.FuncSignatureText(sym.Tree, info, sym.Decl)
	case sema.SymStruct:
		return sema.StructFieldsText(sym.Tree, info, sym.Decl)
	case sema.SymField:
		return sema.FieldTypeText(sym.Tree, info, sym.Decl)
	default:
		return ""
	}
}
