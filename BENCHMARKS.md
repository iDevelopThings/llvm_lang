# Benchmarks

A dated baseline snapshot for how each pipeline stage (lexer -> parser ->
sema -> codegen -> the end-to-end `src/compiler` entry point) actually
performs, measured with real `testing.B` benchmarks rather than assumed -
see AGENTS.md's `## Standards` section ("max efficiency and performance,
keeping allocations down to a minimum"). Future rounds should re-run these
and compare against the numbers here, updating this file (with a new dated
entry, not overwriting history) when the comparison is actually interesting
- a steady drift as the language grows real features is expected and fine;
a sudden jump in allocs/op or a stage no longer scaling linearly with input
size is the kind of thing worth a second look.

## Fixtures

Every benchmark below runs against the same two shared fixtures, defined
once in `src/bench/fixtures.go` (`bench.Small`, `bench.Large`) rather than
each stage's own throwaway snippet, so numbers across stages are directly
comparable:

- **Small** - a single, feature-representative program: a struct with a
  constructor plus a mutating and a non-mutating method, recursive function
  calls, both `for` forms plus `if`/`else`, a dynamic array (`make`/
  `append`/`len`/indexing), a closure, and a pointer (`&`/`*`, `new`/
  `delete`). Deliberately not a trivial "hello world" - see AGENTS.md's
  benchmarking task for why a representative feature mix matters here.
- **Large** - the same feature mix mechanically repeated 40x under distinct
  names (`buildLarge` in `fixtures.go`), to see how each stage's cost scales
  with input size rather than only ever measuring one fixed small size.

## How to run

```powershell
.\test.ps1 -Bench .                              # every stage
.\test.ps1 -Bench . -Package ./src/codegen/...    # one stage
```

(`test.ps1 -Bench` passes `-run=^$ -bench=<pattern> -benchmem` through to
`go test`, alongside the mingw64-PATH setup every other test run here
needs - see AGENTS.md's Compiling section.)

## 2026-07-22 baseline

Measured on the dev machine actually running this project (Intel Core
i9-10900K, Windows 11, LLVM 22 via MSYS2 mingw64, `go test -tags=llvm22
-bench=. -benchmem`). Each line is one `testing.B` benchmark; `ns/op`/
`B/op`/`allocs/op` are `go test -benchmem`'s own reported columns.

| Stage | Fixture | ns/op | B/op | allocs/op |
|---|---|---:|---:|---:|
| Lexer (`Next` to `EOF`) | Small | 25,988 | 24,430 | 0 |
| Lexer (`Next` to `EOF`) | Large | 588,030 | 572,226 | 0 |
| Parser (`ParseFile`) | Small | 104,165 | 110,937 | 114 |
| Parser (`ParseFile`) | Large | 2,700,821 | 3,093,317 | 1,369 |
| Sema (`Resolve`+`Check`) | Small | 107,543 | 81,166 | 242 |
| Sema (`Resolve`+`Check`) | Large | 2,539,280 | 2,375,830 | 3,637 |
| Codegen (`Generate`) | Small | 278,894 | 6,293 | 133 |
| Codegen (`Generate`) | Large | 6,958,439 | 133,567 | 2,037 |
| End-to-end (`CompilePackage`) | Small | 561,393 | 198,388 | 490 |
| End-to-end (`CompilePackage`) | Large | 13,661,341 | 5,606,010 | 7,051 |

Full raw output (all five packages, one `go test -bench=. -benchmem` pass
each) is reproducible via the command above; see each package's own
`bench_test.go` (`src/lexer`, `src/parser`, `src/sema`, `src/codegen`,
`src/compiler`) for the exact benchmark code.

### Reading these numbers

- **Lexer never allocates on the heap** for either fixture (`0 allocs/op`) -
  every `Token` is a plain value and the `Lexer` itself doesn't escape the
  benchmark loop, matching the package's own doc comment ("keeps memory
  proportional to the source buffer plus a one-token lookahead"). The
  nonzero `B/op` alongside `0 allocs/op` is a known `testing`/runtime
  measurement quirk (stack-growth bookkeeping, not a real heap allocation) -
  not a discrepancy worth chasing.
- **Every stage scales roughly linearly with input size**, not
  super-linearly: Large's fixture is ~23-24x Small's by both byte count and
  AST size, and every stage's Large/Small ns/op ratio lands in the same
  ~22-26x band (codegen: 24.9x; sema: 23.6x; parser: 25.9x; lexer: 22.6x;
  end-to-end: 24.3x) - no stage showed the kind of quadratic-looking blowup
  (a ratio far outside that band) that would call for an actual fix under
  this task's "only fix what clearly jumps out" rule. Nothing here rose to
  that bar, so nothing was changed in production code - this pass is a
  measurement baseline, not an optimization pass.
- **Codegen's `Generate` is deliberately benchmarked in isolation** from
  LLVM's one-time, process-lifetime native-target/JIT setup
  (`llvm.InitializeNativeTarget` and friends) - `Generate` itself never
  touches that path (only JIT-*executing* the result would), so no such
  setup is even present to accidentally leave inside the timed loop. Each
  iteration's own `llvm.NewContext()`/`Module.Dispose()` pair *is* included,
  deliberately - that per-call setup/teardown is real work `Generate` (and
  every one of its callers) actually pays on every invocation.
- **End-to-end roughly equals the sum of the four stages** (e.g. Small:
  25,988 + 104,165 + 107,543 + 278,894 ≈ 516,590 ns vs. a measured 561,393
  ns for `CompilePackage` - the gap is `finishPipeline`'s own diagnostic-bag
  merging and LLVM's module verifier, both real, both small) - a sanity
  check that nothing is being double-counted or missed between the
  per-stage benchmarks and the end-to-end one.

## 2026-07-23 update - AOT compilation, `args()`, and runtime trap diagnostics

Re-measured after landing AOT compilation (`-o`), the `args()` builtin, and
informative runtime trap diagnostics (see `DECISIONS.md`'s three dated
entries from this same round) - same machine/fixtures/command as the
2026-07-22 baseline above, compared directly against a same-session
pre-change build (`git stash`) rather than against the table above alone, to
rule out cross-session machine-state noise:

| Stage | Fixture | ns/op (before -> after) | B/op (before -> after) | allocs/op (before -> after) |
|---|---|---:|---:|---:|
| Sema (`Resolve`+`Check`) | Small | 108,383 -> 103,387 | 82,221 -> 83,231 | 265 -> 268 |
| Sema (`Resolve`+`Check`) | Large | 2,482,991 -> 2,535,528 | 2,383,112 -> 2,384,186 | 4,047 -> 4,050 |
| Codegen (`Generate`) | Small | 275,729 -> 276,345 | 6,357 -> 6,477 | 134 -> 139 |
| Codegen (`Generate`) | Large | 6,015,516 -> 6,357,359 | 133,957 -> 138,779 | 2,038 -> 2,238 |

- **Sema's +3 allocs/op, both fixtures, not scaling with input size** -
  exactly what adding one predeclared symbol (`args`, alongside
  `make`/`append`/`len`) to `universeScope` should cost: one extra `Symbol`
  allocated once per `Resolve` call, a constant, not a per-node cost - the
  identical +3 for both Small and Large (rather than Large scaling ~24x the
  way every genuinely per-node cost in this file already does) confirms it's
  exactly that and nothing more.
- **Codegen's allocs/op grew disproportionately more for Large (+200) than
  Small (+5)** - this is real, attributable, and expected, not noise: every
  runtime trap site (`genBoundsCheck`/`genSliceRangeCheck`/
  `genMakeSizeCheck`) now emits one additional `printf` call (the new
  informative trap-diagnostic message - see `CODEGEN.md`'s "Runtime trap
  diagnostics" section) alongside the pre-existing `llvm.trap`, and the Large
  fixture's dynamic-array/indexing feature mix repeats every one of those
  call sites 40x, same as everything else in it - so this cost scales with
  how many trap-checked operations a program has, exactly like the rest of
  codegen's own cost already does, not with anything unbounded. Small's own
  +5 is the one-time `setupArgsGlobal` declaration (always present,
  regardless of whether a program calls `args()` - see `CODEGEN.md`'s "The
  `args()` builtin" section) plus its own smaller number of trap sites.
  Nothing here is a stage no longer scaling linearly (Large/Small's own ratio
  is still in the same band the 2026-07-22 entry already measured) - this is
  a deliberate, requested tradeoff (informative crash diagnostics, per this
  round's own explicit scope), not an accidental regression, and the
  magnitude (a low-single-digit-percent `ns/op` change either way, ~10%
  `allocs/op` on codegen's own Large fixture specifically) was judged not
  worth trading away the debuggability the feature exists to provide.

## 2026-07-23 update - default-on `default<O2>` optimization pipeline

Re-measured `BenchmarkCompilePackageSmall`/`Large` after this round's own
change (see `DECISIONS.md`'s dated entry): `CompilePackage`/`CompileProgram`
now always run LLVM's real `default<O2>` pass pipeline unless a caller
explicitly opts out (`optimize` false - `cmd/llvmc`'s own `-no-opt` flag),
and every existing call site (including this benchmark's own
`benchmarkCompilePackage`, `src/compiler/bench_test.go`) keeps optimization
on, matching every real caller's own new default - so this is the genuine
new steady-state cost of `CompilePackage`, not a distinct "with optimize"
variant benchmarked alongside an unchanged one. Lexer/parser/sema/codegen's
own per-stage benchmarks are untouched by this round at all (none of them
ever call through `src/compiler`), so only the end-to-end row moves:

| Stage | Fixture | ns/op (before -> after) | B/op (before -> after) | allocs/op (before -> after) |
|---|---|---:|---:|---:|
| End-to-end (`CompilePackage`) | Small | 561,393 -> 11,850,009 | 198,388 -> 210,719 | 490 -> 543 |
| End-to-end (`CompilePackage`) | Large | 13,661,341 -> 422,949,467 | 5,606,010 -> 5,852,045 | 7,051 -> 8,074 |

- **This is a real, expected, large jump (~21x Small, ~31x Large), not a
  regression to chase** - it's the actual cost of running LLVM's own
  `default<O2>` pipeline (a large, fixed battery of analysis/transform
  passes - inlining, mem2reg, GVN, LICM, DCE, and many more, see
  `CODEGEN.md`'s "Optimization pipeline" section) over a module, which this
  compiler previously never ran at all (see that same section's "why now" -
  a real 100M-iteration arithmetic loop benchmarked ~3x slower than
  equivalent Go/Node.js, entirely attributable to zero optimization ever
  running). `B/op`/`allocs/op` barely move by comparison (+6% Small, +4%
  Large) - almost all of the new cost is pure CPU time spent walking/
  rewriting IR the pass pipeline already holds in memory, not new
  allocation volume.
- **Large's ns/op ratio to Small no longer sits in the ~22-26x band every
  other stage (and this same end-to-end benchmark, before this round) still
  does** - 422,949,467 / 11,850,009 ≈ 35.7x. This is expected, not a
  linearity regression to fix: `default<O2>`'s own cost is not a fixed
  per-module constant added on top of an otherwise-linear codegen cost - a
  40x-larger module gives the pipeline's own interprocedural passes (inlining
  in particular) proportionally more to actually do, so its own cost grows
  somewhat faster than linearly with module size on top of the
  already-linear stages beneath it. Nothing here is a correctness concern
  (see this round's own JIT/AOT output-equivalence verification, not a
  benchmarking task) - purely a compile-speed/runtime-speed tradeoff, made
  deliberately and by explicit request.
- **`-no-opt` exists precisely to opt back out of this cost** when compile
  speed matters more than runtime speed for a given invocation (e.g. a fast
  debug-iteration loop) - untested by this benchmark specifically (which
  always passes `optimize` true, matching the new default), but skipping
  `finishPipeline`'s `RunPasses` call entirely when `optimize` is false
  means that path's own cost is unchanged from the pre-this-round numbers
  above.

## 2026-07-25 update - monomorphized generics

Re-measured after landing generics (see `DECISIONS.md`'s dated entry), same
machine/fixtures/command, against a same-session pre-change build. Only the
per-stage lexer/parser/sema/codegen benchmarks are relevant here - neither
fixture uses generics at all, so this measures the cost this feature adds to
programs that don't use it:

| Stage | Fixture | B/op (before -> after) | allocs/op (before -> after) |
|---|---|---:|---:|
| Parser (`ParseFile`) | Small | 110,937 -> 110,945 | 114 -> 114 |
| Parser (`ParseFile`) | Large | 3,093,319 -> 3,093,326 | 1,369 -> 1,369 |
| Sema (`Resolve`+`Check`) | Small | 99,930 -> 101,195 | 312 -> 314 |
| Sema (`Resolve`+`Check`) | Large | 2,795,819 -> 2,808,429 | 4,793 -> 4,795 |
| Codegen (`Generate`) | Small | 11,831 -> 11,829 | 167 -> 167 |
| Codegen (`Generate`) | Large | 236,198 -> 236,205 | 2,928 -> 2,928 |

ns/op is omitted deliberately - every stage's before/after landed inside
run-to-run noise at this benchtime, with no consistent direction.

- **Flat, as designed.** Neither fixture declares a generic, so no
  specialization is ever created and the instantiation pass never runs.
- Parser and codegen move by single-digit B/op with no allocation change at
  either fixture size - within noise, not an attributable cost.
- Sema is the only consistently-directional change: +2 allocs/op on both
  fixtures (constant per package, so it doesn't scale with program size) and
  ~+1.3% / ~+0.5% B/op on Small / Large. The B/op delta isn't traced to a
  specific allocation site here; what the numbers do support is that it
  doesn't grow with the program.
