// Tooling-only analysis - never reached by the real compile pipeline
// (frontend.RunProgram/CompileProgram never call into this file), built
// specifically for src/lsp to query real Info.Refs/Info.Scopes over source
// the compiler itself deliberately never resolves as written.
package sema

import (
	"llvm_lang/src/ast"
	"llvm_lang/src/diag"
	"llvm_lang/src/enums"
)

// ResolveTemplateForTooling resolves decl - a generic declaration's own
// template (see IsGenericTemplate) - against its own type parameters, bound
// to a throwaway placeholder, purely so hover/completion/semantic-tokens
// have real Refs/Scopes to read for source the real pipeline leaves
// entirely unresolved until instantiated. Resolve-only: never runs Check,
// never clones decl (contrast instantiateFunc/instantiateStruct), and
// never writes into real, shared state (real.Structs/real.Enums are read
// via the bare *resolver below the same way resolverFor's own callers do,
// never written to) - so this cannot affect the real compile pipeline no
// matter what a caller does with its result. The placeholder type
// parameter is never actually dereferenced: Resolve (unlike Check's
// typeFromNode) never reads Symbol.TypeParamBound, so a zero Type is fine.
//
// nil if decl isn't a generic template at all - nothing to resolve.
func ResolveTemplateForTooling(tree *ast.Tree, real *Info, decl ast.NodeIndex) *Info {
	if !real.IsGenericTemplate(tree, decl) {
		return nil
	}

	shadow := &Info{
		Refs:   make(map[ast.NodeIndex]*Symbol),
		Scopes: make(map[ast.NodeIndex]*Scope),
	}
	r := &resolver{
		tree:    tree,
		info:    shadow,
		bag:     diag.NewBag(), // discarded - see doc comment above
		pkg:     real.PkgScope,
		structs: real.Structs,
		enums:   real.Enums,
	}

	switch tree.Nodes[decl].Kind {
	case enums.NodeKinds.StructDecl:
		resolveStructTemplateForTooling(r, real, tree, decl)
	case enums.NodeKinds.FuncDecl:
		resolveFuncTemplateForTooling(r, real, tree, decl)
	}
	return shadow
}

// resolveStructTemplateForTooling resolves decl's own field types and every
// constructor/destructor body - mirroring instantiateStruct's own recipe
// (generics.go) minus the clone, minus Check, and minus writing the
// throwaway StructInfo anywhere real code could find it. decl's own name
// Symbol already exists in real.Refs (declareStruct always runs, even for a
// template - see its own doc comment) and is reused directly rather than
// fabricated, so `this` inside a method resolves to the real declaration.
func resolveStructTemplateForTooling(r *resolver, real *Info, tree *ast.Tree, decl ast.NodeIndex) {
	nameNode := tree.StructName(decl)
	gi := real.Generics[tree.Text(nameNode)]
	if gi == nil {
		return
	}
	si := &StructInfo{
		Symbol:       real.Refs[nameNode],
		Fields:       make(map[string]*Symbol),
		Methods:      make(map[string]*Symbol),
		Constructors: make(map[int]*Symbol),
	}
	scope := typeParamScope(real.FileScope, gi.Params, placeholderTypes(len(gi.Params)))
	r.declareStructMembers(si, decl)
	r.resolveStructFieldTypes(scope, decl)
	for ctor := range tree.StructConstructors(decl) {
		r.resolveConstructorBody(scope, si, ctor)
	}
	for dtor := range tree.StructDestructors(decl) {
		r.resolveDestructorBody(scope, si, dtor)
	}
}

// resolveFuncTemplateForTooling resolves decl's own body against whichever
// of the three shapes isGenericDecl's own FuncDecl case recognizes: a free
// generic function, a method of a generic struct template (whose receiver
// clause's own type-parameter names - possibly spelled differently than the
// struct's own declaration, see GenericMethod.RecvParams - bind alongside
// the method's own, computed the identical way addGenericStructMethod
// does), or a generic method of an already-concrete type (only its own
// params need binding; the receiver was already resolved by the real pass).
func resolveFuncTemplateForTooling(r *resolver, real *Info, tree *ast.Tree, decl ast.NodeIndex) {
	receiver := tree.FuncReceiver(decl)
	var names []string

	switch {
	case receiver == ast.InvalidNode:
		names = r.typeParamNames(tree.FuncTypeParamList(decl), nil)
	case real.Generics[tree.Text(receiver)] != nil:
		gi := real.Generics[tree.Text(receiver)]
		recvParams := r.typeParamNames(receiver, nil)
		ownParams := r.typeParamNames(tree.FuncTypeParamList(decl), recvParams)
		names = append(recvParams, ownParams...)
		r.info.Refs[receiver] = gi.Symbol
	default:
		names = r.typeParamNames(tree.FuncTypeParamList(decl), nil)
		if sym, ok := real.Refs[receiver]; ok {
			r.info.Refs[receiver] = sym
		}
	}

	scope := typeParamScope(real.FileScope, names, placeholderTypes(len(names)))
	r.resolveFuncBody(scope, decl)
}

func placeholderTypes(n int) []Type {
	return make([]Type, n)
}
