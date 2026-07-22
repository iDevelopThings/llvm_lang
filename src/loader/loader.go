// Package loader resolves a source path (a single .llx file, or a
// directory) into the ordered set of files that make up one llvm_lang
// package - see LANGUAGE.md's "Multi-file packages" section for the
// language-level rule this implements ("directory = package", non-recursive).
//
// This package contains zero language-semantic logic - it's pure file
// discovery/I/O, nothing about scopes/symbols/types. All disk access goes
// through an afero.Fs rather than the os package directly (see AGENTS.md's
// standing convention on this): production code wires in afero.NewOsFs(),
// while this package's own tests build fake multi-file package layouts with
// afero.NewMemMapFs(), never touching the real filesystem.
package loader

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/afero"
)

// sourceExt is this project's source file extension - see LANGUAGE.md/
// CODEGEN.md for why it's ".llx", not ".ll" (LLVM's own textual IR format
// already owns that extension).
const sourceExt = ".llx"

// SourceFile is one package member: a file's path (as passed to
// lexer.NewFile - used only for diagnostics, see that function's own doc
// comment) and its already-read source text.
type SourceFile struct {
	Name string
	Src  string
}

// Load resolves root (a single .llx file, or a directory) to "the directory
// that constitutes the package" - a file resolves to its own containing
// directory, so `llvmc some/dir/main.llx` and `llvmc some/dir` compile the
// identical set of files (see LANGUAGE.md's "Multi-file packages" section) -
// and returns every *.llx file directly inside that directory (never
// recursing into a subdirectory - a subdirectory is not part of the
// package), read and sorted by filename for a deterministic build regardless
// of the underlying filesystem's own directory-iteration order.
//
// Returns a clear error if root doesn't exist, the resolved directory
// contains no .llx files at all, or any discovered file can't be read.
func Load(fs afero.Fs, root string) ([]SourceFile, error) {
	info, err := fs.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("loader: cannot resolve %s: %w", root, err)
	}

	dir := root
	if !info.IsDir() {
		dir = filepath.Dir(root)
	}

	entries, err := afero.ReadDir(fs, dir)
	if err != nil {
		return nil, fmt.Errorf("loader: cannot list %s: %w", dir, err)
	}

	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if !strings.EqualFold(filepath.Ext(e.Name()), sourceExt) {
			continue
		}
		names = append(names, e.Name())
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("loader: no %s files found in %s", sourceExt, dir)
	}
	sort.Strings(names)

	files := make([]SourceFile, len(names))
	for i, name := range names {
		path := filepath.Join(dir, name)
		data, err := afero.ReadFile(fs, path)
		if err != nil {
			return nil, fmt.Errorf("loader: cannot read %s: %w", path, err)
		}
		files[i] = SourceFile{
			Name: path,
			Src:  string(data),
		}
	}
	return files, nil
}
