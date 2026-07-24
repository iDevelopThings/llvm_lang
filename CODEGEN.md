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

`int` lowers to `i32`, not `i64`: `main`'s real LLVM signature must return
`i32` (the OS process exit code), so with `int == i32` a source-level `func
main() int { return code }` needs no truncation/cast at all - the
language's own `int` and the platform C ABI's `int` are simply the same
type. See `DECISIONS.md`.

## Numeric type -> LLVM type, and where the type resolution itself lives

`sema.Type`'s concrete numeric kinds map straight onto go-llvm's own
integer/float constructors (`llvmType`, `src/codegen/types.go`): `i8`/`i16`/
`i32`/`i64` -> `Int8Type`/`Int16Type`/`Int32Type`/`Int64Type`; `f32`/`f64` ->
`FloatType`/`DoubleType`. The unsigned widths `u8`/`u16`/`u32`/`u64` lower to
the *same* LLVM `iN` types as their signed counterparts - LLVM integers carry
no inherent signedness. An LLVM integer/float instruction is already generic
over bit width as long as both operands share the same LLVM type (sema
guarantees this by construction), so no per-width branching is needed inside
`genBinaryExpr`/`genUnaryExpr`/etc.; the *kind* (integer vs. float) is checked
to pick the matching floating-point instruction, and for integers the
sema-level signedness (`Type.IsUnsigned`) picks unsigned over signed only
where LLVM actually differentiates: division/remainder (`udiv`/`urem` vs
`sdiv`/`srem`), ordered comparison (`ULT/ULE/UGT/UGE` vs the `S` forms),
widening (`zext` vs `sext`), int<->float (`uitofp`/`fptoui`), and print
formatting. Add/sub/mul/bitwise/equality are signedness-agnostic in LLVM and
need no branch.

There is **no codegen-side type-position resolution left at all**. A prior
version of this package had its own `resolveTypeNode`/`varDeclType`
functions re-deriving a type-position node's `sema.Type` from scratch,
duplicating `sema/typecheck.go`'s own `computeTypeFromNode`/`declType` logic
- entirely because `sema.Info.Types` didn't yet cover type-position nodes.
That gap is closed: `sema.Check` now stores every type-position node's
resolved `Type` into `Info.Types` too, so every codegen call site that used
to call `resolveTypeNode(n)`/`varDeclType(decl)` is now just a plain
`g.info.Types[n]` map lookup. This was a deliberate architectural fix - two
independent implementations of "what type does this node have" is exactly
the kind of duplication that silently drifts out of sync (this round almost
re-created it for the six new numeric types, before switching directly to
the `Info.Types` fix instead). See `AGENTS.md`'s `## Architecture` section.

## Explicit conversions, concretely

`T(x)` is recognized in codegen (`isConversionCall`, `src/codegen/expr.go`)
exactly the way sema determined it - a `CallExpr`'s callee `Ident` resolving
(via `Info.Refs`) to `sema.SymBuiltinType`, not a function - and lowered
(`genConversion`) using the correct LLVM instruction for the source/target
type pair: widening int via `CreateSExt` for a signed source or `CreateZExt`
for an unsigned one, `CreateTrunc` (narrowing int, bit-pattern-only so
signedness-agnostic), `CreateSIToFP`/`CreateFPToSI` (or the `UIToFP`/`FPToUI`
variants when the unsigned end is the integer one), `CreateFPExt`/
`CreateFPTrunc` (widening/narrowing float). A same-`Kind` "conversion" (e.g.
`i32(someI32Value)`) just returns the value unchanged - no pointless
bitcast/identity instruction emitted.

## `print`'s printf format specifiers - a real Windows/mingw64 platform gotcha

Every numeric width needs its own `printf` format specifier (`genPrintCall`/
`genPrintValueBare`, `src/codegen/runtime.go`): `i8`/`i16` are sign-extended
to `i32` first and reuse `"%d"` - a manually-built variadic `printf` call
doesn't get C's own default-argument-promotion for free, so this package
does it explicitly, same reasoning as `bool`. `f32` is extended to `f64`
first and reuses `"%f"` (variadic C calls always promote `float` to
`double`). `f64` uses `"%f"` directly. The unsigned widths need their own
specifiers: `u8`/`u16`/`u32` zero-extend to `i32` and use `"%u"`, and `u64`
uses `"%llu"` (the `%lld`-family sibling verified below) - printing an
unsigned value through the signed `"%d"`/`"%lld"` would render a value above
the signed max as negative.

`i64` needed to be **verified empirically, not assumed**: this project's
toolchain is mingw64/MSYS2, and `%lld`/`%d`-family format specifier behavior
for 64-bit integers can genuinely differ between an MSVCRT-style `printf`
(whose historic implementation wants the MS-specific `%I64d`, not `%lld`)
and mingw-w64's own "ANSI stdio" `printf` wrapper (`__USE_MINGW_ANSI_STDIO`,
on by default for x86_64 mingw-w64 builds), which does support `%lld`
correctly, C99-style. Tested directly via `src/codegen/stdoutcapture`, which
redirects the real C-runtime stdout (via `freopen`, from a small non-test
cgo file - `go test` rejects `import "C"` inside `_test.go` files, hence the
separate package) so a test can capture exactly what a JIT-executed `print`
call writes to real stdout, byte for byte.
`TestPrintI64FormatSpecifierIsCorrect`/
`TestPrintNegativeI64FormatSpecifierIsCorrect`
(`src/codegen/stdout_capture_test.go`) print a value that doesn't fit in 32
bits and a negative i64 value, asserting the captured bytes are exact.
**Result: `%lld` is correct on this toolchain.** If this project's toolchain
ever moves off mingw64/MSYS2, this is the first thing to re-verify - the
wrong specifier wouldn't crash, it would silently print garbled digits.

## `string` representation

`string` is the literal (unnamed) LLVM struct `{ ptr, i32 }` - a data
pointer plus a length, **not** a null-terminated C string. Every consumer
(`print`, `+`/`+=` concatenation, `==`/`!=`, `< <= > >=`) goes through the
length field, never `strlen`. Concatenation (`genStringConcat`,
`src/codegen/runtime.go`) asks the arena allocator for a buffer sized to fit
both operands and `memcpy`s each in. Equality (`genStringEqual`)
short-circuits on length mismatch before ever calling `memcmp`. Ordering
(`genStringOrder`/`genStringCompareSign`) is a real byte-by-byte
lexicographic comparison, same as Go: `memcmp` over the shorter operand's
length decides it whenever the two differ within that shared prefix, and the
lengths break the tie when one string is a prefix of the other (`"ab" <
"abc"`, matching Go).

`cstring` (see LANGUAGE.md's "The `cstring` type" section) is a completely
different, much simpler representation: just `g.ptrTy`, the same opaque
`ptr` every pointer type already uses (`llvmType`, types.go) - no length
field, since it exists purely to match C's own `char*` at the ABI level.
Only reachable via `genStringToCString`/`genCStringToString` (runtime.go),
`genConversion`'s (expr.go) lowering for the two explicit `cstring(s)`/
`string(cs)` conversions - never a first-class value with its own operators.

## The arena allocator (`src/codegen/runtime.go`'s `setupArena`)

Every codegen-level heap allocation goes through one centralized bump
allocator instead of calling libc `malloc` directly at each call site -
currently just string concatenation (`genStringConcat`), but any future
heap-needing feature should route through this same primitive rather than
reintroducing scattered `malloc` calls. See `DECISIONS.md` for why this
shape was chosen over the alternatives.

Design: **one process-lifetime arena, growing in malloc'd chunks that
themselves grow geometrically.** It's a real generated LLVM function
(`llvm_lang.arena_alloc`, internal-linkage, built directly at `Generate`
time - not a libc call), backed by three mutable globals: `.arena.cursor`
(the next free byte in the current block), `.arena.remaining` (bytes left in
it), and `.arena.next_chunk_size` (how big the *next ordinary* growth chunk
should be).

The *first* chunk is always `arenaChunkSize` (64KiB). Every *ordinary*
(non-oversized) growth event after that doubles `.arena.next_chunk_size` for
next time, capped at `arenaChunkMaxSize` (64MiB - see that constant's own
doc comment in `runtime.go` for the empirical benchmark data behind this
number): 64KiB, 128KiB, 256KiB, ... up to 64MiB, then steady. This mirrors
this project's own dynamic-array `append` doubling strategy - small programs
still start cheap, but a program under sustained allocation pressure gets
progressively bigger chunks instead of paying for thousands of tiny `malloc`
calls (see `DECISIONS.md`'s dated entry on the investigation that motivated
this).

Allocating `size` bytes: if the current block doesn't have `size` bytes
left, `malloc` a fresh block first - sized to the *current*
`.arena.next_chunk_size` for an ordinary request, or exactly `size` for a
single request bigger than that (an oversized one-off, unrelated to the
tracked chunk-size progression) - and point the arena at it; whatever was
left in the abandoned block is simply never reused. Only the ordinary path
advances `.arena.next_chunk_size` afterward, so one unusually large
allocation can't permanently balloon every later *ordinary* chunk. Either
way, the cursor is bumped forward by `size` and the pre-bump address handed
back.

This remains a real, intentional memory leak overall - **no per-allocation
free, no GC, no refcounting** - just centralized behind one primitive
instead of ad hoc `malloc` calls. See `BLOCKERS.md`: a real
memory-management strategy is still an open, deliberately-deferred question
for the user to decide for this arena specifically.

`new`/`delete` (see "Pointers" below) are a deliberate, separate exception,
not a change to the arena itself: each `new` is its own individually
`malloc`'d block on a completely different heap, freed one at a time via a
real `delete`/`free` - the arena's own allocations remain exactly as leaky
as before.

## Dynamic arrays (`[]T`)

See `LANGUAGE.md`'s "Dynamic arrays" section for the language-level feature.
This is the concrete "future heap-needing feature" the arena allocator was
always meant to eventually support.

**Representation.** `sema.Type{Kind: TypeArray, Dynamic: true}` maps to the
literal (unnamed) LLVM struct `{ ptr, i32, i32 }` = `{ dataPtr, len, cap }`
(`g.dynArrTy`, `setupTypes`) - the exact same "pointer + metadata" convention
`string`'s `{ ptr, i32 }` already uses, just with a third field. `len`/`cap`
are `i32`, matching this language's own `int`. One shared LLVM struct type
serves *every* element type `T`: `g.ptrTy` is already an opaque `ptr`
regardless of what it points to, so a dynamic array's own element type only
ever matters at the point some code computes an address into the backing
buffer via a real, explicitly-typed GEP (`genMakeCall`/`genAppendCall`/
`genDynArrayLitInto`, `genAddr`'s `IndexExpr` case) - never in the struct's
own shape.

**`make([]T, n)` / `make([]T, n, cap)`** (`genMakeCall`, `runtime.go`):
allocates a fresh buffer sized for `cap` elements (`n`, when `cap` is
omitted) via the arena allocator (`genArenaAllocElems`, a thin wrapper
around `genArenaAlloc` that also returns the element's `llvm.SizeOf` - the
null-pointer-GEP constant trick, resolved by LLVM itself), `memset`s the
whole allocated region to zero (avoids ever reading uninitialized arena
memory through an element between `len` and `cap` a later `append` hasn't
written yet), and returns the resulting `{ ptr, n, cap }` value. `n`/`cap`
are ordinary runtime `llvm.Value`s, not compile-time constants - so `cap <
n` (when `cap` is given) is checked with a real runtime trap
(`genMakeCapCheck`), the same `llvm.trap`+`unreachable` mechanism
`genBoundsCheck` uses: there's no way to reject a bad runtime relationship
at compile time, so this is a hard process abort, not a `diag.Bag` entry.

**`append(slice, elem)`** (`genAppendCall`, `runtime.go`): a real
`CreateCondBr`/multi-basic-block lowering - `len < cap` branches to a "fit"
block (nothing to allocate) or a "grow" block (`newcap = max(1, cap*2)`,
built via a `select` on `cap*2 < 1` - the "cap==0" edge case landing on `1` -
then a fresh arena allocation, plus a `memcpy` of the existing `len`
elements) - and a join block reads both paths' final pointer/capacity back
via `PHI` nodes before writing the new element at index `len` and returning
`{ finalPtr, len+1, finalCap }`. The "fit" path's `PHI` incoming value is the
*original* pointer, so a caller still holding an older copy of the
pre-append slice genuinely observes the same backing memory being mutated -
matching Go's own well-defined semantics.

**`len(x)`** (`genLenCall`, `runtime.go`): a dynamic array reads its runtime
`len` field (`ExtractValue` index 1, same as a string); a fixed-size array
returns a plain `ConstInt` from its already-known `sema.Type.Size`.

**Indexing** (`genAddr`'s `IndexExpr` case, `expr.go`): a dynamic array's
element address is computed straight from its own `{ ptr, len, cap }`
value's `ptr`/`len` fields - unlike a fixed-size array, no need for the
*slice variable's own* address at all, since backing storage lives
separately on the arena heap; works identically for both a read and a
write. The bounds check itself is `genBoundsCheck`, generalized to take
`size` as an arbitrary computed `llvm.Value` rather than only a compile-time
constant (see "Array bounds checking" below).

**Slice composite literals** (`[]T{1, 2, 3}`, `genDynArrayLitInto`,
`expr.go`): the element count is always known at codegen time (unlike
`make`'s runtime `n`/`cap`), so this allocates a buffer of exactly that
size via `genArenaAllocElems`, fills it positionally, then stores the
resulting `{ ptr, count, count }` fields directly into the destination via
three `CreateStructGEP`s, the same field-by-field fill a struct literal's
destination already uses.

**Printing** (`genPrintDynArrayValue`, `runtime.go`): renders the same `[e0
e1 ...]` shape a fixed-size array does (`genPrintArrayValue`), but as a real
runtime loop over the slice's `len` field, rather than a static unrolled
sequence of `printf` calls - a dynamic array's element count isn't known
until the program runs, so there's no way to unroll it ahead of time.
`genPrintArrayValue` branches on `t.Dynamic` up front to pick one or the
other.

## Array bounds checking

Indexing a fixed-size array - both a read (`a[i]`) and a store (`a[i] = v`)
- lowers to a real runtime check, not a bare GEP: `i < 0 || i >= size`
(`size` always known at compile time) traps immediately via LLVM's
`llvm.trap` intrinsic followed by `unreachable`. See `genBoundsCheck`,
`src/codegen/expr.go` - the same `CreateCondBr`/basic-block shape `if`/`for`
already use. `genBoundsCheck` takes `size` as an arbitrary already-computed
`llvm.Value`, not only a compile-time constant - a dynamic array's index
passes its slice's actual runtime `len` field through the identical check.

```go
a := [5]int{1, 2, 3, 4, 5}
a[2]      // fine
a[5]      // traps - index == size
a[-1]     // traps - negative index
```

This is a hard process abort - there's no exception handling or
panic/recover mechanism in this language, so an out-of-range index is
unrecoverable by design. See `TestOutOfBoundsIndexTraps`
(`src/codegen/bounds_test.go`): JIT-executing a genuinely out-of-range index
would crash the `go test` process itself, so that test re-execs the test
binary as a child process and asserts the *child* exits abnormally - the
same `GO_WANT_HELPER_PROCESS` pattern `os/exec`'s own test suite uses.

## Slicing

See `LANGUAGE.md`'s "Slicing" section for the language-level feature (a Go-
style slice expression producing a fresh header value that shares its
operand's backing memory - no copy) and `ast.Node`'s own `SliceExpr` doc
comment for the `[object, low, high]` grammar shape. Recognized in codegen
(`genSliceExpr`, `src/codegen/expr.go`) the same way sema's own
`checkSliceExpr` dispatches - on the operand's resolved `sema.Type` - to one
of three lowering paths, each building a fresh `{ptr, len, cap}` (dynamic
array) or `{ptr, len}` (string) value with no allocation and no copy:

- **A dynamic array operand** (`genDynArraySlice`): `ptr = GEP(s.ptr, low *
  elemSize)`, `len = high - low`, `cap = s.cap - low` - reusing the exact
  same construction `genMakeCall`/`genAppendCall` already build, just
  derived from an existing slice's own fields.
- **A string operand** (`genStringSlice`): `ptr = GEP(s.ptr, low)`, `len =
  high - low`; strings are immutable, so this sharing is read-only in
  practice.
- **A fixed-size array operand** (`genFixedArraySlice`): takes the array's
  own address (`genAddr`, the same helper `&`/a method receiver already use
  - exactly why sema's `checkArraySliceAddressable` requires the operand to
  be addressable), then the same `{ptr, len, cap}` construction, with `cap =
  N - low` (`N` the array's compile-time-known `Size`).

**The bounds check generalizes from a single index to a range.**
`genBoundsCheck` checks one `0 <= idx < size` condition; a slice expression
needs a genuine *range* check instead - `genSliceRangeCheck` checks `0 <=
low`, `low <= high`, and `high <= max` all at once, trapping via the
identical mechanism on any violation. `max` is an arbitrary already-computed
i32 `llvm.Value`: a dynamic array's caller passes its runtime `cap` field
(not `len` - see `LANGUAGE.md`'s "Slicing" section for why a reslice's upper
bound is checked against capacity, not length), a string's caller passes its
runtime `len`, and a fixed-size array's caller passes a `ConstInt` built
from its compile-time `Size`.

`genSliceBounds` is the shared "resolve omitted defaults, then range-check"
helper all three paths call: an omitted low defaults to `ConstInt` `0`; an
omitted high defaults to whatever `defaultHigh` the caller passed (the
operand's runtime `len` for a dynamic array/string, or `ConstInt` `N` for a
fixed array) - notably *not* always the same value as the range check's own
`max` (a dynamic array passes `len` as `defaultHigh` but `cap` as `max` -
the one place these two genuinely differ, per `LANGUAGE.md`'s own
called-out rule).

Once a slice value is correctly constructed, `len(...)`/`append(...)` on it
just work unchanged - a sliced dynamic array/string is an ordinary value of
the same type afterward (`TestSliceLenAndAppendOnSlicedValue`,
`src/codegen/slice_test.go`).

```go
s := []int{10, 20, 30, 40, 50}
mid := s[1:4]      // {ptr = GEP(s.ptr, 1), len = 3, cap = s.cap - 1}
mid[0] = 99        // writes through the same backing buffer s.ptr points at
```

See `TestSliceDynamicArrayAliasing`/`TestSliceFixedArrayAliasing`
(`src/codegen/slice_test.go`) for the aliasing proof this representation is
built for, and `TestSliceRangeCheckTraps` (same file) for the range-check's
own trap behavior, using the same re-exec-as-a-child-process pattern
`TestOutOfBoundsIndexTraps` established.

## Maps

See `LANGUAGE.md`'s "Maps" section for the language-level feature. All of
this lives in its own file, `src/codegen/maps.go` - a genuinely new runtime
mechanism, unlike dynamic arrays (which mostly reuse the arena allocator's
existing "grow a {ptr,len,cap} buffer" shape).

**Representation.** A map value is a single opaque `ptr` (`g.ptrTy` -
`sema.Type{Kind: TypeMap}`'s `llvmType` case treats it exactly like
`TypePointer`), pointing at a small, arena-allocated **control block**
(`g.mapCtrlTy`, one shared LLVM struct type for every map instantiation,
mirroring `dynArrTy`'s own reasoning):

```
{ ptr buckets, i32 count, i32 bucketCount }
```

The control block's own address never changes across the map's lifetime -
only `buckets`/`bucketCount` change in place when the table grows
(`genMapGrowIfNeeded`). This is what makes assigning one map-typed variable
to another share the same live table (`LANGUAGE.md`'s "maps are a reference
type" rule): copying the map value just copies this one pointer.

**Bucket layout.** Each bucket is `{ i8 tag, K key, V value }` - built fresh
per call site via `g.mapBucketType(keyT, valT)`, not cached per-(K,V) pair:
LLVM's own context already structurally interns two identical unnamed
struct types, so a cache would only save a little bookkeeping, not a real
allocation. `tag` is one of three sentinel byte values (`mapTagEmpty` = 0,
`mapTagOccupied` = 1, `mapTagTombstone` = 2) - zero-filling a freshly
allocated bucket array (`memset`) is what makes every bucket start
`mapTagEmpty` for free.

**Collision resolution: open addressing with linear probing and
tombstones**, not separate chaining - no extra pointer indirection per
entry: `genMapProbe` (`maps.go`) starts at `hash(key) mod bucketCount` and
walks forward one bucket at a time (wrapping around), for at most
`bucketCount` steps (a bound the growth policy below always guarantees is
never actually reached), stopping the instant it finds either a matching
occupied key (a hit) or a genuine `mapTagEmpty` slot (a definitive miss - an
empty slot means the key can't possibly appear further along the probe
chain). A `mapTagTombstone` slot never stops the probe on its own - a live
key further along the same original chain must still be reachable past it -
but the *first* available slot passed along the way is remembered as the
eventual insertion point, so a fresh insert naturally reuses an earlier
tombstone.

**`remove(m, k)`** marks the matching bucket `mapTagTombstone` (not
`mapTagEmpty` - `genMapRemoveCall`) rather than clearing it outright, for
the probe-chain reason above, and decrements `count`. A no-op against a nil
map or an absent key, matching Go's own `delete(m, k)` exactly.

**Growth: doubling, triggered at a 0.75 load factor** (`genMapGrowIfNeeded`):
checked right before any insert that isn't just overwriting an existing
key's value, growing whenever `(count+1)*4 > bucketCount*3` (computed in
`i64` to stay safe against `i32` overflow for a very large map). Growing
allocates a fresh, double-sized, zero-filled bucket array, walks every
still-`mapTagOccupied` bucket of the *old* array, and re-probes each one's
key into the new array (reusing `genMapProbe` itself - a freshly zeroed
array with no duplicate keys always misses on the first probed slot) before
overwriting the control block's `buckets`/`bucketCount` fields in place.
**The old bucket array is simply abandoned, never freed** - the same
tradeoff `genAppendCall`'s own growth path already makes.

**Hash function: a recursive, word-wise FNV-1a-*style* mixing combinator**
(`genMapHash`/`genHashInto`), not a literal byte-for-byte FNV-1a pass over a
key's raw memory. This project's own struct/array *values* are real LLVM
aggregates built via `InsertValue`, with no guarantee inter-field padding
bytes are ever deterministically zeroed - hashing raw bytes could hash two
logically-identical struct values to two different results, silently
breaking the one property a hash table can't survive without (equal keys
MUST hash equal). Recursing through a key's own logical structure instead -
each numeric field/element's own bit pattern, a string's own content bytes,
a nested struct/array's fields/elements recursively - and mixing only those
bits sidesteps the padding hazard while remaining exactly as simple a
mixing function as literal FNV-1a: each 32-bit word is folded via `seed =
(seed XOR word) * 16777619`, seeded from FNV's own 32-bit offset basis
(`2166136261`). An `i64`/`f64`/pointer key splits into two 32-bit halves; a
`string` key's real bytes are walked with a bounded runtime loop
(`genHashStringInto`), since a string's length isn't known until runtime.

**Key equality: `genMapKeyEqual`, a dedicated, self-contained recursive
function - not a reuse of `genValueEqual`** (the existing whole-value `==`/
`!=` lowering). When this map feature landed, `genValueEqual`'s own switch
only implemented `TypeInt`(i32)/`TypeBool`/`TypeString`/`TypeStruct`/
`TypeArray`, panicking on anything else - a real, pre-existing gap, flagged
at the time rather than patched inline. **That gap has since been fixed
directly** (`genValueEqual` now implements every `Kind` `genMapKeyEqual`
does - `ICmp` for every integer width/bool/pointer, `FCmp FloatOEQ` for both
float widths; see `src/codegen/expr.go`'s own doc comment for why
`FloatOEQ` alone, never a separate `FloatUNE` case, is correct even for the
enclosing `!=` operator - De Morgan's law over the recursive per-field `And`
already produces the right semantics from a single top-level `Not`), so the
two functions' switches are now equivalent in coverage - `genMapKeyEqual`
remains its own separate function regardless, since it exists for a
genuinely different reason (map-key hash-table lookup), not because of any
remaining capability gap. Both still independently implement the same
recursive field-by-field `And`-together shape for `TypeStruct`/`TypeArray`.

**`m[k]` (read) and `m[k] = v` (write) never go through `genAddr`/`genLoad`'s
generic array-indexing path at all** - a real, deliberate divergence: an
array index always has a real address to hand back (or fail a bounds check
trying); a map slot might not exist yet, and "does this key exist" can only
be answered by actually running the probe. `genExpr`'s own `IndexExpr` case
(`isMapIndex`) diverts a map-typed target straight to `genMapIndexRead`,
which returns `V`'s zero value for a nil map or a missing key with **no
mutation at all** - critically different from a write, which is a real
get-or-insert-with-possible-growth operation
(`genMapWriteAddr`/`genMapGetOrInsertAddr`). This is also exactly why `&m[k]`
is illegal (`sema.isAddressableChain`'s explicit map exclusion) - a map
index never has a stable address to begin with.

**`v, ok := m[k]`/`v, ok = m[k]`** (the two-result index expression - see
`LANGUAGE.md`'s precise distinction from a real multi-return call) never
builds a `TypeMultiReturn`-shaped aggregate: `genMultiShortVarDecl`/
`genMultiAssignStmt` each special-case an `IndexExpr` sole value node,
calling `genMapIndexRead` directly and storing its two Go return values
(`value`, `found`) into each target's storage directly - no `ExtractValue`
on an aggregate, since there never was one.

**Writing to a nil (never-`make`'d) map traps at runtime** (`genMapTrapIfNil`
- the same printf-then-`llvm.trap`+`unreachable` mechanism every other
runtime safety check uses), mirroring Go's own "assignment to entry in nil
map" panic exactly. **Reading a nil map is perfectly legal** - both
`genMapIndexRead`/`genMapLenValue` branch on a null control-block pointer and
return a zero value/`false`/`0` directly, matching Go's own "reading a nil
map is fine" rule.

```
$ llvmc.exe program.llx
runtime error: assignment to entry in nil map
```
(followed by the real process crash, unchanged.)

## Range loops

See `LANGUAGE.md`'s "Range loops" section for the language-level feature.
`genRangeForStmt` (`src/codegen/stmt.go`) dispatches on the subject's
resolved type to one of two genuinely different lowering strategies -
neither reuses a general iterator protocol; both are hardcoded loops built
directly out of each type's own existing runtime representation, matching
this feature's "hardcoded for performance" scope (see `DECISIONS.md`).

**Array/slice subject (`genRangeForArray`, `stmt.go`): an ordinary indexed
loop, `0..len-1`.** The subject is evaluated once before the loop starts
(its `{ptr, len, cap}` value for a dynamic array, or its own address for a
fixed-size array - reusing the same GEP shapes `genAddr`'s `IndexExpr` case
already uses, just without a per-iteration bounds check: the loop's own
index is provably in range by construction). `key` (when present) binds the
index directly; `value` (when present) is loaded from the computed element
address each iteration.

**Map subject (`genRangeForMap`, `maps.go`): a linear walk over the map's
own bucket array**, skipping any slot that isn't `mapTagOccupied` - no
probing needed. A nil (never-`make`'d) map is handled by phi-ing
`bucketCount` down to 0 rather than a separate zero-trip-count branch, so
the loop needs no nil-awareness of its own. `key`/`value` (when present) are
loaded directly out of the current live bucket's own fields.

**Loop control and destructor discipline**: both lowerings push the
identical `loopCtx` `genForStmt` already uses for break/continue
(`breakTarget`/`continueTarget`/`destructorBase`). The one genuinely new
wrinkle is *where* `destructorBase` is captured: right before `key`/`value`
are bound each iteration, so a `break`/`continue`'s own unwind correctly
destructs that iteration's own bindings together with whatever the body
declared - and a normal fall-through explicitly unwinds down to that same
point before looping, since `genBlock`'s own fall-through unwind only ever
reaches back to its own base.

**A fresh key/value binding needs no `genForStmt`-style per-iteration-capture
workaround.** `genForStmt`'s C-style `init` clause needs one specifically
because `init` is generated once, in the loop's *preheader*, before the body
even exists - so its own storage is naturally shared across every iteration
even when arena-allocated. A range-for's `key`/`value` bindings, by
contrast, are generated directly inside the loop's own body block
(`bindRangeVar`), which - though only *generated* once at compile time -
*executes* fresh every dynamic iteration. `allocLocalSlot`'s existing
`sym.Captured` dispatch then already does the right thing for free: a
captured binding's arena-allocation call sits at a program point re-executed
every iteration, so it returns a genuinely fresh heap address each time -
Go 1.22 per-iteration-capture semantics fall out with zero extra
bookkeeping.

## Runtime trap diagnostics

Every runtime safety trap this package emits - `genBoundsCheck`/
`genSliceRangeCheck` and `genMakeSizeCheck` - now prints a real, informative
diagnostic to stdout via a plain `printf` call (the same `g.printfType`/
`g.printfFn` extern `print`'s own codegen already uses) *immediately before*
the existing `llvm.trap` + `unreachable` sequence, not instead of it: the
abort mechanism itself is completely unchanged, a genuine
illegal-instruction process crash, not a graceful `exit(1)` or any kind of
recoverable panic. This mirrors Go's own runtime-panic convention exactly (a
message, then a hard crash).

Each message is built from the exact same runtime `llvm.Value`s the check
itself already computed - `idx`/`size` (`genBoundsCheck`), `low`/`high`/
`max` (`genSliceRangeCheck`), or `nVal`/`capVal` (`genMakeSizeCheck`)
already in hand as real SSA values:

- `genBoundsCheck`: `"runtime error: index %d out of range [0:%d)\n"`
  (`fmtBoundsTrap`) with `idx`/`size`.
- `genSliceRangeCheck`: `"runtime error: slice bounds out of range [%d:%d]
  with capacity %d\n"` (`fmtSliceRangeTrap`) with `low`/`high`/`max`.
- `genMakeSizeCheck`: `"runtime error: makeslice: len %d, cap %d out of
  range\n"` (`fmtMakeSizeTrap`) with `nVal`/`capVal` - covering all three of
  that check's own combined conditions (`n < 0`, `cap < 0`, `cap < n`) with
  one message, since the process is about to abort regardless of which one
  fired.

Each format string is its own new cached global, built via `defineCString`
in `setupRuntime` exactly like every other format-string global there - no
new mechanism, just three more entries in the same table.

```
$ llvmc.exe program.llx
runtime error: index 5 out of range [0:5)
```
(followed by the real process crash, unchanged - see
`TestOutOfBoundsIndexTraps`, `TestSliceRangeCheckTraps`,
`TestMakeCapLessThanLenTraps`, and `TestMakeNegativeSizeTraps`, all now
additionally asserting the printed message text on top of the abnormal-exit
assertion each already had.)

## Global `var` initializers

A top-level `var`'s initializer can be any well-typed expression now -
matching Go's own real behavior - not just a compile-time constant. Sema
needs no opinion on this at all: it already type-checks a non-constant
global initializer fine - this was always purely a codegen-level question of
what `codegen.GeneratePackage` was willing to lower a global's initializer
to.

**Two lowering paths, chosen per-initializer, not per-program:**

- **Foldable at compile time** (`isConstFoldable`, `src/codegen/constfold.go`)
  - literals, parenthesization, unary `-`/`!`, binary arithmetic/comparison/
  logical/string-concatenation, and struct/fixed-size-array composite
  literals built entirely from constants - is folded directly into the
  global's own LLVM initializer via `constExpr`. `isConstFoldable` is a pure
  structural predicate (no evaluation, no diagnostics); once it says yes,
  `constExpr` is guaranteed to only ever hit a genuinely-erroneous case
  (division by zero, an out-of-range literal) if it fails, never its
  "not a constant at all" default cases.
- **Everything else** (a function call, a reference to another `var`, a
  member/index expression, a dynamic-array/slice literal, `new`, a lambda
  literal, ...) - the global gets a zero-value initializer up front, and its
  real initializer expression is queued (`Generator.globalInits`) for
  `buildGlobalInitFn` (`src/codegen/globalinit.go`) to lower as real
  generated code, once every global and every function/constructor
  signature in the whole package already exists.

**The synthesized init function, and `@llvm.global_ctors`:** `buildGlobalInitFn`
builds one parameterless function (`llvm_lang.global_init`) - the same
per-function generation state `genFuncBody`/`genConstructorBody`/
`genLambdaFunc` each set up - and lowers every queued initializer inside it
via `storeValueInto` (the same helper a local `var`/short-var-decl already
uses). Unlike every other synthesized helper function this package builds
for itself, this one keeps `AddFunction`'s own default linkage (external)
rather than private: `cmd/llvmc`'s JIT driver looks it up directly by this
exact name (see "JIT execution" below), which a private symbol has no name
for. `genCtors` (the same top-level pass that calls `buildGlobalInitFn`)
then registers this function into LLVM's own `@llvm.global_ctors` mechanism
- a standard array of `{ i32, ptr, ptr }` entries (`{ priority, ctor function
pointer, associated data }`) any real linked/loaded program's C runtime
startup sequence scans and calls, in priority order, before ever reaching
`main`. A program whose every global is compile-time-constant gets no
`llvm_lang.global_init` function and no `@llvm.global_ctors` array at all.

**Declaration order, not a full dependency graph:** every queued
initializer runs in plain source declaration order across the whole package
- a deliberately narrower simplification than Go's own real spec (which
topologically sorts by actual variable dependencies) - see `DECISIONS.md`'s
dated entry for why. A global's initializer referencing another global
declared *later* in the same package sees only that other global's zero
value - `TestGlobalNonConstantInitializersRunInDeclarationOrder`
(`src/codegen/globals_test.go`) asserts this directly.

**JIT execution needs this triggered manually:** unlike a normal linked/
loaded program, `cmd/llvmc`'s JIT path (`jitRunMain`) never goes through a
real C runtime startup sequence that would scan `@llvm.global_ctors` on its
own. LLJIT has no equivalent of the legacy MCJIT `ExecutionEngine`'s
`RunStaticConstructors()` - instead, `jitRunMain` (and this package's own
test helpers) looks up `llvm_lang.global_init` by name and calls it
directly, exactly like `main` itself, right after adding the module and
before ever calling `main`. The `@llvm.global_ctors` array is still built
regardless (a real linked/loaded program's C runtime still needs it), but
the JIT path never walks it. A module with no non-constant globals has no
`llvm_lang.global_init` function to find at all, so a failed lookup here
just means there was nothing to run. `-emit-llvm` needs no such change: it
never reaches `llvm.NewLLJIT` in the first place, so the synthesized init
function shows up in the printed IR text like any other generated code,
unexecuted.

## The `print` builtin, concretely

`print(x)` lowers to a call into libc's `printf` (declared extern): every
numeric width gets its own format specifier (`"%d\n"` for `i8`/`i16`/`i32`,
`"%lld\n"` for `i64`, `"%f\n"` for `f32`/`f64`), a string argument uses
`"%.*s\n"` (the explicit length means a non-null-terminated string value
never needs `strlen`), and a bool argument selects between two cached
`"true"`/`"false"` string values first. A pointer prints its raw address via
`"%p\n"` (`fmtPtr`) - standard C `printf`'s own pointer-address specifier,
no new runtime primitive needed. **print always appends a trailing
newline** - there's no separate "print without newline" builtin.

Not every `sema.Type` reaches this switch, by design: `checkPrintCall`
(`src/sema/typecheck.go`) gates the argument through `typeIsPrintable`
before codegen ever sees it - a bare function value or map value, or one
nested anywhere inside a struct/array field, is rejected with a
compile-time diagnostic there, so `genPrintCall`/`genPrintValueBare`'s own
`default:` panic case is unreachable on a tree that already passed
`sema.Check`.

A struct or array value is rendered recursively, Go-`fmt`-`%v`-inspired
(not an exact match of Go's own output, just a reasonable pick - see
`genPrintStructValue`/`genPrintArrayValue`, `src/codegen/runtime.go`):

- a struct prints as `{f0 f1 ...}` - each field's value, space-separated, in
  declaration order, wrapped in braces.
- an array prints as `[e0 e1 ...]` - each element's value, space-separated,
  in index order, wrapped in brackets.
- a struct/array-typed field or element is rendered the same way,
  recursively - e.g. a struct of two `Point{x, y int}` fields prints as
  `{{1 2} {3 4}}`.

Built from repeated `printf` calls (one per field/element, plus one per
punctuation character) rather than one combined format string - there's no
way to know a struct/array's shape at format-string-construction time in
general. Every nested field/element uses a "bare" (no trailing newline)
format string; only the outermost `print(...)` call appends the actual
trailing newline, once, after the whole value has finished rendering.

## First-class functions: fat pointers, and direct vs. indirect calls

See `LANGUAGE.md`'s "First-class functions" section for the language-level
feature and `DECISIONS.md` for why the representation below was chosen.
**This section describes the representation as it shipped in that first
round; the "Lambdas" section below documents the uniform-ABI thunk mechanism
a later round added on top of it** - in particular, the "`ctxPtr` is
extracted but never passed along" claim this section originally made is no
longer accurate once a genuine closure exists that actually needs `ctxPtr`
passed through an indirect call.

**Representation.** `sema.TypeFunc` maps to the literal (unnamed) LLVM
struct `{ ptr, ptr }` - a "fat pointer" of `{ fnPtr, ctxPtr }` (`llvmType`,
`src/codegen/types.go`). A bare, uncalled reference to a declared free
function (`add`, not `add(...)`) builds this struct (`genFuncValue`,
`src/codegen/expr.go`); `ctxPtr` is always `llvm.ConstNull(g.ptrTy)` for this
case specifically - a free-function reference never closes over anything.
`genFuncValue` is the exact extension point a future bound-method value
(`p.move` referenced without a call) could use instead - closing over the
receiver's own address as `ctxPtr` rather than null. Passing/returning/
storing a function value moves this two-field struct like any other small
aggregate value.

**Direct vs. indirect calls.** `genCallExpr`'s dispatch mirrors sema's own
(`funcSigForCall`) exactly, so there is exactly one place on each side of
the pipeline that decides which of the two a given call is:

- A **direct** call - callee is a plain `Ident` resolving (via `Info.Refs`)
  to an actual declared free function (`sema.SymFunc` with a real
  `FuncDecl` - `isDirectFuncCall`) - compiles to a plain, ordinary `call`
  instruction (`genFuncCall`), exactly as before this round: looks the
  callee's LLVM function straight up in `g.funcs` and calls it, no `ctxPtr`
  involved at all. The fat-pointer representation is never constructed or
  touched for this case at all - zero indirection overhead. **This is
  completely unaffected by the "Lambdas" section below** - a lambda is
  never reachable through a direct call in the first place.
- An **indirect** call - anything else that type-checked as callable: a
  function-typed variable/parameter, an ordinary (non-method) struct field
  of function type (`cb.fn(5)` - `isMethodCall` tells a real method-call
  `MemberExpr` apart from a func-typed-field one), or any other expression
  whose value is itself a function (e.g. `getAdder()(x)` chains straight
  through) - goes through `genIndirectCall`: evaluate the callee as an
  ordinary value expression to get its fat-pointer struct, `ExtractValue`
  out both `fnPtr` and `ctxPtr`, build the `llvm.FunctionType` to call
  through directly from the callee's own `sema.Type` (`Params`/`Return`,
  plus a leading `ctxPtr` parameter - see "Lambdas" below for why), and
  `CreateCall` through that raw pointer with `ctxPtr` passed as the real
  first argument.

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
this round that's a real, deliberate consumer of the arena allocator beyond
string concatenation/dynamic arrays - every captured variable's storage,
and every closure's own capture context, is arena-allocated.

### Capture analysis and heap promotion (sema decides, codegen executes)

`sema.Check`'s capture analysis (`src/sema/capture.go`) computes, for every
`FuncLit` node, the ordered list of enclosing-scope symbols it captures by
reference (`Info.Captures[lit]`), and marks each captured `*sema.Symbol`
(`Symbol.Captured`). Codegen makes exactly one decision based on this, at
exactly one call site (`allocLocalSlot`, `src/codegen/func.go`), shared by
every place a local variable/parameter's storage is allocated: a captured
symbol's storage comes from the arena (`genArenaAlloc`) instead of
`createEntryAlloca`'s ordinary stack alloca - necessary, not an optimization
choice, since a captured variable's *address* is stored inside a lambda's
own capture context, and that lambda's value can outlive its declaring
function's own stack frame the moment it's returned, stored, or passed
onward. Both paths return the identical `ptr`-typed `llvm.Value` shape -
every reader treats the result identically regardless of which one it
turned out to be. A non-captured local is completely unaffected.

### Representation: the exact same fat pointer, `ctxPtr` finally does real work

A lambda value is still exactly `{ fnPtr, ctxPtr }` - the identical
two-field struct type `sema.TypeFunc` already lowered to for a bare
free-function reference - there is no second, parallel representation. For
a genuine lambda, `ctxPtr` points to a freshly arena-allocated **capture-
context struct**: a synthesized, anonymous LLVM struct (built fresh per
literal in `genFuncLit`) with one `ptr`-typed field per captured symbol, in
`Info.Captures[lit]`'s own order - each field holds that variable's already-
arena'd *address*, not a copy of its value, since capture is by reference.
`genFuncLit` resolves every captured symbol's address (via `addrOfSymbol`,
see below) *before* switching into the literal's own function-generation
state - still running in the *enclosing* function's own context at that
point.

`genFuncLit` builds the closure value itself as a real runtime aggregate
(`llvm.Undef(g.funcValTy)` + two `CreateInsertValue`s), not a `ConstStruct`
the way a bare free-function reference's fat pointer is - `ctxPtr` here is a
genuine runtime value, and LLVM's `ConstStruct` requires every field to
already be a constant; embedding a non-constant SSA value into a literal
constant aggregate is invalid IR that only surfaces as a verifier failure, a
real mistake hit and fixed while building this feature. A capture-free
literal's `ctxPtr` is still a genuine `ConstNull`, so that case keeps the
cheaper `ConstStruct` path unchanged.

Each `FuncLit`, wherever it lexically appears, becomes its own independent,
real, top-level LLVM function (`genLambdaFunc`) with a synthesized name -
`llvm_lang.lambda.N`, `N` a plain per-module monotonically increasing
counter (`Generator.lambdaCounter`) - simpler than deriving a name from
whichever function lexically encloses this literal, and equally
collision-free regardless of nesting depth.

**Generating a nested literal's body without disturbing the enclosing
function.** `genLambdaFunc` temporarily replaces every one of `Generator`'s
per-function-frame fields (`curFn`/`entryBlock`/`locals`/`loopStack`/
`curFunc`/`curReceiver`, plus the three lambda-specific ones below) with the
literal's own fresh state - saving each in a plain local variable first and
restoring all of them once the literal's body is fully generated. The same
save-in-a-local/restore-after-recursing shape `sema/typecheck.go`'s
`checkFuncLit` already uses for its own `curFunc`: since this is an
ordinary (non-reentrant) function call, Go's own call stack already handles
arbitrary nesting depth for free.

**Reading a captured variable from inside a lambda: one more indirection
than an ordinary local.** Three new `Generator` fields describe the function
*currently being generated*, when (and only when) it's itself a lambda's own
synthesized function: `curCtxPtr` (that function's own real first
parameter), `curCaptureIndex` (each captured symbol's field index within
`curCaptureTy`). `genAddr`'s `Ident` case and `genFuncLit`'s own capture-
context-building lookup both now go through one shared helper,
`addrOfSymbol`: check `g.locals` first (works identically whether storage
is a stack alloca or an arena allocation), then `g.globals`, and only then
`curCaptureIndex`/`curCtxPtr` - load the function's own `ctxPtr` parameter,
`CreateStructGEP` to the matching field (a pointer), `CreateLoad` that
(getting the captured variable's real address), then the caller loads/
stores through that address like any other lvalue.

Routing *both* call sites through this one shared lookup is what makes a
variable captured **two or more enclosing function levels down** work with
no special relaying code anywhere: sema's own capture analysis already
marks a doubly-nested lambda's outer variable as captured by *every*
enclosing lambda between it and the variable's own owning function - so
when `genFuncLit` is asked for some symbol's address while generating an
*enclosing* lambda's own body, and that enclosing lambda doesn't own the
symbol directly either, `addrOfSymbol`'s third branch already knows how to
fetch it through that enclosing lambda's own `ctxPtr`, with zero additional
bookkeeping. `TestTwoLevelNestedClosureCapture`/
`TestTwoLevelNestedCaptureRelaysThroughBothLambdas`
(`src/codegen/lambda_test.go`, `src/sema/lambda_test.go`) exercise exactly
this shape end to end.

### The uniform-ABI thunk: resolving the direct-vs-indirect calling-convention conflict

A free-function reference and a genuine lambda can both flow through the
identical `func(T1, T2) R`-typed variable/parameter at the language level -
an indirect call through that variable can't know statically which of the
two it's holding at runtime, yet it has to emit one single, valid call-
instruction shape. This is a real conflict: a free function's own real
declared LLVM signature has **no** `ctxPtr` parameter at all (keeping a
*direct* call zero-overhead), but a genuine lambda's own real underlying
function **must** take `ctxPtr` as a real, dereferenced first parameter to
reach its captures. Calling through a function pointer whose real callee
has a different real parameter list than the call site expects is
genuinely invalid, UB-risking IR that can silently corrupt the stack/
registers rather than crash cleanly.

**Resolved the standard way real closure implementations do: every
function-value's real, underlying LLVM function shares one uniform,
`ctxPtr`-first calling convention the moment it's called *indirectly*
through a fat pointer.** Concretely:

- A **direct** call to a statically-known free function (`add(1, 2)`) is
  completely untouched - it bypasses the fat pointer entirely and calls
  `add`'s own real, natural (`ctxPtr`-less) signature.
- A **bare reference to a free function used as a value** (`fn := add`, not
  `add(...)`) no longer puts `add`'s own real address into the fat
  pointer's `fnPtr` field. `genFuncValue` now calls `genFuncThunk(sym)`
  instead, which builds (once, memoized in `Generator.thunks`) a small
  adapter function named `add.thunk`: real signature `R add.thunk(ptr
  ignoredCtx, T1, T2, ...)`, whose entire body ignores its own `ctxPtr`
  parameter and calls straight through to `add`'s own real function.
  `fnPtr` in the fat pointer is the thunk's address, not `add`'s own.
- A **genuine lambda's** own synthesized function already has this uniform
  shape natively - every lambda is always called indirectly, never
  directly, so every one gets a `ctxPtr` parameter unconditionally, for
  uniformity, even one that never reads it - so it needs no thunk of its
  own; its own address goes directly into the fat pointer.
- **Every indirect call** (`genIndirectCall`) now always extracts *both*
  `fnPtr` and `ctxPtr` from the fat pointer and passes `ctxPtr` along as the
  callee's real first argument, unconditionally - whether `fnPtr` turns out
  to be a free function's thunk or a genuine lambda's own function, the
  call site's own built `llvm.FunctionType` now always matches the real
  callee's own real signature, since both kinds of real callee share the
  identical shape.

A `FuncLit` is *never* reachable through a direct call at all, by
construction: `isDirectFuncCall` only matches a plain `Ident` resolving to a
declared `sema.SymFunc`, and a function-literal expression is neither - so
every lambda value always goes through `genIndirectCall`, and so always gets
the uniform `ctxPtr`-first treatment automatically.

This preserves "First-class functions"'s explicit, verified "direct calls
are genuinely zero-overhead" property completely untouched
(`TestDirectCallStillCompilesToPlainCall` still passes unchanged, and a
fresh `-emit-llvm` inspection of a plain `add(2, 3)` call shows exactly
`call i32 @add(i32 2, i32 3)`, no thunk generated at all), while making
indirect calls correctly polymorphic over "was this a plain function or a
real closure." `TestUniformAbiAcrossPlainFunctionAndLambda`
(`src/codegen/lambda_test.go`) is the test that would catch a regression
here directly: a single `func(int, int) int`-typed variable holds a plain
free-function reference first, then a genuine lambda, calling it indirectly
both times through the identical variable.

## Generator functions

See `LANGUAGE.md`'s "Generator functions" section for the language-level
feature and `DECISIONS.md`'s dated entry for why a push/callback lowering
was chosen over true suspend/resume coroutines. Two genuinely separate
lowerings meet at one ordinary call: the **producer** (the generator
function itself) takes an implicit trailing callback parameter and never
really returns a value; the **consumer** (a generator-consuming range-for)
synthesizes that callback, reusing the lambda/closure machinery wholesale.

### Producer: an implicit trailing callback parameter, always-void return

`declareFuncSignature` (`func.go`) recognizes a generator FuncDecl (its
declared return type's `Kind == sema.TypeGenerator`) and appends one
implicit trailing parameter to its REAL LLVM signature - `g.funcValTy`, the
exact same `{fnPtr, ctxPtr}` fat-pointer representation this language's
first-class functions/lambdas already use - and forces its real LLVM return
type to `void`, regardless of the declared element type. `genFuncBody`
mirrors this: when generating a generator's own body, it reads that
trailing parameter (`g.curGeneratorCallback`) and the declared element type
(`g.curGeneratorElem`) into per-function Generator fields, and `hasReturn`
is forced false - both the "no missing-return check" and "an ordinary
`return value` is illegal" rules fall out of this one flag.

`genYieldStmt` (`stmt.go`) dispatches on `g.matchExprStack` FIRST
(unchanged from the match-expression case), and only when that's empty
checks `g.curIsGenerator`. The generator case: evaluate the yielded
expression, invoke the trailing callback parameter indirectly via
`genIndirectCallValue` (a small helper factored out of the existing
`genIndirectCall`), then branch on its bool result - `false` means the
consumer broke out of its own range-for early, so this unwinds every
destructor back to 0 (a real early function exit) and emits `ret void`
immediately; `true` falls straight through to whatever follows the `yield`.
Unlike the match-expression case (which always terminates its own block),
this does NOT always terminate - only the `false` branch is a real function
exit.

`genIndirectCallValue` (`expr.go`) is `genIndirectCall`'s own extraction
refactored into a reusable helper: given an already-evaluated fat-pointer
value, a parameter-type list, a return type, and already-evaluated
arguments, it extracts `fnPtr`/`ctxPtr`, builds the matching
`llvm.FunctionType`, and calls through - `genIndirectCall` itself just
evaluates its callee node and forwards to this; `genYieldStmt` calls it
directly with the generator's own trailing parameter as `fnVal`.

### Consumer: a synthesized callback, reusing lambda/closure machinery wholesale

`genRangeForStmt` recognizes a `sema.TypeGenerator` subject and dispatches
to `genRangeForGenerator` (`stmt.go`) - architecturally nothing like
`genRangeForArray`/`genRangeForMap`'s own real loops: there is no loop
generated at the call site at all. The consuming loop's own body becomes a
real, independent, private-linkage LLVM function (the "callback",
`genRangeGeneratorCallbackFunc`) with a fixed signature - `(ctxPtr ptr, v
elemType) -> bool` - built via `buildClosureValue`, the same helper
`genFuncLit` uses to build an ordinary lambda's closure value. The
generator's own declared arguments are evaluated once, in the CONSUMING
function's own context (never inside the callback), then the generator is
called exactly once with the callback's own fat pointer appended as its
real trailing argument - the generator itself drives every iteration by
calling back into it.

**Capture analysis is reused, not reinvented**, because the consuming
loop's own body needed the identical free-variable analysis a `FuncLit`'s
body already gets. `sema.resolveRangeForStmt` (`resolve.go`) promotes a
generator-consuming range-for's own `Scope` to `ScopeFunc` kind (instead of
the ordinary `ScopeBlock` a map/array range-for keeps) whenever its subject
is a direct call to a generator function (`isGeneratorRangeSubject` -
purely syntactic, decidable well before type-checking ever computes
`sema.TypeGenerator`) - exactly the same `ScopeFunc`-with-nil-`Receiver`
shape `resolveFuncLit` already builds for a `FuncLit`. `capture.go`'s
`computeCaptures` then also walks every `RangeForStmt` node, calling
`analyzeGeneratorRangeCaptures` (a thin wrapper around the same
`analyzeCaptures` helper `analyzeFuncLitCaptures` now calls too) whenever
that scope promotion actually happened - walking only the range-for's own
BODY (never its subject expression) and recording the result into
`info.Captures[n]`, keyed by the `RangeForStmt` node itself.
`genRangeGeneratorCallback` reads that same map and passes it straight to
`buildClosureValue` - codegen's own `addrOfSymbol`/capture-context-field
machinery needs zero changes to serve a second caller.

### The one genuinely new mechanism: `loopCtx`'s return-from-callback mode

Inside the synthesized callback, `break`/`continue` can't branch to a real
basic block the way every other loop kind's own `breakTarget`/
`continueTarget` do - there is no real loop inside the callback at all.
`loopCtx` (`codegen.go`) gained one new field, `returnFromCallback bool`:
when true, `genBreakStmt`/`genContinueStmt` (`stmt.go`) - still the ONE
shared implementation every loop kind funnels through - return a bool
directly from the callback's own frame instead of branching: `false` (stop
early) for break, `true` (keep going) for continue. Every other loop kind
constructs its own `loopCtx` exactly as before - `returnFromCallback`
defaults false, so none of them needed a single line changed.

A normal, non-terminating fall-through past the end of the callback's own
body means "continue" (`genRangeGeneratorCallbackFunc`'s own fallback):
unwind whatever's left on `Generator.destructors` and `ret i1 true`.

### What sema rejects to keep this sound

A handful of restrictions exist purely because this lowering has no way to
support them soundly without real additional machinery this round doesn't
build (see `LANGUAGE.md`'s own "Explicitly out of scope" list):

- **`return` reached directly inside a generator-consuming range-for's own
  body** (not nested inside a further `FuncLit`) is a clean diagnostic
  (`checker.inGeneratorRangeBody`) - that body executes as a genuinely
  separate LLVM function at runtime, with no way to make the REAL enclosing
  function return early.
- **A generator function's own body ranging over another generator**
  (nested composition) is rejected in `checkRangeForStmt` the moment
  `c.curFunc.isGenerator` is already true - the inner synthesized callback
  would need to capture the outer generator's own invisible callback
  parameter, which the capture analysis has no way to reach.
- **Any other use of a generator call's own result** (stored, passed,
  returned, or ranged-over indirectly through a stored function value) is
  rejected at the type level (`sema.TypeGenerator`) - it has no legal
  standalone runtime representation under this lowering, only ever
  appearing as a range-for's own direct subject.

## Coroutines

See `LANGUAGE.md`'s "Coroutines" section for the language-level feature
(`async func`/bare `await`, a caller-held handle driven by hand via
`resume`/`done`/`delete`) and `DECISIONS.md`'s dated entry for why this is a
genuinely separate feature from `yield T` generator functions above. Unlike
a generator's push/callback lowering, this uses LLVM's own first-class
coroutine intrinsics directly (`llvm.coro.id`/`coro.begin`/`coro.suspend`/
`coro.resume`/`coro.destroy`/`coro.done`/`coro.free`/`coro.end` - all
declared via the generic intrinsic-declaration mechanism, since none have a
dedicated `llvm-c` header) plus the standard optimization pipeline's own
`CoroSplit`/`CoroCleanup` passes, which this project's existing
`default<O2>` pipeline already runs with zero extra pass-pipeline
configuration.

### Declaring an async function: real signature, `presplitcoroutine`, no implicit parameter

`declareFuncSignature`/`genFuncBody` (`func.go`) recognize an async
`FuncDecl` via `Tree.FuncIsAsync` - unlike a generator, there's no implicit
trailing parameter and no forced-void declared type: an async function's
REAL LLVM return type is instead the coroutine handle `llvm.coro.begin`
produces (a plain `ptr`), regardless of its declared (void-only this round)
result type. The generated function is tagged with the `presplitcoroutine`
enum attribute right after `AddFunction` - required or LLVM's
coroutine-splitting passes silently never look at the function at all.

`genCoroPrologue` (`coroutine.go`) emits the fixed ramp prologue into the
function's own entry block: `coro.id(0, null, null, null)` ->
`coro.size.i64()` -> `malloc` -> `coro.begin(id, mem)`, caching the
resulting id/handle onto `Generator.curCoroId`/`curCoroHandle` - safe as
plain SSA values since the entry block dominates every later use, including
every per-`await` cleanup block below. No `llvm.coro.alloc` allocation-
elision check is emitted - every call always heap-allocates a real frame.

### The coro.suspend switch: three arms, not two - the one bug this took real empirical verification to catch

Every suspend point (`genAwaitStmt`, an ordinary `await`; `finishCoroBody`,
the implicit "final suspend" reached when a body falls off its own end or
hits a bare `return`) is `coro.save` + `coro.suspend` + a real three-way
`switch` on its `i8` result, following
[LLVM's own documented coroutine-suspend shape](https://llvm.org/docs/Coroutines.html#coro-destroy)
exactly:

- **case 0 (resumed)**: continue the body normally - or, for the *final*
  suspend specifically, `llvm.trap()` + `unreachable`, matching LLVM's own
  documented rule that resuming a coroutine already at its final suspend
  point is undefined behavior. This language's own `resume(h)` builtin
  never lets a live caller reach that arm for real (it checks `done(h)`
  first), but nothing in the IR itself proves that.
- **case 1 (destroyed)**: this suspend point's OWN dedicated cleanup block -
  destructor calls (in reverse order) for a *snapshot* of
  `Generator.destructors` taken at exactly this point, then
  `genCoroFreeFrame` (`coro.free` + `free`), then falls through to the
  shared suspend tail. Every `await` gets its own cleanup block rather than
  a shared one dispatched via a saved suspend-index - `coro.suspend`'s own
  per-call switch already gives each suspend point a distinct case-1 target
  for free (see `DECISIONS.md`'s dated entry).
- **switch DEFAULT (bare suspend - not case 1)**: `coroEndBlock` -
  `coro.end` + `ret` the handle, touching nothing else. This is the arm
  every ordinary suspend/resume actually takes - **conflating this with the
  destroy arm was the actual bug**, caught only by JIT-executing a real
  multi-`await` coroutine and observing a genuine double-destruct: routing
  default to destroy makes the ramp's own "haven't been resumed or
  destroyed yet" sentinel value run cleanup unconditionally on the very
  first call. Matching LLVM's own documented switch shape - default is
  bare-suspend, destroy is its own explicit case 1 - fixed it outright; see
  `coroEndBlock`/`genCoroFreeFrame`/`genAwaitStmt`/`finishCoroBody`'s own
  doc comments (`coroutine.go`) for the exact current shape.

### `resume`/`done` builtins: driver-side safety, not raw intrinsic calls

`resume(h)`/`done(h)` (`genResumeCall`/`genDoneCall`, `coroutine.go`) are
never a bare `coro.resume`/`coro.done` call - each first checks whether `h`
is nil (already `delete`d) and short-circuits to a safe, defined result
(`false`/`true` respectively) rather than touching freed memory; `resume`
additionally checks `coro.done(h)` BEFORE calling the raw `coro.resume`
intrinsic, since resuming a coroutine already at its own final suspend
point is undefined behavior at the LLVM level - this driver-side guard is
what makes it safe to call `resume`/`done` on an already-finished handle at
all. `resume`'s own bool result tracks whether the coroutine suspended
again or just finished (checked via `coro.done` immediately after the raw
`coro.resume` call, since that intrinsic itself returns `void`).

### `delete h` and automatic scope-exit: one non-copyable value, two triggers

A coroutine handle is `sema.TypeCoroutine` - a real, storable, non-copyable
value (unlike a generator's call result), so `destructorFuncFor` (`func.go`)
gets a new case returning a small synthesized wrapper,
`coroDestroyLocalFn` (`llvm_lang.coro.destroylocal`): `void(ptr addr)`,
loading the handle stored at `addr` then calling `coro.destroy` on it. This
one small adapter is what lets `pushDestructorEntry`/`unwindDestructorsTo`
(the exact same mechanism every struct/enum destructor already uses) serve
a coroutine handle local with **zero changes to either** - the existing
convention always forwards a local's own storage ADDRESS, but `coro.destroy`
needs the handle BY VALUE, hence the adapter.

Explicit `delete h` (`genCoroDeleteStmt`) is the one genuinely new wrinkle:
a coroutine handle is BOTH explicitly `delete`-able AND automatically
destructor-tracked, a combination no other type in this language has. So an
explicit `delete` must also remove `h`'s own entry from
`Generator.destructors` (`removeDestructorEntry` via `slices.Delete`,
preserving every other entry's relative order) so a LATER automatic
scope-exit unwind never double-destroys it - then nulls the local's own
slot, making a second explicit `delete`, or a `resume`/`done` afterward, a
safe no-op via the nil-handle guards above. Since a coroutine handle is
non-copyable, there's only ever one variable that could hold a given handle
value, so nulling the one local slot is a COMPLETE mitigation for this
type, unlike a raw pointer which can have aliasing copies.

**A second real bug, found by reasoning about `genIfStmt`'s own snapshot/
restore discipline, not by a failing test:** `genIfStmt` unconditionally
restores `Generator.destructors` back to its pre-`if` snapshot after
generating each branch - so `if cond { delete h }` (with no `else`)
resurrects `h`'s own destructor entry at the compile-time bookkeeping level
even after that branch's own `delete` already ran and nulled `h`'s slot at
runtime. Automatic scope-exit cleanup therefore still calls
`coroDestroyLocalFn` against `h` regardless of which branch actually ran -
meaning `coroDestroyLocalFn` MUST itself be null-safe, not just the
explicit-`delete` path. Without this guard, the module still compiled,
verified, and JIT-executed without crashing - but only because the
optimizer had silently exploited undefined behavior (`llvm.coro.destroy`
called twice against the same handle) to collapse the two calls down to one
lucky-looking outcome, confirmed directly by dumping the optimized IR
before and after adding the guard. This is precisely the "looks fine
because nobody's test happened to distinguish it from correct" failure mode
`AGENTS.md`'s review-process section warns about, caught here specifically
by tracing every other place `Generator.destructors` gets mutated against
the new `removeDestructorEntry`, not by a test failing first.

### The `-no-opt` trap: a real, confirmed compile-time restriction

Confirmed directly: JIT-executing (and separately, `-o` AOT-compiling) a
coroutine-using program under `-no-opt` crashes outright - `LLVM ERROR:
Cannot select: intrinsic %llvm.coro.destroy` - since `-no-opt` skips
`RunPasses` entirely, and `llvm.coro.*` intrinsics are only ever lowered
into real code by the optimization pipeline's own `CoroSplit`/`CoroCleanup`
passes. Given this failure mode is worse than a plain compile-time error,
`src/compiler`'s `finishPipeline` now rejects any program declaring an
`async` function outright when its own `optimize` parameter is false
(`checkNoOptAsyncRestriction`) - a clean, source-position-attributed
diagnostic, checked before `codegen.GeneratePackage` even runs.

### The `coroutine` type keyword: zero codegen changes, one gating fix

`coroutine` (`sema/typecheck.go`'s `typeFromSymbol`) resolves straight to
`Type{Kind: TypeCoroutine, Elem: &voidType}` from the universe scope, the
same path `int`/`f64`/etc. already use - every codegen site keyed on
`sema.TypeCoroutine` (`llvmType`, `destructorFuncFor`) already handled it
generically, so this needed no codegen change of its own.

One real gap it did expose: `programHasAsyncFunc` (now `programUsesCoroutines`,
`coroutine.go`) used to gate `setupCoroutines` purely on "does the package
declare an `async func`", on the assumption a coroutine handle could only
ever originate from calling one. A `coroutine`-typed var/field/param can now
exist with no async func anywhere in the program - always nil, but still
destructor-tracked (`destructorFuncFor`'s `TypeCoroutine` case), which would
reference `coroDestroyLocalFn` uninitialized if `setupCoroutines` were
skipped. `programUsesCoroutines` now also scans every tree's `info.Types`
for a `TypeCoroutine` entry (populated for every type-position node - see
`typeFromNode`'s own doc comment) alongside the original async-func check.

## `main` is the real entry point

The function literally named `main` (no receiver) becomes the real LLVM
`i32 @main()` C entry point, regardless of whether the source declares a
return type for it: a bare `return`/falling off its end becomes `ret i32 0`
(a real, valid exit code, never `unreachable`), and `return expr` returns
`expr` directly (typed `int` == `i32`, so no cast is ever needed).

Codegen doesn't validate this itself: `main` declaring anything other than
no return type or `int` is a real semantic rule enforced by
`sema.checkFuncDecl` before codegen ever runs - `declareFuncSignature`
simply forces `main`'s LLVM return type to `i32` unconditionally, trusting
that whatever declared return type sema already accepted is one of those
two (this used to be a codegen-level check instead - see `AGENTS.md`'s
Architecture section).

## Local variables: entry-block allocas

Every `var`/short-var-decl/parameter gets a stack slot via `alloca`, always
hoisted into the function's **entry** block (`createEntryAlloca`), never
emitted at the literal point of declaration. This matters for correctness,
not just style: an `alloca` inside a loop body's own basic block is a fresh
stack allocation on *every dynamic execution* of that block - hoisting to
the entry block means a `var`/`:=` declared inside a loop allocates once
and is simply re-stored each iteration.

## Method receivers: an implicit pointer parameter

A method's receiver (always implicit, always by-reference) lowers to a
real, explicit first LLVM parameter of type "pointer to the receiver's
struct type". A method call's receiver expression is lowered to its
*address* (`genAddr`), never a loaded copy, so a mutating method's writes
are visible to the caller. `this` inside the method body is that same
pointer parameter directly - it needs no `alloca` of its own, since it
already *is* an address.

## Constructors

See `LANGUAGE.md`'s "Constructors" section for the language-level feature.

**Each constructor lowers to its own real LLVM function**, reusing the
implicit-first-pointer-parameter convention an ordinary method's receiver
already uses - a constructor's own declared parameters follow that implicit
pointer, and its LLVM return type is always `void`: a constructor never
declares a return type of its own, since it "returns" the struct being
constructed implicitly, by populating `this` through that same implicit
pointer. A bare `return` inside a constructor body lowers to `ret void`,
same as any other void function/method - sema rejects `return expr` at
check time, so codegen never needs to consider that case.

**Naming**: a constructor has no name of its own in the source (selected by
argument count, not called by name), so its generated LLVM function is
named `Struct.constructor.N` (`N` its declared parameter count) - the same
`Type.MethodName` convention an ordinary method's own generated function
already uses, adapted for a constructor's lack of a name: arity already
uniquely identifies a struct's constructor (`StructInfo.Constructors`,
keyed by arity for exactly this reason).

**Declared in its own pass**, mirroring `declareFuncSignature`/`genFuncBody`'s
own split into a signature-declaration pass and a body-generation pass
(`declareConstructorSignature`/`genConstructorBody`): every constructor in
the whole program is declared before any function or constructor body is
generated, so a constructor call reaches its callee already declared
regardless of declaration order.

**Lowering a call** (`genConstructorCall`, `src/codegen/expr.go`): sema
already resolved *which* constructor a call selected, recording that
specific constructor's own `*sema.Symbol` directly onto the call's callee
node in `Info.Refs`. Codegen recognizes this via a plain `Info.Refs` kind
check (`isConstructorCall`) and lowers it by: allocating a fresh stack slot
for the struct being built (the same alloca-then-load approach a struct
composite literal already uses), calling the selected constructor with that
alloca's own address as the implicit `this` argument followed by the call's
own evaluated arguments, then loading and returning the now-populated
value. This works identically whether the call's callee is a bare `Ident`
or a `MemberExpr` (a package-qualified one, `pkg.Point(args)`) -
`isConstructorCall` never needs to branch on the callee's own node kind,
only on what `Info.Refs` resolved it to.

`Generator.ctors` is `Generator.funcs`' dedicated counterpart for
constructors - kept as its own map purely for read-site clarity, since a
constructor's `*sema.Symbol` (`sema.SymConstructor`) is a completely
different declaration shape from an ordinary free function's or method's.

## Destructors

See `LANGUAGE.md`'s "Destructors" section for the language-level feature.
This section covers how those two triggers - a plain local/parameter's own
scope exit, and `delete` - actually get lowered.

**Declaration/generation follow constructors' own two-pass split exactly**
(`declareDestructorSignature`/`genDestructorBody`): a destructor lowers to
its own real LLVM function, reusing the identical implicit-first-pointer-
parameter convention a method/constructor already uses - always returns
void, and is named `Struct.destructor` (no arity suffix - a struct declares
at most one). `Generator.dtors` is `ctors`'/`funcs`' counterpart for
destructors, but keyed directly by `*sema.StructInfo` rather than by a
destructor's own `*sema.Symbol`: unlike a constructor, a destructor is never
selected by an arity lookup or any other call-site resolution at all. An
enum's own destructor mirrors this exactly one level over -
`declareEnumDestructorSignature`/`genEnumDestructorBody`, `enum.go` - into
its own parallel `Generator.enumDtors` map, keyed by `*sema.EnumInfo`.

`destructorFuncFor(t sema.Type) (funcEntry, bool)` (`func.go`) is the one
shared dispatch both `pushDestructorEntry` (below) and `delete`'s own
`destructorFuncForPointee` go through to answer "does this type have a
destructor, and if so, which function do I call" regardless of whether `t`
is a struct or an enum, so `unwindDestructorsTo` itself (below) never needs
to know or care which type kind gave a given entry its destructor.

### The destructor stack (`Generator.destructors`)

A destructor call is never inserted "wherever a variable happens to go out
of lexical scope" - this package instead maintains one flat, function-
scoped stack, `Generator.destructors` (reset at the start of every
function/constructor/destructor/lambda body), of every still-in-scope
local/parameter whose own declared type directly declares a destructor
(**not** merely non-copyable via a field/variant - see `pushDestructorEntry`,
the single gate every one of these entries passes through: `genVarDecl`/
`genShortVarDecl` call it right after a local's storage is initialized,
the parameter loops call it right after storing each incoming parameter,
and `bindPatternName` (`enum.go`) calls it for each of a `match` arm's own
fresh bound names).

`unwindDestructorsTo(target)` (`stmt.go`) is the one shared primitive every
real scope-exit trigger uses: it emits a real destructor call - via
`genDestructorCall`, the same implicit-`this`-pointer calling convention a
method call already uses - for every entry above `target`, **in reverse
index order** (`LANGUAGE.md`'s "reverse declaration order" requirement
falls straight out of this), then truncates the stack down to `target`:

- **`genBlock`'s own fall-through case** calls `unwindDestructorsTo(base)`
  right before returning `false` (didn't terminate) - exactly "this block's
  own directly-declared locals, nothing from an enclosing scope".
- **`genReturnStmt`** always unwinds to `0` - a return exits the whole
  function, so every entry on the stack gets destructed - evaluating the
  returned value first, *then* unwinding, *then* actually emitting the
  `ret`.
- **`genBreakStmt`/`genContinueStmt`** unwind to the current loop's own
  `loopCtx.destructorBase` before branching to the break/continue target.
- **`genDeleteStmt`** calls the pointee's destructor directly (see below),
  not through this stack at all - `delete` frees a heap value with no
  local/parameter of its own necessarily involved.

`genBlock` itself, when a statement inside it *does* terminate, deliberately
does nothing further to `Generator.destructors` - whichever nested `return`/
`break`/`continue` already unwound everything relevant, quite possibly
*below* this block's own `base`.

### `genIfStmt`'s then/else save-restore - the one genuinely subtle part

`then`/`else` are **alternate, mutually exclusive continuations from the
same starting point**, not a sequential continuation of each other - only
one of them ever actually executes at runtime, but codegen still has to
*generate both*, sequentially, in the same linear pass. A `return`/`break`/
`continue` inside one branch legitimately pops entries off
`Generator.destructors` that the *other* branch's own codegen must **not**
see as already gone - if it did, the sibling branch's own fall-through
unwind would silently stop emitting a destructor call for a local that, on
its own runtime path, is very much still there.

`genIfStmt` (`stmt.go`) therefore snapshots a real copy of
`Generator.destructors` (`preIf`, not just its length) before generating
`thenBB`, restores that copy immediately afterward, generates `elseBB` from
that identical restored state, and restores the copy once more afterward -
so whatever follows the `if` always sees exactly the pre-`if` state. This
was caught directly by `TestDestructorFiresOnFallThroughReturn`/
`TestDestructorFiresOnBreak` (`src/codegen/destructor_test.go`) failing
before this fix existed - without it, only whichever branch was generated
*first* ever got a real destructor call in the emitted IR, the second
branch's own call having been silently skipped because the stack already
looked "empty" by the time its own fall-through unwind ran.

### `delete p`'s destructor-then-free ordering

`genDeleteStmt` (`stmt.go`) checks `destructorFuncForPointee(operand)` -
`p`'s own pointer type's pointee, the identical question `pushDestructorEntry`
asks about a plain local's declared type (both now share `destructorFuncFor`
- see "Enums" below for the enum-kind half) - and, if it does, calls
`genDestructorCall(ptr, entry.fn, entry.fnType)` **before** the existing
`free` call, not after: the destructor's own body (e.g. reading/nulling a
field of `this`, or itself `delete`-ing a further pointer it owns) needs the
pointee's memory to still be valid when it runs. A pointee type with no
destructor is entirely unaffected.

### `move x`

See `LANGUAGE.md`'s "move" subsection for the language-level feature - sema
(`checkMoveExpr`, `typecheck.go`) does all the real *legality* work (use-
after-move tracking, the if/else/match convergence rule, the loop
restriction). `genMoveExpr` (`expr.go`) itself is trivial: an ordinary load
of the operand's current value, plus `removeDestructorEntry` (coroutine.go)
against its own symbol - exactly what an explicit `delete` already does for
a coroutine handle. Whatever consumes the value pushes its own destructor
entry the same way any other freshly-owned value already does; `move` never
needs its own push.

What `move` is NOT sound-for-free with, though, is `removeDestructorEntry`
removing an entry from *anywhere* in the stack rather than just the top -
every scope-boundary bookkeeping mechanism this package already had assumed
that never happened (only `delete` could remove an entry pre-`move`, and
only ever the operand's own, usually-recently-declared local). Two real,
independently-confirmed bugs came out of this, both fixed as part of this
same round (see DECISIONS.md's dated entries):

- **`genBlock`/a loop's own break-continue base/a match expression's own
  yield base** used a plain `base := len(g.destructors)` integer snapshot.
  Removing an entry from *below* that snapshot (e.g. `move`-ing a local
  declared in an enclosing scope, from inside a nested `if`-branch) shifts
  every later index down by one, silently invalidating it - `unwindDestructorsTo`
  would then unwind too few (or the wrong) entries. Fixed by replacing every
  such integer with a `destructorScope` (a set of symbols, not an index) -
  `unwindDestructorsToScope` recomputes the correct integer target against
  the *current* stack every time, exploiting the invariant that pushes only
  ever append and removals only ever delete in place, so survivors always
  form a prefix.
- **`genIfStmt`/`genMatchStmt`/`genValueMatchStmt`'s own
  `snapshotDestructors`/`restoreDestructors(pre)` pair** blindly restored to
  the pre-construct snapshot once every branch/arm had generated - which
  `move` (unlike `delete`) makes actively unsound: if every reachable branch
  moved away a pre-level entry (sema-legal, since move requires exactly
  this consistency), the blind restore resurrected it anyway, letting it
  fire a second, spurious destructor call later. `mergeBranchDestructors`
  replaces that blind restore: a pre-level entry survives into the merged
  result only if it's still present in *at least one* reachable (non-
  diverging) branch's own final snapshot - a union, not an intersection.
  This is deliberately still permissive enough to preserve `delete`'s own
  existing, already-tested "conditional delete inside just one `if`-branch"
  shape (`TestCoroDeleteInsideIfThenScopeExitIsSafe`): `delete` has no
  "must be consistent across every branch" restriction the way `move` does,
  and relies on exactly this resurrection (protected by `coroDestroyLocalFn`'s
  own nil-guard) to stay safe when only one branch actually deleted the
  handle.
  A related subtlety inside this same fix: inside an EXPRESSION-mode match,
  a `yield` also reports "terminated" from `genMatchArm`/`genBlock`'s own
  point of view (same as return/break/continue), but unlike those it
  branches into this exact construct's own `mergeBB`, not away from it -
  `armReachesMatchMerge` (stmt.go) checks whether `frame.incomingVals` grew
  during that arm's own generation to tell the two apart, since treating a
  yielding arm as "never reaches what follows" incorrectly excluded it from
  the merge's own vote.

### The reassignment-leak fix

`genAssignStmt`'s plain `"="` case used to call `storeValueInto` directly
against the target's own existing address, with no destructor call for
whatever it previously held - a real leak for any non-copyable-typed target
(a struct/enum with its own destructor, or a coroutine handle), independent
of `move` itself: `f := Res(1); f = Res(2)` never destructed `Res(1)`.
`genAssignInto` (`stmt.go`) is the fix, replacing that call: it destructs
the target's current value (`destructOldValueIfOwned`, reusing
`destructorFuncFor`/`genDestructorCall` exactly like `pushDestructorEntry`/
`genDeleteStmt` already do) before the new value overwrites it. Ordering
matters - a composite-literal value is destructed-then-filled directly into
the destination address (`genCompositeLitInto` never reads through its own
destination first, so this is safe), but any other value is evaluated
*before* destructing the old one, since it may itself read through the
target's own old contents (`f = Res(f.id + 1)`) - destructing first could
let the old value's own destructor body corrupt that read.

## Enums

See `LANGUAGE.md`'s "Enums" and "match" sections for the language-level
feature. Implemented in its own file, `src/codegen/enum.go`, mirroring
`maps.go`'s own precedent for a self-contained subsystem rather than
scattering `TypeEnum`-specific cases across `expr.go`/`stmt.go`/`runtime.go`
(each of those three files still gains one small `case sema.TypeEnum` arm
apiece, dispatching straight into `enum.go`).

### Representation: one shared `{i32, ptr}`, never a per-enum named struct

Every enum value, regardless of which enum type or which of its variants is
active, is the identical literal (unnamed) LLVM struct `{i32, ptr}` -
`g.enumValTy`, computed once in `setupTypes` exactly like `dynArrTy`/
`funcValTy`/`stringTy` are - a discriminant (the active variant's own
declaration-order index) plus an opaque payload pointer. The alternative
considered (a named per-enum struct sized to its own largest variant,
`{i32, [N x i8]}`) would need `N` known as a real Go integer at the point
the struct type is built, which this project's `llvm.SizeOf`-based sizing
idiom (a constant expression, resolved by LLVM only once the module is
compiled/JIT'd) can't give without threading a real `llvm.TargetData`
through this package for the first time. The `{i32, ptr}` shape sidesteps
that entirely, and is exactly this project's own already-established idiom
for "a small fixed-size header, real payload lives on the heap/arena,
referenced via `ptr`" (dynamic arrays, first-class functions' capture
context, a map's control block all already follow the identical shape).

The payload is **always arena-allocated** (`genArenaAlloc` - never freed
individually, the same permanent-leak tradeoff the arena already makes
everywhere else), **never a stack address**: an enum value is passed/
returned by value exactly like a struct/array/string elsewhere in this
package, so a constructing function's own stack frame must never be what a
*returned* enum value's payload depends on. Null for a unit variant, which
carries no associated data at all.

This representation is also what makes a recursive/self-referential variant
(`Cons(i32, *List)`) or an enum-of-enum field need **zero** special-casing
at all: a pointer is always just `g.ptrTy` regardless of what it points to,
so a variant's own payload struct type never needs another enum's (or its
own) layout to already be complete.

### `enumLayout` (`enum.go`) - the enum-kind counterpart to `structLayout`

Much smaller than `structLayout`, since the *outer* enum value never needs
its own named LLVM type: `enumLayout` only caches, per `*sema.EnumVariant`,
that variant's own unnamed LLVM payload struct type (`variantPayloadType` -
empty for a unit variant) and each payload field's own `sema.Type` in the
identical order (`variantPayloadTypes`). Built by `declareEnumLayout`, in
**one single pass** - unlike `declareStructType`/`defineStructBody`'s own
two-pass declare-then-define split, an enum never has a forward-reference
problem to split around - run right after every struct body is fully
defined and before any function/constructor/destructor body is generated.

### Construction

Every construction shape - a bare unit-variant reference, a tuple-variant
call, or a struct-variant composite literal - funnels through one shared
`genEnumVariantValue(variant, fieldValues)`: build the payload struct value
in registers (`llvm.Undef` + one `CreateInsertValue` per field), arena-
allocate exactly `llvm.SizeOf(payloadTy)` bytes and store it there (skipped
entirely for a unit variant - `payloadPtr` stays a null constant), then
build the outer `{tag, payloadPtr}` value the identical way.

A bare unit-variant reference (`Shape.Point`) is recognized directly in
`genExpr`'s own `MemberExpr` case, *before* ever falling through to the
generic `genLoad`/`genAddr` path: its own object child names the enum
*type*, not a value with real storage to load from - `sema` never even
type-checks it as one for this shape - so this is exactly the same
`SymFunc`/`SymBuiltinValue` special-casing `genExpr`'s `Ident` case already
does for a bare function reference or `nil`, one node kind over.

A struct-variant composite literal is recognized in `checkCompositeLit`
(sema, upfront, by the type-expr's own `Info.Refs` resolving to a
`SymEnumVariant` symbol) and in `genCompositeLitInto`'s own `TypeEnum` case
- unlike a real struct's identical-looking case (which fills `dst`
field-by-field via GEP, since a struct's own storage *is* `dst` directly),
an enum value is built as a whole aggregate first (its payload arena-
allocated as part of that) and then stored into `dst` in one go.

### Resolution: a variant reference is fully resolved by `Resolve` alone

`EnumName.Variant` (in *any* of its three construction/pattern shapes) needs
no type information at all to resolve - unlike an ordinary struct-value
field/method access, an enum's own variant catalog is fully built by the
time anything references it, exactly like a package's own top-level scope
already is. `sema.Resolve` (not `Check`) therefore resolves every
`EnumName.Variant` reference directly into a real `*sema.Symbol`
(`SymEnumVariant`, carrying `Variant *EnumVariant`/`EnumInfo *EnumInfo`) the
moment it's encountered - `resolveEnumVariantRef`, shared by `resolveExpr`'s
`MemberExpr` case, `resolveTypeMemberExpr`, and `resolvePattern`'s own
`resolveMemberPatternRef`. `sema.Check`'s own construction-checking
functions and codegen's own dispatch (`isEnumVariantCall`, mirroring
`isConstructorCall`) all just read that already-resolved `Info.Refs` entry
back.

### `match` patterns: fresh bindings, references, or plain value expressions

A match arm's own pattern reuses construction's exact AST shapes
(`MemberExpr`/`CallExpr`/`CompositeLit`/a bare `Ident` for `_`) verbatim -
but a pattern's own "arguments"/keyed-element values are **fresh binding
names being declared**, the exact opposite of what `resolveExpr`'s ordinary
`CallExpr`/`CompositeLit` cases assume, whenever the pattern actually turns
out to be an enum-variant one. `Resolve` therefore routes a match arm's
pattern through its own dedicated `resolvePattern`, which decides *which* of
two resolution paths applies purely via a lexical peek
(`patternEnumVariantRef` - does the pattern's own leading `EnumName`
reference actually resolve to a declared enum type):

- **A real enum-variant pattern** - resolved exactly as before this round:
  each binding is declared directly into that arm's own child scope
  (`declarePatternBinding`, the same `Scope.Define`-based mechanism an
  ordinary `ShortVarDecl`'s own name already goes through).
- **Anything else** (new this round - see `LANGUAGE.md`'s "Value matching"
  section) - a bare literal, a variable/constant reference, or any other
  expression shape - is instead routed straight through the ordinary
  `resolveExpr` reference-resolution path, introducing no fresh bindings at
  all.

`sema.Check`'s own `checkMatchArmPattern` mirrors the enum-pattern half of
this split: it resolves the pattern against the matched enum's own
`EnumInfo` (rejecting a nonexistent variant, a variant belonging to some
*other* enum, or a pattern shape that doesn't match that variant's own
declared kind), then seeds each binding's own `Type` **directly** into both
`declType`'s memoization cache and `Info.Types` (`seedPatternBinding`). The
value-pattern half instead goes through the ordinary `checkValueExpr`, then
`checkEqualityOperands` against the subject.

`ast.Tree.IsWildcardMatchArm` centralizes "is this arm the bare wildcard" -
exactly one pattern, an `Ident` whose text is exactly `"_"` - shared by
every pass that needs it, since a bare `Ident` pattern is no longer
automatically the wildcard now that an ordinary identifier is also a legal
value pattern.

### Enum-match exhaustiveness checking (`checkEnumMatchStmt`, `sema/typecheck.go`)

The real, hard compile-time check this feature exists to provide - see
`LANGUAGE.md`'s own "Enum matching" section for the exact rules enforced
(every pattern names a real variant of the *matched* enum, no variant
matched twice, every variant covered or a wildcard present, and exactly one
pattern per arm). Implemented as a single pass over the match's own arms,
building a `map[string]bool` of covered variant names against
`EnumInfo.Order` - deliberately a plain map keyed by name, not a bitset,
since a real enum's own variant count is always small enough that this
never remotely matters for performance. An arm with more than one pattern
is a clean diagnostic here rather than an attempt to unify several
variants' differently-shaped bindings into one shared body - see
`DECISIONS.md`'s dated entry for why that stays a deliberate scope limit.

`checkMatchStmt` itself is now just the dispatcher: it type-checks the
subject once, then routes to `checkEnumMatchStmt` (subject resolved to
`TypeEnum`, auto-dereferencing a pointer subject first) or
`checkValueMatchStmt` (subject one of `TypeI8`/`I16`/`I32`/`I64`/`Bool`/
`String` - after defaulting a bare untyped-constant subject the same way
any other no-declared-type-context expression already does), rejecting
anything else (`f32`/`f64`, a struct/array/pointer/map/func) with a clean
diagnostic.

`isTerminatingStmt` (the same flow-analysis function backing "Missing
return") gained its own `MatchStmt` case (`matchStmtTerminates`): a fully-
exhaustive `match` whose every arm terminates is itself terminating,
mirroring an `if`/`else`'s identical two-branches-generalized-to-N rule.
This deliberately **recomputes** the same exhaustiveness fact
`checkMatchStmt` itself already validated rather than caching it anywhere -
`isTerminatingStmt` is a pure function of an already-checked tree, with no
`*checker` receiver to memoize onto, the same no-caching precedent
`forHasOwnBreak` already sets. Generalized alongside the enum-vs-value
split above: for a value-match subject, termination reduces to "every arm
terminates" AND a wildcard genuinely being present, re-derived directly
rather than blindly trusting `checkValueMatchStmt`'s own guarantee.

### Value-match type checking (`checkValueMatchStmt`, `sema/typecheck.go`)

Every arm's every pattern is checked as an ordinary value expression
(`checkValueExpr`), then run through `checkEqualityOperands` - the *exact*
function an ordinary `==` operand pair already uses - against the subject:
untyped-literal defaulting against the subject's own concrete type, then
requiring the same type, with no new type-resolution logic invented for
this feature at all. **A wildcard `_` arm is mandatory** - unlike the enum
path, there is no closed variant set to exhaustively check an unbounded
domain like `int`/`string` against, so its absence is a clean diagnostic.
Duplicate-arm detection is a best-effort nicety, not a blocking guarantee:
`checkDuplicateValuePattern` tracks a `map[literalPatternKey]ast.NodeIndex`
keyed by (node kind, raw source text) - only a bare `NumberLit`/`StringLit`/
`BoolLit` pattern is ever compared this way; anything computed is silently
skipped, since its actual value isn't known until runtime.

### `match` codegen: two lowering strategies, dispatched by subject type

`genMatchStmt` (`enum.go`) type-checks the subject's own type first
(mirroring `checkMatchStmt`'s identical dispatch one layer up) and routes to
one of two genuinely different lowering functions, kept deliberately
separate rather than tangled into one - a value pattern's own runtime
equality simply isn't a compile-time-constant discriminant the way an enum
variant's is, so it needs a different lowering shape entirely:

**Enum subject - a real LLVM `switch`, not a chain of `br` (unchanged from
before this round).** Loads the subject's own discriminant (auto-
dereferencing a pointer-typed subject first) and lowers to a genuine LLVM
`switch` instruction (`CreateSwitch`/`AddCase`), one case per variant the
match covers. Each case's own block extracts/loads the payload into its
bound local names (if any - `bindMatchArmPattern`/`bindPatternName`, which
allocate a fresh local slot per binding exactly like an ordinary `:=`
declaration would, registering it into `g.locals`), runs the arm's own
body, then branches to the match's own single merge block (unless the
arm's own body already terminated).

The wildcard arm (if present) becomes the `switch`'s own default
destination. **If no wildcard exists** - `sema.checkEnumMatchStmt` already
guarantees every variant is then explicitly covered - the default
destination is a real `unreachable` block, matching this project's own
established "genuinely impossible per sema's own guarantee, so
`unreachable` documents it directly in the IR" convention (see
`emitFallbackTerminator`, `func.go`) rather than a silently-do-nothing
fallthrough. The match's own merge block gets the identical treatment when
every arm always terminates, since LLVM still requires every basic block
that exists at all to end in a real terminator.

**Value subject - a short-circuit runtime comparison chain
(`genValueMatchStmt`, new this round).** The subject is evaluated once,
then each arm (in source order, skipping the mandatory wildcard - tested
last, regardless of where it's actually written) becomes a chain of
`CondBr`s: for each of that arm's own patterns, evaluate it and compare
against the already-evaluated subject via `genValueEqual` - the *exact
same* scalar-equality codegen an ordinary `==` operator already uses - not
reinvented here - branching into that arm's own shared body block on the
first match, or continuing to the next pattern (or the next arm) otherwise.
The mandatory wildcard's own body becomes the unconditional final fallback,
always present, so there is no `unreachable`-default case to build here
the way the enum path needs one. Applies the identical
`snapshotDestructors`/`restoreDestructors` discipline the enum path (and
`genIfStmt`) already use, at every arm and once more after the whole
statement.

### `match` as an expression: a shared `frame`, one `phi` at the merge block

`genMatchStmt`/`genValueMatchStmt` both take a `frame *matchExprCodegenCtx`
parameter - `nil` for an ordinary statement-position match, non-nil only
when `genMatchExpr` (`enum.go`) is lowering an expression-position match
instead. This is the *only* thing `frame` changes about either function's
own switch/comparison-chain construction - both lowering strategies above
are reused completely unchanged.

`genMatchExpr` pushes a `matchExprCodegenCtx` frame
(`Generator.matchExprStack`, mirroring `loopStack`/`loopCtx` one construct
over) carrying: `destructorBase` (the match expression's own entry depth,
for `yield`'s own unwind), a shared `mergeBB`, and `incomingVals`/
`incomingBlocks` accumulators. Every arm's own `yield` (`genYieldStmt`,
`stmt.go`) - however deeply nested inside an `if`/`for` - evaluates its
expression, `unwindDestructorsTo(frame.destructorBase)`, appends `(value,
currentBlock)` to the frame's accumulators, and branches to `mergeBB` -
terminating that block exactly like `return`/`break`/`continue` already do.
Once every arm has finished generating, `genMatchExpr` builds one
`CreatePHI` at `mergeBB` and a single batched
`AddIncoming(frame.incomingVals, frame.incomingBlocks)` call - matching
every other `phi` call site in this package, none of which call
`AddIncoming` incrementally either.

The one place both lowering functions' own existing "mark `mergeBB`
unreachable when every arm terminates" logic needs to know about `frame`:
when `frame != nil`, `mergeBB` is genuinely reachable regardless of whether
the statement-mode `allTerminated` bookkeeping would otherwise call it dead
code - so that `unreachable`-marking is gated on `frame == nil` too.

A bare-expression arm (`pattern => expr`, no `yield` in the source at all)
is parsed as an implicit `{ yield expr }` - by the time codegen sees it,
every arm body is the identical shape, so `genYieldStmt` is the only new
codegen entry point this feature needed at all.

### `==`/`!=` and `print()`: a real runtime discriminant dispatch

Unlike a struct (whose every field is always present, so `genValueEqual`/
`genPrintStructValue` can simply walk every field unconditionally at
codegen time), an enum's active variant isn't known until the program
actually runs - both `genEnumEqual` and `genPrintEnumValue` therefore build
their own small runtime `switch` on the discriminant, the identical
`CreateSwitch`/`AddCase` shape `genMatchStmt` uses one section up:

- **`genEnumEqual`** (`genValueEqual`'s own `TypeEnum` case) first compares
  the two operands' discriminants directly - only when they match does
  control reach a runtime `switch` on that shared discriminant, each case
  recursively comparing the active variant's own payload
  (`genVariantPayloadEqual` - trivially `true` for a unit variant, otherwise
  loading both sides' payload structs and recursing field-by-field through
  `genValueEqual` exactly like a struct's own fields already do). A
  discriminant mismatch short-circuits straight to `false` via a `PHI` at
  the shared merge block, without ever touching either side's payload.
- **`genPrintEnumValue`** switches directly on the discriminant, each case
  rendering that variant's own name followed by its associated data, if
  any: parens for a tuple variant (`Circle(5.000000)`) or braces for a
  struct variant (`Triangle{3.000000 4.000000}` - deliberately reusing
  `genPrintStructValue`'s own established "field values only, no names"
  convention). A unit variant prints as its bare name alone, no
  punctuation at all.

### Destructors

An enum's own `destructor()` (fires once, regardless of which variant is
actually active) reuses the exact same `declareDestructorSignature`/
`genDestructorBody`-style two-pass split a struct's own destructor already
has - `declareEnumDestructorSignature`/`genEnumDestructorBody` (`enum.go`) -
into its own parallel `Generator.enumDtors` map. No per-variant dispatch of
any kind is needed here: the destructor's own implicit `this` parameter is
simply a pointer to the shared `g.enumValTy`, exactly like a struct
receiver is a pointer to that struct's own named type.

## Pointers: real `*T`, `&`/`*`, `new`/`delete`, auto-deref, and `nil`

See `LANGUAGE.md`'s "Pointers" section for the language-level feature. Every
`sema.TypePointer` lowers to `g.ptrTy` - the same single opaque `ptr` LLVM
type this package already uses everywhere else a pointer-shaped value is
needed - a pointer's pointee type never affects its own LLVM
representation, only what a dereference/GEP through it targets.

**`&x`/`*p`** are both `UnaryExpr` nodes distinguished by operator text,
same as unary `-`/`!` (`genUnaryExpr`, `src/codegen/expr.go`) - handled
before either falls through to the shared "evaluate the operand as an
rvalue" path:

- **`&x`** lowers to `genAddr(x)` directly - the exact same address
  computation an assignment target, `++`/`--`, or a method-call receiver
  already uses for the same expression shapes - so `&` never spills its
  operand into a fresh temporary. `genAddr` gained one new case for this:
  `UnaryExpr("*")` as an lvalue (`*p = v`, or `&*p`) evaluates its own
  operand (`p`) as a plain rvalue - the address a dereference reads/writes
  through *is* `p`'s own value, not `p`'s own address.
- **`*p`** as a value lowers to evaluating `p` (`genExpr`) and loading
  through the result - `CreateLoad(llvmType(pointee), ptrValue, "")`.

**`new T(args)`/`new T{...}`** (`genNewExpr`, `src/codegen/expr.go`)
`malloc`s exactly `sizeof(T)` bytes (`llvm.SizeOf`) and initializes that
address in place, reusing the *exact same* lowering an ordinary stack-/
field-allocated construction already uses - just pointed at a different
destination:

- A composite-literal inner (`new Point{1, 2}`) calls `genCompositeLitInto`
  directly against the malloc'd pointer.
- A constructor-call inner (`new Point(1, 2)`) calls a new
  `genConstructorCallInto(dst, calleeNode, argNodes)` helper - factored
  *out* of `genConstructorCall` itself specifically so `genNewExpr` can
  reuse the identical this-pointer-as-hidden-first-argument calling
  convention against a malloc'd `dst` instead.

Unlike `genConstructorCall`/a plain `CompositeLit`'s own `genExpr` case
(both of which load and return the constructed *value*), `genNewExpr`
returns the malloc'd pointer directly, never loading through it - the
entire point of `new` is a pointer that outlives the current stack frame.

**`delete p`** (`genDeleteStmt`, `src/codegen/stmt.go`) is a direct call to
libc `free` against `p`'s own evaluated pointer value. **This is a real,
separate heap from the bump-allocator arena** - `new` mallocs its own
individually-freeable block per call, never asking the arena for space, and
`delete` frees exactly that block via a real `free`, never touching the
arena's own bump cursor. `delete`ing a sub-block carved out of a shared
arena chunk another allocation still lives in would be a real bug, so
`new`/`delete` simply stay on their own separate, ordinary malloc/free heap.
The arena itself still has no per-allocation free at all (see
`BLOCKERS.md`); `new`/`delete` are a new, independent code path, not a fix
to the arena's own leak.

**Auto-deref for member access** (`genReceiverAddr`, `src/codegen/expr.go`) -
shared by `genAddr`'s `MemberExpr` case and `genMethodCall`: when the object
expression's own `sema.Type` is `TypePointer`, its *value* is evaluated
directly (`genExpr`, the pointer itself) rather than its *address* (the
address of whatever variable happens to be holding the pointer) -
`p.field`/`p.method(...)` on a `*Point` therefore addresses the exact same
heap struct `(*p).field`/`(*p).method(...)` would, with no copy in between.

**`nil`** (`sema.SymBuiltinValue`) has no storage of its own to load from -
`genExpr`'s `Ident` case special-cases it the way it already special-cases
a bare, uncalled function reference, lowering straight to
`llvm.ConstNull(g.ptrTy)` regardless of which concrete `*T` sema resolved
it to.

## Structs/arrays/strings are passed and returned as real LLVM aggregate types, not manual `sret`/by-ref tricks

A struct, fixed-size array, or string value used as a plain function
parameter or return type lowers to LLVM's own aggregate/array type directly
(e.g. a two-field struct becomes the LLVM struct type `{i32, i32}` used
as-is for a parameter or return type) - LLVM's own backend ABI lowering
handles whatever register/hidden-pointer convention the target actually
needs, transparently. This works because every caller and callee in a given
module is generated by this same package; there's no need to match an
external C ABI by hand.

## Go-style multi-return values

See `LANGUAGE.md`'s "Functions" section for the language-level feature.
This needed **no new ABI mechanism at all** - it reuses the "Structs/
arrays/strings..." convention immediately above verbatim, just for a value
that's never a *named* struct type.

**The `Type` representation.** `sema.TypeMultiReturn` reuses `Type`'s own
`Params []Type` field (the same field `TypeFunc` already uses) to hold the
N component types. A multi-return function's `funcSignature.Return` is
simply this `Type` directly.

**The LLVM type.** `llvmType`'s `TypeMultiReturn` case (`src/codegen/types.go`)
builds an anonymous LLVM struct type `{T1, T2, ...}` from the component
types - exactly the shape a real, named struct's own `llvmType` case
already builds, just anonymous, and computed fresh every time rather than
cached in `setupTypes`, since every multi-return function's own component
types differ.

**Declaring the function itself.** `declareFuncSignature`/`genFuncBody`
needed no changes beyond what already existed: a `FuncDecl`'s return-type
node is looked up in `info.Types` exactly the same way for every
return-type shape. The one new piece of state is `funcCtx.retType` - the
function currently being generated's own declared return type, needed by
`return`'s own multi-value lowering below (a `MultiValueExpr` node carries
no `Type` of its own to read out of `info.Types`).

**`return a, b, ...`** (`genMultiValueExpr`, `src/codegen/stmt.go`): builds
the aggregate value via `llvm.Undef(retTy)` plus one `CreateInsertValue` per
returned expression, evaluated left to right - the same runtime-aggregate-
construction approach `genFuncLit`'s own closure value already uses for its
`{fnPtr, ctxPtr}` fat pointer whenever `ctxPtr` is a genuine runtime value.
`genReturnStmt` dispatches to this the same way it already dispatches on
every other node-kind-specific shape - a plain single-value `return expr`
is completely unchanged.

**`a, b := f(...)`** (`genMultiShortVarDecl`, `stmt.go`): calls `f` exactly
once via the ordinary `genExpr` path, then allocates each name's own storage
(`allocLocalSlot`) and fills it via `CreateExtractValue` against that one
aggregate result, one field index per name.

**`a, b = f(...)`** (`genMultiAssignStmt`, `stmt.go`): the assignment-form
counterpart - every target's own address is resolved via the same `genAddr`
a single-target `AssignStmt` already uses, computed before the call itself
runs, then filled via `CreateExtractValue` the same way
`genMultiShortVarDecl` does.

**Component types genuinely differing in width/kind** need no special
handling anywhere - `CreateInsertValue`/`CreateExtractValue` already
operate on a struct's field index, not a uniform element type the way an
array's own GEP does, so a mixed-shape aggregate was never actually a
special case to guard against; `TestMultiReturnMixedWidthTypes`/
`TestMultiReturnFloatAndStringTypes` (`src/codegen/multireturn_test.go`)
exercise exactly this, JIT-executed.

See `examples/multireturn/multireturn.llx` for the worked dogfooding demo
(a `divide`/`find` pair mirroring Go's own `v, ok := m[k]` idiom), exercised
end to end - JIT and AOT alike - by `cmd/llvmc/main_test.go`'s
`TestBinary_MultiReturnExample`/`TestBinary_AOT_MultiReturn`.

## Terminator safety (LLVM requires every basic block to end in one)

`sema.Check` now runs a full "does every path return" flow analysis and
rejects any function declaring a return type whose body isn't guaranteed to
return. Codegen still keeps a fallback for whenever a function's lowered
body falls off the end without already ending in a terminator, purely as a
defensive backstop (a validated tree should never actually need it - see
`emitFallbackTerminator` in `src/codegen/func.go`):

- `main`, and any function declaring no return type at all, get a real,
  correct terminator (`ret i32 0` / `ret void` respectively) - falling off
  the end of a void function is legitimate, and `main` must always hand a
  real exit code back to its OS caller.
- Any other non-void function gets `unreachable` instead - reaching this
  given sema's guarantee should be impossible; `unreachable` records that
  assumption directly in the IR rather than inventing a fake return value.

Within a single block, a statement that itself terminates (`return`,
`break`, `continue`, or an `if`/`else` where both branches terminate) simply
stops that block's remaining statements from being generated at all - there
is no `goto`/labels in this language, so nothing could ever jump back into
that dead code.

## External functions (FFI): declare-only, zero JIT-side changes

See `LANGUAGE.md`'s "External functions (FFI)" section for the language-level
feature.

**A brand-new, deliberately separate AST node kind** (`ExternFuncDecl`,
`[name, paramList, returnType]`), not a nullable-body variant of `FuncDecl` -
see `DECISIONS.md`'s dated entry for the full reasoning (`FuncDecl`'s own
body-always-present invariant is depended on unconditionally by a large
amount of existing code, so a nullable-body `FuncDecl` would ripple a
defensive nil-check through all of that).

**Sema deliberately reuses `sema.SymFunc`** as the declared symbol's kind
(`resolve.go`'s `declareExternFunc`), not a new `SymbolKind` - an
extern-backed function is indistinguishable from an ordinary one at every
*call site*; the only place anything ever needs to tell the two apart is by
checking `tree.Nodes[sym.Decl].Kind` directly (`funcSigForDecl`, which
dispatches to `computeFuncSig` or `computeExternFuncSig` depending on which
node shape it finds).

**Codegen's lowering is, deliberately, almost nothing**:

- `declareExternFuncSignature` (`src/codegen/func.go`) mirrors
  `declareFuncSignature`'s param/return-type-to-LLVM-type translation, minus
  the receiver and `isMain` special-casing (neither ever applies). It calls
  `llvm.AddFunction(g.mod, name, fnType)` with **default linkage**, not
  private - exactly like `printf`/`malloc`/`memcpy`/`memcmp` in
  `runtime.go`.
- It stores into the **exact same `Generator.funcs` map** `declareFuncSignature`
  does, keyed by the identical `*sema.Symbol` - every call-site needed
  **zero changes** to correctly treat a direct call to an extern function
  exactly like a direct call to an ordinary one.
- **There is no corresponding "generate body" pass at all** - nothing calls
  `genFuncBody` for an `ExternFuncDecl`, since it has no body, ever.
  `genPackage`'s own signature-declaration pass walks every
  `ExternFuncDecl` alongside every `FuncDecl`, but the later body-generation
  pass only ever walks `FuncDecl`s.

**Zero JIT/runtime-side changes were needed for this feature at all.**
`cmd/llvmc/main.go`'s `bindMinGWMainThunk` already registers
`llvm.NewDynamicLibrarySearchGeneratorForProcess(jit.GlobalPrefix())` on the
JIT's `MainJITDylib()` (predating this feature, for an unrelated reason) -
any symbol already loaded into the host process (every kernel32.dll export
on Windows, libc's own exports via the mingw64 runtime) resolves
automatically the moment an extern func's `declare`-only LLVM function is
looked up and called. An extern func declared here but never actually
present in the host process at JIT-execution time fails at `Lookup`/call
time with an ordinary "symbol not found" error, not a compile-time one - the
same class of failure a real statically-linked program would get from its
own linker instead, just moved to run time because this project's execution
model is JIT, not link-then-run.

Mirrors what its type-restriction diagnostic (`checkExternType`) already
stops before this: a `string`/dynamic-array/function-typed parameter or
return type (or a struct containing one) is a sema-layer error, not a
codegen-layer one - codegen never has to consider any of those unsupported
shapes reaching an `ExternFuncDecl`'s `llvmType` translation. `cstring` (see
LANGUAGE.md's "The `cstring` type" section) needed no codegen change of its
own here either - `llvmType(TypeCString)` is just `g.ptrTy`, so a `cstring`
parameter/return lowers exactly like a pointer one already did.

**Struct-by-value FFI (`src/codegen/ffi.go`): a real Windows x64 ABI
coercion, not just `g.llvmType`.** A struct sema now accepts on an extern
signature (`isFFISafeType`) can't just be declared/called with its own raw
LLVM struct type the way `declareFuncSignature`'s intra-language path
already does - verified empirically, not assumed: LLVM's default aggregate-
argument lowering flattens a direct (uncoerced) struct parameter into one
independent register/stack slot *per field*, silently corrupting a real
C callee's second field onward, since the real ABI (Windows x64) instead
requires either "coerce the whole struct to a same-size integer" (exactly
1, 2, 4, or 8 bytes) or "pass by reference" (every other size) - see
DECISIONS.md's dated entry for the exact failure and citation. `abiSizeAlign`
computes a struct's real byte size directly from its field types (not via
LLVM's own `TargetData`, which doesn't exist yet at codegen time - the
module's `DataLayout` is only pinned afterward, in
`compiler.finishPipeline`); `externParamType`/`externReturnType` use that to
pick the real declared LLVM type per position (a coerced integer, or `ptr`
with an `sret` attribute for an indirect return); `genFuncCall`'s
`coerceExternArg`/`bitcastThroughMemory` then adapt each natural struct
value to/from that declared shape at every call site, via a temp alloca (LLVM
has no direct struct↔integer bitcast in SSA form). This is genFuncCall-only:
an intra-language struct-by-value call never diverges (same backend, same
internally-consistent flattening on both call and callee sides), and a bare
reference to a struct-by-value extern func called *indirectly* (through
`genFuncThunk`/`genIndirectCallValue`) isn't covered - out of scope for this
round, since LANGUAGE.md's only documented extern-func calling form is
direct.

**`strlenExtern` (runtime.go): one shared, lazily resolved "strlen"
declaration.** Both the cstring->string conversion (`genCStringToString`)
and the `args()` builtin's own argv marshaling (`buildArgsInitFn`, args.go)
need a real libc `strlen` call. Neither declares its own local `llvm.
AddFunction(g.mod, "strlen", ...)` - a user's own `extern func strlen(...)`
declaring the identical symbol name first would otherwise collide, and LLVM
silently renames the second same-named declaration (e.g. to `strlen.1`)
rather than erroring, which the JIT/linker can never actually resolve.
`strlenExtern` looks up `g.mod.NamedFunction("strlen")` first, reusing
whatever it finds (the user's own extern, or an earlier call to this same
method), and only calls `AddFunction` if genuinely nothing named "strlen"
exists yet in this module - caching the result in `Generator.strlenFn` so
this lookup happens at most once per module regardless of how many call
sites need it.

## `cfunc`: a bare pointer, no fat-pointer/thunk machinery at all

See `LANGUAGE.md`'s "External functions (FFI)" section's own `cfunc`
subsection for the language-level feature; this is its lowering.

**`sema.TypeCFunc` is a distinct `TypeKind`, sharing `TypeFunc`'s own
`Params`/`Return` fields** (`sema/types.go`) - deliberately not `TypeFunc`
plus a flag: every switch that already branches on `Kind` (`Equal`,
`String`, `llvmType`, `isFFISafeType`, `funcSigForCall`, `genCallExpr`'s own
dispatch) gets its own explicit `TypeCFunc` case rather than a conditional
buried inside the `TypeFunc` one.

**`llvmType(TypeCFunc)` is `g.ptrTy`** (`src/codegen/types.go`) - not
`g.funcValTy` (the `{ptr, ptr}` fat pointer `TypeFunc` uses) - there is no
`ctxPtr` slot to make room for at all, matching a real C function pointer's
own single-word representation exactly.

**The func-to-cfunc conversion (`checkFuncToCFuncConversion`,
`sema/typecheck.go`) is a sema-layer decision; codegen just reads its
result off `info.Types`.** Once sema retypes a direct top-level
`FuncDecl`/`ExternFuncDecl` reference's own node to `TypeCFunc`,
`genExpr`'s `Ident`/`SymFunc` case (`src/codegen/expr.go`) checks that
retyped `info.Types` entry and returns `g.funcs[sym].fn` - the function's
own real, already-declared address - directly, skipping
`genFuncValue`/`genFuncThunk` entirely: no `.thunk` adapter is ever
synthesized for a `cfunc` conversion, since there's no `ctxPtr` parameter a
thunk would need to insert/strip in the first place.

**Calling a `cfunc` value (`isCFuncCall`/`genCFuncCall`, `expr.go`) mirrors
`genFuncCall`'s own extern-func ABI coercion, not `genIndirectCall`'s
fat-pointer extraction.** `genCallExpr`'s dispatch checks `isCFuncCall`
(the callee's `info.Types` `Kind == TypeCFunc`) right after
`isDirectFuncCall` - a `cfunc`-typed callee is always a value (a variable,
parameter, struct field, or converted argument), so `genCFuncCall`
evaluates it as an ordinary expression (already the bare pointer, per
`llvmType` above) and calls straight through it, with no `ctxPtr` argument
prepended at all. Parameter/return types go through the identical
`externParamType`/`externReturnType`/`coerceExternArg`/
`bitcastThroughMemory` helpers (`ffi.go`) a direct extern-func call already
uses, applied fresh at the call site rather than read off a memoized
`funcEntry` (there is no `funcEntry` for an arbitrary `cfunc`-typed
*value* - only a real declared symbol has one) - a struct-by-value
parameter/return crossing a `cfunc` call gets the exact same Windows x64
coercion `genFuncCall` already applies to a direct extern call.

**Why an ordinary (non-extern) `FuncDecl` with a struct-by-value
parameter/return can't convert to `cfunc`
(`checkFuncToCFuncConversion`'s own guard, sema/typecheck.go).** An extern
func's own real LLVM signature is *already* built with `ffi.go`'s ABI
coercion (`declareExternFuncSignature`), so it agrees with `genCFuncCall`'s
identical coercion at the call site automatically. An ordinary `FuncDecl`'s
real signature (`declareFuncSignature`) instead uses this compiler's own
internal, uncoerced struct-passing convention (LLVM's default aggregate
flattening, consistent between an intra-language caller and callee, per
this file's own struct-by-value FFI section above) - calling *that* real
function through a `cfunc`-shaped, ABI-coerced call site would silently
disagree with its actual parameter/return representation. Sema rejects the
conversion outright rather than have codegen paper over a real ABI
mismatch.

## The `args()` builtin, concretely

See `LANGUAGE.md`'s "The `args()` builtin" section for the language-level
feature. `src/codegen/args.go` is this feature's own dedicated file,
mirroring `globalinit.go`'s "one feature, one file" precedent.

**Storage: one private, always-present global, populated once.**
`setupArgsGlobal` declares `llvm_lang.args` - a private, zero-initialized
`{ptr, i32, i32}` (`g.dynArrTy`) global - unconditionally, for every module,
regardless of whether the compiled program ever actually calls `args()`
anywhere: it's cheap and entirely self-contained. `genArgsCall` is just a
load of this global's current value - no per-call marshaling work at all.
It also sets `Generator.argsUsed`, read once by `genCtors` (see below)
after every function body in the whole program has been generated.

**Real argc/argv, without touching `main`'s own signature.** The obvious
design - give `main` a real `(argc, argv)` parameter pair - was deliberately
rejected: `main`'s LLVM signature is looked up and called with **zero**
arguments by both `cmd/llvmc`'s `jitRunMain` and dozens of this package's
own `jm.runInt32(t, "main")` test call sites, every one a raw
`syscall.SyscallN` call that would suddenly need to pass two real, meaningful
register arguments instead of none. Instead, `buildArgsInitFn` reads two
plain **extern globals**, `__argc` (`i32`) and `__argv` (`ptr`) - the exact
same well-established MSVCRT/mingw64 C-runtime extension a real,
hand-written C/C++ program on this platform already relies on, populated by
the CRT's own startup sequence before `@llvm.global_ctors` or `main` itself
ever run - so `main`'s own signature and every existing call site needed
**zero** changes.

**Marshaling**: `buildArgsInitFn` builds a small, private, parameterless
function (`llvm_lang.args_init`) whose body: loads `__argc`/`__argv`, asks
the arena allocator (`genArenaAllocElems`) for a buffer of `argc`
`{ptr, i32}` string headers, then a real runtime loop over `0..argc`: for
each index, load `argv[i]` (a `char*`), call a plain libc `strlen` extern
(the same "declare a libc extern, call it directly" convention `malloc`/
`memcpy`/`memcmp`/`memset` already use, rather than hand-rolling a byte-
scanning loop as generated IR) to get its length, build the `{ptr, i32}`
header, and store it into the backing buffer at that index. The final
`{buf, argc, argc}` value is stored into `llvm_lang.args` once the loop
completes.

**Registered into `@llvm.global_ctors`, and *only* when actually needed.**
`genCtors` (`globalinit.go`) now runs *after* every function/constructor/
destructor body has been generated, not before: it needs to know whether
`g.argsUsed` ended up true, which `genArgsCall` only sets while generating
some function's body. `buildArgsInitFn` (and its own `__argc`/`__argv`/
`strlen` externs) is built - and registered into `@llvm.global_ctors`, at a
lower priority number than `llvm_lang.global_init` so it runs *first* - only
when `g.argsUsed` is true. A program that never calls `args()` anywhere
gets none of this machinery beyond the always-present `llvm_lang.args`
global itself. This is not just a minor optimization: `__argc`/`__argv` are
real external symbols this package has no control over the resolvability
of under JIT execution (unlike `malloc`/`printf`/`memcpy`, already proven
resolvable) - keeping them out of every other program's module entirely
means the vast majority of existing/future programs carry zero new
external-symbol risk from this feature's mere existence. See
`TestArgsUnusedProgramHasNoArgsMachinery`/`TestArgsUsedProgramHasArgsMachinery`
(`src/codegen/args_test.go`) for this asserted directly against the
generated IR text.

**The JIT-execution fallback: an empty slice, by deliberate design, not an
oversight.** `cmd/llvmc`'s `jitRunMain` never looks up or calls
`llvm_lang.args_init` - unlike `llvm_lang.global_init`, which it explicitly
does. So under JIT execution, `llvm_lang.args` simply stays at its
zero-initialized value for the whole run: `args()` returns a real, valid,
but always-empty `[]string` every time a program is JIT-executed via `llvmc
program.llx`, regardless of any trailing arguments typed after the path -
`llvmc` does not capture or forward trailing positional arguments at all
this round (a trailing `foo`/`bar` after the path is a usage error, not
something forwarded to the JIT'd program). See `DECISIONS.md`'s dated
"args() builtin" entry for why this specific fallback was chosen, and
`TestArgsCallUnderJITReturnsEmptySlice`/`TestBinary_AOT_Args` for this
contrasted directly against a real AOT-compiled binary's own genuinely
marshaled argv - the same program, same source, deliberately different (and
clearly documented) behavior depending on which of the two ways it's
actually run.

**Why `bindMinGWMainThunk` also binds `__argc`/`__argv` under JIT, if
`args_init` is never called.** LLJIT's default per-module materialization
means merely looking up (and JIT-compiling) *any* symbol in a module that
happens to contain `llvm_lang.args_init` could, in principle, need every
symbol *that function* references to already resolve to something - even
though this driver deliberately never calls it. `bindMinGWMainThunk` binds
both to harmless, always-valid process-local memory via the identical
`AbsoluteSymbols` mechanism already used for the unrelated `__main`
MinGW/GCC ABI quirk - removing any uncertainty up front rather than relying
on an assumption about exactly how LLJIT partitions a module for
compilation.

## Multi-file packages: one shared Module per package

See `LANGUAGE.md`'s "Multi-file packages" section for the language-level
model. `GeneratePackage` (`src/codegen/codegen.go`) is the multi-file
counterpart to `Generate` (now a thin wrapper: `Generate(tree, info, name)`
is exactly `GeneratePackage([]*ast.Tree{tree}, map[*ast.Tree]*sema.Info{tree:
info}, name)`): it takes every file's `*ast.Tree` plus its own
already-resolved/checked `*sema.Info` and lowers all of them into **one
shared `llvm.Module`** - not one module per file with cross-module linking,
which this package has no need for. See `DECISIONS.md` for this choice
recorded as a dated decision.

**Why no cross-file plumbing was needed inside a single function body.**
Every codegen-internal lookup that resolves a *use* of a declared symbol -
`Generator.funcs` (a call's callee), `Generator.globals` (a global var
reference), `Generator.structLayouts` (a struct's field GEP indices/LLVM
type) - is keyed by `*sema.Symbol`/`*sema.StructInfo` pointer identity, not
by `ast.NodeIndex`. A `NodeIndex` is only meaningful relative to the one
`*ast.Tree` it came from - two unrelated declarations in two different files
can share the same NodeIndex value - but a `*sema.Symbol`/`*sema.StructInfo`
pointer is already globally unique the moment it's allocated, so keying on
it sidesteps the cross-tree ambiguity entirely. `funcs` used to be keyed by
the FuncDecl's own `NodeIndex` before this round; that's the one lookup
this round had to change - every other map was already keyed correctly.

Because of that, `genPackage` (the multi-file counterpart to the old
`genFile`) never needs to switch "which file's nodes am I currently
reading" *in the middle of* lowering one declaration's body: every pass
only ever reads nodes that are children of the one declaration currently
being processed - always in the same file - and a cross-file *reference*
inside a function body is resolved purely through the pointer-keyed maps
above, which are already fully populated for the *whole package* by the
time any function body is generated (every file's structs are declared
before any file's globals; every file's globals before any file's function
signatures; every file's function signatures before any file's function
bodies). `Generator.tree`/`Generator.info` still switch per file, but only
at the top of each pass's per-file loop, never mid-body.

## Imports: still one shared Module, now for the whole program

See `LANGUAGE.md`'s "Imports" section for the language-level model. Nothing
above changes at all: `GeneratePackage` is simply called with *every
package's* trees/infos in the whole program flattened into one
`[]*ast.Tree`/`map[*ast.Tree]*sema.Info` (built by `src/compiler`'s
`CompileProgram` from `loader.LoadProgram`'s already-resolved import graph,
via `sema.ResolveProgram`/`CheckProgram`) - there is still only ever **one
shared `llvm.Module` for the entire program**, never one Module per package
linked together, for exactly the same reason multi-file support already
gave for one Module per *file*: every `*sema.Symbol`/`*sema.StructInfo`
lookup is already keyed by pointer identity, not by which tree - let alone
which package - originally declared it. This was verified, not assumed:
`genPackage`'s own five passes need no changes at all to correctly cover a
multi-package program, since they already iterate "every tree passed in"
with no notion of package grouping.

A package-qualified call (`mathutils.Add(...)`) is recognized in codegen
(`isPackageQualifiedCall`) the same way sema recognizes it and lowered as a
**plain direct call** - `genFuncCall`, the exact same lowering an ordinary
same-package free-function call already uses - since a package-qualified
function call has no receiver to compute.

**Diamond dependencies are deduped by directory identity**, not re-lowered
per import edge: `loader.LoadProgram` loads a given package directory
exactly once regardless of how many other packages import it, so its trees
appear exactly once in the flattened list `GeneratePackage` receives. There
is no separate-compilation/linking concept for this project to get right
here at all (see `DECISIONS.md`).

# `src/compiler`: pipeline orchestration

Sits directly above `src/loader` in this project's own layering: `loader`
owns "given a path, discover/parse/resolve the file/package/program
structure"; `compiler` is the next layer up - "given that loaded structure,
actually run it through the semantic/codegen pipeline". It exposes exactly
two entry points:

- `CompilePackage(files []loader.SourceFile, optimize bool) *Result` - the
  flat-file-list case: `lexer.NewFile` -> `parser.ParseFile` per file, then
  `sema.ResolvePackage` -> the shared tail below. `treePackage` is nil going
  into `sema.CheckProgram` - a single, import-less package has no
  cross-package export enforcement to do at all.
- `CompileProgram(prog *loader.Program, optimize bool) *Result` - the real,
  potentially-multi-package case: every package in `prog.Order` (already in
  dependency order) becomes one `sema.PackageUnit`, and every package's
  trees are flattened into one slice, driven through
  `sema.ResolveProgram` -> the same shared tail.

`optimize` is a plain, explicit `bool` parameter on both entry points (this
project's own established style over a hidden default or a functional-
options pattern) - see the "Optimization pipeline" section below for what
it actually does and `cmd/llvmc`'s `-no-opt` flag for the one real caller
that ever passes `false`.

Both funnel into one unexported tail (`finishPipeline`) that mirrors this
file's own `GeneratePackage`/multi-file writeup exactly: `sema.CheckProgram`
-> `codegen.GeneratePackage` -> LLVM's own module verifier -> building the
host target machine -> (when `optimize` is true) running the optimization
pipeline, stopping at the first stage that reports an error-severity
diagnostic in any file. A `Result` carries every tree, every tree's own
merged diagnostics (`Diags`), and - only on full success - the generated,
verified, (by default) optimized `*codegen.Module` plus the
`llvm.TargetMachine` built alongside it (`Result.TargetMachine` - both
`nil`/zero-value on any failure; a rarer verifier/target-machine/optimizer-
only failure with no source position is `Result.VerifyErr` instead).
Disposal of a returned `Module`/`TargetMachine` is the caller's job, same as
`codegen.GeneratePackage`'s own `Module.Dispose()` contract - see the
"Optimization pipeline" section for the full ownership/disposal story
across `cmd/llvmc`'s three consumption paths.

This package is deliberately **pure orchestration and CLI-agnostic**: no
`io.Writer`/stderr, no exit codes, no flag handling, and no lexer/parser/
sema/codegen *logic* of its own beyond calling those packages' existing
entry points in the right order - a `Result` is data a caller decides what
to do with, not something this package prints or exits on its own behalf.

# Optimization pipeline

Before this round, this compiler ran **zero LLVM optimization passes** -
`finishPipeline` only ever called `llvm.VerifyModule` after codegen. This
was discovered while benchmarking llvm_lang against Go/Node.js: a trivial
100M-iteration arithmetic loop ran ~3x slower than equivalent Go/JS code,
almost entirely explained by that gap. `finishPipeline` now runs LLVM's own
`default<O2>` pass pipeline (real optimization - inlining, mem2reg, GVN,
LICM, DCE, and more) over every successfully-verified module by default,
with an explicit escape hatch to turn it back off.

**Why it lives in `finishPipeline`, not duplicated per consumption path** -
JIT execution, `-emit-llvm`, and `-o` all funnel through
`CompilePackage`/`CompileProgram` -> `finishPipeline` already; running the
optimizer once, right there, right after `llvm.VerifyModule` succeeds, means
all three uniformly see the identical optimized module - including
`-emit-llvm`, whose printed IR is optimized IR by default now, matching how
`clang -emit-llvm -O2` already behaves.

**`optimize bool`, threaded explicitly** - `CompilePackage`/`CompileProgram`/
`finishPipeline` all take a plain `optimize bool` parameter rather than
always running `default<O2>` unconditionally. Every existing call site
across the codebase passes `true` except `cmd/llvmc`'s real CLI entry
point, which threads `!noOpt` through.

**Why `default<O2>`, not `O1`/`O3`/`Os`/`Oz`** - `default<O2>` is LLVM's own
standard, well-balanced pipeline (the same one `clang -O2` runs): real
inlining/mem2reg/GVN/LICM/DCE without the more aggressive, occasionally
UB-exploiting or code-size-inflating tradeoffs `default<O3>` makes, and
without sacrificing runtime speed for code size the way `default<Os>`/
`default<Oz>` do.

**`optimize` false genuinely restores the old, pre-this-round behavior
byte-for-byte** - `RunPasses` is skipped *entirely* when `optimize` is
false, never substituted with `"default<O0>"` (which is still a real, if
minimal, pass pipeline, not "no pipeline at all"). This matters for
debugging: comparing `-no-opt` output against a plain codegen dump lets you
tell whether a bug lives in codegen itself or was introduced by an
optimization pass.

**The `TargetMachine`: built once, shared, not duplicated** - `finishPipeline`
builds this host's own `llvm.TargetMachine` unconditionally, even when
`optimize` is false, and exposes it via a new `Result.TargetMachine` field
- both because `RunPasses` itself needs one (when `optimize` is true), and
because `cmd/llvmc`'s `-o` AOT path (`compileToExecutable`) needs one
anyway for its own object-code emission. Before this round,
`compileToExecutable` built its own separate `TargetMachine` from scratch,
duplicating those same three calls; it now just reuses
`Result.TargetMachine` instead. Building a real host target machine needs
LLVM's native-target infrastructure initialized first
(`llvm.InitializeNativeTarget`/`llvm.InitializeNativeAsmPrinter`) -
`src/compiler` now does this itself too, guarded by its own `sync.Once`.
Immediately after building the TM, `finishPipeline` also pins the module's
data layout and target triple from it - required for correct C ABI
aggregate lowering on extern boundaries (see the FFI struct-by-value
section and `DECISIONS.md`).

**Disposal ownership** - a `TargetMachine` must be disposed exactly once,
after every consumer that might still need it is done with it. Concretely,
across `cmd/llvmc`'s three consumption paths (`finish`, `main.go`):

- **`-o` (AOT)**: `compileToExecutable` takes over ownership - it reuses
  `res.TargetMachine` for its own `EmitToMemoryBuffer` call and disposes it
  itself (alongside `mod`).
- **`-emit-llvm`**: no further use for the target machine once `RunPasses`
  (if it ran) is done - `finish` disposes it right alongside `res.Module`.
- **Plain JIT execution**: same as `-emit-llvm` - `finish` disposes it once
  `jitRunMain` returns.

**Verifying optimization actually changes generated code, and that
`-no-opt` actually restores the old shape** - both easy to check by eye via
`-emit-llvm`:

```powershell
PS> .\llvmc.exe -emit-llvm -no-opt path\to\program.llx    # unoptimized: every
                                                           # local still an
                                                           # alloca/load/store,
                                                           # every call still
                                                           # a real call
PS> .\llvmc.exe -emit-llvm path\to\program.llx             # optimized: dead
                                                           # allocas promoted
                                                           # to SSA registers
                                                           # (mem2reg), trivial
                                                           # calls inlined and
                                                           # constant-folded
                                                           # away entirely
```

A small worked example (`func addOne(x int) int { return x + 1 }` called
once from `main` as `addOne(5) + 2`) makes the difference concrete: `-no-opt`
emits `addOne` as a real function with an `alloca`/`store`/`load` for its
parameter and a real `call` from `main`; the default optimized path emits
`addOne` as a one-instruction `add`, and `main` as `ret i32 8` - the entire
computation constant-folded away at compile time, with every unused runtime
extern/global codegen would otherwise always emit dead-code-eliminated out
of the module entirely.

**A real bug this round's own verification caught and fixed: `printf`
calls need the `nobuiltin` attribute.** LLVM's optimizer (specifically
`SimplifyLibCalls`/`InstCombine`, part of `default<O2>`) recognizes any call
literally named `printf` as the real libc function and is willing to
rewrite it into a different, "equivalent" libc entry point it considers
cheaper. Concretely: `genPrintLiteral` - used for every struct/array
bracket, element-separator space, and trailing newline - calls `printf`
with a constant, single-character, no-format-specifier format string,
which InstCombine happily rewrites into a bare `putchar` call. That rewrite
is only truly safe if `putchar` and `printf` share the exact same
underlying stdio buffer - not a safe assumption under this project's own
JIT hosting. The observed symptom: every literal bracket/space/newline
silently vanished from a dynamic array's printed output under the default
optimized path, while ordinary `"%d"`-style prints kept working - the two
families' surviving output visibly running together with no separators.

The fix: every `printf` call this package emits now goes through one choke
point, `callPrintf` (`src/codegen/runtime.go`), which tags the call site
with LLVM's `nobuiltin` attribute - the same attribute Clang emits on every
call in a translation unit built with `-fno-builtin`. It tells the
optimizer this particular call must be treated as an ordinary, opaque
external call, never recognized as the corresponding libc built-in, without
disabling `printf`/libc recognition globally (this is purely about this
compiler's own codegen-emitted `printf` calls, not anything a user's
`extern func`-declared FFI call would ever touch).

# The `llvmc` CLI driver (`cmd/llvmc`)

The first way to actually *run* an llvm_lang program as a human, rather than
only proving the pipeline works via `go test`: given a path (a single
source file, or a directory), it resolves the whole program's transitive
import graph (`src/loader`'s `LoadProgram`, backed by `afero.NewOsFs()`),
hands the result to `src/compiler`'s `CompileProgram` to drive it through
the rest of the pipeline, and on full success JIT-executes the resulting
module's `main` directly in this process - so the program's own `print`
calls (real libc `printf` calls under the hood) write to this process's
real stdout, which a `go test`-hosted JIT call can't easily show.

`cmd/llvmc` itself is now the thinnest possible CLI shell on top of
`src/loader` and `src/compiler`: flag parsing, printing every tree's
diagnostics from a `compiler.Result` (still via `diag.FormatSnippet`),
translating `Result.Module == nil` into the compile-error exit code, and -
on success - either dumping the verified module's IR text (`-emit-llvm`) or
JIT-executing it and propagating its `i32` result as this process's own
exit code. None of the actual resolve/check/codegen/verify orchestration
lives in this package anymore.

A single-package, single-file program goes through this exact same path -
there's no separate single-file/single-package code path in `llvmc` itself,
only `compileAndRun`/`compileAndRunPackage` (used by this package's own
in-process tests that build source strings directly) staying as thin
wrappers that call `src/compiler`'s `CompilePackage` instead of
`CompileProgram` followed by the exact same diagnostic-printing/JIT-or-emit
tail (`finish`) `compileAndRunProgram` shares.

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
text (`Module.LLVM.String()`) to stdout and exits 0, without ever executing
anything. This is purely additive: no flag keeps the default
JIT-execution behavior exactly as before.

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
a global regardless of whether `main` is ever actually called - but `main`'s
body is never run, so `printf` never actually fires.

**The IR printed here is optimized IR by default now** (see the
"Optimization pipeline" section above) - pass `-no-opt` alongside
`-emit-llvm` to see the plain, unoptimized shape codegen itself produces.

Since this path never reaches `llvm.NewLLJIT`, disposal is a plain
`Module.Dispose()`/`TargetMachine.Dispose()` pair - not the JIT path's own
ownership-transfer teardown (see "A non-obvious disposal detail" below).

## `-no-opt`: disabling the optimization pipeline

```powershell
.\llvmc.exe -no-opt path\to\program.llx               # JIT, unoptimized
.\llvmc.exe -no-opt -emit-llvm path\to\program.llx     # dump unoptimized IR
.\llvmc.exe -no-opt -o out.exe path\to\program.llx     # AOT, unoptimized
```

Skips `finishPipeline`'s `RunPasses` call entirely - not "run `default<O0>`
instead," genuinely no optimization pipeline call at all - so the module
JIT-executed/printed/AOT-compiled is byte-for-byte the same shape codegen
itself produced. Combines freely with `-emit-llvm`/`-o` - optimization
never changes program behavior/results, only speed/code shape.

## `-o`: AOT compilation to a native executable

```powershell
.\llvmc.exe -o myprogram.exe path\to\program.llx
.\llvmc.exe -o myprogram.exe -L C:\libs -l foo path\to\program.llx
.\myprogram.exe    # a real, standalone .exe - no llvmc, no Go, no LLVM
                    # toolchain present at all, anywhere in the loop
```

The single biggest gap this round closes (see `DECISIONS.md`'s dated entry
for the full "why now"): before this, `llvmc` could only JIT-execute a
program in its own process, or dump textual IR that nothing could actually
run. `-o` runs the exact same pipeline as every other mode and, on success,
produces a real `.exe` at the given path instead.

**The tail, concretely** (`compileToExecutable`, `cmd/llvmc/main.go`):

1. **Emit a native object file** via LLVM's own target-machine backend
   (`TargetMachine.EmitToMemoryBuffer(mod, llvm.ObjectFile)`) - zero
   vendored-binding changes were needed. `compileToExecutable` no longer
   builds its own `TargetMachine` here - it reuses `res.TargetMachine`,
   already built once by `src/compiler`'s own `finishPipeline`.
2. **Write the resulting object bytes to a temporary `.o` file** via a
   plain `os.CreateTemp`/`os.Remove` - not this project's own `afero.Fs`
   convention (a narrow, deliberate exception - see `DECISIONS.md`).
3. **Link it into a real `.exe`** by shelling out to
   `gcc <temp.o> -o <output> [-Ldir...] [-llib...]`, reusing the exact same
   mingw64 toolchain this project already requires on `PATH` for cgo/dev
   work. `gcc` already resolves ordinary libc symbols and any
   user-declared `extern func` binding to a real Win32 API export
   automatically via mingw64's standard import libraries - confirmed by
   `TestBinary_AOT_ExternFuncScopeTimer`. Third-party libs that are not on
   that default set need explicit repeatable `-L`/`-l` flags (dirs before
   libs on the gcc argv). The same flags also feed the default JIT path
   (see "JIT third-party libraries" below) - `-emit-llvm` alone still
   rejects them as a usage error. Confirmed by `TestBinary_AOT_LinkLib`
   and the struct/`cfunc` AOT link tests.

**`main`'s own LLVM signature needed no change at all** - `main` still
lowers to the exact same parameterless `i32 @main()` this project has
always generated, for both the JIT and the AOT path alike. mingw64's own
CRT startup calling `main()` with argc/argv/envp it never passes to a
zero-parameter callee is completely ordinary, valid C-ABI behavior,
confirmed directly by `TestBinary_AOT_HelloWorld` et al. Real argc/argv
access for the `args()` builtin instead goes through `__argc`/`__argv` - see
"The `args()` builtin, concretely" above.

**Mutually exclusive with `-emit-llvm`** - `run` rejects both flags given
together as a usage error before ever compiling anything.

## JIT third-party libraries (`-L` / `-l` without `-o`)

The default JIT path uses the same `-L`/`-l` flags as AOT. After the
process-wide symbol generator (`bindMinGWMainThunk`), `bindExtraLibraries`
resolves each `-l` name under the given `-L` dirs (`resolveLibraryArtifact`
in `cmd/llvmc/linkresolve.go`) and attaches an ORC definition generator:

- **Shared** (`.dll`, preferred when present): `NewDynamicLibrarySearchGeneratorForPath`
- **Static** (`.a` / `.lib`): `NewStaticLibrarySearchGeneratorForPath` via
  `jit.ObjLinkingLayer()`

Search order per name `foo`: `foo.dll`, `libfoo.dll`, then `libfoo.a`,
`foo.a`, `foo.lib`, `libfoo.lib`. A bare filename that already ends in a
library extension (`libfoo.a`, `foo.dll`) is looked up as that exact name
under each `-L` dir. Only a path with a directory separator is treated as a
literal filesystem path (not searched via `-L`). Mingw import libs (`*.dll.a`)
are rejected with a clear error - they don't contain real code for the static
generator; provide the real `.dll` or a true static archive. Resolution is
limited to the explicit `-L` dirs (no vague system-wide search) so tests stay
hermetic.

Confirmed by `TestBinary_JIT_LinkLibStatic`, `TestBinary_JIT_LinkLibDLL`,
and `TestRun_JIT_MissingLibrary`.

## `-watch`: hot-reload JIT

`llvmc -watch [-init Name] [-tick Name] …` keeps one LLJIT instance alive
(with `-l`/`-L` generators bound once) and reloads the user module when any
loaded `.llx` file's mtime/size changes:

1. Compile via `loader.LoadProgram` + `compiler.CompileProgram`.
2. Add the module under a fresh ORC `ResourceTracker`
   (`AddLLVMIRModuleWithRT`); previous tracker is `Remove`d first so symbol
   names don't clash.
3. Run `llvm_lang.global_init`, then optional Init (void; default name
   `Init`, skipped if absent unless `-init` was set explicitly).
4. Loop calling Tick (default `Frame`), which must return `int`: `0`
   continues, non-zero exits the process with that code.

`main` is unused under `-watch`. Mutually exclusive with `-o` /
`-emit-llvm`. A compile failure after a successful load prints diagnostics
and keeps the last-good module; the next successful edit swaps in the new
one and runs Init again (reset-on-reload). Host libraries stay loaded across
reloads. Arena memory is still process-lifetime (see `BLOCKERS.md`) - it
grows across reloads in v1. Tick should pace itself (e.g. vsync inside
Frame); the driver loops as fast as Tick returns. Stdout is set unbuffered
so `print` is visible while the process stays up.

`runWatch` calls `runtime.LockOSThread()` first thing, so Init and every
Tick run on one fixed OS thread for the process's life - required by any
linked library that binds state to its creating thread (e.g. an OpenGL
context via GLFW). Because Init reruns on every reload, Init itself must be
idempotent for anything with that kind of one-time-only OS-level side
effect (e.g. guard a window-creation call with the library's own
"already initialized" check) - `-watch` re-running it is not a bug.

Confirmed by `TestBinary_Watch_TickExit`, `TestBinary_Watch_Reload`, and
`TestBinary_Watch_LastGoodOnError`.

## Source file extension: `.llx`

This project's source files use the extension `.llx`, not `.ll` - `.ll` is
already LLVM's own textual IR format's extension, and reusing it here would
be a real (and confusing) collision, since this compiler also prints/
inspects real LLVM IR elsewhere. The compiler pipeline proper still doesn't
inspect a file's extension at all - `src/loader`'s directory scan is the one
place `.llx` is now checked for real (case-insensitively), since "every
`.llx` file directly in this directory" has to mean *something* concrete
when resolving a bare directory path. See `examples/` at the repo root for
sample `.llx` programs, each its own subdirectory so a single-file example
doesn't accidentally pull its siblings into the same package.

## Exit codes

- **2** - a usage error: no path argument, an unrecognized flag, `-watch`
  with `-o`/`-emit-llvm`, `-init`/`-tick` without `-watch`, both `-o`
  and `-emit-llvm` given together, `-l`/`-L` with `-emit-llvm`, `-L` without
  `-l`, the path couldn't be resolved to a real file/directory, its resolved
  directory has zero `.llx` files in it, an imported package directory
  couldn't be found, or a real import cycle was detected. A short message
  goes to stderr; nothing is compiled.
- **1** - a compile-time diagnostic from the lexer, parser stage, or from
  `src/compiler`'s `finishPipeline`: `sema.ResolveProgram` (or
  `ResolvePackage`, for an import-less package), `sema.CheckProgram`, or
  `codegen.GeneratePackage` (the pipeline stops at the first stage
  reporting an error-severity diagnostic in any file) - or the module
  failing LLVM's own verifier, or (rarer still) `finishPipeline`'s own
  target-machine construction or (with optimization on) its `RunPasses`
  call failing, or a module with no `main` function to JIT-execute, or JIT
  failing to resolve a `-l` library under the given `-L` dirs. Every
  diagnostic is printed to stderr via `diag.FormatSnippet`. With
  `-emit-llvm`, this is the only non-zero exit code reachable at all. With
  `-o`, this also covers every failure mode specific to that path's own
  tail (`compileToExecutable`) - the target machine failing to resolve/
  emit, a temporary-object-file I/O error, or the `gcc` link step itself
  failing/returning non-zero.
- **otherwise** - the language program's own exit code. `func main()`
  always lowers to a real, parameterless `i32 @main()` regardless of
  whether the source declares a return type for it - falling off the end
  or a bare `return` becomes `ret i32 0`, and `return expr` returns `expr`
  directly. `llvmc` propagates that i32 result directly as its own process
  exit code, so `func main() int { return 2 + 3 }` really does exit the
  `llvmc` process with code 5. This doesn't apply with `-emit-llvm` (`main`
  is never executed) or `-o` (a successful AOT compilation always exits
  `llvmc` with code `0` regardless of what the produced `.exe`'s own `main`
  would return; that exit code only appears later, when someone actually
  runs the resulting standalone binary).

## A non-obvious disposal detail

Once a `codegen.Module`'s `Ctx` and `LLVM` fields are wrapped into a
`ThreadSafeContext`/`ThreadSafeModule` and added to an LLJIT instance, the
LLJIT instance takes ownership of both - calling `Module.Dispose()`
afterward would double-free them (this exact pitfall is already documented
on `src/codegen/codegen_test.go`'s `compileAndJIT` helper). So the two paths
that never reach a live LLJIT instance - a codegen diagnostic, or a failed
`llvm.VerifyModule` - already call `Module.Dispose()` themselves, inside
`src/compiler`'s `finishPipeline`, before ever handing a `Result` back (a
`Result.Module` is always nil on either path). Once JIT execution is about
to happen, disposal instead goes through the LLJIT instance alone
(`jit.Dispose()`) - `cmd/llvmc`'s `jitRunMain` - which tears down the module
and context together, in the correct order, in one call; unlike the legacy
MCJIT `ExecutionEngine` (which only ever took ownership of the module,
leaving the context for the caller to dispose separately), LLJIT's
ownership transfer already covers both. The one remaining case `cmd/llvmc`
itself still calls `Module.Dispose()` directly is `-emit-llvm`'s own
success path - a verified module that's never handed to `llvm.NewLLJIT` at
all.

## A MinGW/GCC ABI quirk: implicit `__main()` calls

A real, empirically-discovered platform gotcha hit while switching this
project's JIT engine from the legacy MCJIT `ExecutionEngine` to LLJIT (see
`DECISIONS.md`'s dated "JIT execution: LLJIT" entry): LLVM's backend, when
compiling a function literally named `main` for a `*-windows-gnu` target -
this project's own mingw64 host - auto-inserts a call to `__main()` at that
function's very start. This is the same thing GCC's own frontend does for a
real MinGW-linked program, there to run static C++-style constructors via a
much older, completely different convention than this project's own
`@llvm.global_ctors` mechanism. MCJIT never took this same code path -
whatever internal target selection it used apparently didn't trigger it,
only LLJIT's real host-detected `TargetMachine` does.

This project has no use for whatever `__main` would normally do, and never
defines it itself - without a real, resolvable `__main` symbol, materializing
`main` at all fails outright ("JIT session error: Symbols not found: [
__main ]"), confirmed directly by capturing the compiled module's own IR
text before and after a JIT run and finding `__main` genuinely referenced
only in the *compiled* form, never the textual IR. `cmd/llvmc`'s
`bindMinGWMainThunk` (mirrored exactly in `src/codegen`'s own test helpers)
works around this by binding `__main` directly to libc's own `rand` via
`AbsoluteSymbols`/`JITDylib.Define` - `rand` is real, already resolvable via
a process-symbol generator attached to the main JITDylib, and safe to call
with zero arguments and an ignored result. This is unrelated to actually
running `llvm_lang.global_init`: binding `__main` to `global_init` directly
wouldn't help any JIT'd function *other* than `main` itself, so
`global_init` still runs through its own separate, explicit Lookup-and-call
(see "Global `var` initializers" above), unaffected by this.

**A second, related discovery made while diagnosing this:** LLJIT's compile
layer empties the original IR module out once it's been compiled to
machine code, unlike the legacy MCJIT `ExecutionEngine` (which kept the
source `Module` intact for its whole lifetime). Calling `Module.String()`
again *after* a JIT-executed call through that module returns just the bare
`; ModuleID = ...`/datalayout header - verified directly by capturing and
comparing the same module's IR text before and after a `runInt32` call -
rather than the real generated IR, and this was observed to crash outright
in at least one case. Since the IR text itself never changes after codegen,
`src/codegen`'s own `jitModule` test helper now captures a module's IR text
once, immediately after codegen and before it's ever handed to an LLJIT
instance (`jitModule.ir`), and every test wanting to assert on generated IR
text uses that instead of calling `jm.mod.LLVM.String()` itself.
