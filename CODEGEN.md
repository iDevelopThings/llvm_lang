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

## Maps

See `LANGUAGE.md`'s "Maps" section for the language-level feature
(`map[K]V`, `make`/insert/lookup/`len`/`remove`, the key-comparability
restriction, the `v, ok := m[k]` two-result index expression, iteration/
composite-literal explicitly out of scope). All of this lives in its own
file, `src/codegen/maps.go` - a genuinely new runtime mechanism, unlike
dynamic arrays (which mostly reuse the arena allocator's existing "grow a
{ptr,len,cap} buffer" shape) - with no prior precedent in this codebase to
extend.

**Representation.** A map value is a single opaque `ptr` (`g.ptrTy` -
`sema.Type{Kind: TypeMap}`'s `llvmType` case treats it exactly like
`TypePointer`), pointing at a small, arena-allocated **control block**
(`g.mapCtrlTy`, one shared LLVM struct type for every map instantiation,
mirroring `dynArrTy`'s own "one shape serves every element type" reasoning):

```
{ ptr buckets, i32 count, i32 bucketCount }
```

The control block's own address never changes across the map's lifetime -
only what `buckets`/`bucketCount` point at/hold changes, in place, when the
table grows (`genMapGrowIfNeeded`). This is exactly what makes assigning one
map-typed variable to another share the same live table (LANGUAGE.md's own
"maps are a reference type" rule): copying the map value just copies this
one pointer, and every copy still reaches the identical, mutable control
block.

**Bucket layout.** Each bucket is `{ i8 tag, K key, V value }` - built fresh
per call site via `g.mapBucketType(keyT, valT)` (`g.ctx.StructType(...)`),
not cached per-(K,V) pair on the `Generator`: LLVM's own context already
structurally interns two identical unnamed struct types, so a Generator-side
cache would only save a little bookkeeping, not a real allocation - the same
reasoning `TypeMultiReturn`'s own `llvmType` case already relies on. `tag` is
one of three sentinel byte values (`mapTagEmpty` = 0, `mapTagOccupied` = 1,
`mapTagTombstone` = 2) - zero-filling a freshly allocated bucket array
(`memset`, exactly like `genMakeCall`'s own dynamic-array buffer) is what
makes every bucket start life `mapTagEmpty` for free, with no separate
per-bucket initialization loop needed.

**Collision resolution: open addressing with linear probing and
tombstones**, not separate chaining - genuinely simple, no extra pointer
indirection per entry: `genMapProbe` (`maps.go`) starts at
`hash(key) mod bucketCount` and walks forward one bucket at a time
(wrapping around), for at most `bucketCount` steps (a bound the growth
policy below always guarantees is never actually reached - a real
`mapTagEmpty` slot is always found well before then), stopping the instant
it finds either a matching occupied key (a hit) or a genuine `mapTagEmpty`
slot (a definitive miss - open addressing's own standard rule: an empty slot
means the key can't possibly appear any further along the probe chain). A
`mapTagTombstone` slot (a deleted entry) never stops the probe on its own -
a live key further along the same original probe chain must still be
reachable past it - but the *first* available slot (empty or tombstone)
passed along the way is remembered as the eventual insertion point, so a
fresh insert naturally reuses an earlier tombstone instead of always
appending past it.

**`remove(m, k)`** marks the matching bucket `mapTagTombstone` (not
`mapTagEmpty` - `genMapRemoveCall`) rather than clearing it outright, for
exactly the probe-chain reason above, and decrements `count`. A no-op
against a nil map or an absent key, matching Go's own real `delete(m, k)`
behavior exactly.

**Growth: doubling, triggered at a 0.75 load factor**, mirroring dynamic
arrays' own doubling-capacity convention (`genMapGrowIfNeeded`): checked
right before any insert that isn't just overwriting an existing key's value,
growing whenever `(count+1)*4 > bucketCount*3` (computed in `i64` to stay
safe against `i32` overflow for a very large map). Growing allocates a fresh,
double-sized, zero-filled bucket array (the same arena-backed
`genArenaAllocElems` primitive `make`/`append` already share), walks every
still-`mapTagOccupied` bucket of the *old* array in a bounded runtime loop,
and re-probes each one's own key into the new array (reusing `genMapProbe`
itself for this - a freshly zeroed array with no duplicate keys inserted yet
always reports a miss on the very first probed slot, so no separate
"probe for an empty slot only" variant is needed at all) before finally
overwriting the control block's own `buckets`/`bucketCount` fields in place.
**The old bucket array is simply abandoned once rehashing finishes, never
freed** - consistent with this project's already-documented "the arena
never frees" design (see "The arena allocator" above), the identical
tradeoff `genAppendCall`'s own growth path already makes for a dynamic
array's abandoned pre-growth buffer.

**Hash function: a recursive, word-wise FNV-1a-*style* mixing combinator**
(`genMapHash`/`genHashInto`), not a literal byte-for-byte FNV-1a pass over a
key value's raw in-memory representation, despite that being the more
"obvious" literal reading of "hash however many bytes the key type's own
LLVM representation occupies, FNV-1a-style" - and this deviation is
deliberate, not a shortcut: this project's own struct/array *values* are
real LLVM aggregates built via `InsertValue` (see "Structs/arrays/strings
are passed and returned as real LLVM aggregate types" above), with no
guarantee that inter-field padding bytes are ever deterministically zeroed.
Hashing "every raw byte the type occupies" could hash two logically-identical
struct values (equal in every field) to two *different* results, purely
because of whatever garbage bits happened to sit in their own padding -
silently breaking the one property a hash table absolutely cannot survive
without (equal keys MUST hash equal). Recursing through a key's own logical
structure instead - each numeric field/element's own bit pattern
individually, a string's own real content bytes, a nested struct/array's own
fields/elements recursively - and mixing only *those* bits sidesteps the
padding hazard entirely, while remaining exactly the same kind of
"genuinely simple, well-known mixing function" FNV-1a itself is: each 32-bit
word is folded in via `seed = (seed XOR word) * 16777619`, seeded from FNV's
own standard 32-bit offset basis (`2166136261`). An `i64`/`f64`/pointer key
splits into two 32-bit halves and mixes each in turn; a `string` key's real
bytes are walked with an actual bounded runtime loop (`genHashStringInto`),
since a string's length isn't known until the program runs.

**Key equality: `genMapKeyEqual`, a dedicated, self-contained recursive
function - not a reuse of `genValueEqual`** (the existing whole-value
`==`/`!=` lowering, "Structs/arrays/strings..." section's own neighbor).
This is a real, separate function because a map key must support every
`Kind` `sema.typeIsComparableKeyType` accepts - every integer width, both
float widths, and a pointer - while `genValueEqual`'s own switch only
actually implements `TypeInt`(i32)/`TypeBool`/`TypeString`/`TypeStruct`/
`TypeArray`, panicking on anything else (i8/i16/i64/f32/f64/a pointer
field) - a real, pre-existing gap in that function, orthogonal to this
feature (flagged separately for a future fix, not patched here, since
widening `genValueEqual` itself is a wider change than this round's own
map-key-comparison need). `genMapKeyEqual` implements every comparable kind
directly instead: `ICmp` for every integer width/bool/pointer, `FCmp
FloatOEQ` for both float widths, `genStringEqual` for `string`, and the
same recursive field-by-field/element-by-element `And`-together shape
`genValueEqual` already established for `TypeStruct`/`TypeArray`.

**`m[k]` (read) and `m[k] = v` (write) never go through `genAddr`/`genLoad`'s
generic array-indexing path at all** - a real, deliberate divergence from
how a fixed-size/dynamic array element is addressed. An array index always
has a real address to hand back (or fail a bounds check trying); a map slot
might not exist yet, and "does this key exist" can only be answered by
actually running the probe - there's no way to produce "an address, maybe
for a slot that doesn't exist" the way `genAddr`'s uniform contract expects.
Concretely: `genExpr`'s own `IndexExpr` case (`isMapIndex`) diverts a
map-typed target straight to `genMapIndexRead`, which returns `V`'s own zero
value for a nil map or a genuinely missing key (Go's own "reading a missing
key returns the zero value" rule) with **no mutation at all** - critically
different from a write, which is a real get-or-insert-with-possible-growth
operation (`genMapWriteAddr`/`genMapGetOrInsertAddr`, reached from
`genAssignStmt`'s own map branch and `genMultiAssignStmt`'s per-target
loop). This is also exactly why `&m[k]` is illegal (see
`sema.isAddressableChain`'s own explicit map exclusion, LANGUAGE.md) - a
map index never has a stable address to begin with, unlike a fixed array
element's inline storage.

**`v, ok := m[k]`/`v, ok = m[k]`** (the two-result index expression - see
LANGUAGE.md's own precise distinction from a real multi-return call)
similarly never builds a `TypeMultiReturn`-shaped aggregate the way an
actual multi-return function call's result does: `genMultiShortVarDecl`/
`genMultiAssignStmt` each special-case a `MultiShortVarDecl`/
`MultiAssignStmt` whose sole value node is an `IndexExpr` up front, calling
`genMapIndexRead` directly and storing its two returned Go values (`value`,
`found`) into each target's own storage directly - no `ExtractValue` on an
aggregate at all, since there never was one to begin with. Every other
value shape (an actual multi-return `CallExpr`) still goes through the
existing aggregate-`ExtractValue` path, completely unchanged.

**Writing to a nil (never-`make`'d) map traps at runtime** (`genMapTrapIfNil`
- the same printf-then-`llvm.trap`+`unreachable` mechanism every other
runtime safety check in this package already uses - see "Runtime trap
diagnostics" below), mirroring Go's own real "assignment to entry in nil
map" panic exactly. **Reading a nil map is perfectly legal** and needs no
trap at all - `genMapIndexRead`/`genMapLenValue` both branch on a null
control-block pointer and return a zero value/`false`/`0` directly, matching
Go's own "reading a nil map is fine" rule.

```
$ llvmc.exe program.llx
runtime error: assignment to entry in nil map
```
(followed by the real process crash, unchanged - the exact same hard-abort
failure mode as every other trap in this package, not a softer recoverable
panic.)

## Runtime trap diagnostics

Every runtime safety trap this package emits - `genBoundsCheck`/
`genSliceRangeCheck` (`src/codegen/expr.go`) and `genMakeSizeCheck`
(`src/codegen/runtime.go`) - now prints a real, informative diagnostic to
stdout via a plain `printf` call (the exact same `g.printfType`/`g.printfFn`
extern `print`'s own codegen already uses - see "The `print` builtin,
concretely" below) *immediately before* the existing `llvm.trap` +
`unreachable` sequence, not instead of it: the abort mechanism itself is
completely unchanged, a genuine illegal-instruction process crash, not a
graceful `exit(1)` or any kind of recoverable panic - there is still no
exception-handling concept anywhere in this language (see "Array bounds
checking" above). This mirrors Go's own runtime-panic convention exactly (a
message, then a hard crash), chosen deliberately as the rough model rather
than inventing a softer recovery mechanism.

Each message is built from the exact same runtime `llvm.Value`s the check
itself already computed - no extra loads/computation needed, since the
trap block is reached with `idx`/`size` (`genBoundsCheck`), `low`/`high`/
`max` (`genSliceRangeCheck`), or `nVal`/`capVal` (`genMakeSizeCheck`)
already in hand as real SSA values:

- `genBoundsCheck`: `"runtime error: index %d out of range [0:%d)\n"`
  (`fmtBoundsTrap`) with `idx`/`size`.
- `genSliceRangeCheck`: `"runtime error: slice bounds out of range [%d:%d]
  with capacity %d\n"` (`fmtSliceRangeTrap`) with `low`/`high`/`max`.
- `genMakeSizeCheck`: `"runtime error: makeslice: len %d, cap %d out of
  range\n"` (`fmtMakeSizeTrap`) with `nVal`/`capVal` - covering all three of
  that check's own combined conditions (`n < 0`, `cap < 0`, `cap < n`) with
  one message, matching that function's own existing "one trap block for
  every violation, since the process is about to abort regardless of which
  one fired" reasoning.

Each format string is its own new cached global, built via `defineCString`
in `setupRuntime` exactly like every other format-string global there
(`fmtInt`/`fmtStr`/etc. - see "`print`'s printf format specifiers" above) -
no new mechanism, just three more entries in the same table.

```
$ llvmc.exe program.llx
runtime error: index 5 out of range [0:5)
```
(followed by the real process crash, unchanged - see `TestOutOfBoundsIndexTraps`,
`TestSliceRangeCheckTraps`, `TestMakeCapLessThanLenTraps`, and
`TestMakeNegativeSizeTraps`, all now additionally asserting the printed
message text via `exec.Command.CombinedOutput()`, on top of the abnormal-exit
assertion each already had.)

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
builds one parameterless function (`llvm_lang.global_init`) - the exact same
per-function generation state (entry block, fresh locals map, no enclosing
loop/receiver/lambda-capture context) `genFuncBody`/`genConstructorBody`/
`genLambdaFunc` each set up for a body of their own - and lowers every queued
initializer inside it via `storeValueInto` (`src/codegen/stmt.go`, the same
helper a local `var`/short-var-decl already uses to store its own
initializer), one plain `evaluate, then store into the global` per entry.
Unlike every other synthesized helper function this package builds for
itself (the arena allocator, lambda thunks - see `runtime.go`/`expr.go`),
this one keeps `AddFunction`'s own default linkage (external) rather than
private: `cmd/llvmc`'s JIT driver looks it up directly by this exact name
(see "JIT execution" below), which a private symbol has no name for at all.
This function is then registered into LLVM's own `@llvm.global_ctors`
mechanism - a standard, well-documented array of `{ i32, ptr, ptr }` entries
(`{ priority, ctor function pointer, associated data }`, appending linkage)
any real linked/loaded program's C runtime startup sequence scans and calls,
in priority order, before ever reaching `main`. A program whose every global
happens to be compile-time-constant gets no `llvm_lang.global_init` function
and no `@llvm.global_ctors` array at all - this mechanism leaves no trace in
the IR unless it's actually needed.

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
own. LLJIT (see "JIT execution: LLJIT" below) has no equivalent of the
legacy MCJIT `ExecutionEngine`'s `RunStaticConstructors()`, which this
project used for this before - instead, `jitRunMain` (and this package's own
test helpers, `compileAndJIT`/`compilePackageAndJIT`/`compileProgramAndJIT`)
looks up `llvm_lang.global_init` by name and calls it directly, exactly like
`main` itself, right after adding the module and before ever calling `main`.
The `@llvm.global_ctors` array itself is still built regardless - a real
linked/loaded program's C runtime would still need it - but the JIT path
never actually walks it; it goes straight to the one function it points at.
A module with no non-constant globals has no `llvm_lang.global_init`
function to find at all (see above), so a failed lookup here just means
there was nothing to run, not a real error. `-emit-llvm` needs no such
change: it never reaches `llvm.NewLLJIT` in the first place (see its own
section below), so the synthesized init function and `@llvm.global_ctors`
array simply show up in the printed IR text like any other generated code,
unexecuted - consistent with `-emit-llvm` never executing anything, before
or after this feature.

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

## Destructors

See `LANGUAGE.md`'s "Destructors" section for the language-level feature
(`destructor() { body }`, at most one per struct, the non-copyable rule and
its transitive propagation, and the two firing triggers). This section
covers how those two triggers - a plain local/parameter's own scope exit,
and `delete` - actually get lowered.

**Declaration/generation follow constructors' own two-pass split exactly**
(`declareDestructorSignature`/`genDestructorBody`, `src/codegen/func.go`): a
destructor lowers to its own real LLVM function, reusing the identical
implicit-first-pointer-parameter convention a method/constructor already
uses - the struct instance being destructed, addressed, never loaded -
always returns void, and is named `Struct.destructor` (no arity suffix at
all, unlike a constructor's `Struct.constructor.N` - a struct declares at
most one, so there's nothing to disambiguate). `Generator.dtors` is
`ctors`'/`funcs`' counterpart for destructors, but keyed directly by
`*sema.StructInfo` rather than by a destructor's own `*sema.Symbol`
(`sema.SymDestructor`): unlike a constructor, a destructor is never selected
by an arity lookup or any other call-site resolution at all - every real
caller of this map (a local/parameter's own declared type, or `delete`'s
pointee type) already holds the `StructInfo` it wants the destructor for
directly, so there's no reason to go through the `Symbol` indirection.

### The destructor stack (`Generator.destructors`)

A destructor call is never inserted "wherever a variable happens to go out
of lexical scope" the way, say, a C++ compiler's AST-scope-exit machinery
might reason about it - this package instead maintains one flat,
function-scoped stack, `Generator.destructors` (reset at the start of every
function/constructor/destructor/lambda body, exactly like `locals`/
`loopStack`), of every still-in-scope local/parameter whose own declared
type directly declares a destructor (**not** merely non-copyable via a
field - see `pushDestructorEntry`, `func.go`, which is the single gate every
one of these entries passes through: `genVarDecl`/`genShortVarDecl` call it
right after a local's storage is initialized, and
`genFuncBody`/`genConstructorBody`/`genLambdaFunc`'s own parameter loops
call it right after storing each incoming parameter).

`unwindDestructorsTo(target)` (`stmt.go`) is the one shared primitive every
real scope-exit trigger uses: it emits a real destructor call - via
`genDestructorCall`, the same implicit-`this`-pointer calling convention a
method call already uses - for every entry above `target`, **in reverse
index order** (LANGUAGE.md's "reverse declaration order" requirement falls
straight out of this, since entries are pushed in declaration order), then
truncates the stack down to `target`:

- **`genBlock`'s own fall-through case** (`base := len(g.destructors)`
  recorded at the block's own start) calls `unwindDestructorsTo(base)` right
  before returning `false` (didn't terminate) - exactly "this block's own
  directly-declared locals, nothing from an enclosing scope".
- **`genReturnStmt`** always unwinds to `0` - a return exits the whole
  function, so every entry on the stack, from every enclosing block, gets
  destructed - evaluating the returned value first (while everything is
  still valid), *then* unwinding, *then* actually emitting the `ret`.
- **`genBreakStmt`/`genContinueStmt`** unwind to the current loop's own
  `loopCtx.destructorBase` - `len(g.destructors)` captured at the moment
  that loop's own body started generating (so an enclosing scope's own
  entries, declared before the loop, are correctly left alone) - before
  branching to the break/continue target.
- **`genDeleteStmt`** calls the pointee's destructor directly (see below),
  not through this stack at all - `delete` frees a heap value with no
  local/parameter of its own necessarily involved.

`genBlock` itself, when a statement inside it *does* terminate (`genStmt`
returns `true`), deliberately does nothing further to `Generator.destructors`
- whichever nested `return`/`break`/`continue` actually caused that already
unwound everything relevant, quite possibly *below* this block's own `base`
(a `break` several blocks deep unwinds all the way back to the enclosing
loop's own boundary, not just the innermost block's) - so there is nothing
left for an enclosing frame to "finish".

### `genIfStmt`'s then/else save-restore - the one genuinely subtle part

`then`/`else` are **alternate, mutually exclusive continuations from the
same starting point**, not a sequential continuation of each other the way
two statements in one `Block` are - only one of them ever actually executes
at runtime, but codegen still has to *generate both*, sequentially, in the
same linear pass. A `return`/`break`/`continue` inside one branch legitimately
pops entries off `Generator.destructors` that the *other* branch's own
codegen must **not** see as already gone - if it did, the sibling branch's
own fall-through unwind would silently stop emitting a destructor call for a
local that, on its own runtime path, is very much still there.

`genIfStmt` (`stmt.go`) therefore snapshots a real copy of
`Generator.destructors` (`preIf`, not just its length - a branch's own
codegen may itself push fresh entries into the same backing array, which a
length-only restore could then read back incorrectly) before generating
`thenBB`, restores that copy immediately afterward (regardless of whether
`thenBB` terminated), generates `elseBB` from that identical restored state,
and restores the copy once more afterward - so whatever follows the `if`
(reached only when at least one branch doesn't terminate) always sees
exactly the pre-`if` state, never a bookkeeping side effect the other branch
happened to leave behind. This was caught directly by
`TestDestructorFiresOnFallThroughReturn`/`TestDestructorFiresOnBreak`
(`src/codegen/destructor_test.go`) failing before this fix existed - without
it, only whichever branch was generated *first* ever got a real destructor
call in the emitted IR at all, the second branch's own call having been
silently skipped because the stack already looked "empty" by the time its
own fall-through unwind ran.

### `delete p`'s destructor-then-free ordering

`genDeleteStmt` (`stmt.go`) checks `destructorInfoForPointee(operand)` - `p`'s
own pointer type's pointee, the identical "does this struct type declare its
own destructor" question `pushDestructorEntry` asks about a plain local's
declared type - and, if it does, calls `genDestructorCall(ptr, info)`
**before** the existing `free` call, not after: the destructor's own body
(e.g. reading/nulling a field of `this`, or itself `delete`-ing a further
pointer it owns) needs the pointee's memory to still be valid when it runs.
A pointee type with no destructor is entirely unaffected - exactly the plain
`free` this statement has always lowered to.

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

## Go-style multi-return values

See `LANGUAGE.md`'s "Functions" section for the language-level feature (a
function may declare a parenthesized `(T1, T2, ...)` return-type list;
`return a, b, ...` supplies them; the sole way to consume the result is
immediate destructuring via `a, b := f(...)`/`a, b = f(...)` - no first-class
tuple type anywhere). This needed **no new ABI mechanism at all** - it
reuses the "Structs/arrays/strings are passed and returned as real LLVM
aggregate types" convention immediately above verbatim, just for a value
that's never a *named* struct type.

**The `Type` representation.** `sema.TypeMultiReturn` reuses `Type`'s own
`Params []Type` field (the same field `TypeFunc` already uses for its own
parameter types) to hold the N component types - see `sema/types.go`'s own
doc comment. A multi-return function's `funcSignature.Return` is simply this
`Type` directly; nothing about `funcSignature`'s own shape changed at all.

**The LLVM type.** `llvmType`'s `TypeMultiReturn` case (`src/codegen/types.go`)
builds an anonymous LLVM struct type `{T1, T2, ...}` from the component
types - exactly the shape a real, named struct's own `llvmType` case already
builds from a `StructInfo`'s declared fields, just anonymous (there's no
`StructInfo` backing a multi-return type at all, only a bare `[]Type`) and
computed fresh every time rather than cached in `setupTypes` (unlike
`stringTy`/`dynArrTy`/`funcValTy`, which are each one fixed shape reused
everywhere, every multi-return function's own component types differ, so
there's nothing to precompute once up front).

**Declaring the function itself.** `declareFuncSignature`/`genFuncBody`
(`src/codegen/func.go`) needed no changes beyond what already existed: a
`FuncDecl`'s return-type node is looked up in `info.Types` exactly the same
way for every return-type shape (a plain type, `MultiReturnType`'s own
`ast.Node`, or none at all) - `sema.Check`'s own `multiReturnTypeFromNode`
already stored the right `Type` there, so `g.llvmType(retType)` just does the
right thing once `TypeMultiReturn` has its own `llvmType` case. The one new
piece of state is `funcCtx.retType` (`src/codegen/codegen.go`) - the
function currently being generated's own declared return type, needed by
`return`'s own multi-value lowering below (a `MultiValueExpr` node carries no
`Type` of its own to read out of `info.Types` the way an ordinary expression
would - the enclosing function's own declared return type is the only place
that information lives).

**`return a, b, ...`** (`genMultiValueExpr`, `src/codegen/stmt.go`): builds
the aggregate value via `llvm.Undef(retTy)` plus one `CreateInsertValue` per
returned expression, evaluated left to right - the same runtime-aggregate-
construction approach `genFuncLit`'s own closure value already uses for its
`{fnPtr, ctxPtr}` fat pointer whenever `ctxPtr` is a genuine runtime value
rather than a compile-time constant (a `ConstStruct` requires every field to
already be constant, which a `return`'s own computed values essentially never
are). `genReturnStmt` dispatches to this the same way it already dispatches
on every other node-kind-specific shape - a plain single-value `return expr`
is completely unchanged, still just `g.genExpr(valueNode)` directly.

**`a, b := f(...)`** (`genMultiShortVarDecl`, `stmt.go`): calls `f` exactly
once via the ordinary `genExpr` path (its callee's own real LLVM signature
already returns the matching anonymous struct - no special call-site lowering
needed at all, direct or indirect alike), then allocates each name's own
storage (`allocLocalSlot`, same captured-vs-stack decision every other local
already goes through) and fills it via `CreateExtractValue` against that one
aggregate result, one field index per name - exactly analogous to how an
ordinary struct's own fields are already extracted elsewhere in this package.

**`a, b = f(...)`** (`genMultiAssignStmt`, `stmt.go`): the assignment-form
counterpart - every target's own address is resolved via the same `genAddr`
a single-target `AssignStmt` already uses (working identically for a plain
variable, a struct field, or an array/slice element - `genAddr` has no notion
of "multi-target" to special-case at all), computed before the call itself
runs, then filled via `CreateExtractValue` the same way `genMultiShortVarDecl`
does.

**Component types genuinely differing in width/kind** (an `i64` alongside a
`bool`, an `f64` alongside a `string`) need no special handling anywhere in
any of this - `CreateInsertValue`/`CreateExtractValue` already operate on a
struct's field index, not a uniform element type the way an array's own GEP
does, so a mixed-shape aggregate was never actually a special case to guard
against; `TestMultiReturnMixedWidthTypes`/`TestMultiReturnFloatAndStringTypes`
(`src/codegen/multireturn_test.go`) exercise exactly this, JIT-executed.

See `examples/multireturn/multireturn.llx` for the worked dogfooding demo
(a `divide`/`find` pair mirroring Go's own `v, ok := m[k]` idiom), exercised
end to end - JIT and AOT alike - by `cmd/llvmc/main_test.go`'s
`TestBinary_MultiReturnExample`/`TestBinary_AOT_MultiReturn`.

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

## External functions (FFI): declare-only, zero JIT-side changes

See `LANGUAGE.md`'s "External functions (FFI)" section for the language-level
feature (`extern func Name(params) RetType` - a top-level declaration with no
body at all, binding a real external C symbol).

**A brand-new, deliberately separate AST node kind** (`ExternFuncDecl`,
`[name, paramList, returnType]`), not a nullable-body variant of `FuncDecl` -
see `DECISIONS.md`'s dated entry for the full reasoning (in short:
`FuncDecl`'s own body-always-present invariant is depended on unconditionally
by a large amount of existing code - `resolveFuncBody`, `checkFuncDecl`'s
return-flow analysis, `genFuncBody`'s whole lowering pass - so a nullable-body
`FuncDecl` would ripple a defensive nil-check through all of that; a separate
node kind keeps every one of them completely untouched).

**Sema deliberately reuses `sema.SymFunc`** as the declared symbol's kind
(`resolve.go`'s `declareExternFunc`), not a new `SymbolKind` - an extern-backed
function is indistinguishable from an ordinary one at every *call site*; the
only place anything ever needs to tell the two apart is by checking
`tree.Nodes[sym.Decl].Kind` directly (`funcSigForDecl`, `typecheck.go`, which
dispatches to `computeFuncSig` or `computeExternFuncSig` depending on which
node shape it finds - the one place the two genuinely different child layouts
matter at all).

**Codegen's lowering is, deliberately, almost nothing**:

- `declareExternFuncSignature` (`src/codegen/func.go`) mirrors
  `declareFuncSignature`'s param/return-type-to-LLVM-type translation, minus
  the receiver and `isMain` special-casing (neither ever applies - an extern
  func can never be a method, and `main` is always a real, bodied `FuncDecl`).
  It calls `llvm.AddFunction(g.mod, name, fnType)` with **default linkage**,
  not private - exactly like `printf`/`malloc`/`memcpy`/`memcmp` in
  `runtime.go` - since this name must resolve as a genuine external symbol,
  not one of this package's own internal helpers.
- It stores into the **exact same `Generator.funcs` map** `declareFuncSignature`
  does, keyed by the identical `*sema.Symbol` - every call-site
  (`genFuncCall`, `isDirectFuncCall`, `genFuncValue`/`genFuncThunk`,
  `src/codegen/expr.go`) needed **zero changes** to correctly treat a direct
  call to an extern function exactly like a direct call to an ordinary one:
  none of them ever branch on *how* a `funcEntry` got populated, only on
  whether one exists.
- **There is no corresponding "generate body" pass at all** - nothing calls
  `genFuncBody` (or anything like it) for an `ExternFuncDecl`, since it has no
  body, ever. `genPackage`'s own signature-declaration pass
  (`src/codegen/codegen.go`) walks every `ExternFuncDecl` in the whole program
  alongside every `FuncDecl`, but the later body-generation pass only ever
  walks `FuncDecl`s.

**Zero JIT/runtime-side changes were needed for this feature at all.**
`cmd/llvmc/main.go`'s `bindMinGWMainThunk` already registers
`llvm.NewDynamicLibrarySearchGeneratorForProcess(jit.GlobalPrefix())` on the
JIT's `MainJITDylib()` (predating this feature, for an unrelated reason - see
that function's own doc comment) - any symbol already loaded into the host
process (which includes every kernel32.dll export on Windows, and libc's own
exports via the mingw64 runtime already linked into this compiler's own
process) resolves automatically through this existing mechanism the moment an
extern func's `declare`-only LLVM function is looked up and called. An extern
func declared here but never actually present in the host process at
JIT-execution time fails at `Lookup`/call time with an ordinary "symbol not
found" error, not a compile-time one - the same class of failure a real
statically-linked program would get from its own linker instead, just moved
to run time because this project's execution model is JIT, not link-then-run.

Mirrors what its type-restriction diagnostic (`checkExternType`,
`sema/typecheck.go`) already stops before this: a `string`/struct-by-value/
dynamic-array/function-typed parameter or return type is a sema-layer error,
not a codegen-layer one - codegen never has to consider (or defend against)
any of those four unsupported shapes reaching an `ExternFuncDecl`'s
`llvmType` translation, since a tree with one already failed `sema.Check`
(see this package's own doc comment: `Generate` assumes fully valid input).

## The `args()` builtin, concretely

See `LANGUAGE.md`'s "The `args()` builtin" section for the language-level
feature (a predeclared, zero-argument builtin returning `[]string`, callable
from anywhere - see `sema.checkArgsCall`, `sema/typecheck.go`, dispatched
from `checkCallExpr` exactly like `make`/`append`/`len` already are).
`src/codegen/args.go` is this feature's own dedicated file, mirroring
`globalinit.go`'s "one feature, one file" precedent.

**Storage: one private, always-present global, populated once.**
`setupArgsGlobal` declares `llvm_lang.args` - a private, zero-initialized
`{ptr, i32, i32}` (`g.dynArrTy`) global - unconditionally, for every module,
regardless of whether the compiled program ever actually calls `args()`
anywhere: it's cheap and entirely self-contained (no external symbol
dependency at all), the same "always set up, never conditional on use"
convention every other cached global in `setupRuntime` already follows.
`genArgsCall` (the call's own codegen, dispatched from `genCallExpr` exactly
like `make`/`append`/`len`) is just a load of this global's current value -
no per-call marshaling work at all, matching the language's own "constructed
once, at program startup" promise. It also sets `Generator.argsUsed`, read
once by `genCtors` (see below) after every function body in the whole
program has been generated.

**Real argc/argv, without touching `main`'s own signature.** The obvious
design - give `main` a real `(argc, argv)` parameter pair, matching a
standard C entry point - was deliberately rejected: `main`'s LLVM signature
is looked up and called with **zero** arguments by both `cmd/llvmc`'s
`jitRunMain` and dozens of this package's own `jm.runInt32(t, "main")` test
call sites (`extern_test.go`, `globals_test.go`, `imports_test.go`,
`multifile_test.go`, `control_flow_test.go`, ... - a real, wide blast radius,
not a hypothetical one), every one of them a raw `syscall.SyscallN` call that
would suddenly need to pass two real, meaningful register arguments instead
of none. Changing what `main` itself takes was a real, unnecessary
regression risk this round explicitly avoided once a working alternative
existed. Instead, `buildArgsInitFn` reads two plain **extern globals**,
`__argc` (`i32`) and `__argv` (`ptr`) - the exact same well-established
MSVCRT/mingw64 C-runtime extension a real, hand-written C/C++ program on
this platform already relies on (`extern int __argc; extern char **__argv;`
from `<stdlib.h>`), populated by the CRT's own startup sequence before
`@llvm.global_ctors` or `main` itself ever run - so `main`'s own signature
and every existing call site (JIT or not) needed **zero** changes.

**Marshaling**: `buildArgsInitFn` builds a small, private, parameterless
function (`llvm_lang.args_init`) whose body: loads `__argc`/`__argv`, asks
the arena allocator (`genArenaAllocElems`, the same primitive
`make`/`append`/a slice composite literal already use) for a buffer of
`argc` `{ptr, i32}` string headers, then a real `CreateCondBr`/`AddBasicBlock`
runtime loop (the same shape `genPrintDynArrayValue`/`genForStmt` already
use) over `0..argc`: for each index, load `argv[i]` (a `char*`), call a
plain libc `strlen` extern (declared here, the same "declare a libc extern,
call it directly" convention `malloc`/`memcpy`/`memcmp`/`memset` already
use, rather than hand-rolling a `while (argv[i][j] != 0) j++` byte-scanning
loop as generated IR) to get its length, build the `{ptr, i32}` header, and
store it into the backing buffer at that index. The final `{buf, argc,
argc}` value is stored into `llvm_lang.args` once the loop completes.

**Registered into `@llvm.global_ctors`, and *only* when actually needed.**
`genCtors` (`globalinit.go`, renamed/refactored from the old
`genGlobalCtors` - see `DECISIONS.md`'s dated entry) now runs *after* every
function/constructor/destructor body has been generated, not before: it
needs to know whether `g.argsUsed` ended up true, which `genArgsCall` only
sets while generating some function's body, so the answer isn't known for
certain until every body has already been generated. `buildArgsInitFn` (and
its own `__argc`/`__argv`/`strlen` externs) is built - and registered into
`@llvm.global_ctors`, at a lower priority number than `llvm_lang.global_init`
so it runs *first* (a non-constant global's own initializer might itself
call `args()`) - **only when `g.argsUsed` is true**. A program that never
calls `args()` anywhere gets none of this: no `__argc`/`__argv` declared, no
`llvm_lang.args_init`, no `@llvm.global_ctors` entry for it - only the
always-present, fully self-contained `llvm_lang.args` global itself. This is
not just a minor optimization: `__argc`/`__argv` are real external symbols
this package has no control over the resolvability of under JIT execution
(unlike `malloc`/`printf`/`memcpy`, already proven resolvable by this
project's entire existing test suite) - keeping them out of every other
program's module entirely means the vast majority of existing/future
programs carry zero new external-symbol risk at all from this feature's
mere existence. See `TestArgsUnusedProgramHasNoArgsMachinery`/
`TestArgsUsedProgramHasArgsMachinery` (`src/codegen/args_test.go`) for this
asserted directly against the generated IR text.

**The JIT-execution fallback: an empty slice, by deliberate design, not an
oversight.** `cmd/llvmc`'s `jitRunMain` never looks up or calls
`llvm_lang.args_init` - unlike `llvm_lang.global_init`, which it explicitly
does look up and call. So under JIT execution, `llvm_lang.args` simply stays
at its zero-initialized value for the whole run: `args()` returns a real,
valid, but always-empty `[]string` (`len(args()) == 0`) every time a program
is JIT-executed via `llvmc program.llx`, regardless of any trailing
arguments typed after the path on the command line - `llvmc` does not
capture or forward trailing positional arguments at all this round (`run`,
`cmd/llvmc/main.go`, still requires exactly one positional argument - a
trailing `foo`/`bar` after the path is a usage error, not something forwarded
to the JIT'd program). See `DECISIONS.md`'s dated "args() builtin" entry for
why this specific fallback (over real trailing-arg-forwarding through the
raw-syscall JIT invocation mechanism) was chosen, and
`TestArgsCallUnderJITReturnsEmptySlice`/`TestBinary_AOT_Args`
(`src/codegen/args_test.go`/`cmd/llvmc/main_test.go`) for this contrasted
directly against a real AOT-compiled binary's own genuinely marshaled argv
(see "`-o`: AOT compilation to a native executable" below) - the same
program, same source, deliberately different (and clearly documented)
behavior depending on which of the two ways it's actually run.

**Why `bindMinGWMainThunk` also binds `__argc`/`__argv` under JIT, if
`args_init` is never called.** LLJIT's default per-module materialization
means merely looking up (and JIT-compiling) *any* symbol in a module that
happens to contain `llvm_lang.args_init` could, in principle, need every
symbol *that function* references to already resolve to something - even
though this driver deliberately never calls it. `cmd/llvmc/main.go`'s (and
`src/codegen/codegen_test.go`'s mirrored copy of) `bindMinGWMainThunk` binds
both to harmless, always-valid process-local memory via the identical
`AbsoluteSymbols` mechanism already used for the unrelated `__main` MinGW/GCC
ABI quirk (see "A MinGW/GCC ABI quirk" below) - removing any uncertainty
about this up front rather than relying on an assumption about exactly how
LLJIT partitions a module for compilation.

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

Since this path never reaches `llvm.NewLLJIT`, disposal is a plain
`Module.Dispose()` - same as the diagnostic/verification-failure paths below,
not the JIT path's own ownership-transfer teardown (see "A non-obvious
disposal detail" below).

## `-o`: AOT compilation to a native executable

```powershell
.\llvmc.exe -o myprogram.exe path\to\program.llx
.\myprogram.exe    # a real, standalone .exe - no llvmc, no Go, no LLVM
                    # toolchain present at all, anywhere in the loop
```

The single biggest gap this round closes (see `DECISIONS.md`'s dated entry
for the full "why now"): before this, `llvmc` could only JIT-execute a
program in its own process, or dump textual IR that nothing could actually
run - there was no way to hand someone a program compiled with this language
as a file they could just double-click or `./run`. `-o` runs the exact same
pipeline as every other mode (lex/parse/resolve/check/codegen/verify -
reusing `src/compiler`'s own `Result`, exactly like `-emit-llvm` and plain
JIT execution both already do) and, on success, produces a real `.exe` at the
given path instead.

**The tail, concretely** (`compileToExecutable`, `cmd/llvmc/main.go`):

1. **Emit a native object file** via LLVM's own target-machine backend
   (`third_party/go-llvm`'s `target.go` - `llvm.DefaultTargetTriple()`,
   `llvm.GetTargetFromTriple`, `Target.CreateTargetMachine`,
   `TargetMachine.EmitToMemoryBuffer(mod, llvm.ObjectFile)`) - zero vendored-
   binding changes were needed for this at all, full target-machine/object-
   emission support was already there, unused until now. The exact same
   native-target initialization the JIT path already performs
   (`InitializeNativeTarget`/`InitializeNativeAsmPrinter`, `initJIT`) turned
   out to already be sufficient - `LLVMInitializeNativeTarget`'s own C
   implementation (`llvm-c/Target.h`) already initializes TargetInfo+Target+
   TargetMC together for the host's native target, and
   `InitializeNativeAsmPrinter` the AsmPrinter needed to actually emit real
   machine code - confirmed concretely by this round's own AOT tests
   succeeding, not assumed.
2. **Write the resulting object bytes to a temporary `.o` file** via a plain
   `os.CreateTemp`/`os.Remove` - not this project's own `afero.Fs` convention
   (see `AGENTS.md`'s "Standards" section): that convention exists so
   `src/loader`'s own tests can fake a package's *input* file layout on
   `afero.NewMemMapFs()` instead of real temp directories; this is a
   single, ephemeral, write-only scratch file for a CLI-only link step, with
   no test needing to fake its contents, immediately removed once `gcc` has
   read it - a narrow, deliberate exception, not a quiet departure from that
   standing convention.
3. **Link it into a real `.exe`** by shelling out to `gcc <temp.o> -o
   <output>` (a plain `os/exec.Command` call) - reusing the exact same
   mingw64 toolchain this project already requires on `PATH` for cgo/dev work
   (see `AGENTS.md`'s "Compiling" section), rather than reimplementing or
   vendoring a linker of this project's own. `gcc` already resolves ordinary
   libc symbols (this package's own `printf`/`malloc`/`free`/`memcpy`/
   `memcmp`/`memset` externs - see `setupRuntime` above) and any
   user-declared `extern func` binding to a real Win32 API export
   (`kernel32.dll`, etc. - see LANGUAGE.md's "External functions (FFI)"
   section) automatically via mingw64's standard import libraries - **no
   special linking flags are needed for either case**, confirmed concretely
   (not assumed): `TestBinary_AOT_ExternFuncScopeTimer`
   (`cmd/llvmc/main_test.go`) AOT-compiles `examples/scope_timer` (which
   binds `QueryPerformanceCounter`/`QueryPerformanceFrequency` from
   `kernel32.dll`) and runs the resulting standalone `.exe` directly,
   proving link-time symbol resolution works - a genuinely different code
   path from the JIT's own runtime process-symbol generator, not just the
   same mechanism running earlier.

**`main`'s own LLVM signature needed no change at all** - a real, empirically
verified finding, not speculation (see `DECISIONS.md`'s dated entry for the
full reasoning this round worked through): `main` still lowers to the exact
same parameterless `i32 @main()` this project has always generated (see "
`main` is the real entry point" above), for both the JIT and the AOT path
alike. mingw64's own CRT startup calling `main()` with (internally tracked,
but never passed as real parameters to a zero-parameter callee) argc/argv/
envp is completely ordinary, valid C-ABI behavior - a callee simply
ignoring arguments a caller's calling convention happens to also carry costs
nothing and breaks nothing, confirmed directly by `TestBinary_AOT_HelloWorld`
et al. actually running correctly. Real argc/argv access for the `args()`
builtin instead goes through `__argc`/`__argv`, two separate mingw64-CRT-
populated globals - see "The `args()` builtin, concretely" above for why,
and for confirmation that this was verified end to end
(`TestBinary_AOT_Args`), not just assumed to work from the CRT's own
documented behavior.

**Mutually exclusive with `-emit-llvm`** - `run` (`cmd/llvmc/main.go`)
rejects both flags given together as a usage error (`exitUsage`) before ever
compiling anything, rather than silently letting one win.

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

- **2** - a usage error: no path argument, an unrecognized flag, both `-o`
  and `-emit-llvm` given together, the path couldn't be resolved to a real
  file/directory, its resolved directory has zero `.llx` files in it, an
  imported package directory couldn't be found, or a real import cycle was
  detected (see `src/loader`'s `Load`/`LoadProgram`). A short message goes to
  stderr; nothing is compiled.
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
  process always exits 0 afterward (see below). With `-o`, this also covers
  every failure mode specific to that path's own tail (`compileToExecutable`)
  - the target machine failing to resolve/emit, a temporary-object-file I/O
  error, or the `gcc` link step itself failing/returning non-zero (its own
  combined stdout+stderr output is included in the printed message) - see
  "`-o`: AOT compilation to a native executable" above.
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
  code - see the "`-emit-llvm` flag" section above. Nor does it apply with
  `-o`: `llvmc` itself never executes the compiled program at all with that
  flag - a successful AOT compilation always exits `llvmc` with code `0`
  regardless of what the *produced* `.exe`'s own `main` would return; that
  exit code only ever appears later, whenever someone actually runs the
  resulting standalone binary as its own, separate process.

## A non-obvious disposal detail

Once a `codegen.Module`'s `Ctx` and `LLVM` fields are wrapped into a
`ThreadSafeContext`/`ThreadSafeModule` and added to an LLJIT instance (see
`llvm.NewThreadSafeContextFromContext`/`NewThreadSafeModule`/
`LLJIT.AddLLVMIRModule`, `third_party/go-llvm/orcjit.go`), the LLJIT
instance takes ownership of both - calling `Module.Dispose()` afterward
would double-free them (this exact pitfall is already documented on
`src/codegen/codegen_test.go`'s `compileAndJIT` helper). So the two paths
that never reach a live LLJIT instance - a codegen diagnostic, or a failed
`llvm.VerifyModule` - already call `Module.Dispose()` themselves, inside
`src/compiler`'s `finishPipeline`, before ever handing a `Result` back (a
`Result.Module` is always nil on either path - see `src/compiler`'s own doc
comment - so `cmd/llvmc` never gets a live `*codegen.Module` for either of
these two cases at all, let
alone a chance to double-dispose one). Once JIT execution is about to happen
(a `Result.Module` came back non-nil), disposal instead goes through the
LLJIT instance alone (`jit.Dispose()`) - `cmd/llvmc`'s `jitRunMain` - which
tears down the module and context together, in the correct order, in one
call; unlike the legacy MCJIT `ExecutionEngine` (which only ever took
ownership of the module, leaving the context for the caller to dispose
separately as a second explicit step), LLJIT's ownership transfer already
covers both. The one remaining case `cmd/llvmc` itself still calls
`Module.Dispose()` directly is `-emit-llvm`'s own success path (`finish`) -
a verified module that's never handed to `llvm.NewLLJIT` at all.

## A MinGW/GCC ABI quirk: implicit `__main()` calls

A real, empirically-discovered platform gotcha hit while switching this
project's JIT engine from the legacy MCJIT `ExecutionEngine` to LLJIT (see
DECISIONS.md's dated "JIT execution: LLJIT" entry) - in the same spirit as
the `%lld` printf-specifier gotcha documented above, verified directly
rather than assumed: LLVM's backend, when compiling a function literally
named `main` for a `*-windows-gnu` target - this project's own mingw64 host,
and the exact target `JITTargetMachineBuilder`'s host-detection picks -
auto-inserts a call to `__main()` at that function's very start. This is the
same thing GCC's own frontend does for a real MinGW-linked program, there to
run static C++-style constructors via a much older, completely different
convention than this project's own `@llvm.global_ctors` mechanism (see
"Global `var` initializers" above). MCJIT never took this same code path -
whatever internal target selection it used apparently didn't trigger it,
only LLJIT's real host-detected `TargetMachine` does.

This project has no use for whatever `__main` would normally do, and never
defines it itself - without a real, resolvable `__main` symbol, materializing
`main` at all fails outright ("JIT session error: Symbols not found:
[ __main ]"), confirmed directly by capturing the compiled module's own IR
text before and after a JIT run and finding `__main` genuinely referenced
only in the *compiled* form, never the textual IR (see below). `cmd/llvmc`'s
`bindMinGWMainThunk` (mirrored exactly in `src/codegen`'s own test helpers)
works around this by binding `__main` directly to libc's own `rand` via
`AbsoluteSymbols`/`JITDylib.Define` (`third_party/go-llvm/orcjit.go`) -
`rand` is real, already resolvable via a process-symbol generator attached
to the main JITDylib, and safe to call with zero arguments and an ignored
result, exactly matching the shape of the auto-inserted call site. This is
unrelated to actually running `llvm_lang.global_init`: binding `__main` to
`global_init` directly wouldn't help any JIT'd function *other* than `main`
itself, and this package's own tests routinely call some other, arbitrarily
named function directly without ever going through `main` at all - so
`global_init` still runs through its own separate, explicit Lookup-and-call
(see "Global `var` initializers" above), unaffected by this.

**A second, related discovery made while diagnosing this:** LLJIT's compile
layer empties the original IR module out once it's been compiled to machine
code, unlike the legacy MCJIT `ExecutionEngine` (which kept the source
`Module` intact for its whole lifetime). Calling `Module.String()` again
*after* a JIT-executed call through that module returns just the bare
`; ModuleID = ...`/datalayout header - verified directly by capturing and
comparing the same module's IR text before and after a `runInt32` call, not
assumed - rather than the real generated IR, and this was observed to crash
outright in at least one case, not just return the wrong thing. Since the
IR text itself never changes after codegen (JIT compilation reads it to
produce machine code, it doesn't rewrite the source module), `src/codegen`'s
own `jitModule` test helper now captures a module's IR text once, immediately
after codegen and before it's ever handed to an LLJIT instance
(`jitModule.ir`), and every test wanting to assert on generated IR text uses
that instead of calling `jm.mod.LLVM.String()` itself.
