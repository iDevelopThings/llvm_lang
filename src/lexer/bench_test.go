package lexer

import (
	"testing"

	"llvm_lang/src/bench"
	"llvm_lang/src/enums"
)

// benchmarkLex tokenizes src end to end (repeatedly calling Next until EOF,
// inclusive - mirroring All's own stopping condition) once per iteration.
// b.ReportAllocs is on for every benchmark in this file - allocation counts
// matter as much as timing here (see AGENTS.md's Standards section).
func benchmarkLex(b *testing.B, src string) {
	b.Helper()
	b.ReportAllocs()
	b.SetBytes(int64(len(src)))
	file := NewFile("bench.llx", src)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		l := New(file)
		for {
			tok := l.Next()
			if tok.Lexeme == enums.Lexemes.EOF {
				break
			}
		}
	}
}

// BenchmarkLexSmall covers bench.Small - see that fixture's own doc comment
// for the feature mix it represents.
func BenchmarkLexSmall(b *testing.B) {
	benchmarkLex(b, bench.Small)
}

// BenchmarkLexLarge covers bench.Large - the same fixture mechanically
// scaled up, to see how lexing cost scales with input size.
func BenchmarkLexLarge(b *testing.B) {
	benchmarkLex(b, bench.Large)
}
