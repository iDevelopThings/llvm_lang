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
// real incremental-reparse (green/red tree) architecture. See
// BLOCKERS_DRAFT.md in this directory for the full write-up of what a real
// incremental architecture would need and why it was deliberately deferred.
//
// Scope note: every file in this package was written under an explicit
// constraint (see the approved plan for this round) to touch only
// src/lsp and cmd/llvmc-lsp, since other work was concurrently touching the
// language core (src/ast, src/sema, src/parser, src/lexer, src/loader,
// src/compiler). Anything that conceptually belongs in one of those
// packages instead - a generic AST helper, a repo-doc entry - is
// implemented/drafted locally here, each marked with a
// "TODO(lsp): move to <real location>" comment, so a later pass can migrate
// it once those packages are safe to touch again. See BLOCKERS_DRAFT.md and
// nodeat.go/positions.go for the concrete instances.
package lsp
