// Package compiler drives an already-loaded llvm_lang program or package
// (see src/loader) through the rest of the compiler pipeline - lexer/parser
// output is turned into a resolved, type-checked, code-generated, and (by
// default) optimized LLVM module: sema.ResolvePackage/ResolveProgram ->
// sema.CheckProgram -> codegen.GeneratePackage -> LLVM's own module verifier
// -> building the host target machine -> (when the caller's own optimize
// parameter is true) running LLVM's "default<O2>" optimization pipeline -
// stopping at the first stage that reports an error-severity diagnostic in
// any file. See CompilePackage/CompileProgram's own doc comments for the
// optimize parameter, and CODEGEN.md's "Optimization pipeline" section for
// the full design.
//
// This is the layer directly above src/loader in this project's own
// architecture: loader owns "given a path, discover/parse/resolve the
// file/package/program structure" (pure I/O + discovery); compiler owns
// "given that loaded structure, actually run it through the semantic/
// codegen pipeline". It is pure orchestration - calling the existing
// lexer/parser/sema/codegen entry points in the right order, in the right
// shape - never a place to reimplement anything those packages already do
// (see AGENTS.md's Architecture section).
//
// Deliberately CLI-agnostic: no io.Writer/stderr, no exit codes, no flag
// handling. A caller (cmd/llvmc today; potentially a test harness or a
// future LSP) gets back a Result and decides for itself how to print
// diagnostics or what process exit code to use.
package compiler

import (
	"fmt"
	"sync"

	"llvm_lang/src/ast"
	"llvm_lang/src/codegen"
	"llvm_lang/src/diag"
	"llvm_lang/src/enums"
	"llvm_lang/src/frontend"
	"llvm_lang/src/lexer"
	"llvm_lang/src/loader"
	"llvm_lang/src/parser"
	"llvm_lang/src/sema"

	"tinygo.org/x/go-llvm"
)

// optimizationPipeline is the pass-pipeline string handed to
// llvm.Module.RunPasses (see finishPipeline) whenever a caller asks for
// optimization - the standard, well-balanced "opt -passes=default<O2>"
// pipeline (inlining, mem2reg, GVN, LICM, DCE, ...), the same one clang uses
// at -O2. Deliberately not default<O3> (more aggressive/riskier - larger
// code, longer compiles, occasionally exposes UB-sensitive miscompiles) or
// default<Os>/default<Oz> (size-optimized, not this project's goal) - see
// DECISIONS.md's dated entry for the full "why O2" writeup.
const optimizationPipeline = "default<O2>"

// Result is the outcome of compiling a program (see CompileProgram) or a
// single import-less package's file list (see CompilePackage) through the
// full pipeline.
//
// Module is nil whenever compilation failed at any stage - either a real
// per-source-position diagnostic (check Diags) or, more rarely, the
// generated module itself failing LLVM's own verifier (check VerifyErr) -
// a caller should always check Module first, not infer success from Diags
// being empty.
type Result struct {
	// Trees is every file's parsed tree, in the exact order Diags/codegen
	// processed them - a caller iterates this (not Diags directly, which is
	// an unordered map) to print diagnostics in a stable order.
	Trees []*ast.Tree
	// Diags holds every tree's own diagnostics, merged across every stage
	// that actually ran (parsing, resolving, checking, codegen) - every tree
	// passed to CompilePackage/CompileProgram has an entry here, even if
	// empty. A stage that never ran (because an earlier one already stopped
	// the pipeline) simply contributes nothing further.
	Diags map[*ast.Tree]*diag.Bag
	// Module is the generated, verified LLVM module - nil on any failure.
	// The caller owns disposal (Module.Dispose(), or handing it to
	// llvm.NewLLJIT, whichever it needs).
	Module *codegen.Module
	// TargetMachine is the host target machine built once inside
	// finishPipeline - only meaningful when Module != nil (never built at
	// all on a failed compile). Used internally to run the optimization
	// pipeline (see the optimize parameter on CompilePackage/CompileProgram)
	// and handed back here so a caller with its own further use for one (the
	// -o AOT path's own object-code emission, cmd/llvmc's
	// compileToExecutable) doesn't need to build a second, separate one.
	// Caller-owned disposal, exactly like Module: TargetMachine.Dispose()
	// exactly once, whenever every consumer that might still need it (an AOT
	// path's own EmitToMemoryBuffer call included) is done with it - never
	// before, never twice.
	TargetMachine llvm.TargetMachine
	// VerifyErr is set only when codegen itself reported no diagnostics but
	// the generated module still failed LLVM's own verifier, or a later
	// infrastructure-level step past that point (building the host target
	// machine, or - when optimize is true - running the optimization
	// pipeline) failed - none of these are attributable to any source
	// position, hence never a Diags entry. Module is nil whenever this is
	// set.
	VerifyErr error
}

// CompilePackage compiles files (see loader.SourceFile) as one package with
// no imports of its own (see LANGUAGE.md's "Multi-file packages" section):
// lexer.NewFile -> parser.ParseFile per file, then sema.ResolvePackage ->
// sema.CheckProgram (merging/stopping on error at each stage via
// frontend.MergeStage - see src/frontend's own package doc comment for why
// that piece lives there, shared with src/lsp), then the shared codegen
// tail (finishPipeline). This is the flat-file-list case an in-process
// caller (a test, or a future tool) reaches for when it has source text in
// hand but no real filesystem/loader.Program to build one from.
//
// optimize threads straight through to finishPipeline's own RunPasses call -
// see its doc comment for what that actually does.
func CompilePackage(files []loader.SourceFile, optimize bool) *Result {
	trees := make([]*ast.Tree, len(files))
	diags := make(map[*ast.Tree]*diag.Bag, len(files))
	anyParseErrors := false

	for i, f := range files {
		lf := lexer.NewFile(f.Name, f.Src)
		tree, pdiags := parser.ParseFile(lf)
		diags[tree] = pdiags
		if pdiags.HasErrors() {
			anyParseErrors = true
		}
		trees[i] = tree
	}
	if anyParseErrors {
		return &Result{
			Trees: trees,
			Diags: diags,
		}
	}

	infos, rdiags := sema.ResolvePackage(trees)
	if frontend.MergeStage(diags, trees, rdiags) {
		return &Result{
			Trees: trees,
			Diags: diags,
		}
	}

	// treePackage is nil - a single package has no cross-package export
	// enforcement to do at all (see sema.CheckProgram's own doc comment).
	cdiags := sema.CheckProgram(trees, infos, nil)
	if frontend.MergeStage(diags, trees, cdiags) {
		return &Result{
			Trees: trees,
			Diags: diags,
		}
	}

	// moduleName matches this driver's own single-file convention of using
	// the compiled path as the module's name - the package's first file is
	// as reasonable a choice as any for a multi-file package.
	return finishPipeline(trees, diags, infos, files[0].Name, optimize)
}

// CompileProgram compiles prog - potentially spanning several packages
// linked by `import` declarations (see loader.LoadProgram, which discovers/
// parses/dedups/cycle-checks the entire transitive import graph up front -
// every file's own parse diagnostics were already collected then, not here)
// - into one shared LLVM module.
//
// The resolve/check sequence itself (frontend.RunProgram) is shared with
// src/lsp, which needs the identical result minus this function's own
// codegen tail - see src/frontend's own package doc comment. Every
// package's trees are then driven through the exact same finishPipeline
// codegen tail CompilePackage shares - see CODEGEN.md's "Multi-file
// packages" section for why one shared Module needs no package-boundary
// awareness at all.
//
// optimize threads straight through to finishPipeline's own RunPasses call -
// see its doc comment for what that actually does.
func CompileProgram(prog *loader.Program, optimize bool) *Result {
	fe := frontend.RunProgram(prog)
	if fe.HasErrors {
		return &Result{
			Trees: fe.Trees,
			Diags: fe.Diags,
		}
	}

	// moduleName matches CompilePackage's own convention: the entry
	// package's first file is as reasonable a choice as any for the whole
	// program's module name.
	moduleName := prog.Entry.Files[0].Name
	return finishPipeline(fe.Trees, fe.Diags, fe.Infos, moduleName, optimize)
}

// finishPipeline drives the codegen tail of the pipeline once every tree
// has already been parsed, resolved, AND type-checked (diags already holds
// each tree's own merged parse+resolve+check diagnostics, with both callers
// above guaranteeing zero errors at any of those stages before ever
// reaching here): codegen.GeneratePackage, merge + stop on any codegen
// error; otherwise LLVM's own module verifier; otherwise build the host
// target machine (buildTargetMachine) and, when optimize is true, run
// optimizationPipeline ("default<O2>") over the verified module via
// llvm.Module.RunPasses before ever handing it back.
//
// This is the one place in the whole pipeline any of these three JIT/
// -emit-llvm/-o consumption paths ever runs, so every one of them uniformly
// sees the same optimized (or, with optimize false, exactly as-generated)
// module - see CODEGEN.md's "Optimization pipeline" section for the full
// design and why it lives here rather than duplicated per caller.
//
// optimize false does not run "default<O0>" - RunPasses is skipped
// entirely, so today's pre-optimization behavior is restored byte-for-byte,
// useful for isolating whether a bug lives in codegen itself or was
// introduced by an optimization pass.
// checkNoOptAsyncRestriction rejects any use of coroutine codegen machinery
// when optimize is false (see finishPipeline's own caller, cmd/llvmc's
// `-no-opt` flag) - llvm.coro.* intrinsics are only ever lowered into real
// code by the optimization pipeline's own coroutine-splitting passes (see
// CODEGEN.md's "Coroutines" section), so skipping RunPasses entirely
// (exactly what optimize=false does) crashes LLVM's own instruction
// selection outright rather than silently miscompiling. Two independent
// triggers, mirroring codegen.programUsesCoroutines exactly (see its own
// doc comment): declaring an `async func`, and the `coroutine` type keyword
// appearing in any var/field/param declaration - a coroutine-typed
// declaration with no async func anywhere still reaches setupCoroutines at
// codegen time regardless.
// firstCoroutineTypedNode returns the lowest-indexed node in info.Types
// whose type is TypeCoroutine, for a deterministic (not map-iteration-order-
// dependent) diagnostic span - which specific node wins doesn't matter
// semantically (see checkNoOptAsyncRestriction's own doc comment), only that
// repeated compiles of the same source report the same span.
func firstCoroutineTypedNode(info *sema.Info) (ast.NodeIndex, bool) {
	best := ast.InvalidNode
	for n, t := range info.Types {
		if t.Kind != sema.TypeCoroutine {
			continue
		}
		if best == ast.InvalidNode || n < best {
			best = n
		}
	}
	return best, best != ast.InvalidNode
}

func checkNoOptAsyncRestriction(trees []*ast.Tree, infos map[*ast.Tree]*sema.Info) map[*ast.Tree]*diag.Bag {
	stageDiags := make(map[*ast.Tree]*diag.Bag, len(trees))
	for _, tree := range trees {
		bag := diag.NewBag()
		for decl := range tree.TopLevelDeclsOfKind(enums.NodeKinds.FuncDecl) {
			if !tree.FuncIsAsync(decl) {
				continue
			}
			span := tree.SpanOf(decl)
			bag.ErrorfSpan(span.Start, span.End, "async functions require the optimization pipeline (llvm.coro.* intrinsics are only lowered by it) - cannot compile with -no-opt")
		}
		if n, ok := firstCoroutineTypedNode(infos[tree]); ok {
			// One diagnostic per tree, not one per matching node - a `var h
			// coroutine` declaration records TypeCoroutine against BOTH its
			// own VarDecl node and its type-annotation Ident (for LSP hover
			// on the type name), and enumerating every declaration-site node
			// kind here to dedupe would be fragile; a single "this program
			// uses coroutine typing somewhere" signal is all -no-opt's own
			// caller needs to fix their build flags.
			span := tree.SpanOf(n)
			bag.ErrorfSpan(span.Start, span.End, "a coroutine-typed declaration requires the optimization pipeline (llvm.coro.* intrinsics are only lowered by it) - cannot compile with -no-opt")
		}
		stageDiags[tree] = bag
	}
	return stageDiags
}

func finishPipeline(trees []*ast.Tree, diags map[*ast.Tree]*diag.Bag, infos map[*ast.Tree]*sema.Info, moduleName string, optimize bool) *Result {
	if !optimize {
		if hasErrors := frontend.MergeStage(diags, trees, checkNoOptAsyncRestriction(trees, infos)); hasErrors {
			return &Result{
				Trees: trees,
				Diags: diags,
			}
		}
	}

	mod, gdiags := codegen.GeneratePackage(trees, infos, moduleName)
	if frontend.MergeStage(diags, trees, gdiags) {
		mod.Dispose()
		return &Result{
			Trees: trees,
			Diags: diags,
		}
	}

	// llvm.ReturnStatusAction, not llvm.PrintMessageAction: this package's
	// own doc comment promises CLI-agnostic behavior (no io.Writer, no
	// printed side effects - a caller decides how to print, if at all).
	// PrintMessageAction would make LLVM itself write straight to the real
	// OS stderr here, bypassing that promise - every other verifying call
	// site in this codebase (codegen_test.go, imports_test.go,
	// multifile_test.go) already uses ReturnStatusAction for exactly this
	// reason. VerifyErr below already carries the error as a return value;
	// cmd/llvmc's own finish is what actually prints it.
	if err := llvm.VerifyModule(mod.LLVM, llvm.ReturnStatusAction); err != nil {
		mod.Dispose()
		return &Result{
			Trees:     trees,
			Diags:     diags,
			VerifyErr: fmt.Errorf("module verification failed: %w", err),
		}
	}

	// Built unconditionally (even when optimize is false) - a caller's own
	// further use for one (cmd/llvmc's -o AOT path) needs it regardless of
	// whether optimization actually ran, and building one here once is
	// exactly what lets that path stop building a second, separate one of
	// its own (see Result.TargetMachine's own doc comment).
	tm, err := buildTargetMachine()
	if err != nil {
		mod.Dispose()
		return &Result{
			Trees:     trees,
			Diags:     diags,
			VerifyErr: fmt.Errorf("building target machine: %w", err),
		}
	}

	if optimize {
		pbo := llvm.NewPassBuilderOptions()
		err := mod.LLVM.RunPasses(optimizationPipeline, tm, pbo)
		pbo.Dispose()
		if err != nil {
			mod.Dispose()
			tm.Dispose()
			return &Result{
				Trees:     trees,
				Diags:     diags,
				VerifyErr: fmt.Errorf("running optimization passes: %w", err),
			}
		}
	}

	return &Result{
		Trees:         trees,
		Diags:         diags,
		Module:        mod,
		TargetMachine: tm,
	}
}

// initNativeTargetOnce/initNativeTargetErr guard llvm.InitializeNativeTarget/
// llvm.InitializeNativeAsmPrinter - process-global LLVM setup meant to run at
// most once per process (mirroring cmd/llvmc/main.go's own initJIT and
// src/codegen/codegen_test.go's own jitInit, each independently needing the
// exact same pair of calls before touching a host target at all) - now
// needed here too, since buildTargetMachine (called unconditionally by
// finishPipeline, on every successful compile) is the first place in this
// package that ever resolves a real host Target/TargetMachine.
var (
	initNativeTargetOnce sync.Once
	initNativeTargetErr  error
)

// buildTargetMachine resolves this host's own target machine
// (llvm.DefaultTargetTriple -> llvm.GetTargetFromTriple ->
// Target.CreateTargetMachine, the same three calls cmd/llvmc/main.go's own
// compileToExecutable used to make on its own before this round - see
// CODEGEN.md's "Optimization pipeline" section) - used both to drive
// RunPasses (when optimize is true) and hand back via Result.TargetMachine
// for the -o AOT path's own object-code emission.
func buildTargetMachine() (llvm.TargetMachine, error) {
	initNativeTargetOnce.Do(func() {
		if err := llvm.InitializeNativeTarget(); err != nil {
			initNativeTargetErr = err
			return
		}
		initNativeTargetErr = llvm.InitializeNativeAsmPrinter()
	})
	if initNativeTargetErr != nil {
		return llvm.TargetMachine{}, initNativeTargetErr
	}

	triple := llvm.DefaultTargetTriple()
	target, err := llvm.GetTargetFromTriple(triple)
	if err != nil {
		return llvm.TargetMachine{}, err
	}

	return target.CreateTargetMachine(triple, "", "", llvm.CodeGenLevelDefault, llvm.RelocDefault, llvm.CodeModelDefault), nil
}
