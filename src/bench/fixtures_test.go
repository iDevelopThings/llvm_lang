package bench

import (
	"testing"

	"llvm_lang/src/compiler"
	"llvm_lang/src/loader"
)

// TestFixturesCompileCleanly guards Small/Large themselves: every real
// benchmark built on top of these fixtures (see src/lexer/bench_test.go and
// its siblings) assumes they're valid, diagnostic-free llvm_lang programs -
// if that ever stops being true (e.g. a future language change invalidates
// something these fixtures use), every stage's benchmark would otherwise
// fail for a confusing reason far from this actual root cause.
func TestFixturesCompileCleanly(t *testing.T) {
	tests := []struct {
		name string
		src  string
	}{
		{"Small", Small},
		{"Large", Large},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := compiler.CompilePackage([]loader.SourceFile{{Name: "fixture.llx", Src: tt.src}}, true)
			t.Cleanup(func() {
				if res.Module != nil {
					res.Module.Dispose()
					res.TargetMachine.Dispose()
				}
			})
			if res.Module == nil {
				var diags []string
				for _, tree := range res.Trees {
					b := res.Diags[tree]
					if b == nil {
						continue
					}
					for _, d := range b.All() {
						diags = append(diags, d.Msg)
					}
				}
				t.Fatalf("fixture failed to compile; VerifyErr = %v, diags = %v", res.VerifyErr, diags)
			}
		})
	}
}
