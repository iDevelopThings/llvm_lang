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
//   - AssignStmt, IncDecStmt, MultiAssignStmt: the assignment/inc-dec
//     operator token (=, +=, -=, *=, /=, ++, --; MultiAssignStmt only ever
//     carries plain `=` - see its own doc comment below)
//   - ShortVarDecl, MultiShortVarDecl: the `:=` token
//   - VarDecl, FuncDecl, StructDecl, IfStmt, ForStmt, ReturnStmt, BreakStmt,
//     ContinueStmt, FuncType, ConstructorDecl, DestructorDecl, FuncLit,
//     NewExpr, DeleteStmt, ExternFuncDecl, EnumDecl, MatchStmt, YieldStmt,
//     AwaitStmt: the leading keyword token (EnumDecl's is `enum`, MatchStmt's
//     is `match`, YieldStmt's is `yield` - see LANGUAGE.md's "Enums"/"match"
//     sections). FuncDecl's is normally `func`, but is `async` instead for an
//     `async func` (see LANGUAGE.md's "Coroutines" section) - the same "same
//     shape, different flag" convention as YieldReturnType below, just
//     carried in Tok rather than a child node, since async is a whole-
//     declaration marker, not a return-type-position one; Tree.FuncIsAsync
//     reads this rather than every call site comparing Tok.Keyword directly.
//   - EnumVariant: the variant's own name identifier token
//     (ExternFuncDecl's is the `extern` keyword, not the `func` that follows
//     it - see LANGUAGE.md's "External functions (FFI)" section; FuncType's
//     is the `func` that introduces a function-type expression, e.g.
//     `func(int) int`, see LANGUAGE.md's Types section; FuncLit's is the
//     `func` that introduces a function-literal expression, e.g.
//     `func(x int) int { return x }`, see LANGUAGE.md's "Lambdas" section -
//     the same keyword, disambiguated from FuncType purely by whether a `{`
//     body follows the parameter list/return type, exactly the same way
//     FuncDecl's own body already disambiguates it from a bare declaration;
//     NewExpr's is the `new` keyword, DeleteStmt's the `delete` keyword -
//     see LANGUAGE.md's "Pointers" section; DestructorDecl's is the
//     `destructor` keyword - see LANGUAGE.md's "Destructors" section;
//     MoveExpr's is the `move` keyword, same section)
//   - `&`/`*` address-of/dereference are ordinary UnaryExpr nodes - Tok is
//     the operator token exactly like unary `-`/`!`, distinguished purely by
//     Tok.Lexeme, no new node kind needed for either (see LANGUAGE.md's
//     "Pointers" section)
//   - everything else (File, Block, ParamList, Param, Field, CallExpr,
//     ParenExpr, IndexExpr, ArrayType, PointerType, CompositeLit,
//     KeyValueExpr, ExprStmt, ParamTypeList, MapType, MultiReturnType,
//     MultiValueExpr, MatchArm): unused, left as the zero Token
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
//   - MapType: [key, elem] - fixed arity, a type-position node for `map[K]V`
//     (see LANGUAGE.md's "Maps" section) - the map counterpart to ArrayType's
//     own [size, elem] shape, minus the size slot a map type has no use for
//     (a map's own runtime element count is never part of its declared
//     type, unlike a fixed-size array's `N`). Parsed by parseTypeExpr the
//     exact same recursive-into-element-type way `[]T` already is, keyed on
//     the `map` keyword instead of `[` - see parser's own parseTypeExpr.
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
//   - YieldStmt: [expr] - fixed arity, ReturnStmt's direct structural analog
//     for `yield expr` (see LANGUAGE.md's "match" section: "match as an
//     expression") - expr is always present (there is no bare `yield`,
//     unlike a bare `return`). Legal only inside a match-expression arm's
//     own block - enforced by sema (checkYieldStmt), not this grammar,
//     exactly the way `break`/`continue` are grammatically legal anywhere
//     but sema-rejected outside a loop (checkBreakOrContinue's c.loopDepth
//     check is the model - checkYieldStmt's own c.matchExprStack is its
//     stack-shaped counterpart, since yield additionally needs live access
//     to its enclosing match expression's own running result type, not just
//     a yes/no "am I nested deep enough" counter). Produced two ways: a
//     `yield expr` the user actually wrote inside a block-bodied arm, or a
//     synthetic node the parser itself builds (parseMatchExprArm) when an
//     arm's body is a bare expression with no braces - both shapes reach
//     sema/codegen identically, so a match expression's arm body is always
//     exactly one canonical shape by the time either pass sees it: a Block
//     whose every reachable path ends in a YieldStmt.
//   - BreakStmt, ContinueStmt: no children (leaves)
//   - AwaitStmt: no children (leaf) - a bare `await`, legal only inside an
//     async function's own body (see LANGUAGE.md's "Coroutines" section) -
//     unlike YieldStmt, there is no operand at all this round.
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
//   - MoveExpr: [operand] - fixed arity; operand is always a bare Ident (see
//     LANGUAGE.md's "Destructors" section) - the parser rejects any other
//     shape (`this.field`, `arr[i]`, a parenthesized name) at parse time,
//     never producing a MoveExpr around one.
//   - ExternFuncDecl: [name, paramList, returnType] - fixed arity; returnType
//     may be InvalidNode (an implicitly-void extern declaration). A deliberate,
//     separate top-level declaration kind for `extern func Name(params)
//     RetType` (see LANGUAGE.md's "External functions (FFI)" section) - NOT a
//     nullable-body variant of FuncDecl: FuncDecl's own body slot is always
//     present everywhere else this grammar/sema/codegen relies on that (see
//     DECISIONS.md), so binding an external C symbol (no body at all, ever)
//     gets its own narrow node kind instead of threading a nil-body case
//     through all of that. No receiver clause (an extern func can never be a
//     method) and no body - the declaration ends right after the optional
//     return type, exactly like a type-less `var` already does for statement
//     termination. Reuses the exact same ParamList grammar node FuncDecl's own
//     paramList child does (parameters are still `name Type` pairs).
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
//   - MultiReturnType: [type0, type1, ...] - variable arity, each child a bare
//     type-position node (an Ident, ArrayType, PointerType, or FuncType - no
//     name, the same shape ParamTypeList's own children already have) - a
//     multi-return function's declared `(T1, T2, ...)` return-type list (see
//     LANGUAGE.md's "Go-style multi-return values" section). Sits in
//     FuncDecl's existing single return-type child slot in place of a plain
//     type-position node, mirroring how ParamList already wraps FuncDecl's
//     own variable-arity params inside one fixed slot - FuncReturnType(decl)
//     keeps returning exactly one node; that node is now sometimes this
//     wrapper instead. Only ever produced parsing a FuncDecl's own return
//     type (parser.parseFuncDeclReturnType) - a FuncType/FuncLit/
//     ExternFuncDecl's return-type slot still only ever parses a single plain
//     type-position node via parseTypeExpr, unchanged, so this node kind can
//     never appear there.
//   - MultiValueExpr: [value0, value1, ...] - variable arity, each child an
//     ordinary value expression - a multi-value `return a, b, ...` (see
//     LANGUAGE.md's "Go-style multi-return values" section). Sits in
//     ReturnStmt's existing single `expr` child slot in place of a plain
//     value expression, the exact same "wrap the variable-arity case in its
//     own node so the fixed slot stays fixed" convention MultiReturnType just
//     above uses - a plain single-value `return expr` is a completely
//     unchanged ReturnStmt whose expr child is simply that one expression
//     directly, never this wrapper.
//   - MultiShortVarDecl: [name0, name1, ..., nameN, value] - variable arity;
//     every child except the last is a freshly-declared Ident name, the last
//     is the sole right-hand-side value being destructured - either an
//     ordinary call expression (the multi-return case, `a, b := f()` - see
//     LANGUAGE.md's "Go-style multi-return values" section) or an IndexExpr
//     naming a map (the two-result-index case, `v, ok := m[k]` - see
//     LANGUAGE.md's "Maps" section and sema.checkDestructureSource, which
//     branches on the two shapes). At least two names (a single-name
//     `x := f()`/`x := m[k]` is the existing, completely unchanged
//     ShortVarDecl). Use ast.Tree's MultiShortVarDeclNames/
//     MultiShortVarDeclValue accessors rather than indexing directly - the
//     split point (last child vs. everything before it) isn't a fixed
//     position the way most other variable-arity node's "special" slot is.
//   - EnumDecl: [name, member0, member1, ...] - variable arity, name always
//     first - the exact same shape StructDecl already uses (see LANGUAGE.md's
//     "Enums" section). Each member is either an EnumVariant node or (at most
//     one) a DestructorDecl, interspersed in declaration order, reusing
//     DestructorDecl completely unchanged (an enum's destructor fires once,
//     regardless of which variant is actually active, exactly like a
//     struct's) - see ast.Tree's EnumVariants/EnumDestructors accessors,
//     which each filter the full member list down to their own kind, mirroring
//     StructFields/StructDestructors.
//   - EnumVariant: Tok is the variant's own name identifier. Children vary by
//     which of the three variant kinds this is, distinguished purely by
//     shape - no separate kind flag on the node itself:
//   - a unit variant (`Point`) has no children at all.
//   - a tuple variant (`Circle(f64)`) has one child per associated type,
//     each an ordinary type-position node (Ident/ArrayType/PointerType/
//     MapType/FuncType/MemberExpr) - never a Field node.
//   - a struct variant (`Triangle { base f64, height f64 }`) has one Field
//     child ([name, type], the exact same shape a struct's own field
//     already uses) per named associated field.
//     Since a type-position node is never itself Kind == Field, inspecting
//     the first child's own Kind (when any children exist at all) is
//     sufficient to tell a tuple variant from a struct variant - see
//     ast.Tree's EnumVariantKind-classifying accessor.
//   - MatchStmt: [subject, arm0, arm1, ...] - variable arity, subject (the
//     value being matched) always first (see LANGUAGE.md's "match" section).
//     Reached two genuinely different ways now, both producing this exact
//     same node shape - checkStmt/genStmt's own dispatch for a bare
//     statement-position `match x {...}` (checkMatchStmt/genMatchStmt,
//     unchanged since match was first introduced), or checkExpr/genExpr's
//     own dispatch when it appears anywhere an expression is legal instead
//     (checkMatchExprStmt/genMatchExpr - see LANGUAGE.md's "match" section's
//     "match as an expression" subsection) - which of the two a given node
//     was parsed as depends purely on which grammar rule reached it
//     (parseMatchStmt vs. parseMatchExpr, parser/stmt.go vs. parser/expr.go),
//     never a flag on the node itself. Each arm is a MatchArm node, in
//     source order.
//   - MatchArm: [pattern0, pattern1, ..., patternN, body] - variable arity,
//     at least one pattern always present, body always last (see ast.Tree's
//     MatchArmPatterns/MatchArmPattern/MatchArmBody accessors). Multiple
//     comma-separated patterns per arm (`1, 2, 3 => { ... }`, Go's own
//     `case a, b, c:` shape) is a value-match-only feature (see LANGUAGE.md's
//     "match" section's plain-value-pattern extension) - an enum-match arm
//     is restricted back down to exactly one pattern by sema
//     (checkEnumMatchStmt), not by this grammar, since the grammar can't yet
//     know whether the subject is an enum or a plain value. Each pattern is
//     whatever parseExpr's ordinary grammar already produces for one of: a
//     bare wildcard (`_`, an Ident node - the only Ident pattern shape
//     resolve.go's resolvePattern special-cases; any *other* Ident is now
//     instead an ordinary value-pattern reference, e.g. a constant/variable
//     used as a case value - see LANGUAGE.md's "match" section), a
//     unit-variant pattern (`EnumName.Variant`, a MemberExpr - identical
//     shape to a unit variant's own construction expression), a tuple-variant
//     pattern (`EnumName.Variant(a, b)`, a CallExpr whose "arguments" are
//     fresh binding-name Ident nodes rather than value expressions -
//     identical shape to a tuple variant's own construction call), a
//     struct-variant pattern (`EnumName.Variant{field: newName, ...}`, a
//     CompositeLit whose keyed elements' values are likewise fresh binding
//     names - identical shape to a struct variant's own construction
//     literal), or - new this round - an ordinary value expression (a
//     literal, a variable/constant reference, or any other expression shape
//     not recognized as one of the enum-pattern shapes above - see
//     LANGUAGE.md's "match" section's plain-value-pattern extension).
//     Reusing construction's own CallExpr/CompositeLit/MemberExpr grammar
//     verbatim needs zero new expression-parsing code - only sema's
//     pattern-resolution/type-checking (resolvePattern/checkMatchArmPattern)
//     actually tells "this MemberExpr/CallExpr/CompositeLit is an
//     enum-variant pattern, not an ordinary expression - the calls inside are
//     fresh bindings, not references" apart from an ordinary construction use
//     of the identical shape, or an ordinary value-expression use of it.
//     body is an ordinary Block for a statement-position match's arm
//     (parseMatchArm), always. For an expression-position match's arm
//     (parseMatchExprArm - see LANGUAGE.md's "match" section's "match as an
//     expression" subsection), body is ALSO always a Block by the time
//     sema/codegen see it, but may have gotten there either way a user
//     actually wrote it: a real brace-delimited `{ ... }` the user wrote
//     (parsed via parseBlock, completely unchanged - may contain `yield`
//     anywhere inside, at any nesting depth, alongside ordinary
//     if/for/whatever), or a bare expression with no braces at all
//     (`pattern => expr`), which the parser itself desugars into a
//     synthetic single-statement Block wrapping a synthetic YieldStmt around
//     that expression - mirroring ForStmt's own init/post slots, which
//     already hold "whatever parseStmt/a dedicated rule produced," not
//     always the exact same node kind. This desugaring exists purely so
//     sema/codegen only ever have to handle ONE canonical arm-body shape ("a
//     Block whose every reachable path must yield") regardless of which
//     surface form the user actually wrote - see YieldStmt's own doc
//     comment above.
//   - MultiAssignStmt: [target0, target1, ..., targetN, value] - the
//     assignment-form counterpart to MultiShortVarDecl, identical shape:
//     every child except the last is an already-existing lvalue target
//     (Ident, MemberExpr, IndexExpr, or a `*p` UnaryExpr dereference - exactly
//     the same shapes plain AssignStmt's own single target already allows),
//     the last is the sole right-hand-side value being destructured - either
//     an ordinary call expression (the multi-return case, `a, b = f()`) or an
//     IndexExpr naming a map (the two-result-index case, `v, ok = m[k]` -
//     same two shapes MultiShortVarDecl's own doc comment above describes).
//     At least two targets (a single-target `x = f()` is the existing,
//     completely unchanged AssignStmt). Use MultiAssignStmtTargets/
//     MultiAssignStmtValue, same reasoning as MultiShortVarDecl above.
//   - RangeExpr: [subject] - fixed arity, `range subject` in EXPRESSION
//     position (see LANGUAGE.md's "Range loops" section) - grammatically
//     legal anywhere an expression is (mirroring match/new - see
//     parser/expr.go's parseIdentExpr), though only ever meaningful as a
//     for-loop header's `:=` value; anywhere else sema rejects it with a
//     clean diagnostic (checkExpr's own RangeExpr case).
//   - RangeForStmt: [key, value, subject, body] - fixed arity, `for [key[,
//     value]] := range subject { body }` (see LANGUAGE.md's "Range loops"
//     section) - key/value are each InvalidNode when that binding is
//     omitted (the zero-binding `for range subject {}` form has both
//     InvalidNode; the one-binding form has only key set - binding the map
//     key or the array index, never the value, per Go's own real rule - see
//     LANGUAGE.md). A dedicated node, deliberately not squeezed into
//     ForStmt's own [init, cond, post, body] shape - a genuinely different
//     construct (see parser/stmt.go's parseForStmt). Built by unwrapping the
//     ExprStmt/ShortVarDecl/MultiShortVarDecl grammar parseSimpleStmt already
//     produces once its value is a RangeExpr - key/value are simply that
//     statement's own already-parsed name node(s), never a fresh grammar
//     rule of their own.
//   - YieldReturnType: [elemType] - fixed arity, a FuncDecl's own `yield T`
//     return-type marker (see LANGUAGE.md's "Generator functions" section) -
//     elemType is an ordinary type-position node, the same shape
//     PointerType's own single child already has. Sits in FuncDecl's
//     existing return-type child slot, mirroring MultiReturnType - only ever
//     produced parsing a FuncDecl's own return type
//     (parser.parseFuncDeclReturnType); a method's receiver-clause
//     restriction (a generator can't be a method) is a sema concern, not a
//     grammar one, since parseFuncDecl's return-type parsing doesn't know
//     yet whether a receiver clause preceded it.
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
