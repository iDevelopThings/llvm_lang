// Package sema is the semantic-analysis layer built on top of a parsed
// ast.Tree: name/scope resolution first (this file), type checking next.
package sema

import (
	"llvm_lang/src/ast"
	"llvm_lang/src/diag"
	"llvm_lang/src/enums"
)

// Info is the result of resolving one file (one *ast.Tree) within a package:
// every reference's resolved Symbol, every scope-introducing node's Scope,
// and (once Check has also run - see typecheck.go) every expression's
// inferred Type - all keyed by NodeIndex, meaningful only relative to this
// Info's own Tree (see ast.NodeIndex's doc comment and Symbol.Tree's) - the
// same side-table pattern as the rest of the compiler: semantic data lives
// here, not bolted onto ast.Node.
//
// Structs is the one field that ISN'T per-file: every Info produced by the
// same ResolvePackage call shares the exact same Structs map instance (see
// ResolvePackage) - a struct declared in one file is just as much a part of
// the package's shared catalog as one declared in the file whose Info you
// happen to be holding. This needs no special-casing at read sites: existing
// code that indexes info.Structs by name already gets the whole package's
// catalog, not just "this file's own structs".
//
// Refs covers both declaring occurrences (the name in `var a int`) and
// referencing ones (a later use of `a`) uniformly - both map to the same
// *Symbol, so "what does this Ident mean" doesn't need to special-case
// which kind of occurrence it is. Once Check has run, Refs additionally
// covers what Resolve alone deliberately left unresolved: a MemberExpr's
// field/method name and a CompositeLit's keyed element names (see
// resolveExpr and resolveCompositeLit below).
//
// Types is nil until Check runs - Resolve alone has no type information to
// populate it with.
type Info struct {
	Refs    map[ast.NodeIndex]*Symbol
	Scopes  map[ast.NodeIndex]*Scope
	Structs map[string]*StructInfo
	Types   map[ast.NodeIndex]Type
}

// boundImport is one file's own import binding with its target already
// resolved to a real *PackageResult - the resolver's own internal shape
// (see resolver.fileImports); the public sema.FileImport (program.go) names
// its target only indirectly (TargetKey), since ResolveProgram may not have
// resolved that target unit yet at the point the caller builds its input.
type boundImport struct {
	LocalName string
	Target    *PackageResult
}

// resolver's tree/info/bag fields always describe "whichever file is
// currently being walked" - see enterFile. Resolve itself never needs to
// follow a Symbol into a different file than the one currently being walked
// (unlike sema/typecheck.go's checker - see that file's own doc comment for
// why Check's lazy signature/decl-type computation genuinely does need
// that): every node Resolve visits belongs to the tree it started from,
// and a cross-file Symbol it looks up (scope.Lookup) is only ever recorded
// into Refs, never dereferenced back into its own declaring file's tree.
type resolver struct {
	infos map[*ast.Tree]*Info
	bags  map[*ast.Tree]*diag.Bag

	pkg     *Scope                 // shared package scope, every file's top-level names
	structs map[string]*StructInfo // shared struct catalog, every file's structs

	// fileImports is this package's input (nil for a plain single-package
	// ResolvePackage call, which has no imports at all): each file's own
	// already-resolved `import "path"` bindings, keyed by that file's own
	// Tree - see PackageUnit.FileImports's doc comment for why this is
	// file-scoped rather than a plain package-wide list. Unlike the public
	// FileImport (which names its target only indirectly, by TargetKey - see
	// ResolveProgram), each boundImport here already carries its target's
	// real *PackageResult - ResolveProgram resolves TargetKey against its
	// own accumulating results table before ever calling resolveOnePackage.
	fileImports map[*ast.Tree][]boundImport

	// fileScopes is this package's own per-file ScopeFile scopes (built by
	// buildFileScope, one per tree, before any of the three main passes
	// run), each holding that file's own import bindings - see ScopeKind's
	// doc comment. Every per-file resolution pass (resolveVarDeclBody,
	// resolveFuncBody, resolveStructFieldTypes) looks names up starting from
	// this scope instead of r.pkg directly, so an import is visible in the
	// file that declared it and nowhere else, while every top-level
	// package-level name remains visible everywhere in the package exactly
	// as before (declareStruct/declareLocal/declareFunc still declare
	// directly into the shared r.pkg, never a fileScope).
	fileScopes map[*ast.Tree]*Scope

	tree *ast.Tree
	info *Info
	bag  *diag.Bag
}

// enterFile switches the resolver's current-file bookkeeping to tree,
// recording tree's own Root -> shared package Scope mapping as it does (see
// resolveFile's per-file passes, all of which call this once per file before
// walking that file's own declarations).
func (r *resolver) enterFile(tree *ast.Tree) {
	r.tree = tree
	r.info = r.infos[tree]
	r.bag = r.bags[tree]
	r.info.Scopes[tree.Root] = r.pkg
}

// Resolve walks tree (the output of parser.ParseFile) and resolves every
// name reference it can from lexical scope alone: variables, parameters,
// functions, struct type names, and `this`. Struct field/method access
// (`p.field`, `p.method()`) isn't resolved here - see StructInfo's doc
// comment - because that needs to know p's type, which belongs to the
// type-checking pass this feeds into, not lexical scoping.
//
// Resolve is the single-file case of ResolvePackage - see its doc comment
// for the multi-file package entry point this wraps.
func Resolve(tree *ast.Tree) (*Info, *diag.Bag) {
	infos, bags := ResolvePackage([]*ast.Tree{tree})
	return infos[tree], bags[tree]
}

// ResolvePackage resolves every file in trees as one package: every file's
// top-level struct/var/func names (and every method) are declared into one
// shared package Scope, and every struct is entered into one shared catalog -
// so a name declared in one file is visible from every other file in the
// same package, exactly like within a single file today, regardless of which
// file is processed first or which order trees is given in (see
// LANGUAGE.md's "Multi-file packages" section).
//
// This package has no imports of its own (fileImports is nil) - see
// ResolveProgram for the multi-package entry point that does.
//
// Returns one *Info and one *diag.Bag per input tree, keyed by the tree
// itself - a diagnostic's Pos is only meaningful relative to the one file
// it's reported against (see diag.Bag's own doc comment), so unlike Structs/
// the package Scope, diagnostics are never merged across files.
func ResolvePackage(trees []*ast.Tree) (map[*ast.Tree]*Info, map[*ast.Tree]*diag.Bag) {
	infos, bags, _ := resolveOnePackage("", trees, nil)
	return infos, bags
}

// resolveOnePackage is the shared guts of ResolvePackage/ResolveProgram: it
// resolves one package's files (with its own fresh universe-rooted package
// Scope - never shared with any other package, so two packages can declare
// the same name with no collision) and also returns a *PackageResult
// exposing that package's own Scope/Structs catalog, for a second package's
// import binding (see ResolveProgram) to be wired up against.
func resolveOnePackage(name string, trees []*ast.Tree, fileImports map[*ast.Tree][]boundImport) (map[*ast.Tree]*Info, map[*ast.Tree]*diag.Bag, *PackageResult) {
	r := &resolver{
		infos:       make(map[*ast.Tree]*Info, len(trees)),
		bags:        make(map[*ast.Tree]*diag.Bag, len(trees)),
		structs:     make(map[string]*StructInfo),
		fileImports: fileImports,
		fileScopes:  make(map[*ast.Tree]*Scope, len(trees)),
	}
	universe := universeScope()
	r.pkg = newScope(ScopePackage, universe, ast.InvalidNode)

	for _, tree := range trees {
		r.infos[tree] = &Info{
			Refs:    make(map[ast.NodeIndex]*Symbol),
			Scopes:  make(map[ast.NodeIndex]*Scope),
			Structs: r.structs,
		}
		r.bags[tree] = diag.NewBag()
	}

	r.resolvePackage(trees)

	out := make(map[*ast.Tree]*Info, len(trees))
	diags := make(map[*ast.Tree]*diag.Bag, len(trees))
	for _, tree := range trees {
		out[tree] = r.infos[tree]
		diags[tree] = r.bags[tree]
	}
	return out, diags, &PackageResult{
		Name:    name,
		Scope:   r.pkg,
		Structs: r.structs,
	}
}

func (r *resolver) errorAt(n ast.NodeIndex, format string, a ...any) {
	span := r.tree.SpanOf(n)
	r.bag.ErrorfSpan(span.Start, span.End, format, a...)
}

// errorAtLabel is errorAt, plus a short trailing hint (see
// diag.Bag.ErrorfLabel) pointing at n's own "main" token (Node.Tok) rather
// than n's whole span - used where a sub-token is the more precise thing to
// underline (e.g. just the member name in `pkg.member`, not the whole
// qualified expression).
func (r *resolver) errorAtLabel(n ast.NodeIndex, label, format string, a ...any) {
	tok := r.tree.Nodes[n].Tok
	r.bag.ErrorfLabel(tok.Start, tok.End, label, format, a...)
}

// resolvePackage processes every file in trees in four passes, so
// declaration order both within a file and across the whole package never
// matters (Go allows top-level declarations to reference each other
// regardless of which comes first, or which file declares them) - the same
// guarantee the single-file resolveFile used to provide one level down, now
// applied across every file before any file's bodies are resolved:
//  0. every file's own ScopeFile, populated with that file's own import
//     bindings (see buildFileScope) - needs nothing but r.pkg to exist
//     already, so it can run before any declaration is processed at all.
//  1. every file's struct type names and field catalogs, so methods (pass 2)
//     and type positions (pass 3) can always find them regardless of which
//     file declares the struct
//  2. every file's top-level var/free-function names, and every method
//     (attached to the struct catalog pass 1 already built)
//  3. every file's own declaration content - types, initializers, function
//     bodies - now that every top-level name in the whole package is known.
//     Uses each file's own fileScope (not r.pkg directly) as the base scope,
//     so an import is visible only in the file that declared it.
func (r *resolver) resolvePackage(trees []*ast.Tree) {
	for _, tree := range trees {
		r.enterFile(tree)
		r.buildFileScope(tree)
	}
	for _, tree := range trees {
		r.enterFile(tree)
		for decl := range tree.TopLevelDeclsOfKind(enums.NodeKinds.StructDecl) {
			r.declareStruct(r.pkg, decl)
		}
	}
	for _, tree := range trees {
		r.enterFile(tree)
		for _, decl := range tree.Children(tree.Root) {
			switch tree.Nodes[decl].Kind {
			case enums.NodeKinds.VarDecl:
				r.declareLocal(r.pkg, decl, tree.Child(decl, 0), SymVar)
			case enums.NodeKinds.FuncDecl:
				r.declareFunc(r.pkg, decl)
			}
		}
	}
	for _, tree := range trees {
		r.enterFile(tree)
		fileScope := r.fileScopes[tree]
		for _, decl := range tree.Children(tree.Root) {
			switch tree.Nodes[decl].Kind {
			case enums.NodeKinds.VarDecl:
				r.resolveVarDeclBody(fileScope, decl)
			case enums.NodeKinds.FuncDecl:
				r.resolveFuncBody(fileScope, decl)
			case enums.NodeKinds.StructDecl:
				r.resolveStructFieldTypes(fileScope, decl)
			}
		}
	}
}

// buildFileScope creates tree's own ScopeFile scope (child of r.pkg) and
// populates it with every import binding tree's own ImportDecl nodes
// declared, zipped in file order against fileImports[tree] (already
// resolved to a target *PackageResult by whoever built this resolver's
// input - the loader package for a real program, or a test's own fixture -
// sema has no filesystem/path-resolution logic of its own; see
// PackageUnit's doc comment). A duplicate local name (two imports resolving
// to the same local name in one file) is reported as a redeclaration, same
// as any other Scope.Define conflict.
func (r *resolver) buildFileScope(tree *ast.Tree) {
	fileScope := newScope(ScopeFile, r.pkg, tree.Root)
	r.fileScopes[tree] = fileScope

	imports := r.fileImports[tree]
	idx := 0
	for decl := range tree.TopLevelDeclsOfKind(enums.NodeKinds.ImportDecl) {
		if idx >= len(imports) {
			break // an unresolved import - already reported by the loader
		}
		imp := imports[idx]
		idx++

		sym := &Symbol{
			Name:    imp.LocalName,
			Kind:    SymPackage,
			Decl:    decl,
			Tree:    tree,
			Scope:   fileScope,
			Package: imp.Target,
		}
		if _, redeclared := fileScope.Define(sym); redeclared {
			r.errorAt(decl, "%s redeclared in this file (previously imported)", sym.Name)
			continue
		}
		r.info.Refs[decl] = sym
	}
}

// declareLocal registers a name-bearing node (VarDecl, ShortVarDecl, or
// Param - anything whose first child is its name Ident) into scope,
// reporting a redeclaration and recording the Ident's Ref either way.
func (r *resolver) declareLocal(scope *Scope, n, nameNode ast.NodeIndex, kind SymbolKind) *Symbol {
	name := r.tree.Text(nameNode)
	sym := &Symbol{
		Name:     name,
		Kind:     kind,
		Decl:     n,
		Tree:     r.tree,
		Scope:    scope,
		Exported: isExportedName(name),
	}
	if prior, redeclared := scope.Define(sym); redeclared {
		r.errorAt(n, "%s redeclared in this scope (previously declared as %s)", sym.Name, prior.Kind)
	}
	r.info.Refs[nameNode] = sym
	return sym
}

func (r *resolver) declareStruct(pkg *Scope, decl ast.NodeIndex) {
	nameNode := r.tree.Child(decl, 0)
	sym := r.declareLocal(pkg, decl, nameNode, SymStruct)

	info := &StructInfo{
		Symbol:  sym,
		Fields:  make(map[string]*Symbol),
		Methods: make(map[string]*Symbol),
	}
	sym.StructInfo = info
	r.info.Structs[sym.Name] = info

	for _, field := range r.tree.StructFields(decl) {
		fieldNameNode := r.tree.Child(field, 0)
		fieldName := r.tree.Text(fieldNameNode)
		fieldSym := &Symbol{
			Name:     fieldName,
			Kind:     SymField,
			Decl:     field,
			Tree:     r.tree,
			Scope:    pkg,
			Exported: isExportedName(fieldName),
		}
		if _, exists := info.Fields[fieldName]; exists {
			r.errorAt(field, "field %s redeclared in struct %s", fieldName, sym.Name)
		} else {
			info.Fields[fieldName] = fieldSym
		}
		r.info.Refs[fieldNameNode] = fieldSym
	}
}

func (r *resolver) resolveStructFieldTypes(pkg *Scope, decl ast.NodeIndex) {
	for _, field := range r.tree.StructFields(decl) {
		r.resolveType(pkg, r.tree.Child(field, 1))
	}
}

// declareFunc registers a free function into package scope, or - if it has
// a receiver clause - attaches it to that receiver struct's method table
// instead (see StructInfo: methods aren't in package scope at all, since
// they're looked up via the receiver's type, not a plain name lookup).
func (r *resolver) declareFunc(pkg *Scope, decl ast.NodeIndex) {
	receiver := r.tree.FuncReceiver(decl)
	nameNode := r.tree.FuncName(decl)

	if receiver == ast.InvalidNode {
		r.declareLocal(pkg, decl, nameNode, SymFunc)
		return
	}

	name := r.tree.Text(nameNode)
	sym := &Symbol{
		Name:     name,
		Kind:     SymFunc,
		Decl:     decl,
		Tree:     r.tree,
		Exported: isExportedName(name),
	}
	r.info.Refs[nameNode] = sym
	r.addMethod(receiver, sym)
}

func (r *resolver) addMethod(receiver ast.NodeIndex, sym *Symbol) {
	receiverName := r.tree.Text(receiver)
	info, ok := r.info.Structs[receiverName]
	if !ok {
		r.errorAt(receiver, "undefined: %s (method receiver must be a declared struct)", receiverName)
		return
	}
	r.info.Refs[receiver] = info.Symbol
	sym.Scope = info.Symbol.Scope
	if _, exists := info.Methods[sym.Name]; exists {
		r.errorAt(sym.Decl, "method %s redeclared on struct %s", sym.Name, receiverName)
		return
	}
	info.Methods[sym.Name] = sym
}

// resolveVarDeclBody resolves a VarDecl's type (as a type reference) and
// initializer (as a value) - shared between top-level and local var decls.
func (r *resolver) resolveVarDeclBody(scope *Scope, decl ast.NodeIndex) {
	if typ := r.tree.Child(decl, 1); typ != ast.InvalidNode {
		r.resolveType(scope, typ)
	}
	if init := r.tree.Child(decl, 2); init != ast.InvalidNode {
		r.resolveExpr(scope, init)
	}
}

func (r *resolver) resolveFuncBody(pkg *Scope, decl ast.NodeIndex) {
	receiver := r.tree.FuncReceiver(decl)
	paramList := r.tree.FuncParamList(decl)
	returnType := r.tree.FuncReturnType(decl)
	body := r.tree.FuncBody(decl)

	fnScope := newScope(ScopeFunc, pkg, decl)
	r.info.Scopes[decl] = fnScope

	if receiver != ast.InvalidNode {
		if info, ok := r.info.Structs[r.tree.Text(receiver)]; ok {
			fnScope.Receiver = &Symbol{
				Name: "this",
				Kind: SymReceiver,
				Decl: info.Symbol.Decl,
				// The receiver struct may be declared in a different file
				// than the method itself (see Symbol.Tree's doc comment) -
				// this must be the struct's own owning tree, not r.tree
				// (this method's file), so a later cross-file dereference
				// (sema/typecheck.go's checkThisExpr) reads the right one.
				Tree:  info.Symbol.Tree,
				Scope: fnScope,
			}
		}
		// else: already reported by addMethod during declaration.
	}

	for _, param := range r.tree.Children(paramList) {
		r.declareLocal(fnScope, param, r.tree.Child(param, 0), SymParam)
		r.resolveType(fnScope, r.tree.Child(param, 1))
	}

	if returnType != ast.InvalidNode {
		r.resolveType(fnScope, returnType)
	}

	r.resolveBlock(fnScope, body)
}

// resolveBlock creates a nested ScopeBlock for a Block node and resolves
// each statement in file order, so a var's scope only begins after its own
// declaring statement - `x := x + 1` must resolve the right-hand `x`
// against whatever `x` was visible *before* this statement, not the new
// one it's about to declare.
func (r *resolver) resolveBlock(parent *Scope, block ast.NodeIndex) *Scope {
	scope := newScope(ScopeBlock, parent, ast.InvalidNode)
	r.info.Scopes[block] = scope
	for _, stmt := range r.tree.Children(block) {
		r.resolveStmt(scope, stmt)
	}
	return scope
}

func (r *resolver) resolveStmt(scope *Scope, n ast.NodeIndex) {
	switch r.tree.Nodes[n].Kind {
	case enums.NodeKinds.VarDecl:
		r.resolveVarDeclBody(scope, n)
		r.declareLocal(scope, n, r.tree.Child(n, 0), SymVar)
	case enums.NodeKinds.ShortVarDecl:
		r.resolveExpr(scope, r.tree.Child(n, 1)) // before declaring - see resolveBlock's comment
		r.declareLocal(scope, n, r.tree.Child(n, 0), SymVar)
	case enums.NodeKinds.AssignStmt:
		r.resolveExpr(scope, r.tree.Child(n, 0))
		r.resolveExpr(scope, r.tree.Child(n, 1))
	case enums.NodeKinds.IncDecStmt:
		r.resolveExpr(scope, r.tree.Child(n, 0))
	case enums.NodeKinds.ExprStmt:
		r.resolveExpr(scope, r.tree.Child(n, 0))
	case enums.NodeKinds.ReturnStmt:
		if v := r.tree.Child(n, 0); v != ast.InvalidNode {
			r.resolveExpr(scope, v)
		}
	case enums.NodeKinds.Block:
		r.resolveBlock(scope, n)
	case enums.NodeKinds.IfStmt:
		r.resolveIfStmt(scope, n)
	case enums.NodeKinds.ForStmt:
		r.resolveForStmt(scope, n)
	}
}

func (r *resolver) resolveIfStmt(scope *Scope, n ast.NodeIndex) {
	r.resolveExpr(scope, r.tree.Child(n, 0))
	r.resolveStmtInOwnScope(scope, r.tree.Child(n, 1))
	if elseBranch := r.tree.Child(n, 2); elseBranch != ast.InvalidNode {
		r.resolveStmtInOwnScope(scope, elseBranch)
	}
}

// resolveStmtInOwnScope resolves a statement that needs its own nested
// scope even when it isn't itself a Block - the single-statement form of
// `if cond: stmt`. A Block or another IfStmt (an `else if` chain) already
// manage their own scoping via resolveBlock/resolveIfStmt recursively.
func (r *resolver) resolveStmtInOwnScope(parent *Scope, n ast.NodeIndex) {
	switch r.tree.Nodes[n].Kind {
	case enums.NodeKinds.Block:
		r.resolveBlock(parent, n)
	case enums.NodeKinds.IfStmt:
		r.resolveIfStmt(parent, n)
	default:
		r.resolveStmt(newScope(ScopeBlock, parent, ast.InvalidNode), n)
	}
}

// resolveForStmt wraps the whole statement (all three header clauses plus
// the body) in one Scope, so `for i := 0; ...` makes `i` visible to the
// condition/post/body but nowhere after the loop ends.
func (r *resolver) resolveForStmt(parent *Scope, n ast.NodeIndex) {
	scope := newScope(ScopeBlock, parent, ast.InvalidNode)
	r.info.Scopes[n] = scope

	if init := r.tree.Child(n, 0); init != ast.InvalidNode {
		r.resolveStmt(scope, init)
	}
	if cond := r.tree.Child(n, 1); cond != ast.InvalidNode {
		r.resolveExpr(scope, cond)
	}
	if post := r.tree.Child(n, 2); post != ast.InvalidNode {
		r.resolveStmt(scope, post)
	}
	r.resolveBlock(scope, r.tree.Child(n, 3))
}

// resolveType resolves n as a type reference (an Ident naming a builtin or
// struct type, an ArrayType, or a FuncType - see LANGUAGE.md's "First-class
// functions" section). Distinct from resolveExpr because the *role* differs
// even where the node shape doesn't: an Ident in type position must resolve
// to something IsType(), not a variable.
func (r *resolver) resolveType(scope *Scope, n ast.NodeIndex) {
	switch r.tree.Nodes[n].Kind {
	case enums.NodeKinds.Ident:
		name := r.tree.Text(n)
		sym, ok := scope.Lookup(name)
		if !ok {
			r.errorAtLabel(n, "not found", "undefined: %s", name)
			return
		}
		if !sym.Kind.IsType() {
			r.errorAt(n, "%s is not a type (declared as %s)", name, sym.Kind)
		}
		r.info.Refs[n] = sym
	case enums.NodeKinds.MemberExpr:
		r.resolveTypeMemberExpr(scope, n)
	case enums.NodeKinds.ArrayType:
		if size := r.tree.Child(n, 0); size != ast.InvalidNode {
			r.resolveExpr(scope, size)
		}
		r.resolveType(scope, r.tree.Child(n, 1))
	case enums.NodeKinds.FuncType:
		paramList := r.tree.Child(n, 0)
		for _, param := range r.tree.Children(paramList) {
			r.resolveType(scope, param)
		}
		if ret := r.tree.Child(n, 1); ret != ast.InvalidNode {
			r.resolveType(scope, ret)
		}
	}
}

// resolveTypeMemberExpr resolves a package-qualified type reference
// (`pkg.Point` - parsed as a MemberExpr node in type position, see
// parser.parseTypeExpr) - the type-position counterpart to
// resolvePackageMemberExpr below. Unlike an ordinary struct field/method
// access, this needs no type information at all (a package's own scope is
// already fully built by the time anything imports it - see
// ResolveProgram's dependency-ordering guarantee), so - unlike a value-level
// MemberExpr - it resolves entirely here, during Resolve, not deferred to
// Check.
func (r *resolver) resolveTypeMemberExpr(scope *Scope, n ast.NodeIndex) {
	object := r.tree.Child(n, 0)
	if r.tree.Nodes[object].Kind != enums.NodeKinds.Ident {
		r.errorAt(n, "invalid type expression")
		return
	}
	objName := r.tree.Text(object)
	objSym, ok := scope.Lookup(objName)
	if !ok {
		r.errorAtLabel(object, "not found", "undefined: %s", objName)
		return
	}
	r.info.Refs[object] = objSym
	if objSym.Kind != SymPackage {
		r.errorAt(n, "%s is not a package", objName)
		return
	}

	name := r.tree.Text(n)
	sym, ok := objSym.Package.Scope.LookupLocal(name)
	if !ok {
		r.errorAtLabel(n, "not found", "undefined: %s.%s", objName, name)
		return
	}
	if !sym.Kind.IsType() {
		r.errorAt(n, "%s.%s is not a type (declared as %s)", objName, name, sym.Kind)
		return
	}
	if !sym.Exported {
		r.errorAtLabel(n, "unexported symbol", "%s.%s is not exported", objName, name)
		return
	}
	r.info.Refs[n] = sym
}

// resolveExpr resolves n as a value expression. See Info's doc comment for
// what's deliberately NOT resolved here - struct field/method names
// (MemberExpr's Tok, a CompositeLit's keyed element names) both need type
// information this pass doesn't have.
func (r *resolver) resolveExpr(scope *Scope, n ast.NodeIndex) {
	if n == ast.InvalidNode {
		return
	}
	switch r.tree.Nodes[n].Kind {
	case enums.NodeKinds.Ident:
		name := r.tree.Text(n)
		sym, ok := scope.Lookup(name)
		if !ok {
			r.errorAtLabel(n, "not found", "undefined: %s", name)
			return
		}
		r.info.Refs[n] = sym
	case enums.NodeKinds.ThisExpr:
		fnScope := nearestFunc(scope)
		if fnScope == nil || fnScope.Receiver == nil {
			r.errorAt(n, "this is only valid inside a method")
			return
		}
		r.info.Refs[n] = fnScope.Receiver
	case enums.NodeKinds.BinaryExpr:
		r.resolveExpr(scope, r.tree.Child(n, 0))
		r.resolveExpr(scope, r.tree.Child(n, 1))
	case enums.NodeKinds.UnaryExpr, enums.NodeKinds.ParenExpr:
		r.resolveExpr(scope, r.tree.Child(n, 0))
	case enums.NodeKinds.CallExpr:
		for _, c := range r.tree.Children(n) {
			r.resolveExpr(scope, c)
		}
	case enums.NodeKinds.IndexExpr:
		r.resolveExpr(scope, r.tree.Child(n, 0))
		r.resolveExpr(scope, r.tree.Child(n, 1))
	case enums.NodeKinds.MemberExpr:
		// The object always resolves lexically first. A package-qualified
		// access (`mathutils.Add` - the object is a bare Ident that
		// resolves to an import binding) is fully resolved right here, same
		// as resolveTypeMemberExpr's type-position counterpart - it needs no
		// type information, unlike a struct field/method access, which is
		// deliberately left for type-checking (needs the object's type).
		object := r.tree.Child(n, 0)
		r.resolveExpr(scope, object)
		if r.tree.Nodes[object].Kind == enums.NodeKinds.Ident {
			if objSym, ok := r.info.Refs[object]; ok && objSym.Kind == SymPackage {
				r.resolvePackageMemberExpr(n, objSym)
			}
		}
	case enums.NodeKinds.ArrayType:
		// Reachable only via a parse-error recovery path (a bare array
		// type used where an expression was expected, already flagged by
		// the parser) - forwarded here rather than silently ignored.
		r.resolveType(scope, n)
	case enums.NodeKinds.CompositeLit:
		r.resolveCompositeLit(scope, n)
	}
}

// resolvePackageMemberExpr resolves a package-qualified value reference
// (`mathutils.Add`, `mathutils.SomeExportedVar`) - n is the MemberExpr node
// itself, pkgSym its already-resolved object (a SymPackage symbol). Looked
// up directly in the target package's own top-level scope (LookupLocal, not
// Lookup - see its own doc comment for why a plain outward-walking lookup
// would be wrong here) and, since this is always a genuine cross-package
// access by construction (the only way to reach this at all is through a
// real import binding), always export-checked.
func (r *resolver) resolvePackageMemberExpr(n ast.NodeIndex, pkgSym *Symbol) {
	name := r.tree.Text(n)
	sym, ok := pkgSym.Package.Scope.LookupLocal(name)
	if !ok {
		r.errorAtLabel(n, "not found", "undefined: %s.%s", pkgSym.Name, name)
		return
	}
	if !sym.Exported {
		r.errorAtLabel(n, "unexported symbol", "%s.%s is not exported", pkgSym.Name, name)
		return
	}
	r.info.Refs[n] = sym
}

func (r *resolver) resolveCompositeLit(scope *Scope, n ast.NodeIndex) {
	typeExpr, elems := r.tree.CompositeLitElems(n)
	r.resolveType(scope, typeExpr)
	for _, elem := range elems {
		if r.tree.IsKeyedElement(elem) {
			// The key is a struct field name, resolved once the literal's
			// type is known (type-checking's job); only the value
			// resolves lexically here.
			r.resolveExpr(scope, r.tree.Child(elem, 1))
			continue
		}
		r.resolveExpr(scope, elem)
	}
}
