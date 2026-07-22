# Architecture decision log

A dated, append-only log of real design forks in this project - the "why we
did X instead of Y" narrative that doesn't fit anywhere else: too
cross-cutting for a single code comment, not language-spec material for
`LANGUAGE.md`, and not "how to work in this repo" material for `AGENTS.md`.
Entries are never edited to look like they were always right - if a
decision is later reversed, add a new entry that supersedes the old one and
say so, rather than rewriting history.

This is a decision log, not a task tracker - see `BLOCKERS.md` for
currently-open questions still needing a human call, and `AGENTS.md` for the
pointer to all of this project's docs.

---

## 2026-07-22 - Numeric type widths: six concrete kinds, `int` as an alias

**Decision:** add explicit-width integers `i8`/`i16`/`i32`/`i64` and floats
`f32`/`f64`, with `int` kept as a pure alias for `i32` (not a distinct type -
`sema.TypeInt == sema.TypeI32`, literally the same `Type` value) rather than
its own 64-bit type or a separate concept entirely.

**Why:** `main`'s real LLVM signature must return `i32` (the OS process exit
code) - keeping `int == i32` means a source-level `func main() int { return
code }` needs no truncation/cast at all, since the language's own `int` and
the platform C ABI's `int` are simply the same type (see `CODEGEN.md`'s
"`int` is 32-bit" section). No unsigned types were added alongside these -
they weren't requested and bring their own complexity (comparison semantics,
printf specifiers) that's easy to layer in later if actually wanted.

**Status:** shipped. See `LANGUAGE.md`'s Types section for the full rules,
including the untyped-numeric-constant model this made necessary (six
concrete widths made bare literals like `5` ambiguous without Go's own
untyped-constant deferral/defaulting rules).

---

## 2026-07-22 - The arena allocator: one process-lifetime bump allocator, not scoped frees

**Decision:** every codegen-level heap allocation (currently just string
concatenation) goes through one centralized, generated LLVM function
(`llvm_lang.arena_alloc`) that bump-allocates out of malloc'd 64KiB chunks,
grown for the lifetime of the process. No `free`, no refcounting, no GC -
this is a real, intentional, permanent memory leak.

**Why:** this project doesn't have a real memory-management strategy
designed yet (scoped stack-frame frees, refcounting, a tracing GC are all
still on the table - see the open entry in `BLOCKERS.md`), and inventing one
wasn't in scope for landing string concatenation. Centralizing every
allocation behind one primitive now means whichever real strategy gets
chosen later only has one call site to change, instead of having to hunt
down and retrofit scattered ad hoc `malloc` calls across the codebase. This
is explicitly groundwork for that future decision, not an attempt to
preempt it.

**Status:** shipped, and treated as the default allocation path for any
future heap-needing feature (e.g. dynamic arrays) until the real
memory-management question is answered. See `CODEGEN.md`'s "The arena
allocator" section for the mechanics.

---

## 2026-07-22 - First-class functions: fat-pointer `{fnPtr, ctxPtr}` representation

**Decision:** a function value (currently: a free-function reference only)
lowers to a two-pointer LLVM struct `{ fnPtr, ctxPtr }`, not a bare function
pointer. This round, `ctxPtr` is always null and unused - only `fnPtr` does
anything. A direct call (`add(1, 2)` where `add` is a statically-known
function name) bypasses this representation entirely and stays a plain
direct `call`, zero overhead; only a call *through* a function-typed
variable goes through the fat pointer.

**Why:** the user explicitly asked that this representation account for a
future bound-method value (`p.move` referenced without being called) even
though method values are out of scope this round - a bound method value
naturally needs to carry both a function pointer *and* the receiver address
it closes over, which is exactly the `ctxPtr` slot this representation
already has room for. Choosing the fat-pointer shape now means that future
feature can slot into the same representation and calling convention
without a later redesign/migration of every existing function-value site.

**Status:** shipped. Free functions are first-class values (reference,
assign, pass, return, call indirectly); method values remain explicitly out
of scope. See `LANGUAGE.md`'s "First-class functions" section for the
language-level rules and `CODEGEN.md`'s "First-class functions" section for
the fat-pointer construction site (`genFuncValue`) and the direct-vs-
indirect call dispatch (`isDirectFuncCall`/`genIndirectCall`).

Note (superseded): "this round, `ctxPtr` is always null and unused" above no
longer holds - the "Lambdas" entry further down added real closures, whose
`ctxPtr` genuinely carries a non-null capture-context pointer through an
indirect call. See that entry (and `CODEGEN.md`'s own equivalent correction
note on its "First-class functions" section) for the current behavior; the
original decision/rationale above is left as-is, historical-log style.

---

## 2026-07-22 - Multi-file packages: directory = package, non-recursive

**Decision:** a package is exactly "every `.llx` file directly inside one
directory" - Go's own model, adopted as-is rather than inventing a distinct
convention. Explicitly non-recursive: a subdirectory's `.llx` files are
never pulled in, even implicitly. `llvmc some/dir/main.llx` and
`llvmc some/dir` are defined to compile the identical file set (a file
argument resolves to its own containing directory - see `src/loader`).

**Why:** this is the one genuinely obvious choice here - Go developers
already know exactly what "package = directory, no recursion" means, and
this language is already deliberately Go-flavored throughout (`LANGUAGE.md`'s
own opening line). The alternative (an explicit file list in some manifest,
or a recursive package tree like a namespace hierarchy) would add real
design surface - manifest syntax, or nested-package name resolution - for a
problem Go already has a well-understood answer to, with no motivating
reason this project needs something different. Non-recursive specifically
avoids a subdirectory silently becoming part of a package it wasn't
obviously meant to belong to (e.g. a `testdata/`-style subfolder of sample
`.llx` files next to a real package).

**Status:** shipped. See `LANGUAGE.md`'s "Multi-file packages" section for
the full model, and this round's `examples/multifile/` for a real example.
Cross-package `import` syntax, actually acting on it, and the already-present
`sema.Symbol.Exported` hook becoming a real enforced rule are all explicitly
out of scope for this round - a single package is still the only unit that
exists right now.

---

## 2026-07-22 - Multi-file packages: one shared Module per package, not one Module per file

**Decision:** `codegen.GeneratePackage` lowers every file in a package into
one single `llvm.Module`, never one `llvm.Module` per file linked together
afterward.

**Why:** every file in a package always ends up needing to call into, and be
called from, every other file in that same package - there is no notion of
"this file's functions are private to it" yet (see the `Exported` entry
above - not enforced this round), so a per-file-module design would need
every cross-file call to go through an external-linkage declaration plus a
real link step (LLVM's own module linker, or an equivalent), purely to
re-assemble something that one shared module already gives for free. Since
this compiler is the only producer of every module in play - there's no
external LLVM module from some other toolchain that this needs to
interoperate with - there's no requirement to keep files as separate
compilation units at the LLVM IR level; only a *frontend* file/tree
distinction is required at all (for diagnostics, and so `ast.NodeIndex`
stays meaningful - see `sema.Symbol.Tree`'s doc comment), not a *backend*
one. One shared module is simply less machinery for a requirement (separate
files, still one compiled program) this project doesn't have.

**Status:** shipped. See `CODEGEN.md`'s "Multi-file packages: one shared
Module per package" section for the mechanics (`Generator.funcs`/`globals`/
`structLayouts` all keyed by `*sema.Symbol`/`*sema.StructInfo` pointer
identity, not `ast.NodeIndex`, which is what makes a shared module free of
extra cross-file plumbing).

---

## 2026-07-22 - Adopting `afero` for file loading

**Decision:** all disk I/O this compiler needs for multi-file package
loading (`src/loader`) goes through `github.com/spf13/afero`'s `afero.Fs`
interface rather than calling `os.ReadFile`/`os.ReadDir`/`os.Stat` directly.
Production wires in `afero.NewOsFs()`; tests build fake package layouts with
`afero.NewMemMapFs()`.

**Why:** `src/loader`'s own test suite needs to exercise several directory
shapes (multiple files, an empty directory, a file resolving to its
containing directory, an unreadable file) that would otherwise mean
creating and tearing down real temp directories on disk for every test case
- slower, and it leaves a real (if test-scoped) filesystem footprint that
has to be cleaned up correctly on every path, including a failing test. An
in-memory `afero.Fs` gives the exact same `Stat`/`ReadDir`/`Open`-shaped
interface this package already needs, so `Load` itself has zero knowledge of
which implementation it's handed - production and test code share the
identical code path, not a mocked variant that could drift from what
actually runs.

**Status:** shipped, and adopted as a standing convention going forward, not
just for this one package - see `AGENTS.md`'s new note under `## Standards`:
any future disk I/O this compiler needs should go through `afero.Fs` for the
same testability reason, rather than reaching for `os` directly out of
habit.

---

## 2026-07-22 - Cross-package imports: relative-path resolution, not a module-root/manifest scheme

**Decision:** an `import "path"` is resolved relative to the *importing
file's own directory* - confirmed directly with the user, not inferred.
`./mathutils` written in `app/main.llx` resolves to `app/mathutils`; a
different file in a different directory importing the exact same path text
resolves relative to *its own* directory instead. There is no notion of a
project/module root, no manifest file naming a module path, and no absolute
"package path" concept at all.

**Why:** this is the simplest possible scheme that still fully supports the
one thing this round needs (one package referencing another one it knows
the relative location of) - a module-root/manifest scheme (Go modules,
`go.mod`-style) adds real design surface (where does the root live, what
syntax names a module, how does a package path map back to a directory) for
a problem this project doesn't have yet: there's still no multi-repository/
external-dependency story at all, so nothing needs a globally-unique module
path to disambiguate against. Relative-path resolution reuses `src/loader`'s
already-existing directory-resolution logic almost as-is, extended to
recurse.

**Status:** shipped. See `LANGUAGE.md`'s "Imports" section and
`src/loader/program.go`'s `LoadProgram`.

---

## 2026-07-22 - Imports: one shared Module for the whole program, not real separate compilation

**Decision:** extending the existing "one shared Module per package" model
(see the two entries above) to "one shared Module for the entire program" -
every package reachable via the import graph still lowers into the exact
same single `llvm.Module`, not separate per-package modules with a real
link step.

**Why:** identical reasoning to the original one-Module-per-package
decision, one level up: this compiler is still the only producer of every
module in play (no external LLVM module from another toolchain to
interoperate with), and every cross-file lookup codegen needs
(`Generator.funcs`/`globals`/`structLayouts`) was already keyed by
`*sema.Symbol`/`*sema.StructInfo` pointer identity rather than by which file
- or, it turns out, which *package* - originally declared it. Verified, not
assumed, before shipping: `genPackage`'s five passes needed zero changes to
correctly handle a multi-package program, since none of them have any
notion of "package" to begin with - they just iterate "every tree passed
in". Real separate compilation (an object-file backend, a real linker) isn't
a need this project has yet, and building one purely to say packages are
"really" separately compiled would be speculative machinery with no
motivating requirement behind it.

**Status:** shipped. See `CODEGEN.md`'s "Imports: still one shared Module,
now for the whole program" section.

---

## 2026-07-22 - Imports: no aliasing syntax this round

**Decision:** `import "./mathutils"` always binds its path's own last
segment as the local name (`mathutils`) - there is no `import m
"./mathutils"` form to pick a different local name.

**Why:** not needed for this round to be a complete, usable feature -
every real use case this round targets (one package calling into another
it doesn't already have a name collision with) works fine without it.
Deliberately deferred rather than designed-then-unused: aliasing is a small,
additive grammar extension (an optional identifier before the path string in
`ImportDecl`) that can be layered on later with no rework of the
path-resolution/binding machinery this round already built - it wasn't worth
the extra grammar/scope-binding surface now on spec alone.

**Status:** deferred, not shipped. See `LANGUAGE.md`'s "Imports" section.

---

## 2026-07-22 - Struct constructors: overloading scoped to constructors only, no Go precedent

**Decision:** a struct may declare one or more `constructor(params) { body }`
blocks nested directly inside the struct declaration, invoked via
`Name(args)` call syntax (distinct from the unchanged `Name{...}` composite
literal). Multiple constructors on the same struct are overloaded by
argument count only - not by argument type - and this overloading is
explicitly, deliberately scoped to constructors alone: it is **not** a
precedent for general function/method overloading anywhere else in the
language. Two free functions or two methods sharing a name remain a
redeclaration error, unchanged.

**Why:** this is a genuine language-design fork with no Go precedent to fall
back on, unlike almost everything else built this session (numeric widths,
first-class functions, multi-file packages, imports - each of those had a
direct, uncontroversial Go analogue to adopt as-is). Go itself has no
constructors and no overloading of any kind, so this couldn't be settled by
"do what Go does" the way every other round this session was. The user's own
reasoning, stated explicitly when confirming this design: scoping the
overload resolution to constructors specifically keeps it "bound to the type
explicit and differ it from a regular function" - a constructor call is
already type-directed (its own struct type name is the callee), so
overloading it by arity is a contained, narrow special case rather than an
opening for arbitrary function/method overloading, which brings its own,
much larger set of design questions (name mangling, overload resolution
ambiguity across argument *types* not just counts, interaction with
first-class function values) that were never asked for and remain
deliberately out of scope.

**Status:** shipped. See `LANGUAGE.md`'s "Constructors" section for the full
language-level rule and `CODEGEN.md`'s "Constructors" section for the
lowering (each constructor becomes its own real LLVM function, reusing the
implicit-receiver-pointer convention an ordinary method already uses, named
`Struct.constructor.N` since a constructor has no name of its own to draw
from).

---

## 2026-07-22 - Dynamic arrays: `append` scoped to exactly one element per call

**Decision:** `append(slice, elem)` takes exactly one element to append, not
Go's full variadic `append(s, e1, e2, ...)` form.

**Why:** this language has no variadic functions anywhere yet, and inventing
that machinery (parameter-list syntax, argument-count checking against an
open-ended arity, how it'd interact with the existing fixed-arity function-
type grammar - see `LANGUAGE.md`'s "First-class functions" section) purely
to give `append` a multi-element form was out of scope for this round.
Appending several elements is simply several calls (`s = append(s, a); s =
append(s, b)`), which is no less correct, just more verbose at the call
site - a real, deliberate restriction, not an oversight, and a natural
extension point once (if) variadic functions are ever designed generally: at
that point `append` would be the obvious first beneficiary, not a special
case needing its own bespoke retrofit.

**Status:** shipped. See `LANGUAGE.md`'s "Dynamic arrays" section for the
language-level rule and `CODEGEN.md`'s "Dynamic arrays" section for
`genAppendCall`'s lowering.

---

## 2026-07-22 - Dynamic arrays: capacity growth by simple doubling

**Decision:** `append`'s growth strategy, once a slice's `len == cap`, is
`newcap = max(1, cap*2)` - the simplest possible doubling strategy, not
Go's own more elaborate real-world growth curve (which slows its growth
factor for larger slices to bound total copying overhead across a program's
lifetime, and has changed more than once across Go's own releases).

**Why:** this project doesn't have a performance-sensitive workload driving
slice growth yet, and Go's own tuned curve is itself an incidental
implementation detail of Go's runtime, not a language-semantic guarantee -
nothing about `append`'s observable behavior (same-backing-pointer-when-it-
fits, a fresh buffer with old data preserved when it doesn't) depends on
*which* doubling/growth curve is used, only on `cap` ending up large enough.
Simple doubling is the smallest correct implementation that satisfies that
contract, and is a well-understood, easy-to-revisit default if a real
workload ever demonstrates it matters.

**Status:** shipped. See `CODEGEN.md`'s "Dynamic arrays" section for
`genAppendCall`'s exact lowering (`newcap = max(1, cap*2)`, built via a
`select` on `cap*2 < 1`).

---

## 2026-07-22 - Lambdas: a uniform, `ctxPtr`-first ABI for every indirectly-called function value

**Decision:** once genuine closures (function-literal expressions with
by-reference capture - see `LANGUAGE.md`'s "Lambdas" section) exist, a free
function referenced as a bare value no longer puts its own real function
address into the `{fnPtr, ctxPtr}` fat pointer's `fnPtr` field - it puts the
address of a small, per-function, memoized adapter ("thunk") instead, whose
real signature has an extra leading `ctxPtr` parameter it simply ignores
before calling straight through to the real function. Every genuine lambda's
own synthesized function already has this same `ctxPtr`-first shape natively
(it needs to actually dereference `ctxPtr` to reach its captures). Every
*indirect* call now unconditionally extracts and passes `ctxPtr` along as
the real callee's first argument; a *direct* call to a statically-known
function name is completely unaffected either way - it never touches the fat
pointer at all.

**Why:** a single `func(T1, T2) R`-typed variable can hold either kind of
function value at runtime - a plain free-function reference or a genuine
closure - and an indirect call through it has no way to tell which, at the
call site, before it emits its one call instruction. But the two kinds'
*real* underlying LLVM functions have genuinely different natural
signatures: a free function's has no `ctxPtr` at all (necessary to keep a
*direct* call to it zero-overhead - see the "First-class functions" entry
above), while a lambda's real function must take a real, dereferenced
`ctxPtr` to reach its own captures. Calling through a function pointer whose
real callee has a different real parameter list than the call site built is
not "probably fine" - it's invalid, UB-risking IR that can silently corrupt
the stack/registers at runtime rather than fail cleanly, exactly the kind of
subtle miscompilation this project's rigor (see e.g. the `i64` printf-format-
specifier entry, verified empirically rather than assumed) treats as
unacceptable to leave unresolved. A per-free-function thunk, built lazily
and memoized rather than reused-if-possible-else-regenerated, is the
standard technique real closure-supporting language implementations use for
exactly this "uniform calling convention across heterogeneous callees"
problem - it costs nothing at all for a direct call (the thunk isn't even
built unless some code actually takes that function's address as a bare
value) and only a single small adapter call for an indirect one, which
already had fat-pointer overhead anyway.

**Status:** shipped. See `CODEGEN.md`'s "Lambdas" section (the "uniform-ABI
thunk" subsection) for the exact mechanism (`genFuncThunk`/`genFuncLit`/
`genIndirectCall`, `src/codegen/expr.go`), and
`TestUniformAbiAcrossPlainFunctionAndLambda` (`src/codegen/lambda_test.go`)
for the regression test that exercises a single func-typed variable holding
each kind of value in turn, calling it indirectly both times - the test that
would have caught this exact class of bug had the fix been wrong or missing.

---

## 2026-07-22 - Pointers: `new`/`delete` get a real, separate heap from the arena

**Decision:** `new T(args)`/`new T{...}` mallocs its own individually-sized
block directly (a plain libc `malloc` call, not routed through
`llvm_lang.arena_alloc`), and `delete p` frees exactly that block via a real
libc `free` call. The bump-allocator arena (string concatenation, dynamic
arrays) is completely untouched by either - two genuinely separate heaps,
not one heap with two access paths into it.

**Why:** the arena's own design (see the arena entry above) is a bump
allocator over 64KiB chunks with no notion of "give this one sub-allocation
back" at all - freeing a single `new`'d value out of a chunk other, still-
live allocations share would either be a no-op (defeating the point of
`delete`) or require retrofitting real free-list bookkeeping onto a
primitive deliberately kept as simple as possible for its own use case
(string concatenation and dynamic arrays never need to free a single
element early - they leak for the process's lifetime by design, an accepted
trade-off documented in the arena's own entry). Reusing a bare
`malloc`/`free` pair instead needs none of that: it's the direct, obviously-
correct mapping from "the user asked for exactly this much heap memory
back" to the real system call underneath, has clean, unambiguous semantics
per-allocation, and doesn't touch or complicate the arena's own bump-pointer
invariants at all. The two heaps mixing would be the actual bug to avoid,
not a caveat to work around.

**Status:** shipped. See `CODEGEN.md`'s "Pointers" section for the mechanism
(`genNewExpr`/`genDeleteStmt`) and the arena section for why the arena itself
still has no per-allocation free. Destructors/RAII (running a struct's own
cleanup logic automatically at `delete` time) are explicitly out of scope
for this round - `delete` only frees raw memory; there is no such concept
in this language yet, and adding one is a separate, larger design question
left for later.

---

## 2026-07-22 - Pointers: `nil` scoped to pointer types only, not a general zero value

**Decision:** `nil` is a predeclared, untyped placeholder value usable only
where a pointer type is expected (a `*T` variable's initializer, either side
of `==`/`!=` against a pointer) - it is not a general "zero value" concept
usable against a struct, array, numeric, string, or bool type the way, say,
a Go `interface{}` holding `nil` or a `nil` map/slice would suggest.

**Why:** this language has no interface/`any` type and no reference-typed
non-pointer value (a struct/array/string is always a real concrete value,
never something that could itself be absent) - the *only* thing that can
meaningfully have "no value" is a pointer, so there was no reason to design
a broader zero-value concept just to give `nil` a home. Modeling it as its
own narrow untyped-constant kind (`sema.TypeUntypedNil`, deliberately kept
out of the existing `IsUntyped()`/`IsNumeric()` predicates every numeric
untyped-constant code path already assumes "untyped" means "numeric")
reused this language's existing untyped-constant deferral machinery
(`checkAssignable`/a dedicated `checkNilEquality`) almost entirely as-is,
rather than inventing a second, parallel mechanism. Unlike an untyped
numeric constant, `nil` was deliberately given **no default type** - Go
itself allows `var x interface{} = nil` with no further context because
`interface{}` is nil's own natural home; this language has nothing
equivalent, so a context that never pins down a concrete `*T` (`p := nil`)
is a real error rather than a silently-accepted default to some arbitrary
pointer type.

**Status:** shipped. See `LANGUAGE.md`'s "Pointers" section for the exact
rules and `sema/pointer_test.go` for the coverage (including the rejected
cases: bare `:= nil`, `nil == nil`, `nil` against a non-pointer).

---

## 2026-07-22 - Pointers: auto-deref scoped to member access only, not indexing

**Decision:** `p.field`/`p.method(...)` on a `*T` auto-dereferences (behaves
exactly like `(*p).field`/`(*p).method(...)`), matching Go's own automatic
pointer-dereference rule for selector expressions - but indexing does not:
`p[0]` on a `*[N]T` is rejected; `(*p)[0]` is required.

**Why:** Go itself only auto-derefs for selector expressions (`.field`/
method calls), never for indexing (`p[0]` on a `*[N]T` is also a compile
error in real Go) - this isn't a narrower carve-out invented for this
project, it's matching the same precedent exactly. Member access auto-deref
pulls its weight because a pointer-to-struct is the overwhelmingly common
shape once `new` exists at all (a heap-allocated struct is *always* accessed
through a pointer - forcing `(*p).field` at every call site would make `new`
noticeably more awkward to use than it needs to be for no real benefit).
Indexing through a pointer-to-array is a rarer shape by comparison, and Go's
own choice not to special-case it suggests the inconsistency-avoidance isn't
worth it there either - extending auto-deref to indexing was simply out of
scope rather than rejected on its own merits; it can be added later if a
real need for `[N]T` behind a pointer turns out to be common enough to
justify it.

**Status:** shipped, scoped as described above. See `LANGUAGE.md`'s
"Pointers" section and `sema/pointer_test.go`'s
`TestNewCompositeLitProducesPointer` for the explicit `(*a)[0]` case this
implies.
