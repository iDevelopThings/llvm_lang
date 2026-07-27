package sema

import (
	"strings"
	"testing"

	"llvm_lang/src/ast"
	"llvm_lang/src/lexer"
	"llvm_lang/src/parser"
)

// mustParseFile parses (name, src) and fails the test immediately if
// parsing itself produced an error - a parse error here means the test
// source is broken, not the imports machinery under test.
func mustParseFile(t *testing.T, name, src string) *ast.Tree {
	t.Helper()
	tree, diags := parser.ParseFile(lexer.NewFile(name, src), false)
	if diags.HasErrors() {
		t.Fatalf("unexpected parse errors in %s: %v", name, diags.All())
	}
	return tree
}

// resolveAndCheckProgram runs ResolveProgram then CheckProgram over units,
// returning every diagnostic collected across every file in every unit
// (resolve-phase and check-phase diagnostics merged together, in no
// particular order) - tests that expect success assert this is empty; tests
// that expect a specific failure search it for their expected message.
func resolveAndCheckProgram(t *testing.T, units []*PackageUnit) []string {
	t.Helper()
	infos, rdiags, _, treePackage := ResolveProgram(units)

	var allTrees []*ast.Tree
	for _, u := range units {
		allTrees = append(allTrees, u.Trees...)
	}

	var msgs []string
	for _, tree := range allTrees {
		for _, d := range rdiags[tree].All() {
			msgs = append(msgs, d.Msg)
		}
	}

	cdiags := CheckProgram(allTrees, infos, treePackage)
	for _, tree := range allTrees {
		for _, d := range cdiags[tree].All() {
			msgs = append(msgs, d.Msg)
		}
	}
	return msgs
}

func requireNoDiags(t *testing.T, msgs []string) {
	t.Helper()
	if len(msgs) != 0 {
		t.Fatalf("expected no diagnostics, got: %v", msgs)
	}
}

func requireDiagContaining(t *testing.T, msgs []string, substr string) {
	t.Helper()
	for _, m := range msgs {
		if strings.Contains(m, substr) {
			return
		}
	}
	t.Fatalf("expected a diagnostic containing %q, got: %v", substr, msgs)
}

// TestImports_CrossPackageCallToExportedFunction covers the core positive
// case (see LANGUAGE.md's "Imports" section): a package importing another
// and calling one of its exported (capitalized) free functions.
func TestImports_CrossPackageCallToExportedFunction(t *testing.T) {
	mathTree := mustParseFile(t, "math/add.llx", "func Add(a int, b int) int {\n\treturn a + b\n}\n")
	mainTree := mustParseFile(t, "app/main.llx", "import \"./math\"\n\nfunc main() int {\n\treturn mathutils.Add(1, 2)\n}\n")

	units := []*PackageUnit{
		{
			Key:   "math",
			Name:  "math",
			Trees: []*ast.Tree{mathTree},
		},
		{
			Key:   "app",
			Name:  "app",
			Trees: []*ast.Tree{mainTree},
			FileImports: map[*ast.Tree][]FileImport{
				mainTree: {{LocalName: "mathutils", TargetKey: "math"}},
			},
		},
	}

	requireNoDiags(t, resolveAndCheckProgram(t, units))
}

// TestImports_CrossPackageStructType covers a struct type declared in one
// package, used (as a var's declared type and a composite literal) from
// another - the "structs (as types)" half of export enforcement.
func TestImports_CrossPackageStructType(t *testing.T) {
	shapesTree := mustParseFile(t, "shapes/point.llx", "struct Point {\n\tX int\n\tY int\n}\n")
	mainTree := mustParseFile(t, "app/main.llx", `import "./shapes"

func main() int {
	var p shapes.Point = shapes.Point{1, 2}
	return p.X + p.Y
}
`)

	units := []*PackageUnit{
		{Key: "shapes", Name: "shapes", Trees: []*ast.Tree{shapesTree}},
		{
			Key:   "app",
			Name:  "app",
			Trees: []*ast.Tree{mainTree},
			FileImports: map[*ast.Tree][]FileImport{
				mainTree: {{LocalName: "shapes", TargetKey: "shapes"}},
			},
		},
	}

	requireNoDiags(t, resolveAndCheckProgram(t, units))
}

// TestImports_UnexportedFunctionIsError covers referencing an unexported
// (lowercase) top-level function through a package qualifier - must be
// rejected.
func TestImports_UnexportedFunctionIsError(t *testing.T) {
	mathTree := mustParseFile(t, "math/add.llx", "func add(a int, b int) int {\n\treturn a + b\n}\n")
	mainTree := mustParseFile(t, "app/main.llx", "import \"./math\"\n\nfunc main() int {\n\treturn mathutils.add(1, 2)\n}\n")

	units := []*PackageUnit{
		{Key: "math", Name: "math", Trees: []*ast.Tree{mathTree}},
		{
			Key:   "app",
			Name:  "app",
			Trees: []*ast.Tree{mainTree},
			FileImports: map[*ast.Tree][]FileImport{
				mainTree: {{LocalName: "mathutils", TargetKey: "math"}},
			},
		},
	}

	requireDiagContaining(t, resolveAndCheckProgram(t, units), "not exported")
}

// TestImports_UnexportedStructTypeIsError covers a package-qualified type
// reference (`pkg.point`) naming an unexported struct.
func TestImports_UnexportedStructTypeIsError(t *testing.T) {
	shapesTree := mustParseFile(t, "shapes/point.llx", "struct point {\n\tX int\n}\n")
	mainTree := mustParseFile(t, "app/main.llx", "import \"./shapes\"\n\nfunc main() {\n\tvar p shapes.point\n\t_ = p\n}\n")

	units := []*PackageUnit{
		{Key: "shapes", Name: "shapes", Trees: []*ast.Tree{shapesTree}},
		{
			Key:   "app",
			Name:  "app",
			Trees: []*ast.Tree{mainTree},
			FileImports: map[*ast.Tree][]FileImport{
				mainTree: {{LocalName: "shapes", TargetKey: "shapes"}},
			},
		},
	}

	requireDiagContaining(t, resolveAndCheckProgram(t, units), "not exported")
}

// TestImports_UnexportedFieldAccessIsError covers accessing an unexported
// field of a struct value whose type belongs to another package - the
// "methods/fields accessed via a value whose struct type belongs to another
// package" half of export enforcement (see typecheck.go's
// checkExportedAccess). Point itself is exported; only its "secret" field
// is not.
func TestImports_UnexportedFieldAccessIsError(t *testing.T) {
	shapesTree := mustParseFile(t, "shapes/point.llx", "struct Point {\n\tX int\n\tsecret int\n}\n")
	mainTree := mustParseFile(t, "app/main.llx", `import "./shapes"

func main() int {
	p := shapes.Point{1, 2}
	return p.secret
}
`)

	units := []*PackageUnit{
		{Key: "shapes", Name: "shapes", Trees: []*ast.Tree{shapesTree}},
		{
			Key:   "app",
			Name:  "app",
			Trees: []*ast.Tree{mainTree},
			FileImports: map[*ast.Tree][]FileImport{
				mainTree: {{LocalName: "shapes", TargetKey: "shapes"}},
			},
		},
	}

	requireDiagContaining(t, resolveAndCheckProgram(t, units), "not exported")
}

// TestImports_PositionalLiteralRejectedForUnexportedField covers Go's own
// stricter construction rule (see AGENTS.md's "Types" section on cross-
// package struct literal construction): a *positional* composite literal
// constructing a struct from another package is rejected the moment that
// struct has ANY unexported field - even one this literal never mentions by
// name, and even though every value actually supplied here (X and Y) is
// itself fine. There's no way to positionally "skip" a field, so allowing
// this would silently let outside code set a private field's value.
func TestImports_PositionalLiteralRejectedForUnexportedField(t *testing.T) {
	shapesTree := mustParseFile(t, "shapes/point.llx", "struct Point {\n\tX int\n\tsecret int\n}\n")
	mainTree := mustParseFile(t, "app/main.llx", `import "./shapes"

func main() int {
	p := shapes.Point{1, 2}
	return p.X
}
`)

	units := []*PackageUnit{
		{Key: "shapes", Name: "shapes", Trees: []*ast.Tree{shapesTree}},
		{
			Key:   "app",
			Name:  "app",
			Trees: []*ast.Tree{mainTree},
			FileImports: map[*ast.Tree][]FileImport{
				mainTree: {{LocalName: "shapes", TargetKey: "shapes"}},
			},
		},
	}

	requireDiagContaining(t, resolveAndCheckProgram(t, units), "positional literal")
}

// TestImports_KeyedLiteralAllowedDespiteUnexportedField covers the other half
// of the same rule: a *keyed* literal from another package is fine as long
// as it doesn't explicitly name the unexported field - simply omitting it
// (as here) leaves it untouched, unlike a positional literal, which has no
// way to skip it.
func TestImports_KeyedLiteralAllowedDespiteUnexportedField(t *testing.T) {
	shapesTree := mustParseFile(t, "shapes/point.llx", "struct Point {\n\tX int\n\tsecret int\n}\n")
	mainTree := mustParseFile(t, "app/main.llx", `import "./shapes"

func main() int {
	p := shapes.Point{X: 1}
	return p.X
}
`)

	units := []*PackageUnit{
		{Key: "shapes", Name: "shapes", Trees: []*ast.Tree{shapesTree}},
		{
			Key:   "app",
			Name:  "app",
			Trees: []*ast.Tree{mainTree},
			FileImports: map[*ast.Tree][]FileImport{
				mainTree: {{LocalName: "shapes", TargetKey: "shapes"}},
			},
		},
	}

	requireNoDiags(t, resolveAndCheckProgram(t, units))
}

// TestImports_KeyedLiteralExplicitUnexportedFieldIsError covers explicitly
// naming the unexported field itself in a keyed literal - ordinary
// unexported-access, the same restriction reading p.secret would hit, now
// enforced by checkKeyedStructElem too (previously it resolved a keyed
// field name against the struct's field catalog without ever checking
// export visibility at all).
func TestImports_KeyedLiteralExplicitUnexportedFieldIsError(t *testing.T) {
	shapesTree := mustParseFile(t, "shapes/point.llx", "struct Point {\n\tX int\n\tsecret int\n}\n")
	mainTree := mustParseFile(t, "app/main.llx", `import "./shapes"

func main() int {
	p := shapes.Point{X: 1, secret: 2}
	return p.X
}
`)

	units := []*PackageUnit{
		{Key: "shapes", Name: "shapes", Trees: []*ast.Tree{shapesTree}},
		{
			Key:   "app",
			Name:  "app",
			Trees: []*ast.Tree{mainTree},
			FileImports: map[*ast.Tree][]FileImport{
				mainTree: {{LocalName: "shapes", TargetKey: "shapes"}},
			},
		},
	}

	requireDiagContaining(t, resolveAndCheckProgram(t, units), "not exported")
}

// TestImports_EmptyLiteralAllowedDespiteUnexportedField covers the fix for a
// fully-empty composite literal (`T{}`, checkStructCompositeLit,
// sema/typecheck.go): unlike a real positional literal
// (TestImports_PositionalLiteralRejectedForUnexportedField above), `pkg.
// Point{}` from another package must be legal even though Point has an
// unexported field - providing zero values isn't "setting" anything the way
// a positional literal with actual values would be, so the empty case skips
// crossPackageStructConstruction's unexported-field check entirely, the same
// way it skips the arity check.
func TestImports_EmptyLiteralAllowedDespiteUnexportedField(t *testing.T) {
	shapesTree := mustParseFile(t, "shapes/point.llx", "struct Point {\n\tX int\n\tsecret int\n}\n")
	mainTree := mustParseFile(t, "app/main.llx", `import "./shapes"

func main() {
	p := shapes.Point{}
	print(p.X)
}
`)

	units := []*PackageUnit{
		{Key: "shapes", Name: "shapes", Trees: []*ast.Tree{shapesTree}},
		{
			Key:   "app",
			Name:  "app",
			Trees: []*ast.Tree{mainTree},
			FileImports: map[*ast.Tree][]FileImport{
				mainTree: {{LocalName: "shapes", TargetKey: "shapes"}},
			},
		},
	}

	requireNoDiags(t, resolveAndCheckProgram(t, units))
}

// TestImports_SamePackageCaseInsensitivityStillHolds is a regression check:
// within one package (even when checked via the new ResolveProgram/
// CheckProgram multi-package path, not just plain ResolvePackage/
// CheckPackage), an unexported (lowercase) name declared in one file must
// still be freely usable from a sibling file in the *same* package,
// unaffected by export enforcement - see LANGUAGE.md's "Multi-file
// packages" section: case must never matter for same-package visibility.
func TestImports_SamePackageCaseInsensitivityStillHolds(t *testing.T) {
	pointTree := mustParseFile(t, "app/point.llx", "struct point {\n\tx int\n\ty int\n}\n\nfunc newPoint() point {\n\treturn point{1, 2}\n}\n")
	mainTree := mustParseFile(t, "app/main.llx", "func main() int {\n\tp := newPoint()\n\treturn p.x + p.y\n}\n")

	units := []*PackageUnit{
		{
			Key:   "app",
			Name:  "app",
			Trees: []*ast.Tree{pointTree, mainTree},
		},
	}

	requireNoDiags(t, resolveAndCheckProgram(t, units))
}

// TestImports_ConstructorUsableCrossPackageWhenStructExported covers
// LANGUAGE.md's "Constructors" section's export rule: a constructor doesn't
// get its own independent export bit - a struct's constructors are usable
// cross-package if and only if the struct type itself is exported, exactly
// like its fields/methods already work. Point is exported, so
// `shapes.Point(1)` (a package-qualified constructor call - a MemberExpr
// callee, not a bare Ident) must resolve and type-check cleanly.
func TestImports_ConstructorUsableCrossPackageWhenStructExported(t *testing.T) {
	shapesTree := mustParseFile(t, "shapes/point.llx", "struct Point {\n\tX int\n\n\tconstructor(v int) {\n\t\tthis.X = v\n\t}\n}\n")
	mainTree := mustParseFile(t, "app/main.llx", `import "./shapes"

func main() int {
	p := shapes.Point(5)
	return p.X
}
`)

	units := []*PackageUnit{
		{Key: "shapes", Name: "shapes", Trees: []*ast.Tree{shapesTree}},
		{
			Key:   "app",
			Name:  "app",
			Trees: []*ast.Tree{mainTree},
			FileImports: map[*ast.Tree][]FileImport{
				mainTree: {{LocalName: "shapes", TargetKey: "shapes"}},
			},
		},
	}

	requireNoDiags(t, resolveAndCheckProgram(t, units))
}

// TestImports_ConstructorNotUsableCrossPackageWhenStructUnexported covers
// the other half: an unexported struct's constructor is unreachable from
// another package - same as any other unexported-struct-type reference
// (TestImports_UnexportedStructTypeIsError) - since the struct type name
// itself is never resolvable through the package qualifier in the first
// place.
func TestImports_ConstructorNotUsableCrossPackageWhenStructUnexported(t *testing.T) {
	shapesTree := mustParseFile(t, "shapes/point.llx", "struct point {\n\tx int\n\n\tconstructor(v int) {\n\t\tthis.x = v\n\t}\n}\n")
	mainTree := mustParseFile(t, "app/main.llx", `import "./shapes"

func main() int {
	p := shapes.point(5)
	return p.x
}
`)

	units := []*PackageUnit{
		{Key: "shapes", Name: "shapes", Trees: []*ast.Tree{shapesTree}},
		{
			Key:   "app",
			Name:  "app",
			Trees: []*ast.Tree{mainTree},
			FileImports: map[*ast.Tree][]FileImport{
				mainTree: {{LocalName: "shapes", TargetKey: "shapes"}},
			},
		},
	}

	requireDiagContaining(t, resolveAndCheckProgram(t, units), "not exported")
}

// TestImports_FileScopedNotPackageScoped covers the file-scoping rule
// itself (see LANGUAGE.md's "Imports" section and ScopeFile's doc comment):
// an import declared in one file of a package is NOT visible from a sibling
// file in the same package that didn't itself write that import - here,
// main.llx imports "./math" but a sibling file, other.llx, tries to use the
// same qualifier without importing it itself, and must fail as undefined.
func TestImports_FileScopedNotPackageScoped(t *testing.T) {
	mathTree := mustParseFile(t, "math/add.llx", "func Add(a int, b int) int {\n\treturn a + b\n}\n")
	mainTree := mustParseFile(t, "app/main.llx", "import \"./math\"\n\nfunc main() int {\n\treturn mathutils.Add(1, 2)\n}\n")
	otherTree := mustParseFile(t, "app/other.llx", "func other() int {\n\treturn mathutils.Add(3, 4)\n}\n")

	units := []*PackageUnit{
		{Key: "math", Name: "math", Trees: []*ast.Tree{mathTree}},
		{
			Key:   "app",
			Name:  "app",
			Trees: []*ast.Tree{mainTree, otherTree},
			FileImports: map[*ast.Tree][]FileImport{
				mainTree: {{LocalName: "mathutils", TargetKey: "math"}},
			},
		},
	}

	requireDiagContaining(t, resolveAndCheckProgram(t, units), "undefined: mathutils")
}

// TestImports_PackageQualifiedUndefinedTypeIsError covers
// resolveTypeMemberExpr's own "undefined member" branch (type position,
// distinct from an ordinary undefined value-level member access) - a
// package-qualified type reference naming something the target package
// never declared at all.
func TestImports_PackageQualifiedUndefinedTypeIsError(t *testing.T) {
	shapesTree := mustParseFile(t, "shapes/point.llx", "struct Point {\n\tX int\n}\n")
	mainTree := mustParseFile(t, "app/main.llx", "import \"./shapes\"\n\nvar p shapes.NotAType\n")

	units := []*PackageUnit{
		{Key: "shapes", Name: "shapes", Trees: []*ast.Tree{shapesTree}},
		{
			Key:   "app",
			Name:  "app",
			Trees: []*ast.Tree{mainTree},
			FileImports: map[*ast.Tree][]FileImport{
				mainTree: {{LocalName: "shapes", TargetKey: "shapes"}},
			},
		},
	}

	requireDiagContaining(t, resolveAndCheckProgram(t, units), "undefined: shapes.NotAType")
}

// TestImports_QualifierNotAPackageInTypePositionIsError covers
// resolveTypeMemberExpr rejecting a `x.Y` type reference whose qualifier
// resolves to a real symbol that just isn't a package (here, an ordinary
// top-level var shadowing what would otherwise look like a package name) -
// distinct from the qualifier being entirely undefined.
func TestImports_QualifierNotAPackageInTypePositionIsError(t *testing.T) {
	mainTree := mustParseFile(t, "app/main.llx", "var shapes int = 1\nvar p shapes.Point\n")

	units := []*PackageUnit{
		{Key: "app", Name: "app", Trees: []*ast.Tree{mainTree}},
	}

	requireDiagContaining(t, resolveAndCheckProgram(t, units), "is not a package")
}

// TestImports_PackageQualifiedVarRead covers reading an exported
// package-level `var` through a package qualifier (`lib.X`) - resolved the
// same way any other package member is (resolvePackageMemberExpr), and typed
// like a bare identifier reference (checkMemberExpr's own memberObjectIsPackage
// branch).
func TestImports_PackageQualifiedVarRead(t *testing.T) {
	libTree := mustParseFile(t, "lib/lib.llx", "var X int = 42\n")
	mainTree := mustParseFile(t, "app/main.llx", "import \"./lib\"\n\nfunc main() int {\n\treturn lib.X\n}\n")

	units := []*PackageUnit{
		{Key: "lib", Name: "lib", Trees: []*ast.Tree{libTree}},
		{
			Key:   "app",
			Name:  "app",
			Trees: []*ast.Tree{mainTree},
			FileImports: map[*ast.Tree][]FileImport{
				mainTree: {{LocalName: "lib", TargetKey: "lib"}},
			},
		},
	}

	requireNoDiags(t, resolveAndCheckProgram(t, units))
}

// TestImports_UnexportedVarIsError covers the negative half of
// TestImports_PackageQualifiedVarRead: an unexported (lowercase) package-level
// var stays unreachable through a package qualifier, same as any other
// unexported top-level symbol.
func TestImports_UnexportedVarIsError(t *testing.T) {
	libTree := mustParseFile(t, "lib/lib.llx", "var x int = 42\n")
	mainTree := mustParseFile(t, "app/main.llx", "import \"./lib\"\n\nfunc main() int {\n\treturn lib.x\n}\n")

	units := []*PackageUnit{
		{Key: "lib", Name: "lib", Trees: []*ast.Tree{libTree}},
		{
			Key:   "app",
			Name:  "app",
			Trees: []*ast.Tree{mainTree},
			FileImports: map[*ast.Tree][]FileImport{
				mainTree: {{LocalName: "lib", TargetKey: "lib"}},
			},
		},
	}

	requireDiagContaining(t, resolveAndCheckProgram(t, units), "not exported")
}

// TestImports_CrossPackageEnumUnitVariantConstruction covers a
// `pkg.Type.Variant` qualifier chain (a nested MemberExpr - object =
// `pkg.Type`, itself a MemberExpr, not a bare Ident) constructing a unit
// variant of another package's enum - resolveTypeMemberExpr/resolveExpr's
// MemberExpr case previously only recognized a single-hop qualifier.
func TestImports_CrossPackageEnumUnitVariantConstruction(t *testing.T) {
	osTree := mustParseFile(t, "os/os.llx", "enum Error {\n\tOk,\n\tNotFound\n}\n")
	mainTree := mustParseFile(t, "app/main.llx", "import \"./os\"\n\nfunc main() {\n\te := os.Error.Ok\n}\n")

	units := []*PackageUnit{
		{Key: "os", Name: "os", Trees: []*ast.Tree{osTree}},
		{
			Key:   "app",
			Name:  "app",
			Trees: []*ast.Tree{mainTree},
			FileImports: map[*ast.Tree][]FileImport{
				mainTree: {{LocalName: "os", TargetKey: "os"}},
			},
		},
	}

	requireNoDiags(t, resolveAndCheckProgram(t, units))
}

// TestImports_CrossPackageEnumTupleVariantConstruction covers the tuple-
// variant call form of the same qualifier chain (`pkg.Type.Variant(args)` -
// the callee is the identical nested-MemberExpr shape).
func TestImports_CrossPackageEnumTupleVariantConstruction(t *testing.T) {
	stringsTree := mustParseFile(t, "strings/strings.llx", "enum IntParseResult {\n\tOk(int),\n\tErr\n}\n")
	mainTree := mustParseFile(t, "app/main.llx", "import \"./strings\"\n\nfunc main() int {\n\tr := strings.IntParseResult.Ok(5)\n\treturn 0\n}\n")

	units := []*PackageUnit{
		{Key: "strings", Name: "strings", Trees: []*ast.Tree{stringsTree}},
		{
			Key:   "app",
			Name:  "app",
			Trees: []*ast.Tree{mainTree},
			FileImports: map[*ast.Tree][]FileImport{
				mainTree: {{LocalName: "strings", TargetKey: "strings"}},
			},
		},
	}

	requireNoDiags(t, resolveAndCheckProgram(t, units))
}

// TestImports_CrossPackageEnumMatchBindsTupleVariant covers the same
// qualifier chain reached through match-pattern resolution instead of a
// plain expression (patternEnumVariantRef/peekQualifierSym) - a tuple-variant
// pattern's binding must still work.
func TestImports_CrossPackageEnumMatchBindsTupleVariant(t *testing.T) {
	stringsTree := mustParseFile(t, "strings/strings.llx", "enum IntParseResult {\n\tOk(int),\n\tErr\n}\n")
	mainTree := mustParseFile(t, "app/main.llx", `import "./strings"

func main() int {
	r := strings.IntParseResult.Ok(5)
	match r {
		strings.IntParseResult.Ok(n) => {
			return n
		}
		strings.IntParseResult.Err => {
			return -1
		}
	}
}
`)

	units := []*PackageUnit{
		{Key: "strings", Name: "strings", Trees: []*ast.Tree{stringsTree}},
		{
			Key:   "app",
			Name:  "app",
			Trees: []*ast.Tree{mainTree},
			FileImports: map[*ast.Tree][]FileImport{
				mainTree: {{LocalName: "strings", TargetKey: "strings"}},
			},
		},
	}

	requireNoDiags(t, resolveAndCheckProgram(t, units))
}

// TestImports_CrossPackageEnumVariantUnexportedEnumIsError covers the
// negative half: an unexported (lowercase) enum type stays unreachable
// through a `pkg.Type.Variant` chain, exactly like any other unexported
// package member - the chain must not silently construct anyway.
func TestImports_CrossPackageEnumVariantUnexportedEnumIsError(t *testing.T) {
	osTree := mustParseFile(t, "os/os.llx", "enum error {\n\tOk\n}\n")
	mainTree := mustParseFile(t, "app/main.llx", "import \"./os\"\n\nfunc main() {\n\te := os.error.Ok\n}\n")

	units := []*PackageUnit{
		{Key: "os", Name: "os", Trees: []*ast.Tree{osTree}},
		{
			Key:   "app",
			Name:  "app",
			Trees: []*ast.Tree{mainTree},
			FileImports: map[*ast.Tree][]FileImport{
				mainTree: {{LocalName: "os", TargetKey: "os"}},
			},
		},
	}

	requireDiagContaining(t, resolveAndCheckProgram(t, units), "not exported")
}

// TestImports_CrossPackageEnumVariantUndefinedTypeIsError covers naming a
// type the target package never declared at all as the first hop of a
// `pkg.Type.Variant` chain - a clean "undefined: pkg.Type" diagnostic, not a
// panic or a cascading/confusing message.
func TestImports_CrossPackageEnumVariantUndefinedTypeIsError(t *testing.T) {
	osTree := mustParseFile(t, "os/os.llx", "enum Error {\n\tOk\n}\n")
	mainTree := mustParseFile(t, "app/main.llx", "import \"./os\"\n\nfunc main() {\n\te := os.NotAType.Ok\n}\n")

	units := []*PackageUnit{
		{Key: "os", Name: "os", Trees: []*ast.Tree{osTree}},
		{
			Key:   "app",
			Name:  "app",
			Trees: []*ast.Tree{mainTree},
			FileImports: map[*ast.Tree][]FileImport{
				mainTree: {{LocalName: "os", TargetKey: "os"}},
			},
		},
	}

	requireDiagContaining(t, resolveAndCheckProgram(t, units), "undefined: os.NotAType")
}

// TestImports_CrossPackageEnumVariantUndefinedVariantIsError covers naming a
// real cross-package enum type but a variant it doesn't declare - a clean
// "undefined: Error.Nonexistent" diagnostic (resolveEnumVariantRef, the exact
// same message a same-package undefined variant already gets).
func TestImports_CrossPackageEnumVariantUndefinedVariantIsError(t *testing.T) {
	osTree := mustParseFile(t, "os/os.llx", "enum Error {\n\tOk\n}\n")
	mainTree := mustParseFile(t, "app/main.llx", "import \"./os\"\n\nfunc main() {\n\te := os.Error.Nonexistent\n}\n")

	units := []*PackageUnit{
		{Key: "os", Name: "os", Trees: []*ast.Tree{osTree}},
		{
			Key:   "app",
			Name:  "app",
			Trees: []*ast.Tree{mainTree},
			FileImports: map[*ast.Tree][]FileImport{
				mainTree: {{LocalName: "os", TargetKey: "os"}},
			},
		},
	}

	requireDiagContaining(t, resolveAndCheckProgram(t, units), "undefined: Error.Nonexistent")
}
