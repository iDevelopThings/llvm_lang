// Command (root) is not the compiler - see the redirect message in main
// below. This file exists to anchor the top-level go:generate directives
// that don't belong to any one package (cmd/enum_codegen's own README
// covers the mechanism); the real entry points are cmd/llvmc (the compiler
// CLI) and cmd/llvmc-lsp (the language server) - see build.ps1.
package main

//go:generate go run ./cmd/enum_codegen -in ./src/enums

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "llvm_lang: this root package is not the compiler - run cmd/llvmc (the CLI) or cmd/llvmc-lsp (the language server) instead. See build.ps1.")
	os.Exit(1)
}
