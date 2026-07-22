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
