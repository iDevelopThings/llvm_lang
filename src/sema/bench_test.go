package sema

import (
	"testing"

	"llvm_lang/src/bench"
	"llvm_lang/src/lexer"
	"llvm_lang/src/parser"
)

// benchmarkResolveCheck parses src once (outside the timed loop - parsing
// isn't what this benchmark measures, see src/parser/bench_test.go for
// that), then repeatedly runs Resolve+Check against that same already-parsed
// tree: both return a fresh *Info/*diag.Bag per call with no shared mutable
// state carried between calls (see Info's own doc comment), so re-running
// them against one parsed tree b.N times measures exactly the resolve+check
// cost a real caller pays once per compile, without re-paying parse cost
// b.N times over.
func benchmarkResolveCheck(b *testing.B, src string) {
	b.Helper()
	tree, pdiags := parser.ParseFile(lexer.NewFile("bench.llx", src))
	if pdiags.HasErrors() {
		b.Fatalf("unexpected parse errors: %v", pdiags.All())
	}
	b.ReportAllocs()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		info, rdiags := Resolve(tree)
		if rdiags.HasErrors() {
			b.Fatalf("unexpected resolve errors: %v", rdiags.All())
		}
		cdiags := Check(tree, info)
		if cdiags.HasErrors() {
			b.Fatalf("unexpected check errors: %v", cdiags.All())
		}
	}
}

// BenchmarkResolveCheckSmall covers bench.Small.
func BenchmarkResolveCheckSmall(b *testing.B) {
	benchmarkResolveCheck(b, bench.Small)
}

// BenchmarkResolveCheckLarge covers bench.Large.
func BenchmarkResolveCheckLarge(b *testing.B) {
	benchmarkResolveCheck(b, bench.Large)
}
