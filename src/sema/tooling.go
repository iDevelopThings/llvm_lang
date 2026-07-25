// Tooling-only analysis - never reached by the real compile pipeline
// (frontend.RunProgram/CompileProgram never call into this file), built
// specifically for src/lsp to query real Info.Refs/Info.Scopes over source
// the compiler itself deliberately never resolves as written.
package sema

import (
	"llvm_lang/src/ast"
	"llvm_lang/src/enums"
)

// ResolveTemplatesForTooling enriches info in place with real Refs/Scopes
// entries for every one of tree's own top-level generic declarations - see
// ResolveTemplateForTooling for what that resolves and why. Safe to call
// unconditionally; a tree with no generic declarations does nothing. Merged
// directly into info's own maps rather than kept as a separate overlay, so
// every existing Info.Refs[n]/Info.Scopes[n] read site picks it up for free
// - a real resolve/check pass never writes an entry for a template's own
// nodes, so there's no key collision to worry about.
func ResolveTemplatesForTooling(tree *ast.Tree, info *Info) {
	if info == nil {
		return
	}
	// One cache shared across every top-level declaration this call
	// processes, so a generic struct's own body and each of its separately-
	// resolved methods (this loop treats every top-level decl as its own
	// independent ResolveTemplateForTooling call) all reuse the identical
	// Field/Method Symbol objects - see shadowStructInfo's own doc comment
	// for why that identity is load-bearing, not cosmetic.
	shadowStructs := make(map[string]*StructInfo)
	for _, decl := range tree.Children(tree.Root) {
		shadow := resolveTemplateForTooling(tree, info, decl, shadowStructs)
		if shadow == nil {
			continue
		}
		for n, sym := range shadow.Refs {
			info.Refs[n] = sym
		}
		for n, scope := range shadow.Scopes {
			info.Scopes[n] = scope
		}
	}
}

// ResolveTemplateForTooling resolves decl - a generic declaration's own
// template (see IsGenericTemplate) - against its own type parameters, bound
// to a throwaway placeholder, purely so hover/completion/semantic-tokens
// have real Refs/Scopes to read for source the real pipeline leaves
// unresolved until instantiated. Returns those entries as a throwaway Info,
// or nil if decl isn't a generic template at all.
//
// Resolve-only: never runs Check, never clones decl (contrast
// instantiateFunc/instantiateStruct), and never writes into real, shared
// state - so it cannot affect the real compile pipeline no matter what a
// caller does with the result. The placeholder type parameter is never
// dereferenced: Resolve (unlike Check's typeFromNode) never reads
// Symbol.TypeParamBound, so a zero Type is fine.
func ResolveTemplateForTooling(tree *ast.Tree, real *Info, decl ast.NodeIndex) *Info {
	return resolveTemplateForTooling(tree, real, decl, make(map[string]*StructInfo))
}

// resolveTemplateForTooling is ResolveTemplateForTooling's own real
// implementation, taking an explicit shadowStructs cache so
// ResolveTemplatesForTooling can share one across every top-level
// declaration in a single call - see that cache's own doc comment
// (shadowStructInfo) for why. The exported singular entry point (used
// standalone by tests, and anywhere that only ever resolves one declaration
// in isolation) just seeds a fresh, call-scoped cache of its own.
func resolveTemplateForTooling(tree *ast.Tree, real *Info, decl ast.NodeIndex, shadowStructs map[string]*StructInfo) *Info {
	if !real.IsGenericTemplate(tree, decl) {
		return nil
	}

	r := newToolingResolver(real, tree)
	shadow := r.info

	switch tree.Nodes[decl].Kind {
	case enums.NodeKinds.StructDecl:
		resolveStructTemplateForTooling(r, real, tree, decl, shadowStructs)
	case enums.NodeKinds.FuncDecl:
		resolveFuncTemplateForTooling(r, real, tree, decl, shadowStructs)
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
func resolveStructTemplateForTooling(r *resolver, real *Info, tree *ast.Tree, decl ast.NodeIndex, shadowStructs map[string]*StructInfo) {
	nameNode := tree.StructName(decl)
	gi := real.Generics[tree.Text(nameNode)]
	if gi == nil {
		return
	}
	si := shadowStructInfo(r, gi, shadowStructs)
	scope := typeParamScope(real.FileScope, gi.Params, placeholderTypes(len(gi.Params)))
	r.resolveStructFieldTypes(scope, decl)
	for ctor := range tree.StructConstructors(decl) {
		r.resolveConstructorBody(scope, si, ctor)
		resolveThisMemberAccesses(r, tree, tree.ConstructorBody(ctor), si)
	}
	for dtor := range tree.StructDestructors(decl) {
		r.resolveDestructorBody(scope, si, dtor)
		resolveThisMemberAccesses(r, tree, tree.DestructorBody(dtor), si)
	}
}

// shadowStructInfo returns gi's own tooling-only synthetic StructInfo -
// fields declared fresh via declareStructMembers (a generic template never
// gets a real one of its own - see declareStruct's own doc comment), methods
// reused directly from gi.Methods (already real, shared data: built during
// the REAL Resolve pass, since addGenericStructMethod runs unconditionally,
// not only for tooling - see generics.go). Memoized in shadowStructs (per
// struct name) rather than rebuilt per call: a generic struct's own body and
// each of its methods are separately-resolved top-level declarations (each
// its own ResolveTemplateForTooling call), and References/hover unify a
// field's declaration with every `this.field` access across those calls
// purely by Symbol pointer identity (see Workspace.References) - a fresh
// Fields map per call would give every occurrence its own disconnected
// Symbol, breaking that unification even though each one individually still
// "looks" correct.
func shadowStructInfo(r *resolver, gi *GenericInfo, shadowStructs map[string]*StructInfo) *StructInfo {
	if si, ok := shadowStructs[gi.Symbol.Name]; ok {
		return si
	}
	si := &StructInfo{
		Symbol:       gi.Symbol,
		Fields:       make(map[string]*Symbol),
		Methods:      make(map[string]*Symbol),
		Constructors: make(map[int]*Symbol),
	}
	r.declareStructMembers(si, gi.Decl)
	for _, gm := range gi.Methods {
		si.Methods[gm.Name] = gm.Sym
	}
	shadowStructs[gi.Symbol.Name] = si
	return si
}

// resolveFuncTemplateForTooling resolves decl's own body against whichever
// of the three shapes isGenericDecl's own FuncDecl case recognizes: a free
// generic function, a method of a generic struct template (whose receiver
// clause's own type-parameter names - possibly spelled differently than the
// struct's own declaration, see GenericMethod.RecvParams - bind alongside
// the method's own, computed the identical way addGenericStructMethod
// does), or a generic method of an already-concrete type (only its own
// params need binding; the receiver was already resolved by the real pass).
func resolveFuncTemplateForTooling(r *resolver, real *Info, tree *ast.Tree, decl ast.NodeIndex, shadowStructs map[string]*StructInfo) {
	receiver := tree.FuncReceiver(decl)
	var names []string
	var recvStruct *StructInfo

	switch {
	case receiver == ast.InvalidNode:
		names = r.typeParamNames(tree.FuncTypeParamList(decl), nil)
	case real.Generics[tree.Text(receiver)] != nil:
		gi := real.Generics[tree.Text(receiver)]
		recvParams := r.typeParamNames(receiver, nil)
		ownParams := r.typeParamNames(tree.FuncTypeParamList(decl), recvParams)
		names = append(recvParams, ownParams...)
		r.info.Refs[receiver] = gi.Symbol
		recvStruct = shadowStructInfo(r, gi, shadowStructs)
	default:
		names = r.typeParamNames(tree.FuncTypeParamList(decl), nil)
		if sym, ok := real.Refs[receiver]; ok {
			r.info.Refs[receiver] = sym
			// The receiver type itself is already concrete (only the
			// method's own type parameters need placeholder binding here),
			// so unlike the generic-struct-template case above, sym's own
			// StructInfo is already real and correctly self-referential -
			// no synthetic shadow needed, just reuse it directly.
			recvStruct = sym.StructInfo
		}
	}

	scope := typeParamScope(real.FileScope, names, placeholderTypes(len(names)))
	r.resolveFuncBody(scope, decl)
	if recvStruct == nil {
		return
	}

	// resolveFuncBody built `this`'s own receiver Symbol from gi.Symbol
	// (real.Refs[receiver] above), whose StructInfo is nil - a generic
	// struct template never gets a real one (see declareStruct's own doc
	// comment) - so `this` itself would otherwise carry no field/method
	// catalog at all. fnScope.Receiver is a fresh, tooling-only Symbol
	// (built by receiverSymbol, never shared/real state - see its own doc
	// comment), so overwriting its StructInfo here is safe.
	if fnScope, ok := r.info.Scopes[decl]; ok && fnScope.Receiver != nil {
		fnScope.Receiver.StructInfo = recvStruct
	}
	resolveThisMemberAccesses(r, tree, tree.FuncBody(decl), recvStruct)
}

// resolveThisMemberAccesses gives `this.field`/`this.method` MemberExpr
// nodes real Info.Refs entries anywhere inside body - the one thing
// resolveFuncBody's ordinary (shared with the real pipeline) body-walk
// deliberately leaves for Check to resolve later (see resolve.go's own
// MemberExpr case doc comment), which a generic template's body never gets
// (see this file's own doc comment). Narrowly scoped to `this.` access
// specifically - a template body has no other way to reach a concretely-
// enough-known value's own fields/methods; every other expression's type is
// either a placeholder type parameter (nothing to look up) or something only
// Check's full type inference could resolve.
func resolveThisMemberAccesses(r *resolver, tree *ast.Tree, body ast.NodeIndex, recvStruct *StructInfo) {
	for n := range tree.Descendants(body) {
		if tree.Nodes[n].Kind != enums.NodeKinds.MemberExpr {
			continue
		}
		if tree.Nodes[tree.Child(n, 0)].Kind != enums.NodeKinds.ThisExpr {
			continue
		}
		name := tree.Text(n)
		if sym, ok := recvStruct.Fields[name]; ok {
			r.info.Refs[n] = sym
			continue
		}
		if sym, ok := recvStruct.Methods[name]; ok {
			r.info.Refs[n] = sym
		}
	}
}

func placeholderTypes(n int) []Type {
	return make([]Type, n)
}
