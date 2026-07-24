// Package lsp is the protocol-agnostic logic behind cmd/llvmc-lsp: given a
// workspace of open .llx documents, it re-runs this compiler's own frontend
// (lexer -> parser -> sema.ResolvePackage/ResolveProgram -> sema.CheckProgram
// - never codegen, never LLVM/cgo) and answers diagnostics/hover/
// go-to-definition/semantic-highlighting queries against the result. See
// cmd/llvmc-lsp's own doc comment for the JSON-RPC/LSP transport this feeds.
//
// Deliberately whole-file-reparse-per-edit, not incremental: lexer+parser+
// sema together benchmark at ~238us (small file) to ~3.2ms (large, per
// BENCHMARKS.md) - cheap enough to re-run on every debounced edit without a
// real incremental-reparse (green/red tree) architecture. See BLOCKERS.md's
// "Incremental reparse / a real green-red tree for the LSP" entry for the
// full write-up of what a real incremental architecture would need and why
// it was deliberately deferred.
package lsp
