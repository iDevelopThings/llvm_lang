package lsp

import (
	"llvm_lang/src/ast"
	"llvm_lang/src/enums"
	"llvm_lang/src/sema"

	protocol "github.com/tliron/glsp/protocol_3_16"
)

// Completion answers a textDocument/completion request: every symbol
// visible at pos, or (at `expr.<cursor>` position) every field/method on
// expr's own checked type - nil when path has no analysis yet, or pos
// doesn't land on a node completion knows how to handle.
//
// Reuses the same resolveNode preamble Hover/Definition already use - no
// throwaway reparse needed: frontend.RunProgram now populates Info
// best-effort even past a parse error (see its own doc comment), and a
// dangling `expr.` (itself a parse error - nothing typed after the dot
// yet) still parses to a real MemberExpr node whose object subexpression
// resolves normally.
func (w *Workspace) Completion(path string, pos protocol.Position) []protocol.CompletionItem {
	fa, n, ok := w.resolveNode(path, pos)
	if !ok {
		return nil
	}

	if fa.Tree.Nodes[n].Kind == enums.NodeKinds.MemberExpr {
		return w.memberCompletions(fa, n)
	}
	return w.identifierCompletions(fa, n)
}

// memberCompletions handles `expr.<cursor>`, dispatching on what kind of
// thing expr's own object subexpression actually is - three genuinely
// different resolution paths this language's own resolver keeps separate
// (see resolveMember and resolveTypeMemberExpr in src/sema), so completion
// keeps them separate too rather than pretending they're one case:
//
//  1. expr is a real value (Info.Types[object] resolved) - a struct's own
//     Fields+Methods, or (an enum VALUE never exposes its variants outside
//     `match` - see resolveMember's own TypeEnum case) just an enum's
//     Methods.
//  2. expr is a bare enum type name (Info.Refs[object] is a SymEnum) -
//     `Shape.<cursor>` unit-variant construction syntax - that enum's own
//     Variants, and only its Variants (see resolveTypeMemberExpr).
//  3. expr is a bare, already-imported package name (Info.Refs[object] is
//     a SymPackage) - that package's own exported top-level declarations.
//
// Anything else (an unresolved bare identifier, e.g. `slices` with no
// import at all) falls back to the not-yet-imported-package path.
func (w *Workspace) memberCompletions(fa *FileAnalysis, memberExpr ast.NodeIndex) []protocol.CompletionItem {
	object := fa.Tree.Child(memberExpr, 0)

	if objType, ok := fa.Info.Types[object]; ok && !objType.IsInvalid() {
		return w.valueMemberCompletions(objType)
	}

	if objSym, ok := fa.Info.Refs[object]; ok && objSym != nil {
		switch objSym.Kind {
		case sema.SymEnum:
			return enumVariantCompletions(objSym.EnumInfo)
		case sema.SymPackage:
			return w.packageMemberCompletions(objSym.Package)
		case sema.SymReceiver:
			// `this.<cursor>` inside a generic template's own method body:
			// Info.Types[object] above is never populated there (only Check
			// sets it, and a template body never runs Check - see
			// sema/tooling.go's own doc comment), so this receiver's own
			// StructInfo/EnumInfo - set by that same tooling pass
			// specifically so this works - is the only way to answer with
			// real fields/methods instead of falling through to the
			// not-yet-imported-package guess below.
			switch {
			case objSym.StructInfo != nil:
				return w.valueMemberCompletions(sema.Type{Kind: sema.TypeStruct, Struct: objSym.StructInfo})
			case objSym.EnumInfo != nil:
				return w.valueMemberCompletions(sema.Type{Kind: sema.TypeEnum, Enum: objSym.EnumInfo})
			}
		}
	}

	return w.unimportedPackageMemberCompletions(fa, object)
}

// valueMemberCompletions handles case 1 above: a real value's own fields/
// methods, auto-dereferencing one pointer level exactly like ordinary
// member access does (see sema.Type.Underlying).
func (w *Workspace) valueMemberCompletions(objType sema.Type) []protocol.CompletionItem {
	infos := w.declaringInfos()
	switch objType.Underlying().Kind {
	case sema.TypeStruct:
		info := objType.Underlying().Struct
		items := make([]protocol.CompletionItem, 0, len(info.Fields)+len(info.Methods))
		for name, sym := range info.Fields {
			items = append(items, symbolCompletionItem(infos, name, protocol.CompletionItemKindField, sym))
		}
		for name, sym := range info.Methods {
			items = append(items, symbolCompletionItem(infos, name, protocol.CompletionItemKindMethod, sym))
		}
		return items
	case sema.TypeEnum:
		info := objType.Underlying().Enum
		items := make([]protocol.CompletionItem, 0, len(info.Methods))
		for name, sym := range info.Methods {
			items = append(items, symbolCompletionItem(infos, name, protocol.CompletionItemKindMethod, sym))
		}
		return items
	default:
		return nil
	}
}

// enumVariantCompletions handles case 2 above: `Shape.<cursor>` naming the
// enum TYPE itself, offering its unit-variant construction names.
func enumVariantCompletions(info *sema.EnumInfo) []protocol.CompletionItem {
	items := make([]protocol.CompletionItem, 0, len(info.Variants))
	for name := range info.Variants {
		items = append(items, protocol.CompletionItem{
			Label: name,
			Kind:  completionItemKindPtr(protocol.CompletionItemKindEnumMember),
		})
	}
	return items
}

// packageMemberCompletions handles case 3 above: an already-imported
// package's own exported top-level declarations (see LANGUAGE.md's
// "Imports" section - only a capitalized-name declaration is even visible
// through a package qualifier at all).
func (w *Workspace) packageMemberCompletions(pkg *sema.PackageResult) []protocol.CompletionItem {
	if pkg == nil || pkg.Scope == nil {
		return nil
	}
	infos := w.declaringInfos()
	var items []protocol.CompletionItem
	for sym := range pkg.Scope.Local() {
		if !sym.Exported {
			continue
		}
		items = append(items, symbolCompletionItem(infos, sym.Name, symbolKindToCompletionItemKind(sym.Kind), sym))
	}
	return items
}

// identifierCompletions handles every other cursor position: every symbol
// visible in n's own enclosing scope (locals, params, the method receiver,
// package-level decls, already-imported package names - see
// sema.Scope.Visible), plus every not-yet-imported package name.
func (w *Workspace) identifierCompletions(fa *FileAnalysis, n ast.NodeIndex) []protocol.CompletionItem {
	scope := fa.Info.EnclosingScope(fa.Tree, n)
	if scope == nil {
		return nil
	}

	infos := w.declaringInfos()
	var items []protocol.CompletionItem
	visible := make(map[string]bool)
	for sym := range scope.Visible() {
		visible[sym.Name] = true
		items = append(items, symbolCompletionItem(infos, sym.Name, symbolKindToCompletionItemKind(sym.Kind), sym))
	}
	items = append(items, w.unimportedPackageNameCompletions(fa, visible)...)
	return items
}

// symbolCompletionItem builds a plain (no additional edits) CompletionItem
// for a resolved *Symbol - label is passed separately rather than read off
// sym.Name since a struct/enum member's map key is already the label an
// LSP client should offer (matters nowhere today, but keeps the field
// lookup and the display label from silently drifting apart). Detail is
// populated via symbolDetail (a function's real signature, a struct's own
// field list) whenever sym is a kind it knows how to render - infos is that
// function's own per-request declaring-file lookup, built once by the caller
// rather than per item.
func symbolCompletionItem(infos map[*ast.Tree]*sema.Info, label string, kind protocol.CompletionItemKind, sym *sema.Symbol) protocol.CompletionItem {
	item := protocol.CompletionItem{
		Label: label,
		Kind:  completionItemKindPtr(kind),
	}
	if sym != nil && sym.Tree != nil && sym.Decl != ast.InvalidNode {
		if doc := sym.Tree.DocComment(sym.Decl); doc != "" {
			item.Documentation = doc
		}
		if detail := symbolDetail(infos[sym.Tree], sym); detail != "" {
			item.Detail = &detail
		}
	}
	return item
}

func completionItemKindPtr(kind protocol.CompletionItemKind) *protocol.CompletionItemKind {
	return &kind
}

// unimportedPackageMemberCompletions and unimportedPackageNameCompletions
// are filled in separately (see completion_import.go) - the not-yet-
// imported-package half of completion, which needs Workspace.PackageIndex
// and an auto-import AdditionalTextEdits.

func symbolKindToCompletionItemKind(kind sema.SymbolKind) protocol.CompletionItemKind {
	switch kind {
	case sema.SymFunc:
		return protocol.CompletionItemKindFunction
	case sema.SymStruct:
		return protocol.CompletionItemKindStruct
	case sema.SymEnum:
		return protocol.CompletionItemKindEnum
	case sema.SymEnumVariant:
		return protocol.CompletionItemKindEnumMember
	case sema.SymField:
		return protocol.CompletionItemKindField
	case sema.SymPackage:
		return protocol.CompletionItemKindModule
	default: // SymVar, SymParam, SymReceiver
		return protocol.CompletionItemKindVariable
	}
}
