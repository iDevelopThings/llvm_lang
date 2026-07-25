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
		resolveGenericTemplatesForTooling(tree, info)
		out[tree.File.Name] = &FileAnalysis{
			Tree:       tree,
			Info:       info,
			Diags:      fe.Diags[tree],
			Generation: generation,
		}
	}
	return out
}

// resolveGenericTemplatesForTooling enriches info in place with real
// Refs/Scopes entries for every one of tree's own generic declarations
// (struct/func templates - see sema.Info.IsGenericTemplate) - source the
// real compile pipeline deliberately never resolves as written (only each
// concrete instantiation is), which otherwise leaves every hover/
// completion/semantic-highlighting query inside a generic declaration's
// own body with nothing to read (see sema.ResolveTemplateForTooling's own
// doc comment for why this is safe: Resolve-only, never touches real
// shared state). Merged directly into info's own maps rather than kept as
// a separate overlay - every existing Info.Refs[n]/Info.Scopes[n] read
// site picks this up for free, with zero key collisions possible (a real
// resolve/check pass never writes an entry for a template's own nodes in
// the first place).
func resolveGenericTemplatesForTooling(tree *ast.Tree, info *sema.Info) {
	if info == nil {
		return
	}
	for _, decl := range tree.Children(tree.Root) {
		shadow := sema.ResolveTemplateForTooling(tree, info, decl)
		if shadow == nil {
			continue
		}
		for n, sym := range shadow.Refs {
			info.Refs[n] = sym
		}
		for n, scope := range shadow.Scopes {
			info.Scopes[n] = scope
		}
	}
}
