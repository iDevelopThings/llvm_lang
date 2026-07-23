// This file is sema's multi-*package* entry point - ResolveProgram/
// CheckProgram - built directly on top of ResolvePackage/CheckPackage (see
// resolve.go/typecheck.go), the same way those two are themselves built on
// top of the single-file case. See LANGUAGE.md's "Imports" section for the
// language-level model this implements.
package sema

import (
	"llvm_lang/src/ast"
	"llvm_lang/src/diag"
)

// PackageResult is one already-resolved package's surface: its shared
// top-level Scope (every var/func/struct name declared in any of its files -
// see ResolvePackage) and its shared struct catalog. Returned by
// ResolveProgram, keyed by each PackageUnit's own Key, purely as a
// diagnostic/inspection convenience - ResolveProgram already wires every
// dependent unit's own FileImports against the right PackageResult
// internally (see the TargetKey lookup in ResolveProgram's own loop), so a
// caller never needs to feed one back in itself.
type PackageResult struct {
	Name    string
	Scope   *Scope
	Structs map[string]*StructInfo
	Enums   map[string]*EnumInfo
}

// FileImport is one file's own `import "path"` binding, as a PackageUnit
// describes it: TargetKey names *which other unit* (by that unit's own Key)
// this import resolves to - not a Symbol or PackageResult directly, since
// the target unit may not have been resolved yet at the point the whole
// units slice is built (see ResolveProgram, which resolves TargetKey against
// its own accumulating per-key PackageResult table as it processes units in
// order, so the caller never has to pre-resolve or mutate anything itself).
// LocalName is this import's local name in the file that declared it -
// there's no aliasing syntax yet (see LANGUAGE.md's "Imports" section), so
// it's always the import path's own last segment.
type FileImport struct {
	LocalName string
	TargetKey string
}

// PackageUnit is one package's input to ResolveProgram/CheckProgram: its own
// files (as already-parsed trees, e.g. from src/loader) plus, for each file,
// the imports it declared. FileImports is deliberately keyed per file, not
// once for the whole package - an import binding is file-scoped, not
// package-scoped (see LANGUAGE.md's "Imports" section: a sibling file that
// doesn't itself write `import "./x"` can't see that binding, even within
// the same package) - a file with no imports of its own simply has no entry
// (or an empty slice) here.
//
// Key is an opaque, caller-chosen unique identity for this package (e.g.
// src/loader.Package.Dir, its resolved directory - never inspected by sema
// itself, just used to match a FileImport's own TargetKey against the right
// unit). Name is only a display name (used to build this unit's own
// PackageResult.Name); two units may share a Name (e.g. two same-named
// packages loaded from different parent directories) as long as their Key
// differs.
type PackageUnit struct {
	Key         string
	Name        string
	Trees       []*ast.Tree
	FileImports map[*ast.Tree][]FileImport
}

// ResolveProgram resolves every package in units - already given in
// dependency order by the caller (every unit a given unit imports, named by
// TargetKey, must already appear earlier in units - see
// src/loader.Program.Order, which computes exactly this order while also
// deduping a diamond dependency and rejecting an import cycle) - each via
// resolveOnePackage (ResolvePackage's shared guts, extended with that
// unit's own imports, resolved against the packages already processed
// earlier in this same loop), and returns:
//
//   - one *Info/*diag.Bag per tree, merged across every unit into one flat
//     map (exactly the shape CheckProgram/codegen.GeneratePackage expect -
//     see their own doc comments for why a flat, tree-keyed map needs no
//     package-boundary awareness at all, the same pointer-identity
//     reasoning multi-file support already established one level down);
//   - one *PackageResult per unit, keyed by that unit's own Key;
//   - one *Scope per tree naming which package's own top-level Scope that
//     tree belongs to (treePackage) - CheckProgram needs this to enforce
//     export visibility (a field/method access must know whether the
//     accessing code and the declaring struct are the same package - see
//     typecheck.go's checkExportedAccess), since a flat cross-package tree
//     list on its own has no such notion once merged.
func ResolveProgram(units []*PackageUnit) (infos map[*ast.Tree]*Info, diags map[*ast.Tree]*diag.Bag, results map[string]*PackageResult, treePackage map[*ast.Tree]*Scope) {
	infos = make(map[*ast.Tree]*Info)
	diags = make(map[*ast.Tree]*diag.Bag)
	results = make(map[string]*PackageResult, len(units))
	treePackage = make(map[*ast.Tree]*Scope)

	for _, u := range units {
		boundImports := make(map[*ast.Tree][]boundImport, len(u.FileImports))
		for tree, imps := range u.FileImports {
			for _, imp := range imps {
				// A missing target here would mean a caller-side ordering
				// bug (an import naming a unit not yet processed) - not
				// something a real program can trigger (loader.LoadProgram
				// already guarantees dependency order, and a genuinely
				// unresolvable import path is rejected before ResolveProgram
				// ever runs at all).
				target := results[imp.TargetKey]
				boundImports[tree] = append(boundImports[tree], boundImport{
					LocalName: imp.LocalName,
					Target:    target,
				})
			}
		}

		unitInfos, unitDiags, result := resolveOnePackage(u.Name, u.Trees, boundImports)
		for _, tree := range u.Trees {
			infos[tree] = unitInfos[tree]
			diags[tree] = unitDiags[tree]
			treePackage[tree] = result.Scope
		}
		results[u.Key] = result
	}
	return infos, diags, results, treePackage
}
