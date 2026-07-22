package ast

import (
	"llvm_lang/src/enums"
	"llvm_lang/src/lexer"
)

// NodeIndex refers to a Node within a Tree's flat Nodes array - never a
// pointer. Index 0 is reserved (see InvalidNode); real nodes start at 1.
type NodeIndex int32

// InvalidNode is the zero value of NodeIndex: "no node". Tree reserves slot
// 0 as an unused placeholder specifically so this holds - a node's
// zero-valued Parent field then means "no parent" (true for the root)
// without needing a separate sentinel check.
const InvalidNode NodeIndex = 0

// Span is a node's full source range - from the start of its first child to
// the end of its last, or just its own token for a leaf - distinct from
// Node.Tok's span, which is only the node's "main" token (see Node.Tok).
type Span struct {
	Start lexer.Pos
	End   lexer.Pos
}

// Node is one entry in a Tree's flat array. It carries no pointers: Parent
// and Children are indices into arrays the owning Tree holds, so a Node is a
// small, fixed-size, cheaply-copied value - deliberately uniform across
// every NodeKind rather than a distinct Go type per kind.
//
// Tok is the node's single "main" token, reused for whatever scalar payload
// that NodeKind needs instead of a dedicated field per kind:
//   - Ident, NumberLit, StringLit, BoolLit, ThisExpr: the literal/identifier/
//     keyword token itself (name or text via Tree.Text)
//   - ImportDecl: the string-literal token holding the raw import path (e.g.
//     `"./mathutils"` - decode it via File.StringValue, exactly like a
//     StringLit expression; there's no aliasing syntax yet - see
//     LANGUAGE.md's "Imports" section - so there's no separate name node)
//   - BinaryExpr, UnaryExpr: the operator token (Tok.Lexeme says which)
//   - MemberExpr: the field-name identifier token (`a.b` - Tok is `b`)
//   - AssignStmt, IncDecStmt: the assignment/inc-dec operator token
//     (=, +=, -=, *=, /=, ++, --)
//   - ShortVarDecl: the `:=` token
//   - VarDecl, FuncDecl, StructDecl, IfStmt, ForStmt, ReturnStmt, BreakStmt,
//     ContinueStmt, FuncType, ConstructorDecl, DestructorDecl, FuncLit,
//     NewExpr, DeleteStmt: the leading keyword token (FuncType's is the `func`
//     that introduces a function-type expression, e.g. `func(int) int`, see
//     LANGUAGE.md's Types section; FuncLit's is the `func` that introduces a
//     function-literal expression, e.g. `func(x int) int { return x }`, see
//     LANGUAGE.md's "Lambdas" section - the same keyword, disambiguated from
//     FuncType purely by whether a `{` body follows the parameter list/return
//     type, exactly the same way FuncDecl's own body already disambiguates it
//     from a bare declaration; NewExpr's is the `new` keyword, DeleteStmt's
//     the `delete` keyword - see LANGUAGE.md's "Pointers" section;
//     DestructorDecl's is the `destructor` keyword - see LANGUAGE.md's
//     "Destructors" section)
//   - `&`/`*` address-of/dereference are ordinary UnaryExpr nodes - Tok is
//     the operator token exactly like unary `-`/`!`, distinguished purely by
//     Tok.Lexeme, no new node kind needed for either (see LANGUAGE.md's
//     "Pointers" section)
//   - everything else (File, Block, ParamList, Param, Field, CallExpr,
//     ParenExpr, IndexExpr, ArrayType, PointerType, CompositeLit,
//     KeyValueExpr, ExprStmt, ParamTypeList): unused, left as the zero Token
//
// Children shapes, by kind:
//   - CallExpr: [callee, arg0, arg1, ...] - variable arity, callee always first
//   - IndexExpr: [target, index] - fixed arity
//   - MemberExpr: [object] - fixed arity; the field name lives in Tok, not a child
//   - ParenExpr: [inner] - fixed arity
//   - UnaryExpr: [operand] - fixed arity
//   - BinaryExpr: [left, right] - fixed arity
//   - ArrayType: [size, elem] - fixed arity; size is InvalidNode for a
//     dynamic/slice type (`[]T`) rather than a fixed-size one (`[N]T`)
//   - CompositeLit: [typeExpr, elem0, elem1, ...] - variable arity, typeExpr
//     (an Ident or ArrayType) always first; each elem is either a bare
//     expression (positional) or a KeyValueExpr (keyed)
//   - KeyValueExpr: [key, value] - fixed arity
//   - ThisExpr: no children (leaf)
//   - ImportDecl: no children (leaf) - see Tok above
//   - VarDecl: [name, type, init] - fixed arity; type and/or init may be
//     InvalidNode (at least one must be present, but that's a sema concern,
//     not a parse error)
//   - ShortVarDecl: [name, init] - fixed arity
//   - AssignStmt: [target, value] - fixed arity; target is an lvalue
//     (Ident, MemberExpr, or IndexExpr)
//   - IncDecStmt: [target] - fixed arity
//   - ExprStmt, ReturnStmt: [expr] - fixed arity; ReturnStmt's expr may be
//     InvalidNode (bare `return`)
//   - BreakStmt, ContinueStmt: no children (leaves)
//   - Block: [stmt0, stmt1, ...] - variable arity
//   - IfStmt: [cond, then, else] - fixed arity; else may be InvalidNode.
//     then/else are whatever parseStmt/parseBlock produced - a Block for the
//     brace form, any single statement for the one-line `if cond: stmt` form
//   - ForStmt: [init, cond, post, body] - fixed arity; init/cond/post may be
//     InvalidNode (bare `for {}`, cond-only `for cond {}`, or any clause
//     omitted in the 3-clause form)
//   - ParamList: [param0, param1, ...] - variable arity
//   - Param, Field: [name, type] - fixed arity (identical shape; which one
//     a node is depends on its parent - a Param under a ParamList, a Field
//     under a StructDecl - not on anything intrinsic to the node)
//   - FuncDecl: [receiver, name, paramList, returnType, body] - fixed
//     arity; receiver and returnType may be InvalidNode. Params aren't
//     flattened directly into FuncDecl's own children because they're
//     variable-arity but followed by more fixed slots (returnType, body) -
//     ParamList is its own variable-arity node for exactly the reason
//     CompositeLit's elements or Block's statements are, just wrapped so
//     FuncDecl itself can stay fixed-arity
//   - StructDecl: [name, member0, member1, ...] - variable arity, name always
//     first (same CallExpr-style shape - nothing follows the variable part,
//     unlike FuncDecl, so no wrapper is needed here). Each member is either a
//     Field, a ConstructorDecl, or a DestructorDecl, interspersed in
//     declaration order - see ast.Tree's StructFields/StructConstructors/
//     StructDestructors, which each filter the full member list down to
//     their own kind.
//   - ConstructorDecl: [paramList, body] - fixed arity. A constructor is a
//     narrow, deliberate exception to "structs are data-only, methods
//     declared separately" (see LANGUAGE.md's "Constructors" section): it's
//     nested directly inside its StructDecl, has no name of its own (it's
//     selected by argument count, not called by name), no receiver clause
//     (its receiver is always the struct being constructed - `this` inside
//     its body resolves exactly like inside an ordinary method), and no
//     return type (it "returns" the struct implicitly by populating `this`).
//   - DestructorDecl: [paramList, body] - fixed arity, the exact same shape
//     as ConstructorDecl (see LANGUAGE.md's "Destructors" section) - reused
//     directly rather than inventing a paramList-less shape, even though a
//     destructor's own paramList is always semantically required to be empty
//     (sema, not the grammar, rejects a non-empty one - see
//     sema.checkDestructorDecl): a destructor is never called with explicit
//     arguments, only invoked implicitly (at a local's scope exit, or by
//     `delete` against a pointer to it), so there's no call-site syntax that
//     would ever need to supply any. At most one per struct - a second one is
//     a compile-time error, reported at struct-declaration time exactly like
//     a duplicate-arity constructor (see sema.declareDestructor).
//   - File: [decl0, decl1, ...] - variable arity (the parse tree root)
//   - ParamTypeList: [type0, type1, ...] - variable arity, each child a bare
//     type-position node (an Ident, ArrayType, or another FuncType - no name,
//     unlike ParamList's Param children) - a function type's parameter
//     types, e.g. the `int, string` in `func(int, string) bool`
//   - FuncType: [paramList, returnType] - fixed arity; paramList is always a
//     ParamTypeList node (empty for a no-parameter function type); returnType
//     may be InvalidNode (the function type declares no return value, same
//     as FuncDecl's own optional return type - `func(int)` is a function
//     type over a function taking one int and returning nothing). See
//     LANGUAGE.md's "First-class functions" section for the language-level
//     feature this represents, and CODEGEN.md for how it's lowered.
//   - FuncLit: [paramList, returnType, body] - fixed arity; paramList is
//     always a ParamList node (empty for a no-parameter literal, same as
//     FuncDecl's own); returnType may be InvalidNode (an implicitly-void
//     literal, same optional-return-type rule FuncDecl/FuncType both already
//     have). Exactly FuncDecl's own [receiver, name, paramList, returnType,
//     body] shape minus the two slots a literal has no use for - it's
//     anonymous (no name) and never has a receiver (see LANGUAGE.md's
//     "Lambdas" section: a lambda closes over its enclosing scope by
//     reference instead, which is a sema/codegen-level concern, not a
//     grammar one - the AST shape itself carries no capture information).
//   - PointerType: [elem] - fixed arity, a type-position node for `*T` (see
//     LANGUAGE.md's "Pointers" section) - the pointer counterpart to
//     ArrayType's own [size, elem] shape, minus the size slot a pointer type
//     has no use for.
//   - NewExpr: [inner] - fixed arity; inner is an ordinary, already-legal
//     constructor-call (`T(args)`) or composite-literal (`T{...}`) expression
//   - `new` wraps one of those two unchanged, it doesn't introduce a new
//     inner grammar of its own (see LANGUAGE.md's "Pointers" section).
//   - DeleteStmt: [expr] - fixed arity; expr is the pointer-typed expression
//     being freed (see LANGUAGE.md's "Pointers" section) - a statement, not a
//     value-producing call, the same way BreakStmt/ContinueStmt/ReturnStmt
//     are their own dedicated statement forms rather than call-shaped
//     builtins.
//   - SliceExpr: [object, low, high] - fixed arity; a Go-style slice
//     expression (`s[a:b]`, `s[:b]`, `s[a:]`, `s[:]` - see LANGUAGE.md's
//     "Slicing" section). low/high are each InvalidNode when omitted (the
//     same "reserve the positional slot" convention every other optional
//     child already uses - e.g. FuncDecl's own optional return-type slot),
//     defaulting to 0 / the operand's own length-or-capacity respectively -
//     a sema/codegen concern, not a grammar one. Parsed by the same `[`
//     infix rule IndexExpr already is (parser/expr.go's parseIndexExpr):
//     after `[`, an optional low expression (absent when the very next token
//     is `:`), then a `:` disambiguates this from a plain IndexExpr - no `:`
//     following the first expression means IndexExpr, unchanged.
//   - a fixed-arity kind may reserve a positional slot as InvalidNode for an
//     omitted optional child (e.g. VarDecl's type annotation); a
//     variable-arity kind (Block's statements, CallExpr's arguments) is
//     accessed by iterating the full Children range instead.
type Node struct {
	Kind enums.NodeKind
	Tok  lexer.Token
	Span Span

	Parent        NodeIndex
	IndexInParent int32

	Children lexer.Range
}
