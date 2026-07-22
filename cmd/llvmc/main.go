// Command llvmc is the first real, human-facing way to run an llvm_lang
// program: given a path - a single source file, or a directory (see
// LANGUAGE.md's "Multi-file packages" section: a file resolves to its own
// containing directory, so both forms compile the identical set of files) -
// it resolves the package's full file list (src/loader), drives every file
// through the full compiler pipeline (lexer -> parser per file, then
// sema.ResolvePackage -> sema.CheckPackage -> codegen.GeneratePackage across
// all of them together), and, on full success, JIT-executes the resulting
// shared module's `func main()` directly in this process - so the program's
// own `print` calls (which lower to real libc `printf` calls, see
// AGENTS.md's codegen section) write to this process's real stdout,
// something a `go test`-hosted JIT call can't easily show (see BLOCKERS.md's
// codegen-phase entry 7).
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
//   - 1: a compile-time diagnostic - the lexer, parser, sema.ResolvePackage,
//     sema.CheckPackage, or codegen.GeneratePackage stage reported at least
//     one error-severity diagnostic in any file. Every diagnostic collected
//     by whichever stage failed first is printed to stderr via
//     diag.FormatSnippet (a "file:line:col: severity: message" header plus
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

	"llvm_lang/src/ast"
	"llvm_lang/src/codegen"
	"llvm_lang/src/diag"
	"llvm_lang/src/lexer"
	"llvm_lang/src/loader"
	"llvm_lang/src/parser"
	"llvm_lang/src/sema"

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
// packages" section): lexer.NewFile -> parser.ParseFile per file, then
// sema.ResolvePackage -> runPipeline. Kept as its own entry point (rather
// than routing through loader.LoadProgram/compileAndRunProgram) purely so
// this package's existing in-process tests, which build source strings
// directly with no real file/directory on disk, don't need one - see
// compileAndRunProgram for the real multi-*package* (import-aware) driver
// run actually uses.
func compileAndRunPackage(files []loader.SourceFile, stderr io.Writer, emitLLVM bool) int {
	trees := make([]*ast.Tree, len(files))
	anyParseErrors := false

	for i, f := range files {
		lf := lexer.NewFile(f.Name, f.Src)
		tree, pdiags := parser.ParseFile(lf)
		if pdiags.Len() > 0 {
			printDiags(stderr, lf, pdiags)
		}
		if pdiags.HasErrors() {
			anyParseErrors = true
		}
		trees[i] = tree
	}
	if anyParseErrors {
		return exitCompile
	}

	infos, rdiags := sema.ResolvePackage(trees)
	// moduleName matches this driver's own single-file convention of using
	// the compiled path as the module's name - the package's first file is
	// as reasonable a choice as any for a multi-file package. treePackage is
	// nil - a single package has no cross-package export enforcement to do
	// at all (see sema.CheckProgram's own doc comment).
	return runPipeline(trees, infos, rdiags, nil, files[0].Name, stderr, emitLLVM)
}

// compileAndRunProgram drives the full pipeline for prog - a whole program,
// potentially spanning several packages linked by `import` declarations
// (see loader.LoadProgram, which discovers/parses/dedups/cycle-checks the
// entire transitive import graph up front - every file's own parse
// diagnostics were already collected then, not here).
//
// prog.Order already lists every package in dependency order (see its own
// doc comment), which is exactly the order sema.ResolveProgram needs: each
// package's own PackageUnit is built and resolved before any package that
// imports it, so a FileImport's TargetKey (this driver uses each package's
// own resolved Dir as that key) always names an already-resolved unit.
//
// Every package's trees are then flattened into one slice and driven through
// the exact same sema.CheckProgram/codegen.GeneratePackage/verify/execute
// tail every other entry point in this file shares (runPipeline) - see
// CODEGEN.md's "Multi-file packages" section for why one shared Module needs
// no package-boundary awareness at all, the same reasoning extended one
// level up unchanged.
func compileAndRunProgram(prog *loader.Program, stderr io.Writer, emitLLVM bool) int {
	var trees []*ast.Tree
	units := make([]*sema.PackageUnit, 0, len(prog.Order))
	anyParseErrors := false

	for _, pkg := range prog.Order {
		unitTrees := make([]*ast.Tree, len(pkg.Files))
		var fileImports map[*ast.Tree][]sema.FileImport

		for i, f := range pkg.Files {
			unitTrees[i] = f.Tree
			trees = append(trees, f.Tree)

			if f.Diags.Len() > 0 {
				printDiags(stderr, f.Tree.File, f.Diags)
			}
			if f.Diags.HasErrors() {
				anyParseErrors = true
			}

			if len(f.Imports) == 0 {
				continue
			}
			if fileImports == nil {
				fileImports = make(map[*ast.Tree][]sema.FileImport, len(pkg.Files))
			}
			imps := make([]sema.FileImport, len(f.Imports))
			for j, imp := range f.Imports {
				imps[j] = sema.FileImport{
					LocalName: imp.LocalName,
					TargetKey: imp.Package.Dir,
				}
			}
			fileImports[f.Tree] = imps
		}

		units = append(units, &sema.PackageUnit{
			Key:         pkg.Dir,
			Name:        pkg.Name,
			Trees:       unitTrees,
			FileImports: fileImports,
		})
	}
	if anyParseErrors {
		return exitCompile
	}

	infos, rdiags, _, treePackage := sema.ResolveProgram(units)
	// moduleName matches compileAndRunPackage's own convention: the entry
	// package's first file is as reasonable a choice as any for the whole
	// program's module name.
	moduleName := prog.Entry.Files[0].Name
	return runPipeline(trees, infos, rdiags, treePackage, moduleName, stderr, emitLLVM)
}

// runPipeline drives the shared tail of the compiler pipeline, once trees
// have already been resolved (infos/rdiags) by whichever of
// compileAndRunPackage/compileAndRunProgram's own sema entry point:
// sema.CheckProgram (treePackage nil disables its cross-package export
// enforcement entirely - see its own doc comment - exactly right for
// compileAndRunPackage's plain single, import-less package) ->
// codegen.GeneratePackage -> LLVM's own module verifier -> either
// JIT-execution (the default) or -emit-llvm's IR dump, stopping at the
// first stage that reports an error-severity diagnostic in any file (any
// diagnostics from a stage - warnings included - are printed even when that
// stage doesn't itself report an error, so nothing collected along the way
// is silently dropped).
func runPipeline(trees []*ast.Tree, infos map[*ast.Tree]*sema.Info, rdiags map[*ast.Tree]*diag.Bag, treePackage map[*ast.Tree]*sema.Scope, moduleName string, stderr io.Writer, emitLLVM bool) int {
	if printAnyDiags(stderr, trees, rdiags) {
		return exitCompile
	}

	cdiags := sema.CheckProgram(trees, infos, treePackage)
	if printAnyDiags(stderr, trees, cdiags) {
		return exitCompile
	}

	mod, gdiags := codegen.GeneratePackage(trees, infos, moduleName)
	if printAnyDiags(stderr, trees, gdiags) {
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

// printAnyDiags prints every file's diagnostics (see printDiags) from a
// per-tree diagnostic map, using each tree's own *lexer.File (ast.Tree.File)
// to decode its diagnostics' Pos values into a line/column (see diag.Bag's
// own doc comment: a diagnostic's position is only meaningful relative to
// the file it was reported against). Reports whether any file had at least
// one error-severity diagnostic, so the caller knows whether to stop the
// pipeline.
func printAnyDiags(w io.Writer, trees []*ast.Tree, diags map[*ast.Tree]*diag.Bag) bool {
	hasErrors := false
	for _, tree := range trees {
		b := diags[tree]
		if b.Len() > 0 {
			printDiags(w, tree.File, b)
		}
		if b.HasErrors() {
			hasErrors = true
		}
	}
	return hasErrors
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
