package loader

import (
	"fmt"
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
	Name  string
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

// LoadProgram resolves root (a single .llx file, or a directory - see Load's
// own doc comment) to the entry package, then transitively discovers,
// parses, and loads every package it imports, recursively - deduping a
// diamond dependency (two different packages importing the same third one)
// by directory identity so it's only ever loaded once, and rejecting a real
// import cycle with a clear error naming the cycle (e.g.
// "loader: import cycle: a -> b -> a") rather than looping forever or
// overflowing the stack.
//
// An import path is resolved relative to the *importing file's own
// directory* - not the entry package's directory, and not any notion of a
// module root/manifest - matching this project's confirmed design (see
// DECISIONS.md).
func LoadProgram(fs afero.Fs, root string) (*Program, error) {
	dir, err := resolvePackageDir(fs, root)
	if err != nil {
		return nil, err
	}

	l := &programLoader{
		fs:      fs,
		loaded:  make(map[string]*Package),
		loading: make(map[string]bool),
	}
	entry, err := l.loadPackage(dir)
	if err != nil {
		return nil, err
	}
	return &Program{Entry: entry, Order: l.order}, nil
}

// programLoader carries LoadProgram's recursive-discovery state: loaded
// dedups a fully-loaded package by directory; loading/stack together detect
// a real import cycle (loading alone can't tell a cycle apart from a
// harmless diamond re-visit - a dir already fully in loaded is a diamond, a
// dir still marked loading when revisited is a genuine cycle).
type programLoader struct {
	fs      afero.Fs
	loaded  map[string]*Package
	loading map[string]bool
	stack   []string
	order   []*Package
}

// loadPackage loads dir's package (recursively resolving/loading every
// import it or its own dependencies declare) and returns it, reusing an
// already-loaded package by directory identity rather than reloading it -
// see LoadProgram's own doc comment.
func (l *programLoader) loadPackage(dir string) (*Package, error) {
	dir = filepath.Clean(dir)

	if pkg, ok := l.loaded[dir]; ok {
		return pkg, nil // diamond dependency - already fully loaded once
	}
	if l.loading[dir] {
		return nil, fmt.Errorf("loader: import cycle: %s", formatCycle(l.stack, dir))
	}

	l.loading[dir] = true
	l.stack = append(l.stack, dir)
	defer func() {
		l.stack = l.stack[:len(l.stack)-1]
		delete(l.loading, dir)
	}()

	srcFiles, err := Load(l.fs, dir)
	if err != nil {
		return nil, err
	}

	pkg := &Package{
		Dir:  dir,
		Name: filepath.Base(dir),
	}

	for _, sf := range srcFiles {
		lf := lexer.NewFile(sf.Name, sf.Src)
		tree, diags := parser.ParseFile(lf)
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
			targetDir := filepath.Clean(filepath.Join(fileDir, filepath.FromSlash(importPath)))

			targetPkg, err := l.loadPackage(targetDir)
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

	l.loaded[dir] = pkg
	l.order = append(l.order, pkg)
	return pkg, nil
}

// importLocalName is an import path's own local name - its last path
// segment (e.g. "mathutils" for "./mathutils", "util" for
// "../shared/util") - Go's own convention (see LANGUAGE.md's "Imports"
// section: no aliasing yet). Uses the "path" package (forward-slash only),
// not "path/filepath", since an import path is written with forward
// slashes regardless of the host OS, exactly like a Go import path - only
// the *filesystem* resolution (loadPackage's targetDir) needs to be
// OS-separator-aware.
func importLocalName(importPath string) string {
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
