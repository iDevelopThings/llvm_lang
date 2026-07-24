package lsp

import (
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"llvm_lang/src/ast"
	"llvm_lang/src/enums"
	"llvm_lang/src/lexer"
	"llvm_lang/src/loader"
	"llvm_lang/src/parser"

	"github.com/spf13/afero"
	protocol "github.com/tliron/glsp/protocol_3_16"
)

// unimportedPackageMemberCompletions handles `pkg.<cursor>` where pkg
// itself never resolved to anything (no import, no local binding) - object
// is pkg's own Ident node. Matches object's text against Workspace's
// package index by directory base name and, for each match, offers its
// exported top-level declarations, each wired to auto-insert the matching
// `import` line via AdditionalTextEdits.
func (w *Workspace) unimportedPackageMemberCompletions(fa *FileAnalysis, object ast.NodeIndex) []protocol.CompletionItem {
	if fa.Tree.Nodes[object].Kind != enums.NodeKinds.Ident {
		return nil
	}
	name := fa.Tree.Text(object)
	currentDir := filepath.Dir(fa.Tree.File.Name)

	var items []protocol.CompletionItem
	for _, c := range w.PackageIndex() {
		if c.Name != name || c.Dir == currentDir {
			continue
		}
		edit := importEdit(fa, c.Dir)
		for _, decl := range exportedPackageDecls(w.fs, c.Dir) {
			items = append(items, protocol.CompletionItem{
				Label:               decl.Name,
				Kind:                completionItemKindPtr(symbolOutlineKindToCompletionItemKind(decl.Kind)),
				AdditionalTextEdits: []protocol.TextEdit{edit},
			})
		}
	}
	return items
}

// unimportedPackageNameCompletions offers every package in Workspace's
// index whose own name isn't already visible (imported, or shadowed by a
// local/global) in the current scope, as Module-kind candidates - accepting
// one inserts both its name and the matching `import` line.
func (w *Workspace) unimportedPackageNameCompletions(fa *FileAnalysis, visible map[string]bool) []protocol.CompletionItem {
	currentDir := filepath.Dir(fa.Tree.File.Name)

	var items []protocol.CompletionItem
	seen := make(map[string]bool)
	for _, c := range w.PackageIndex() {
		if visible[c.Name] || seen[c.Name] || c.Dir == currentDir {
			continue
		}
		seen[c.Name] = true
		items = append(items, protocol.CompletionItem{
			Label:               c.Name,
			Kind:                completionItemKindPtr(protocol.CompletionItemKindModule),
			AdditionalTextEdits: []protocol.TextEdit{importEdit(fa, c.Dir)},
		})
	}
	return items
}

// exportedPackageDecls parses every file in dir (no sema - a not-yet-
// imported candidate isn't reachable from the current program's import
// graph, so there's nothing to resolve it against yet) and returns its own
// top-level declarations, filtered to LANGUAGE.md's capitalized-name export
// rule and to the kinds actually nameable through a package qualifier -
// free functions, structs, enums, and package-level vars, never a method
// (only ever callable on a value, not the package itself - see
// packageMemberCompletions' identical use of Scope.Local, which excludes
// methods for the exact same reason: addMethod never puts them into a
// package's own top-level Scope). An unreadable/unparseable candidate
// (a race against a concurrent edit, or simply a bad file) just yields no
// declarations rather than failing the whole completion request.
func exportedPackageDecls(fs afero.Fs, dir string) []ast.DeclSymbol {
	files, err := loader.Load(fs, dir)
	if err != nil {
		return nil
	}

	var out []ast.DeclSymbol
	for _, f := range files {
		tree, _ := parser.ParseFile(lexer.NewFile(f.Name, f.Src))
		if tree == nil {
			// A hopelessly broken file (10+ parse errors) bails out with no
			// tree at all (see parser.Run) - skip it rather than crash on
			// DeclSymbols below.
			continue
		}
		for _, decl := range tree.DeclSymbols() {
			if !isExportedDeclName(decl.Name) || !isPackageQualifiableKind(decl.Kind) {
				continue
			}
			out = append(out, decl)
		}
	}
	return out
}

func symbolOutlineKindToCompletionItemKind(kind ast.SymbolOutlineKind) protocol.CompletionItemKind {
	switch kind {
	case ast.SymbolOutlineFunction:
		return protocol.CompletionItemKindFunction
	case ast.SymbolOutlineStruct:
		return protocol.CompletionItemKindStruct
	case ast.SymbolOutlineEnum:
		return protocol.CompletionItemKindEnum
	default: // SymbolOutlineVariable - the only other kind isPackageQualifiableKind allows through
		return protocol.CompletionItemKindVariable
	}
}

func isPackageQualifiableKind(kind ast.SymbolOutlineKind) bool {
	switch kind {
	case ast.SymbolOutlineFunction,
		ast.SymbolOutlineStruct,
		ast.SymbolOutlineEnum,
		ast.SymbolOutlineVariable:
		return true
	default:
		return false
	}
}

func isExportedDeclName(name string) bool {
	r, _ := utf8.DecodeRuneInString(name)
	return unicode.IsUpper(r)
}

// importEdit builds the AdditionalTextEdits entry that adds
// `import "<relative path>"` for targetDir, relative to fa's own directory
// (LANGUAGE.md's "Imports" section: a path is always relative to the
// importing file, forward-slashed, `./`-prefixed if it doesn't already
// start with a dot - matching every example there, e.g. "./mathutils",
// "../../std/mathutil"). Inserted right after the last existing top-level
// ImportDecl if any, else at the very start of the file - imports must
// precede every other top-level declaration.
func importEdit(fa *FileAnalysis, targetDir string) protocol.TextEdit {
	currentDir := filepath.Dir(fa.Tree.File.Name)
	path := targetDir
	if rel, err := filepath.Rel(currentDir, targetDir); err == nil {
		path = filepath.ToSlash(rel)
		if !strings.HasPrefix(path, ".") {
			path = "./" + path
		}
	}
	line := `import "` + path + `"`

	if last, ok := lastImportDecl(fa.Tree); ok {
		pos := byteOffsetToPosition(fa.Tree.File, fa.Tree.SpanOf(last).End)
		return protocol.TextEdit{
			Range:   protocol.Range{Start: pos, End: pos},
			NewText: "\n" + line,
		}
	}
	start := protocol.Position{Line: 0, Character: 0}
	return protocol.TextEdit{
		Range:   protocol.Range{Start: start, End: start},
		NewText: line + "\n\n",
	}
}

func lastImportDecl(tree *ast.Tree) (last ast.NodeIndex, ok bool) {
	last = ast.InvalidNode
	for decl := range tree.TopLevelDeclsOfKind(enums.NodeKinds.ImportDecl) {
		last = decl
	}
	return last, last != ast.InvalidNode
}
