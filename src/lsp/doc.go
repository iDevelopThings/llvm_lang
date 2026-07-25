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
//
// When a language feature lands (a new grammar construct, a new sema
// concept), add or extend an *_test.go file here covering it against every
// major capability (hover, definition, references, documentHighlight,
// documentSymbol, foldingRange, semanticTokens, completion) - both a valid
// example and at least one deliberately incomplete/broken-source variant
// (AGENTS.md's own review-process standard applies here exactly as it does
// to src/sema/src/parser). Generics landed with zero such coverage and a
// real, user-visible bug (a whole generic struct rendering as a wall of
// spurious underlines) went unnoticed for several rounds as a result - see
// generics_test.go/coroutines_test.go for the shape this should take, and
// the existing *_test.go helpers (singleFileWorkspace, loadProgram,
// appAnalysis) for what's already there to build on - this is normally a
// few lines per feature, not a new harness.
package lsp
