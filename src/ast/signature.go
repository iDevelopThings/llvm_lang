package ast

import (
	"strings"

	"llvm_lang/src/enums"
)

// FuncSignatureText renders decl's own parameter list and return type as a
// compact "(name Type, ...) Return" string, each type node rendered by
// renderType. The child-node shape walk (which children, in what order, with
// what bracing) lives here once; how a *type* renders is the caller's own
// concern - this package has no resolved types to render from, so pass
// Tree.SourceText for "exactly as written", or sema.FuncSignatureText for
// the Type-aware rendering.
//
// decl may be a FuncDecl or an ExternFuncDecl: the two have different child
// layouts (see ExternFuncParamList), so which accessor set to use is decided
// here rather than assumed.
func (t *Tree) FuncSignatureText(decl NodeIndex, renderType func(NodeIndex) string) string {
	paramList, returnType := t.FuncParamList(decl), t.FuncReturnType(decl)
	if t.Nodes[decl].Kind == enums.NodeKinds.ExternFuncDecl {
		paramList, returnType = t.ExternFuncParamList(decl), t.ExternFuncReturnType(decl)
	}

	var params []string
	for _, p := range t.Children(paramList) {
		params = append(params, t.Text(t.Child(p, 0))+" "+renderTypeNode(t.Child(p, 1), renderType))
	}
	sig := "(" + strings.Join(params, ", ") + ")"
	if returnType != InvalidNode {
		sig += " " + renderTypeNode(returnType, renderType)
	}
	return sig
}

// StructFieldsText renders decl's own fields, in declaration order, as a
// compact "{ name Type, ... }" summary - see FuncSignatureText for renderType.
func (t *Tree) StructFieldsText(decl NodeIndex, renderType func(NodeIndex) string) string {
	var fields []string
	for _, f := range t.StructFields(decl) {
		fields = append(fields, t.Text(t.Child(f, 0))+" "+renderTypeNode(t.Child(f, 1), renderType))
	}
	return "{ " + strings.Join(fields, ", ") + " }"
}

// renderTypeNode guards renderType against a missing type node - a real
// possibility mid-edit, where SourceText would otherwise panic.
func renderTypeNode(n NodeIndex, renderType func(NodeIndex) string) string {
	if n == InvalidNode {
		return ""
	}
	return renderType(n)
}
