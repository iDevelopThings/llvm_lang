<!--
TODO(lsp): move this entry into the real BLOCKERS.md once core/doc files are
safe to touch again (see doc.go's own scope-constraint note - this round was
restricted to src/lsp and cmd/llvmc-lsp only, so this couldn't be written
directly into BLOCKERS.md the way it normally would be). Paste the section
below in as its own "## " entry, following BLOCKERS.md's existing convention
(what the question is, why it isn't inferable, and the current default) -
then delete this file.
-->

## Incremental reparse / a real green-red tree for the LSP

Building `src/lsp` (an editor language server) surfaced a real, open design
question: `ast.Tree` today is index/arena-based (`NodeIndex int32` into a
flat `Tree.Nodes` slice, no pointers) - already structurally close to a
Roslyn/rust-analyzer "green tree." But `ast.Node.Span` stores **absolute**
byte offsets, not relative width. A real green tree stores width specifically
so an unedited subtree's identity is position-invariant and structurally
shareable across an edit (the "red tree" is then a thin, lazily-materialized
layer on top that computes absolute positions/parent chains on demand).
Bolting a red layer onto the current absolute-offset representation would not
get that sharing - it would need `ast.Node`'s own representation reworked
first (relative width instead of absolute `Span`, touching the parser and
every `Span` consumer), a genuinely separate, nontrivial project from "add
LSP support."

**Why this isn't inferable/default-able:** it's a real product/performance
tradeoff (how much engineering effort now vs. later, for a benefit that only
matters once reparse-per-edit is measurably too slow), not something with an
obvious "right" default the way most engineering calls in this codebase are.

**Current default (explicitly chosen, not just deferred by omission):**
`src/lsp` re-runs the whole frontend (lexer -> parser -> sema.Resolve/
CheckProgram) on every debounced edit, no incremental reuse at all.
`BENCHMARKS.md`'s own numbers - lexer+parser+sema together at ~238us for a
small fixture, ~3.2ms for a large (40x) one - make this comfortably fast
enough for an interactive editor loop at this project's current scale.
Revisit only once a real, large `.llx` file actually demonstrates
reparse-per-edit is too slow in practice - not speculatively ahead of that.
