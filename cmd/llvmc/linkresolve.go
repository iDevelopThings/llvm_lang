package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// libraryKind classifies a resolved artifact for the JIT path: shared uses
// NewDynamicLibrarySearchGeneratorForPath, static uses
// NewStaticLibrarySearchGeneratorForPath (see bindExtraLibraries).
type libraryKind int

const (
	libraryShared libraryKind = iota
	libraryStatic
)

// libraryArtifact is one resolved -l result: an on-disk path plus how the
// JIT should attach it.
type libraryArtifact struct {
	Path string
	Kind libraryKind
}

// resolveLibraryArtifacts resolves every -l name against -L dirs (and any
// literal file paths). Shared (.dll) wins over static (.a/.lib) when both
// exist in the same search pass. Import libs (*.dll.a) are never accepted -
// they don't contain real code for the static generator.
func resolveLibraryArtifacts(libs, dirs []string) ([]libraryArtifact, error) {
	out := make([]libraryArtifact, 0, len(libs))
	for _, name := range libs {
		art, err := resolveLibraryArtifact(name, dirs)
		if err != nil {
			return nil, err
		}
		out = append(out, art)
	}
	return out, nil
}

func resolveLibraryArtifact(name string, dirs []string) (libraryArtifact, error) {
	// Only a real path (with a directory component) is a literal. A bare
	// "libfoo.a" / "foo.dll" must still search -L dirs - otherwise -L is
	// silently ignored (see TestResolveLibraryArtifactBareFilenameUsesDirs).
	if isLiteralLibraryPath(name) {
		kind, ok := classifyLibraryPath(name)
		if !ok {
			return libraryArtifact{}, fmt.Errorf("library %q: unsupported artifact (want .dll, .a, or .lib; not an import lib *.dll.a)", name)
		}
		if _, err := os.Stat(name); err != nil {
			return libraryArtifact{}, fmt.Errorf("library %q: %w", name, err)
		}
		return libraryArtifact{Path: name, Kind: kind}, nil
	}

	// Bare name that already looks like a library filename: look for that
	// exact basename under each -L dir before treating it as a stem.
	if kind, ok := classifyLibraryPath(name); ok {
		base := filepath.Base(name)
		for _, dir := range dirs {
			p := filepath.Join(dir, base)
			if fileExists(p) {
				return libraryArtifact{Path: p, Kind: kind}, nil
			}
		}
		searched := "(no -L dirs)"
		if len(dirs) > 0 {
			searched = strings.Join(dirs, ", ")
		}
		return libraryArtifact{}, fmt.Errorf("library %q: not found under -L dirs [%s]", name, searched)
	}

	// A bare name that's exactly an import-lib filename (e.g. "libfoo.dll.a",
	// no directory separator) falls through classifyLibraryPath's own "not
	// ok" for *.dll.a (by design, so the literal-path branch above gives the
	// clear rejection when there IS a separator) - without this check it
	// would fall all the way to the generic stem-expansion below instead,
	// producing a confusing "not found" error built from garbage derived
	// candidates (e.g. "libfoo.dll.a.a") instead of the same clear message
	// the literal-path and stem-expansion branches already give.
	if isImportLibFilename(name) {
		base := filepath.Base(name)
		for _, dir := range dirs {
			p := filepath.Join(dir, base)
			if fileExists(p) {
				return libraryArtifact{}, fmt.Errorf(
					"library %q: found import lib %s but no real .dll or static .a/.lib - provide the DLL (or a true static archive), not a mingw *.dll.a import lib",
					name, p,
				)
			}
		}
	}

	sharedNames := []string{name + ".dll", "lib" + name + ".dll"}
	staticNames := []string{"lib" + name + ".a", name + ".a", name + ".lib", "lib" + name + ".lib"}
	importNames := []string{"lib" + name + ".dll.a", name + ".dll.a"}

	var foundImport string
	for _, dir := range dirs {
		for _, base := range sharedNames {
			p := filepath.Join(dir, base)
			if fileExists(p) {
				return libraryArtifact{Path: p, Kind: libraryShared}, nil
			}
		}
		for _, base := range staticNames {
			p := filepath.Join(dir, base)
			if fileExists(p) {
				return libraryArtifact{Path: p, Kind: libraryStatic}, nil
			}
		}
		if foundImport == "" {
			for _, base := range importNames {
				p := filepath.Join(dir, base)
				if fileExists(p) {
					foundImport = p
				}
			}
		}
	}

	if foundImport != "" {
		return libraryArtifact{}, fmt.Errorf(
			"library %q: found import lib %s but no real .dll or static .a/.lib - provide the DLL (or a true static archive), not a mingw *.dll.a import lib",
			name,
			foundImport,
		)
	}

	searched := "(no -L dirs)"
	if len(dirs) > 0 {
		searched = strings.Join(dirs, ", ")
	}
	return libraryArtifact{}, fmt.Errorf("library %q: not found under -L dirs [%s] (tried shared %v then static %v)", name, searched, sharedNames, staticNames)
}

// isLiteralLibraryPath reports whether name is an explicit filesystem path
// (has a directory separator). Bare filenames like "libfoo.a" are not
// literal - they still resolve via -L.
func isLiteralLibraryPath(name string) bool {
	return strings.ContainsAny(name, `/\`) || filepath.IsAbs(name)
}

// isImportLibFilename reports whether name's own basename is exactly a
// mingw import-lib filename ("libfoo.dll.a"/"foo.dll.a") - the one
// classifyLibraryPath deliberately rejects, so callers can give the clear
// "found import lib" error instead of a generic "unsupported"/"not found".
func isImportLibFilename(name string) bool {
	return strings.HasSuffix(strings.ToLower(filepath.Base(name)), ".dll.a")
}

func classifyLibraryPath(path string) (libraryKind, bool) {
	base := strings.ToLower(filepath.Base(path))
	if strings.HasSuffix(base, ".dll.a") {
		return 0, false
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".dll":
		return libraryShared, true
	case ".a", ".lib":
		return libraryStatic, true
	default:
		return 0, false
	}
}

func fileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}
