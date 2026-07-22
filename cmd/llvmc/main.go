// Command llvmc is the first real, human-facing way to run an llvm_lang
// source file: read it, drive it through the full compiler pipeline
// (lexer -> parser -> sema.Resolve -> sema.Check -> codegen), and, on full
// success, JIT-execute the resulting module's `func main()` directly in this
// process - so the program's own `print` calls (which lower to real libc
// `printf` calls, see AGENTS.md's codegen section) write to this process's
// real stdout, something a `go test`-hosted JIT call can't easily show (see
// BLOCKERS.md's codegen-phase entry 7).
//
// Usage:
//
//	llvmc <file.llx>
//	llvmc -emit-llvm <file.llx>
//
// The -emit-llvm flag runs the exact same pipeline (including LLVM's own
// module verifier) but, instead of JIT-executing the result, prints the
// generated module's LLVM IR text to stdout and exits 0 without ever
// executing anything - useful for inspecting what a language feature lowers
// to without writing a throwaway `go test` each time. Every other flag
// combination (no flag) keeps the default JIT-execution behavior exactly as
// before.
//
// Source file extension: this project picks ".llx" for llvm_lang source
// files - ".ll" is already LLVM's own textual IR format's extension, and
// reusing it here would be a real (and confusing) collision with that.
// Nothing in the compiler pipeline itself actually inspects the extension -
// lexer.NewFile just takes a name (used only for diagnostics) and the source
// text - so this is purely a human-facing convention, not an enforced one.
//
// Exit codes:
//   - 2: usage error - no file argument, an unrecognized flag, or the file
//     couldn't be read. A short usage message is printed to stderr; nothing
//     is compiled.
//   - 1: a compile-time diagnostic - the lexer, parser, sema.Resolve,
//     sema.Check, or codegen.Generate stage reported at least one
//     error-severity diagnostic. Every diagnostic collected by whichever
//     stage failed first is printed to stderr via diag.FormatSnippet (a
//     "file:line:col: severity: message" header plus the offending source
//     line and a caret), and no later stage runs. This also covers the
//     module failing LLVM's own verifier, and the module JIT-executing but
//     containing no `main` function to run. With -emit-llvm, this is the
//     only non-zero exit code possible - a verified module's IR is always
//     printed and this process always exits 0 afterward.
//   - otherwise: the language program's own exit code. `func main()` always
//     lowers to a real `i32 @main()` LLVM function regardless of whether the
//     source declares a return type for it (see AGENTS.md's "`main` is the
//     real entry point" section) - falling off the end or a bare `return`
//     becomes `ret i32 0`, and `return expr` returns `expr` directly (`int`
//     is `i32`, see AGENTS.md's "`int` is 32-bit" section, so this needs no
//     truncation/cast either way). This driver propagates that i32 result
//     directly as its own process exit code, so `func main() int { return
//     2 + 3 }` really does exit this process with code 5 - the same
//     convention C, Go's `os.Exit`, and every other compiled-to-a-real-
//     entry-point language already use.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"sync"
	"syscall"

	"llvm_lang/src/codegen"
	"llvm_lang/src/diag"
	"llvm_lang/src/lexer"
	"llvm_lang/src/parser"
	"llvm_lang/src/sema"

	"tinygo.org/x/go-llvm"
)

// Exit codes - see the package doc comment for what each means.
const (
	exitUsage   = 2
	exitCompile = 1
)

func main() {
	os.Exit(run(os.Args[1:], os.Stderr))
}

// usage is the short usage message printed on any usage error, and also
// documents the -emit-llvm flag (see the package doc comment for the full
// exit-code writeup).
const usage = "usage: llvmc [-emit-llvm] <file.llx>"

// run is main's testable body: it never calls os.Exit itself, so a test can
// invoke it directly and just inspect the returned code plus whatever was
// written to stderr.
func run(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("llvmc", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintln(stderr, usage)
	}

	emitLLVM := fs.Bool(
		"emit-llvm",
		false,
		"print the generated LLVM IR to stdout instead of JIT-executing it, then exit 0",
	)

	if err := fs.Parse(args); err != nil {
		return exitUsage
	}

	if fs.NArg() != 1 {
		fs.Usage()
		return exitUsage
	}

	path := fs.Arg(0)
	src, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(stderr, "llvmc: cannot read %s: %v\n", path, err)
		return exitUsage
	}

	return compileAndRun(path, string(src), stderr, *emitLLVM)
}

// compileAndRun drives the full pipeline for one source file: lexer.NewFile
// -> parser.ParseFile -> sema.Resolve -> sema.Check -> codegen.Generate,
// stopping at the first stage that reports an error-severity diagnostic (any
// diagnostics from a stage - warnings included - are printed even when that
// stage doesn't itself report an error, so nothing collected along the way
// is silently dropped). On full success, it verifies the generated module
// and then either JIT-executes its `main` (the default), or - when emitLLVM
// is set (the `-emit-llvm` flag) - prints the module's LLVM IR text to
// stdout and returns 0 without ever executing anything.
func compileAndRun(name, src string, stderr io.Writer, emitLLVM bool) int {
	file := lexer.NewFile(name, src)

	tree, pdiags := parser.ParseFile(file)
	if pdiags.Len() > 0 {
		printDiags(stderr, file, pdiags)
	}
	if pdiags.HasErrors() {
		return exitCompile
	}

	info, rdiags := sema.Resolve(tree)
	if rdiags.Len() > 0 {
		printDiags(stderr, file, rdiags)
	}
	if rdiags.HasErrors() {
		return exitCompile
	}

	cdiags := sema.Check(tree, info)
	if cdiags.Len() > 0 {
		printDiags(stderr, file, cdiags)
	}
	if cdiags.HasErrors() {
		return exitCompile
	}

	mod, gdiags := codegen.Generate(tree, info, name)
	if gdiags.Len() > 0 {
		printDiags(stderr, file, gdiags)
	}
	if gdiags.HasErrors() {
		mod.Dispose()
		return exitCompile
	}

	// llvm.PrintMessageAction matches main.go's own smoke-test usage exactly
	// (see that file): LLVM prints any verification failure to stderr itself,
	// we just need to know whether to stop here.
	if err := llvm.VerifyModule(mod.LLVM, llvm.PrintMessageAction); err != nil {
		fmt.Fprintf(stderr, "llvmc: module verification failed: %v\n", err)
		mod.Dispose()
		return exitCompile
	}

	// -emit-llvm: dump the verified module's IR text and stop here - this
	// path never reaches llvm.NewExecutionEngine, so a plain mod.Dispose()
	// (rather than the engine/context dance jitRunMain does below) is
	// correct, same as the diagnostic/verification-failure paths above.
	if emitLLVM {
		fmt.Print(mod.LLVM.String())
		mod.Dispose()
		return 0
	}

	code, err := jitRunMain(mod)
	if err != nil {
		fmt.Fprintf(stderr, "llvmc: %v\n", err)
		return exitCompile
	}
	return code
}

// printDiags renders every diagnostic in b (sorted by source position) via
// diag.FormatSnippet - "file:line:col: severity: message" plus the offending
// source line and a caret - separated by a blank line, to stderr (or
// wherever a test points it).
func printDiags(w io.Writer, file *lexer.File, b *diag.Bag) {
	for _, d := range b.Sorted() {
		fmt.Fprintln(w, diag.FormatSnippet(file, d))
		fmt.Fprintln(w)
	}
}

// jitInit performs LLVM's process-global JIT setup exactly once, mirroring
// src/codegen/codegen_test.go's own jitInit - these calls aren't meant to run
// more than once per process.
var jitInit sync.Once

func initJIT() {
	jitInit.Do(func() {
		llvm.LinkInMCJIT()
		if err := llvm.InitializeNativeTarget(); err != nil {
			panic(err)
		}
		if err := llvm.InitializeNativeAsmPrinter(); err != nil {
			panic(err)
		}
	})
}

// jitRunMain JIT-executes mod's `main` (the language's `func main()`, always
// lowered to a real, parameterless `i32 @main()` - see declareFuncSignature,
// src/codegen/func.go) and returns its i32 result as a plain int, ready to
// hand straight to os.Exit.
//
// This calls through the function's raw address via syscall.SyscallN rather
// than ExecutionEngine.RunFunction/GenericValue, mirroring
// src/codegen/codegen_test.go's runInt32 helper exactly (see its doc comment
// for why: RunFunction hits a real, fatal "Full-featured argument passing
// not supported yet!" for call shapes past a couple of scalar parameters).
// `main` always takes zero arguments, so there's no argument-marshaling
// concern here at all - just the zero-argument call and its i32 result.
//
// Module disposal: NewExecutionEngine takes ownership of mod.LLVM the moment
// it succeeds, so mod.Dispose() (which would call mod.LLVM.Dispose() itself)
// must never run afterward - that's a double free (see
// src/codegen/codegen_test.go's compileAndJIT doc comment, which hit exactly
// this). Once the engine exists, only the engine and then the owning
// Context get disposed, in that order, and never mod.Dispose() itself.
func jitRunMain(mod *codegen.Module) (int, error) {
	initJIT()

	engine, err := llvm.NewExecutionEngine(mod.LLVM)
	if err != nil {
		mod.Dispose()
		return 0, fmt.Errorf("failed to create execution engine: %w", err)
	}

	addr := engine.GetFunctionAddress("main")
	if addr == 0 {
		engine.Dispose()
		mod.Ctx.Dispose()
		return 0, fmt.Errorf("no main function found in module")
	}

	r1, _, _ := syscall.SyscallN(uintptr(addr))

	engine.Dispose()
	mod.Ctx.Dispose()

	return int(int32(uint32(r1))), nil
}
