package sema

import (
	"llvm_lang/src/ast"
)

// ScopeKind classifies a Scope by what introduces it, mirroring Go's own
// scope hierarchy (universe > package > file > function > block). Universe
// and Package are the only ones that matter for cross-file concerns today,
// but File (import bindings, once import syntax exists) and Package
// aggregating declarations across multiple files are both meant to be
// additive later - adding files to loop over, not restructuring how
// scoping itself works.
type ScopeKind int

const (
	ScopeUniverse ScopeKind = iota
	ScopePackage
	ScopeFile
	ScopeFunc
	ScopeBlock
)

// Scope is a lexical scope: a name table plus a parent link. The parent
// chain is kept around after resolution, not discarded once a lookup
// finishes - a later capture-analysis pass (for closures) needs to walk
// from a reference's declaring Scope up through enclosing function scopes
// and ask "does this cross a function boundary," which only works if the
// scope structure itself still exists to walk.
type Scope struct {
	Kind   ScopeKind
	Parent *Scope
	Owner  ast.NodeIndex // the File/FuncDecl this scope belongs to; InvalidNode for Universe/Block

	// Receiver is set only on a ScopeFunc belonging to a method - the
	// symbol `this` resolves to inside that function's body. nil for a
	// free function or any non-func scope.
	Receiver *Symbol

	names map[string]*Symbol
}

func newScope(kind ScopeKind, parent *Scope, owner ast.NodeIndex) *Scope {
	return &Scope{
		Kind:   kind,
		Parent: parent,
		Owner:  owner,
		names:  make(map[string]*Symbol),
	}
}

// Define adds sym to the scope under sym.Name, reporting whether a symbol
// with that name was already declared directly in this scope. Shadowing a
// symbol from an *enclosing* scope is fine and isn't reported here - only
// two declarations of the same name in the same scope are a conflict.
func (s *Scope) Define(sym *Symbol) (prior *Symbol, redeclared bool) {
	if existing, ok := s.names[sym.Name]; ok {
		return existing, true
	}
	s.names[sym.Name] = sym
	return nil, false
}

// Lookup resolves name by walking outward from s through enclosing scopes.
func (s *Scope) Lookup(name string) (*Symbol, bool) {
	for sc := s; sc != nil; sc = sc.Parent {
		if sym, ok := sc.names[name]; ok {
			return sym, true
		}
	}
	return nil, false
}

// nearestFunc walks up from scope to the nearest enclosing ScopeFunc, or
// nil if scope isn't inside any function.
func nearestFunc(scope *Scope) *Scope {
	for s := scope; s != nil; s = s.Parent {
		if s.Kind == ScopeFunc {
			return s
		}
	}
	return nil
}

// SymbolKind classifies what a Symbol names.
type SymbolKind int

const (
	SymVar SymbolKind = iota
	SymParam
	SymFunc
	SymStruct
	SymField
	SymBuiltinType
	SymReceiver
)

func (k SymbolKind) String() string {
	switch k {
	case SymVar:
		return "var"
	case SymParam:
		return "param"
	case SymFunc:
		return "func"
	case SymStruct:
		return "struct"
	case SymField:
		return "field"
	case SymBuiltinType:
		return "builtin type"
	case SymReceiver:
		return "receiver"
	default:
		return "symbol"
	}
}

// IsType reports whether a symbol of this kind can be used in a type
// position (`var a Kind`, an array element type, a method receiver clause).
func (k SymbolKind) IsType() bool {
	return k == SymStruct || k == SymBuiltinType
}

// Symbol is one declared (or compiler-predeclared) name.
type Symbol struct {
	Name string
	Kind SymbolKind

	// Decl is the declaring node (VarDecl/Param/FuncDecl/StructDecl/Field),
	// or InvalidNode for a predeclared symbol (print, int, ...) that has no
	// declaration site in the source at all.
	Decl ast.NodeIndex

	// Scope is the scope this symbol was declared into - for a method,
	// its receiver struct's own scope (package scope today).
	Scope *Scope

	// Exported is a hook for a future visibility rule, not enforced by
	// anything yet - there's no cross-package access to check without
	// modules/imports existing. Whether the policy ends up being Go-style
	// name-case or an explicit keyword isn't decided; this field exists so
	// that decision doesn't require touching every Symbol construction site.
	Exported bool
}

// StructInfo catalogs one struct's fields and methods by name, built
// directly from its declaration - no type inference needed for either,
// since both are just child nodes of the StructDecl/FuncDecl themselves.
// Field/method *use* sites (`p.field`, `p.method()`) aren't resolved
// against this catalog by this package - that needs to know p's type,
// which is what the type-checking pass this feeds into is for.
type StructInfo struct {
	Symbol  *Symbol
	Fields  map[string]*Symbol
	Methods map[string]*Symbol
}

// universeScope holds the language's predeclared names - built-in
// primitive types and builtin functions - as the outermost scope, above
// package scope, exactly mirroring where Go puts `int`/`len`/`true`/etc.
// `print` in particular has no declaration anywhere in AGENTS.md's
// examples, so it must be predeclared rather than user-defined.
func universeScope() *Scope {
	u := newScope(ScopeUniverse, nil, ast.InvalidNode)
	for _, name := range []string{
		"int",
		"i8",
		"i16",
		"i32",
		"i64",
		"f32",
		"f64",
		"string",
		"bool",
	} {
		u.Define(&Symbol{
			Name:  name,
			Kind:  SymBuiltinType,
			Scope: u,
		})
	}
	u.Define(&Symbol{
		Name:  "print",
		Kind:  SymFunc,
		Scope: u,
	})
	return u
}
