// Package compiler drives an already-loaded llvm_lang program or package
// (see src/loader) through the rest of the compiler pipeline - lexer/parser
// output is turned into a resolved, type-checked, and code-generated LLVM
// module: sema.ResolvePackage/ResolveProgram -> sema.CheckProgram ->
// codegen.GeneratePackage -> LLVM's own module verifier - stopping at the
// first stage that reports an error-severity diagnostic in any file.
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

	"llvm_lang/src/ast"
	"llvm_lang/src/codegen"
	"llvm_lang/src/diag"
	"llvm_lang/src/lexer"
	"llvm_lang/src/loader"
	"llvm_lang/src/parser"
	"llvm_lang/src/sema"

	"tinygo.org/x/go-llvm"
)

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
	// VerifyErr is set only when codegen itself reported no diagnostics but
	// the generated module still failed LLVM's own verifier - a real bug in
	// codegen's own lowering, not attributable to any source position, hence
	// not a Diags entry. Module is nil whenever this is set.
	VerifyErr error
}

// CompilePackage compiles files (see loader.SourceFile) as one package with
// no imports of its own (see LANGUAGE.md's "Multi-file packages" section):
// lexer.NewFile -> parser.ParseFile per file, then sema.ResolvePackage ->
// the shared pipeline tail (finishPipeline). This is the flat-file-list case
// an in-process caller (a test, or a future tool) reaches for when it has
// source text in hand but no real filesystem/loader.Program to build one
// from.
func CompilePackage(files []loader.SourceFile) *Result {
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
	// moduleName matches this driver's own single-file convention of using
	// the compiled path as the module's name - the package's first file is
	// as reasonable a choice as any for a multi-file package. treePackage is
	// nil - a single package has no cross-package export enforcement to do
	// at all (see sema.CheckProgram's own doc comment).
	return finishPipeline(trees, diags, infos, rdiags, nil, files[0].Name)
}

// CompileProgram compiles prog - potentially spanning several packages
// linked by `import` declarations (see loader.LoadProgram, which discovers/
// parses/dedups/cycle-checks the entire transitive import graph up front -
// every file's own parse diagnostics were already collected then, not here)
// - into one shared LLVM module.
//
// prog.Order already lists every package in dependency order (see its own
// doc comment), which is exactly the order sema.ResolveProgram needs: each
// package's own PackageUnit is built and resolved before any package that
// imports it, so a FileImport's TargetKey (this driver uses each package's
// own resolved Dir as that key) always names an already-resolved unit.
//
// Every package's trees are then flattened into one slice and driven
// through the exact same finishPipeline tail CompilePackage shares - see
// CODEGEN.md's "Multi-file packages" section for why one shared Module
// needs no package-boundary awareness at all, the same reasoning extended
// one level up unchanged.
func CompileProgram(prog *loader.Program) *Result {
	var trees []*ast.Tree
	diags := make(map[*ast.Tree]*diag.Bag)
	units := make([]*sema.PackageUnit, 0, len(prog.Order))
	anyParseErrors := false

	for _, pkg := range prog.Order {
		unitTrees := make([]*ast.Tree, len(pkg.Files))
		var fileImports map[*ast.Tree][]sema.FileImport

		for i, f := range pkg.Files {
			unitTrees[i] = f.Tree
			trees = append(trees, f.Tree)
			diags[f.Tree] = f.Diags
			if f.Diags.HasErrors() {
				anyParseErrors = true
			}

			if len(f.Imports) == 0 {
				continue
			}
			if fileImports == nil {
				fileImports = make(map[*ast.Tree][]sema.FileImport, len(pkg.Files))
			}
			imps := make([]sema.FileImport, len(f.Imports))
			for j, imp := range f.Imports {
				imps[j] = sema.FileImport{
					LocalName: imp.LocalName,
					TargetKey: imp.Package.Dir,
				}
			}
			fileImports[f.Tree] = imps
		}

		units = append(units, &sema.PackageUnit{
			Key:         pkg.Dir,
			Name:        pkg.Name,
			Trees:       unitTrees,
			FileImports: fileImports,
		})
	}
	if anyParseErrors {
		return &Result{
			Trees: trees,
			Diags: diags,
		}
	}

	infos, rdiags, _, treePackage := sema.ResolveProgram(units)
	// moduleName matches CompilePackage's own convention: the entry
	// package's first file is as reasonable a choice as any for the whole
	// program's module name.
	moduleName := prog.Entry.Files[0].Name
	return finishPipeline(trees, diags, infos, rdiags, treePackage, moduleName)
}

// finishPipeline drives the shared tail of the pipeline once every tree has
// already been parsed (diags already holds each tree's own parse
// diagnostics, with zero parse errors guaranteed by both callers above) and
// resolved (infos/rdiags, from whichever of sema.ResolvePackage/
// ResolveProgram the caller used): merge rdiags into diags and stop on any
// resolve error; otherwise sema.CheckProgram, merge + stop on any check
// error; otherwise codegen.GeneratePackage, merge + stop on any codegen
// error; otherwise LLVM's own module verifier.
func finishPipeline(trees []*ast.Tree, diags map[*ast.Tree]*diag.Bag, infos map[*ast.Tree]*sema.Info, rdiags map[*ast.Tree]*diag.Bag, treePackage map[*ast.Tree]*sema.Scope, moduleName string) *Result {
	if mergeStage(diags, trees, rdiags) {
		return &Result{
			Trees: trees,
			Diags: diags,
		}
	}

	cdiags := sema.CheckProgram(trees, infos, treePackage)
	if mergeStage(diags, trees, cdiags) {
		return &Result{
			Trees: trees,
			Diags: diags,
		}
	}

	mod, gdiags := codegen.GeneratePackage(trees, infos, moduleName)
	if mergeStage(diags, trees, gdiags) {
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

	return &Result{
		Trees:  trees,
		Diags:  diags,
		Module: mod,
	}
}

// mergeStage merges each tree's bag from stageDiags into diags (see
// mergeBag) and reports whether any tree had an error-severity diagnostic at
// this stage - the caller stops the pipeline right there if so, exactly like
// every stage boundary in this pipeline always has.
func mergeStage(diags map[*ast.Tree]*diag.Bag, trees []*ast.Tree, stageDiags map[*ast.Tree]*diag.Bag) bool {
	hasErrors := false
	for _, tree := range trees {
		dst, ok := diags[tree]
		if !ok {
			dst = diag.NewBag()
			diags[tree] = dst
		}
		src := stageDiags[tree]
		mergeBag(dst, src)
		if src.HasErrors() {
			hasErrors = true
		}
	}
	return hasErrors
}

// mergeBag appends every diagnostic in src into dst, preserving each one's
// original position/span, label, message, and severity - used to combine
// diagnostics collected across pipeline stages into the one bag per tree
// Result.Diags exposes, since diag.Bag has no direct "merge" of its own.
// Always goes through the *Label variants (with Label simply "" for a
// diagnostic that never had one) so a span/label produced by an earlier
// stage survives the merge instead of collapsing back to a single-point
// caret. Uses src.Seq() rather than src.All() - this only ever ranges over
// src once and discards it, so there's no need for All()'s defensive copy.
func mergeBag(dst *diag.Bag, src *diag.Bag) {
	for d := range src.Seq() {
		if d.Severity == diag.SeverityWarning {
			dst.WarnfLabel(d.Pos, d.End, d.Label, "%s", d.Msg)
		} else {
			dst.ErrorfLabel(d.Pos, d.End, d.Label, "%s", d.Msg)
		}
	}
}
