// Package codegen lowers a resolved and type-checked *ast.Tree (plus its
// *sema.Info) - or, for a multi-file package, every file's own tree/info
// pair together (see GeneratePackage and LANGUAGE.md's "Multi-file
// packages" section) - into one LLVM Module, via tinygo.org/x/go-llvm.
//
// This package assumes its input is already fully valid: Generate/
// GeneratePackage are meant to be called only on tree(s) for which
// sema.Resolve/sema.ResolvePackage and sema.Check/sema.CheckPackage all
// returned an empty diagnostic Bag (see AGENTS.md - "a whole program is
// available as tree+info with everything already validated; you are
// lowering already-correct, already-typed code to LLVM IR, not re-deriving
// semantics"). Feeding it a tree with unresolved names or type errors is not
// supported and may panic - every lookup here (info.Refs, info.Types,
// info.Structs) assumes the entry it wants is actually present.
//
// A top-level `var`'s initializer is real generated code now, not required
// to be a compile-time constant (see CODEGEN.md's "Global var initializers"
// section and globalinit.go) - one still-possible codegen-only diagnostic is
// a genuine error inside an otherwise constant-*shaped* initializer (division
// by zero, an out-of-range literal), which lands in the affected file's own
// diag.Bag rather than panicking, same as every other pass in this compiler.
package codegen

import (
	"llvm_lang/src/ast"
	"llvm_lang/src/diag"
	"llvm_lang/src/enums"
	"llvm_lang/src/sema"

	"tinygo.org/x/go-llvm"
)

// Module is a generated LLVM module together with the Context that owns it.
// Both must be disposed once the caller is done (verifying, JIT-executing,
// printing) - see Dispose.
type Module struct {
	Ctx  llvm.Context
	LLVM llvm.Module
}

// Dispose releases the module and its owning context. Safe to call once,
// after which m must not be used again.
func (m *Module) Dispose() {
	m.LLVM.Dispose()
	m.Ctx.Dispose()
}

// loopCtx is one nested for-loop's break/continue branch targets - see
// genForStmt and genBreakStmt/genContinueStmt.
type loopCtx struct {
	breakTarget    llvm.BasicBlock
	continueTarget llvm.BasicBlock
}

// funcCtx is pushed once per function body being generated - the state
// genReturnStmt and the function's final fallback-terminator logic need
// about the function currently being built. A function literal (FuncLit -
// see LANGUAGE.md's "Lambdas" section) can now nest arbitrarily deep inside
// another function or another literal; Generator still only needs a single
// field for this (not an explicit stack), the same save-in-a-local/restore
// shape sema/typecheck.go's curFunc uses one layer up - see genLambdaFunc,
// which saves every one of Generator's per-function-frame fields (this one
// included) before switching to a nested literal's own fresh state, and
// restores them once that literal's body is fully generated.
type funcCtx struct {
	isMain    bool
	hasReturn bool
}

// funcEntry is what the module-scope pass over every FuncDecl/method
// records, before any body is generated - see declareFuncSignature. Looking
// a call's callee up in Generator.funcs never has to re-derive a function's
// LLVM type from its AST node, however many call sites reference it.
type funcEntry struct {
	fn       llvm.Value
	fnType   llvm.Type
	retType  sema.Type
	isMethod bool
}

// structLayout is one struct type's LLVM shape: its (named) LLVM struct
// type, each field's positional GEP index (keyed by the field's *sema.Symbol
// - the same one info.Refs resolves a MemberExpr's field name to), and each
// field's LLVM type in declaration order (needed to build a per-field zero
// value for a keyed composite literal that doesn't mention every field).
// fieldSemaTypes parallels fieldTypes but keeps each field's sema.Type
// (rather than its LLVM type) in the same declaration order - genPrintCall's
// struct case (runtime.go) needs the actual sema.Type to decide how to
// render each field, recursively.
type structLayout struct {
	llvmType       llvm.Type
	fieldIndex     map[*sema.Symbol]int
	fieldTypes     []llvm.Type
	fieldSemaTypes []sema.Type
}

// Generator holds every piece of state needed to lower a whole package (one
// or more *ast.Trees, sharing one *sema.Info per tree - see GeneratePackage)
// into one Module. It's used once per GeneratePackage call, never reused.
//
// tree/info/diags always describe "whichever file's declarations are
// currently being lowered" - genPackage's own pass loops switch these via
// enter as they move from one file to the next. Unlike sema/typecheck.go's
// checker, nothing here ever needs to *switch back* mid-declaration to
// follow a Symbol into another file: every cross-file reference a function
// body can contain (a call to a function declared elsewhere, a struct type
// named elsewhere, a global var declared elsewhere) is looked up through
// funcs/globals/structLayouts below, all keyed by *sema.Symbol or
// *sema.StructInfo pointer identity rather than by ast.NodeIndex - so once
// declareStructType/defineStructBody/genGlobalVarDecl/declareFuncSignature
// have each run once (across every file, before any function body is
// generated - see genPackage), every one of those lookups is a plain,
// tree-agnostic map read, regardless of which file originally declared the
// symbol being referenced.
type Generator struct {
	infos    map[*ast.Tree]*sema.Info
	allDiags map[*ast.Tree]*diag.Bag

	tree *ast.Tree
	info *sema.Info

	ctx     llvm.Context
	mod     llvm.Module
	builder llvm.Builder
	diags   *diag.Bag

	// Primitive LLVM types - computed once in setupTypes. See AGENTS.md's
	// codegen section for why `int` is i32 (not i64) and `string` is the
	// literal struct {ptr, i32} (pointer + length, not null-terminated).
	i8Ty     llvm.Type
	i16Ty    llvm.Type
	i32Ty    llvm.Type
	i64Ty    llvm.Type
	f32Ty    llvm.Type
	f64Ty    llvm.Type
	boolTy   llvm.Type
	ptrTy    llvm.Type
	stringTy llvm.Type
	voidTy   llvm.Type

	// funcValTy is the fat-pointer LLVM representation of a first-class
	// function value: the literal struct {ptr, ptr} = {fnPtr, ctxPtr} - see
	// CODEGEN.md's "First-class functions" section and genFuncValue
	// (expr.go), the one site that actually constructs one.
	funcValTy llvm.Type

	// dynArrTy is a dynamic array's (`[]T`) LLVM representation: the literal
	// struct {ptr, i32, i32} = {dataPtr, len, cap} - see CODEGEN.md's
	// "Dynamic arrays" section and setupTypes for why one shared struct type
	// serves every element type.
	dynArrTy llvm.Type

	structLayouts map[*sema.StructInfo]*structLayout
	globals       map[*sema.Symbol]llvm.Value

	// globalInits queues every non-constant top-level `var`'s initializer -
	// appended by genGlobalVarDecl, in source declaration order across every
	// file in the package (the same order genPackage's own per-tree loops
	// already visit declarations in) - for genGlobalCtors (globalinit.go) to
	// consume, once, after every global/function/constructor signature in the
	// whole package already exists. See CODEGEN.md's "Global var
	// initializers" section for why declaration order (not a full
	// dependency-graph topological sort) is this round's deliberately scoped
	// ordering rule.
	globalInits []globalInitEntry

	// funcs is keyed by *sema.Symbol (the declared free function or
	// method's own symbol, from Info.Refs), not by its FuncDecl's
	// ast.NodeIndex - a NodeIndex is only meaningful relative to the one
	// Tree it came from (see ast.NodeIndex's doc comment), and once a
	// package can span multiple files/trees, two unrelated functions in
	// different files can share the same NodeIndex value. A *sema.Symbol
	// pointer is already globally unique (one fresh Symbol per declaration,
	// see sema/resolve.go), so keying on it sidesteps the collision
	// entirely - the same reasoning globals/structLayouts already apply.
	funcs map[*sema.Symbol]funcEntry

	// ctors is funcs' counterpart for constructors (see LANGUAGE.md's
	// "Constructors" section) - kept as its own map, rather than folded into
	// funcs, since a constructor's Symbol (sema.SymConstructor) is a
	// distinct kind from an ordinary free-function/method's (sema.SymFunc):
	// keying both kinds into one map would work (the two Symbol pointer
	// spaces never collide), but a dedicated map keeps "which of these two
	// completely different declaration shapes does this call resolve to"
	// obvious at every read site, the same reason structLayouts/globals are
	// each their own map rather than one generic "everything" map. Populated
	// by declareConstructorSignature, read by genConstructorCall - see
	// func.go.
	ctors map[*sema.Symbol]funcEntry

	// locals is reset at the start of every function (see genFuncBody) -
	// every VarDecl/ShortVarDecl/Param declaration node produces its own
	// distinct *sema.Symbol (see sema/resolve.go's declareLocal), so a flat
	// map needs no explicit scope-stack to avoid collisions between two
	// same-named variables in different nested blocks of the same function.
	locals map[*sema.Symbol]llvm.Value

	// strLiterals dedupes identical string-literal contents into a single
	// backing global (keyed by the literal's already-unescaped text).
	strLiterals map[string]llvm.Value

	// curFn/entryBlock/curFunc/loopStack/curReceiver are all per-function
	// generation state, set at the start of genFuncBody (and, for
	// curReceiver, cleared for a non-method) and read by whatever's being
	// generated inside that function's body. genLambdaFunc saves and
	// restores every one of these (plus curCtxPtr/curCaptureIndex/
	// curCaptureTy just below) around generating a nested FuncLit's own
	// body, so a lambda's own frame never bleeds into its enclosing
	// function's once control returns to it - see its own doc comment.
	curFn       llvm.Value
	entryBlock  llvm.BasicBlock
	curFunc     *funcCtx
	loopStack   []loopCtx
	curReceiver llvm.Value

	// curCtxPtr/curCaptureIndex/curCaptureTy describe the function currently
	// being generated only when it's itself a lambda's own synthesized
	// function (see genLambdaFunc) - the zero value/nil for an ordinary
	// function, method, or constructor, none of which ever receives a ctxPtr
	// parameter at all (see CODEGEN.md's "Lambdas" section: only a genuine
	// lambda's real underlying function does). curCtxPtr is that function's
	// own real first parameter; curCaptureIndex maps each symbol it captures
	// (info.Captures of the FuncLit genLambdaFunc is generating) to that
	// value's own field index within curCaptureTy, its capture-context
	// struct type (needed as CreateStructGEP's aggregate-type argument) -
	// see addrOfSymbol, the one place both are read.
	curCtxPtr       llvm.Value
	curCaptureIndex map[*sema.Symbol]int
	curCaptureTy    llvm.Type

	// thunks memoizes, per free-function Symbol, the small uniform-ABI thunk
	// genFuncValue builds the first time that function is ever referenced
	// bare (as a value, not called) - see genFuncThunk's own doc comment for
	// why this exists at all (CODEGEN.md's "Lambdas" section: a direct call
	// bypasses this entirely and stays genuinely zero-overhead).
	thunks map[*sema.Symbol]llvm.Value

	// lambdaCounter synthesizes each FuncLit's own unique, collision-free LLVM
	// function name (genLambdaFunc) - a plain monotonically increasing
	// counter shared across the whole module is simpler than deriving a name
	// from "whichever function lexically encloses this literal" and equally
	// guaranteed collision-free, since every lambda gets its own fresh number
	// regardless of nesting depth or which function contains it.
	lambdaCounter int

	// Runtime externs and cached format-string globals - see runtime.go.
	printfType llvm.Type
	printfFn   llvm.Value
	mallocType llvm.Type
	mallocFn   llvm.Value
	freeType   llvm.Type
	freeFn     llvm.Value
	memcpyType llvm.Type
	memcpyFn   llvm.Value
	memcmpType llvm.Type
	memcmpFn   llvm.Value
	memsetType llvm.Type
	memsetFn   llvm.Value
	trapType   llvm.Type
	trapFn     llvm.Value
	fmtInt     llvm.Value
	fmtInt64   llvm.Value
	fmtFloat   llvm.Value
	fmtStr     llvm.Value

	// The bump-allocator arena (see setupArena in runtime.go): a generated
	// LLVM function every heap-needing string operation calls into instead of
	// malloc directly, plus the two mutable globals backing its state (the
	// current block's bump cursor and remaining byte count). See AGENTS.md's
	// codegen section for the exact design.
	arenaAllocType       llvm.Type
	arenaAllocFn         llvm.Value
	arenaCursorGlobal    llvm.Value
	arenaRemainingGlobal llvm.Value

	// Struct/array printing (see genPrintStructValue/genPrintArrayValue in
	// runtime.go) needs a handful of additional cached format-string
	// globals: "bare" (no trailing newline) int/string specifiers for a
	// nested field/element, plus the literal punctuation gluing them
	// together. See AGENTS.md's codegen section for the exact format.
	fmtIntBare   llvm.Value
	fmtInt64Bare llvm.Value
	fmtFloatBare llvm.Value
	fmtStrBare   llvm.Value
	fmtSpace     llvm.Value
	fmtLBrace    llvm.Value
	fmtRBrace    llvm.Value
	fmtLBracket  llvm.Value
	fmtRBracket  llvm.Value
	fmtNewline   llvm.Value
}

// Generate lowers tree (with its resolved/checked info) into a fresh LLVM
// Module named moduleName. See the package doc comment for the validity
// assumption this makes about tree/info, and BLOCKERS.md for the codegen-
// level restrictions (constant global initializers, unsupported print
// argument types) that can still produce diagnostics here.
//
// Generate is the single-file case of GeneratePackage - see its doc comment
// for the multi-file package entry point this wraps.
//
// The returned Module owns its own Context; the caller must call
// Module.Dispose once done with it (after verifying/JIT-executing/printing).
func Generate(tree *ast.Tree, info *sema.Info, moduleName string) (*Module, *diag.Bag) {
	mod, diags := GeneratePackage([]*ast.Tree{tree}, map[*ast.Tree]*sema.Info{tree: info}, moduleName)
	return mod, diags[tree]
}

// GeneratePackage lowers every file in trees (each with its own already-
// resolved/checked *sema.Info, sharing one package-wide struct catalog and
// symbol identity - see sema.CheckPackage) into one shared LLVM Module named
// moduleName: a function or global declared in one file is directly
// callable/referenceable from another file's code in the same module, using
// the same *sema.Symbol/*sema.StructInfo pointer identity codegen already
// keys every lookup by (see the Generator doc comment) - not one Module per
// file with cross-module linking, which this package has no need for since
// every file in a package always ends up in the exact same module anyway.
//
// The returned Module owns its own Context; the caller must call
// Module.Dispose once done with it (after verifying/JIT-executing/printing).
// Diagnostics come back one *diag.Bag per file (a diagnostic's Pos is only
// meaningful relative to the one file it's reported against - see
// sema.CheckPackage's own doc comment for the identical reasoning), keyed by
// tree, same as sema.ResolvePackage/sema.CheckPackage.
func GeneratePackage(trees []*ast.Tree, infos map[*ast.Tree]*sema.Info, moduleName string) (*Module, map[*ast.Tree]*diag.Bag) {
	ctx := llvm.NewContext()
	mod := ctx.NewModule(moduleName)
	builder := ctx.NewBuilder()

	g := &Generator{
		infos:         infos,
		allDiags:      make(map[*ast.Tree]*diag.Bag, len(trees)),
		ctx:           ctx,
		mod:           mod,
		builder:       builder,
		structLayouts: make(map[*sema.StructInfo]*structLayout),
		globals:       make(map[*sema.Symbol]llvm.Value),
		funcs:         make(map[*sema.Symbol]funcEntry),
		ctors:         make(map[*sema.Symbol]funcEntry),
		strLiterals:   make(map[string]llvm.Value),
		thunks:        make(map[*sema.Symbol]llvm.Value),
	}
	for _, tree := range trees {
		g.allDiags[tree] = diag.NewBag()
	}
	g.setupTypes()
	g.setupRuntime()
	g.genPackage(trees)

	builder.Dispose()

	diags := make(map[*ast.Tree]*diag.Bag, len(trees))
	for _, tree := range trees {
		diags[tree] = g.allDiags[tree]
	}
	return &Module{
		Ctx:  ctx,
		LLVM: mod,
	}, diags
}

// enter switches the Generator's current-file bookkeeping to tree - see the
// Generator doc comment for why nothing here ever needs to switch back
// mid-declaration the way sema/typecheck.go's checker does.
func (g *Generator) enter(tree *ast.Tree) {
	g.tree = tree
	g.info = g.infos[tree]
	g.diags = g.allDiags[tree]
}

// genPackage drives the whole module in passes across every file in trees,
// mirroring sema's own resolvePackage/checkPackage shape (struct catalogs,
// then globals, then function/constructor signatures, then function/
// constructor bodies - each pass covering every file before the next begins)
// so declaration order never matters, either within one file or across the
// whole package: a function can call another declared later (in the same
// file or a different one), a global's type can name a struct declared
// later (ditto), and a constructor call can reach a constructor declared
// later, or on a struct in a different file/package entirely (see
// LANGUAGE.md's "Constructors" section) - every constructor's signature is
// declared across the whole program before any function/constructor body is
// generated, exactly like an ordinary function's.
func (g *Generator) genPackage(trees []*ast.Tree) {
	for _, tree := range trees {
		g.enter(tree)
		for d := range tree.TopLevelDeclsOfKind(enums.NodeKinds.StructDecl) {
			g.declareStructType(d)
		}
	}
	for _, tree := range trees {
		g.enter(tree)
		for d := range tree.TopLevelDeclsOfKind(enums.NodeKinds.StructDecl) {
			g.defineStructBody(d)
		}
	}
	for _, tree := range trees {
		g.enter(tree)
		for d := range tree.TopLevelDeclsOfKind(enums.NodeKinds.VarDecl) {
			g.genGlobalVarDecl(d)
		}
	}
	for _, tree := range trees {
		g.enter(tree)
		for d := range tree.TopLevelDeclsOfKind(enums.NodeKinds.FuncDecl) {
			g.declareFuncSignature(d)
		}
		for d := range tree.TopLevelDeclsOfKind(enums.NodeKinds.StructDecl) {
			for ctor := range tree.StructConstructors(d) {
				g.declareConstructorSignature(ctor)
			}
		}
	}
	// genGlobalCtors (globalinit.go) needs every function/constructor
	// signature already declared (a non-constant global initializer may call
	// one - see LANGUAGE.md's "Global var initializers" section) but runs
	// before any function/constructor body is generated below - its own
	// synthesized init function is just one more function as far as the rest
	// of this package is concerned, and ordering it here keeps every one of
	// genPackage's own passes in the same "declare everything, then generate
	// every body" shape.
	g.genGlobalCtors()
	for _, tree := range trees {
		g.enter(tree)
		for d := range tree.TopLevelDeclsOfKind(enums.NodeKinds.FuncDecl) {
			g.genFuncBody(d)
		}
		for d := range tree.TopLevelDeclsOfKind(enums.NodeKinds.StructDecl) {
			for ctor := range tree.StructConstructors(d) {
				g.genConstructorBody(ctor)
			}
		}
	}
}

// errorAt records a codegen-level diagnostic at n's position - see the
// package doc comment for what still gets a real diagnostic instead of a
// panic (non-constant global initializers, unsupported print argument
// types) versus what's assumed impossible on a validated tree.
func (g *Generator) errorAt(n ast.NodeIndex, format string, a ...any) {
	span := g.tree.SpanOf(n)
	g.diags.ErrorfSpan(span.Start, span.End, format, a...)
}

// declareStructType creates decl's named (but still empty/opaque) LLVM
// struct type - split from defineStructBody into its own pass so two
// mutually-referencing structs (A has a field of type B, B has a field of
// type A's... well, arrays/values of each other) can each find the other
// struct's type handle already created, regardless of declaration order.
func (g *Generator) declareStructType(decl ast.NodeIndex) {
	nameNode := g.tree.Child(decl, 0)
	name := g.tree.Text(nameNode)
	info := g.info.Structs[name]

	g.structLayouts[info] = &structLayout{
		llvmType:   g.ctx.StructCreateNamed(name),
		fieldIndex: make(map[*sema.Symbol]int),
	}
}

// defineStructBody fills in decl's struct type body, once every struct's
// named (opaque) type already exists - see declareStructType.
func (g *Generator) defineStructBody(decl ast.NodeIndex) {
	nameNode := g.tree.Child(decl, 0)
	info := g.info.Structs[g.tree.Text(nameNode)]
	layout := g.structLayouts[info]

	fieldNodes := g.tree.StructFields(decl)
	fieldTypes := make([]llvm.Type, len(fieldNodes))
	fieldSemaTypes := make([]sema.Type, len(fieldNodes))
	for i, fieldNode := range fieldNodes {
		fieldNameNode := g.tree.Child(fieldNode, 0)
		fieldTypeNode := g.tree.Child(fieldNode, 1)
		sym := g.info.Refs[fieldNameNode]

		fieldSemaTypes[i] = g.info.Types[fieldTypeNode]
		fieldTypes[i] = g.llvmType(fieldSemaTypes[i])
		layout.fieldIndex[sym] = i
	}
	layout.fieldTypes = fieldTypes
	layout.fieldSemaTypes = fieldSemaTypes
	layout.llvmType.StructSetBody(fieldTypes, false)
}

// structLitFieldSlot resolves one struct composite-literal element - elems[i]
// in declaration order, e itself, keyed reporting whether the whole literal
// is keyed (see ast.Tree.IsKeyedElement) - to the field index it fills and
// the value expression node that supplies it: a positional element fills
// field i directly from e itself; a keyed element's field comes from its own
// key (already resolved by sema - see Info.Refs), with i unused, and its
// value from the KeyValueExpr's own value child. Shared by constfold.go's
// constCompositeLit and expr.go's genCompositeLitInto - the one difference
// left between those two callers (building a []llvm.Value vs storing into an
// already-addressed destination) is all that remains once this element-to-
// field mapping is factored out into one place.
func (g *Generator) structLitFieldSlot(layout *structLayout, e ast.NodeIndex, i int, keyed bool) (fieldIndex int, valueNode ast.NodeIndex) {
	if !keyed {
		return i, e
	}
	sym := g.info.Refs[g.tree.Child(e, 0)]
	return layout.fieldIndex[sym], g.tree.Child(e, 1)
}

// globalInitEntry is one non-constant top-level `var`'s initializer, queued
// by genGlobalVarDecl (Generator.globalInits) for genGlobalCtors
// (globalinit.go) to lower later, once every global/function/constructor
// signature in the whole package already exists. tree is recorded alongside
// glob/initNode (rather than assuming whatever tree is currently entered)
// since globalInits accumulates across every file in the package before
// genGlobalCtors ever consumes it - initNode is only ever meaningful relative
// to the one Tree it came from (see ast.NodeIndex's doc comment), so
// genGlobalCtors must re-enter the right tree before generating each entry.
type globalInitEntry struct {
	tree     *ast.Tree
	glob     llvm.Value
	initNode ast.NodeIndex
}

// genGlobalVarDecl lowers one top-level `var` into a real LLVM global, always
// given a zero-value initializer up front (matching Go's own zero-value
// convention for a global whose real initializer hasn't run yet - see
// CODEGEN.md's "Global var initializers" section). An initializer that's
// foldable at compile time (isConstFoldable, constfold.go) is folded
// immediately, overwriting that zero initializer with the real constant - the
// exact same behavior this function has always had, unchanged. Anything else
// keeps the zero initializer and is instead queued into g.globalInits, to be
// lowered as real generated code inside the synthesized init function
// genGlobalCtors builds once every global in the package has been declared
// (globalinit.go) - see LANGUAGE.md's "Global var initializers" section for
// what's now legal there (a function call, a reference to another global, a
// dynamic-array/slice literal, ...) and the declaration-order guarantee this
// queuing relies on.
func (g *Generator) genGlobalVarDecl(decl ast.NodeIndex) {
	nameNode := g.tree.Child(decl, 0)
	initNode := g.tree.Child(decl, 2)
	sym := g.info.Refs[nameNode]

	llt := g.llvmType(g.info.Types[decl])
	glob := llvm.AddGlobal(g.mod, llt, sym.Name)
	glob.SetInitializer(llvm.ConstNull(llt))
	g.globals[sym] = glob

	if initNode == ast.InvalidNode {
		return
	}
	if g.isConstFoldable(initNode) {
		// isConstFoldable already guarantees constExpr won't hit its "not a
		// constant at all" default cases - only a genuinely-erroneous
		// constant expression (division by zero, an out-of-range literal)
		// can still fail here, in which case the zero initializer already in
		// place is exactly the same recovery this package has always used.
		if v, ok := g.constExpr(initNode); ok {
			glob.SetInitializer(v)
		}
		return
	}

	g.globalInits = append(g.globalInits, globalInitEntry{
		tree:     g.tree,
		glob:     glob,
		initNode: initNode,
	})
}
