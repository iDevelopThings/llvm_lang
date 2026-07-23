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

# Compiling

Because of llvm support, we need cgo.
There is a build.ps1 script you can use to compile.

Use `test.ps1` to run tests (`.\test.ps1`, or `.\test.ps1 -Run TestFoo` for a
single test) rather than hand-typing `go test`/mingw64-PATH setup in an ad-hoc
shell - both scripts share the same mingw64-on-PATH requirement (needed for
cgo to build against gcc/g++, and for the resulting binary to load
libLLVM-22.dll at run time), and getting that PATH setup wrong looks like a
native crash (`0xC0000139`), not an obvious "PATH is wrong" error.

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



## Parser

`src/parser` is split by concern, not dumped into one file:
- `parser.go` - core scaffolding only: token cursor, expect/accept/sync, diagnostics, bailout/Run. No grammar rules belong here.
- `expr.go` - expression parsing.
- `stmt.go` - statement parsing.
- `decl.go` - top-level declarations.

Expression parsing uses a **Pratt parser** (operator-precedence parsing via a table keyed by `enums.Lexeme`, mapping to prefix/infix parse functions plus a precedence) - not a hand-cranked function per precedence level. Postfix operators (call `(`, index `[`, member `.`) are registered as infix table entries at the highest precedence, so they chain through the same loop as binary operators instead of a separate bolted-on postfix phase.

This area needs strong test coverage as it grows - precedence/associativity bugs are easy to introduce and easy to miss without tests backing every rule. Prefer table-driven tests asserting `Tree.Dump()` output for precedence/associativity/chaining cases.
