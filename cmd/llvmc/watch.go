package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"syscall"
	"time"

	"llvm_lang/src/ast"
	"llvm_lang/src/codegen"
	"llvm_lang/src/compiler"
	"llvm_lang/src/enums"
	"llvm_lang/src/loader"

	"github.com/spf13/afero"
	"tinygo.org/x/go-llvm"
)

// errCompileFailed means diagnostics (or VerifyErr) were already printed.
var errCompileFailed = errors.New("compile failed")

// watchConfig is the -watch driver knobs parsed in run.
type watchConfig struct {
	EntryPath    string
	Optimize     bool
	LinkLibs     []string
	LinkDirs     []string
	InitName     string
	TickName     string
	InitRequired bool // true when -init was set explicitly (missing Init is then an error)
}

type fileStamp struct {
	modTime time.Time
	size    int64
}

// runWatch keeps one LLJIT instance alive, loads the user module under a
// ResourceTracker, and loops calling TickName (default Frame). On source
// change it recompiles and swaps the tracker (reset-on-reload: Init runs
// again). Sema/parse failures after a successful load keep the last-good
// module. main is unused under -watch.
//
// LockOSThread pins this goroutine to one OS thread: GLFW/OpenGL bind their
// context to whichever thread created it, so without this a scheduler
// migration between ticks leaves later calls on a threadless context.
func runWatch(cfg watchConfig, stderr io.Writer) int {
	runtime.LockOSThread()
	setStdoutUnbuffered()
	initJIT()

	jit, err := llvm.NewLLJIT(llvm.NewLLJITBuilder())
	if err != nil {
		fmt.Fprintf(stderr, "llvmc: failed to create LLJIT instance: %v\n", err)
		return exitCompile
	}
	defer jit.Dispose()

	if err := bindMinGWMainThunk(jit); err != nil {
		fmt.Fprintf(stderr, "llvmc: failed to bind __main thunk: %v\n", err)
		return exitCompile
	}
	if err := bindExtraLibraries(jit, cfg.LinkLibs, cfg.LinkDirs); err != nil {
		fmt.Fprintf(stderr, "llvmc: %v\n", err)
		return exitCompile
	}

	fs := afero.NewOsFs()
	var (
		rt      llvm.ResourceTracker
		hasRT   bool
		stamps  map[string]fileStamp
		reloads uint64
	)

	unload := func() {
		if !hasRT {
			return
		}
		_ = rt.Remove()
		rt.Release()
		hasRT = false
	}
	defer unload()

	// install checks Init/Tick's shape against trees (a pure AST check) before
	// touching the live module at all, so a shape failure - including Tick
	// missing entirely, e.g. a non-atomic editor write caught mid-save -
	// leaves the running module and hasRT untouched and reports via
	// errCompileFailed like any other reload failure; mod isn't owned by
	// anything yet at that point, so it must be disposed on those paths. Past
	// that, ORC can't host two modules defining the same symbols, so the old
	// tracker is removed first - a failure from there on cannot restore
	// last-good (see orcjit.go's NewThreadSafeModule doc comment for the
	// ownership handoff, and jitRunMain for the identical pattern).
	install := func(mod *codegen.Module, trees []*ast.Tree) error {
		fail := func(format string, args ...any) error {
			mod.Dispose()
			fmt.Fprintf(stderr, "llvmc: "+format+"\n", args...)
			return errCompileFailed
		}
		initFound := false
		if cfg.InitName != "" {
			found, verr := validateWatchEntrySig(trees, cfg.InitName, false)
			if verr != nil {
				return fail("%v", verr)
			}
			if !found && cfg.InitRequired {
				return fail("no %s function found in module", cfg.InitName)
			}
			initFound = found
		}
		tickFound, verr := validateWatchEntrySig(trees, cfg.TickName, true)
		if verr != nil {
			return fail("%v", verr)
		}
		if !tickFound {
			return fail("no %s function found in module", cfg.TickName)
		}

		unload()
		rt = jit.MainJITDylib().CreateResourceTracker()
		hasRT = true
		tsctx := llvm.NewThreadSafeContextFromContext(mod.Ctx)
		tsm := llvm.NewThreadSafeModule(mod.LLVM, tsctx)
		if err := jit.AddLLVMIRModuleWithRT(rt, tsm); err != nil {
			unload()
			return fmt.Errorf("failed to add module to LLJIT: %w", err)
		}
		if initAddr, err := jit.Lookup("llvm_lang.global_init"); err == nil {
			syscall.SyscallN(uintptr(initAddr))
		}
		if initFound {
			initAddr, err := jit.Lookup(cfg.InitName)
			if err != nil {
				unload()
				return fmt.Errorf("internal: %s passed signature validation but has no address: %w", cfg.InitName, err)
			}
			syscall.SyscallN(uintptr(initAddr))
		}
		return nil
	}

	tryCompile := func() (map[string]fileStamp, error) {
		prog, err := loader.LoadProgramWithOptions(fs, cfg.EntryPath, loaderOptionsFunc())
		if err != nil {
			return nil, err
		}
		paths := programSourcePaths(prog)
		newStamps, err := stampFiles(fs, paths)
		if err != nil {
			return nil, err
		}
		// Each reload gets its own module identifier - see
		// compiler.CompileProgramNamed's own doc comment for why reusing
		// cfg.EntryPath verbatim on every reload collides with LLJIT's
		// per-module ORC static-initializer bookkeeping once the package has
		// a non-constant global.
		moduleName := fmt.Sprintf("%s#%d", cfg.EntryPath, reloads)
		reloads++
		res := compiler.CompileProgramNamed(prog, cfg.Optimize, moduleName)
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
			return newStamps, errCompileFailed
		}
		res.TargetMachine.Dispose()
		if err := install(res.Module, res.Trees); err != nil {
			return newStamps, err
		}
		return newStamps, nil
	}

	stamps, err = tryCompile()
	if err != nil {
		if !errors.Is(err, errCompileFailed) {
			fmt.Fprintf(stderr, "llvmc: %v\n", err)
		}
		return exitCompile
	}

	for {
		changed, err := sourcesChanged(fs, stamps)
		if err != nil {
			fmt.Fprintf(stderr, "llvmc: %v\n", err)
			return exitCompile
		}
		if changed {
			newStamps, err := tryCompile()
			if err != nil {
				if newStamps != nil {
					stamps = newStamps
				}
				if errors.Is(err, errCompileFailed) && hasRT {
					fmt.Fprintf(stderr, "llvmc: reload failed; keeping last good module\n")
					time.Sleep(50 * time.Millisecond)
					continue
				}
				if !errors.Is(err, errCompileFailed) {
					fmt.Fprintf(stderr, "llvmc: %v\n", err)
				}
				return exitCompile
			}
			stamps = newStamps
		}

		addr, err := jit.Lookup(cfg.TickName)
		if err != nil {
			fmt.Fprintf(stderr, "llvmc: no %s function found in module\n", cfg.TickName)
			return exitCompile
		}
		r1, _, _ := syscall.SyscallN(uintptr(addr))
		code := int(int32(uint32(r1)))
		if code != 0 {
			return code
		}
	}
}

func programSourcePaths(prog *loader.Program) []string {
	var paths []string
	for _, pkg := range prog.Order {
		for _, f := range pkg.Files {
			paths = append(paths, f.Name)
		}
	}
	return paths
}

func stampFiles(fs afero.Fs, paths []string) (map[string]fileStamp, error) {
	out := make(map[string]fileStamp, len(paths))
	for _, p := range paths {
		info, err := fs.Stat(p)
		if err != nil {
			return nil, fmt.Errorf("stat %s: %w", p, err)
		}
		out[p] = fileStamp{
			modTime: info.ModTime(),
			size:    info.Size(),
		}
	}
	return out, nil
}

func sourcesChanged(fs afero.Fs, stamps map[string]fileStamp) (bool, error) {
	for p, prev := range stamps {
		info, err := fs.Stat(p)
		if err != nil {
			if os.IsNotExist(err) {
				return true, nil
			}
			return false, fmt.Errorf("stat %s: %w", p, err)
		}
		cur := fileStamp{
			modTime: info.ModTime(),
			size:    info.Size(),
		}
		if !cur.modTime.Equal(prev.modTime) || cur.size != prev.size {
			return true, nil
		}
	}
	return false, nil
}

// validateWatchEntrySig looks for a top-level, non-method function named
// name across trees, checking its declared shape purely at the AST level
// (no sema/codegen dependency needed - read through ast.Tree's own FuncDecl
// accessors, never a raw child index, since a wrong slot here silently
// validates nothing and lets a bad Frame reach the raw-syscall call below):
// not generic, zero parameters always, plus an "int" return type if wantInt,
// or no declared return type otherwise. found is false only when no function
// named name exists at all; err is set when one exists but its shape doesn't
// match what -watch requires of it.
func validateWatchEntrySig(trees []*ast.Tree, name string, wantInt bool) (found bool, err error) {
	for _, tree := range trees {
		for decl := range tree.TopLevelDeclsOfKind(enums.NodeKinds.FuncDecl) {
			if tree.FuncReceiver(decl) != ast.InvalidNode {
				continue
			}
			if tree.Text(tree.FuncName(decl)) != name {
				continue
			}
			if tree.FuncTypeParamList(decl) != ast.InvalidNode {
				// A template is never lowered at all, so every later check
				// would be validating a function that has no address.
				return true, fmt.Errorf("%s must not be generic", name)
			}
			if n := len(tree.Children(tree.FuncParamList(decl))); n != 0 {
				return true, fmt.Errorf("%s must take no parameters, got %d", name, n)
			}
			retNode := tree.FuncReturnType(decl)
			switch {
			case wantInt && (retNode == ast.InvalidNode || tree.Text(retNode) != "int"):
				return true, fmt.Errorf("%s must return int", name)
			case !wantInt && retNode != ast.InvalidNode:
				return true, fmt.Errorf("%s must not declare a return type", name)
			}
			return true, nil
		}
	}
	return false, nil
}
