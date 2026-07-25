package sema

import "testing"

func TestResolveTemplateForTooling_GenericFunc_NeverInstantiated(t *testing.T) {
	tree, info := checkSrc(t, "func Sum[T](a T, b T) T {\n\treturn a + b\n}\n")
	decl := tree.Children(tree.Root)[0]

	shadow := ResolveTemplateForTooling(tree, info, decl)
	if shadow == nil {
		t.Fatal("ResolveTemplateForTooling returned nil for a real generic template")
	}

	bodyA := tree.FindIdentByText(decl, "a")
	sym, ok := shadow.Refs[bodyA]
	if !ok || sym == nil {
		t.Fatalf("body identifier 'a' has no Refs entry in the tooling Info")
	}
	if sym.Kind != SymParam {
		t.Errorf("'a' resolved to Kind %v, want SymParam", sym.Kind)
	}

	// The real Info must still have nothing for this template's own body -
	// this pass must never write into the real Info by mistake.
	if _, ok := info.Refs[bodyA]; ok {
		t.Error("the REAL Info now has a Refs entry for the template body - it must stay untouched")
	}
}

func TestResolveTemplateForTooling_GenericStruct_FieldsAndMethod(t *testing.T) {
	tree, info := checkSrc(t, "struct Box[T] {\n\tvalue T\n}\n"+
		"func (Box[T]) Get() T {\n\treturn this.value\n}\n")
	structDecl := tree.Children(tree.Root)[0]
	methodDecl := tree.Children(tree.Root)[1]

	shadowStruct := ResolveTemplateForTooling(tree, info, structDecl)
	if shadowStruct == nil {
		t.Fatal("ResolveTemplateForTooling returned nil for the struct template")
	}
	fieldName := tree.FindIdentByText(structDecl, "value")
	sym, ok := shadowStruct.Refs[fieldName]
	if !ok || sym == nil || sym.Kind != SymField {
		t.Fatalf("struct field 'value' Refs = %+v (ok=%v), want a SymField", sym, ok)
	}

	shadowMethod := ResolveTemplateForTooling(tree, info, methodDecl)
	if shadowMethod == nil {
		t.Fatal("ResolveTemplateForTooling returned nil for the method template")
	}
	// "this" isn't a plain Ident node (see LANGUAGE.md) - assert the method
	// body resolved at all instead by checking its own Scope was recorded.
	if _, ok := shadowMethod.Scopes[methodDecl]; !ok {
		t.Error("method template got no Scopes entry - resolveFuncBody did not run")
	}
}

func TestResolveTemplateForTooling_NonGenericDeclReturnsNil(t *testing.T) {
	tree, info := checkSrc(t, "func Plain(a int) int {\n\treturn a\n}\n")
	decl := tree.Children(tree.Root)[0]

	if shadow := ResolveTemplateForTooling(tree, info, decl); shadow != nil {
		t.Errorf("ResolveTemplateForTooling(non-generic decl) = %+v, want nil", shadow)
	}
}

func TestResolveTemplateForTooling_DoesNotMutateSharedState(t *testing.T) {
	tree, info := checkSrc(t, "func Sum[T](a T, b T) T {\n\treturn a + b\n}\n"+
		"func main() {\n\tprint(Sum(1, 2))\n}\n")
	decl := tree.Children(tree.Root)[0]

	specializationsBefore := len(info.Specializations)
	structsBefore := len(info.Structs)
	pkgScopeBefore := info.PkgScope

	ResolveTemplateForTooling(tree, info, decl)

	if len(info.Specializations) != specializationsBefore {
		t.Errorf("Info.Specializations changed: %d -> %d - the tooling pass must never instantiate/clone",
			specializationsBefore, len(info.Specializations))
	}
	if len(info.Structs) != structsBefore {
		t.Errorf("Info.Structs changed: %d -> %d - the tooling pass must never write to the real struct catalog",
			structsBefore, len(info.Structs))
	}
	if info.PkgScope != pkgScopeBefore {
		t.Error("Info.PkgScope pointer changed - the tooling pass must never replace the real package scope")
	}
}
