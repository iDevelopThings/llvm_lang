// Monomorphized generics (see LANGUAGE.md's "Generics" section). A generic
// declaration is never resolved, type-checked, or lowered as written: its
// template is catalogued at Resolve time, and Check produces one ordinary,
// fully concrete clone of it per distinct type-argument list actually reached,
// resolved and checked exactly like a hand-written declaration in a scope that
// binds each type parameter to a real type. codegen then lowers those clones
// with no notion of genericity at all (Info.Specializations).
package sema

import (
	"fmt"
	"iter"
	"slices"
	"strings"

	"llvm_lang/src/ast"
	"llvm_lang/src/diag"
	"llvm_lang/src/enums"
)

// GenericInfo is one generic declaration's template - a `func Foo[T]`, a
// `struct Foo[T]`, or a method carrying type parameters of its own.
//
// A method's template also carries the receiver side: OwnerSym is the concrete
// struct/enum it is a method of, and OuterParams/OuterArgs its receiver
// clause's own type parameters, already bound (empty for a method on a
// non-generic type).
type GenericInfo struct {
	Symbol *Symbol
	Decl   ast.NodeIndex
	Tree   *ast.Tree

	// Params are the declared type-parameter names, in declaration order -
	// empty for a non-generic method of a generic struct, which still needs a
	// template because its receiver's parameters are what vary.
	Params []string

	// Methods is every method declared against this generic struct, in
	// declaration order. nil for anything but a struct template.
	Methods []*GenericMethod

	OwnerSym    *Symbol
	OuterParams []string
	OuterArgs   []Type

	// Method is set only when this GenericInfo is one receiver struct's own
	// per-instantiation method template (built by GenericMethod.templateFor)
	// - the GenericMethod it came from, so instantiateFunc can point
	// Symbol.GenericTemplate at the method's real declaring Symbol rather
	// than this template's synthetic one (see Symbol.GenericMethod). nil for
	// a free generic func/struct template, where Symbol above already IS the
	// real declaring Symbol.
	Method *GenericMethod

	instances map[string]*genericInstance
}

// GenericMethod is one method declared against a generic struct, before that
// struct is instantiated - RecvParams is the receiver clause's own spelling of
// the struct's type parameters (`func (SlotMap[E])` may name them differently
// than `struct SlotMap[T]` does), OwnParams the method's own extra ones.
type GenericMethod struct {
	Name       string
	Decl       ast.NodeIndex
	Tree       *ast.Tree
	Sym        *Symbol
	RecvParams []string
	OwnParams  []string

	// templates memoizes one method template per instantiated struct - a
	// method's own instantiations are per-receiver-instance, so SlotMap[int]'s
	// Get and SlotMap[f64]'s Get never share an instance cache.
	templates map[*StructInfo]*GenericInfo
}

// genericInstance is one already-created specialization of a template: the
// symbol a call site refers to (a function/method) or the struct catalog a
// type position resolves to (a struct), plus the exact type arguments it was
// created for - kept so instanceKey can tell a genuine cache hit from a
// mangled-name collision between two structurally different argument lists.
type genericInstance struct {
	args       []Type
	sym        *Symbol
	structInfo *StructInfo
}

// ---------------------------------------------------------------------------
// Resolve side: cataloguing templates.
// ---------------------------------------------------------------------------

// declareGeneric turns sym into a generic template when list (its own
// TypeParamList child) is present, reporting whether it did. Only a generic
// *struct* is entered into the shared by-name catalog - that's the one lookup
// that has nothing but a name to go on (a method's receiver clause); everything
// else reaches a template through Symbol.Generic directly.
func (r *resolver) declareGeneric(sym *Symbol, decl, list ast.NodeIndex) (*GenericInfo, bool) {
	if list == ast.InvalidNode {
		return nil, false
	}
	params := r.typeParamNames(list, nil)
	if len(params) == 0 {
		r.errorAt(list, "%s declares an empty type-parameter list", sym.Name)
		return nil, false
	}
	gi := &GenericInfo{
		Symbol:    sym,
		Decl:      decl,
		Tree:      r.tree,
		Params:    params,
		instances: make(map[string]*genericInstance),
	}
	sym.Generic = gi
	if sym.Kind == SymStruct {
		r.generics[sym.Name] = gi
	}
	return gi, true
}

// typeParamNames reads list's declared type-parameter names in order,
// rejecting a duplicate within the list and any name already bound by outer
// (a method's receiver clause). A rejected name is dropped rather than
// carried forward, so the resulting list always has as many usable entries as
// it has distinct names.
func (r *resolver) typeParamNames(list ast.NodeIndex, outer []string) []string {
	if list == ast.InvalidNode {
		return nil
	}
	children := r.tree.Children(list)
	names := make([]string, 0, len(children))
	for _, nameNode := range children {
		name := r.tree.Text(nameNode)
		switch {
		case slices.Contains(outer, name):
			// Never shadowing - see LANGUAGE.md's "Generics" section.
			r.errorAt(nameNode, "type parameter %s is already bound by the receiver clause", name)
		case slices.Contains(names, name):
			r.errorAt(nameNode, "type parameter %s redeclared", name)
		default:
			names = append(names, name)
		}
	}
	return names
}

// addGenericStructMethod attaches a method declared against generic struct gi
// to that template, instead of to any concrete StructInfo (there is none until
// gi is instantiated). The receiver clause must name exactly as many type
// parameters as the struct declares - they're positional, not matched by name.
func (r *resolver) addGenericStructMethod(gi *GenericInfo, receiver, decl ast.NodeIndex, sym *Symbol) {
	recvParams := r.typeParamNames(receiver, nil)
	if len(recvParams) != len(gi.Params) {
		r.errorAt(receiver, "%s declares %d type parameter(s); its receiver clause must name exactly that many (e.g. (%s[%s]))",
			gi.Symbol.Name, len(gi.Params), gi.Symbol.Name, strings.Join(gi.Params, ", "))
		return
	}
	for _, existing := range gi.Methods {
		if existing.Name == sym.Name {
			r.errorAt(decl, "method %s redeclared on struct %s", sym.Name, gi.Symbol.Name)
			return
		}
	}
	sym.Scope = gi.Symbol.Scope
	gm := &GenericMethod{
		Name:       sym.Name,
		Decl:       decl,
		Tree:       r.tree,
		Sym:        sym,
		RecvParams: recvParams,
		OwnParams:  r.typeParamNames(r.tree.FuncTypeParamList(decl), recvParams),
		templates:  make(map[*StructInfo]*GenericInfo),
	}
	sym.GenericMethod = gm
	gi.Methods = append(gi.Methods, gm)
}

// IsGenericTemplate reports whether decl - a top-level declaration in tree -
// is a generic declaration's own template rather than something to compile.
// A template has no concrete types anywhere in it and was never resolved or
// checked; its Specializations are what a consumer actually wants (see
// Info.Specializations).
func (i *Info) IsGenericTemplate(tree *ast.Tree, decl ast.NodeIndex) bool {
	return isGenericDecl(tree, i.Generics, decl)
}

// isGenericDecl reports whether decl is a declaration whose body must be left
// unresolved/unchecked until instantiated: a generic func/struct, or a method
// of either.
func isGenericDecl(tree *ast.Tree, generics map[string]*GenericInfo, decl ast.NodeIndex) bool {
	switch tree.Nodes[decl].Kind {
	case enums.NodeKinds.StructDecl:
		return tree.StructTypeParamList(decl) != ast.InvalidNode
	case enums.NodeKinds.FuncDecl:
		if tree.FuncTypeParamList(decl) != ast.InvalidNode {
			return true
		}
		recv := tree.FuncReceiver(decl)
		if recv == ast.InvalidNode {
			return false
		}
		_, generic := generics[tree.Text(recv)]
		return generic
	default:
		return false
	}
}

// ---------------------------------------------------------------------------
// Check side: instantiation.
// ---------------------------------------------------------------------------

// newBodyResolver builds a resolver over tree's already-built package/file
// scopes (read from src), writing its own Refs/Scopes into dst and its
// diagnostics into bag - the single place listing the fields every
// body-resolution path needs, shared by resolverFor and newToolingResolver
// below so a field added for one is visibly missing from neither.
//
// generics/fileImports/fileScopes are deliberately left nil: they only matter
// to the declaration-cataloguing passes, and every caller here starts from an
// explicit scope (typeParamScope over Info.FileScope) instead. Anything that
// makes body resolution read them has to populate them here too.
func newBodyResolver(tree *ast.Tree, src, dst *Info, bag *diag.Bag) *resolver {
	return &resolver{
		pkg:     src.PkgScope,
		structs: src.Structs,
		enums:   src.Enums,
		tree:    tree,
		info:    dst,
		bag:     bag,
	}
}

// resolverFor builds an instantiation's resolver, so a specialization goes
// through the exact same name-resolution code a hand-written declaration did.
// Resolve keeps no state between declarations, so a fresh one per
// instantiation is both correct and cheap.
func (c *checker) resolverFor(tree *ast.Tree) *resolver {
	info := c.infos[tree]
	r := newBodyResolver(tree, info, info, c.allDiags[tree])
	r.infos = c.infos
	r.bags = c.allDiags
	return r
}

// newToolingResolver builds ResolveTemplateForTooling's own resolver: the
// same shape resolverFor uses, but writing into a throwaway Info (returned
// as r.info) and a discarded diagnostic bag, so a tooling query can never
// touch what the real pipeline reads. See tooling.go.
func newToolingResolver(real *Info, tree *ast.Tree) *resolver {
	shadow := &Info{
		Refs:   make(map[ast.NodeIndex]*Symbol),
		Scopes: make(map[ast.NodeIndex]*Scope),
	}
	return newBodyResolver(tree, real, shadow, diag.NewBag())
}

// typeParamScope binds each of names to the corresponding concrete type,
// directly under parent (the declaring file's own scope, so imports stay
// visible). This - not any rewriting of source text - is what makes a cloned
// declaration's `T` mean a real type.
func typeParamScope(parent *Scope, names []string, args []Type) *Scope {
	scope := newScope(ScopeBlock, parent, ast.InvalidNode)
	for i, name := range names {
		if i >= len(args) {
			break
		}
		bound := args[i]
		scope.Define(&Symbol{
			Name:           name,
			Kind:           SymTypeParam,
			Decl:           ast.InvalidNode,
			Scope:          scope,
			TypeParamBound: &bound,
		})
	}
	return scope
}

// Instances yields every already-created specialization's own Symbol - the
// func/method Symbol a call site's callee resolved to, or the struct
// Symbol a `Name[args]` type position resolved to (see genericInstance) -
// letting a consumer (Workspace.References) treat every instantiation of
// one generic as the same logical symbol as the template itself.
func (gi *GenericInfo) Instances() iter.Seq[*Symbol] {
	return func(yield func(*Symbol) bool) {
		for _, inst := range gi.instances {
			sym := inst.sym
			if sym == nil && inst.structInfo != nil {
				sym = inst.structInfo.Symbol
			}
			if sym == nil {
				continue
			}
			if !yield(sym) {
				return
			}
		}
	}
}

// GenericInstances yields every already-created instantiation of s - a free
// generic func/struct template's own declaring Symbol (Generic set), or a
// generic struct method's (GenericMethod set - see Symbol.GenericMethod for
// why methods need their own case). Empty for any other Symbol, including an
// already-instantiated one: follow GenericTemplate first, or just use
// GenericFamily.
func (s *Symbol) GenericInstances() iter.Seq[*Symbol] {
	switch {
	case s.Generic != nil:
		return s.Generic.Instances()
	case s.GenericMethod != nil:
		return s.GenericMethod.Instances()
	default:
		return func(func(*Symbol) bool) {}
	}
}

// GenericFamily returns every Symbol a consumer should treat as one and the
// same declaration as s - s's own declaring template and every already-
// created instantiation of it - plus that declaring Symbol itself, the one
// DeclaringNameNode/Tree are meaningful against (s itself unless s is a
// specialization, see Symbol.GenericTemplate). An ordinary symbol yields
// just itself. A set, not an iter.Seq: every caller tests membership per
// entry while scanning a whole Info.Refs map.
func (s *Symbol) GenericFamily() (decl *Symbol, family map[*Symbol]bool) {
	decl = s
	if s.GenericTemplate != nil {
		decl = s.GenericTemplate
	}
	family = map[*Symbol]bool{
		s:    true,
		decl: true,
	}
	for inst := range decl.GenericInstances() {
		family[inst] = true
	}
	return decl, family
}

// Instances yields every already-created specialization of gm, across every
// receiver struct instantiation it's been reached through - SlotMap[int].Get
// and SlotMap[f64].Get are separate per-struct templates (gm.templates), each
// with their own instance cache, so this can't be one GenericInfo.Instances
// call (see Symbol.GenericMethod).
func (gm *GenericMethod) Instances() iter.Seq[*Symbol] {
	return func(yield func(*Symbol) bool) {
		for _, mt := range gm.templates {
			for sym := range mt.Instances() {
				if !yield(sym) {
					return
				}
			}
		}
	}
}

// instanceKey is a template's mangled specialization name - "SlotMap[int]",
// "Pair[int,string]" - and doubles as its instance-cache key. Type.String()
// alone isn't guaranteed injective (two same-named structs in different
// packages render identically), so a key already taken by a structurally
// different argument list gets a numeric suffix rather than silently aliasing
// onto it.
func (gi *GenericInfo) instanceKey(args []Type) string {
	base := gi.Symbol.Name + typeArgSuffix(args)
	key := base
	for n := 1; ; n++ {
		inst, taken := gi.instances[key]
		if !taken || typeListsEqual(inst.args, args) {
			return key
		}
		key = fmt.Sprintf("%s#%d", base, n)
	}
}

func typeArgSuffix(args []Type) string {
	if len(args) == 0 {
		return ""
	}
	parts := make([]string, len(args))
	for i, a := range args {
		parts[i] = a.String()
	}
	return "[" + strings.Join(parts, ",") + "]"
}

func typeListsEqual(a, b []Type) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !a[i].Equal(b[i]) {
			return false
		}
	}
	return true
}

// bindingsFor pairs every type-parameter name gi's clone needs bound - its
// receiver's already-known ones first, then its own - with the concrete types
// to bind them to.
func (gi *GenericInfo) bindingsFor(args []Type) (names []string, types []Type) {
	names = append(names, gi.OuterParams...)
	types = append(types, gi.OuterArgs...)
	return append(names, gi.Params...), append(types, args...)
}

// enqueueBody defers a specialization's body check until no other body is
// being checked. checker state like curFunc/move/loopDepth belongs to exactly
// one body at a time, so a body reached from the middle of another one would
// corrupt it; signatures and field types carry no such state and are computed
// eagerly instead, right where a caller needs them.
func (c *checker) enqueueBody(tree *ast.Tree, check func()) {
	c.pending = append(c.pending, func() {
		restore := c.pushPackage(tree)
		check()
		restore()
	})
}

// pushPackage is pushTree plus the accessing-package identity export checks
// read (checker.curPkgScope) - a specialization's body belongs to the package
// that DECLARED the generic, not to whichever call site triggered it.
func (c *checker) pushPackage(tree *ast.Tree) (restore func()) {
	prev := c.curPkgScope
	restoreTree := c.pushTree(tree)
	c.curPkgScope = c.treePackage[tree]
	return func() {
		restoreTree()
		c.curPkgScope = prev
	}
}

// drainPending runs every deferred specialization body check, including ones
// enqueued by those checks themselves (a generic function calling another).
func (c *checker) drainPending() {
	for len(c.pending) > 0 {
		job := c.pending[0]
		c.pending = c.pending[1:]
		job()
	}
}

// maxTypeArgDepth bounds how deeply one instantiation's type arguments may
// nest - the only genuinely unbounded case (`F[T]` instantiating `F[Box[T]]`,
// which has no fixed point). Real generics recurse over values, not types.
const maxTypeArgDepth = 32

// maxInstantiations is a whole-program safety net for a runaway that somehow
// widens without nesting - deliberately far above anything a real program
// reaches (a generic container's methods each cost one per element type).
const maxInstantiations = 5000

// refuseInstantiation reports whether gi may not be instantiated for args,
// diagnosing why - the two failure modes are genuinely different and say so.
func (c *checker) refuseInstantiation(gi *GenericInfo, args []Type, at ast.NodeIndex) bool {
	if typeListDepth(args) > maxTypeArgDepth {
		c.errorAt(at, "%s's type arguments nest more than %d levels deep - a generic that instantiates itself at an ever-larger type never terminates",
			gi.Symbol.Name, maxTypeArgDepth)
		return true
	}
	if c.instantiations < maxInstantiations {
		return false
	}
	if c.instantiations == maxInstantiations {
		c.instantiations++ // report exactly once, however many sites follow
		c.errorAt(at, "too many generic instantiations in one program (limit %d)", maxInstantiations)
	}
	return true
}

// typeListDepth is the deepest of args, or 0 for none.
func typeListDepth(args []Type) int {
	depth := 0
	for _, a := range args {
		depth = max(depth, typeDepth(a))
	}
	return depth
}

// typeDepth is how many levels of type nesting t has: 1 for a scalar, one
// more per pointer/array/map/func layer and per generic type argument.
func typeDepth(t Type) int {
	inner := 0
	if t.Elem != nil {
		inner = max(inner, typeDepth(*t.Elem))
	}
	if t.Key != nil {
		inner = max(inner, typeDepth(*t.Key))
	}
	if t.Return != nil {
		inner = max(inner, typeDepth(*t.Return))
	}
	for _, p := range t.Params {
		inner = max(inner, typeDepth(p))
	}
	if t.Struct != nil {
		inner = max(inner, typeListDepth(t.Struct.TypeArgs))
	}
	return inner + 1
}

// instantiateFunc specializes gi (a generic function or method template) for
// args, returning the specialization's own Symbol - the symbol every call site
// records into Info.Refs, so codegen sees an ordinary direct call to an
// ordinary function. nil when refuseInstantiation turns it down.
func (c *checker) instantiateFunc(gi *GenericInfo, args []Type, at ast.NodeIndex) *Symbol {
	key := gi.instanceKey(args)
	if inst, ok := gi.instances[key]; ok {
		return inst.sym
	}
	if c.refuseInstantiation(gi, args, at) {
		return nil
	}
	c.instantiations++

	tree := gi.Tree
	restore := c.pushTree(tree)
	defer restore()
	info := c.info
	firstNode := len(tree.Nodes)

	// For a method, gi is one receiver instantiation's own per-struct
	// template, whose Symbol is synthetic - gi.Method.Sym is the real
	// declaring Symbol (see Symbol.GenericMethod).
	templateSym := gi.Symbol
	if gi.Method != nil {
		templateSym = gi.Method.Sym
	}

	clone := tree.CloneSubtree(gi.Decl)
	sym := &Symbol{
		Name:            key,
		Kind:            SymFunc,
		Decl:            clone,
		Tree:            tree,
		Scope:           gi.Symbol.Scope,
		Exported:        gi.Symbol.Exported,
		GenericTemplate: templateSym,
	}
	info.Refs[tree.FuncName(clone)] = sym
	if gi.OwnerSym != nil {
		info.Refs[tree.FuncReceiver(clone)] = gi.OwnerSym
	}
	// Registered before the body is resolved so a recursive generic (a
	// specialization that calls itself) finds this instance instead of
	// recursing forever.
	gi.instances[key] = &genericInstance{
		args: args,
		sym:  sym,
	}

	names, types := gi.bindingsFor(args)
	c.resolverFor(tree).resolveFuncBody(typeParamScope(info.FileScope, names, types), clone)
	computeCapturesFrom(tree, info, c.diags, firstNode)
	info.Specializations = append(info.Specializations, clone)

	c.funcSigForDecl(clone) // the call site needs this now; the body can wait
	c.enqueueBody(tree, func() {
		c.checkFuncDecl(clone)
	})
	return sym
}

// instantiateStruct specializes gi (a generic struct template) for args,
// returning the concrete StructInfo every type position naming
// `Name[args...]` resolves to. Every method the template declares is
// instantiated alongside it - a method with type parameters of its own stays a
// template on the result, instantiated per call. nil when refuseInstantiation
// turns it down.
func (c *checker) instantiateStruct(gi *GenericInfo, args []Type, at ast.NodeIndex) *StructInfo {
	key := gi.instanceKey(args)
	if inst, ok := gi.instances[key]; ok {
		return inst.structInfo
	}
	if c.refuseInstantiation(gi, args, at) {
		return nil
	}
	c.instantiations++

	tree := gi.Tree
	restore := c.pushTree(tree)
	defer restore()
	info := c.info
	firstNode := len(tree.Nodes)

	clone := tree.CloneSubtree(gi.Decl)
	si := &StructInfo{
		Fields:       make(map[string]*Symbol),
		Methods:      make(map[string]*Symbol),
		Constructors: make(map[int]*Symbol),
		Operators:    make(map[string]*OperatorSet),
		Generic:      gi,
		TypeArgs:     args,
	}
	si.Symbol = &Symbol{
		Name:            key,
		Kind:            SymStruct,
		Decl:            clone,
		Tree:            tree,
		Scope:           gi.Symbol.Scope,
		Exported:        gi.Symbol.Exported,
		StructInfo:      si,
		GenericTemplate: gi.Symbol,
	}
	info.Structs[key] = si
	info.Refs[tree.StructName(clone)] = si.Symbol
	// Registered before anything below runs, so a self-referential generic
	// (a field or method mentioning its own instantiated type) terminates.
	gi.instances[key] = &genericInstance{
		args:       args,
		structInfo: si,
	}

	scope := typeParamScope(info.FileScope, gi.Params, args)
	r := c.resolverFor(tree)
	r.declareStructMembers(si, clone)
	r.resolveStructFieldTypes(scope, clone)
	for ctor := range tree.StructConstructors(clone) {
		r.resolveConstructorBody(scope, si, ctor)
	}
	for dtor := range tree.StructDestructors(clone) {
		r.resolveDestructorBody(scope, si, dtor)
	}
	for op := range tree.StructOperators(clone) {
		r.resolveOperatorBody(scope, si, op)
	}
	computeCapturesFrom(tree, info, c.diags, firstNode)
	info.Specializations = append(info.Specializations, clone)

	// Field types eagerly - codegen's struct layout and every use site need
	// them; the constructor/destructor bodies inside checkStructDecl can wait.
	for _, field := range tree.StructFields(clone) {
		c.typeFromNode(tree.Child(field, 1))
	}
	c.enqueueBody(tree, func() {
		c.checkStructDecl(clone)
	})

	for _, gm := range gi.Methods {
		mt := gm.templateFor(si)
		if len(mt.Params) > 0 {
			si.Methods[gm.Name] = mt.Symbol
			continue
		}
		if sym := c.instantiateFunc(mt, nil, at); sym != nil {
			si.Methods[gm.Name] = sym
		}
	}
	return si
}

// templateFor binds gm's receiver-clause type parameters to si's own concrete
// type arguments, producing the template gm's specializations for that one
// instantiated struct come from (memoized: two calls to SlotMap[int].Get must
// share an instance cache, while SlotMap[f64].Get's is separate).
func (gm *GenericMethod) templateFor(si *StructInfo) *GenericInfo {
	if mt, ok := gm.templates[si]; ok {
		return mt
	}
	mt := &GenericInfo{
		Decl:        gm.Decl,
		Tree:        gm.Tree,
		Params:      gm.OwnParams,
		OwnerSym:    si.Symbol,
		OuterParams: gm.RecvParams,
		OuterArgs:   si.TypeArgs,
		Method:      gm,
		instances:   make(map[string]*genericInstance),
	}
	mt.Symbol = &Symbol{
		Name:     gm.Name,
		Kind:     SymFunc,
		Decl:     gm.Decl,
		Tree:     gm.Tree,
		Scope:    si.Symbol.Scope,
		Exported: gm.Sym.Exported,
		Generic:  mt,
	}
	gm.templates[si] = mt
	return mt
}

// ---------------------------------------------------------------------------
// Check side: type arguments, inference, and call/type-position dispatch.
// ---------------------------------------------------------------------------

// genericRef reports whether n names a generic declaration's template,
// returning it - the single "is this an instantiation rather than ordinary
// indexing / an ordinary call" test, made here in sema exactly once so codegen
// never has to ask.
func (c *checker) genericRef(n ast.NodeIndex) (*GenericInfo, bool) {
	sym, ok := c.info.Refs[n]
	if !ok || sym.Generic == nil {
		return nil, false
	}
	return sym.Generic, true
}

// typeArgsFromNode converts n's (an IndexExpr's) explicit type arguments,
// checking the count against gi. Reports and returns false on any bad
// argument, so a caller never instantiates against a half-known substitution.
func (c *checker) typeArgsFromNode(n ast.NodeIndex, gi *GenericInfo) ([]Type, bool) {
	argNodes := c.tree.TypeArgNodes(n)
	if len(argNodes) != len(gi.Params) {
		c.errorAt(n, "%s takes %d type argument(s), got %d", gi.Symbol.Name, len(gi.Params), len(argNodes))
		return nil, false
	}
	args := make([]Type, len(argNodes))
	for i, argNode := range argNodes {
		t := c.typeArgFromNode(argNode)
		if t.IsInvalid() {
			c.errorAt(argNode, "invalid type argument for %s", gi.Params[i])
			return nil, false
		}
		args[i] = t
	}
	return args, true
}

// typeArgFromNode is typeFromNode plus the one shape only an expression-
// position type argument can produce: `Foo[*T]`'s `*T`, which the Pratt parser
// builds as a unary dereference rather than a PointerType (see the parser's
// atTypeOnlyStart for why `*` stays ambiguous there).
func (c *checker) typeArgFromNode(n ast.NodeIndex) Type {
	if c.tree.Nodes[n].Kind == enums.NodeKinds.UnaryExpr && c.tree.Nodes[n].Tok.Lexeme == enums.Lexemes.Asterisk {
		elem := c.typeArgFromNode(c.tree.Child(n, 0))
		if elem.IsInvalid() {
			return invalidType
		}
		return Type{
			Kind: TypePointer,
			Elem: &elem,
		}
	}
	return c.typeFromNode(n)
}

// checkGenericTypeExpr types a `Name[args...]` type position, instantiating
// the named generic struct. Returns false when n isn't a generic instantiation
// at all, leaving the caller's ordinary interpretation of the same IndexExpr
// shape (array/map indexing) alone.
func (c *checker) checkGenericTypeExpr(n ast.NodeIndex) (Type, bool) {
	target := c.tree.Child(n, 0)
	gi, ok := c.genericRef(target)
	if !ok {
		return invalidType, false
	}
	if gi.Symbol.Kind != SymStruct {
		c.errorAt(n, "%s is a generic function, not a type", gi.Symbol.Name)
		return invalidType, true
	}
	args, ok := c.typeArgsFromNode(n, gi)
	if !ok {
		return invalidType, true
	}
	si := c.instantiateStruct(gi, args, n)
	if si == nil {
		return invalidType, true
	}
	return Type{
		Kind:   TypeStruct,
		Struct: si,
	}, true
}

// resolveGenericStructCallee instantiates callee (an IndexExpr) when it names
// a generic struct, recording the instantiation's own Symbol over callee's
// Info.Refs entry so every later reader sees a plain struct-type callee.
// isGenericStruct is false for anything else wearing the same IndexExpr shape,
// which the caller must then leave entirely alone; ok is false when it IS one
// but couldn't be instantiated (already reported).
func (c *checker) resolveGenericStructCallee(callee ast.NodeIndex) (ok, isGenericStruct bool) {
	gi, found := c.genericRef(c.tree.Child(callee, 0))
	if !found || gi.Symbol.Kind != SymStruct {
		return false, false
	}
	args, resolved := c.typeArgsFromNode(callee, gi)
	if !resolved {
		return false, true
	}
	si := c.instantiateStruct(gi, args, callee)
	if si == nil {
		return false, true
	}
	c.info.Refs[callee] = si.Symbol
	return true, true
}

// checkGenericCall type-checks a call whose callee names a generic function or
// method, instantiating it and rewriting the callee's own Info.Refs entry to
// point at the specialization - after which the call is an ordinary direct
// call in every later pass. Returns false when the callee isn't generic.
func (c *checker) checkGenericCall(n, callee ast.NodeIndex, args []ast.NodeIndex) (Type, bool) {
	if c.rejectMethodTypeArgs(callee) {
		return invalidType, true
	}
	gi, explicit, ok := c.genericCallee(callee)
	if !ok {
		return invalidType, false
	}

	argTypes := make([]Type, len(args))
	for i, a := range args {
		argTypes[i] = c.checkValueExpr(a)
	}

	// Arity first: it's the same for every specialization, and a wrong count
	// is a far more useful answer than the inference failure it would cause.
	if want := len(gi.Tree.Children(gi.Tree.FuncParamList(gi.Decl))); len(args) != want {
		c.errorAtNodes(args, n, "wrong number of arguments in call: got %d, want %d", len(args), want)
		return invalidType, true
	}

	var typeArgs []Type
	if explicit != ast.InvalidNode {
		typeArgs, ok = c.typeArgsFromNode(explicit, gi)
	} else {
		typeArgs, ok = c.inferTypeArgs(gi, argTypes, n)
	}
	if !ok {
		return invalidType, true
	}

	sym := c.instantiateFunc(gi, typeArgs, n)
	if sym == nil {
		return invalidType, true
	}
	c.info.Refs[callee] = sym
	if explicit != ast.InvalidNode {
		// callee is the whole Name[T] IndexExpr here - also point the bare
		// name's own Ref at sym, or hovering directly over "Name" (not the
		// [T] part) still finds genericRef's original template-pointing
		// resolution instead of this instantiation's real signature.
		c.info.Refs[c.tree.Child(callee, 0)] = sym
	}

	restore := c.pushTree(sym.Tree)
	sig := c.funcSigForDecl(sym.Decl)
	restore()

	c.checkCallArgs(n, args, argTypes, sig)
	return sig.Return, true
}

// rejectMethodTypeArgs reports `p.m[int](x)` - explicit type arguments on a
// method call have no supported spelling (see BLOCKERS.md). Without this, the
// ordinary index-expression path reports "m is a method, not a field (call it
// with ())" about a call that already has its `()`.
func (c *checker) rejectMethodTypeArgs(callee ast.NodeIndex) bool {
	if c.tree.Nodes[callee].Kind != enums.NodeKinds.IndexExpr {
		return false
	}
	// A package-qualified generic function (`lib.Id[int](x)`) wears the same
	// shape but is an ordinary explicit instantiation.
	target := c.tree.Child(callee, 0)
	if c.tree.Nodes[target].Kind != enums.NodeKinds.MemberExpr || c.memberObjectIsPackage(target) {
		return false
	}
	sym, ok := c.resolveMember(target)
	if !ok || sym.Kind != SymFunc {
		return false
	}
	if sym.Generic != nil {
		c.errorAt(callee, "explicit type arguments are not supported on a method call - %s's type parameters are inferred from its arguments", sym.Name)
	} else {
		c.errorAt(callee, "%s is not generic and takes no type arguments", sym.Name)
	}
	return true
}

// genericCallee classifies a call's callee: the template it names, and the
// IndexExpr carrying explicit type arguments (InvalidNode when the call
// relies on inference). Only a generic *function/method* template counts - a
// generic struct's own `Box[int](args)` is a constructor call, already claimed
// by checkConstructorCall before this ever runs. A method callee is never
// explicit either - see rejectMethodTypeArgs.
func (c *checker) genericCallee(callee ast.NodeIndex) (gi *GenericInfo, explicit ast.NodeIndex, ok bool) {
	switch c.tree.Nodes[callee].Kind {
	case enums.NodeKinds.Ident:
		gi, ok = c.genericRef(callee)
	case enums.NodeKinds.IndexExpr:
		gi, ok = c.genericRef(c.tree.Child(callee, 0))
		explicit = callee
	case enums.NodeKinds.MemberExpr:
		var sym *Symbol
		if sym, ok = c.resolveMember(callee); ok {
			gi = sym.Generic
		}
	}
	if !ok || gi == nil || gi.Symbol.Kind != SymFunc {
		return nil, ast.InvalidNode, false
	}
	return gi, explicit, true
}

// inferTypeArgs solves gi's own type parameters from a call's actual argument
// types, by matching each declared parameter's type-position node - which may
// mention those parameters anywhere inside it - against the corresponding
// argument's already-concrete type. A parameter appearing in no parameter
// position at all (only in the return type, say) can't be inferred and needs
// explicit instantiation instead.
func (c *checker) inferTypeArgs(gi *GenericInfo, argTypes []Type, at ast.NodeIndex) ([]Type, bool) {
	u := &unifier{
		tree:   gi.Tree,
		info:   c.infos[gi.Tree],
		params: make(map[string]bool, len(gi.Params)),
		bound:  make(map[string]Type, len(gi.Params)),
	}
	// Only gi's OWN parameters are solvable - the receiver's are already fixed
	// by the receiver's type, so `func (SlotMap[T]) Put[U](a T, b U)` solves U
	// and leaves T alone (unify skips any name not in params).
	for _, p := range gi.Params {
		u.params[p] = true
	}

	paramNodes := gi.Tree.Children(gi.Tree.FuncParamList(gi.Decl))
	for i, paramNode := range paramNodes {
		if i >= len(argTypes) || argTypes[i].IsInvalid() {
			continue
		}
		u.unify(gi.Tree.Child(paramNode, 1), c.concreteArgType(argTypes[i]))
	}

	if u.conflict != "" {
		c.errorAt(at, "cannot infer type parameter %s of %s: it would be both %s and %s",
			u.conflict, gi.Symbol.Name, u.conflictA, u.conflictB)
		return nil, false
	}

	args := make([]Type, len(gi.Params))
	for i, p := range gi.Params {
		t, bound := u.bound[p]
		if !bound {
			c.errorAt(at, "cannot infer type parameter %s of %s from the arguments - instantiate it explicitly, e.g. %s[%s](...)",
				p, gi.Symbol.Name, gi.Symbol.Name, strings.Join(gi.Params, ", "))
			return nil, false
		}
		args[i] = t
	}
	return args, true
}

// concreteArgType is the type inference actually matches against: an untyped
// constant contributes its default type (`Sum(1, 2)` infers int), since a type
// parameter must end up naming a real type. Retyping the argument node itself
// is deliberately left to the ordinary argument check against the
// specialization's now-concrete parameter type.
func (c *checker) concreteArgType(t Type) Type {
	if t.IsUntyped() {
		return c.defaultUntyped(t)
	}
	return t
}

// unifier matches a template's declared type-position nodes against concrete
// types, accumulating one binding per type-parameter name. A parameter bound
// twice to different types is recorded as a conflict rather than silently
// keeping either - `Pair[T](a T, b T)` called with mismatched arguments is an
// inference failure, not a coin flip.
type unifier struct {
	tree   *ast.Tree
	info   *Info
	params map[string]bool
	bound  map[string]Type

	conflict             string
	conflictA, conflictB Type
}

func (u *unifier) unify(node ast.NodeIndex, arg Type) {
	if node == ast.InvalidNode || arg.IsInvalid() {
		return
	}
	switch u.tree.Nodes[node].Kind {
	case enums.NodeKinds.Ident:
		name := u.tree.Text(node)
		if !u.params[name] {
			return // a concrete type name - nothing to learn from it
		}
		if prior, ok := u.bound[name]; ok {
			if !prior.Equal(arg) && u.conflict == "" {
				u.conflict, u.conflictA, u.conflictB = name, prior, arg
			}
			return
		}
		u.bound[name] = arg
	case enums.NodeKinds.PointerType:
		if arg.Kind == TypePointer {
			u.unify(u.tree.Child(node, 0), *arg.Elem)
		}
	case enums.NodeKinds.ArrayType:
		if arg.Kind == TypeArray {
			u.unify(u.tree.Child(node, 1), *arg.Elem)
		}
	case enums.NodeKinds.MapType:
		if arg.Kind == TypeMap {
			u.unify(u.tree.Child(node, 0), *arg.Key)
			u.unify(u.tree.Child(node, 1), *arg.Elem)
		}
	case enums.NodeKinds.FuncType:
		if arg.Kind != TypeFunc {
			return
		}
		paramNodes := u.tree.Children(u.tree.Child(node, 0))
		for i, p := range paramNodes {
			if i < len(arg.Params) {
				u.unify(p, arg.Params[i])
			}
		}
		if ret := u.tree.Child(node, 1); ret != ast.InvalidNode && arg.Return != nil {
			u.unify(ret, *arg.Return)
		}
	case enums.NodeKinds.IndexExpr:
		// A declared `SlotMap[T]` parameter against an actual, already
		// instantiated SlotMap[int] - matched positionally against the
		// argument's own recorded type arguments, and only when both name the
		// same template.
		si := arg.Underlying().Struct
		if si == nil || si.Generic == nil {
			return
		}
		// Matched by name against the shared generic-struct catalog, not via
		// Info.Refs: a template is never resolved, so its own nodes have no
		// Refs entries at all.
		if u.info.Generics[u.tree.Text(u.tree.Child(node, 0))] != si.Generic {
			return
		}
		for i, argNode := range u.tree.TypeArgNodes(node) {
			if i < len(si.TypeArgs) {
				u.unify(argNode, si.TypeArgs[i])
			}
		}
	}
}
