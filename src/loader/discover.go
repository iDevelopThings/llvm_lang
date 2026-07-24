package loader

import (
	"io/fs"
	"iter"
	"path/filepath"
	"strings"

	"github.com/spf13/afero"
)

// PackageCandidate is one directory discovered by DiscoverPackages: a real,
// loadable package that root's own tree contains, whether or not anything
// currently imports it.
type PackageCandidate struct {
	// Dir is the candidate's resolved, cleaned directory path - the same
	// identity Load/LoadProgram key a package by.
	Dir string
	// Name is Dir's own last path segment (this project's own
	// directory-as-package-name convention - see Package.Name).
	Name string
}

// DiscoverPackages walks root recursively (skipping any directory whose own
// name starts with "." - VCS/tooling directories like .git, not a real
// package) and yields one PackageCandidate for every directory Load
// succeeds against, i.e. every directory containing at least one .llx file
// directly inside it. This is pure discovery, not resolution: a
// candidate's own declarations are only ever parsed later, by whichever
// caller actually needs them (see src/lsp's not-yet-imported-package
// completion, the reason this exists) - a workspace can have far more
// candidate directories than any one completion request needs to inspect.
func DiscoverPackages(afs afero.Fs, root string) iter.Seq[PackageCandidate] {
	return func(yield func(PackageCandidate) bool) {
		_ = afero.Walk(afs, root, func(path string, info fs.FileInfo, err error) error {
			if err != nil {
				return nil // an unreadable entry just isn't a candidate - keep walking
			}
			if !info.IsDir() {
				return nil
			}
			if info.Name() != "." && strings.HasPrefix(info.Name(), ".") {
				return filepath.SkipDir
			}
			if _, loadErr := Load(afs, path); loadErr != nil {
				return nil // no .llx files here (or unreadable) - not a candidate
			}
			if !yield(PackageCandidate{Dir: path, Name: filepath.Base(path)}) {
				return filepath.SkipAll
			}
			return nil
		})
	}
}
