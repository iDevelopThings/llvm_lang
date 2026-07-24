package lsp

import (
	"fmt"
	"path/filepath"
	"sync"

	"llvm_lang/src/ast"
	"llvm_lang/src/loader"

	"github.com/spf13/afero"
	protocol "github.com/tliron/glsp/protocol_3_16"
)

// Workspace holds every open document's live buffer content and the most
// recently computed analysis for every file that analysis touched - see
// doc.go for why this is a whole-package reparse per edit, not incremental.
//
// The editor's buffer, not disk, is the source of truth for an open
// document: fs is a copy-on-write overlay (afero.NewCopyOnWriteFs) over the
// real filesystem, so a didChange writes the live buffer text into the
// writable layer, while any sibling file in the same package that isn't
// currently open still resolves from disk through the same afero.Fs - this
// reuses src/loader completely unmodified (see LoadProgram, which already
// takes an afero.Fs).
type Workspace struct {
	mu sync.Mutex

	fs afero.Fs

	// analysis holds the latest FileAnalysis for every file path last seen
	// by a recompute - not just currently-open documents, since a package's
	// dependency files (imported, but not themselves open in the editor)
	// get analyzed too and their Info is just as valid to serve from cache
	// (e.g. a definition jumping into an unopened file).
	analysis map[string]*FileAnalysis

	// nextGeneration is a monotonically increasing counter, one value
	// consumed per OpenOrChange call - see FileAnalysis.Generation's own
	// doc comment for why every recompute needs a distinct identity.
	nextGeneration int
}

// NewWorkspace returns an empty Workspace rooted at the real OS filesystem,
// with an in-memory overlay layer for live editor buffers.
func NewWorkspace() *Workspace {
	return &Workspace{
		fs:       afero.NewCopyOnWriteFs(afero.NewOsFs(), afero.NewMemMapFs()),
		analysis: make(map[string]*FileAnalysis),
	}
}

// OpenOrChange records path's current buffer text (didOpen or didChange -
// both simply mean "this is the document's current full content" under
// TextDocumentSyncKindFull, see cmd/llvmc-lsp/main.go) and re-analyzes path's
// whole containing package (see LANGUAGE.md's "Multi-file packages" section:
// a package is every .llx file directly in one directory) plus everything it
// imports, transitively.
//
// Returns every file touched by this recompute, keyed by path - a change in
// one file can affect diagnostics in a sibling the editor also has open (a
// removed function a sibling calls), so the caller republishes diagnostics
// for every result entry that happens to be an open document, not just path
// itself. A package that merely *imports* the recomputed one (rather than
// being imported by it) is NOT re-analyzed here - loader.LoadProgram only
// walks dependencies, not dependents - so a stale diagnostic in a downstream
// importer persists until that file is itself opened/edited. A known,
// documented v1 limitation (see the approved plan for this round), not an
// oversight: a real fix needs a reverse dependency index this round doesn't
// build.
func (w *Workspace) OpenOrChange(path, text string) (map[string]*FileAnalysis, error) {
	if err := afero.WriteFile(w.fs, path, []byte(text), 0o644); err != nil {
		return nil, err
	}

	prog, err := loader.LoadProgram(w.fs, filepath.Dir(path))
	if err != nil {
		return nil, err
	}

	w.mu.Lock()
	w.nextGeneration++
	generation := w.nextGeneration
	w.mu.Unlock()

	result, err := safeAnalyzeProgram(prog, generation)
	if err != nil {
		return nil, err
	}

	w.mu.Lock()
	for p, fa := range result {
		w.analysis[p] = fa
	}
	w.mu.Unlock()

	return result, nil
}

// safeAnalyzeProgram runs analyzeProgram with a panic recovered into a plain
// error, rather than letting it propagate. Every request into this LSP
// server runs on live, arbitrarily-mid-edit editor buffers - text a real
// compile would never see, since a saved-to-disk file is what every
// lexer/parser/sema test fixture and BENCHMARKS.md's own numbers are drawn
// from. glsp's request dispatch (github.com/tliron/glsp/server) has no
// recover of its own around a handler call, so an uncaught panic here would
// take down this whole long-running server process, not just fail the one
// request that triggered it - a much worse outcome for an interactive tool
// than one request returning an error the editor can just retry after the
// next edit.
func safeAnalyzeProgram(prog *loader.Program, generation int) (result map[string]*FileAnalysis, err error) {
	defer func() {
		if r := recover(); r != nil {
			result = nil
			err = fmt.Errorf("lsp: analysis panicked (likely a sema bug against in-progress source): %v", r)
		}
	}()
	return analyzeProgram(prog, generation), nil
}

// Analysis returns the most recently computed FileAnalysis for path, if any
// recompute has ever touched it.
func (w *Workspace) Analysis(path string) (*FileAnalysis, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	fa, ok := w.analysis[path]
	return fa, ok
}

// resolveNode is the "what is under the cursor, and does it have a real
// Info to query" preamble Hover/Definition/References all start with
// identically: look up path's own FileAnalysis, bail if it has none at all
// (fa.Info == nil - only possible if analyzeProgram was never run against
// this file), convert pos to a byte offset, and find the innermost node
// there. ok is false whenever there's nothing meaningful to query - every
// caller can treat that uniformly as "return nil/no results" without
// re-deriving why.
func (w *Workspace) resolveNode(path string, pos protocol.Position) (fa *FileAnalysis, n ast.NodeIndex, ok bool) {
	fa, ok = w.Analysis(path)
	if !ok || fa.Info == nil {
		return nil, ast.InvalidNode, false
	}
	offset := positionToByteOffset(fa.Tree.File.Src, pos)
	n = fa.Tree.NodeAt(offset)
	if n == ast.InvalidNode {
		return nil, ast.InvalidNode, false
	}
	return fa, n, true
}

// analysisSnapshot returns every cached FileAnalysis at the moment of the
// call, as a defensive copy of the slice (not the *FileAnalysis values
// themselves, which are never mutated after creation) - used by
// Workspace.References, which needs to scan the whole cache without holding
// w.mu for the duration of that scan.
func (w *Workspace) analysisSnapshot() []*FileAnalysis {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]*FileAnalysis, 0, len(w.analysis))
	for _, fa := range w.analysis {
		out = append(out, fa)
	}
	return out
}

// Forget drops path's cached analysis (didClose) - the buffer overlay
// itself is deliberately left in place (the file's on-disk content is
// unaffected either way, and a future re-open/change will overwrite the
// overlay with fresh content regardless), this just stops serving stale
// hover/definition results for a document the editor no longer has open.
func (w *Workspace) Forget(path string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.analysis, path)
}
