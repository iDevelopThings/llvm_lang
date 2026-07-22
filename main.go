package main

//go:generate go run ./cmd/enum_codegen -in ./src/enums

import (
	"fmt"

	"tinygo.org/x/go-llvm"
)

// Smoke test: build `i32 @add(i32 %a, i32 %b)` in memory, print the IR,
// and verify the module. If this runs, the whole go-llvm + LLVM 22 + cgo
// chain is wired up correctly.
//
// Build tag: -tags=llvm22 (the Windows cgo flags live in the local
// third_party/go-llvm copy, so no CGO_* env or byollvm tag is needed).
//
//	go run -tags=llvm22 .      # or just press Run in GoLand once llvm22 is set
func main() {
	ctx := llvm.NewContext()
	defer ctx.Dispose()

	mod := ctx.NewModule("llvm_lang_demo_test")
	defer mod.Dispose()

	// define i32 @add(i32 %a, i32 %b) { entry: ret i32 (a + b) }
	i32 := ctx.Int32Type()
	fnType := llvm.FunctionType(i32, []llvm.Type{i32, i32}, false)
	fn := llvm.AddFunction(mod, "add", fnType)
	fn.Param(0).SetName("a")
	fn.Param(1).SetName("b")

	entry := ctx.AddBasicBlock(fn, "entry")
	b := ctx.NewBuilder()
	defer b.Dispose()
	b.SetInsertPointAtEnd(entry)

	sum := b.CreateAdd(fn.Param(0), fn.Param(1), "sum")
	b.CreateRet(sum)

	// Print the IR we just generated.
	fmt.Print(mod.String())

	// Verify it is well-formed.
	if err := llvm.VerifyModule(mod, llvm.PrintMessageAction); err != nil {
		fmt.Println("module verification failed:", err)
		return
	}

	fmt.Printf("// go-llvm against LLVM %s is working ✅ ✅ ✅\n", llvm.Version)
}
