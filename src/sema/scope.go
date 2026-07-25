package sema

import (
	"unicode"
	"unicode/utf8"

	"llvm_lang/src/ast"
	"llvm_lang/src/enums"
)

// ScopeKind classifies a Scope by what introduces it, mirroring Go's own
// scope hierarchy (universe > package > file > function > block). A
// package's ScopePackage scope is genuinely shared across every file in
// that package (see LANGUAGE.md's "Multi-file packages" section and
// ResolvePackage) - every file's top-level struct/var/func names are
// declared into the exact same Scope instance, which is exactly what makes
// a name declared in one file visible when resolving another's body,
// regardless of file order. ScopeFile now holds every import binding that
// file itself declared (see LANGUAGE.md's "Imports" section and
// resolver.buildFileScope, resolve.go) - file-scoped, not package-scoped:
// a sibling file that doesn't itself write `import "./x"` can't see that
// binding, even within the same package.
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
	Owner  ast.NodeIndex // the FuncDecl this scope belongs to; InvalidNode for Universe/Package (now spans every file - no single owning node)/Block

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

// LookupLocal resolves name directly in s, without walking outward to any
// enclosing scope - used for a package-qualified access (`pkg.Name`, see
// resolve.go's resolvePackageMemberExpr/resolveTypeMemberExpr): a name must
// be genuinely declared in that package's own top-level scope, not merely
// reachable via scopes *it* encloses (every package scope's own parent is
// eventually the universe scope - see ResolvePackage - so a plain Lookup
// would incorrectly let `somepkg.int` resolve, since "int" is reachable by
// walking up from any package's scope at all).
func (s *Scope) LookupLocal(name string) (*Symbol, bool) {
	sym, ok := s.names[name]
	return sym, ok
}

// packageScopeOf returns the nearest ScopePackage ancestor of s - a
// package-level symbol's own Symbol.Scope already IS that package scope
// directly (see declareLocal/declareStruct/declareFunc/addMethod, all of
// which declare into the shared package scope); this walks up through any
// nested function/block scope for the (rarer) case of a symbol declared
// somewhere nested. Used purely to decide "does this symbol belong to the
// same package as the code accessing it" for export enforcement (see
// typecheck.go's checkExportedAccess) - every package gets its own fresh
// Scope instance (one per sema.ResolveProgram unit), so pointer identity
// alone decides this.
func packageScopeOf(s *Scope) *Scope {
	for sc := s; sc != nil; sc = sc.Parent {
		if sc.Kind == ScopePackage {
			return sc
		}
	}
	return nil
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

// nearestReceiverFunc walks up from scope to the nearest enclosing ScopeFunc
// that actually has a receiver (Receiver != nil) - i.e. the nearest enclosing
// *method's* own function scope, or nil if scope isn't inside one. Unlike
// plain nearestFunc, this keeps walking straight past a ScopeFunc whose
// Receiver is nil (a FuncLit's own function scope - see resolveFuncLit,
// resolve.go) instead of stopping at the first ScopeFunc found at all: a
// `this` reference lexically inside a lambda still needs to resolve to the
// nearest *enclosing method's* receiver (see resolveExpr's ThisExpr case),
// not bail out just because the lambda's own scope happens to have no
// receiver of its own. Walking past a nil-receiver ScopeFunc belonging to a
// free (non-method) function is harmless too: a free function is never
// itself nested inside a method, so the walk simply continues outward and
// still correctly finds nothing, same as today.
func nearestReceiverFunc(scope *Scope) *Scope {
	for s := scope; s != nil; s = s.Parent {
		if s.Kind == ScopeFunc && s.Receiver != nil {
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
	// SymPackage is an import binding (`import "./mathutils"` - see
	// LANGUAGE.md's "Imports" section): a name that resolves to another
	// package's exported surface (Symbol.Package), not a value or a type of
	// its own. File-scoped, not package-scoped - see ScopeFile.
	SymPackage
	// SymConstructor names one constructor nested inside a struct
	// declaration (see LANGUAGE.md's "Constructors" section) - never itself
	// bound into any lexical Scope (a constructor has no name to look up;
	// it's selected by argument count - see StructInfo.Constructors), so
	// this Symbol only ever exists as the value a constructor-call callee's
	// Info.Refs entry is overwritten to point at once sema.Check has
	// resolved which constructor a call selected (see checkConstructorCall,
	// typecheck.go) - the same "record which specific declaration a call
	// resolved to" idea an ordinary method call's Info.Refs entry already
	// captures.
	SymConstructor
	// SymDestructor names a struct's own `destructor() {...}` block (see
	// LANGUAGE.md's "Destructors" section) - the destructor-kind counterpart
	// to SymConstructor, and, like it, never itself bound into any lexical
	// Scope: a destructor has no name and no call syntax of its own at all
	// (unlike a constructor, which is at least reachable via `Name(args)`) -
	// it's invoked purely implicitly, by codegen, at a local's scope exit or
	// by `delete` against a pointer to it. Recorded once per struct on
	// StructInfo.Destructor.
	SymDestructor
	// SymBuiltinValue names a predeclared value with no declaration of its
	// own - currently only `nil` (see universeScope and LANGUAGE.md's
	// "Pointers" section) - a value, deliberately distinct from
	// SymBuiltinType (a type name) and SymFunc (print/make/append/len are
	// callable; nil never is): it needs neither IsType() nor any call-site
	// signature handling, just a Type of its own (typeOfSymbolValue,
	// typecheck.go).
	SymBuiltinValue
	// SymEnum names a top-level `enum Name { ... }` declaration (see
	// LANGUAGE.md's "Enums" section) - the enum-kind counterpart to
	// SymStruct, IsType() included: a bare enum name is legal in type
	// position exactly like a struct's.
	SymEnum
	// SymEnumVariant names one specific variant of some enum
	// (`Shape.Circle`) - never bound into any lexical Scope by its own bare
	// name (only reachable through EnumName.Variant, exactly like
	// SymConstructor is only ever reachable through Name(args)): once
	// Resolve resolves a MemberExpr/CallExpr-callee/CompositeLit-type-expr
	// naming EnumName.Variant, Info.Refs for that node is set to point at
	// this Symbol, the same "record which specific declaration a reference
	// resolved to" idea SymConstructor's own Info.Refs entry already
	// captures - see resolve.go's resolveEnumVariantRef.
	SymEnumVariant
	// SymTypeParam names one type parameter bound to a concrete type inside a
	// single instantiation of a generic declaration (see LANGUAGE.md's
	// "Generics" section) - never declared into any scope the user writes,
	// only into the synthetic scope generics.go builds per instantiation.
	// IsType() included: `T` is legal anywhere a type name is.
	SymTypeParam
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
	case SymPackage:
		return "package"
	case SymConstructor:
		return "constructor"
	case SymDestructor:
		return "destructor"
	case SymBuiltinValue:
		return "builtin value"
	case SymEnum:
		return "enum"
	case SymEnumVariant:
		return "enum variant"
	case SymTypeParam:
		return "type parameter"
	default:
		return "symbol"
	}
}

// IsType reports whether a symbol of this kind can be used in a type
// position (`var a Kind`, an array element type, a method receiver clause).
func (k SymbolKind) IsType() bool {
	return k == SymStruct || k == SymBuiltinType || k == SymEnum || k == SymTypeParam
}

// Symbol is one declared (or compiler-predeclared) name.
type Symbol struct {
	Name string
	Kind SymbolKind

	// Decl is the declaring node (VarDecl/Param/FuncDecl/StructDecl/Field),
	// or InvalidNode for a predeclared symbol (print, int, ...) that has no
	// declaration site in the source at all.
	Decl ast.NodeIndex

	// Tree is the *ast.Tree Decl is an index into - nil for a predeclared
	// symbol (Decl == InvalidNode), which has no owning file at all. This
	// exists specifically for multi-file packages (see LANGUAGE.md's
	// "Multi-file packages" section): ast.NodeIndex is only ever meaningful
	// relative to the one Tree it came from (see ast.NodeIndex's own doc
	// comment), so the moment a Symbol can be visible from a file other than
	// the one that declares it, Decl alone is no longer enough to find the
	// declaring node again - Tree says which of the package's files to index
	// into. Every consumer that dereferences a foreign Symbol's Decl (a
	// call/reference resolved in one file but declared in another) reads
	// this field rather than assuming "whichever tree is currently being
	// walked" - see sema/typecheck.go's checker.pushTree. codegen never
	// actually needs to do this itself (see the Generator doc comment,
	// src/codegen/codegen.go, for why every codegen-level lookup is already
	// keyed by *Symbol/*StructInfo pointer identity instead), but the field
	// still exists on Symbol - not just inside sema's own checker state -
	// since Resolve, not Check, is what constructs every Symbol in the
	// first place.
	Tree *ast.Tree

	// Scope is the scope this symbol was declared into - for a method,
	// its receiver struct's own scope (package scope today).
	Scope *Scope

	// Exported reports whether this symbol is visible from another package
	// (see LANGUAGE.md's "Imports" section): Go-style name-case - a
	// capitalized first letter. Computed once at declaration time
	// (isExportedName) for every top-level func/struct/field/method symbol;
	// meaningless (left false) for a local var/param, which can never be
	// referenced cross-package at all. Enforced by typecheck.go's
	// checkExportedAccess/resolve.go's resolvePackageMemberExpr /
	// resolveTypeMemberExpr - never by anything in codegen, which assumes an
	// already-fully-checked tree (see AGENTS.md's codegen section).
	Exported bool

	// StructInfo is set for a SymStruct symbol - a direct back-pointer to the
	// same StructInfo that already points forward to this Symbol
	// (StructInfo.Symbol), so any consumer holding a struct-type *Symbol*
	// (e.g. one resolved through a package qualifier - see
	// resolveTypeMemberExpr) can reach its Fields/Methods/Constructors
	// catalog directly, without a name-based lookup into some package's own
	// (possibly not currently-in-scope, or - for a constructor call resolved
	// cross-package - not even the *current* package's) Structs map. Also
	// set for a SymConstructor symbol, pointing at the struct it constructs -
	// the same direct-pointer reasoning applies there even more: codegen
	// needs the constructed struct's own layout (g.structLayouts) from a
	// constructor-call callee's resolved Symbol, and by construction that
	// callee may belong to any package in the program (see LANGUAGE.md's
	// "Constructors" section: a struct's constructors are usable
	// cross-package iff the struct itself is exported) - a name-based lookup
	// through whichever tree's own Info.Structs happens to be active would
	// be wrong the moment the constructor was declared in a different
	// package's file than the call site.
	StructInfo *StructInfo

	// Captured reports whether some function-literal expression (a FuncLit -
	// see LANGUAGE.md's "Lambdas" section) anywhere in the program captures
	// this symbol by reference from an enclosing function scope - only ever
	// set true for a SymVar/SymParam symbol (see capture.go's
	// analyzeFuncLitCaptures; a lambda referencing an enclosing method's
	// `this` is rejected outright rather than supported, so SymReceiver never
	// sets this). Computed once, by Resolve's own tail pass
	// (computeCaptures), after every file's ordinary lexical resolution has
	// already run - a symbol can only be known to be captured once every
	// FuncLit anywhere that might reference it has actually been visited.
	// codegen reads this to decide a local's storage: captured means arena-
	// allocated (so its address can safely outlive its declaring function's
	// own stack frame - see CODEGEN.md), not the ordinary entry-block alloca
	// every non-captured local still gets.
	Captured bool

	// EnumInfo is set for a SymEnum symbol (the enum type's own catalog,
	// mirroring StructInfo's identical role for SymStruct) and for a
	// SymEnumVariant symbol (the enum that variant belongs to) - the same
	// direct-back-pointer reasoning StructInfo already documents: a consumer
	// holding a resolved variant Symbol (e.g. a match arm's own pattern, or a
	// package-qualified enum reference) can reach the whole Variants/Methods/
	// Destructor catalog directly, with no name-based lookup into some
	// package's own Info.Enums needed.
	EnumInfo *EnumInfo

	// Generic is set for a symbol naming a generic declaration's *template* -
	// a `func Foo[T]`, a `struct Foo[T]`, or a method carrying type parameters
	// of its own (see LANGUAGE.md's "Generics" section). A template is never
	// itself resolved, type-checked, or lowered; every use of it goes through
	// one monomorphized specialization per distinct type-argument list (see
	// generics.go). nil for every ordinary symbol.
	Generic *GenericInfo

	// GenericTemplate is set only on a specialization's own Symbol (built by
	// instantiateFunc/instantiateStruct, generics.go) - the template's own
	// declaring Symbol (the one Generic above is set on) it was instantiated
	// from. Lets a consumer that only has one call site's specialized
	// Symbol in hand (e.g. Workspace.References) still find its way back to
	// the template, and from there to every other instantiation
	// (GenericInfo.Instances), without a separate name-based lookup. nil
	// for every ordinary symbol, including the template itself.
	GenericTemplate *Symbol

	// TypeParamBound is set only for a SymTypeParam symbol - the concrete type
	// that type parameter is bound to inside one specialization. There is no
	// unbound form: a type parameter only ever becomes a real Symbol at
	// instantiation time, in that instantiation's own scope.
	TypeParamBound *Type

	// Variant is set only for a SymEnumVariant symbol - which specific
	// variant (name, discriminant index, kind, associated-data shape) this
	// Symbol names, within EnumInfo above.
	Variant *EnumVariant

	// Package is set only for a SymPackage symbol (an import binding) - the
	// imported package's own resolved surface (its shared top-level Scope
	// and struct catalog), already built by the time this binding is
	// created (see ResolveProgram: packages are processed in dependency
	// order, so an importer's own imports are always already-resolved
	// PackageResults by the time its file scope is built).
	Package *PackageResult
}

// DeclaringNameNode returns the specific node within s.Decl that s's own
// declaring Info.Refs entry is actually keyed by - distinct from Decl itself
// whenever Decl is a *container* node (VarDecl, FuncDecl, StructDecl, ...)
// rather than the name occurrence directly, which is the common case: Refs
// is keyed by nameNode, a child of the n that's also passed as Decl (see
// resolve.go's declareLocal) - so a caller trying to recognize "is this
// particular Refs entry the declaring occurrence, or a later reference"
// (e.g. an LSP find-references implementation excluding the declaration, or
// a future rename refactor) needs this, not s.Decl directly, or it will
// simply never match anything.
//
// A handful of SymbolKinds are their own exception, each documented at its
// own declaring call site in resolve.go - and one non-exception frequently
// mistaken for one: a MultiShortVarDecl-destructured name, where Decl and
// the Refs key are deliberately the very same Ident node already (see
// declareLocal's own doc comment), caught here by the same "Decl's own Kind
// is already Ident" check as any of the real exceptions below, without
// needing its own separate case.
//
//   - SymEnumVariant, SymPackage, SymConstructor, SymDestructor: each IS its
//     own declaring Info.Refs entry directly - an EnumVariant's own Tok is
//     its name; an ImportDecl has no separate name node; a constructor/
//     destructor has no name at all to have one (see declareEnum/
//     buildFileScope/declareConstructor/declareDestructor).
//   - SymFunc (a FuncDecl) / an ExternFuncDecl: the name isn't the first
//     child (FuncDecl's own children lead with an optional receiver clause)
//   - use FuncName/ExternFuncName instead of assuming Child(0).
//   - everything else declareLocal ever calls Decl for (VarDecl,
//     ShortVarDecl, Param, StructDecl, EnumDecl, Field): the name is always
//     the first child - see ast.Node's own doc comment.
//
// Returns ast.InvalidNode for a predeclared symbol (Decl == ast.InvalidNode,
// no declaration site to point at at all) or a SymReceiver ("this" has no
// real per-occurrence declaring node of its own - see resolveFuncBody's own
// Receiver construction, which reuses the receiver struct/enum's own Decl,
// not something specific to "this").
func (s *Symbol) DeclaringNameNode(tree *ast.Tree) ast.NodeIndex {
	if s.Decl == ast.InvalidNode || s.Kind == SymReceiver {
		return ast.InvalidNode
	}
	switch tree.Nodes[s.Decl].Kind {
	case enums.NodeKinds.Ident,
		enums.NodeKinds.EnumVariant,
		enums.NodeKinds.ImportDecl,
		enums.NodeKinds.ConstructorDecl,
		enums.NodeKinds.DestructorDecl:
		return s.Decl
	case enums.NodeKinds.FuncDecl:
		return tree.FuncName(s.Decl)
	case enums.NodeKinds.ExternFuncDecl:
		return tree.ExternFuncName(s.Decl)
	default:
		return tree.Child(s.Decl, 0)
	}
}

// isExportedName reports whether name is exported by this language's
// Go-style name-case convention: a capitalized first rune. Used the moment
// every top-level func/struct/field/method Symbol is constructed, so
// Exported never needs recomputing later (see checkExportedAccess/
// resolvePackageMemberExpr/resolveTypeMemberExpr, the only readers).
func isExportedName(name string) bool {
	r, _ := utf8.DecodeRuneInString(name)
	return r != utf8.RuneError && unicode.IsUpper(r)
}

// StructInfo catalogs one struct's fields, methods, and constructors, built
// directly from its declaration - no type inference needed for any of the
// three, since all are just child nodes of the StructDecl/FuncDecl/
// ConstructorDecl themselves. Field/method *use* sites (`p.field`,
// `p.method()`) aren't resolved against this catalog by this package - that
// needs to know p's type, which is what the type-checking pass this feeds
// into is for.
type StructInfo struct {
	Symbol  *Symbol
	Fields  map[string]*Symbol
	Methods map[string]*Symbol

	// Generic/TypeArgs are set only for a struct produced by instantiating a
	// generic one (see LANGUAGE.md's "Generics" section): the template it came
	// from, and the concrete type arguments it was instantiated with, in
	// declaration order. Both nil for an ordinary struct. Read by type-argument
	// inference, which unifies a declared `SlotMap[T]` parameter shape against
	// an actual argument's own already-instantiated struct type.
	Generic  *GenericInfo
	TypeArgs []Type

	// Constructors catalogs each declared `constructor(params) {...}` block
	// (see LANGUAGE.md's "Constructors" section), keyed by its declared
	// parameter count - constructors are overloaded by argument count only,
	// deliberately scoped to this one construct (not a general overloading
	// mechanism - see LANGUAGE.md), so a single map keyed by arity is enough
	// to both catalog every constructor and answer "which one does a call
	// with N arguments mean" in one lookup (checkConstructorCall,
	// typecheck.go). Built by declareConstructor at struct-declaration time,
	// which also rejects two constructors sharing the same arity right
	// there, as a real diagnostic - a structural problem regardless of
	// whether either is ever called. May be empty (most structs have no
	// constructors at all).
	Constructors map[int]*Symbol

	// Destructor is this struct's own `destructor() {...}` block (see
	// LANGUAGE.md's "Destructors" section), or nil if it declares none - at
	// most one is ever legal; a second is rejected right at struct-
	// declaration time (declareDestructor, resolve.go), the same "a
	// structural problem regardless of whether it's ever used" reasoning
	// Constructors' own arity-collision check already uses.
	Destructor *Symbol

	// Copyable reports whether a value of this struct type may be freely
	// duplicated - false iff this struct declares its own Destructor, or
	// (transitively) embeds any field whose own type is itself non-copyable,
	// directly or through a fixed-size array (see LANGUAGE.md's "Destructors"
	// section for the full rule, and why no move semantics or last-use
	// analysis is needed to keep this sound: a non-copyable value can only
	// ever have one live instance, so "when does it destruct" is never
	// ambiguous). Computed lazily and memoized on first use, since one
	// struct's copyability can depend on another struct declared later in
	// the same file, in a different file, or (a struct's constructors are
	// usable cross-package the moment the struct itself is exported - same
	// rule this feature follows) a different package entirely - see
	// checker.structCopyable, typecheck.go.
	Copyable bool

	// copyableComputed reports whether Copyable already holds a real,
	// computed answer - distinct from Copyable's own zero value (false)
	// specifically so "not computed yet" and "computed, and it's false" are
	// never confused (see checker.structCopyable).
	copyableComputed bool
}

// EnumVariantKind classifies one EnumVariant - unit (no associated data),
// tuple (positional associated data), or struct (named associated data) -
// see LANGUAGE.md's "Enums" section. A small hand-rolled enum, not
// enum_codegen: it needs zero supporting code beyond the three bare
// constants (no String()/Parse()/iteration helper any caller actually
// needs - every diagnostic that names a variant's kind already has its own
// wording to pick), the exact bar AGENTS.md's enum_codegen criterion sets for
// when a plain const block remains the right call.
type EnumVariantKind int

const (
	EnumVariantUnit EnumVariantKind = iota
	EnumVariantTuple
	EnumVariantStruct
)

// EnumField is one named associated-data field of a struct-style variant
// (`Triangle { base f64, height f64 }`'s own `base`/`height`) - the
// enum-variant counterpart to a struct's own field, in declaration order.
type EnumField struct {
	Name string
	Type Type
	// Sym is this field's own Symbol (SymField, mirroring an ordinary
	// struct field's) - set so a match arm's keyed pattern element
	// (`{base: b}`) can resolve `base` into Info.Refs exactly like a real
	// struct composite literal's own keyed element already does
	// (checkKeyedStructElem, typecheck.go).
	Sym *Symbol
}

// EnumVariant is one variant of an EnumInfo - its own name, discriminant
// index (declaration order, 0-based - the same "declaration order is the
// natural index" convention this project's struct field layout already
// uses), kind, and associated-data shape. Tuple/Fields are populated by
// Check (checkEnumDecl, typecheck.go), not Resolve - mirroring how a struct
// field's own Type is likewise computed lazily via typeFromNode, never by
// Resolve itself (see StructInfo's own doc comment).
type EnumVariant struct {
	Name  string
	Index int
	Kind  EnumVariantKind

	// Decl is this variant's own EnumVariant ast.NodeIndex.
	Decl ast.NodeIndex

	// Tuple holds each associated type, positional, when Kind ==
	// EnumVariantTuple - nil otherwise.
	Tuple []Type

	// Fields holds each associated field, in declaration order, when Kind ==
	// EnumVariantStruct - nil otherwise.
	Fields []EnumField

	// Sym is this variant's own Symbol (SymEnumVariant) - see
	// SymEnumVariant's own doc comment for why a variant is never bound into
	// any lexical Scope by its bare name.
	Sym *Symbol

	// Enum is the EnumInfo this variant belongs to.
	Enum *EnumInfo
}

// FieldByName returns the associated field named name (only meaningful when
// Kind == EnumVariantStruct), and whether one was found at all - a small
// linear scan rather than a parallel name->index map, since a real variant's
// field count is always small (a handful of named fields at most).
func (v *EnumVariant) FieldByName(name string) (EnumField, bool) {
	for _, f := range v.Fields {
		if f.Name == name {
			return f, true
		}
	}
	return EnumField{}, false
}

// EnumInfo catalogs one enum type's variants, methods, and destructor, built
// directly from its declaration - the enum-kind counterpart to StructInfo
// (see LANGUAGE.md's "Enums" section). No Constructors field - deliberately
// not applicable here: variant construction (a bare unit-variant reference,
// a tuple-variant call, or a struct-variant composite literal) already fully
// serves the role a struct constructor exists for, so there's no separate
// constructor concept to catalog.
type EnumInfo struct {
	Symbol *Symbol

	// Variants is keyed by variant name, for a name-based lookup (a pattern
	// or a construction reference resolving `EnumName.Variant`).
	Variants map[string]*EnumVariant

	// Order holds every variant in declaration order - the same order
	// Index above assigns - needed wherever declaration order itself matters
	// (exhaustiveness diagnostics listing every uncovered variant, codegen's
	// own discriminant-switch construction), not just name-based lookup.
	Order []*EnumVariant

	Methods    map[string]*Symbol
	Destructor *Symbol

	// Copyable/copyableComputed mirror StructInfo's own identical fields
	// exactly - see LANGUAGE.md's "Enums" section (non-copyable propagation):
	// false iff this enum declares its own Destructor, or any variant's any
	// associated-data type is itself non-copyable, transitively - computed
	// lazily and memoized on first use, same reasoning as structCopyable
	// (an enum's own variant types may name a struct declared later in the
	// package, or in a different file/package entirely).
	Copyable         bool
	copyableComputed bool
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
		"u8",
		"u16",
		"u32",
		"u64",
		"f32",
		"f64",
		"string",
		"cstring",
		"bool",
		"coroutine",
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
	// make/append/len/args are predeclared exactly like print (see
	// LANGUAGE.md's "Dynamic arrays" and "The args() builtin" sections): each
	// is a SymFunc with no real Decl, so it can't go through the normal
	// FuncDecl-based signature machinery every user function does -
	// checkCallExpr/genCallExpr recognize each by name (isBuiltinCall in
	// typecheck.go/runtime.go) and check/lower it with its own bespoke logic
	// instead of an ordinary call's argument-count/type matching against a
	// declared signature.
	// remove is predeclared exactly like make/append/len/args - see
	// LANGUAGE.md's "Maps" section: a deliberately new, distinctly-named
	// builtin for map key removal (`remove(m, k)`), not an extension of the
	// existing `delete p` statement (a wholly unrelated real pointer/heap
	// deallocation operation).
	// resume/done are predeclared exactly like make/append/len/args/remove -
	// see LANGUAGE.md's "Coroutines" section: `resume(h) bool`/`done(h) bool`
	// drive/query a coroutine handle by hand, the same free-function-not-
	// dot-method shape every other builtin here already uses.
	for _, name := range []string{"make", "append", "len", "args", "remove", "resume", "done"} {
		u.Define(&Symbol{
			Name:  name,
			Kind:  SymFunc,
			Scope: u,
		})
	}
	// nil is a predeclared value (see LANGUAGE.md's "Pointers" section) -
	// deliberately scoped to pointer types only this round, not a general
	// zero-value concept: it starts life as the untyped TypeUntypedNil (same
	// deferred-resolution precedent as an untyped numeric literal - see
	// sema/types.go's own doc comment) and only adapts to a concrete *T the
	// moment it's assigned/compared against one (checkAssignable/
	// checkEqualityOperands, typecheck.go).
	u.Define(&Symbol{
		Name:  "nil",
		Kind:  SymBuiltinValue,
		Scope: u,
	})
	return u
}
