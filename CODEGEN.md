# Codegen (`src/codegen`) and the `llvmc` CLI driver

How the compiler lowers the language described in `LANGUAGE.md` into LLVM
IR, plus the `cmd/llvmc` CLI that actually runs a compiled program. See
`AGENTS.md` for the doc split this file is part of, and `DECISIONS.md` for
the "why" behind choices summarized here in passing (numeric widths, the
arena allocator's design, etc).

Lowers a resolved+type-checked `*ast.Tree`/`*sema.Info` pair into an LLVM
`Module` via `tinygo.org/x/go-llvm`. Assumes its input is already fully
valid (zero diagnostics from `sema.Resolve`/`sema.Check`) - it is lowering
already-correct code, not re-deriving semantics, so most invalid input is a
panic, not a diagnostic. The exceptions - real codegen-level restrictions
sema has no opinion on - are documented below and still produce a proper
`diag.Bag` entry.

## `int` is 32-bit

`int` lowers to `i32`, not `i64`. This isn't just "pick one, they're
symmetric": `main`'s real LLVM signature must return `i32` (the actual OS
process exit code), so with `int` == `i32` a source-level `func main() int {
return code }` needs no truncation/cast at all - the language's own `int`
and the platform C ABI's `int` are simply the same type. See `DECISIONS.md`.

## Numeric type -> LLVM type, and where the type resolution itself lives

`sema.Type`'s six concrete numeric kinds map straight onto go-llvm's own
integer/float constructors (`llvmType`, `src/codegen/types.go`): `i8`/`i16`/
`i32`/`i64` -> `Int8Type`/`Int16Type`/`Int32Type`/`Int64Type`; `f32`/`f64` ->
`FloatType`/`DoubleType`. An LLVM integer/float instruction (`CreateAdd`,
`CreateICmp`, ...) is already generic over bit width as long as both
operands share the same LLVM type - which sema guarantees by construction
(see `LANGUAGE.md`'s Types section) - so no per-width branching is needed
inside `genBinaryExpr`/`genUnaryExpr`/etc.; only the *kind* (integer vs.
float) needs to be checked, to pick the matching real floating-point
instruction (`CreateFAdd`/`CreateFSub`/`CreateFMul`/`CreateFDiv`/
`CreateFCmp`/`CreateFNeg`) instead of the integer one.

There is **no codegen-side type-position resolution left at all** - a prior
version of this package had its own `resolveTypeNode`/`varDeclType`
functions that re-derived a type-position node's `sema.Type` from scratch
(duplicating `sema/typecheck.go`'s own `computeTypeFromNode`/`declType`
logic, entirely because `sema.Info.Types` didn't yet cover type-position
nodes - only value-expression ones). That gap is closed: `sema.Check` now
stores every type-position node's resolved `Type` into `Info.Types` too - a
`VarDecl`/`Param`'s own declaration node (`declType`), a `VarDecl`/`Param`/
`Field`/`FuncDecl` return type's annotation node and an `ArrayType` node and
its element (`typeFromNode`) - so every codegen call site that used to call
`resolveTypeNode(n)` or `varDeclType(decl)` is now just a plain
`g.info.Types[n]` map lookup, same as any other expression node. This was a
deliberate architectural fix, not a refactor for its own sake: two
independent implementations of "what type does this node have" is exactly
the kind of duplication that silently drifts out of sync over time (this
round almost re-created it for the six new numeric types, before switching
directly to the `Info.Types` fix instead). See `AGENTS.md`'s `## Architecture`
section - this is the exact mistake that principle now exists to prevent.

## Explicit conversions, concretely

`T(x)` (see `LANGUAGE.md`'s Types/Explicit-conversions sections) is
recognized in codegen (`isConversionCall`, `src/codegen/expr.go`) exactly the
way sema determined it - a `CallExpr`'s callee `Ident` resolving (via
`Info.Refs`) to `sema.SymBuiltinType`, not a function - and lowered
(`genConversion`) using the correct LLVM instruction for the source/target
type pair: `CreateSExt` (widening int - every integer here is signed, so
sign-extension is always correct, never zero-extension), `CreateTrunc`
(narrowing int), `CreateSIToFP`/`CreateFPToSI` (int<->float), `CreateFPExt`/
`CreateFPTrunc` (widening/narrowing float). A same-`Kind` "conversion" (e.g.
`i32(someI32Value)`) is recognized up front and just returns the value
unchanged - no pointless bitcast/identity instruction emitted for it.

## `print`'s printf format specifiers - a real Windows/mingw64 platform gotcha

Every numeric width needs its own `printf` format specifier (`genPrintCall`/
`genPrintValueBare`, `src/codegen/runtime.go`): `i8`/`i16` are sign-extended
to `i32` first (`CreateSExt`) and reuse `"%d"` - a manually-built variadic
`printf` call like this one doesn't get C's own default-argument-promotion
for free, so this package has to do it explicitly, the same reasoning that
already applied to `bool`. `f32` is extended to `f64` first (`CreateFPExt`)
and reuses `"%f"` - variadic C calls always promote `float` to `double`,
same rule. `f64` uses `"%f"` directly.

`i64` needed to be **verified empirically, not assumed** - this project's
toolchain is mingw64/MSYS2 (see SETUP.md), and `%lld`/`%d`-family format
specifier behavior for 64-bit integers can genuinely differ between an
MSVCRT-style `printf` (whose *historic* implementation doesn't understand
`%lld` at all - it wants the MS-specific `%I64d`) and mingw-w64's own
"ANSI stdio" `printf` wrapper (`__USE_MINGW_ANSI_STDIO`, on by default for
x86_64 mingw-w64 builds), which *does* support `%lld` correctly, C99-style.
This was tested directly, not guessed at: `src/codegen/stdoutcapture`
redirects the real C-runtime `stdout` FILE* (via `freopen`, from a small
non-test cgo file - `go test` flatly rejects `import "C"` inside any
`_test.go` file, "use of cgo in test ... not supported", a real toolchain
restriction hit while building this, hence the separate package) so a test
can capture exactly what a JIT-executed `print` call writes to real stdout,
byte for byte - something no prior round of this project had a way to do.
`TestPrintI64FormatSpecifierIsCorrect`/
`TestPrintNegativeI64FormatSpecifierIsCorrect` (`src/codegen/stdout_capture_test.go`)
print a value that doesn't fit in 32 bits (`4000000000`) and a negative i64
value, and assert the captured bytes are exactly correct. **Result: `%lld`
is correct on this toolchain** - mingw64's ANSI-stdio printf handles it
properly, confirmed via a real, byte-exact captured comparison, not
assumption. If this project's toolchain ever moves off mingw64/MSYS2 (a
plain MSVC-linked build, for instance), this is the first thing to
re-verify - the wrong specifier wouldn't crash, it would silently print
garbled digits.

## `string` representation

`string` is the literal (unnamed) LLVM struct `{ ptr, i32 }` - a data
pointer plus a length, **not** a null-terminated C string. Every consumer
(`print`, `+`/`+=` concatenation, `==`/`!=`, `< <= > >=`) goes through the
length field, never `strlen`. Concatenation (`genStringConcat`,
`src/codegen/runtime.go`) asks the arena allocator (see below) for a buffer
sized to fit both operands and `memcpy`s each in. Equality (`genStringEqual`)
short-circuits on length mismatch before ever calling `memcmp`. Ordering
(`genStringOrder`/`genStringCompareSign`) is a real byte-by-byte
lexicographic comparison, same as Go: `memcmp` over the shorter operand's
length decides it whenever the two differ within that shared prefix, and the
lengths themselves break the tie when one string is a prefix of the other
(so `"ab" < "abc"`, matching Go).

## The arena allocator (`src/codegen/runtime.go`'s `setupArena`)

Every codegen-level heap allocation goes through one centralized bump
allocator instead of calling libc `malloc` directly at each call site -
currently that's just string concatenation (`genStringConcat`), but any
future heap-needing feature (e.g. dynamic arrays, if/when they're designed)
should route through this same primitive rather than reintroducing scattered
`malloc` calls. See `DECISIONS.md` for why this shape was chosen over the
alternatives.

Design: **one process-lifetime arena, growing in malloc'd chunks** (64KiB
each, `arenaChunkSize`) - not a single fixed-size block reserved up front, and
not a `malloc`-per-request scheme either. It's a real generated LLVM function
(`llvm_lang.arena_alloc`, an internal-linkage function this package builds
directly at `Generate` time - not a libc call), backed by two mutable
globals: `.arena.cursor` (the next free byte in the current block) and
`.arena.remaining` (how many bytes are left in it). Allocating `size` bytes:
if the current block doesn't have `size` bytes left, `malloc` a fresh block
first (`arenaChunkSize`, or exactly `size` for a single request bigger than
that) and point the arena at it - whatever was left in the abandoned block is
simply never reused, not reclaimed; then bump the cursor forward by `size`
and hand back the pre-bump address.

This remains a real, intentional memory leak overall - **no per-allocation
free, no GC, no refcounting** - exactly as before, just centralized behind
one primitive instead of ad hoc `malloc` calls at each use site. See
`BLOCKERS.md`: a real memory-management strategy (actual scoped frees,
refcounting, a real GC) is still an open, deliberately-deferred question for
the user to decide for *this arena specifically* - it is groundwork for that
future decision, not an attempt to answer it.

`new`/`delete` (see "Pointers" below) are a deliberate, separate exception to
that, not a change to the arena itself: each `new` is its own individually
`malloc`'d block on a completely different heap, freed one at a time via a
real `delete`/`free` - the arena's own allocations (string concatenation,
dynamic arrays) are untouched by either of them and remain exactly as
leaky as before.

## Dynamic arrays (`[]T`)

See `LANGUAGE.md`'s "Dynamic arrays" section for the language-level feature
(`make`/`append`/`len`, single-element `append`, no slice equality, slice
composite literals). This is the concrete "future heap-needing feature" the
arena allocator (see below) was always meant to eventually support.

**Representation.** `sema.Type{Kind: TypeArray, Dynamic: true}` maps to the
literal (unnamed) LLVM struct `{ ptr, i32, i32 }` = `{ dataPtr, len, cap }`
(`g.dynArrTy`, `setupTypes`, `src/codegen/types.go`) - the exact same
"pointer + metadata, passed like any other small aggregate value" convention
`string`'s `{ ptr, i32 }` already uses (see "`string` representation"
below), just with a third field. `len`/`cap` are `i32`, matching this
language's own `int` (see "`int` is 32-bit" above) - not `i64`, for the same
consistency reason. One shared LLVM struct type serves *every* element
type `T`: `g.ptrTy` is already an opaque `ptr` regardless of what it points
to (see its own field comment, `codegen.go`), so a dynamic array's own
element type only ever matters at the point some code computes an address
into the backing buffer via a real, explicitly-typed GEP (`genMakeCall`/
`genAppendCall`/`genDynArrayLitInto`, `genAddr`'s `IndexExpr` case) - never
in the struct's own shape.

**`make([]T, n)` / `make([]T, n, cap)`** (`genMakeCall`, `runtime.go`):
allocates a fresh buffer sized for `cap` elements (`n`, when `cap` is
omitted) via the arena allocator (`genArenaAllocElems`, a thin wrapper around
`genArenaAlloc` that also returns the element's `llvm.SizeOf` - the classic
null-pointer-GEP constant trick, resolved by LLVM itself with no target-data-
layout query of this package's own), `memset`s the whole allocated region to
zero (a real libc extern, declared in `setupRuntime` alongside `malloc`/
`memcpy`/`memcmp` - "zero the entire allocation" is the simplest, safest
choice: it avoids ever reading uninitialized arena memory through an element
between `len` and `cap` a later `append` hasn't written yet), and returns the
resulting `{ ptr, n, cap }` value. `n`/`cap` are ordinary already-evaluated
runtime `llvm.Value`s, not compile-time constants (unlike `[N]T`'s own `N`) -
so `cap < n` (when `cap` is given) is checked with a real runtime trap
(`genMakeCapCheck`), the exact same `llvm.trap`+`unreachable` mechanism
`genBoundsCheck` already uses for an out-of-range index (see "Array bounds
checking" below): there's no way to reject a bad runtime relationship
between two arbitrary expressions at compile time, so this is a hard process
abort, same failure mode, not a `diag.Bag` entry.

**`append(slice, elem)`** (`genAppendCall`, `runtime.go`): a real
`CreateCondBr`/multi-basic-block lowering, not a single unconditional path -
`len < cap` branches to a "fit" block (nothing to allocate; the existing
pointer/capacity flow through unchanged) or a "grow" block (`newcap =
max(1, cap*2)` - built via a `select` on `cap*2 < 1`, which is only true
when `cap` itself is `0`, exactly the "cap==0" edge case landing on `1` -
see `DECISIONS.md` - then a fresh arena allocation of that size, plus a
`memcpy` of the existing `len` elements into it), and a join block reads
both paths' final pointer/capacity back via `PHI` nodes before writing the
new element at index `len` (through whichever pointer the `PHI` selected)
and returning `{ finalPtr, len+1, finalCap }`. This is the one place this
package's "pointer aliasing" story actually matters at the IR level: the
"fit" path's `PHI` incoming value is the *original* pointer, so a caller
still holding an older copy of the pre-append slice value genuinely observes
the same backing memory being mutated - matching Go's own well-defined (if
occasionally surprising) semantics exactly, not approximating it.

**`len(x)`** (`genLenCall`, `runtime.go`): a dynamic array reads its runtime
`len` field (`ExtractValue` index 1, same as a string reads its own length
field); a fixed-size array returns a plain `ConstInt` built directly from its
already-known `sema.Type.Size` - no different from any other compile-time
constant, and in particular the exact same value its own bounds check
already uses; a string reads its own runtime length field the same way a
dynamic array does.

**Indexing** (`genAddr`'s `IndexExpr` case, `expr.go`): a dynamic array's
element address is computed straight from its own `{ ptr, len, cap }`
value's `ptr`/`len` fields (`genExpr(targetNode)` then two `ExtractValue`s) -
unlike a fixed-size array, there's no need for the *slice variable's own*
address at all, since the backing storage always lives separately on the
arena heap; this works identically for both a read (`genLoad`) and a write
(`genAssignStmt`), since both go through `genAddr` the same way. The bounds
check itself is the same `genBoundsCheck` a fixed-size array's index already
used, generalized to take its `size` operand as an arbitrary already-computed
`llvm.Value` rather than only a compile-time `int64` constant (see "Array
bounds checking" below) - a dynamic array's caller passes its slice's actual
runtime `len` field; a fixed-size array's caller passes a plain `ConstInt`
built from its own compile-time-known `Size`, exactly as before.

**Slice composite literals** (`[]T{1, 2, 3}`, `genDynArrayLitInto`,
`expr.go`): unlike `make`'s own `n`/`cap` (arbitrary runtime expressions),
a literal's element count is always known at codegen time - however many
elements it lexically lists - so this allocates a buffer of exactly that
size via the same `genArenaAllocElems` primitive `make`/`append`'s growth
path both use, fills it positionally (mirroring the fixed-size array
literal's own element-by-element lowering just above it, but writing into a
fresh heap allocation instead of the destination's own inline storage), and
then stores the resulting `{ ptr, count, count }` fields directly into the
destination (a pointer to `g.dynArrTy`) via three `CreateStructGEP`s - the
same field-by-field fill a struct literal's own destination already uses,
rather than building a temporary aggregate value and copying it wholesale.

**Printing** (`genPrintDynArrayValue`, `runtime.go`): renders the same
`[e0 e1 ...]` shape a fixed-size array already does (`genPrintArrayValue`),
but as a real `CreateCondBr`/`AddBasicBlock` runtime loop over the slice's
own `len` field - the same control-flow shape `genForStmt`/`genBoundsCheck`
already use elsewhere in this package - rather than a static unrolled
sequence of `printf` calls the way a fixed-size array's element count
(known at codegen time) allows: a dynamic array's element count isn't known
until the program actually runs, so there's no way to unroll it ahead of
time. `genPrintArrayValue` branches on `t.Dynamic` right up front to pick
one or the other - the two are different enough in shape (static unroll vs.
a real loop with its own basic blocks) that forcing them into one code path
would only make both harder to read, not simpler.

## Array bounds checking

Indexing a fixed-size array - both a read (`a[i]`) and a store
(`a[i] = v`) - lowers to a real runtime check, not a bare GEP: `i < 0 || i >=
size` (`size` is always known at compile time - see `LANGUAGE.md`'s Array
sizes section) traps immediately via LLVM's `llvm.trap` intrinsic (declared
as a plain extern `void()` function named exactly `llvm.trap` in
`setupRuntime`, `src/codegen/runtime.go` - LLVM recognizes the `llvm.`
prefix as an intrinsic regardless of how it's declared in the IR) followed
by `unreachable`, rather than ever proceeding to read/write through an
out-of-range address. See `genBoundsCheck`, `src/codegen/expr.go` - the same
`CreateCondBr`/basic-block shape `if`/`for` lowering already uses elsewhere
in this package. `genBoundsCheck` takes its `size` operand as an arbitrary
already-computed `llvm.Value`, not only a compile-time constant - a dynamic
array's index (see "Dynamic arrays" above) passes its slice's actual runtime
`len` field through the identical check, unchanged.

```go
a := [5]int{1, 2, 3, 4, 5}
a[2]      // fine
a[5]      // traps - index == size
a[-1]     // traps - negative index
```

This is a hard process abort (the same failure mode any other LLVM `trap`
produces) - there's no exception handling or panic/recover mechanism in this
language, so an out-of-range index is unrecoverable by design, not merely
undetected. See `TestOutOfBoundsIndexTraps` (`src/codegen/bounds_test.go`)
for how this is tested despite that: JIT-executing a genuinely out-of-range
index would crash the `go test` process itself, so that test re-execs the
test binary as a child process and asserts the *child* exits abnormally -
the same `GO_WANT_HELPER_PROCESS` pattern `os/exec`'s own test suite uses -
rather than ever tripping the trap in-process.

## Slicing

See `LANGUAGE.md`'s "Slicing" section for the language-level feature (a Go-
style slice expression producing a fresh header value that shares its
operand's backing memory - no copy) and `ast.Node`'s own `SliceExpr` doc
comment for the `[object, low, high]` grammar shape (`low`/`high` are
`ast.InvalidNode` when omitted). Recognized in codegen (`genSliceExpr`,
`src/codegen/expr.go`) the same way sema's own `checkSliceExpr` dispatches -
on the operand's already-resolved `sema.Type` - to one of three lowering
paths, each building a fresh `{ptr, len, cap}` (dynamic array) or `{ptr,
len}` (string) value with no allocation and no copy:

- **A dynamic array operand** (`genDynArraySlice`): `ptr = GEP(s.ptr, low *
  elemSize)`, `len = high - low`, `cap = s.cap - low` - reusing the exact
  same `{ptr, len, cap}` construction `genMakeCall`/`genAppendCall` already
  build, just derived from an existing slice's own fields instead of a fresh
  arena allocation.
- **A string operand** (`genStringSlice`): `ptr = GEP(s.ptr, low)`, `len =
  high - low` - a fresh `{ptr, len}` value, same no-copy sharing; strings are
  immutable (see the `string` representation section above), so this sharing
  is read-only in practice, unlike the dynamic-array case.
- **A fixed-size array operand** (`genFixedArraySlice`): takes the array's
  own address (`genAddr` - the exact same helper `&`/a method receiver/an
  ordinary index already use to get a real, addressable location - this is
  exactly why sema's own `checkArraySliceAddressable` requires the operand to
  be addressable in the first place), then the same `{ptr, len, cap}`
  construction the dynamic-array case uses, with `cap = N - low` (`N` the
  array's own compile-time-known `Size`).

**The bounds check generalizes from a single index to a range.**
`genBoundsCheck` (above) checks one `0 <= idx < size` condition; a slice
expression needs a genuine *range* check instead - `genSliceRangeCheck`
checks `0 <= low`, `low <= high`, and `high <= max` all at once (three
`ICmp`s ANDed together), trapping via the identical `llvm.trap`+
`unreachable` mechanism/basic-block shape `genBoundsCheck`/
`genMakeSizeCheck` already use on any violation. `max` is an arbitrary
already-computed i32 `llvm.Value`, not necessarily a compile-time constant -
mirroring `genBoundsCheck`'s own `size` parameter, generalized the same way
dynamic arrays already generalized it once before (see "Array bounds
checking" above): a dynamic array's caller passes its own runtime `cap`
field (not `len` - see `LANGUAGE.md`'s "Slicing" section for why a reslice's
upper bound is checked against capacity, not length), a string's caller
passes its own runtime `len` field, and a fixed-size array's caller passes a
plain `ConstInt` built from its own compile-time-known `Size`.

`genSliceBounds` is the shared "resolve omitted defaults, then range-check"
helper every one of the three paths above calls into: an omitted low
(`ast.InvalidNode`) defaults to a plain `ConstInt` `0`; an omitted high
defaults to whatever `defaultHigh` the caller passed in (the operand's own
runtime `len` for a dynamic array/string, or a `ConstInt` `N` for a fixed
array) - notably *not* always the same value as the range check's own `max`
upper bound (a dynamic array passes `len` as `defaultHigh` but `cap` as
`max` - the one place these two genuinely differ, per `LANGUAGE.md`'s own
called-out rule).

Once a slice value is correctly constructed, `len(...)`/`append(...)` on it
just work unchanged - a sliced dynamic array/string is an ordinary value of
the same type afterward, nothing about it needed any special-casing in
`genLenCall`/`genAppendCall`'s own existing logic (`TestSliceLenAndAppendOnSlicedValue`,
`src/codegen/slice_test.go`, exercises exactly this).

```go
s := []int{10, 20, 30, 40, 50}
mid := s[1:4]      // {ptr = GEP(s.ptr, 1), len = 3, cap = s.cap - 1}
mid[0] = 99        // writes through the same backing buffer s.ptr points at
```

See `TestSliceDynamicArrayAliasing`/`TestSliceFixedArrayAliasing`
(`src/codegen/slice_test.go`) for the aliasing proof this representation is
actually built for - mutating through a slice and reading back through the
original (and vice versa) - and `TestSliceRangeCheckTraps` (same file) for
the range-check's own actual trap behavior, using the same re-exec-as-a-
child-process pattern `TestOutOfBoundsIndexTraps` above already established.

## Global `var` initializers

A top-level `var`'s initializer can be any well-typed expression now -
matching Go's own real behavior (a package-level `var`'s initializer isn't
required to be a compile-time constant; it runs automatically before `main`)
- not just a compile-time constant the way this section used to describe.
Sema needs no opinion on this at all: it already type-checks a non-constant
global initializer fine (a call, a reference to another `var`, a `new`
heap allocation, a lambda literal, ...) - this was always purely a
codegen-level question of what `codegen.GeneratePackage` was willing to
lower a *global's* initializer to.

**Two lowering paths, chosen per-initializer, not per-program:**

- **Foldable at compile time** (`isConstFoldable`, `src/codegen/constfold.go`)
  - literals, parenthesization, unary `-`/`!`, binary arithmetic/comparison/
  logical/string-concatenation, and struct/fixed-size-array composite
  literals built entirely from constants - is folded directly into the
  global's own LLVM initializer via `constExpr`, exactly as this package has
  always done. `isConstFoldable` is a pure structural predicate (no
  evaluation, no diagnostics) that decides *which* path a given initializer
  takes; once it says yes, `constExpr` is guaranteed to only ever hit a
  genuinely-erroneous case (division by zero, an out-of-range literal) if it
  fails, never its "not a constant at all" default cases - so a diagnostic
  reported past that point is always a real error in an expression that
  really does look constant, not "this needed the other path instead".
- **Everything else** (a function call, a reference to another `var`, a
  member/index expression, a dynamic-array/slice literal, `new`, a lambda
  literal, ...) - the global gets a zero-value initializer up front (matching
  Go's own zero-value convention for a global whose real initializer hasn't
  run yet), and its real initializer expression is queued
  (`Generator.globalInits`) for `genGlobalCtors`
  (`src/codegen/globalinit.go`) to lower as real generated code, once every
  global and every function/constructor signature in the whole package
  already exists (a non-constant initializer's expression can reference
  either).

**The synthesized init function, and `@llvm.global_ctors`:** `genGlobalCtors`
builds one internal-linkage, parameterless function
(`llvm_lang.global_init`) - the exact same per-function generation state
(entry block, fresh locals map, no enclosing loop/receiver/lambda-capture
context) `genFuncBody`/`genConstructorBody`/`genLambdaFunc` each set up for a
body of their own - and lowers every queued initializer inside it via
`storeValueInto` (`src/codegen/stmt.go`, the same helper a local
`var`/short-var-decl already uses to store its own initializer), one plain
`evaluate, then store into the global` per entry. This function is then
registered into LLVM's own `@llvm.global_ctors` mechanism - a standard,
well-documented array of `{ i32, ptr, ptr }` entries (`{ priority, ctor
function pointer, associated data }`, appending linkage) any real linked/
loaded program's C runtime startup sequence scans and calls, in priority
order, before ever reaching `main`. A program whose every global happens to
be compile-time-constant gets no `llvm_lang.global_init` function and no
`@llvm.global_ctors` array at all - this mechanism leaves no trace in the IR
unless it's actually needed.

**Declaration order, not a full dependency graph:** every queued
initializer runs in plain source declaration order across the whole package
(each file in processing order, each file's own globals in the order
they're written) - a deliberately narrower simplification than Go's own real
spec (which topologically sorts by actual variable dependencies) - see
`DECISIONS.md`'s dated entry for why this was scoped this way. A global's
initializer referencing another global declared *later* in the same package
sees only that other global's zero value, not whatever its own initializer
would eventually compute - `TestGlobalNonConstantInitializersRunInDeclarationOrder`
(`src/codegen/globals_test.go`) asserts this exact behavior directly, not
just that it type-checks/compiles.

**JIT execution needs this triggered manually:** unlike a normal linked/
loaded program, `cmd/llvmc`'s JIT path (`jitRunMain`) never goes through a
real C runtime startup sequence that would scan `@llvm.global_ctors` on its
own - MCJIT's `ExecutionEngine` has no such thing. `jitRunMain` (and this
package's own test helper, `compileAndJIT`) calls
`engine.RunStaticConstructors()` explicitly, right after creating the engine
and before ever looking up/calling `main` - go-llvm's exact binding for this
purpose. Always safe to call even when no `@llvm.global_ctors` array exists
at all (a program with only compile-time-constant globals). `-emit-llvm`
needs no such change: it never reaches `llvm.NewExecutionEngine` in the
first place (see its own section below), so the synthesized init function
and `@llvm.global_ctors` array simply show up in the printed IR text like
any other generated code, unexecuted - consistent with `-emit-llvm` never
executing anything, before or after this feature.

## The `print` builtin, concretely

`print(x)` lowers to a call into libc's `printf` (declared extern - there's
no runtime of this language's own yet): every numeric width gets its own
format specifier (`"%d\n"` for `i8`/`i16`/`i32` - the narrower two widths are
sign-extended to `i32` first, see "print's printf format specifiers" above -
`"%lld\n"` for `i64`, `"%f\n"` for `f32`/`f64` - `f32` is extended to `f64`
first, matching C's own variadic float-promotion rule), a string argument
uses `"%.*s\n"` (the explicit length means a non-null-terminated string
value never needs `strlen`), and a bool argument selects between two cached
`"true"`/`"false"` string values first. **print always appends a trailing
newline** - there's no separate "print without newline" builtin.

A struct or array value is rendered recursively, Go-`fmt`-`%v`-inspired (not
an exact match of Go's own output, just a reasonable pick - see
`genPrintStructValue`/`genPrintArrayValue`, `src/codegen/runtime.go`):

- a struct prints as `{f0 f1 ...}` - each field's value, space-separated, in
  declaration order, wrapped in braces.
- an array prints as `[e0 e1 ...]` - each element's value, space-separated,
  in index order, wrapped in brackets.
- a struct/array-typed field or element is rendered the same way,
  recursively - e.g. a struct of two `Point{x, y int}` fields prints as
  `{{1 2} {3 4}}`.

This is built from repeated `printf` calls (one per field/element, plus one
per punctuation character) rather than one combined format string - there's
no way to know a struct/array's shape at format-string-construction time in
general, so each piece is its own call. Every nested field/element uses a
"bare" (no trailing newline) format string; only the outermost `print(...)`
call appends the actual trailing newline, once, after the whole value has
finished rendering.

## First-class functions: fat pointers, and direct vs. indirect calls

See `LANGUAGE.md`'s "First-class functions" section for the language-level
feature (a free function's name is now a value) and `DECISIONS.md` for why
the representation below was chosen the way it was. **This section describes
the representation as it shipped in that first round; the "Lambdas" section
below documents the uniform-ABI thunk mechanism a later round added on top of
it** - in particular, the "`ctxPtr` is extracted but never passed along"
claim this section originally made is no longer accurate (see "Lambdas"'s
own note on this) once a genuine closure exists that actually needs `ctxPtr`
passed through an indirect call.

**Representation.** `sema.TypeFunc` maps to the literal (unnamed) LLVM
struct `{ ptr, ptr }` - a "fat pointer" of `{ fnPtr, ctxPtr }` (`llvmType`,
`src/codegen/types.go`). A bare, uncalled reference to a declared free
function (`add`, not `add(...)`) builds this struct (`genFuncValue`,
`src/codegen/expr.go`); `ctxPtr` is always `llvm.ConstNull(g.ptrTy)` for this
case specifically - a free-function reference never closes over anything, so
there's nothing to put there. `genFuncValue` is commented as the exact
extension point a future bound-method value (`p.move` referenced without a
call) could use instead - closing over the receiver's own address as
`ctxPtr` rather than null - so the representation and calling convention
need no redesign for that, either, if it's ever built. Passing/returning/
storing a function value moves this two-field struct like any other small
aggregate value, the same convention already used for structs/arrays/strings
(see "Structs/arrays/strings are passed and returned as real LLVM aggregate
types" below).

**Direct vs. indirect calls.** `genCallExpr`'s dispatch (`src/codegen/expr.go`)
mirrors sema's own (`funcSigForCall`, `src/sema/typecheck.go`) exactly, so
there is exactly one place on each side of the pipeline that decides which
of the two a given call is:

- A **direct** call - callee is a plain `Ident` resolving (via `Info.Refs`)
  to an actual declared free function (`sema.SymFunc` with a real `FuncDecl`,
  i.e. `Decl != InvalidNode` - `isDirectFuncCall`) - compiles to a plain,
  ordinary `call` instruction (`genFuncCall`), exactly as before this round:
  looks the callee's LLVM function straight up in `g.funcs` and calls it,
  by its own real signature - no `ctxPtr` involved at all. The fat-pointer
  representation is never constructed or touched for this case at all - zero
  indirection overhead for the common case of calling a function by its own
  name. **This is completely unaffected by the "Lambdas" section below** -
  a lambda is never reachable through a direct call in the first place (see
  that section), so this path needed no changes when lambdas were added.
- An **indirect** call - anything else that type-checked as callable: a
  function-typed variable/parameter, an ordinary (non-method) struct field of
  function type (`cb.fn(5)` - see `LANGUAGE.md`'s "First-class functions"
  section; `isMethodCall` is what tells a real method-call `MemberExpr` apart
  from a func-typed-field one, mirroring sema's own `methodSigForCallee`
  `isField` distinction exactly), or any other expression whose value is
  itself a function (e.g. a call whose own result is a function, so
  `getAdder()(x)` chains straight through) - goes through `genIndirectCall`:
  evaluate the callee as an ordinary value expression to get its fat-pointer
  struct, `ExtractValue` out both `fnPtr` and `ctxPtr`, build the
  `llvm.FunctionType` to call through directly from the callee's own
  `sema.Type` (`Params`/`Return`, plus a leading `ctxPtr` parameter - see
  "Lambdas" below for why - there's no `FuncDecl` node backing an indirect
  callee the way a direct call's `g.funcs` lookup has), and `CreateCall`
  through that raw pointer with `ctxPtr` passed as the real first argument. A
  func-typed field's own fat-pointer value is read exactly like any other
  field access (`genAddr`'s `MemberExpr` case plus `genLoad`) - no special
  casing was needed there at all, only in the dispatch that decides a
  `MemberExpr` callee isn't a method call.

```llvm
; func apply(fn func(int) int, x int) int { return fn(x) }
define i32 @apply({ ptr, ptr } %0, i32 %1) {
  %3 = extractvalue { ptr, ptr } %2, 0
  %4 = extractvalue { ptr, ptr } %2, 1
  %6 = call i32 %3(ptr %4, i32 %5)
  ...
}

; apply(double, 5) - a direct call passes a literal fat-pointer constant,
; ctxPtr always null for a free-function reference - but fnPtr is now
; double's own uniform-ABI thunk (double.thunk), not double's own real
; address (see "Lambdas" below):
%4 = call i32 @apply({ ptr, ptr } { ptr @double.thunk, ptr null }, i32 5)
```

## Lambdas

See `LANGUAGE.md`'s "Lambdas" section for the language-level feature (a real
function-literal expression, `FuncLit`, capturing an enclosing local/
parameter by reference) and `DECISIONS.md`'s dated entry for the uniform-ABI
thunk resolution this section documents in detail. This is the one feature
this round that's a real, deliberate consumer of the arena allocator (see
"The arena allocator" above) beyond string concatenation/dynamic arrays -
every captured variable's storage, and every closure's own capture context,
is arena-allocated, per that section's already-stated default for any new
heap-needing feature.

### Capture analysis and heap promotion (sema decides, codegen executes)

`sema.Check`'s capture analysis (`src/sema/capture.go`) computes, for every
`FuncLit` node, the ordered list of enclosing-scope symbols it captures by
reference (`Info.Captures[lit]`), and marks each captured `*sema.Symbol`
(`Symbol.Captured`). Codegen makes exactly one decision based on this, at
exactly one call site (`allocLocalSlot`, `src/codegen/func.go`), shared by
every place a local variable/parameter's storage is allocated
(`genVarDecl`/`genShortVarDecl`, `src/codegen/stmt.go`; the param loops in
`genFuncBody`/`genConstructorBody`/`genLambdaFunc`, `src/codegen/func.go`
and `expr.go`): a captured symbol's storage comes from the arena
(`genArenaAlloc`) instead of `createEntryAlloca`'s ordinary stack alloca -
this is necessary, not an optimization choice, since a captured variable's
*address* is stored inside a lambda's own capture context, and that lambda's
value can outlive its declaring function's own stack frame the moment it's
returned, stored, or passed onward (exactly the `makeCounter` example in
`LANGUAGE.md`). Both paths return the identical `ptr`-typed `llvm.Value`
shape (this project already treats every pointer as opaque - see
`codegen.go`'s `ptrTy` field comment) - every reader (`genAddr`'s `Ident`
case, now routed through the shared `addrOfSymbol` helper described below)
treats the result identically regardless of which one it turned out to be. A
non-captured local is completely unaffected, unchanged from before this
round.

### Representation: the exact same fat pointer, `ctxPtr` finally does real work

A lambda value is still exactly `{ fnPtr, ctxPtr }` - the identical
two-field struct type `sema.TypeFunc` already lowered to for a bare
free-function reference (see "First-class functions" above) - there is no
second, parallel representation. For a genuine lambda, `ctxPtr` points to a
freshly arena-allocated **capture-context struct**: a synthesized, anonymous
LLVM struct (`g.ctx.StructType`, built fresh per literal in `genFuncLit`,
`src/codegen/expr.go`) with one `ptr`-typed field per captured symbol, in
`Info.Captures[lit]`'s own order - each field holds that variable's own
already-arena'd *address*, not a copy of its value, since capture is by
reference. `genFuncLit` resolves every captured symbol's address (via
`addrOfSymbol`, see below) *before* switching into the literal's own
function-generation state - it's still running in the *enclosing* function's
own context at that point, exactly where those addresses are directly
reachable.

`genFuncLit` builds the closure value itself as a real runtime aggregate
(`llvm.Undef(g.funcValTy)` + two `CreateInsertValue`s), not a `ConstStruct`
the way a bare free-function reference's fat pointer is (`genFuncValue`) -
`ctxPtr` here is a genuine runtime value (an `arena_alloc` call's result),
and LLVM's `ConstStruct` requires every field to itself already be a
constant; embedding a non-constant SSA value into a literal constant
aggregate is invalid IR that only surfaces as a verifier failure ("Use of
instruction is not an instruction!"), not a Go-level type error - a real
mistake hit and fixed while building this feature, not a hypothetical
concern. A capture-free literal's `ctxPtr` is still a genuine `ConstNull`, so
that case keeps the cheaper `ConstStruct` path unchanged.

Each `FuncLit`, wherever it lexically appears (even nested inside another
function or another lambda), becomes its own independent, real, top-level
LLVM function (`genLambdaFunc`) with a synthesized name -
`llvm_lang.lambda.N`, `N` a plain per-module monotonically increasing
counter (`Generator.lambdaCounter`) - simpler than deriving a name from
"whichever function lexically encloses this literal" and equally guaranteed
collision-free, since every lambda gets its own fresh number regardless of
nesting depth or which function contains it.

**Generating a nested literal's body without disturbing the enclosing
function.** `genLambdaFunc` temporarily replaces every one of `Generator`'s
per-function-frame fields (`curFn`/`entryBlock`/`locals`/`loopStack`/
`curFunc`/`curReceiver`, plus the three lambda-specific ones below) with the
literal's own fresh state - and the builder's own current insert block with
the literal's own entry block - saving each in a plain local variable first
and restoring all of them once the literal's body is fully generated. This
is the same save-in-a-local/restore-after-recursing shape
`sema/typecheck.go`'s `checkFuncLit` already uses for its own `curFunc`, one
layer up: since this is an ordinary (non-reentrant) function call, Go's own
call stack already handles arbitrary nesting depth for free - a `FuncLit`
nested inside another simply recurses into `genLambdaFunc` again, one level
deeper, saving/restoring the identical fields around its own body.

**Reading a captured variable from inside a lambda: one more indirection
than an ordinary local.** Three new `Generator` fields describe the function
*currently being generated*, when (and only when) it's itself a lambda's own
synthesized function - the zero value/nil for an ordinary function, method,
or constructor, none of which ever receives a `ctxPtr` parameter at all:
`curCtxPtr` (that function's own real first parameter), `curCaptureIndex`
(each captured symbol's field index within `curCaptureTy`, its own
capture-context struct type - needed as `CreateStructGEP`'s aggregate-type
argument). `genAddr`'s `Ident` case and `genFuncLit`'s own capture-context-
building lookup both now go through one shared helper, `addrOfSymbol`: check
`g.locals` first (a symbol owned directly by the function currently being
generated - works identically whether its storage is a stack alloca or an
arena allocation), then `g.globals` (a top-level var), and only then
`curCaptureIndex`/`curCtxPtr` - load the function's own `ctxPtr` parameter,
`CreateStructGEP` to the matching field (a pointer), `CreateLoad` *that*
(getting the captured variable's real address), then the caller
loads/stores through *that* address exactly like any other lvalue.

Routing *both* call sites through this one shared lookup is what makes a
variable captured **two or more enclosing function levels down** work with
no special relaying code anywhere: sema's own capture analysis
(`sema/capture.go`) already marks a doubly-nested lambda's outer variable as
captured by *every* enclosing lambda between it and the variable's own
owning function, not just the innermost one (see that file's own doc
comment for why walking straight through a nested `FuncLit`'s subtree,
rather than stopping at its boundary, produces exactly this). So when
`genFuncLit` is asked for some symbol's address while generating an
*enclosing* lambda's own body (to relay it into a `FuncLit` nested inside
that lambda), and that enclosing lambda doesn't own the symbol directly
either, `addrOfSymbol`'s third branch already knows how to fetch it - through
that enclosing lambda's *own* `ctxPtr` - with zero additional bookkeeping.
`TestTwoLevelNestedClosureCapture`/`TestTwoLevelNestedCaptureRelaysThroughBothLambdas`
(`src/codegen/lambda_test.go`, `src/sema/lambda_test.go`) exercise exactly
this shape end to end.

### The uniform-ABI thunk: resolving the direct-vs-indirect calling-convention conflict

A free-function reference and a genuine lambda can both flow through the
identical `func(T1, T2) R`-typed variable/parameter at the language level -
an indirect call through that variable can't know statically which of the
two it's holding at runtime, yet it has to emit one single, valid call-
instruction shape. This is a real conflict, not just an inconvenience: a free
function's own real declared LLVM signature (`g.funcs[sym]`) has **no**
`ctxPtr` parameter at all (so a *direct* call stays genuinely zero-overhead -
see "First-class functions" above, completely unchanged), but a genuine
lambda's own real underlying function (`genLambdaFunc`) **must** take
`ctxPtr` as a real, dereferenced first parameter - it needs to actually read
through it to reach its captures. Calling through a function pointer whose
real underlying function has a different real parameter list than the call
site expects isn't "probably fine on this target's ABI" - it's genuinely
invalid, UB-risking IR that can silently corrupt the stack/registers rather
than crash cleanly.

**Resolved the standard way real closure implementations do: every
function-value's real, underlying LLVM function - whichever kind it turns
out to be - shares one uniform, `ctxPtr`-first calling convention the moment
it's called *indirectly* through a fat pointer.** Concretely:

- A **direct** call to a statically-known free function (`add(1, 2)`) is
  completely untouched - it bypasses the fat pointer entirely and calls
  `add`'s own real, natural (`ctxPtr`-less) signature, exactly as before this
  round (see "First-class functions" above).
- A **bare reference to a free function used as a value** (`fn := add`, not
  `add(...)`) no longer puts `add`'s own real address into the fat pointer's
  `fnPtr` field. `genFuncValue` now calls `genFuncThunk(sym)` instead, which
  builds (once, memoized in `Generator.thunks` - not regenerated per
  reference) a small adapter function named `add.thunk`: real signature
  `R add.thunk(ptr ignoredCtx, T1, T2, ...)`, whose entire body ignores its
  own `ctxPtr` parameter and calls straight through to `add`'s own real
  function with the rest, returning its result unchanged. `fnPtr` in the fat
  pointer is the thunk's address, not `add`'s own.
- A **genuine lambda's** own synthesized function (`genLambdaFunc`) already
  has this uniform shape natively - it needs a real `ctxPtr` regardless of
  whether it actually captures anything (every lambda is always called
  indirectly, never directly - see below - so every one gets a `ctxPtr`
  parameter unconditionally, for uniformity, even one that never reads it) -
  so it needs no thunk of its own; its own address goes directly into the
  fat pointer.
- **Every indirect call** (`genIndirectCall`) now always extracts *both*
  `fnPtr` and `ctxPtr` from the fat pointer and passes `ctxPtr` along as the
  callee's real first argument, unconditionally - whether `fnPtr` turns out
  to be a free function's thunk or a genuine lambda's own function, the call
  site's own built `llvm.FunctionType` (`ctxPtr`'s type, then the callee's
  ordinary declared parameter types) now always matches the real callee's
  own real signature, since both kinds of real callee share the identical
  shape.

A `FuncLit` is *never* reachable through a direct call at all, by
construction: `isDirectFuncCall` only ever matches a plain `Ident` resolving
to a declared `sema.SymFunc`, and a function-literal expression is neither an
`Ident` nor a declared symbol - so every lambda value, immediately-invoked or
otherwise, always goes through `genIndirectCall`, and so always gets the
uniform `ctxPtr`-first treatment automatically, with no separate dispatch
case needed for it in `genCallExpr` at all.

This preserves "First-class functions"'s explicit, verified "direct calls
are genuinely zero-overhead" property completely untouched (confirmed
concretely, not just by inspection - `TestDirectCallStillCompilesToPlainCall`,
already in `src/codegen/firstclass_func_test.go`, still passes unchanged,
and a fresh `-emit-llvm` inspection of a plain `add(2, 3)` call shows exactly
`call i32 @add(i32 2, i32 3)`, no thunk generated at all since `add` is never
referenced as a bare value in that program), while making indirect calls
correctly polymorphic over "was this a plain function or a real closure" -
exactly the property a `func(T) R`-typed variable needs, since it can hold
either one. `TestUniformAbiAcrossPlainFunctionAndLambda`
(`src/codegen/lambda_test.go`) is the test that would catch a regression
here directly: a single `func(int, int) int`-typed variable holds a plain
free-function reference first, then a genuine lambda, calling it indirectly
both times through the identical variable.

## `main` is the real entry point

The function literally named `main` (no receiver) becomes the real LLVM
`i32 @main()` C entry point, regardless of whether the source declares a
return type for it: a bare `return`/falling off its end becomes `ret i32 0`
(a real, valid exit code, never `unreachable` - see the terminator-safety
section below), and `return expr` returns `expr` directly (typed `int` ==
`i32`, so no cast is ever needed - see above).

Codegen doesn't validate this itself: `main` declaring anything other than
no return type or `int` is a real semantic rule (see `LANGUAGE.md`'s "The
`main` function's return type" section), enforced by `sema.checkFuncDecl`
before codegen ever runs - `declareFuncSignature` (`src/codegen/func.go`)
simply forces `main`'s LLVM return type to `i32` unconditionally, trusting
that whatever declared return type sema already accepted is one of those two
(this used to be a codegen-level `g.errorAt` check instead, which was real
type-checking logic living at the wrong layer - see `AGENTS.md`'s
Architecture section).

## Local variables: entry-block allocas

Every `var`/short-var-decl/parameter gets a stack slot via `alloca`, always
hoisted into the function's **entry** block (`createEntryAlloca`), never
emitted at the literal point of declaration. This matters for correctness,
not just style: an `alloca` inside a loop body's own basic block is a fresh
stack allocation on *every dynamic execution* of that block, not just a
lexical one - hoisting to the entry block means a `var`/`:=` declared inside
a loop allocates once and is simply re-stored each iteration.

## Method receivers: an implicit pointer parameter

A method's receiver (see `LANGUAGE.md`'s Structs section - always implicit,
always by-reference) lowers to a real, explicit first LLVM parameter of
type "pointer to the receiver's struct type". A method call's receiver
expression is lowered to its *address* (`genAddr`), never a loaded copy, so
a mutating method's writes are visible to the caller. `this` inside the
method body is that same pointer parameter directly - it needs no `alloca`
of its own, since it already *is* an address.

## Constructors

See `LANGUAGE.md`'s "Constructors" section for the language-level feature
(`constructor(params) { body }` blocks nested inside a struct declaration,
overloaded by argument count only, invoked via `Name(args)` call syntax
distinct from a `Name{...}` composite literal).

**Each constructor lowers to its own real LLVM function**, reusing the
*exact same* implicit-first-pointer-parameter convention an ordinary
method's receiver already uses (see "Method receivers" above) - a
constructor's own declared parameters follow that implicit pointer, and its
LLVM return type is always `void`: a constructor never declares (or needs) a
return type of its own, since it "returns" the struct being constructed
implicitly, by populating `this` through that same implicit pointer, exactly
like a mutating method's writes are visible to its caller. A bare `return`
inside a constructor body lowers to `ret void`, same as any other void
function/method (`genReturnStmt`) - sema rejects `return expr` at check time
(see `LANGUAGE.md`), so codegen never needs to consider that case.

**Naming**: a constructor has no name of its own in the source (see
`LANGUAGE.md`: it's selected by argument count, not called by name), so its
generated LLVM function is named `Struct.constructor.N` (`N` its declared
parameter count) - the same `Type.MethodName` convention an ordinary
method's own generated function already uses (`declareFuncSignature`),
adapted for a constructor's lack of a name: arity already uniquely
identifies a struct's constructor (see `StructInfo.Constructors`, keyed by
arity for exactly this reason), so it doubles as the disambiguating suffix
here too.

**Declared in its own pass**, mirroring `declareFuncSignature`/`genFuncBody`'s
own split into a signature-declaration pass and a body-generation pass
(`declareConstructorSignature`/`genConstructorBody`, `src/codegen/func.go`):
every constructor in the whole program is declared before any function or
constructor body is generated, so a constructor call reaches its callee
already declared regardless of declaration order - another constructor
calling it first, a call from a different file, or (since a struct's
constructors are usable cross-package the moment the struct itself is
exported - see `LANGUAGE.md`) a call from a different package entirely.

**Lowering a call** (`genConstructorCall`, `src/codegen/expr.go`): sema
already resolved *which* constructor a call selected, recording that
specific constructor's own `*sema.Symbol` (`sema.SymConstructor`) directly
onto the call's callee node in `Info.Refs` (`checkConstructorCall`,
`sema/typecheck.go`) - the same "record which specific declaration a call
resolved to" idea an ordinary method call's callee already carries. Codegen
recognizes this the same way it recognizes every other sema-resolved call
shape - a plain `Info.Refs` kind check (`isConstructorCall`) - and lowers it
by: allocating a fresh stack slot for the struct being built (the same
alloca-then-load approach a struct composite literal already uses -
`genAddr`'s `CompositeLit` case), calling the selected constructor with that
alloca's own address as the implicit `this` argument followed by the call's
own evaluated arguments, then loading and returning the now-populated value
- matching how this package already returns struct/array/string values by
value everywhere else (see "Structs/arrays/strings are passed and returned
as real LLVM aggregate types" below). This works identically whether the
call's callee is a bare `Ident` (a same-package struct type) or a
`MemberExpr` (a package-qualified one, `pkg.Point(args)`) - `isConstructorCall`
never needs to branch on the callee's own node kind, only on what
`Info.Refs` resolved it to.

`Generator.ctors` is `Generator.funcs`' dedicated counterpart for
constructors - kept as its own map (rather than folded into `funcs`) purely
for read-site clarity: a constructor's `*sema.Symbol` (`sema.SymConstructor`)
is a completely different declaration shape from an ordinary free function's
or method's (`sema.SymFunc`), even though the two Symbol pointer spaces never
actually collide.

## Pointers: real `*T`, `&`/`*`, `new`/`delete`, auto-deref, and `nil`

See `LANGUAGE.md`'s "Pointers" section for the language-level feature. Every
`sema.TypePointer` lowers to `g.ptrTy` (`llvmType`, `src/codegen/types.go`) -
the same single opaque `ptr` LLVM type this package already uses everywhere
else a pointer-shaped value is needed (a string's data pointer, a dynamic
array's, a first-class function value's fat-pointer fields, a method's
implicit receiver) - a pointer's pointee type never affects its own LLVM
representation, only what a dereference/GEP through it targets, so there's
no need for a distinct LLVM pointer-to-`X` type per pointee.

**`&x`/`*p`** are both `UnaryExpr` nodes distinguished by operator text, same
as unary `-`/`!` (`genUnaryExpr`, `src/codegen/expr.go`) - handled before
either falls through to the shared "evaluate the operand as an rvalue" path
the arithmetic/logical operators use:

- **`&x`** lowers to `genAddr(x)` directly - the exact same address
  computation an assignment target, `++`/`--`, or a method-call receiver
  already uses for the same expression shapes (`Ident`, `MemberExpr`,
  `IndexExpr`, `ThisExpr`, another `*p`) - so `&` never spills its operand
  into a fresh temporary the way `genAddr`'s own fallback case does for a
  genuine non-addressable rvalue; it *is* that fallback's addressable case,
  reused. `genAddr` gained one new case for this: `UnaryExpr("*")` as an
  lvalue (`*p = v`, or `&*p`) evaluates its own operand (`p`) as a plain
  rvalue - the address a dereference reads/writes through *is* `p`'s own
  value, not `p`'s own address.
- **`*p`** as a value lowers to evaluating `p` (`genExpr`) and loading
  through the result - `CreateLoad(llvmType(pointee), ptrValue, "")`.

**`new T(args)`/`new T{...}`** (`genNewExpr`, `src/codegen/expr.go`) `malloc`s
exactly `sizeof(T)` bytes (`llvm.SizeOf`, the same null-pointer-GEP-trick
helper `genArenaAllocElems`/`genLambdaFunc`'s own capture-context sizing
already use) and initializes that address in place, reusing the *exact
same* lowering an ordinary stack-/field-allocated construction already
uses - just pointed at a different destination:

- A composite-literal inner (`new Point{1, 2}`) calls `genCompositeLitInto`
  directly against the malloc'd pointer, identical to `genAddr`'s own
  `CompositeLit` case except the destination is heap, not a fresh
  `createEntryAlloca`.
- A constructor-call inner (`new Point(1, 2)`) calls a new
  `genConstructorCallInto(dst, calleeNode, argNodes)` helper - factored
  *out* of `genConstructorCall` itself (which now just allocates its own
  stack slot and delegates to it) specifically so `genNewExpr` can reuse the
  identical this-pointer-as-hidden-first-argument calling convention against
  a malloc'd `dst` instead, without duplicating it.

Unlike `genConstructorCall`/a plain `CompositeLit`'s own `genExpr` case
(both of which load and return the constructed *value*, since this language
passes structs by value everywhere else - see "Structs/arrays/strings..."
below), `genNewExpr` returns the malloc'd pointer directly, never loading
through it - the entire point of `new` is a pointer that outlives the
current stack frame.

**`delete p`** (`genDeleteStmt`, `src/codegen/stmt.go`) is a direct call to
libc `free` against `p`'s own evaluated pointer value - no different from
any other libc-extern call this package already makes (`malloc`/`memcpy`/
`memcmp`/`memset`). **This is a real, separate heap from the bump-allocator
arena** `setupArena` builds (string concatenation/dynamic arrays) - `new`
mallocs its own individually-freeable block per call, never asking the arena
for space, and `delete` frees exactly that block via a real `free`, never
touching the arena's own bump cursor. The two heaps deliberately never mix:
`delete`ing a sub-block carved out of a shared arena chunk another
allocation still lives in would be a real bug, so `new`/`delete` simply stay
on their own separate, ordinary malloc/free heap instead. The arena itself
still has no per-allocation free at all (see `BLOCKERS.md`) - that
limitation is unchanged; `new`/`delete` are a new, independent code path, not
a fix to the arena's own leak.

**Auto-deref for member access** (`genReceiverAddr`, `src/codegen/expr.go`) -
shared by `genAddr`'s `MemberExpr` case and `genMethodCall` (a plain field
read/write and a method-call receiver are, at the address level, the exact
same "get me this struct value's address" operation): when the object
expression's own `sema.Type` is `TypePointer`, its *value* is evaluated
directly (`genExpr`, the pointer itself) rather than its *address*
(`genAddr`, the address of whatever variable happens to be holding the
pointer) - `p.field`/`p.method(...)` on a `*Point` therefore addresses the
exact same heap struct `(*p).field`/`(*p).method(...)` would, with no copy
in between; a mutating method called through a pointer receiver mutates the
same shared allocation every other alias of that pointer also sees.

**`nil`** (`sema.SymBuiltinValue`, see `LANGUAGE.md`) has no storage of its
own to load from - `genExpr`'s `Ident` case special-cases it exactly the way
it already special-cases a bare, uncalled function reference (`SymFunc`),
lowering straight to `llvm.ConstNull(g.ptrTy)` regardless of which concrete
`*T` sema resolved it to (a pointer's own pointee type never affects its
LLVM representation - see above).

## Structs/arrays/strings are passed and returned as real LLVM aggregate
## types, not manual `sret`/by-ref tricks

A struct, fixed-size array, or string value used as a plain function
parameter or return type lowers to LLVM's own aggregate/array type directly
(e.g. a two-field struct becomes the LLVM struct type `{i32, i32}` used
as-is for a parameter or return type) - LLVM's own backend ABI lowering
handles whatever register/hidden-pointer convention the target actually
needs, transparently, the same way it does for any other LLVM-to-LLVM call.
This works because every caller and callee in a given module is generated
by this same package; there's no need to match an external C ABI by hand.

## Terminator safety (LLVM requires every basic block to end in one)

`sema.Check` now runs a full "does every path return" flow analysis (see
`LANGUAGE.md`'s "Missing return" section) and rejects any function declaring
a return type whose body isn't guaranteed to return. Codegen still keeps a
fallback for whenever a function's lowered body falls off the end without
already ending in a terminator, purely as a defensive backstop (a validated
tree should never actually need it - see `emitFallbackTerminator` in
`src/codegen/func.go` for the full reasoning):

- `main`, and any function declaring no return type at all, get a real,
  correct terminator (`ret i32 0` / `ret void` respectively) - falling off
  the end of a void function is legitimate (same as Go, and sema places no
  termination requirement on it either), and `main` must always hand a real
  exit code back to its OS caller, never undefined behavior.
- Any other non-void function gets `unreachable` instead - reaching this
  given sema's guarantee should be impossible; `unreachable` records that
  assumption directly in the IR rather than inventing a fake return value
  that could silently mask a real bug, in case the flow analysis itself
  ever has a gap.

Within a single block, a statement that itself terminates (`return`,
`break`, `continue`, or an `if`/`else` where both branches terminate) simply
stops that block's remaining statements from being generated at all - there
is no `goto`/labels in this language, so nothing could ever jump back into
that dead code anyway.

## Multi-file packages: one shared Module per package

See `LANGUAGE.md`'s "Multi-file packages" section for the language-level
model (directory = package, non-recursive, shared scope). `GeneratePackage`
(`src/codegen/codegen.go`) is the multi-file counterpart to `Generate`
(now a thin wrapper: `Generate(tree, info, name)` is exactly
`GeneratePackage([]*ast.Tree{tree}, map[*ast.Tree]*sema.Info{tree: info},
name)`): it takes every file's `*ast.Tree` plus its own already-resolved/
checked `*sema.Info` (see `sema.ResolvePackage`/`sema.CheckPackage`) and
lowers all of them into **one shared `llvm.Module`** - not one module per
file with cross-module linking, which this package has no need for: every
file in a package always ends up in the exact same module regardless, so
introducing separate per-file modules would only add real linking
complexity (symbol visibility, declaring externs for cross-module calls)
to solve a problem that doesn't exist here. See `DECISIONS.md` for this
choice recorded as a dated decision.

**Why no cross-file plumbing was needed inside a single function body.**
Every codegen-internal lookup that resolves a *use* of a declared symbol -
`Generator.funcs` (a call's callee), `Generator.globals` (a global var
reference), `Generator.structLayouts` (a struct's field GEP indices/LLVM
type) - is keyed by `*sema.Symbol`/`*sema.StructInfo` pointer identity, not
by `ast.NodeIndex`. A `NodeIndex` is only meaningful relative to the one
`*ast.Tree` it came from (see `ast.NodeIndex`'s own doc comment) - two
unrelated declarations in two different files can share the same NodeIndex
value - but a `*sema.Symbol`/`*sema.StructInfo` pointer is already globally
unique the moment it's allocated (one fresh value per declaration, shared
by every file's own `Info.Refs`/`Info.Structs` - see `sema.ResolvePackage`),
so keying on it sidesteps the cross-tree ambiguity entirely. `funcs` used to
be keyed by the FuncDecl's own `NodeIndex` before this round; that's the one
lookup this round had to change (`declareFuncSignature`/`genFuncBody` now
fetch the function's own `*sema.Symbol` via `Info.Refs` first) - every other
map was already keyed correctly and needed no change at all.

Because of that, `genPackage` (the multi-file counterpart to the old
`genFile`) never needs to switch "which file's nodes am I currently
reading" *in the middle of* lowering one declaration's body, unlike
`sema.CheckPackage`'s checker (see `sema/typecheck.go`'s own doc comment on
this exact point): every pass (`declareStructType`/`defineStructBody`,
`genGlobalVarDecl`, `declareFuncSignature`, `genFuncBody`) only ever reads
nodes that are children of the one declaration currently being processed -
always in the same file - and a cross-file *reference* inside a function
body (a call to a function declared elsewhere, a struct type named
elsewhere) is resolved purely through the pointer-keyed maps above, which
are already fully populated for the *whole package* by the time any
function body is generated (every file's structs are declared before any
file's globals; every file's globals before any file's function
signatures; every file's function signatures before any file's function
bodies - see `genPackage`). `Generator.tree`/`Generator.info` still switch
per file, but only at the top of each pass's per-file loop, never mid-body.

## Imports: still one shared Module, now for the whole program

See `LANGUAGE.md`'s "Imports" section for the language-level model
(directory = package, `import "path"` resolved relative to the importing
file, exported-only cross-package access). Nothing above changes at all:
`GeneratePackage` is simply called with *every package's* trees/infos in the
whole program flattened into one `[]*ast.Tree`/`map[*ast.Tree]*sema.Info`
(built by `src/compiler`'s `CompileProgram` from `loader.LoadProgram`'s
already-resolved import graph, via `sema.ResolveProgram`/`CheckProgram` -
see their own doc comments, and the "`src/compiler`: pipeline orchestration"
section below for where that call now actually lives) - there is still only
ever **one shared `llvm.Module` for the entire program**, never one Module
per package linked
together, for exactly the same reason multi-file support already gave for
one Module per *file*: every `*sema.Symbol`/`*sema.StructInfo` lookup
(`Generator.funcs`/`globals`/`structLayouts`) is already keyed by pointer
identity, not by which tree - let alone which package - originally declared
it, so a package boundary is no more special to codegen than a file
boundary already was. This was verified, not assumed: `genPackage`'s own
five passes (structs, struct bodies, globals, func signatures, func bodies)
need no changes at all to correctly cover a multi-package program, since
they already iterate "every tree passed in" with no notion of package
grouping.

A package-qualified call (`mathutils.Add(...)`) is recognized in codegen
(`isPackageQualifiedCall`, `src/codegen/expr.go`) the same way sema
recognizes it (`Info.Refs` on the callee `MemberExpr`'s own object resolving
to `sema.SymPackage`) and lowered as a **plain direct call** - `genFuncCall`,
the exact same lowering an ordinary same-package free-function call already
uses - since a package-qualified function call has no receiver to compute,
unlike an ordinary method call (`genMethodCall`), which the dispatch in
`genCallExpr` still falls through to otherwise.

**Diamond dependencies are deduped by directory identity**, not re-lowered
per import edge: `loader.LoadProgram` loads a given package directory
exactly once regardless of how many other packages import it (see
`src/loader`'s own doc comment), so its trees appear exactly once in the
flattened list `GeneratePackage` receives - its functions/structs are
declared into the shared Module exactly once, and every importer's calls
into it resolve to that one declaration via the same pointer-keyed maps
above. There is no separate-compilation/linking concept for this project to
get right here at all (see `DECISIONS.md`).

# `src/compiler`: pipeline orchestration

Sits directly above `src/loader` in this project's own layering: `loader`
owns "given a path, discover/parse/resolve the file/package/program
structure" (pure I/O + discovery, per its own doc comment); `compiler` is
the next layer up - "given that loaded structure, actually run it through
the semantic/codegen pipeline". It exposes exactly two entry points:

- `CompilePackage(files []loader.SourceFile) *Result` - the flat-file-list
  case (no real filesystem/`loader.Program` needed): `lexer.NewFile` ->
  `parser.ParseFile` per file, then `sema.ResolvePackage` -> the shared
  tail below. `treePackage` is nil going into `sema.CheckProgram` - a
  single, import-less package has no cross-package export enforcement to
  do at all (see `sema.CheckProgram`'s own doc comment).
- `CompileProgram(prog *loader.Program) *Result` - the real,
  potentially-multi-package case: every package in `prog.Order` (already in
  dependency order - see that field's own doc comment) becomes one
  `sema.PackageUnit`, and every package's trees are flattened into one
  slice, driven through `sema.ResolveProgram` -> the same shared tail.

Both funnel into one unexported tail (`finishPipeline`) that mirrors this
file's own `GeneratePackage`/multi-file writeup exactly: `sema.CheckProgram`
-> `codegen.GeneratePackage` -> LLVM's own module verifier, stopping at the
first stage that reports an error-severity diagnostic in any file. A
`Result` carries every tree, every tree's own merged diagnostics (`Diags`),
and - only on full success - the generated, verified `*codegen.Module`
(`nil` on any failure; a rarer verifier-only failure with no source
position to attribute it to is `Result.VerifyErr` instead, `Diags` in that
case being otherwise empty). Disposal of a returned `Module` is the caller's
job, same as `codegen.GeneratePackage`'s own `Module.Dispose()` contract.

This package is deliberately **pure orchestration and CLI-agnostic**: no
`io.Writer`/stderr, no exit codes, no flag handling, and no lexer/parser/
sema/codegen *logic* of its own beyond calling those packages' existing
entry points in the right order - a `Result` is data a caller decides what
to do with (print how it wants, choose an exit code, feed it to a JIT, feed
it to an LSP), not something this package prints or exits on its own behalf.

# The `llvmc` CLI driver (`cmd/llvmc`)

The first way to actually *run* an llvm_lang program as a human, rather than
only proving the pipeline works via `go test`: given a path (a single source
file, or a directory - see `LANGUAGE.md`'s "Multi-file packages" section),
it resolves the whole program's transitive import graph
(`src/loader`'s `LoadProgram`, backed by `afero.NewOsFs()` - see
`LANGUAGE.md`'s "Imports" section for the path-resolution/dedup/cycle rules
this implements, and `src/loader`'s own package doc comment for why file
*parsing* now lives there too, not just discovery), hands the result to
`src/compiler`'s `CompileProgram` (see above) to drive it through the rest
of the pipeline, and on full success JIT-executes the resulting module's
`main` directly in this process - so the program's own `print` calls (real
libc `printf` calls under the hood) write to this process's real stdout,
which a `go test`-hosted JIT call can't easily show.

`cmd/llvmc` itself is now the thinnest possible CLI shell on top of
`src/loader` and `src/compiler`: flag parsing, printing every tree's
diagnostics from a `compiler.Result` (still via `diag.FormatSnippet` -
unchanged, see below), translating `Result.Module == nil` into the
compile-error exit code, and - on success - either dumping the verified
module's IR text (`-emit-llvm`) or JIT-executing it and propagating its
`i32` result as this process's own exit code. None of the actual
resolve/check/codegen/verify orchestration lives in this package anymore.

A single-package, single-file program (a directory containing exactly one
`.llx` file, or a file whose sibling directory has no other `.llx` files,
with no `import` declarations at all) goes through this exact same path -
there's no separate single-file/single-package code path in `llvmc` itself,
only `compileAndRun`/`compileAndRunPackage` (used by this package's own
in-process tests that build source strings directly, with no real
file/directory on disk and so no need to go through `loader.LoadProgram` at
all) staying as thin wrappers that call `src/compiler`'s `CompilePackage`
instead of `CompileProgram` (and so, transitively, `sema.ResolvePackage`
instead of `sema.ResolveProgram`, with no cross-package export enforcement -
see `CompilePackage`'s own doc comment) followed by the exact same
diagnostic-printing/JIT-or-emit tail (`finish`) `compileAndRunProgram`
shares - the same relationship `codegen.Generate`/`sema.Resolve`/
`sema.Check` each have to their own multi-file counterpart, one level
further up.

## Building and running

```powershell
$mingw = "C:\msys64\mingw64\bin"
if ($env:Path -notlike "*$mingw*") { $env:Path = "$mingw;$env:Path" }
go build -tags=llvm22 -o llvmc.exe ./cmd/llvmc

.\llvmc.exe path\to\program.llx     # a single file - its containing
                                    # directory's other .llx files (if any)
                                    # are part of the same package too
.\llvmc.exe path\to\a\directory     # every .llx file directly in it
```

## The `-emit-llvm` flag

```powershell
.\llvmc.exe -emit-llvm path\to\program.llx
```

Runs the exact same pipeline - including LLVM's own module verifier - but
instead of JIT-executing the result, prints the generated module's LLVM IR
text (`Module.LLVM.String()`, the same method the repo root `main.go` smoke
test already uses) to stdout and exits 0, without ever executing anything.
This is purely additive: no flag keeps the default JIT-execution behavior
exactly as before. It's meant for debugging a language feature's codegen
lowering without writing a throwaway `go test` every time.

```powershell
PS> .\llvmc.exe -emit-llvm .\examples\hello.llx
; ModuleID = '.\examples\hello.llx'
source_filename = ".\\examples\\hello.llx"
...
@.str.0 = private unnamed_addr constant [14 x i8] c"Hello, World!\00"
...
define i32 @main() {
entry:
  %0 = call i32 (ptr, ...) @printf(ptr @.fmt.str, i32 13, ptr @.str.0)
  ret i32 0
}
PS> echo $LASTEXITCODE
0
```

Note the source string still shows up as module-level constant data
(`@.str.0`) even here - every string literal `codegen` sees gets embedded as
a global regardless of whether `main` is ever actually called (see the
"`string` representation" section above) - but `main`'s body is never run,
so `printf` never actually fires and nothing is written to the real stdout
beyond the IR text itself.

Since this path never reaches `llvm.NewExecutionEngine`, disposal is a plain
`Module.Dispose()` - same as the diagnostic/verification-failure paths below,
not the JIT path's more careful engine/context teardown.

## Source file extension: `.llx`

This project's source files use the extension `.llx`, not `.ll` - `.ll` is
already LLVM's own textual IR format's extension, and reusing it here would
be a real (and confusing) collision with that, since this compiler also
prints/inspects real LLVM IR (`Module.LLVM.String()`) elsewhere. The
compiler pipeline proper still doesn't inspect a file's extension at all -
`lexer.NewFile` just takes a name (used only for diagnostics) and the source
text, exactly as before - `src/loader`'s directory scan is the one place
`.llx` is now checked for real (case-insensitively), since "every `.llx`
file directly in this directory" (see `LANGUAGE.md`'s "Multi-file packages"
section) has to mean *something* concrete when resolving a bare directory
path rather than one named file. See `examples/` at the repo root for sample
`.llx` programs, each now its own subdirectory so a single-file example
doesn't accidentally pull its siblings into the same package (`hello/`,
a struct+method+loop+arithmetic program in `features/`, a deliberately
invalid program in `error/` demonstrating the failure path, and
`multifile/` - three files demonstrating cross-file calls, see that
directory's own doc comments).

## Exit codes

- **2** - a usage error: no path argument, an unrecognized flag, the path
  couldn't be resolved to a real file/directory, its resolved directory has
  zero `.llx` files in it, an imported package directory couldn't be found,
  or a real import cycle was detected (see `src/loader`'s `Load`/
  `LoadProgram`). A short message goes to stderr; nothing is compiled.
- **1** - a compile-time diagnostic from the lexer, parser stage, or from
  `src/compiler`'s `finishPipeline` (see this file's own "`src/compiler`:
  pipeline orchestration" section above): `sema.ResolveProgram` (or
  `ResolvePackage`, for an import-less package - `cmd/llvmc` always goes
  through `loader.LoadProgram` -> `compiler.CompileProgram` regardless of
  whether the compiled path is a single file or spans several packages, so
  this is the one path actually reachable, not the older single-file
  `sema.Resolve`/`sema.Check`/`codegen.Generate` entry points this section
  used to describe here), `sema.CheckProgram`, or `codegen.GeneratePackage`
  (the pipeline stops at the first stage reporting an error-severity
  diagnostic in any file) - or the module failing LLVM's own verifier, or a
  module with no `main` function to JIT-execute. Every diagnostic from
  whichever stage failed is printed to stderr via `diag.FormatSnippet` (a
  `file:line:col: severity: message` header plus the offending source line
  and a caret). With `-emit-llvm`, this is the only non-zero exit code
  reachable at all - a verified module's IR is always printed and the
  process always exits 0 afterward (see below).
- **otherwise** - the language program's own exit code. `func main()` always
  lowers to a real, parameterless `i32 @main()` regardless of whether the
  source declares a return type for it (see the "`main` is the real entry
  point" section above) - falling off the end or a bare `return` becomes
  `ret i32 0`, and `return expr` returns `expr` directly (`int` is `i32`, so
  no truncation/cast is ever needed either way - see "`int` is 32-bit"
  above). `llvmc` propagates that i32 result directly as its own process
  exit code, so `func main() int { return 2 + 3 }` really does exit the
  `llvmc` process with code 5. This doesn't apply with `-emit-llvm`: `main`
  is never executed, so its return value never becomes the process's exit
  code - see the "`-emit-llvm` flag" section above.

## A non-obvious disposal detail

Once a `codegen.Module`'s `LLVM` field is handed to
`llvm.NewExecutionEngine`, the engine takes ownership of it - calling
`Module.Dispose()` afterward would double-free it (this exact pitfall is
already documented on `src/codegen/codegen_test.go`'s `compileAndJIT`
helper). So the two paths that never reach a live execution engine - a
codegen diagnostic, or a failed `llvm.VerifyModule` - already call
`Module.Dispose()` themselves, inside `src/compiler`'s `finishPipeline`,
before ever handing a `Result` back (a `Result.Module` is always nil on
either path - see `src/compiler`'s own doc comment - so `cmd/llvmc` never
gets a live `*codegen.Module` for either of these two cases at all, let
alone a chance to double-dispose one). Once JIT execution is about to
happen (a `Result.Module` came back non-nil), disposal goes through the
engine (`engine.Dispose()`) and then the module's owning `Context`
(`mod.Ctx.Dispose()`), in that order, instead - `cmd/llvmc`'s `jitRunMain`,
unchanged. The one remaining case `cmd/llvmc` itself still calls
`Module.Dispose()` directly is `-emit-llvm`'s own success path (`finish`) -
a verified module that's never handed to `llvm.NewExecutionEngine` at all.
