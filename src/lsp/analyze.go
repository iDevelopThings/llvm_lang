package lsp

import (
	"llvm_lang/src/ast"
	"llvm_lang/src/diag"
	"llvm_lang/src/loader"
	"llvm_lang/src/sema"

	protocol "github.com/tliron/glsp/protocol_3_16"
)

// FileAnalysis is one file's own frontend result within a just-recomputed
// program: its Tree (always present, even on a parse error), its Info
// (nil if analysis never reached name resolution/type checking - a parse
// error anywhere in the program stops the pipeline before Resolve/Check
// ever run, mirroring src/compiler's own "don't resolve/check a
// structurally broken tree" rule), and its merged parse+resolve+check
// diagnostics.
type FileAnalysis struct {
	Tree  *ast.Tree
	Info  *sema.Info
	Diags *diag.Bag
}

// ProtocolDiagnostics translates fa's own diagnostics into LSP form - see
// toProtocolDiagnostics.
func (fa *FileAnalysis) ProtocolDiagnostics() []protocol.Diagnostic {
	return toProtocolDiagnostics(fa.Tree.File, fa.Diags)
}

// analyzeProgram runs this compiler's sema frontend (Resolve then Check)
// over prog - already loaded and parsed by loader.LoadProgram, which lexes/
// parses every file itself (see loader.Package.Files' own Tree/Diags) - and
// returns one fileAnalysis per file, keyed by that file's own path.
//
// TODO(lsp): this mirrors src/compiler.CompileProgram's own
// loader.Program -> sema.PackageUnit translation (compiler.go) almost
// exactly, minus the codegen/LLVM tail this package deliberately never runs
// (an LSP only ever needs diagnostics/Info, never a real compiled module -
// see doc.go). Kept as its own small, separate implementation here rather
// than factoring a shared helper out of src/compiler, since src/compiler is
// off-limits to touch this round (see doc.go's scope-constraint note) -
// worth deduplicating once core packages are safe to touch again.
func analyzeProgram(prog *loader.Program) map[string]*FileAnalysis {
	var trees []*ast.Tree
	diags := make(map[*ast.Tree]*diag.Bag)
	paths := make(map[*ast.Tree]string)
	units := make([]*sema.PackageUnit, 0, len(prog.Order))
	anyParseErrors := false

	for _, pkg := range prog.Order {
		unitTrees := make([]*ast.Tree, len(pkg.Files))
		var fileImports map[*ast.Tree][]sema.FileImport

		for i, f := range pkg.Files {
			unitTrees[i] = f.Tree
			trees = append(trees, f.Tree)
			paths[f.Tree] = f.Name
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

	out := make(map[string]*FileAnalysis, len(trees))

	// A structurally broken tree isn't safe to hand to Resolve/Check (see
	// src/compiler.CompilePackage/CompileProgram's own identical guard) -
	// every open document still gets back its own parse diagnostics, just
	// no Info (hover/definition/semantic-tokens degrade to "nothing
	// resolved" rather than risk analyzing a malformed tree).
	if anyParseErrors {
		for _, tree := range trees {
			out[paths[tree]] = &FileAnalysis{
				Tree:  tree,
				Diags: diags[tree],
			}
		}
		return out
	}

	infos, rdiags, _, treePackage := sema.ResolveProgram(units)
	if mergeDiags(diags, trees, rdiags) {
		// A resolve-time error means at least one Ref/Scope entry Check
		// depends on is missing or wrong for some tree in this program -
		// src/compiler.finishPipeline never calls CheckProgram in this case
		// either (it stops and returns right after merging rdiags), and
		// sema.Check's own doc comment says it "uses the name/scope
		// resolution info already computed by Resolve" - i.e. assumes
		// Resolve succeeded. Mirror that guard exactly rather than handing
		// Check a resolve result it was never written to tolerate.
		for _, tree := range trees {
			out[paths[tree]] = &FileAnalysis{
				Tree:  tree,
				Diags: diags[tree],
			}
		}
		return out
	}

	cdiags := sema.CheckProgram(trees, infos, treePackage)
	mergeDiags(diags, trees, cdiags)

	for _, tree := range trees {
		out[paths[tree]] = &FileAnalysis{
			Tree:  tree,
			Info:  infos[tree],
			Diags: diags[tree],
		}
	}
	return out
}

// mergeDiags appends every diagnostic in src into dst's matching per-tree
// bag, preserving position/span/label/severity - the same merge src/compiler
// (mergeStage/mergeBag) uses to combine diagnostics across pipeline stages
// into one bag per tree - and reports whether any tree had an error-severity
// diagnostic at this stage, exactly like mergeStage's own bool return
// (compiler.go), which analyzeProgram uses to decide whether it's safe to
// proceed to the next stage.
func mergeDiags(dst map[*ast.Tree]*diag.Bag, trees []*ast.Tree, src map[*ast.Tree]*diag.Bag) bool {
	hasErrors := false
	for _, tree := range trees {
		d, ok := dst[tree]
		if !ok {
			d = diag.NewBag()
			dst[tree] = d
		}
		s := src[tree]
		if s.HasErrors() {
			hasErrors = true
		}
		for e := range s.Seq() {
			if e.Severity == diag.SeverityWarning {
				d.WarnfLabel(e.Pos, e.End, e.Label, "%s", e.Msg)
			} else {
				d.ErrorfLabel(e.Pos, e.End, e.Label, "%s", e.Msg)
			}
		}
	}
	return hasErrors
}
