# LLVM Go Custom Language

Hobby project to create a custom programming language using LLVM and Go.

We're using Go 1.26, so **ALWAYS** opt for the latest language features.

# Documentation map

This file is the repo-conventions/how-to-work-here doc, kept deliberately
slim since every agent reads it. The rest lives in linked files, loaded only
when actually relevant to the task at hand:

- **`LANGUAGE.md`** - the language's own syntax and semantics: what a
  program looks like and how it behaves (types, operators, control flow,
  structs, arrays, conversions). Read this for anything touching the
  lexer/parser/sema grammar or type rules.
- **`CODEGEN.md`** - how the constructs in `LANGUAGE.md` lower into LLVM IR,
  plus the `cmd/llvmc` CLI driver. Read this for anything touching
  `src/codegen` or `cmd/llvmc`.
- **`DECISIONS.md`** - a dated architecture decision log: the "why we chose
  X over Y" narrative behind real design forks, too cross-cutting for a
  single code comment and not spec material for `LANGUAGE.md`.
- **`BLOCKERS.md`** - currently-open questions needing a human judgment
  call, actively pruned once answered (see that file's own header for the
  exact convention - it is not a changelog or archive).
- **`BENCHMARKS.md`** - dated `testing.B` baseline numbers (ns/op, B/op,
  allocs/op) for each pipeline stage - lexer, parser, sema, codegen, and the
  end-to-end `src/compiler` entry point - measured against a shared fixture
  in `src/bench`. Read this (and update it) whenever a change might
  meaningfully move performance/allocations in one of those stages, per the
  `## Standards` section below.
- **`docs/`** - concise, user-facing guides for learning and using the
  language. These explain what users write and observe; they are not a
  replacement for the exact rules in `LANGUAGE.md` or compiler internals in
  `CODEGEN.md`.

# User-facing documentation

Every user-visible language, standard-library, compiler CLI, diagnostic, or
editor feature change must update `docs/` in the same round. Documentation is
part of the feature, not follow-up work.

Keep the layers distinct:

- `LANGUAGE.md` is the precise language specification.
- `CODEGEN.md` records lowering and compiler-driver implementation details.
- `DECISIONS.md` records why a design was chosen.
- `BLOCKERS.md` records unresolved questions.
- `docs/` teaches users what exists, how to use it, and the limits they will
  encounter.

Do not copy implementation history into `docs/`. Avoid phrases such as "this
round", AST/sema/codegen narration, internal function names, and rejected
alternatives unless they materially affect how a user writes or runs a
program. Link to the exact reference document instead of duplicating its long
explanation.

## Documentation style

Assume the reader already understands ordinary programming concepts but is
new to this language. Write for quick scanning and low attention overhead:

- Lead with the syntax or outcome, then explain it in one short paragraph.
- Keep one concept per section and one non-obvious point per paragraph.
- Prefer a small runnable example, compact table, or short list over several
  paragraphs of prose.
- Keep snippets focused on one idea. Link to a full program in `examples/`
  instead of pasting a large demo or explaining it line by line.
- Show expected output only when it clarifies behavior that is not obvious
  from the snippet.
- Use plain language. Introduce project-specific terms only when the user
  needs them.
- State important restrictions beside the feature they restrict, not several
  pages later.
- Do not narrate provenance, implementation chronology, or the full reasoning
  behind a rule. That belongs in `DECISIONS.md` or `CODEGEN.md`.
- Do not assume Go behavior merely because the syntax looks like Go. Verify
  the actual compiler/spec behavior, especially `_`, capture, range, ownership,
  conversion, and addressability rules.

The beginner path must stay small. Put uncommon caveats in
`docs/current-limitations.md`, exact feature routing in
`docs/feature-index.md`, and longer edge-case detail in `LANGUAGE.md`.
Do not make the introductory pages exhaustive.

## Keeping the docs complete

For every new or changed user-visible feature:

1. Update its topic page under `docs/`.
2. Add or update its row in `docs/feature-index.md`.
3. Update `docs/current-limitations.md` when a limitation is introduced,
   removed, or narrowed.
4. Update `docs/compiler.md` for CLI/editor behavior,
   `docs/packages.md` for package behavior, or
   `docs/standard-library.md` for stdlib behavior.
5. Add the runnable example to `docs/examples.md`, or explicitly use `—` in
   the feature index when no dedicated example exists.
6. Keep `docs/README.md` navigation correct when pages are added, renamed, or
   moved.

Every teaching page belongs in the numbered walkthrough in `docs/README.md`
and links to the previous and next page. Reference pages such as the feature
index, examples list, and current limitations stay outside that sequence.

Avoid explaining the same feature independently on several pages. Give it one
main home; other pages should summarize in a sentence and link there.

Examples in `docs/` must compile as written once placed in the context they
claim to use. After changing user-facing docs:

- compile the relevant documentation snippets or equivalent example programs;
- compile every changed example package;
- verify every relative Markdown link and heading anchor;
- confirm every file under `examples/` remains represented in
  `docs/examples.md`.

When implementation and docs disagree, do not quietly document whichever
behavior seems nicer. Verify the compiler, tests, examples, and
`LANGUAGE.md`; fix the stale layer or record a genuine unresolved question in
`BLOCKERS.md`.

# Compiling

Because of llvm support, we need cgo.
There is a build.ps1 script you can use to compile.

Use `test.ps1` to run tests (`.\test.ps1`, or `.\test.ps1 -Run TestFoo` for a
single test) rather than hand-typing `go test`/mingw64-PATH setup in an ad-hoc
shell - both scripts share the same mingw64-on-PATH requirement (needed for
cgo to build against gcc/g++, and for the resulting binary to load
libLLVM-22.dll at run time), and getting that PATH setup wrong looks like a
native crash (`0xC0000139`), not an obvious "PATH is wrong" error.

# Branching and worktrees

Base any new branch or worktree (in this repo or the sibling JetBrains
plugin repo) on the **local** `master`, never `origin/master` or any other
remote ref. Work regularly lands on local `master` directly, well ahead of
whatever's actually been pushed - a branch forked from remote can be many
commits stale before its own first commit, and that staleness doesn't
surface until it's rebased/merged much later, by which point it may
silently conflict with or even delete work that landed in the meantime
(this has happened in practice: a branch forked from a stale remote master
was missing an entire since-landed feature, and diffing it against current
local master showed real code from that feature as "to be removed").

# Project Info

## Enums

Enums are code generated using go generate, you can find existing yml defs inside src/enums.
It's preferable to use the enum gen rather than go style enums, the generator is flexible, lets us add extra fields, creates maps we need, provides extra helper functions for the type etc.

See: cmd/enum_codegen/README.md for a full reference.

**Default to enum_codegen for any new discriminated-`kind`-style type** (a
small closed set of named values - a new `NodeKind`, a new IR-level
"opcode"/"tag" type, etc.) rather than hand-rolling a plain Go
`const ... iota` block with a hand-maintained `String()` switch. The
generator gives you the constants, `Values()`/`Infos()`/`All()`/`Parse()`,
and a real `String()` for free - a hand-written enum has to redo all of that
by hand and keep it in sync forever.

A hand-rolled Go `const ... iota` block is only justified when the type
needs **zero supporting code** - no `String()`, no lookup map, no `Parse`/
iteration helper, nothing beyond the bare constants themselves. Whether the
type is package-internal or a cross-cutting language concept doesn't factor
into this at all - the only question is whether there's any supporting code
to write. The moment a kind-type needs even one piece of that (most
commonly a `String()` for debugging/diagnostics), generate it instead of
hand-maintaining it.

A few things that are easy to miss from the README alone:

- **Specs don't have to live in `src/enums`.** That's just where most of the
  current cross-cutting ones happen to sit, kept separate because
  enum_codegen generates extra supporting code around them. A
  package-internal enum can keep its `.yml` right next to the package it
  belongs to - the only requirement is a correct `//go:generate` directive
  pointing at it (`go generate ./...` finds and runs every one).
- **Members can carry arbitrary typed metadata**, not just a name/value -
  extra YAML keys become typed columns (inferred from the YAML scalar, or
  pinned explicitly via a top-level `fields:` block), including references
  to *other* generated enums (compile-checked, not stringly-typed) and even
  slices of a real Go struct in the same package, rendered as proper nested
  composite literals. This is the concrete case for reaching for
  enum_codegen over a plain Go enum: the moment a "kind" wants to carry any
  side data per member (a display name, a set of flags, a related enum
  value), that's a strong signal it should be generated rather than
  hand-maintained as a parallel `map[Kind]something`.
- **Extra iterators** (`iterators: [colA, colB]`) generate an `iter.Seq`
  walker over a specific metadata column, in declaration order - useful
  instead of hand-writing a small loop helper.
- `sema.TypeKind` is a plausible future candidate for this treatment if it
  ever needs named per-kind metadata (e.g. bit width, signedness, a
  human-facing name) beyond what its current hand-written `String()`/helper
  methods do - not an immediate action, just noted here since the shape
  fits well.

## Standards

Max efficiency and performance, keeping allocations down to a minimum. We already have cgo overhead, so we must avoid adding anymore.

When the choice is right, opt for using iter.Seq yield feature, slices.X from std library etc.

To be specific about the iter.Seq preference above: it's not a checkbox to tick, it's about avoiding hand-rolled loops-with-conditions when a custom iterator can express the same filtering/derivation more directly. For any new utility/helper function that conceptually "returns a collection" (a filtered/derived set of nodes, symbols, fields, etc.) - default to returning `iter.Seq[T]` rather than materializing and returning a `[]T`. This isn't absolute: a caller that genuinely needs indexed/random access, a length known upfront, or multiple passes over the same data can still reasonably want a plain slice (or `slices.Collect` over an iterator) - but that should be the exception, decided at the call site, not the default shape a new helper is written in.

Any disk I/O this compiler needs goes through `afero.Fs` (`github.com/spf13/afero`), never direct `os` calls (`os.ReadFile`, `os.ReadDir`, `os.Stat`, ...) - see `src/loader` for the first (and, so far, only) place this matters. Production code wires in `afero.NewOsFs()`; tests build fake filesystem layouts with `afero.NewMemMapFs()` instead of creating/tearing down real temp directories. This is a deliberate standing convention (see `DECISIONS.md`'s "Adopting afero for file loading" entry), not just incidental to `src/loader` - keep it in mind for any future feature that needs to touch the filesystem.

## Architecture

All code should be at it's correct layer, for example no code gen logic should be inside type checker(sema), and vice versa. (This applies everywhere, not just these two examples).
It's important to maintain and embrace this, or the project will quickly become a tangled mess of unmaintainable and difficult code.

**Before writing new logic, actively look for whether it already exists somewhere reusable, or belongs somewhere reusable, rather than defaulting to a private, one-off version in whichever file happens to need it right now.** This is a recurring failure mode specifically in agent-written code (not unique to this project): reaching for "just write it inline here" over "find/create the shared home for this" reads as the path of least resistance in the moment, but it's the same root cause as the duplication this file's review process already watches for - it just happens *before* a second copy exists rather than being caught after. Treat "does another part of the codebase already do this, or plausibly want to do this later" as a real question to ask before writing a helper, not an afterthought a reviewer should have to raise. A concrete instance of this: reusable AST-shape recognition (matching a declaration by a naming+signature convention - a name pattern, a parameter's type shape, walking children to confirm a signature; pure AST-level pattern matching, not semantic/type-checking) belongs in `src/ast`, not private helpers inside whichever `cmd/` tool needed it first. `cmd/` tools are leaf consumers of `src/` packages, never the reverse, so discovery logic written inline in a `cmd/` file is structurally unreachable from `src/lsp` even when `src/lsp` obviously wants the identical recognition later (e.g. a "run this test" code-lens needs the exact same `TestXxx(t *test.Runner)` convention `llvmc -test` discovers) - not an active violation the day it's written, but a foreseeable one, and the fix once a second consumer needs it is usually a duplicate reimplementation rather than a shared helper. Default this kind of logic to a public, tested `src/ast` function from the start (an `iter.Seq` per the Standards section above, letting the specific caller `slices.Collect` if it needs indexed/counted access), even with only one caller on day one.

## Review process **IMPORTANT**

Every round that lands new or changed code in `src/`, `cmd/`, or `std/` (a
new language feature, a bug fix touching sema/codegen/parser, anything
beyond a pure documentation/comment change) needs a dedicated, read-only
review pass - checking layering violations, duplication, and strictness
gaps - **in addition to, and separate from, functional verification**
(build it, run it, confirm the output matches). This is not optional for
"small" or "low-risk" changes, and it is not something to reserve only for
rounds that feel risky in the moment - that judgment call is exactly what
fails silently over time.

Functional testing only proves the specific paths someone thought to try.
It does not catch a `switch` over a type/kind enum that's silently
incomplete for a case nobody happened to test, a semantic check in one
layer that's looser than what a downstream layer can actually handle, or a
pattern hand-rolled two or three times that should have been one shared
helper. A concrete real example, not hypothetical: a struct/array `==`
check accepted any field type via a bare whole-type-equality check, with no
per-field validation - every hands-on test of the feature passed, because
none of them happened to compare a struct holding a map or a slice field.
The result was silently wrong (not even a crash): two structs differing
only in a slice field's real contents compared as equal. That's the
specific failure mode this review step exists to catch, and it survived
across several consecutive feature rounds specifically because the review
step was skipped in favor of "it built, it ran, the output matched."

Skipping this because a change "looks straightforward" is precisely how it
gets missed - straightforward-looking changes are exactly the ones nobody
double-checks.

**This review pass is not something a dispatched implementation agent does
to its own work.** An agent spinning up its own internal review sub-agent
as part of the same task is not a substitute for this step - it shares the
same blind spots as the code it's reviewing, and it slows the round down for
no independent signal gained. The review happens afterward, done by whoever
dispatched the work (or a separately-dispatched reviewer with no stake in
the implementation), never folded into the implementing agent's own run.

**Functional tests need invalid-path coverage, not just the happy path.**
A suite that only proves a feature works when used correctly won't catch a
missing diagnostic, a rejection that should fire and doesn't, or a
double-free/use-after-invalidation case - see the struct/array `==` example
above, which slipped through for exactly this reason. Every dispatch brief
for new/changed `src/`, `cmd/`, or `std/` code should explicitly ask for
tests covering illegal usage and boundary/edge cases alongside the valid
ones, not just "test that it works."

**This applies to `src/lsp` too, every time a language feature lands** -
see `src/lsp/doc.go`'s own note on the exact shape (every major LSP
capability, plus a broken/incomplete-source variant). Generics landed with
zero `src/lsp` coverage and a real, user-visible bug went unnoticed for
several rounds as a direct result.

## Keeping the JetBrains plugin in sync

A separate, sibling project (`F:\Go\llvm_lang_jb_plugin`) implements IDE
support for this language - its own repo, its own history, not part of this
one. After a language feature lands here (new syntax, a new builtin, a new
diagnostic shape, anything the plugin's own lexer/parser/PSI/inspections
would need to mirror), dispatch a separate agent against that other repo to
keep it in sync, in parallel with whatever comes next here - never let
plugin-sync work block or gate this repo's own compiler work. It is lower
priority than the compiler itself, done opportunistically alongside it.

## Project Code Style Preferences **IMPORTANT**

Avoid single line syntax, for ex:

Bad:
```go
return Token{Lexeme: enums.Lexemes.Semicolon, Start: Pos(l.pos), End: Pos(l.pos)}
```
Good:
```go
return Token{
    Lexeme: enums.Lexemes.Semicolon,
    Start: Pos(l.pos),
    End: Pos(l.pos),
}
```

### Switches:
Bad:
```go
switch t.Lexeme {
	case enums.Lexemes.Number, enums.Lexemes.String,
	enums.Lexemes.RightParen, enums.Lexemes.LeftParen, enums.Lexemes.Identifier:
        return true
    case enums.Lexemes.Identifier:
        return true
    default:
        return false
}
```
Good:
```go
switch t.Lexeme {
	case enums.Lexemes.Number,
		enums.Lexemes.String,
		enums.Lexemes.RightParen,
		enums.Lexemes.RightBracket,
		enums.Lexemes.RightBrace,
		enums.Lexemes.PlusPlus,
		enums.Lexemes.MinusMinus:
        return true
    case enums.Lexemes.Identifier:
        return true
    default:
        return false
}
```



### Comments **IMPORTANT**

Comments have been trending steadily longer and more elaborate as the
project has grown - each new round tends to match the surrounding file's
existing comment density, which then ratchets it up further next round.
Pull this back deliberately, on every edit, not just when writing new code:

- State the non-obvious fact in one or two sentences. Don't narrate the
  full provenance of how execution reaches this line (which caller, via
  which dispatch, guaranteed by which other function, proven by which
  test) when a single short cross-reference already lets a reader chase it
  down themselves.
- Don't chain more than one "see X" per comment. Pick the single most
  useful pointer, not three.
- Never explain the same fact twice in two nearby places (e.g. a long
  rationale at a call site *and* an equally long one on the function it
  calls). Put it in exactly one place - normally the function/type's own
  doc comment - and have every other site reference it briefly.
- If deleting the comment would leave a future reader confused about a
  hidden constraint or a subtle *why*, keep a short version. If it would
  just remove restated *what* the adjacent code already makes obvious,
  delete it.

Bad:
```go
case enums.NodeKinds.MatchStmt:
    // A MatchStmt node reached HERE - via checkExpr/inferExpr's own
    // dispatch - is always the expression-position flavor (see
    // ast.Node's own MatchStmt doc comment): a bare statement-position
    // `match x {...}` is dispatched by checkStmt directly to
    // checkMatchStmt, never by way of checkExpr at all (parseStmt's own
    // keyword-first dispatch guarantees the two surface forms produce
    // genuinely different call paths - see parser/stmt.go's own
    // parseStmt doc comment, and the regression test proving it).
    return c.checkMatchExprStmt(n)
```
Good:
```go
case enums.NodeKinds.MatchStmt:
    // Always expression-position here - statement-position match is
    // dispatched by checkStmt directly, never through checkExpr.
    return c.checkMatchExprStmt(n)
```

**This applies equally to `BLOCKERS.md`/`DECISIONS.md`/`CODEGEN.md` prose
additions, not just Go comments.** The same ratcheting happens there: a new
entry matches the surrounding write-up's density, and it compounds round
over round. Keep new entries in these files to the same standard - state the
non-obvious fact, cross-reference once, don't re-narrate a story that's
already told at the code site.

## Parser

`src/parser` is split by concern, not dumped into one file:
- `parser.go` - core scaffolding only: token cursor, expect/accept/sync, diagnostics, bailout/Run. No grammar rules belong here.
- `expr.go` - expression parsing.
- `stmt.go` - statement parsing.
- `decl.go` - top-level declarations.

Expression parsing uses a **Pratt parser** (operator-precedence parsing via a table keyed by `enums.Lexeme`, mapping to prefix/infix parse functions plus a precedence) - not a hand-cranked function per precedence level. Postfix operators (call `(`, index `[`, member `.`) are registered as infix table entries at the highest precedence, so they chain through the same loop as binary operators instead of a separate bolted-on postfix phase.

This area needs strong test coverage as it grows - precedence/associativity bugs are easy to introduce and easy to miss without tests backing every rule. Prefer table-driven tests asserting `Tree.Dump()` output for precedence/associativity/chaining cases.
