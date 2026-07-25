package parser

import (
	"testing"

	"llvm_lang/src/bench"
	"llvm_lang/src/lexer"
)

// benchmarkParse runs ParseFile on a fresh lexer.File each iteration - a
// *lexer.File/Tree pair isn't meant to be reused across parses, so a fresh
// one is built inside the timed loop, same cost a real caller always pays.
func benchmarkParse(b *testing.B, src string) {
	b.Helper()
	b.ReportAllocs()
	b.SetBytes(int64(len(src)))

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		file := lexer.NewFile("bench.llx", src)
		tree, diags := ParseFile(file, false)
		if diags.HasErrors() {
			b.Fatalf("unexpected parse errors: %v", diags.All())
		}
		_ = tree
	}
}

// BenchmarkParseSmall covers bench.Small.
func BenchmarkParseSmall(b *testing.B) {
	benchmarkParse(b, bench.Small)
}

// BenchmarkParseLarge covers bench.Large.
func BenchmarkParseLarge(b *testing.B) {
	benchmarkParse(b, bench.Large)
}
