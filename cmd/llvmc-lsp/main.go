// Command llvmc-lsp is a Language Server Protocol server for llvm_lang,
// giving an editor live diagnostics, hover, go-to-definition, and semantic
// highlighting - all built on top of src/lsp, which does the real work
// (re-running this compiler's own lexer/parser/sema frontend against the
// editor's live buffers - see that package's own doc comment). This binary
// is deliberately just a thin JSON-RPC/LSP transport shell over src/lsp,
// mirroring cmd/llvmc's own "thin CLI shell over src/loader and
// src/compiler" shape - no analysis logic of its own.
//
// This binary imports neither src/codegen nor tinygo.org/x/go-llvm (src/lsp
// itself only ever calls into sema.Resolve/Check, never codegen - see
// src/lsp/analyze.go) - `go build ./cmd/llvmc-lsp` succeeds with no cgo/LLVM
// toolchain on PATH at all, unlike cmd/llvmc.
//
// Speaks LSP over stdio (the standard transport every general-purpose
// editor/client expects) using github.com/tliron/glsp - see AGENTS.md's
// review-process note and the approved plan for this round for why an
// existing protocol library was chosen over hand-rolling JSON-RPC framing.
//
// Usage:
//
//	llvmc-lsp
//
// (no flags - an editor's own LSP client launches this and speaks to it over
// stdin/stdout; there is nothing to configure on the command line today).
package main

import (
	"fmt"

	"llvm_lang/src/lsp"

	"github.com/tliron/commonlog"
	_ "github.com/tliron/commonlog/simple"
	"github.com/tliron/glsp"
	protocol "github.com/tliron/glsp/protocol_3_16"
	"github.com/tliron/glsp/server"
)

const serverName = "llvmc-lsp"

var serverVersion = "0.1.0"

var workspace = lsp.NewWorkspace()

var handler protocol.Handler

func main() {
	commonlog.Configure(1, nil)

	handler = protocol.Handler{
		Initialize:                      initialize,
		Initialized:                     initialized,
		Shutdown:                        shutdown,
		WorkspaceDidChangeConfiguration: didChangeConfiguration,
		TextDocumentDidOpen:             didOpen,
		TextDocumentDidChange:           didChange,
		TextDocumentDidClose:            didClose,
		TextDocumentHover:               hover,
		TextDocumentDeclaration:         declaration,
		TextDocumentDefinition:          definition,
		TextDocumentReferences:          references,
		TextDocumentDocumentHighlight:   documentHighlight,
		TextDocumentDocumentSymbol:      documentSymbol,
		TextDocumentFoldingRange:        foldingRange,
		TextDocumentSemanticTokensFull:  semanticTokensFull,
		TextDocumentCompletion:          completion,
	}

	srv := server.NewServer(&handler, serverName, false)
	srv.RunStdio()
}

// initialize advertises this server's capabilities. TextDocumentSync is
// forced to TextDocumentSyncKindFull, overriding CreateServerCapabilities'
// own default of Incremental for a TextDocumentDidChange handler - src/lsp's
// Workspace always wants a document's whole current text (see
// Workspace.OpenOrChange's own doc comment: it re-lexes/re-parses the whole
// file every time regardless, so there's no benefit to incremental deltas,
// only more client-side complexity for no payoff this round).
func initialize(context *glsp.Context, params *protocol.InitializeParams) (any, error) {
	capabilities := handler.CreateServerCapabilities()

	syncOptions, ok := capabilities.TextDocumentSync.(*protocol.TextDocumentSyncOptions)
	if !ok {
		syncOptions = &protocol.TextDocumentSyncOptions{}
		capabilities.TextDocumentSync = syncOptions
	}
	fullSync := protocol.TextDocumentSyncKindFull
	syncOptions.Change = &fullSync

	if semanticOptions, ok := capabilities.SemanticTokensProvider.(*protocol.SemanticTokensOptions); ok {
		semanticOptions.Legend = lsp.SemanticTokensLegend()
	}

	capabilities.CompletionProvider = &protocol.CompletionOptions{
		TriggerCharacters: []string{"."},
	}
	if root := workspaceRoot(params); root != "" {
		workspace.SetRoot(root)
	}

	return protocol.InitializeResult{
		Capabilities: capabilities,
		ServerInfo: &protocol.InitializeResultServerInfo{
			Name:    serverName,
			Version: &serverVersion,
		},
	}, nil
}

// workspaceRoot resolves the client's own advertised project root -
// preferring RootURI, falling back to the first WorkspaceFolders entry -
// to a real filesystem path, for Workspace.SetRoot (completion's
// not-yet-imported-package discovery - see lsp.Workspace.PackageIndex).
// Returns "" if the client never advertised one at all (an editor opening
// a single loose file rather than a folder) - PackageIndex then just
// reports no candidates, same as before this feature existed.
func workspaceRoot(params *protocol.InitializeParams) string {
	if params.RootURI != nil {
		if path, err := lsp.PathFromURI(*params.RootURI); err == nil {
			return path
		}
	}
	if len(params.WorkspaceFolders) > 0 {
		if path, err := lsp.PathFromURI(params.WorkspaceFolders[0].URI); err == nil {
			return path
		}
	}
	return ""
}

func initialized(context *glsp.Context, params *protocol.InitializedParams) error {
	return nil
}

func shutdown(context *glsp.Context) error {
	protocol.SetTraceValue(protocol.TraceValueOff)
	return nil
}

// didChangeConfiguration acknowledges the client's own settings changing -
// a real no-op, since this server has no configuration surface at all yet
// (no settings.json/initializationOptions consumed anywhere - see
// src/lsp). A client is free to send this notification unconditionally
// whenever its own settings change, regardless of whether the server ever
// declared interest in it - leaving this field nil (glsp's default) is
// what previously produced a "method not supported: workspace/
// didChangeConfiguration" error in the client's own LSP log (LSP4IJ sends
// it on every settings change): protocol.Handler.Handle only dispatches a
// notification method whose matching field is non-nil, so a real handler,
// even one that does nothing, is what correctly acknowledges it instead.
func didChangeConfiguration(context *glsp.Context, params *protocol.DidChangeConfigurationParams) error {
	return nil
}

func didOpen(context *glsp.Context, params *protocol.DidOpenTextDocumentParams) error {
	return openOrChange(context, params.TextDocument.URI, params.TextDocument.Text)
}

// didChange reads the latest full-document text from ContentChanges -
// TextDocumentSyncKindFull (see initialize) guarantees the client always
// sends exactly one TextDocumentContentChangeEventWhole per notification, so
// the last entry (defensively, in case a client ever sends more than one)
// is always this change's complete new text.
func didChange(context *glsp.Context, params *protocol.DidChangeTextDocumentParams) error {
	var text string
	var found bool
	for _, c := range params.ContentChanges {
		if whole, ok := c.(protocol.TextDocumentContentChangeEventWhole); ok {
			text = whole.Text
			found = true
		}
	}
	if !found {
		return nil
	}
	return openOrChange(context, params.TextDocument.URI, text)
}

func openOrChange(context *glsp.Context, uri protocol.DocumentUri, text string) error {
	path, err := lsp.PathFromURI(uri)
	if err != nil {
		return err
	}

	touched, err := workspace.OpenOrChange(path, text)
	if err != nil {
		// didOpen/didChange are LSP *notifications* - there is no response
		// for a returned error to ever reach (the editor never sees a
		// notification's return value at all, only a request's). An import
		// cycle, an unreadable sibling file, or a recovered analysis panic
		// (see safeAnalyzeProgram) would otherwise fail completely silently
		// from the user's point of view - window/logMessage is what
		// actually surfaces this in the editor's own LSP output channel.
		context.Notify(protocol.ServerWindowLogMessage, protocol.LogMessageParams{
			Type:    protocol.MessageTypeError,
			Message: fmt.Sprintf("llvmc-lsp: analyzing %s: %v", path, err),
		})
		return err
	}
	for touchedPath, fa := range touched {
		publishDiagnostics(context, lsp.URIFromPath(touchedPath), fa)
	}
	return nil
}

func didClose(context *glsp.Context, params *protocol.DidCloseTextDocumentParams) error {
	path, err := lsp.PathFromURI(params.TextDocument.URI)
	if err != nil {
		return err
	}
	workspace.Forget(path)

	// Clear whatever diagnostics this document last had - the editor no
	// longer has it open, so this server should stop claiming anything
	// about it until it's reopened.
	context.Notify(protocol.ServerTextDocumentPublishDiagnostics, protocol.PublishDiagnosticsParams{
		URI:         params.TextDocument.URI,
		Diagnostics: []protocol.Diagnostic{},
	})
	return nil
}

func publishDiagnostics(context *glsp.Context, uri protocol.DocumentUri, fa *lsp.FileAnalysis) {
	context.Notify(protocol.ServerTextDocumentPublishDiagnostics, protocol.PublishDiagnosticsParams{
		URI:         uri,
		Diagnostics: fa.ProtocolDiagnostics(),
	})
}

func hover(context *glsp.Context, params *protocol.HoverParams) (*protocol.Hover, error) {
	path, err := lsp.PathFromURI(params.TextDocument.URI)
	if err != nil {
		return nil, err
	}
	return workspace.Hover(path, params.Position), nil
}

func completion(context *glsp.Context, params *protocol.CompletionParams) (any, error) {
	path, err := lsp.PathFromURI(params.TextDocument.URI)
	if err != nil {
		return nil, err
	}
	return workspace.Completion(path, params.Position), nil
}

// declaration reuses Definition outright: this language has no separate
// forward-declaration concept the way C/C++ headers do (every func/struct/
// var has exactly one declaration site, same as Go) - "go to declaration"
// and "go to definition" always mean the identical location here, so there's
// no reason for a second, parallel implementation. Wired up anyway (rather
// than leaving the handler nil) since some clients call textDocument/
// declaration specifically for certain gestures rather than falling back to
// textDocument/definition on their own.
//
// textDocument/implementation is deliberately NOT wired up at all: this
// language has no interface/abstract-dispatch concept whatsoever (no
// interface type, no virtual methods - see LANGUAGE.md) for "go to
// implementation" to mean anything about. There's no reasonable target it
// could ever return.
func declaration(context *glsp.Context, params *protocol.DeclarationParams) (any, error) {
	path, err := lsp.PathFromURI(params.TextDocument.URI)
	if err != nil {
		return nil, err
	}
	loc := workspace.Definition(path, params.Position)
	if loc == nil {
		return nil, nil
	}
	return *loc, nil
}

func definition(context *glsp.Context, params *protocol.DefinitionParams) (any, error) {
	path, err := lsp.PathFromURI(params.TextDocument.URI)
	if err != nil {
		return nil, err
	}
	loc := workspace.Definition(path, params.Position)
	if loc == nil {
		return nil, nil
	}
	return *loc, nil
}

func references(context *glsp.Context, params *protocol.ReferenceParams) ([]protocol.Location, error) {
	path, err := lsp.PathFromURI(params.TextDocument.URI)
	if err != nil {
		return nil, err
	}
	return workspace.References(path, params.Position, params.Context.IncludeDeclaration), nil
}

func documentHighlight(context *glsp.Context, params *protocol.DocumentHighlightParams) ([]protocol.DocumentHighlight, error) {
	path, err := lsp.PathFromURI(params.TextDocument.URI)
	if err != nil {
		return nil, err
	}
	return workspace.DocumentHighlight(path, params.Position), nil
}

func documentSymbol(context *glsp.Context, params *protocol.DocumentSymbolParams) (any, error) {
	path, err := lsp.PathFromURI(params.TextDocument.URI)
	if err != nil {
		return nil, err
	}
	return workspace.DocumentSymbols(path), nil
}

func foldingRange(context *glsp.Context, params *protocol.FoldingRangeParams) ([]protocol.FoldingRange, error) {
	path, err := lsp.PathFromURI(params.TextDocument.URI)
	if err != nil {
		return nil, err
	}
	return workspace.FoldingRanges(path), nil
}

func semanticTokensFull(context *glsp.Context, params *protocol.SemanticTokensParams) (*protocol.SemanticTokens, error) {
	path, err := lsp.PathFromURI(params.TextDocument.URI)
	if err != nil {
		return nil, err
	}
	return workspace.SemanticTokens(path), nil
}
