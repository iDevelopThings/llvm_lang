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

---

## 2026-07-22 - Non-constant global initializers: declaration order, not a dependency-graph topological sort

**Decision:** every non-constant top-level `var`'s real initializer now runs
in one synthesized init function, in plain **source declaration order**
across the whole package (each file in the order it's processed, each file's
own globals in the order they're written) - not a full dependency-graph
topological sort the way Go's own spec actually requires for package-level
variable initialization (Go orders strictly by each variable's dependencies,
so `var a = b + 1; var b = 2` initializes `b` before `a` regardless of
which is written first).

**Why:** a real topological sort needs a dependency graph built from which
other globals each initializer's expression references (transitively, through
any function it calls too, in general), plus cycle detection for when that
graph doesn't have one - genuinely more analysis machinery than this round's
actual goal (lifting the "must be a compile-time constant" restriction so a
global's initializer can be an arbitrary expression at all, matching Go's
*shape* of the feature) needed to justify building right away. Declaration
order is simple, predictable, and correct for the overwhelmingly common case
(a global's initializer depending only on globals already declared above it,
or on nothing but function calls) - it only diverges from Go's real
semantics in the narrower case of a global's initializer referencing another
global declared *later* in the same package, which now deterministically
observes that other global's zero value rather than Go's own
however-it-actually-depends result.

**Status:** shipped, with this scoping deliberately called out as narrower
than Go's real behavior (not an oversight) - see `CODEGEN.md`'s "Global var
initializers" section for the exact mechanism (`@llvm.global_ctors` plus one
synthesized `llvm_lang.global_init` function, `src/codegen/globalinit.go`)
and `src/codegen/globals_test.go`'s
`TestGlobalNonConstantInitializersRunInDeclarationOrder` for the passing
declaration-order behavior asserted directly. A real dependency-graph sort
(matching Go's spec exactly) remains a reasonable future upgrade if a program
ever needs it, without changing anything about the language-level feature
itself - only which order this same synthesized function runs its stores in.

---

## 2026-07-22 - Slicing: reusing `{ptr, len, cap}`/`{ptr, len}` directly, and a range check generalized from `genBoundsCheck`

**Decision:** a slice expression (`s[a:b]`, `LANGUAGE.md`'s "Slicing"
section) never allocates or copies anything - it builds a fresh `{ptr, len,
cap}` (dynamic array) or `{ptr, len}` (string) value directly from its
operand's own already-existing fields (or, for a fixed array, its own real
address), reusing the exact same struct shapes `make`/string literals
already use rather than inventing a distinct "slice view" representation.
The single-index bounds check (`genBoundsCheck`) is generalized to a genuine
range check (`genSliceRangeCheck`: `0 <= low <= high <= max`) rather than
reusing `genBoundsCheck` twice (once for `low`, once for `high`) - two
separate single-bound checks can't express `low <= high` at all, which is
exactly as real a violation as either endpoint being individually
out-of-range (`s[3:1]` must trap even though both `3` and `1` are
individually in-range for a length-5 slice).

The one genuinely non-obvious wrinkle: for a dynamic array specifically, the
omitted-high *default* and the range check's own upper *bound* are
deliberately two different values - `len(s)` and `cap(s)`, respectively.
Getting this backwards (defaulting the high bound to `cap(s)` instead of
`len(s)`) would silently expose a slice's spare capacity to any bare
`s[a:]`, which is not what Go itself does (confirmed directly against Go's
own spec before implementing, per this task's own explicit instruction not
to get this backwards) - Go's real rule is "a reslice's upper bound is
allowed to reach into spare capacity only when written explicitly", not "an
omitted upper bound reaches into it automatically". `genSliceBounds`
threads `defaultHigh` and `max` as two separate parameters for exactly this
reason, even though they happen to coincide for a string/fixed-array operand
(neither has a separate capacity concept, so both are simply `len`/`N`).

**Why:** no allocation/copy matches this feature's entire reason for
existing (`LANGUAGE.md`'s own opening line: "a slice expression produces a
new header value sharing the same backing memory as the original") - copying
would be a strictly different, and strictly less useful, feature. Reusing
the existing struct shapes needed no new LLVM type and no change to
`len`/`append`'s own existing logic at all (a sliced value is simply an
ordinary value of the same type afterward - see `CODEGEN.md`'s "Slicing"
section). A dedicated range-check helper (rather than composing two
single-bound checks, or generalizing `genBoundsCheck` itself to take a
lower bound parameter) keeps `genBoundsCheck`'s own existing call sites
(plain indexing) completely unchanged, while still sharing its exact
trap/`unreachable` mechanism and `CreateCondBr` basic-block shape.

**Status:** shipped. See `CODEGEN.md`'s "Slicing" section for the three
lowering paths (`genDynArraySlice`/`genStringSlice`/`genFixedArraySlice`,
`src/codegen/expr.go`) and `genSliceRangeCheck`/`genSliceBounds`'s own doc
comments for the range-check/defaulting mechanics; `TestSliceDynamicArrayReslicePastLenWithinCap`
(`src/codegen/slice_test.go`) is the regression test that would catch the
len-vs-cap default direction being flipped.

---

## 2026-07-22 - Destructors: blanket non-copyable rejection instead of move semantics/last-use analysis

**Decision:** a struct may declare one `destructor() { body }`, and declaring
one makes that struct (and anything transitively embedding it by value)
**non-copyable, full stop** - every copy of an *existing* live value is
rejected (`b := a`/`b = a`, a by-value field/array-element store), with
**no** "this happens to be the last use of the variable, so the copy is
actually safe" leniency anywhere. A destructor fires at exactly two
triggers - a plain local/parameter's own scope exit (return/break/continue/
fall-through, reverse declaration order) and `delete` against a pointer to
it - and does **not** automatically cascade into a by-value-embedded field
lacking its own destructor (see the "known limitation" below).

**Why (non-copyable rule):** the alternative that would make full copy-freedom possible - real
move semantics (a value's ownership transfers on its last use, the source
binding becoming invalid) or a last-use/liveness analysis deciding exactly
which copy is "the" final one - is a substantially harder feature: it needs
a real liveness/escape analysis over the whole function body (not just
per-statement type-checking, which is all this pass otherwise ever does),
a way to mark a moved-from binding as unusable afterward, and careful
interaction with every existing construct that can alias a value (structs,
arrays, function calls, loops). This was discussed explicitly and scoped
down on purpose: blanket non-copyable rejection sidesteps the double-
destruction problem entirely (if a value can never be duplicated, there is
only ever one instance of it, so "when does it destruct" is never
ambiguous) without needing any of that machinery. The one deliberate carve-
out - a *fresh* composite-literal/constructor-call/`new` construction is
never "a copy," so it stays legal even for a non-copyable type - falls out
of the same reasoning: constructing the one instance isn't duplicating an
existing one, so no soundness question is being dodged by allowing it.

Argument-passing gets this same fresh-construction exception (unlike a
return statement's value, which allows none at all, even a fresh value) for
a related but distinct reason: a fresh argument's soundness is entirely
local to the one call expression (the callee's own parameter becomes the
value's sole owner, with nothing else anywhere still referencing it),
whereas soundly allowing a fresh *return* would require knowing, at every
call site consuming the result, that the callee always hands back a freshly
-owned instance - a small but real escape-analysis question of its own,
avoided by simply not allowing it. This is what forces "a resource-owning,
destructor-having type only really moves across a function boundary through
a `new`'d pointer" - the deliberate, accepted trade-off named directly in
`LANGUAGE.md`'s "Destructors" section.

The second scoping choice bundled into this same decision: **no automatic
recursive destruction through an embedded field either** - if struct
`Outer` embeds a destructor-having `Inner` by value as a plain field, and
`Outer` declares no `destructor()` of its own, `Inner`'s destructor simply
never fires when an `Outer` value's own scope ends.

**Why (embedded fields):** cascading destruction through arbitrary by-value-embedded fields
(and, transitively, through fields-of-fields, arrays of structs, etc.) is
real, general RAII - a much larger feature than this round's, and the
harder problem this task was explicitly scoped to avoid taking on. The
intended, documented pattern for a resource-owning type stays simple
instead: hold a `*T` **pointer** field to what it owns, and manually
`delete` it from the containing struct's own `destructor()` body (see the
`FileHandle` example in `LANGUAGE.md`'s "Destructors" section) - one level
of manual wiring, not a general cascading mechanism.

**Status:** shipped. See `LANGUAGE.md`'s "Destructors" section for the full
language-level rule (non-copyable propagation, the two firing triggers, the
known-limitation callout) and `CODEGEN.md`'s "Destructors" section for the
lowering - in particular `genIfStmt`'s then/else destructor-stack save/
restore, the one genuinely subtle part: caught directly by
`TestDestructorFiresOnFallThroughReturn`/`TestDestructorFiresOnBreak`
(`src/codegen/destructor_test.go`) failing before that fix existed, since
`if`/`else` are alternate, mutually-exclusive codegen-time continuations
from the same starting point, not a sequential continuation of each other
the way two statements in one `Block` are.

---

## 2026-07-23 - JIT execution: LLJIT (ORCv2) instead of the legacy MCJIT `ExecutionEngine`

**Decision:** `cmd/llvmc`'s `jitRunMain`, and every JIT-executing test helper
in `src/codegen` (`compileAndJIT`, `compilePackageAndJIT`,
`compileProgramAndJIT`), now JIT-execute through `go-llvm`'s LLJIT bindings
(`third_party/go-llvm/orcjit.go` - ORCv2, LLVM's current JIT infrastructure)
instead of the legacy MCJIT-based `ExecutionEngine`
(`third_party/go-llvm/executionengine.go`) this project used until now.

**Why:** MCJIT is unmaintained upstream - LLVM itself documents ORCv2/LLJIT
as its replacement (see https://llvm.org/docs/ORCv2.html). The switch
surfaced one real, non-drop-in gap: LLJIT has no equivalent of
`ExecutionEngine.RunStaticConstructors()`, which this project relied on to
run `@llvm.global_ctors` (see `CODEGEN.md`'s "Global var initializers"
section) before `main`. This is solved by looking up and calling
`llvm_lang.global_init` directly by name instead - the exact same
synthesized function `@llvm.global_ctors`'s own single entry already points
at - rather than relying on the generic ctors-array-walking convenience
MCJIT provided. That needed one small companion change: `buildGlobalInitFn`
(`src/codegen/globalinit.go`) no longer gives that function private linkage,
since a private symbol has no name a JIT's `Lookup` can resolve at all - it
now keeps `AddFunction`'s own default (external), the same as every other
language-level function. The `@llvm.global_ctors` array itself is left in
place, unused by the JIT path now, since it's still the correct mechanism
for a real linked/loaded program's C runtime startup sequence - a future
AOT/native-executable output path would still need it.

The disposal/ownership model also changed shape: MCJIT's
`NewExecutionEngine` only ever took ownership of the `Module`, leaving the
owning `Context` for the caller to dispose separately (`engine.Dispose()` +
`mod.Ctx.Dispose()`, two calls, two failure modes). LLJIT's
`ThreadSafeContext`/`ThreadSafeModule` wrapping instead folds both into the
LLJIT instance's own ownership, so disposing it (`jit.Dispose()`) alone
tears down the module and context together, in the correct order - one call
where MCJIT needed two.

Two further real, empirically-verified (not assumed) gotchas surfaced while
making the switch - see `CODEGEN.md`'s "A MinGW/GCC ABI quirk: implicit
`__main()` calls" section for the full write-up of both:

- LLVM's backend auto-inserts a call to `__main()` at the very start of any
  function literally named `main`, when compiling for this project's own
  `*-windows-gnu` (mingw64) host - a real MinGW/GCC ABI compatibility
  convention, unrelated to this project's own `@llvm.global_ctors`
  mechanism, that MCJIT's own target selection apparently never triggered.
  Worked around by binding `__main` to libc's own `rand` via
  `AbsoluteSymbols`/`JITDylib.Define` (`cmd/llvmc`'s `bindMinGWMainThunk`,
  mirrored in `src/codegen`'s test helpers) - this is exactly the kind of
  thing the "practical LLJIT surface" scoping decision on the `go-llvm`
  bindings side turned out to need almost immediately.
- LLJIT's compile layer empties the source IR module out once compiled to
  machine code, unlike MCJIT (which kept it intact for its whole lifetime) -
  calling `Module.String()` again after a JIT-executed call returns just the
  bare module header, and this was observed to crash outright in at least
  one case. `src/codegen`'s `jitModule` test helper now captures a module's
  IR text once, up front, before it's ever handed to an LLJIT instance.

The actual "call a JIT'd function" mechanism didn't change at all:
`LLJIT.Lookup` hands back a raw address exactly like
`ExecutionEngine.GetFunctionAddress` did, so the existing
`syscall.SyscallN`-based call-through-raw-address approach (see
`src/codegen/codegen_test.go`'s `runInt32` doc comment) carries over
unchanged - actually invoking a resolved address is deliberately out of
scope for both JIT engines' own C APIs alike, so this project was always
bringing its own call mechanism regardless of which one it used.

**Status:** shipped. See `CODEGEN.md`'s "Global `var` initializers" and "A
non-obvious disposal detail" sections for the mechanics.

---

## 2026-07-23 - For-loop header variables: per-iteration capture (Go 1.22+), diverging from this project's own prior implicit behavior

**Decision:** a `for` loop's own init-clause variable (`for i := 0; ...;
i++`), when captured by a lambda created inside the loop's body, now gets a
fresh per-iteration value - confirmed with the project owner as a deliberate
divergence from this project's own prior, pre-1.22-Go-style behavior (one
shared slot, mutated in place by the post-clause, observed at whatever value
it holds when the closure is finally called) to match modern Go instead.

**Why:** the shared-slot behavior is exactly Go's own infamous
closures-in-a-loop gotcha - confirmed by manual dogfooding here too, not
hypothetical: `for i := 0; i < 5; i++ { fns = append(fns, func() int {
return i }) }` printed `5,5,5,5,5` (every closure sharing one slot the loop's
own `i++` had already driven to its final value) instead of the obviously-
intended `0,1,2,3,4`. Go itself changed this exact behavior in 1.22 (each
iteration gets its own variable) specifically because it was such a common,
confusing footgun; there was no reason for a brand-new language to
deliberately re-introduce a mistake an established one already spent years
walking back. The root cause was purely a codegen accident, not a deliberate
semantic choice up to this point: `genForStmt` (`src/codegen/stmt.go`) only
ever calls `genStmt` on the init clause once, before the loop's own
`for.body`/`for.post` basic blocks even exist (see `preInitBase`'s own doc
comment) - so a captured init variable's arena-heap slot (`allocLocalSlot`,
`src/codegen/func.go`) only ever gets written to by that one `arena_alloc`
call, unlike a variable freshly declared *inside* the loop body (`captured
:= i`), whose own declaring statement genuinely re-executes, and so
genuinely re-allocates, on every dynamic iteration.

**What changed:** `genForStmt` now does two symmetric hand-offs around the
loop body, only for an eligible variable (init declares exactly one name,
that name's `sym.Captured` is true, and its type isn't non-copyable - see
below): entering the body, it copies the loop variable's current value into
a fresh arena slot and repoints `g.locals[sym]` at it, so the body (and any
lambda inside it) reads/writes that iteration's own private copy; entering
the post-clause block (reached by both the ordinary fallthrough and every
`continue` alike), it copies that value back into the loop variable's real,
original storage before the post-statement (`i++`) runs, so the condition
and post-clause keep observing the one real slot exactly as before - a body
mutation of `i` still correctly reaches the next iteration's check.
`break` bypasses this entirely (branches straight past the loop, same as
before), needing no special handling.

Deliberately excluded: a non-copyable loop variable (a struct with its own
`destructor()`, or a fixed-size array of one) never gets this treatment,
falling back to today's exact shared-slot behavior instead - giving it
fresh per-iteration semantics would mean an implicit copy once per
iteration, which would silently violate this project's own "non-copyable,
zero exceptions" rule (see `LANGUAGE.md`'s "Destructors" section) that every
other part of this codebase already enforces without exception. Nothing
today can actually construct this case (no non-copyable type can be a `for`
loop's own header variable in the first place), so this is a defensive
guard against a future grammar change, not a live gap.

**Status:** shipped. See `LANGUAGE.md`'s "Lambdas" section for the
user-facing semantics (including the non-copyable exclusion) and
`src/codegen/stmt.go`'s `genForStmt`/`typeIsNonCopyable` for the
implementation, with new coverage in `src/codegen/lambda_test.go`
(`TestForLoopCapturedHeaderVariableGetsPerIterationValue` and its
sibling tests covering `continue`, `break`, and nested loops).

---

## 2026-07-23 - External functions (FFI): a general `extern func` mechanism instead of one-off builtins

**Decision:** add a general-purpose FFI declaration, `extern func Name(params)
RetType` at top level - a real, user-declarable binding to an external C
symbol, resolved at JIT-execution time - rather than hand-rolling one more
bespoke compiler builtin the way `print`/`make`/`append`/`len` already are.

**Why:** the immediate need (`ScopeTimer`, a RAII-style timing helper,
needing Windows' `QueryPerformanceCounter`/`QueryPerformanceFrequency`) could
have been solved with a fifth hardcoded builtin, exactly like the existing
four - but every one of those already required its own bespoke parser/sema/
codegen special-casing, and a fifth one-off would only buy exactly one more
function, forever. A general mechanism costs barely more to build (this
project's own internal externs - `printf`/`malloc`/`free`/`memcpy`/`memcmp`/
`memset`/`llvm.trap`, `src/codegen/runtime.go` - were already just
`llvm.AddFunction` calls with no body; this feature is that same primitive,
made user-declarable) and buys something the one-off approach never could:
a future "standard library" can simply be ordinary `.llx` packages wrapping
`extern func` declarations behind the existing import system (see
`LANGUAGE.md`'s "Multi-file packages"/"Imports" sections) - no new language
concept needed for that, ever again.

A brand-new, separate `ExternFuncDecl` AST node kind was chosen over a
nullable-body `FuncDecl` variant specifically because `FuncDecl`'s own
always-has-a-body invariant is depended on unconditionally by a large amount
of already-shipped code (`resolveFuncBody`, `checkFuncDecl`'s return-flow
analysis, `genFuncBody`'s whole lowering pass) - see `CODEGEN.md`'s own
"External functions (FFI)" section for the full reasoning, matching this
project's own established precedent (`ConstructorDecl`/`DestructorDecl` were
each their own new node kind for the identical reason, not shoehorned into
`FuncDecl`).

This round's scope was deliberately narrowed on several axes, each a
plausible-but-separate follow-up rather than a forgotten piece:

- **Type-allowlist, not general marshaling.** Only a numeric type, `bool`, or
  a pointer type may cross an extern func's signature - `string`, a struct by
  value, a dynamic array, and a function type are all rejected with a real
  diagnostic. Each of the four rejected shapes is really a small fat struct/
  closure in this compiler's own representation, not a single scalar/pointer
  value a real C caller would recognize - solving "how does a `{ptr,i32}`
  string cross a real C ABI boundary" is a genuinely separate, harder problem
  (does the C side expect null-terminated? whose responsibility is it to
  allocate?) that would have blocked landing the much narrower, already-
  motivated pointer/numeric case indefinitely.
- **No rename/alias syntax.** The declared name is the linked symbol name,
  verbatim, this round - deferred rather than designing a syntax
  (`extern func Name = "realSymbol"(...)`?) with no concrete motivating case
  yet.
- **No variadic extern functions, no `extern var`.** Neither was needed by
  the motivating case; both are separate mechanisms with their own design
  questions (a variadic C call's own argument-promotion rules; how an
  external global's storage/initialization would even work under this
  project's JIT execution model) not worth answering speculatively.
- **No non-Windows platform consideration.** This project currently only
  targets Windows/mingw64 at all (see `AGENTS.md`/`SETUP.md`) - there is no
  second platform yet for a platform-conditional extern declaration to
  matter against.

**Status:** shipped. See `LANGUAGE.md`'s "External functions (FFI)" section
for the full language-level rule and `CODEGEN.md`'s own section of the same
name for the lowering (in short: `declareExternFuncSignature` populates the
exact same `Generator.funcs` map an ordinary `FuncDecl` does, so every
existing direct-call codegen path needed zero changes), plus the new
`examples/scope_timer` worked example and test coverage across
`src/parser`/`src/sema`/`src/codegen`.

---

## 2026-07-23 - AOT compilation (`-o`): shelling out to `gcc`, not a vendored/hand-written linker

**Decision:** `llvmc -o <output> <program>` emits a native object file via
the vendored `go-llvm` bindings' already-present target-machine support
(`Target.CreateTargetMachine`/`TargetMachine.EmitToMemoryBuffer(mod,
llvm.ObjectFile)` - `third_party/go-llvm/target.go`, zero vendored-binding
changes needed), writes it to a temporary file, and links it into a real
`.exe` by shelling out to `gcc` (`os/exec.Command("gcc", objPath, "-o",
outputPath)`) - the same mingw64 toolchain this project already requires on
`PATH` for cgo/dev work (see `AGENTS.md`'s "Compiling" section) - rather than
reimplementing a linker of this project's own, or vendoring one (e.g.
LLVM's own `lld`).

**Why:** this project's entire toolchain story already depends on mingw64
being present on `PATH` for an unrelated reason (cgo needs `gcc`/`g++` to
build against `libLLVM-22.dll` itself) - requiring it again for the one new
purpose an AOT output mode needs (turning an object file into a real
executable) adds no new environmental dependency at all, just a new use for
one already required. Writing a linker (even a minimal one, understanding
just enough of PE/COFF object format and mingw64's own CRT startup/import-
library conventions to produce a working `.exe`) is a substantial, genuinely
separate engineering effort with its own large surface of platform-specific
correctness questions this round had no reason to take on, when `gcc`
already solves it completely, for free, using infrastructure this project's
own build already assumes exists. This also gets the two "does an extern
symbol actually resolve" cases correct automatically and for free: ordinary
libc symbols (`printf`/`malloc`/etc., already declared via `AddFunction` in
`runtime.go`) and any user-declared `extern func` binding to a real Win32 API
export (`kernel32.dll`, etc.) both resolve through mingw64's own standard
import libraries exactly the way a hand-written C program calling the same
APIs already would - no special linking flags needed for either, confirmed
concretely (`TestBinary_AOT_ExternFuncScopeTimer`,
`cmd/llvmc/main_test.go`), not assumed just because the JIT's own separate
runtime process-symbol generator already handles this case a different way.

**The temporary object file goes through a plain `os.CreateTemp`/`os.Remove`,
not this project's own `afero.Fs` convention** (see `AGENTS.md`'s "Standards"
section, and its own dated `DECISIONS.md` entry): that convention exists
specifically so `src/loader`'s own tests can fake a package's *input* file
layout on `afero.NewMemMapFs()` instead of real temp directories - a concern
that doesn't apply here at all. This is a single, ephemeral, write-only
scratch file for a CLI-only link step, with no test needing to fake its
contents, immediately removed once `gcc` has read it - a narrow, deliberate,
explicitly-documented exception, not a quiet, unexplained departure from a
standing convention.

**`main`'s own LLVM signature needed no change at all - verified concretely,
not assumed.** The instinctive design (mirroring a standard C entry point)
would give `main` a real `(argc, argv)` parameter pair for the AOT path.
This was deliberately *not* done: `main`'s LLVM signature is looked up and
called with zero arguments by both `cmd/llvmc`'s own `jitRunMain` and dozens
of this package's own `jm.runInt32(t, "main")` test call sites across
`src/codegen` (a real, wide blast radius, not a hypothetical one) - changing
what `main` itself takes would have forced every one of those raw
`syscall.SyscallN` call sites to suddenly pass two real, meaningful
arguments instead of none, a real regression risk explicitly avoided once a
working alternative existed (see the `args()` entry immediately below for
what that alternative is, and why). Confirmed directly, not just reasoned
about: `TestBinary_AOT_HelloWorld` et al. AOT-compile and run real, standalone
executables successfully with `main`'s signature completely unchanged -
mingw64's own CRT calling a zero-parameter `main()` with argc/argv/envp it
simply never reads is ordinary, valid C-ABI behavior, not a hazard.

**The `-o` flag itself** was named to mirror gcc/clang's own long-established
convention for exactly this purpose (`gcc foo.c -o foo`), consistent with
this project's own existing `-emit-llvm` flag precedent (a long, descriptive
name for a less common, debugging-oriented flag; a short, conventional one
for the everyday "compile to a file" case).

**Status:** shipped. See `CODEGEN.md`'s new "`-o`: AOT compilation to a
native executable" section for the full mechanism
(`compileToExecutable`, `cmd/llvmc/main.go`) and its exit-code table update,
and `cmd/llvmc/main_test.go`'s `TestBinary_AOT_HelloWorld`/
`TestBinary_AOT_Features`/`TestBinary_AOT_ExternFuncScopeTimer`/
`TestBinary_AOT_Args` for the real, standalone-process acceptance tests this
round's own verification leaned on.

---

## 2026-07-23 - `args()` builtin: `__argc`/`__argv` CRT globals instead of changing `main`'s ABI, and an empty slice under JIT

**Decision:** the predeclared `args() []string` builtin does **not** read
real argc/argv through `main`'s own parameters (`main`'s LLVM signature stays
the exact same parameterless `i32 @main()` it always was - see the `-o`
entry above for why changing it was rejected). Instead, `buildArgsInitFn`
(`src/codegen/args.go`) reads two plain extern globals, `__argc`/`__argv` -
the same well-established MSVCRT/mingw64 C-runtime extension a real,
hand-written C/C++ program on this platform already relies on
(`extern int __argc; extern char **__argv;`, from `<stdlib.h>`), populated
by the CRT's own startup sequence before `@llvm.global_ctors` or `main`
itself ever run - and marshals them into a freshly arena-allocated
`[]string`, stored into a private `llvm_lang.args` global once, via a
synthesized ctor function (`llvm_lang.args_init`) registered into
`@llvm.global_ctors` at a lower priority than `llvm_lang.global_init` (so it
runs first - a non-constant global's own initializer might itself call
`args()`).

**Built (and its own `__argc`/`__argv`/`strlen` externs declared) only for a
program that actually calls `args()` somewhere** (`Generator.argsUsed`,
set by `genArgsCall`) - not unconditionally for every module the way
`printf`/`malloc`/etc. already are. This forced `genPackage`'s own final
pass (renamed `genCtors`, `globalinit.go`) to move from running *before*
every function/constructor/destructor body is generated to running *after*
- `g.argsUsed`'s final value isn't known for certain until every body has
already been generated. This reordering changes nothing about correctness
(neither synthesized ctor's own body-generation actually depends on any
*other* function's body existing first, only on every global/function/
constructor *signature* already existing, true well before either ordering
point) - confirmed by this project's full existing test suite passing
unchanged.

**Why not build this unconditionally, the way every other runtime extern in
`setupRuntime` already is?** `__argc`/`__argv` are real external symbols
this package has no control over the resolvability of under JIT execution -
genuinely unlike `malloc`/`printf`/`memcpy`/etc., already proven resolvable
by this entire project's existing test suite. Keeping them (and
`llvm_lang.args_init`, and its `@llvm.global_ctors` entry) out of every
program's module unless that program actually calls `args()` means the vast
majority of existing and future programs carry zero new external-symbol risk
at all from this feature's mere existence - a real, considered risk
management decision, not just "slightly more efficient." `bindMinGWMainThunk`
(`cmd/llvmc/main.go`, mirrored in `src/codegen/codegen_test.go`) additionally
binds both to harmless, always-valid process-local memory under JIT via the
same `AbsoluteSymbols` mechanism already used for the unrelated `__main`
MinGW/GCC ABI quirk, removing even the residual risk that LLJIT's own
per-module materialization strategy might need them to resolve to *something*
merely by virtue of being declared in a module some other symbol gets looked
up from - confirmed directly, not assumed, by `TestArgsCallUnderJITReturnsEmptySlice`
et al. actually JIT-executing successfully.

**Why an empty slice under JIT, not real trailing-argument forwarding
through `llvmc`.** The alternative explicitly considered: have `llvmc`
itself accept trailing positional arguments after the compiled program's own
path (`llvmc program.llx foo bar`) and thread them through the JIT's own
raw-`syscall.SyscallN`-based `main` invocation. This was rejected as
genuinely awkward given how that invocation mechanism actually works, for
two compounding reasons: (1) it would need `main`'s own LLVM signature to
carry real argc/argv parameters after all - exactly the regression risk the
entry above already explains rejecting; (2) even granting that, correctly
poking an already-marshaled `{ptr, i32, i32}` slice value (referencing
further heap-allocated `{ptr, i32}` string headers, each pointing at real
string byte data) directly into a live JIT'd module's global memory *from
the Go host process* would require this driver to independently reconstruct
LLVM's own exact struct layout/alignment rules for `g.dynArrTy`/`g.stringTy`
by hand, entirely outside any actual generated code - a fragile, easy-to-get-
subtly-wrong approach for a fallback path this project's own explicit
instructions permitted skipping ("an acceptable, clearly documented fallback
is: args() returns ... an empty slice - pick whichever is simpler to
implement correctly ... don't spend excessive effort forcing full
trailing-arg-forwarding through JIT if it's fighting the existing invocation
mechanism"). An empty slice is simple, cannot ever be subtly wrong, and is
clearly, prominently documented (`LANGUAGE.md`'s "The `args()` builtin"
section) rather than silently different behavior a user could stumble into
unwarned.

**Status:** shipped. See `LANGUAGE.md`'s "The `args()` builtin" section for
the full language-level rule (including the JIT-vs-AOT behavioral
difference, called out explicitly) and `CODEGEN.md`'s own section of the
same name for the lowering; `src/codegen/args_test.go`/
`cmd/llvmc/main_test.go`'s `TestBinary_AOT_Args` for the JIT-empty-slice vs.
AOT-real-argv contrast asserted directly, and
`TestArgsUnusedProgramHasNoArgsMachinery`/`TestArgsUsedProgramHasArgsMachinery`
for the conditional-machinery behavior this entry describes.

---

## 2026-07-23 - Runtime trap diagnostics: an informative message, unchanged hard-abort mechanism

**Decision:** every runtime safety trap this project already had
(`genBoundsCheck`/`genSliceRangeCheck`, `src/codegen/expr.go`;
`genMakeSizeCheck`, `src/codegen/runtime.go`) now prints a real, informative
`printf`-based message - the actual runtime values involved (an
out-of-range index and the size it was checked against; a bad slice's
low/high/capacity; make's own len/cap) - immediately before the existing
`llvm.trap` + `unreachable` sequence. The abort mechanism itself is
completely unchanged: still a genuine illegal-instruction process crash, not
a graceful `exit(1)` or any kind of recoverable panic/exception - this
language still has no exception-handling concept anywhere, by design (see
"Array bounds checking" and "Destructors" entries above).

**Why:** a bare `llvm.trap` with zero diagnostic output made debugging a real
program's out-of-bounds/bad-slice/bad-make failure needlessly painful -
nothing distinguished *which* check failed or *what* the actual bad values
were, from the outside, short of attaching a debugger. Go's own runtime
panic convention (a message, then a hard crash) was the explicit model to
follow, deliberately not a softer recovery mechanism - inventing one (a
`recover`-like construct, a graceful `exit(1)`) was explicitly out of scope
and would have been a much larger, unrelated language-design change this
round had no mandate to make. Reusing the exact same `printf`/cached-format-
string mechanism `print`'s own codegen already established (`g.printfType`/
`g.printfFn`, `defineCString`) meant this needed no new runtime primitive at
all - three more format-string globals (`fmtBoundsTrap`/`fmtSliceRangeTrap`/
`fmtMakeSizeTrap`) in the same table every other cached format string
already lives in, and one more `printf` call inserted immediately before
each existing trap block.

**Status:** shipped. See `CODEGEN.md`'s new "Runtime trap diagnostics"
section for the exact message text/values per site, and
`TestOutOfBoundsIndexTraps`/`TestSliceRangeCheckTraps`/
`TestMakeCapLessThanLenTraps`/`TestMakeNegativeSizeTraps`
(`src/codegen/bounds_test.go`/`slice_test.go`/`dynamic_array_test.go`) for
the printed-message assertions added on top of each test's existing
abnormal-exit assertion - confirming the abort mechanism itself is
byte-for-byte unchanged, only informative output was added before it.

---

## 2026-07-23 - Go-style multi-return values: destructuring only, no tuple type

**Decision:** add Go-style multi-return values - `func f() (T1, T2, ...)`,
`a, b := f()` - confirmed directly with the project owner (asked explicitly,
not inferred) as this language's answer to error handling, now that a
fallible function has a real way to signal failure besides a sentinel value
or an out-pointer parameter. Scoped deliberately narrowly, mirroring Go's own
actual restriction rather than inventing something looser: **there is no
first-class tuple type** - a multi-return call's result can only ever be
consumed by destructuring it immediately, at the exact point a matching call
happens (`a, b := f(...)`/`a, b = f(...)`) - it can never be stored in a
single variable, passed onward as one value, or used any other way. Every
other position (a single-name `:=`, a call argument, `print`, an operator
operand, ...) rejects a multi-return result outright, with a real
diagnostic, the same way this pass already rejects a `void` result almost
everywhere.

**Why (destructuring only, no tuple type):** a real tuple type would be a
substantially larger feature - a new storable `Type` that itself needs its
own assignability/equality/composite-literal rules, interacting with every
existing construct that can hold an ordinary value (struct fields, array
elements, function parameters) - none of which the motivating error-handling
use case (`v, ok := f(...)`) actually needs. Go itself draws the identical
line for the identical reason: a multi-value result is a call-site-only
construct there too, never a real value with its own type. Modeling this as
`Type{Kind: TypeMultiReturn, Params: []Type}` (reusing the exact same
`Params []Type` field `TypeFunc` already carries its own parameter types in)
and rejecting it everywhere except the two consuming positions was the
smallest change that fully supports the real use case without opening any of
that larger design surface.

**Why new node kinds, not retrofitted existing ones:** `ReturnStmt` stays
fixed-arity `[expr]` unchanged - a multi-value return wraps its values in a
new variable-arity `MultiValueExpr` node sitting in that same slot, mirroring
`ParamList`'s own established "variable-arity part gets its own wrapper node
so the containing fixed-arity node doesn't have to change shape" precedent
exactly. Likewise a multi-return function's return-type position gets a new
`MultiReturnType` wrapper (sitting in `FuncDecl`'s existing single
return-type slot), and the two destructuring statement forms
(`MultiShortVarDecl`/`MultiAssignStmt`) are genuinely new node kinds rather
than nullable-slot variants of `ShortVarDecl`/`AssignStmt`. This follows the
same reasoning `ConstructorDecl`/`DestructorDecl`/`ExternFuncDecl` already
established: every single-value `return`/single-name `:=`/single-target `=`
call site across `sema`/`codegen` assumes exactly one value/name/target, and
retrofitting those shapes directly would have rippled a nil-check through all
of that for zero benefit - a plain single-value `return`/`:=`/`=` needed
(and got) zero changes to its own existing node shape or codegen path.

**Why each destructured name's own type is eagerly cached, not left lazy:**
a `MultiShortVarDecl`-declared name's own component type is computed once,
directly against that name's own `Ident` node (`checkMultiShortVarDeclNode`,
`sema/typecheck.go`) - both `declType`'s memoization cache and `info.Types`
are seeded there unconditionally, rather than relying on `declType` being
invoked lazily the first time some later expression references the name (the
way an ordinary `Param`'s type effectively is). A destructured name that's
declared but never referenced again anywhere in the program would otherwise
never have its own `info.Types` entry populated at all - codegen's own
`g.info.Types[nameNode]` lookup would silently read the `Type{}` zero value.
Ordinary `ShortVarDecl` doesn't have this gap only because `checkStmt` itself
always forces `declType` on the *whole* declaration node directly, unlike a
multi-declaration's individual names.

**Why the codegen ABI needed nothing new at all:** a multi-return function's
real LLVM signature returns an anonymous struct `{T1, T2, ...}` - exactly the
same "struct/array/string passed and returned as a real LLVM aggregate type,
no manual `sret`/by-ref tricks" convention this project's structs already
use (see `CODEGEN.md`'s own section of that name), just for an anonymous
aggregate instead of a named `StructInfo`-backed one. `return a, b` builds
it via `llvm.Undef` + `CreateInsertValue` (the same runtime-aggregate
construction `genFuncLit`'s own closure fat-pointer already uses whenever a
field is a genuine runtime value, not a compile-time constant); destructuring
reads it back via `CreateExtractValue`, the same instruction a struct's own
field access already uses. No new LLVM type needed caching in `setupTypes`
either - unlike `stringTy`/`dynArrTy`/`funcValTy` (one fixed shape reused
everywhere), every multi-return function's own component types differ, so
each one's anonymous struct type is simply built fresh, on demand.

**Explicitly out of scope, confirmed directly rather than assumed
worth adding:**

- **General Go-style parallel multi-assignment** (`a, b := 1, 2`, each side
  independently evaluated and paired positionally) - a genuinely different,
  larger feature than destructuring one multi-return call, not needed for
  the motivating use case. This language's destructuring grammar only ever
  parses a single expression as the right-hand side of a multi-target
  `:=`/`=`, so this form isn't even reachable through the new grammar at
  all - a second comma-separated value left over is simply an ordinary
  syntax error (an unconsumed token where a statement separator was
  expected), not a dedicated diagnostic naming this case specifically.
- **Argument-spreading** (Go's own `f(g())` - forwarding a multi-return
  call's results onward as multiple arguments to another call). Every
  argument position is an ordinary single-value context (`checkValueExpr`),
  so this is rejected the same way any other single-value position rejects
  a multi-return result - no special-casing needed anywhere.
- **A blank identifier (`_`) for discarding one of several destructured
  values.** This language has no blank-identifier concept anywhere yet (see
  `src/sema/resolve_test.go`'s own existing comment on this) - every
  destructured value must bind to (or assign into) a real, distinctly-named
  target this round. Documented in `LANGUAGE.md` as a deliberate,
  likely-worth-revisiting-later gap, not a silent one.

**Status:** shipped. See `LANGUAGE.md`'s "Functions" section for the full
language-level rule (including every explicit scope boundary above) and
`CODEGEN.md`'s new "Go-style multi-return values" section for the lowering.
New coverage across `src/parser` (`multireturn_test.go` - grammar/`Tree.Dump`
shape, plus the parallel-multi-assign/missing-assign-op clean-rejection
cases), `src/sema` (`multireturn_test.go` - the destructuring/return-matching
type rules and every out-of-scope rejection), and `src/codegen`
(`multireturn_test.go`, JIT-executed - the `divide`/`find` idiom, mixed-width
component types, both destructuring forms including non-ident assignment
targets, and 3+ return values), plus a real worked example
(`examples/multireturn/multireturn.llx`) exercised end to end - JIT and AOT
alike - by `cmd/llvmc/main_test.go`.

---

## 2026-07-23 - Maps: open addressing + tombstones, a word-wise FNV-1a-style hash, `remove` over reusing `delete`

**Decision:** `map[K]V` is a real hash table, backed by the same arena
allocator every other heap-needing feature already routes through - open
addressing with linear probing and tombstone-marked deletions (not separate
chaining), a word-wise FNV-1a-*style* recursive hash combinator (not a
literal byte-for-byte FNV-1a pass over a key's raw memory), doubling growth
at a 0.75 load factor, and a brand-new predeclared `remove(m, k)` builtin
for key removal rather than reusing the existing `delete p` statement.
Scoped to storage/lookup/removal only - no `range`-style iteration, no
`keys(m)`/`values(m)` helpers, no map composite-literal syntax.

**Why open addressing + tombstones over separate chaining:** genuinely
simpler to implement correctly and reason about - one flat, arena-allocated
bucket array per map (`{ptr buckets, i32 count, i32 bucketCount}` control
block plus a `{i8 tag, K key, V value}` bucket array), no per-entry pointer
indirection/chaining links to allocate, walk, or reason about aliasing for.
The one real wrinkle open addressing introduces - a naive "clear the slot on
delete" would break a probe sequence that legitimately continues past a
deleted slot to reach a still-live key further along the same original
chain - is solved the standard way: a distinct tombstone tag, never treated
as "probe stops here" the way a genuine empty slot is, but still eligible to
be reused as an insertion point.

**Why a word-wise recursive hash combinator over literal byte-for-byte
FNV-1a:** this project's own struct/array *values* are real LLVM aggregates
built via `InsertValue`, with no guarantee their own inter-field padding
bytes are ever deterministically zeroed - hashing "every raw byte the LLVM
type occupies" risks hashing two logically-identical struct values (equal in
every field) to two different results, purely from whatever garbage bits sat
in their own padding, silently breaking the one property a hash table can't
survive without (equal keys MUST hash equal). Recursing through a key's own
*logical* structure - each numeric field/element's own bit pattern, a
string's own real content bytes, walked recursively for a nested struct/
array - and mixing only those bits with the same simple FNV-1a-style
`seed = (seed XOR word) * 16777619` fold sidesteps the padding hazard
entirely, while staying exactly as simple a mixing function as literal
FNV-1a itself. See `CODEGEN.md`'s "Maps" section for the full worked
rationale.

**Why `remove(m, k)` as a genuinely new builtin, not an extension of
`delete p`:** `delete p` is a real, unrelated operation - heap pointer
deallocation via `new`/`delete` (see LANGUAGE.md's "Pointers" section) -
operating on a completely different kind of value (a `*T`) for a completely
different reason (freeing memory, not removing a table entry). Overloading
that one keyword for "also remove a map key" would be a confusing collision
between two unrelated concepts sharing only surface-level vocabulary
("delete something"); a clean, distinctly-named `remove(m, k)` builtin (the
same predeclared-function-with-no-declaration-site mechanism `make`/
`append`/`len`/`args` already use) avoids the collision entirely and needs
no new grammar at all.

**Why map iteration (`for k, v := range m`) was left out entirely, not just
narrowly deferred:** this language has **no `range`-style for-loop grammar
at all yet**, for anything - only the three plain C-style `for` forms exist.
Inventing `range` just for maps, when nothing else in the language has it
either, is a substantially bigger, genuinely separate feature (new grammar,
new sema iteration-variable binding rules, a real design question of its
own for what a future array/slice `range` would look like too) - not
something to back into narrowly scoped to maps alone. `keys(m)`/`values(m)`
helpers were considered as a smaller consolation but likewise deferred,
since they weren't needed to make the feature's own worked example
(`examples/word_freq/word_freq.llx`) complete - every value the demo needs
is reached by direct lookup, never enumeration.

**Why a map composite literal (`map[string]int{"a": 1}`) was left out:**
Go has this, but it's a real, separate grammar extension - this language's
existing `CompositeLit` machinery is built specifically around struct/array
shapes (a bare positional or `field: value` element list), not a `key:
value` *pair* list keyed by arbitrary expressions the way a map literal
needs. `make(map[K]V)` plus individual `m[k] = v` insertions was judged
sufficient for this round; writing `map[...]...{...}` today is simply a
plain parse error (`map` has nowhere legal to start an expression), not a
silently-mishandled case.

**Status:** shipped. See `LANGUAGE.md`'s new "Maps" section for the full
language-level rule (key-comparability restriction, the `v, ok := m[k]`
two-result index expression and its precise distinction from a real
multi-return call, every explicit scope boundary above) and `CODEGEN.md`'s
new "Maps" section for the hash table's exact representation/growth/
collision-resolution scheme. New coverage across `src/parser`
(`map_test.go` - `map[K]V` grammar/`Tree.Dump` shape, nested maps, `make`'s
own bespoke argument grammar applied to a map type, and the clean parse
diagnostic for a bare `map[...]...{...}` expression), `src/sema`
(`map_test.go` - the type itself, `make`/`len`/`remove`'s own dispatch, the
key-comparability restriction across every rejected/accepted shape, the
two-result index expression's context-dependent typing alongside a real
multi-return call's own unchanged rejection, and every mutation restriction
- `&m[k]`, compound assignment, `++`/`--`), and `src/codegen`
(`map_test.go`, JIT-executed - make/insert/lookup/overwrite, `len`, `remove`
(including from a nil map and an absent key), the two-result idiom for both
a present and absent key alongside a plain single-value read in the same
program, a real growth/rehash forced by 50 distinct integer keys with every
one still correctly retrievable afterward, struct-typed keys colliding by
structural value rather than identity, and a nested `map[K]map[K2]V2`
value), plus a real worked example (`examples/word_freq/word_freq.llx` - a
word-frequency counter over `std/strings.Split`) exercised end to end -
JIT and AOT alike, both producing byte-identical, hand-verified output.

---

## 2026-07-23 - `==`/`!=` and `print` gain a real comparability/printability gate in sema

**Decision:** `checkEqualityOperands`'s struct/array branch
(`src/sema/typecheck.go`) now runs a new `typeIsComparable` predicate over the
whole aggregate type, alongside its existing `lt.Equal(rt)` check, before
admitting `==`/`!=` between two same-typed structs/arrays. `typeIsComparable`
is `typeIsComparableKeyType` (originally written for `map[K]V`'s own
key-type restriction) generalized and renamed - the exact same recursive
"walk every field/element, reject a dynamic array/function type/map type
anywhere nested inside" logic, now shared by both the map-key-declaration
site (`mapTypeFromNode`) and this operator. `checkPrintCall` gets its own,
separate `typeIsPrintable` predicate, gating `print`'s single argument the
same recursive way.

**Why (root cause, not another codegen patch):** a 5-agent code review
found `checkEqualityOperands` accepted `==`/`!=` between two same-typed
structs/arrays via a bare whole-type `Type.Equal` check, never validating
that every recursively-nested field/element `Kind` was actually something
codegen's `genValueEqual` (`src/codegen/expr.go`) could lower. Two distinct
failure modes followed from that one gap: a struct field of `TypeMap` or
`TypeFunc` reached `genValueEqual`'s `default:` case and panicked the whole
compiler; far worse, a struct field of a *dynamic array* (`[]T`) reached
`genValueEqual`'s `TypeArray` case, whose `for i := 0; i < int(t.Size); i++`
loop runs *zero times* for a dynamic array (`t.Size` is never set when
`Dynamic` is true) - silently comparing that field as always-equal
regardless of its real length/contents, so `a == b` for two structs
differing only in a slice field's contents evaluated to `true`. `checkPrintCall`
had the identical shape of bug for a different reason: it accepted "exactly
one argument, of any type" with zero restriction, while codegen's
`genPrintCall`/`genPrintValueBare` only ever implemented a fixed set of
`Kind`s and panicked on a bare function value, map value, or either nested
inside a struct field. In both cases the reactive fix - just widening
codegen's switch again - was rejected: `genValueEqual` had already been
widened once, that same day (i8/i16/i64/f32/f64/pointer fields), on a doc
comment claiming its switch "must cover every Kind a struct field or array
element can legitimately have" - a claim that was never actually true (no
`TypeMap`/`TypeFunc` case, and the dynamic-array case was never correct to
begin with). The real, load-bearing fix belongs in the layer that decides
what's legal in the first place - sema - restoring codegen's own
"unreachable given an already-checked tree" invariant (see
`src/codegen/codegen.go`'s package doc comment) instead of chasing the
symptom in codegen a second time.

**Why two separate predicates, not one shared one:** `typeIsComparable` and
`typeIsPrintable` are deliberately *not* the same allowlist, despite sharing
almost all of their logic (same numeric/bool/string/pointer/struct/array
base, both recursing into struct fields and rejecting `TypeFunc`/`TypeMap`
anywhere nested). A dynamic array is printable - `genPrintArrayValue`
already renders one correctly, an existing, tested, working feature that
must not regress - but is never comparable (`==`/`!=` already rejected a
bare slice outright, for the same "no meaningful equality" reason a map/func
value is rejected). Reusing one predicate for both call sites would have
either wrongly allowed comparing two slices or wrongly rejected printing
one; keeping them as two small, separately-named functions makes that
difference an explicit, checkable invariant instead of an implicit
coincidence.

**Also landed alongside this:** real `TypePointer` support in codegen's
printing (`genPrintCall`/`genPrintValueBare`, `src/codegen/runtime.go`) - a
pointer was already comparable and is included in the new printable set too,
but codegen had no case for it at all (not even a panic case) until now. It
prints via a new `"%p\n"`/`"%p"` format-string pair (`fmtPtr`/`fmtPtrBare`),
the same "declare a libc-printf format string" convention every other
`fmtInt`/`fmtFloat`/`fmtStr` pair already follows - no new runtime primitive
needed.

**Status:** shipped. Both failure modes (the map/func-field panic, and the
dynamic-array-field silent-`true` bug) now produce a clean compile-time
diagnostic instead. See `LANGUAGE.md`'s Operators section and its `print`
builtin section for the user-facing rule, and `CODEGEN.md`'s "`print`
builtin, concretely" section for the pointer-printing addition.

---

## 2026-07-23 - Default-on LLVM `default<O2>` optimization pipeline, with a `-no-opt` escape hatch

**Decision:** `src/compiler`'s `finishPipeline` now runs LLVM's real
`default<O2>` pass pipeline (`llvm.Module.RunPasses`, the vendored
`third_party/go-llvm`'s `passes.go` - already fully present, unused until
now) over every module right after `llvm.VerifyModule` succeeds, in the one
shared pipeline tail every consumption path (JIT execution, `-emit-llvm`,
`-o`) already funnels through - not duplicated per path. `CompilePackage`/
`CompileProgram`/`finishPipeline` all gained an explicit `optimize bool`
parameter (this project's own established style over a hidden default or a
functional-options pattern - no existing API here uses that shape); every
existing call site now passes `true` except `cmd/llvmc`'s new `-no-opt` CLI
flag, which threads `false` through when set. `finishPipeline` also now
builds this host's own `llvm.TargetMachine` once, unconditionally (needed
either way: to drive `RunPasses` when `optimize` is true, and for `-o`'s own
object-code emission regardless), and exposes it via a new
`Result.TargetMachine` field - `cmd/llvmc`'s `compileToExecutable` (the `-o`
AOT tail) now reuses that instead of building a second, separate
`TargetMachine` of its own the way it used to.

**Why now:** discovered while benchmarking llvm_lang against Go/Node.js - a
trivial 100M-iteration arithmetic loop ran ~3x slower than equivalent Go/JS
code, and grepping/reading `src/compiler/compiler.go` directly confirmed
why: this compiler ran **zero LLVM optimization passes**, ever, at any
stage. Go's compiler and V8's JIT both do real optimization; this one did
none. The 100M-loop benchmark's own gap is almost entirely explained by
that, not by anything else in codegen's own lowering.

**Why `default<O2>`, not `O1`/`O3`/`Os`/`Oz`:** `O2` is LLVM's own standard,
well-balanced pipeline - the same one `clang -O2` runs - giving real
inlining/mem2reg/GVN/LICM/DCE without `O3`'s more aggressive, occasionally
UB-exploiting/code-size-inflating tradeoffs, and without `Os`/`Oz` trading
away runtime speed for code size (not this project's goal - a hobby
compiler chasing "don't be 3x slower than Go," not embedded/size-constrained
deployment).

**Why on by default, with a disable flag, not opt-in:** every real consumer
of this compiler (the JIT path, `-emit-llvm`, `-o`) wants fast code by
default - matching how every mainstream compiler (`clang`, `go build`,
`rustc`) treats *some* optimization as the ordinary, expected case, not a
special mode you have to ask for. `-no-opt` exists specifically for
debugging: comparing its output against the default optimized output tells
you whether a bug lives in codegen's own lowering or was introduced by an
optimization pass - and, unlike a hypothetical `"default<O0>"` substitution,
`-no-opt` skips `RunPasses` entirely, so it's a genuine, byte-for-byte
restoration of this compiler's pre-this-round behavior, not "a different,
still-real pass pipeline."

**Why the `TargetMachine` moved into `src/compiler` and got shared, not left
duplicated:** `cmd/llvmc/main.go`'s `-o` tail (`compileToExecutable`) already
built its own `TargetMachine` from scratch (`llvm.DefaultTargetTriple` ->
`llvm.GetTargetFromTriple` -> `Target.CreateTargetMachine`) purely for its
own object-code emission. `RunPasses` needs one too, and `finishPipeline` is
now the one place that always runs regardless of which consumption path the
caller ultimately wants - building it there once, and handing it back via
`Result` for `-o`'s own further use, removes the duplication instead of
adding a second copy of the same three calls. Disposal is caller-owned,
exactly like `Result.Module` already is: `cmd/llvmc`'s `finish` disposes it
directly for the `-emit-llvm`/JIT paths (neither has any further use for
it); `compileToExecutable` takes over ownership for `-o` (it needs the
`TargetMachine` alive through its own `EmitToMemoryBuffer` call) and disposes
it itself instead.

**A real regression this round's own verification caught, not shipped:**
turning on `default<O2>` broke dynamic array/struct printing - every literal
punctuation character `genPrintLiteral` prints (`[`, `]`, ` `, the trailing
newline) vanished from JIT/AOT output, because LLVM's `SimplifyLibCalls`
recognizes `printf` called with a constant, single-character,
no-format-specifier format string and rewrites it into a bare `putchar`
call - not a safe rewrite under this project's own JIT hosting (LLJIT
materializing symbols straight out of the already-running host process,
rather than through one program's own single, statically-linked CRT
startup/shutdown sequence), where `putchar`'s and `printf`'s underlying
stdio buffers can't be assumed to be the same one. Fixed by routing every
`printf` call this package emits through one new choke point,
`callPrintf` (`src/codegen/runtime.go`), which tags the call site with
LLVM's `nobuiltin` attribute (the same attribute Clang emits under
`-fno-builtin`) - telling the optimizer never to recognize these particular
calls as the corresponding libc built-in, so no library-call rewrite ever
applies to them, without disabling `printf`/libc recognition globally (which
would give up real, safe optimizations everywhere a *user's* compiled
program calls libc functions on its own terms). Caught by this round's own
full-example regression sweep (optimized vs. `-no-opt` output diffed for
every program under `examples/`) before being considered done, not shipped
and discovered later.

**Status:** shipped. See `CODEGEN.md`'s new "Optimization pipeline" section
for the full design (including the `nobuiltin` fix's own write-up) and
`BENCHMARKS.md`'s dated entry for the resulting `CompilePackage` end-to-end
cost increase (real and expected - the actual price of the passes now
genuinely running).

---

## 2026-07-23 - Arena allocator: geometric (doubling) chunk growth, not a fixed 64KiB every time

**Decision:** replace `setupArena`'s fixed-size growth (every chunk after the
first was another flat `arenaChunkSize`, 64KiB, forever) with geometric
growth: a new mutable global, `.arena.next_chunk_size`, starts at
`arenaChunkSize` and doubles on every *ordinary* (non-oversized) growth
event, capped at a new `arenaChunkMaxSize` (64MiB). The starting chunk size
itself is unchanged - this is purely about what happens on the *second* and
every subsequent normal growth event, not the first one. An oversized
one-off request (bigger than the current tracked chunk size) is still served
at exactly its own size, same as before, and deliberately does **not**
advance `.arena.next_chunk_size` - see `CODEGEN.md`'s "The arena allocator"
section for the full mechanics and `src/codegen/runtime.go`'s own doc
comments for the exact IR shape.

**Why:** empirically root-caused, not theorized. A 50,000-iteration
`s = s + "x"` loop (`examples`-style AOT benchmark, the classic O(n^2)
naive-immutable-string-concat pattern - same shape as Go's own naive `+=`)
was running about 2.8x slower than Go's own equally-naive `+=` on the
identical workload - a real, unexplained-by-algorithm gap. Traced precisely:
this benchmark's *cumulative* allocation volume (the arena never reuses
anything, by design - see the "one process-lifetime bump allocator" entry
above) is roughly 1.25GB over the whole loop (the sum of every intermediate
string's own size, 1+2+...+50000 bytes). At a fixed 64KiB chunk size, that's
about 19,000 real `malloc` calls over the course of one loop - confirmed
directly by temporarily hardcoding `arenaChunkSize` to 16MiB (~683ms ->
~400ms) and then to 128MiB (~313-344ms, closing most of the remaining gap to
Go's own ~243ms baseline on the same workload), then reverting both
experimental changes before implementing the real, permanent fix. The fixed
small chunk size - not `memcpy` throughput, not codegen quality - was the
dominant cost.

**Why 64MiB specifically (not 16MiB or 128MiB):** the two manual experiments
above bracket a real tradeoff. 128MiB gets closer to Go's own baseline, but
means a long-running, allocation-heavy program keeps requesting genuinely
large single blocks well past the point of real per-`malloc`-overhead
benefit, with no way to reclaim an abandoned block's unused tail (the arena
never frees - see above). 64MiB sits between the two experimental data
points, capturing most of the realistic win a bigger chunk buys (post-fix,
the same 50,000-iteration benchmark measured 355-474ms across repeated
min-of-5 runs, comfortably inside the 313-442ms range the manual experiments
found) without reserving as aggressively as 128MiB would for a workload with
no guarantee it will ever need that much.

**Why an oversized one-off deliberately doesn't touch the tracked
baseline:** a single unusually large allocation (e.g. one big `make([]T, n)`
call for an otherwise string-concatenation-light program) would otherwise
permanently inflate every *later, ordinary* chunk for the rest of the
program's run, even though its own steady-state allocation pattern never
asked for anything that large again. Keeping the two paths - "ordinary,
tracked-and-doubling" vs. "oversized, served-once-and-forgotten" - genuinely
separate avoids that: only a real, sustained pattern of hitting the current
chunk size (not one spike) grows the baseline for next time.

**Verification given the severity class of a bug here** (silent heap
corruption in the one allocator every heap-needing language feature routes
through): beyond the standard `gofmt`/`go vet`/full `go test` sweep and a
byte-identical regression diff across every example under `examples/`
(before vs. after, same stdout and exit code for all 19), a dedicated
correctness stress suite was added
(`src/codegen/arena_growth_test.go`) specifically scaled to walk the *entire*
geometric progression (64KiB through the 64MiB cap) via many small
independent allocations, build one genuinely large (128MiB) single string via
doubling with byte-exact content checks at start/middle/end (not just
length), interleave small string concatenations with large dynamic-array
append bursts, append 3,000,000 elements to a single dynamic array
(verifying every element plus an `i64` closed-form checksum), and churn a
20,000+5,000-key map through heavy insert/remove/insert cycles (verifying
every surviving, removed, and newly-inserted key individually) - each
deliberately checking real per-element/per-byte content, not just a final
length or count. One of these tests was confirmed to actually fail (a real
process crash, not a soft assertion failure) when a deliberate bug was
temporarily reintroduced into the grow path's `needsBigger` comparison,
before being reverted - confirming the suite doesn't just pass vacuously.

**Status:** shipped. See `CODEGEN.md`'s "The arena allocator" section for the
updated design and `src/codegen/runtime.go`'s `arenaChunkSize`/
`arenaChunkMaxSize`/`setupArena` doc comments for the exact mechanics.

---

## 2026-07-23 - Rust-style enums + `match`: comprehensive from day one, `{i32, ptr}` representation, statement-only/enum-only scope for `match`

**Decision:** build the full feature in one round rather than a narrower
first cut - unit/tuple/struct variants, methods, destructors with the exact
same non-copyable propagation rule structs already have, recursive/self-
referential variants, `==`/`!=`/`print()` with a real runtime discriminant
dispatch, and an exhaustive `match` statement - all landing together,
instead of (say) unit-and-tuple variants first and struct-style/destructors/
match exhaustiveness in a later round.

**Why comprehensive, not narrow, this time:** every previous feature in this
project's history started narrow specifically because there wasn't much
existing language surface for it to sit consistently alongside yet (the
first pass at structs had no constructors; the first pass at pointers had no
`new`/`delete`). Enums are the opposite case: by the time this round landed,
the project already had a mature, load-bearing precedent for every piece
this feature needed to reuse - a real non-copyable/destructor system
(structs), a real keyed-composite-literal grammar (structs), a real
receiver-method mechanism (structs), and a real arena-allocation idiom for
"small fixed header, real payload on the heap" (dynamic arrays, closures,
maps). Landing enums narrow and adding struct-style variants/destructors/
exhaustiveness in a *third* round later would have meant redoing the same
integration work twice for no real benefit - unlike the earlier features,
there was no open design question here forcing a narrower first cut.

**Why variant construction needs no separate `constructor(){}` block (unlike
a struct):** a struct constructor exists specifically to run custom logic a
bare composite literal can't (arbitrary computation, invariants, side
effects) - a struct's own composite literal is deliberately "raw structural
construction, bypassing constructors entirely" (see `LANGUAGE.md`'s
"Constructors" section), so the two coexist as genuinely different things.
A variant's own construction (unit/tuple/struct-literal) *is* the value - there
is no second, "raw" way to build the identical value that a constructor
could meaningfully differ from, so there's nothing for a constructor
concept to add here that construction doesn't already fully cover.

**Why `{i32, ptr}` - one shared LLVM type for every enum - rather than a
named per-enum struct sized to its largest variant:** the natural
alternative, `{i32, [N x i8]}` with `N` = the largest variant's own
byte size, needs `N` as a real Go integer at struct-type-construction time.
This project's existing `llvm.SizeOf`-based sizing idiom is a lazy LLVM
constant expression (`getelementptr(null, 1)` + `ptrtoint`), only ever
resolved once the module is compiled/JIT'd - getting a real Go-side integer
out of it before that point would need a genuine `llvm.TargetData` threaded
through this package for the first time, a new dependency with its own
surface area, purely to serve one type kind's own struct layout. The
`{i32, ptr}` shape needs none of that, and is exactly this project's own
already-established idiom elsewhere (a dynamic array, a closure's capture
context, a map's control block are all "small fixed header, real payload
referenced via `ptr`") - reusing it here cost nothing new to build and, as a
direct consequence, made every genuinely hard case (a recursive/self-
referential variant, an enum-of-enum field) fall out for free with zero
special-casing: a pointer is always just `g.ptrTy` regardless of what it
points to, so no variant's own payload type ever needs another enum's (or
its own) layout to already exist. The real cost is the one already
documented in `LANGUAGE.md`/`CODEGEN.md`: every non-unit variant construction
is a real arena allocation, never a stack value the way a struct's own
fields are - judged an acceptable, well-precedented tradeoff (see the
arena-allocator entries above) rather than a genuine regression, since every
comparable "small header + heap payload" value in this project already pays
the identical cost.

**Why `match` is a statement, not an expression, this round:** the
motivating use case (dispatching on an enum's active variant, running
different logic per case) doesn't need a value handed back to an enclosing
expression - every arm in the worked example (`Shape.Area`, `List.Sum`)
already `return`s directly, needing nothing more than an ordinary
side-effecting `Block` per arm. Building `match` as an expression too would
mean deciding a whole second set of rules this round didn't actually need
answered yet (every arm's result type must unify to one common type; what a
non-exhaustive match-expression with no covering value even means) - a
real, separable feature addition for a later round, not a gap in this one.
Go itself draws an analogous line (`switch` is a statement, not an
expression) for the same underlying reason.

**Why `match` is scoped to enum-variant patterns only, not a general
value-switch:** the exhaustiveness check - genuinely the entire value
proposition of building `match` as its own construct rather than an
unchecked switch - only has real, checkable meaning against a *closed* set
(an enum's own declared variants). A general `switch` over arbitrary values
(ints, strings, bools) has no such closed set to check exhaustiveness
against at all (Go's own `switch` has no exhaustiveness checking for
exactly this reason) - conflating the two into one construct this round
would have diluted the one thing that makes enum-variant `match` worth
building in the first place. The general value-switch capability remains a
clean, separable extension of the identical `match` keyword/statement shape,
deferred to a later round rather than an oversight here.

**Status:** shipped. See `LANGUAGE.md`'s "Enums"/"match" sections for the
full language-level rules (including both deferred-to-a-later-round
boundaries called out above, documented there as deliberate scope, not
gaps) and `CODEGEN.md`'s "Enums" section for the representation and
codegen this entry describes.
