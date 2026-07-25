package ast

import (
	"iter"
	"strings"
	"unicode"
	"unicode/utf8"

	"llvm_lang/src/enums"
)

// TestFunc identifies one top-level FuncDecl matching the TestXxx discovery
// convention (see TestFuncs).
type TestFunc struct {
	Decl NodeIndex
	Name string
}

// TestFuncs yields every top-level FuncDecl in t matching the TestXxx
// discovery convention `llvmc -test` uses (see CODEGEN.md's "-test"
// section): no receiver, no type parameters, a name IsTestFuncName accepts,
// no declared return type, and exactly one parameter of type
// *runnerPkg.runnerType. Exported here (not private to cmd/llvmc) so a
// second consumer recognizing the same convention - e.g. an LSP "run test"
// code lens - has a shared implementation to call rather than its own copy.
func (t *Tree) TestFuncs(runnerPkg, runnerType string) iter.Seq[TestFunc] {
	return func(yield func(TestFunc) bool) {
		for decl := range t.TopLevelDeclsOfKind(enums.NodeKinds.FuncDecl) {
			if t.FuncReceiver(decl) != InvalidNode {
				continue
			}
			if t.FuncTypeParamList(decl) != InvalidNode {
				continue
			}
			name := t.Text(t.FuncName(decl))
			if !IsTestFuncName(name) {
				continue
			}
			if t.FuncReturnType(decl) != InvalidNode {
				continue
			}
			params := t.Children(t.FuncParamList(decl))
			if len(params) != 1 {
				continue
			}
			paramType := t.Child(params[0], 1)
			if !t.IsPointerToQualifiedType(paramType, runnerPkg, runnerType) {
				continue
			}
			if !yield(TestFunc{Decl: decl, Name: name}) {
				return
			}
		}
	}
}

// IsTestFuncName reports whether name matches Go's own testing convention:
// "Test" immediately followed by an uppercase rune - so "Test" alone, or
// "Testing", don't count.
func IsTestFuncName(name string) bool {
	if !strings.HasPrefix(name, "Test") || name == "Test" {
		return false
	}
	r, _ := utf8.DecodeRuneInString(name[len("Test"):])
	return unicode.IsUpper(r)
}

// IsPointerToQualifiedType reports whether typ is *pkg.Name - a pointer to
// a package-qualified type reference, which parses as PointerType wrapping
// a MemberExpr (see parseTypeExpr's own doc comment, src/parser/stmt.go).
func (t *Tree) IsPointerToQualifiedType(typ NodeIndex, pkg, name string) bool {
	if typ == InvalidNode || t.Nodes[typ].Kind != enums.NodeKinds.PointerType {
		return false
	}
	member := t.Child(typ, 0)
	if member == InvalidNode || t.Nodes[member].Kind != enums.NodeKinds.MemberExpr {
		return false
	}
	if t.Text(member) != name {
		return false
	}
	obj := t.Child(member, 0)
	if obj == InvalidNode || t.Nodes[obj].Kind != enums.NodeKinds.Ident {
		return false
	}
	return t.Text(obj) == pkg
}
