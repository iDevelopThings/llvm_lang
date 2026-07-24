// Package frontend drives an already-loaded llvm_lang program through name
// resolution and type checking - lexer/parser output (see src/loader) in,
// a resolved and type-checked *sema.Info per file out - stopping short of
// codegen entirely. This is the one piece of src/compiler's own pipeline
// that never touches LLVM/cgo, factored out so src/lsp (which only ever
// needs diagnostics/Info, never a compiled module) can share it without
// pulling in src/codegen/tinygo.org/x/go-llvm at all - see src/lsp's own
// doc comment for why that matters.
//
// src/compiler.CompileProgram is the other caller: it drives the identical
// sequence via RunProgram, then continues into codegen/verify/optimize
// itself whenever RunProgram reports no errors.
package frontend

import (
	"llvm_lang/src/ast"
	"llvm_lang/src/diag"
	"llvm_lang/src/loader"
	"llvm_lang/src/sema"
)

// Result is prog driven through name resolution and type checking as far
// as it got. Diags always holds every tree's own merged parse+resolve+check
// diagnostics, however far the pipeline actually reached. Infos/TreePackage
// are nil whenever HasErrors is true (a parse or resolve error stopped the
// pipeline before CheckProgram ever ran, mirroring sema.CheckProgram's own
// "assumes Resolve succeeded" precondition) - HasErrors is the one signal
// every caller needs to decide whether it's safe to keep going, rather than
// re-deriving it from Diags itself.
type Result struct {
	Trees       []*ast.Tree
	Diags       map[*ast.Tree]*diag.Bag
	Infos       map[*ast.Tree]*sema.Info
	TreePackage map[*ast.Tree]*sema.Scope
	HasErrors   bool
}

// RunProgram drives prog (already lexed/parsed by loader.LoadProgram -
// every file's own parse diagnostics were already collected then, not
// here) through sema.ResolveProgram then, if that succeeds, sema.
// CheckProgram. Every package's own PackageUnit is built and resolved
// before any package that imports it (see prog.Order's own doc comment),
// so a FileImport's TargetKey (this driver uses each package's own resolved
// Dir as that key) always names an already-resolved unit.
func RunProgram(prog *loader.Program) *Result {
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
		return &Result{Trees: trees, Diags: diags, HasErrors: true}
	}

	infos, rdiags, _, treePackage := sema.ResolveProgram(units)
	if MergeStage(diags, trees, rdiags) {
		return &Result{Trees: trees, Diags: diags, HasErrors: true}
	}

	cdiags := sema.CheckProgram(trees, infos, treePackage)
	hasCheckErrors := MergeStage(diags, trees, cdiags)

	return &Result{
		Trees:       trees,
		Diags:       diags,
		Infos:       infos,
		TreePackage: treePackage,
		HasErrors:   hasCheckErrors,
	}
}

// MergeStage appends every diagnostic in stageDiags into diags' matching
// per-tree bag (mergeBag), preserving each one's original position/span/
// label/severity, and reports whether any tree had an error-severity
// diagnostic at this stage - the signal every caller uses to decide
// whether it's safe to proceed to the next pipeline stage.
func MergeStage(diags map[*ast.Tree]*diag.Bag, trees []*ast.Tree, stageDiags map[*ast.Tree]*diag.Bag) bool {
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
func mergeBag(dst, src *diag.Bag) {
	for d := range src.Seq() {
		if d.Severity == diag.SeverityWarning {
			dst.WarnfLabel(d.Pos, d.End, d.Label, "%s", d.Msg)
		} else {
			dst.ErrorfLabel(d.Pos, d.End, d.Label, "%s", d.Msg)
		}
	}
}
