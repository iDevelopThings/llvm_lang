package lsp

import (
	"llvm_lang/src/ast"
	"llvm_lang/src/diag"
	"llvm_lang/src/frontend"
	"llvm_lang/src/loader"
	"llvm_lang/src/sema"

	protocol "github.com/tliron/glsp/protocol_3_16"
)

// FileAnalysis is one file's own frontend result within a just-recomputed
// program: its Tree (always present, even on a parse error), its Info
// (nil if analysis never reached name resolution/type checking - a parse
// or resolve error anywhere in the program stops the pipeline before Check
// ever runs, mirroring src/frontend's own "don't check a tree Resolve
// didn't finish" rule), and its merged parse+resolve+check diagnostics.
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

// analyzeProgram runs prog (already loaded and parsed by loader.LoadProgram)
// through frontend.RunProgram - the same resolve/check sequence
// src/compiler.CompileProgram drives before its own codegen tail, shared
// rather than duplicated here (see src/frontend's own package doc comment
// for why this package specifically needs the cgo-free half of that
// pipeline) - and returns one FileAnalysis per file, keyed by that file's
// own path.
func analyzeProgram(prog *loader.Program) map[string]*FileAnalysis {
	fe := frontend.RunProgram(prog)

	out := make(map[string]*FileAnalysis, len(fe.Trees))
	for _, tree := range fe.Trees {
		out[tree.File.Name] = &FileAnalysis{
			Tree:  tree,
			Info:  fe.Infos[tree], // nil whenever fe.HasErrors stopped the pipeline before Check ran
			Diags: fe.Diags[tree],
		}
	}
	return out
}
