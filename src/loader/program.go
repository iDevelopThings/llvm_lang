package loader

import (
	"fmt"
	"iter"
	"os"
	"path"
	"path/filepath"
	"strings"

	"llvm_lang/src/ast"
	"llvm_lang/src/diag"
	"llvm_lang/src/enums"
	"llvm_lang/src/lexer"
	"llvm_lang/src/parser"

	"github.com/spf13/afero"
)

// File is one already-parsed package member: the parser's own output
// (Tree/Diags) plus every import binding it declared, each already resolved
// to its target Package - see LANGUAGE.md's "Imports" section for the
// language-level rule (an import binding is file-scoped, not
// package-scoped: a sibling file that doesn't itself write `import "./x"`
// can't see this file's own Imports).
type File struct {
	Name    string
	Tree    *ast.Tree
	Diags   *diag.Bag
	Imports []Import
}

// Import is one resolved `import "path"` binding.
type Import struct {
	// Path is the raw import path text exactly as written in source (e.g.
	// "./mathutils").
	Path string
	// LocalName is this import's local name in the file that declared it -
	// the path's own last segment (Go's own convention; there's no
	// aliasing syntax yet - see LANGUAGE.md's "Imports" section).
	LocalName string
	// Package is the already-loaded target package.
	Package *Package
}

// Package is one loaded llvm_lang package: every .llx file directly in Dir,
// already parsed, with every file's own imports already resolved to their
// target Packages.
type Package struct {
	// Dir is this package's resolved, cleaned directory path - the identity
	// key LoadProgram dedups a diamond dependency by and detects a cycle
	// against.
	Dir string
	// Name is this package's local name - Dir's own last path segment
	// (Go's own directory-as-package-name convention, matching
	// LANGUAGE.md's "Multi-file packages" section).
	Name string
	// FS is the filesystem this package's own Files were read from - the
	// plain fs passed to LoadProgram for an ordinary relative-path package,
	// but a scheme root's own afero.Fs (see schemeRoot) for a std:/lib:
	// import, which is a genuinely different filesystem than the entry
	// package's. Every File.Name is only ever meaningful when resolved
	// against THIS Fs, never assumed to be the same one another package in
	// the same Program used - cmd/llvmc's own -watch file-staleness
	// tracking needs this to stat each file against the right filesystem.
	FS    afero.Fs
	Files []*File
}

// Program is a whole compiled program: every package reachable, transitively,
// from the entry package.
type Program struct {
	Entry *Package
	// Order lists every package exactly once, in dependency order: a
	// package never appears before any package it imports (a real
	// post-order graph walk - see loadPackage). sema.ResolveProgram needs
	// this exact order, since an importing package's file scope has to be
	// wired up against its dependency's already-resolved package surface.
	Order []*Package
}

// Files iterates every source file across every package in Order, paired
// with the afero.Fs it must be read from - see Package.FS's own doc comment
// for why a caller can't assume one shared fs covers every file in a
// Program. cmd/llvmc's own -watch reload detection is the first caller.
func (p *Program) Files() iter.Seq2[afero.Fs, string] {
	return func(yield func(afero.Fs, string) bool) {
		for _, pkg := range p.Order {
			for _, f := range pkg.Files {
				if !yield(pkg.FS, f.Name) {
					return
				}
			}
		}
	}
}

// LoadProgram resolves root (a single .llx file, or a directory - see Load's
// own doc comment) to the entry package, then transitively discovers,
// parses, and loads every package it imports, recursively - deduping a
// diamond dependency (two different packages importing the same third one)
// by directory identity so it's only ever loaded once, and rejecting a real
// import cycle with a clear error naming the cycle (e.g.
// "loader: import cycle: a -> b -> a") rather than looping forever or
// overflowing the stack.
//
// An ordinary import path is resolved relative to the *importing file's own
// directory* - not the entry package's directory, and not any notion of a
// module root/manifest - matching this project's confirmed design (see
// DECISIONS.md). A "scheme:path" import (e.g. "std:mathutil") is the one
// exception: it resolves against that scheme's own registered root instead,
// completely independent of the importing file's location - see
// resolveImportPath and DECISIONS.md's "std:/lib: import schemes" entry.
//
// Neither scheme is backed by anything here - a "std:"/"lib:" import fails
// with a clear "not available" error unless the caller uses
// LoadProgramWithOptions instead, supplying a real StdFS (see StdlibFS).
// Keeping LoadProgram itself scheme-free means it stays deterministic and
// fully testable with no dependency on the running process's own binary
// location - only real production entry points (cmd/llvmc, cmd/llvmc-lsp)
// need that.
func LoadProgram(fs afero.Fs, root string) (*Program, error) {
	return LoadProgramWithOptions(fs, root, Options{})
}

// Options configures LoadProgramWithOptions' scheme-qualified import roots.
type Options struct {
	// StdFS is where a "std:" import resolves against - nil (the zero
	// value) means "std:" isn't available, same as the still-unimplemented
	// "lib:" scheme. Real callers get one from StdlibFS.
	StdFS afero.Fs
	// TestMode splices every tests{} block belonging to the entry package
	// (never an imported dependency's own tests{} blocks - see loadPackage's
	// isEntry parameter) into ordinary top-level declarations, instead of
	// leaving each one wrapped in an invisible TestBlockDecl node - see
	// parser.ParseFile and LANGUAGE.md's "tests{}" section.
	TestMode bool
}

// LoadProgramWithOptions is LoadProgram with its scheme-qualified import
// roots (see LANGUAGE.md's "Imports" section) explicitly supplied, rather
// than left unbacked - see LoadProgram's own doc comment for why the two
// are separate entry points.
func LoadProgramWithOptions(fs afero.Fs, root string, opts Options) (*Program, error) {
	dir, err := resolvePackageDir(fs, root)
	if err != nil {
		return nil, err
	}

	schemes := map[string]schemeRoot{
		"lib": {Reason: "third-party package imports aren't implemented yet"},
	}
	if opts.StdFS != nil {
		schemes["std"] = schemeRoot{FS: opts.StdFS}
	} else {
		schemes["std"] = schemeRoot{Reason: "no standard library location was configured for this run"}
	}

	l := &programLoader{
		fs:       fs,
		schemes:  schemes,
		testMode: opts.TestMode,
		loaded:   make(map[string]*Package),
		loading:  make(map[string]bool),
	}
	entry, err := l.loadPackage(resolvedImport{fs: fs, dir: dir, key: dir}, true)
	if err != nil {
		return nil, err
	}
	return &Program{Entry: entry, Order: l.order}, nil
}

// StdlibFS locates this compiler's own standard library on disk: a "std"
// directory expected to sit right next to the running executable (see
// DECISIONS.md's "std:/lib: import schemes" entry - the compiler ships as a
// plain executable plus a sibling std/ directory, not a single
// self-contained binary, exactly like this repo's own layout: llvmc.exe/
// llvmc-lsp.exe build to the repo root, right next to std/ itself). Uses
// os.Executable/os.Stat directly rather than going through an afero.Fs (see
// AGENTS.md's standing afero convention) because there is no afero
// equivalent for "where is my own running binary" - it's process
// introspection, not file content access; the actual std/ directory check
// and every subsequent read of it still go through afero (via the returned
// afero.Fs, a real OS filesystem rooted at std/).
func StdlibFS() (afero.Fs, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("loader: cannot locate this executable's own path: %w", err)
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return nil, fmt.Errorf("loader: cannot resolve this executable's real path: %w", err)
	}
	return stdlibFSNextTo(exe)
}

// stdlibFSNextTo is StdlibFS' own real logic, split out so a test can drive
// it against a real temp-directory path instead of the actual running
// test binary's own location (which never has a real std/ sibling) -
// StdlibFS itself is just this plus the os.Executable/EvalSymlinks lookup.
func stdlibFSNextTo(exePath string) (afero.Fs, error) {
	stdDir := filepath.Join(filepath.Dir(exePath), "std")

	fs := afero.NewOsFs()
	info, err := fs.Stat(stdDir)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("loader: no std/ directory found next to this executable (%s)", stdDir)
	}
	return afero.NewBasePathFs(fs, stdDir), nil
}

// schemeRoot is one registered "scheme:" import root - see
// resolveImportPath. FS is nil when this scheme isn't available for this
// run (a genuinely unimplemented scheme like "lib", or "std" when no
// StdFS was supplied) - Reason explains why, surfaced verbatim in the
// resulting diagnostic rather than a generic "unknown import scheme"
// (reserved for a scheme name this project doesn't recognize at all).
type schemeRoot struct {
	FS     afero.Fs
	Reason string
}

// programLoader carries LoadProgram's recursive-discovery state: loaded
// dedups a fully-loaded package by key; loading/stack together detect a
// real import cycle (loading alone can't tell a cycle apart from a harmless
// diamond re-visit - a key already fully in loaded is a diamond, a key still
// marked loading when revisited is a genuine cycle).
type programLoader struct {
	fs       afero.Fs
	schemes  map[string]schemeRoot
	testMode bool
	loaded   map[string]*Package
	loading  map[string]bool
	stack    []string
	order    []*Package
}

// resolvedImport is one import's own already-resolved location: which
// filesystem to read it from, the directory within that filesystem, and a
// dedup/cycle-detection key that can never collide across two different
// filesystems even if their directory strings happen to look identical
// (e.g. a project-local "std/mathutil" vs the compiler's own bundled
// "std/mathutil") - see resolveImportPath.
type resolvedImport struct {
	fs  afero.Fs
	dir string
	key string
}

// resolveImportPath resolves importPath (as written in fileDir's own
// source) to a concrete location. A "scheme:rest" path (see LANGUAGE.md's
// "Imports" section) resolves against that scheme's own registered root,
// completely independent of fileDir; anything else resolves the original
// way, relative to fileDir.
func (l *programLoader) resolveImportPath(fileDir, importPath string) (resolvedImport, error) {
	if scheme, rest, ok := splitScheme(importPath); ok {
		root, known := l.schemes[scheme]
		if !known {
			return resolvedImport{}, fmt.Errorf("loader: unknown import scheme %q in %q", scheme, importPath)
		}
		if root.FS == nil {
			return resolvedImport{}, fmt.Errorf("loader: %q: %s", importPath, root.Reason)
		}
		dir := filepath.Clean(filepath.FromSlash(rest))
		return resolvedImport{fs: root.FS, dir: dir, key: scheme + ":" + dir}, nil
	}
	dir := filepath.Clean(filepath.Join(fileDir, filepath.FromSlash(importPath)))
	return resolvedImport{fs: l.fs, dir: dir, key: dir}, nil
}

// splitScheme reports whether importPath starts with a "scheme:" prefix - a
// colon appearing before any '/' in the path - splitting it into the scheme
// name and the remainder. A colon appearing after the first '/' (or not at
// all) is never a scheme prefix, just an ordinary relative path: a colon
// there is illegal in a Windows path outright, and this project only
// targets Windows/mingw64 (see DECISIONS.md), so a real relative path can
// never legitimately need one there.
func splitScheme(importPath string) (scheme, rest string, ok bool) {
	i := strings.IndexAny(importPath, ":/")
	if i < 0 || importPath[i] != ':' {
		return "", "", false
	}
	return importPath[:i], importPath[i+1:], true
}

// loadPackage loads loc's package (recursively resolving/loading every
// import it or its own dependencies declare) and returns it, reusing an
// already-loaded package by loc.key rather than reloading it - see
// LoadProgram's own doc comment. isEntry is true only for the very first
// call (root itself); every import this package or a transitive dependency
// declares recurses with isEntry false, so l.testMode only ever splices the
// entry package's own tests{} blocks, never an imported dependency's.
func (l *programLoader) loadPackage(loc resolvedImport, isEntry bool) (*Package, error) {
	if pkg, ok := l.loaded[loc.key]; ok {
		return pkg, nil // diamond dependency - already fully loaded once
	}
	if l.loading[loc.key] {
		return nil, fmt.Errorf("loader: import cycle: %s", formatCycle(l.stack, loc.dir))
	}

	l.loading[loc.key] = true
	l.stack = append(l.stack, loc.dir)
	defer func() {
		l.stack = l.stack[:len(l.stack)-1]
		delete(l.loading, loc.key)
	}()

	srcFiles, err := Load(loc.fs, loc.dir)
	if err != nil {
		return nil, err
	}

	pkg := &Package{
		Dir:  loc.dir,
		Name: filepath.Base(loc.dir),
		FS:   loc.fs,
	}

	for _, sf := range srcFiles {
		lf := lexer.NewFile(sf.Name, sf.Src)
		tree, diags := parser.ParseFile(lf, l.testMode && isEntry)
		file := &File{
			Name:  sf.Name,
			Tree:  tree,
			Diags: diags,
		}

		fileDir := filepath.Dir(sf.Name)
		for _, decl := range tree.Children(tree.Root) {
			if tree.Nodes[decl].Kind != enums.NodeKinds.ImportDecl {
				continue
			}
			importPath := tree.File.StringValue(tree.Nodes[decl].Tok)
			targetLoc, err := l.resolveImportPath(fileDir, importPath)
			if err != nil {
				return nil, err
			}

			targetPkg, err := l.loadPackage(targetLoc, false)
			if err != nil {
				return nil, err
			}
			file.Imports = append(file.Imports, Import{
				Path:      importPath,
				LocalName: importLocalName(importPath),
				Package:   targetPkg,
			})
		}
		pkg.Files = append(pkg.Files, file)
	}

	l.loaded[loc.key] = pkg
	l.order = append(l.order, pkg)
	return pkg, nil
}

// importLocalName is an import path's own local name - its last path
// segment (e.g. "mathutils" for "./mathutils", "util" for
// "../shared/util", "mathutil" for "std:mathutil") - Go's own convention
// (see LANGUAGE.md's "Imports" section: no aliasing yet). Uses the "path"
// package (forward-slash only), not "path/filepath", since an import path
// is written with forward slashes regardless of the host OS, exactly like a
// Go import path - only the *filesystem* resolution (resolveImportPath)
// needs to be OS-separator-aware. A "scheme:" prefix is stripped first, so
// it's never mistaken for part of the name.
func importLocalName(importPath string) string {
	if _, rest, ok := splitScheme(importPath); ok {
		return path.Base(rest)
	}
	return path.Base(importPath)
}

// formatCycle renders the chain of package names currently being loaded
// (stack) followed by closing - the directory loadPackage just found
// already on that stack - as "a -> b -> a", for a clear cycle diagnostic.
func formatCycle(stack []string, closing string) string {
	names := make([]string, 0, len(stack)+1)
	for _, dir := range stack {
		names = append(names, filepath.Base(dir))
	}
	names = append(names, filepath.Base(closing))
	return strings.Join(names, " -> ")
}
