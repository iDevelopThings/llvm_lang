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
// Returns one *Info and one *diag.Bag per input tree, keyed by the tree
// itself - a diagnostic's Pos is only meaningful relative to the one file
// it's reported against (see diag.Bag's own doc comment), so unlike Structs/
// the package Scope, diagnostics are never merged across files.
func ResolvePackage(trees []*ast.Tree) (map[*ast.Tree]*Info, map[*ast.Tree]*diag.Bag) {
	r := &resolver{
		infos:   make(map[*ast.Tree]*Info, len(trees)),
		bags:    make(map[*ast.Tree]*diag.Bag, len(trees)),
		structs: make(map[string]*StructInfo),
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
	return out, diags
}

func (r *resolver) errorAt(n ast.NodeIndex, format string, a ...any) {
	r.bag.Errorf(r.tree.SpanOf(n).Start, format, a...)
}

// resolvePackage processes every file in trees in three passes, so
// declaration order both within a file and across the whole package never
// matters (Go allows top-level declarations to reference each other
// regardless of which comes first, or which file declares them) - the same
// guarantee the single-file resolveFile used to provide one level down, now
// applied across every file before any file's bodies are resolved:
//  1. every file's struct type names and field catalogs, so methods (pass 2)
//     and type positions (pass 3) can always find them regardless of which
//     file declares the struct
//  2. every file's top-level var/free-function names, and every method
//     (attached to the struct catalog pass 1 already built)
//  3. every file's own declaration content - types, initializers, function
//     bodies - now that every top-level name in the whole package is known
func (r *resolver) resolvePackage(trees []*ast.Tree) {
	for _, tree := range trees {
		r.enterFile(tree)
		for _, decl := range tree.Children(tree.Root) {
			if tree.Nodes[decl].Kind == enums.NodeKinds.StructDecl {
				r.declareStruct(r.pkg, decl)
			}
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
		for _, decl := range tree.Children(tree.Root) {
			switch tree.Nodes[decl].Kind {
			case enums.NodeKinds.VarDecl:
				r.resolveVarDeclBody(r.pkg, decl)
			case enums.NodeKinds.FuncDecl:
				r.resolveFuncBody(r.pkg, decl)
			case enums.NodeKinds.StructDecl:
				r.resolveStructFieldTypes(r.pkg, decl)
			}
		}
	}
}

// declareLocal registers a name-bearing node (VarDecl, ShortVarDecl, or
// Param - anything whose first child is its name Ident) into scope,
// reporting a redeclaration and recording the Ident's Ref either way.
func (r *resolver) declareLocal(scope *Scope, n, nameNode ast.NodeIndex, kind SymbolKind) *Symbol {
	sym := &Symbol{
		Name:  r.tree.Text(nameNode),
		Kind:  kind,
		Decl:  n,
		Tree:  r.tree,
		Scope: scope,
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
	r.info.Structs[sym.Name] = info

	for _, field := range r.tree.Children(decl)[1:] {
		fieldNameNode := r.tree.Child(field, 0)
		fieldName := r.tree.Text(fieldNameNode)
		fieldSym := &Symbol{
			Name:  fieldName,
			Kind:  SymField,
			Decl:  field,
			Tree:  r.tree,
			Scope: pkg,
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
	for _, field := range r.tree.Children(decl)[1:] {
		r.resolveType(pkg, r.tree.Child(field, 1))
	}
}

// declareFunc registers a free function into package scope, or - if it has
// a receiver clause - attaches it to that receiver struct's method table
// instead (see StructInfo: methods aren't in package scope at all, since
// they're looked up via the receiver's type, not a plain name lookup).
func (r *resolver) declareFunc(pkg *Scope, decl ast.NodeIndex) {
	receiver := r.tree.Child(decl, 0)
	nameNode := r.tree.Child(decl, 1)

	if receiver == ast.InvalidNode {
		r.declareLocal(pkg, decl, nameNode, SymFunc)
		return
	}

	sym := &Symbol{
		Name: r.tree.Text(nameNode),
		Kind: SymFunc,
		Decl: decl,
		Tree: r.tree,
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
	receiver := r.tree.Child(decl, 0)
	paramList := r.tree.Child(decl, 2)
	returnType := r.tree.Child(decl, 3)
	body := r.tree.Child(decl, 4)

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
			r.errorAt(n, "undefined: %s", name)
			return
		}
		if !sym.Kind.IsType() {
			r.errorAt(n, "%s is not a type (declared as %s)", name, sym.Kind)
		}
		r.info.Refs[n] = sym
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
			r.errorAt(n, "undefined: %s", name)
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
		// Only the object resolves lexically; the field name (Tok) needs
		// the object's type, resolved by type-checking, not here.
		r.resolveExpr(scope, r.tree.Child(n, 0))
	case enums.NodeKinds.ArrayType:
		// Reachable only via a parse-error recovery path (a bare array
		// type used where an expression was expected, already flagged by
		// the parser) - forwarded here rather than silently ignored.
		r.resolveType(scope, n)
	case enums.NodeKinds.CompositeLit:
		r.resolveCompositeLit(scope, n)
	}
}

func (r *resolver) resolveCompositeLit(scope *Scope, n ast.NodeIndex) {
	children := r.tree.Children(n)
	r.resolveType(scope, children[0])
	for _, elem := range children[1:] {
		if r.tree.Nodes[elem].Kind == enums.NodeKinds.KeyValueExpr {
			// The key is a struct field name, resolved once the literal's
			// type is known (type-checking's job); only the value
			// resolves lexically here.
			r.resolveExpr(scope, r.tree.Child(elem, 1))
			continue
		}
		r.resolveExpr(scope, elem)
	}
}
