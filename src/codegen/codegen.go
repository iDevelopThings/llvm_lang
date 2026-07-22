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
// The one class of error still possible on a semantically valid tree is a
// codegen-level restriction sema doesn't know about (see BLOCKERS.md): a
// non-constant top-level var initializer. That lands in the affected file's
// own diag.Bag rather than panicking, so a caller gets it reported, same as
// every other pass in this compiler.
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
// about the function currently being built. Functions never nest in this
// grammar (no closures, no nested func literals), so a single field on
// Generator holds it, mirroring sema/typecheck.go's enclosingFunc.
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

	structLayouts map[*sema.StructInfo]*structLayout
	globals       map[*sema.Symbol]llvm.Value

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
	// generated inside that function's body.
	curFn       llvm.Value
	entryBlock  llvm.BasicBlock
	curFunc     *funcCtx
	loopStack   []loopCtx
	curReceiver llvm.Value

	// Runtime externs and cached format-string globals - see runtime.go.
	printfType llvm.Type
	printfFn   llvm.Value
	mallocType llvm.Type
	mallocFn   llvm.Value
	memcpyType llvm.Type
	memcpyFn   llvm.Value
	memcmpType llvm.Type
	memcmpFn   llvm.Value
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
		strLiterals:   make(map[string]llvm.Value),
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
// then globals, then function signatures, then bodies - each pass covering
// every file before the next begins) so declaration order never matters,
// either within one file or across the whole package: a function can call
// another declared later (in the same file or a different one), and a
// global's type can name a struct declared later (ditto).
func (g *Generator) genPackage(trees []*ast.Tree) {
	for _, tree := range trees {
		g.enter(tree)
		for _, d := range tree.Children(tree.Root) {
			if tree.Nodes[d].Kind == enums.NodeKinds.StructDecl {
				g.declareStructType(d)
			}
		}
	}
	for _, tree := range trees {
		g.enter(tree)
		for _, d := range tree.Children(tree.Root) {
			if tree.Nodes[d].Kind == enums.NodeKinds.StructDecl {
				g.defineStructBody(d)
			}
		}
	}
	for _, tree := range trees {
		g.enter(tree)
		for _, d := range tree.Children(tree.Root) {
			if tree.Nodes[d].Kind == enums.NodeKinds.VarDecl {
				g.genGlobalVarDecl(d)
			}
		}
	}
	for _, tree := range trees {
		g.enter(tree)
		for _, d := range tree.Children(tree.Root) {
			if tree.Nodes[d].Kind == enums.NodeKinds.FuncDecl {
				g.declareFuncSignature(d)
			}
		}
	}
	for _, tree := range trees {
		g.enter(tree)
		for _, d := range tree.Children(tree.Root) {
			if tree.Nodes[d].Kind == enums.NodeKinds.FuncDecl {
				g.genFuncBody(d)
			}
		}
	}
}

// errorAt records a codegen-level diagnostic at n's position - see the
// package doc comment for what still gets a real diagnostic instead of a
// panic (non-constant global initializers, unsupported print argument
// types) versus what's assumed impossible on a validated tree.
func (g *Generator) errorAt(n ast.NodeIndex, format string, a ...any) {
	g.diags.Errorf(g.tree.SpanOf(n).Start, format, a...)
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

	fieldNodes := g.tree.Children(decl)[1:]
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

// genGlobalVarDecl lowers one top-level `var` into a real LLVM global. See
// AGENTS.md's codegen section for the decision this encodes: a global's
// initializer must be a compile-time constant expression (constExpr, in
// constfold.go) - there's no synthesized Go-style init-routine-before-main
// in this language. A non-constant initializer is a codegen-level error
// (reported, not panicked - see the package doc comment); codegen recovers
// by zero-initializing the global and moving on, so one bad global doesn't
// stop every other diagnostic from surfacing.
func (g *Generator) genGlobalVarDecl(decl ast.NodeIndex) {
	nameNode := g.tree.Child(decl, 0)
	initNode := g.tree.Child(decl, 2)
	sym := g.info.Refs[nameNode]

	llt := g.llvmType(g.info.Types[decl])
	glob := llvm.AddGlobal(g.mod, llt, sym.Name)
	g.globals[sym] = glob

	if initNode == ast.InvalidNode {
		glob.SetInitializer(llvm.ConstNull(llt))
		return
	}
	if v, ok := g.constExpr(initNode); ok {
		glob.SetInitializer(v)
	} else {
		glob.SetInitializer(llvm.ConstNull(llt))
	}
}
