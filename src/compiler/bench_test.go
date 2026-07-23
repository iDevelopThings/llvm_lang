package compiler

import (
	"testing"

	"llvm_lang/src/bench"
	"llvm_lang/src/loader"
)

// benchmarkCompilePackage runs the full lex-parse-resolve-check-codegen-
// verify pipeline (CompilePackage) end to end, once per iteration - a
// realistic "how long does compiling this whole program actually take"
// number. Deliberately does NOT JIT-execute the result (no LLJIT instance,
// no running llvm_lang.global_init, no calling main) - that's a separate
// concern (see AGENTS.md's benchmarking task) already covered by
// src/codegen's own JIT-execution tests, not this compile-speed benchmark.
//
// Every real per-call cost CompilePackage itself incurs - including its own
// llvm.NewContext()/llvm.VerifyModule call, mirroring
// src/codegen/bench_test.go's reasoning for why context/module setup stays
// inside the loop - is measured; res.Module.Dispose() closes out each
// iteration's own module/context pair so the loop doesn't leak one per b.N.
func benchmarkCompilePackage(b *testing.B, src string) {
	b.Helper()
	files := []loader.SourceFile{{Name: "bench.llx", Src: src}}
	b.ReportAllocs()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		res := CompilePackage(files)
		if res.Module == nil {
			b.Fatalf("unexpected compile failure: VerifyErr = %v, diags = %v", res.VerifyErr, dumpDiags(res))
		}
		res.Module.Dispose()
	}
}

// BenchmarkCompilePackageSmall covers bench.Small.
func BenchmarkCompilePackageSmall(b *testing.B) {
	benchmarkCompilePackage(b, bench.Small)
}

// BenchmarkCompilePackageLarge covers bench.Large.
func BenchmarkCompilePackageLarge(b *testing.B) {
	benchmarkCompilePackage(b, bench.Large)
}
