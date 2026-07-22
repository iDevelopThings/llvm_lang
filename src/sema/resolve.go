// Package sema is the semantic-analysis layer built on top of a parsed
// ast.Tree: name/scope resolution first (this file), type checking next.
package sema

import (
	"llvm_lang/src/ast"
	"llvm_lang/src/diag"
	"llvm_lang/src/enums"
)

// Info is the result of resolving one Tree: every reference's resolved
// Symbol, every scope-introducing node's Scope, each struct's field/
// method catalog, and (once Check has also run - see typecheck.go) every
// expression's inferred Type - all keyed by NodeIndex (or struct name for
// Structs), the same side-table pattern as the rest of the compiler:
// semantic data lives here, not bolted onto ast.Node.
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

type resolver struct {
	tree  *ast.Tree
	diags *diag.Bag
	info  *Info
}

// Resolve walks tree (the output of parser.ParseFile) and resolves every
// name reference it can from lexical scope alone: variables, parameters,
// functions, struct type names, and `this`. Struct field/method access
// (`p.field`, `p.method()`) isn't resolved here - see StructInfo's doc
// comment - because that needs to know p's type, which belongs to the
// type-checking pass this feeds into, not lexical scoping.
func Resolve(tree *ast.Tree) (*Info, *diag.Bag) {
	r := &resolver{
		tree:  tree,
		diags: diag.NewBag(),
		info: &Info{
			Refs:    make(map[ast.NodeIndex]*Symbol),
			Scopes:  make(map[ast.NodeIndex]*Scope),
			Structs: make(map[string]*StructInfo),
		},
	}
	r.resolveFile()
	return r.info, r.diags
}

func (r *resolver) errorAt(n ast.NodeIndex, format string, a ...any) {
	r.diags.Errorf(r.tree.SpanOf(n).Start, format, a...)
}

// resolveFile processes the whole tree in three passes, so declaration
// order within the file never matters (Go allows top-level declarations to
// reference each other regardless of which comes first):
//  1. every struct's type name and field catalog, so methods (pass 2) and
//     type positions (pass 3) can always find them
//  2. every top-level var/free-function name, and every method (attached
//     to the struct catalog pass 1 already built)
//  3. every declaration's own content - types, initializers, function
//     bodies - now that every top-level name is known
func (r *resolver) resolveFile() {
	universe := universeScope()
	pkg := newScope(ScopePackage, universe, r.tree.Root)
	r.info.Scopes[r.tree.Root] = pkg

	decls := r.tree.Children(r.tree.Root)

	for _, decl := range decls {
		if r.tree.Nodes[decl].Kind == enums.NodeKinds.StructDecl {
			r.declareStruct(pkg, decl)
		}
	}
	for _, decl := range decls {
		switch r.tree.Nodes[decl].Kind {
		case enums.NodeKinds.VarDecl:
			r.declareLocal(pkg, decl, r.tree.Child(decl, 0), SymVar)
		case enums.NodeKinds.FuncDecl:
			r.declareFunc(pkg, decl)
		}
	}
	for _, decl := range decls {
		switch r.tree.Nodes[decl].Kind {
		case enums.NodeKinds.VarDecl:
			r.resolveVarDeclBody(pkg, decl)
		case enums.NodeKinds.FuncDecl:
			r.resolveFuncBody(pkg, decl)
		case enums.NodeKinds.StructDecl:
			r.resolveStructFieldTypes(pkg, decl)
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
				Name:  "this",
				Kind:  SymReceiver,
				Decl:  info.Symbol.Decl,
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
