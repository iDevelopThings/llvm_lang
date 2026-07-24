package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"
	"time"

	"llvm_lang/src/codegen"
	"llvm_lang/src/compiler"
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
func runWatch(cfg watchConfig, stderr io.Writer) int {
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
		rt     llvm.ResourceTracker
		hasRT  bool
		stamps map[string]fileStamp
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

	// install replaces the live module. ORC cannot host two modules that
	// define the same symbols, so the old tracker is removed first; a
	// failure after that point cannot restore last-good (compile errors
	// never reach here - they return before install).
	install := func(mod *codegen.Module) error {
		unload()
		rt = jit.MainJITDylib().CreateResourceTracker()
		hasRT = true
		tsctx := llvm.NewThreadSafeContextFromContext(mod.Ctx)
		tsm := llvm.NewThreadSafeModule(mod.LLVM, tsctx)
		if err := jit.AddLLVMIRModuleWithRT(rt, tsm); err != nil {
			unload()
			mod.Dispose()
			return fmt.Errorf("failed to add module to LLJIT: %w", err)
		}
		if initAddr, err := jit.Lookup("llvm_lang.global_init"); err == nil {
			syscall.SyscallN(uintptr(initAddr))
		}
		if cfg.InitName != "" {
			initAddr, err := jit.Lookup(cfg.InitName)
			if err != nil {
				if cfg.InitRequired {
					unload()
					return fmt.Errorf("no %s function found in module", cfg.InitName)
				}
			} else {
				syscall.SyscallN(uintptr(initAddr))
			}
		}
		if _, err := jit.Lookup(cfg.TickName); err != nil {
			unload()
			return fmt.Errorf("no %s function found in module", cfg.TickName)
		}
		return nil
	}

	tryCompile := func() (map[string]fileStamp, error) {
		prog, err := loader.LoadProgram(fs, cfg.EntryPath)
		if err != nil {
			return nil, err
		}
		paths := programSourcePaths(prog)
		newStamps, err := stampFiles(fs, paths)
		if err != nil {
			return nil, err
		}
		res := compiler.CompileProgram(prog, cfg.Optimize)
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
		if err := install(res.Module); err != nil {
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
