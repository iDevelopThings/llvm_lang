// Command llvmc is the first real, human-facing way to run an llvm_lang
// program: given a path - a single source file, or a directory (see
// LANGUAGE.md's "Multi-file packages" section: a file resolves to its own
// containing directory, so both forms compile the identical set of files) -
// it resolves the package's full file list (src/loader), drives every file
// through the full compiler pipeline (lexer -> parser per file, then
// sema.ResolvePackage/ResolveProgram -> sema.CheckProgram ->
// codegen.GeneratePackage across all of them together - the actual pipeline
// orchestration lives in src/compiler, not this package; see that package's
// own doc comment for why), and, on full success, JIT-executes the resulting
// shared module's `func main()` directly in this process - so the program's
// own `print` calls (which lower to real libc `printf` calls, see
// AGENTS.md's codegen section) write to this process's real stdout,
// something a `go test`-hosted JIT call can't easily show (see BLOCKERS.md's
// codegen-phase entry 7). This package itself is now the thinnest possible
// CLI shell on top of src/loader and src/compiler: flag parsing, printing
// diagnostics/IR, exit codes, and JIT execution - no pipeline logic of its
// own.
//
// Usage:
//
//	llvmc <file.llx>
//	llvmc <directory>
//	llvmc -emit-llvm <file.llx or directory>
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
// reusing it here would be a real (and confusing) collision with that. The
// compiler pipeline proper still doesn't inspect a file's extension at all -
// lexer.NewFile just takes a name (used only for diagnostics) and the source
// text - src/loader's directory scan is the one place ".llx" is actually
// checked for, since resolving a bare directory path needs a concrete
// answer to "which files in here are part of the package".
//
// Exit codes:
//   - 2: usage error - no path argument, an unrecognized flag, the path
//     couldn't be resolved to a real file/directory, or its resolved
//     directory has zero .llx files in it. A short usage message is printed
//     to stderr; nothing is compiled.
//   - 1: a compile-time diagnostic - the lexer, parser,
//     sema.ResolvePackage/ResolveProgram, sema.CheckProgram (src/compiler's
//     finishPipeline always calls CheckProgram, even for a single, import-
//     less package - treePackage is simply nil then, since CheckPackage is
//     just CheckProgram(trees, infos, nil)), or codegen.GeneratePackage stage
//     reported at least one error-severity diagnostic in any file. Every
//     diagnostic collected by whichever stage failed first is printed to
//     stderr via diag.FormatSnippet (a "file:line:col: severity: message" header plus
//     the offending source line and a caret), and no later stage runs. This
//     also covers the module failing LLVM's own verifier, and the module
//     JIT-executing but containing no `main` function to run. With
//     -emit-llvm, this is the only non-zero exit code possible - a verified
//     module's IR is always printed and this process always exits 0
//     afterward.
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
	"llvm_lang/src/compiler"
	"llvm_lang/src/diag"
	"llvm_lang/src/lexer"
	"llvm_lang/src/loader"

	"github.com/spf13/afero"
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
const usage = "usage: llvmc [-emit-llvm] <file.llx | directory>"

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
	prog, err := loader.LoadProgram(afero.NewOsFs(), path)
	if err != nil {
		fmt.Fprintf(stderr, "llvmc: %v\n", err)
		return exitUsage
	}

	return compileAndRunProgram(prog, stderr, *emitLLVM)
}

// compileAndRun drives the full pipeline for a single source file - the
// one-file case of compileAndRunPackage, kept as its own entry point purely
// so this package's existing in-process tests (which build a source string
// directly, with no need for a real file/directory on disk) don't have to
// go through the loader package at all.
func compileAndRun(name, src string, stderr io.Writer, emitLLVM bool) int {
	return compileAndRunPackage([]loader.SourceFile{{Name: name, Src: src}}, stderr, emitLLVM)
}

// compileAndRunPackage drives the full pipeline for every file in files, as
// one package with no imports of its own (see LANGUAGE.md's "Multi-file
// packages" section) - the actual pipeline orchestration lives in
// src/compiler (compiler.CompilePackage); this is just that call plus this
// CLI's own diagnostic-printing/JIT-or-emit tail (finish). Kept as its own
// entry point (rather than routing through loader.LoadProgram/
// compileAndRunProgram) purely so this package's existing in-process tests,
// which build source strings directly with no real file/directory on disk,
// don't need one - see compileAndRunProgram for the real multi-*package*
// (import-aware) driver run actually uses.
func compileAndRunPackage(files []loader.SourceFile, stderr io.Writer, emitLLVM bool) int {
	return finish(compiler.CompilePackage(files), stderr, emitLLVM)
}

// compileAndRunProgram drives the full pipeline for prog - a whole program,
// potentially spanning several packages linked by `import` declarations
// (see loader.LoadProgram, which discovers/parses/dedups/cycle-checks the
// entire transitive import graph up front - every file's own parse
// diagnostics were already collected then, not here) - via
// compiler.CompileProgram, then this CLI's own diagnostic-printing/
// JIT-or-emit tail (finish), exactly like compileAndRunPackage.
func compileAndRunProgram(prog *loader.Program, stderr io.Writer, emitLLVM bool) int {
	return finish(compiler.CompileProgram(prog), stderr, emitLLVM)
}

// finish is this CLI's own tail shared by compileAndRunPackage/
// compileAndRunProgram, once src/compiler has already driven res's trees
// through the whole resolve/check/codegen/verify pipeline: print every
// tree's diagnostics (any diagnostics collected along the way - warnings
// included - are printed even when the pipeline ultimately succeeded, so
// nothing collected is silently dropped), then, if res.Module is nil,
// report why (a real diagnostic, or - more rarely - res.VerifyErr) and
// return the compile-error exit code; otherwise either dump the verified
// module's LLVM IR text (-emit-llvm) or JIT-execute its `main` (the
// default).
//
// This is the only place left in this package that's CLI-specific rather
// than pipeline orchestration: printing (io.Writer/diag.FormatSnippet),
// exit codes, -emit-llvm's stdout dump, and JIT execution (jitRunMain) all
// belong here, not in src/compiler.
func finish(res *compiler.Result, stderr io.Writer, emitLLVM bool) int {
	for _, tree := range res.Trees {
		b := res.Diags[tree]
		if b.Len() > 0 {
			printDiags(stderr, tree.File, b)
		}
	}

	if res.Module == nil {
		if res.VerifyErr != nil {
			fmt.Fprintf(stderr, "llvmc: %v\n", res.VerifyErr)
		}
		return exitCompile
	}

	// -emit-llvm: dump the verified module's IR text and stop here - this
	// path never reaches llvm.NewLLJIT, so a plain res.Module.Dispose()
	// (rather than the ThreadSafeContext/ThreadSafeModule/LLJIT ownership
	// transfer jitRunMain does below) is correct, same as the diagnostic/
	// verification-failure paths above.
	if emitLLVM {
		fmt.Print(res.Module.LLVM.String())
		res.Module.Dispose()
		return 0
	}

	code, err := jitRunMain(res.Module)
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
		if err := llvm.InitializeNativeTarget(); err != nil {
			panic(err)
		}
		if err := llvm.InitializeNativeAsmPrinter(); err != nil {
			panic(err)
		}
	})
}

// bindMinGWMainThunk works around a real MinGW/GCC ABI compatibility quirk,
// found empirically while switching this project's JIT engine from MCJIT to
// LLJIT (see DECISIONS.md's dated "JIT execution: LLJIT" entry): LLVM's
// backend, when compiling a function literally named `main` for a
// `*-windows-gnu` target - this project's own host, and the exact target
// JITTargetMachineBuilder's host-detection picks - auto-inserts a call to
// `__main()` at that function's very start, the same thing GCC's own
// frontend does for real MinGW-linked programs, there to run static
// C++-style constructors via a much older, completely different convention
// than this project's own `@llvm.global_ctors` (see CODEGEN.md's "Global
// var initializers" section). MCJIT apparently never took this same code
// path. This project has no use for whatever `__main` would normally do and
// never defines it itself, so without this, materializing `main` at all
// fails outright: "JIT session error: Symbols not found: [ __main ]".
//
// `__main` just needs to resolve to *something* harmless - bound here to
// libc's own `rand` (real, already resolvable via the process-symbol
// generator attached below, and safe to call with zero arguments and an
// ignored result, exactly matching the shape of the auto-inserted call
// site). This is unrelated to actually running llvm_lang.global_init: that
// still happens through its own explicit Lookup-and-call in jitRunMain,
// since binding __main to global_init directly wouldn't help any JIT'd
// function *other* than main itself (this driver only ever calls main, but
// this package's own test helpers - src/codegen/codegen_test.go's
// compileAndJIT and friends - routinely call some other, arbitrarily named
// function directly, which never goes through this __main mechanism at
// all).
func bindMinGWMainThunk(jit llvm.LLJIT) error {
	dg, err := llvm.NewDynamicLibrarySearchGeneratorForProcess(jit.GlobalPrefix())
	if err != nil {
		return err
	}
	jit.MainJITDylib().AddGenerator(dg)

	randAddr, err := jit.Lookup("rand")
	if err != nil {
		return err
	}

	name := jit.ExecutionSession().Intern("__main")
	defer name.Release()
	mu := llvm.AbsoluteSymbols([]llvm.AbsoluteSymbol{
		{
			Name: name,
			Value: llvm.EvaluatedSymbol{
				Address: randAddr,
				Flags:   llvm.SymbolFlags{Generic: llvm.SymbolFlagExported | llvm.SymbolFlagCallable},
			},
		},
	})
	if err := jit.MainJITDylib().Define(mu); err != nil {
		mu.Dispose()
		return err
	}
	return nil
}

// jitRunMain JIT-executes mod's `main` (the language's `func main()`, always
// lowered to a real, parameterless `i32 @main()` - see declareFuncSignature,
// src/codegen/func.go) and returns its i32 result as a plain int, ready to
// hand straight to os.Exit.
//
// This uses go-llvm's LLJIT bindings (third_party/go-llvm/orcjit.go) -
// ORCv2, LLVM's current JIT infrastructure - rather than the legacy
// MCJIT-based ExecutionEngine this driver used before (see DECISIONS.md's
// dated "JIT execution: LLJIT" entry for the full why).
//
// llvm_lang.global_init (see src/codegen/globalinit.go's genGlobalCtors) is
// looked up and called directly, exactly like main itself, before main runs.
// A normal linked/loaded program's own C runtime startup sequence would scan
// and call every entry in `@llvm.global_ctors` (see CODEGEN.md's "Global var
// initializers" section) before ever reaching main on its own - unlike
// MCJIT's ExecutionEngine, LLJIT has no RunStaticConstructors-style call to
// trigger that automatically, so this looks up the well-known synthesized
// function by name instead of walking the ctors array at all (the array
// itself is still emitted, for a real linked/loaded program's benefit - see
// genGlobalCtors). A module with no non-constant globals has no such
// function to find in the first place, so a failed Lookup here just means
// there was nothing to run, not a real error.
//
// This calls through the resolved address via syscall.SyscallN, same as
// before LLJIT.Lookup hands back a raw address exactly like
// ExecutionEngine.GetFunctionAddress did, and actually invoking it is
// deliberately out of scope for either JIT engine's own API (see orcjit.go's
// own doc comment on Lookup) - so this driver still brings its own call
// mechanism, unchanged. `main` always takes zero arguments, so there's no
// argument-marshaling concern here at all.
//
// Module/context disposal: wrapping mod.Ctx in a ThreadSafeContext and
// mod.LLVM in a ThreadSafeModule (see orcjit.go) transfers ownership of both
// to jit the moment AddLLVMIRModule succeeds - mod.Dispose() must never run
// afterward, that's a double free. Unlike MCJIT (which only ever took
// ownership of the Module, leaving the Context for the caller to dispose
// separately), a single jit.Dispose() tears down the module and context
// together, in the correct order - no separate mod.Ctx.Dispose() call needed
// at all once jit exists.
func jitRunMain(mod *codegen.Module) (int, error) {
	initJIT()

	jit, err := llvm.NewLLJIT(llvm.NewLLJITBuilder())
	if err != nil {
		mod.Dispose()
		return 0, fmt.Errorf("failed to create LLJIT instance: %w", err)
	}

	if err := bindMinGWMainThunk(jit); err != nil {
		// mod.Ctx/mod.LLVM haven't been wrapped/handed to jit yet at this
		// point (that only happens below, via AddLLVMIRModule) - mod is
		// still fully owned here, so it needs its own Dispose alongside
		// jit's, unlike the AddLLVMIRModule failure branch further down.
		mod.Dispose()
		jit.Dispose()
		return 0, fmt.Errorf("failed to bind __main thunk: %w", err)
	}

	tsctx := llvm.NewThreadSafeContextFromContext(mod.Ctx)
	tsm := llvm.NewThreadSafeModule(mod.LLVM, tsctx)
	if err := jit.AddLLVMIRModule(jit.MainJITDylib(), tsm); err != nil {
		jit.Dispose()
		return 0, fmt.Errorf("failed to add module to LLJIT: %w", err)
	}

	if initAddr, err := jit.Lookup("llvm_lang.global_init"); err == nil {
		syscall.SyscallN(uintptr(initAddr))
	}

	addr, err := jit.Lookup("main")
	if err != nil {
		jit.Dispose()
		return 0, fmt.Errorf("no main function found in module")
	}

	r1, _, _ := syscall.SyscallN(uintptr(addr))
	jit.Dispose()

	return int(int32(uint32(r1))), nil
}
