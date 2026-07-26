package codewriter

import "testing"

func BenchmarkBraceBlock(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		w := New(Grow(512))
		w.Brace("type Foo struct", func() {
			for i := range 32 {
				w.Linef("F%d int", i)
			}
		})
		_ = w.Len()
	}
}

func BenchmarkLinePlain(b *testing.B) {
	b.ReportAllocs()
	w := New(Grow(64 << 10))
	b.ResetTimer()
	for b.Loop() {
		w.Reset()
		for range 64 {
			w.Line("x int")
		}
	}
}
