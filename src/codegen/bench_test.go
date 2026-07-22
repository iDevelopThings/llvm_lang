package codegen

import (
	"testing"

	"llvm_lang/src/bench"
	"llvm_lang/src/lexer"
	"llvm_lang/src/parser"
	"llvm_lang/src/sema"
)

// benchmarkGenerate parses+resolves+checks src once outside the timed loop -
// that cost is already covered by src/parser's and src/sema's own
// benchmarks, and Generate assumes an already-valid tree+info pair as its
// input (see this package's own doc comment) - then repeatedly calls
// Generate against that same tree/info.
//
// Generate creates a brand-new llvm.Context/Module on every call (see
// GeneratePackage) - that per-call context/module setup is genuinely part of
// what Generate does on every real invocation, so it stays inside the timed
// loop. What must NOT be inside the loop is the one-time, process-lifetime
// LLVM native-target/JIT setup (llvm.InitializeNativeTarget and friends,
// see codegen_test.go's initJIT) - Generate itself never touches any of
// that (only actually JIT-*executing* the result would), so this benchmark
// never calls it at all.
//
// mod.Dispose is called at the end of each iteration, inside the timed
// loop: it's the real, symmetric other half of the per-call
// context/module lifecycle Generate begins, and leaving it out would also
// leak an LLVM Context per b.N iteration.
func benchmarkGenerate(b *testing.B, src string) {
	b.Helper()
	tree, pdiags := parser.ParseFile(lexer.NewFile("bench.llx", src))
	if pdiags.HasErrors() {
		b.Fatalf("unexpected parse errors: %v", pdiags.All())
	}
	info, rdiags := sema.Resolve(tree)
	if rdiags.HasErrors() {
		b.Fatalf("unexpected resolve errors: %v", rdiags.All())
	}
	cdiags := sema.Check(tree, info)
	if cdiags.HasErrors() {
		b.Fatalf("unexpected check errors: %v", cdiags.All())
	}
	b.ReportAllocs()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		mod, gdiags := Generate(tree, info, "bench")
		if gdiags.HasErrors() {
			b.Fatalf("unexpected codegen errors: %v", gdiags.All())
		}
		mod.Dispose()
	}
}

// BenchmarkGenerateSmall covers bench.Small.
func BenchmarkGenerateSmall(b *testing.B) {
	benchmarkGenerate(b, bench.Small)
}

// BenchmarkGenerateLarge covers bench.Large.
func BenchmarkGenerateLarge(b *testing.B) {
	benchmarkGenerate(b, bench.Large)
}
