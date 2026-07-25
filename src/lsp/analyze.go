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
// (populated best-effort regardless of errors anywhere in the program -
// see frontend.RunProgram's own doc comment), and its merged
// parse+resolve+check diagnostics.
//
// Generation identifies which single analyzeProgram call produced this
// FileAnalysis (see Workspace.OpenOrChange) - every *sema.Symbol pointer is
// only ever comparable within the one resolver run that built it (a fresh
// Scope/Symbol graph is built from scratch on every recompute - see
// sema.ResolveProgram - nothing survives across two separate ones), so a
// consumer comparing Symbol identity across cached files (e.g.
// Workspace.References) must first confirm they share a Generation, not
// just assume every entry in Workspace's cache is mutually comparable.
type FileAnalysis struct {
	Tree       *ast.Tree
	Info       *sema.Info
	Diags      *diag.Bag
	Generation int
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
// own path, all stamped with the same generation (see FileAnalysis.Generation's
// own doc comment for why every file from one call must share an identity
// distinct from any other call's).
func analyzeProgram(prog *loader.Program, generation int) map[string]*FileAnalysis {
	fe := frontend.RunProgram(prog)

	out := make(map[string]*FileAnalysis, len(fe.Trees))
	for _, tree := range fe.Trees {
		info := fe.Infos[tree]
		// A generic declaration's own body is never resolved by the real
		// pipeline (only each instantiation is) - see
		// sema.ResolveTemplatesForTooling's own doc comment - which
		// otherwise leaves every hover/completion/semantic-highlighting
		// query inside one with nothing to read.
		sema.ResolveTemplatesForTooling(tree, info)
		out[tree.File.Name] = &FileAnalysis{
			Tree:       tree,
			Info:       info,
			Diags:      fe.Diags[tree],
			Generation: generation,
		}
	}
	return out
}
