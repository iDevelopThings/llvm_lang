package lsp

import (
	"sort"

	"llvm_lang/src/ast"
	"llvm_lang/src/enums"
	"llvm_lang/src/lexer"
	"llvm_lang/src/sema"

	protocol "github.com/tliron/glsp/protocol_3_16"
)

// Semantic token type legend - the constant's own value is the index
// encoded into every textDocument/semanticTokens/full response (see
// SemanticTokens below), so this order must exactly match
// semanticTokenTypeNames/SemanticTokensLegend.
const (
	semTokKeyword = iota
	semTokOperator
	semTokComment
	semTokString
	semTokNumber
	semTokType
	semTokStruct
	semTokEnum
	semTokEnumMember
	semTokNamespace
	semTokFunction
	semTokMethod
	semTokParameter
	semTokVariable
	semTokProperty
)

var semanticTokenTypeNames = []string{
	semTokKeyword:    string(protocol.SemanticTokenTypeKeyword),
	semTokOperator:   string(protocol.SemanticTokenTypeOperator),
	semTokComment:    string(protocol.SemanticTokenTypeComment),
	semTokString:     string(protocol.SemanticTokenTypeString),
	semTokNumber:     string(protocol.SemanticTokenTypeNumber),
	semTokType:       string(protocol.SemanticTokenTypeType),
	semTokStruct:     string(protocol.SemanticTokenTypeStruct),
	semTokEnum:       string(protocol.SemanticTokenTypeEnum),
	semTokEnumMember: string(protocol.SemanticTokenTypeEnumMember),
	semTokNamespace:  string(protocol.SemanticTokenTypeNamespace),
	semTokFunction:   string(protocol.SemanticTokenTypeFunction),
	semTokMethod:     string(protocol.SemanticTokenTypeMethod),
	semTokParameter:  string(protocol.SemanticTokenTypeParameter),
	semTokVariable:   string(protocol.SemanticTokenTypeVariable),
	semTokProperty:   string(protocol.SemanticTokenTypeProperty),
}

// Semantic token modifier legend - bit position is the modifier bit set in
// each token's own modifiers bitmask.
const modReadonly = 1 << 0

var semanticTokenModifierNames = []string{
	string(protocol.SemanticTokenModifierReadonly),
}

// SemanticTokensLegend is this server's fixed token-type/modifier legend,
// advertised once at initialize time (see cmd/llvmc-lsp/main.go) and
// referenced by index from every textDocument/semanticTokens/full response.
func SemanticTokensLegend() protocol.SemanticTokensLegend {
	return protocol.SemanticTokensLegend{
		TokenTypes:     semanticTokenTypeNames,
		TokenModifiers: semanticTokenModifierNames,
	}
}

// rawToken is one classified token, in absolute line/character terms,
// before being sorted and delta-encoded into the wire format.
type rawToken struct {
	line, char int // both already UTF-16-based (see byteOffsetToPosition)
	length     int // in UTF-16 units, not bytes
	typeIdx    int
	modifiers  int
}

// SemanticTokens answers a textDocument/semanticTokens/full request: every
// classifiable token in path's source, encoded per the LSP spec's relative
// delta format (deltaLine, deltaStartChar, length, tokenType, tokenModifiers
// per token, sorted by position). nil when path has no analysis yet.
//
// Three passes build the token list: collectReassignedSymbols finds every
// variable/parameter ever reassigned anywhere in the file (needed up front,
// since a reassignment can appear after the point being classified);
// collectNodeTokens walks the parsed Tree, classifying each node's own main
// token; collectLexicalExtras re-lexes the raw source for what the tree walk
// alone can't reach - see its own doc comment.
func (w *Workspace) SemanticTokens(path string) *protocol.SemanticTokens {
	fa, ok := w.Analysis(path)
	if !ok || fa.Tree == nil {
		return nil
	}

	reassigned := make(map[*sema.Symbol]bool)
	if fa.Info != nil {
		collectReassignedSymbols(fa.Tree, fa.Info, fa.Tree.Root, reassigned)
	}

	covered := make(map[lexer.Pos]bool)
	var raw []rawToken
	collectNodeTokens(fa.Tree, fa.Info, fa.Tree.Root, reassigned, covered, &raw)
	collectLexicalExtras(fa.Tree.File.Name, fa.Tree.File.Src, covered, &raw)

	sort.Slice(raw, func(i, j int) bool {
		if raw[i].line != raw[j].line {
			return raw[i].line < raw[j].line
		}
		return raw[i].char < raw[j].char
	})

	data := make([]protocol.UInteger, 0, len(raw)*5)
	prevLine, prevChar := 0, 0
	for _, t := range raw {
		deltaLine := t.line - prevLine
		deltaChar := t.char
		if deltaLine == 0 {
			deltaChar = t.char - prevChar
		}
		data = append(data,
			protocol.UInteger(deltaLine),
			protocol.UInteger(deltaChar),
			protocol.UInteger(t.length),
			protocol.UInteger(t.typeIdx),
			protocol.UInteger(t.modifiers),
		)
		prevLine, prevChar = t.line, t.char
	}

	return &protocol.SemanticTokens{Data: data}
}

// collectReassignedSymbols walks tree from n, recording into out every
// symbol that's ever the target of a plain assignment (AssignStmt,
// MultiAssignStmt) or increment/decrement (IncDecStmt) anywhere - the
// signal classifyIdentToken uses to decide the readonly modifier (see
// SemanticTokens' own doc comment for why this has to be its own pass,
// separate from classification, and modReadonly's own doc comment for why
// that modifier matters beyond just being "more accurate": LSP4IJ's default
// color mapping for a modifier-less "variable" token is
// REASSIGNED_LOCAL_VARIABLE, not LOCAL_VARIABLE - visually indistinguishable
// from a real reassignment (typically an underline) for every variable this
// server tags, whether or not it's ever actually reassigned, unless this
// modifier says otherwise). Only a bare Ident target counts - `p.field = v`/
// `arr[i] = v`/`*p = v` don't reassign p/arr/p themselves - markReassignedTarget
// checks the target's own Kind explicitly for exactly this reason: a
// MemberExpr/IndexExpr/UnaryExpr(*) target still gets a real Info.Refs entry
// (see typecheck.go's checkMemberExpr/checkLValue), so skipping it isn't
// something the AssignStmt/IncDecStmt/MultiAssignStmt cases below get "for
// free" just by shape - it has to be checked.
func collectReassignedSymbols(tree *ast.Tree, info *sema.Info, n ast.NodeIndex, out map[*sema.Symbol]bool) {
	if n == ast.InvalidNode {
		return
	}
	switch tree.Nodes[n].Kind {
	case enums.NodeKinds.AssignStmt,
		enums.NodeKinds.IncDecStmt:
		markReassignedTarget(tree, info, tree.Child(n, 0), out)
	case enums.NodeKinds.MultiAssignStmt:
		for _, target := range tree.MultiAssignStmtTargets(n) {
			markReassignedTarget(tree, info, target, out)
		}
	}
	for _, c := range tree.Children(n) {
		collectReassignedSymbols(tree, info, c, out)
	}
}

// markReassignedTarget records target's own resolved Symbol into out, but
// only when target is itself a bare Ident - see collectReassignedSymbols'
// own doc comment for why that Kind check is load-bearing, not redundant.
func markReassignedTarget(tree *ast.Tree, info *sema.Info, target ast.NodeIndex, out map[*sema.Symbol]bool) {
	if target == ast.InvalidNode || tree.Nodes[target].Kind != enums.NodeKinds.Ident {
		return
	}
	sym, ok := info.Refs[target]
	if ok && sym != nil {
		out[sym] = true
	}
}

// collectNodeTokens walks tree from n, classifying every node's own "main"
// token (see ast.Node's doc comment - most kinds carry one; Block/CallExpr/
// ParenExpr/... deliberately don't, and are skipped via the Start==End zero
// -Token check) into out, and recording each emitted token's own start
// offset into covered - see collectLexicalExtras' own doc comment for why.
func collectNodeTokens(tree *ast.Tree, info *sema.Info, n ast.NodeIndex, reassigned map[*sema.Symbol]bool, covered map[lexer.Pos]bool, out *[]rawToken) {
	if n == ast.InvalidNode {
		return
	}
	node := &tree.Nodes[n]
	if node.Tok.Start != node.Tok.End {
		if typeIdx, modifiers, ok := classifyNodeToken(node.Tok, info, n, reassigned); ok {
			covered[node.Tok.Start] = true
			appendSpanToken(tree.File, node.Tok.Start, node.Tok.End, typeIdx, modifiers, out)
		}
	}
	for _, c := range tree.Children(n) {
		collectNodeTokens(tree, info, c, reassigned, covered, out)
	}
}

// classifyNodeToken classifies tok - a node's own main token, at node n -
// into a semantic token type, or reports ok=false for anything this legend
// has no matching type for (punctuation: parens/braces/brackets/comma/dot/
// colon/semicolon/question - left to the client's own base grammar/theme).
// The fat arrow (`=>`, a match arm's own pattern/body separator - see
// LANGUAGE.md's "match" section) falls into this same "no matching type"
// bucket for a different reason: it isn't reachable at all via this
// function, since MatchArm's own Tok is unused (see ast.Node's doc comment)
// - no node anywhere carries `=>` as its main token, unlike a BinaryExpr/
// UnaryExpr operator. collectLexicalExtras' own re-lex pass would catch it
// structurally the same way it catches an uncaptured keyword, but doesn't:
// it only classifies a re-lexed token when tok.Keyword != "", and `=>` is
// an operator, not a keyword - extending that check to operators generally
// is a reasonable future addition if punctuation highlighting is ever
// wanted, not something this round needed.
func classifyNodeToken(tok lexer.Token, info *sema.Info, n ast.NodeIndex, reassigned map[*sema.Symbol]bool) (typeIdx, modifiers int, ok bool) {
	if tok.Keyword != "" {
		return semTokKeyword, 0, true
	}
	switch tok.Lexeme {
	case enums.Lexemes.Number:
		return semTokNumber, 0, true
	case enums.Lexemes.String:
		return semTokString, 0, true
	case enums.Lexemes.Identifier:
		return classifyIdentToken(info, n, reassigned)
	case enums.Lexemes.Plus,
		enums.Lexemes.Minus,
		enums.Lexemes.Slash,
		enums.Lexemes.Asterisk,
		enums.Lexemes.Percent,
		enums.Lexemes.Caret,
		enums.Lexemes.Equal,
		enums.Lexemes.LessThan,
		enums.Lexemes.GreaterThan,
		enums.Lexemes.LessThanEqual,
		enums.Lexemes.GreaterThanEqual,
		enums.Lexemes.NotEqual,
		enums.Lexemes.And,
		enums.Lexemes.Or,
		enums.Lexemes.Not,
		enums.Lexemes.ColonEqual,
		enums.Lexemes.EqualEqual,
		enums.Lexemes.PlusPlus,
		enums.Lexemes.MinusMinus,
		enums.Lexemes.PlusEqual,
		enums.Lexemes.MinusEqual,
		enums.Lexemes.AsteriskEqual,
		enums.Lexemes.SlashEqual,
		enums.Lexemes.Ampersand,
		enums.Lexemes.Pipe:
		return semTokOperator, 0, true
	default:
		return 0, 0, false
	}
}

// classifyIdentToken refines an identifier token via its own resolved
// Symbol (Info.Refs), when Check/Resolve reached it at all - falling back to
// a plain variable classification (this package's own documented fallback,
// see the approved plan) when info is nil, or the node was never resolved
// (an undefined-name error, a generic template's own unresolved body - see
// sema.ResolveTemplateForTooling's own doc comment for why that's only a
// partial mitigation - or a node kind Resolve/Check deliberately never
// populates Refs for). The fallback always carries modReadonly (never 0):
// LSP4IJ's default color mapping renders a modifier-less "variable" token as
// REASSIGNED_LOCAL_VARIABLE (underlined) regardless of whether it's actually
// a variable at all, so every unresolved identifier in the file would
// otherwise render as a wall of spurious underlines - see
// collectReassignedSymbols' own doc comment for the identical reasoning
// applied to a real SymVar/SymParam/SymReceiver.
//
// A SymVar/SymParam/SymReceiver that reassigned doesn't contain gets the
// readonly modifier too, for the same reason.
func classifyIdentToken(info *sema.Info, n ast.NodeIndex, reassigned map[*sema.Symbol]bool) (typeIdx, modifiers int, ok bool) {
	if info == nil {
		return semTokVariable, modReadonly, true
	}
	sym, refOK := info.Refs[n]
	if !refOK || sym == nil {
		return semTokVariable, modReadonly, true
	}
	switch sym.Kind {
	case sema.SymFunc:
		return semTokFunction, 0, true
	case sema.SymStruct:
		return semTokStruct, 0, true
	case sema.SymEnum:
		return semTokEnum, 0, true
	case sema.SymEnumVariant:
		return semTokEnumMember, 0, true
	case sema.SymField:
		return semTokProperty, 0, true
	case sema.SymBuiltinType:
		return semTokType, 0, true
	case sema.SymPackage:
		return semTokNamespace, 0, true
	case sema.SymConstructor,
		sema.SymDestructor:
		return semTokMethod, 0, true
	case sema.SymParam:
		return semTokParameter, variableModifiers(sym, reassigned), true
	default: // SymVar, SymReceiver, SymBuiltinValue
		return semTokVariable, variableModifiers(sym, reassigned), true
	}
}

// variableModifiers returns modReadonly iff sym is never reassigned
// anywhere in the file (see collectReassignedSymbols).
func variableModifiers(sym *sema.Symbol, reassigned map[*sema.Symbol]bool) int {
	if reassigned[sym] {
		return 0
	}
	return modReadonly
}

// collectLexicalExtras re-lexes src (see SemanticTokens' own doc comment for
// why this is a fresh, throwaway File/Lexer) to reach comment trivia (never
// represented as ast.Nodes - see ast.Node's own doc comment) and any keyword
// whose own token isn't captured as any node's Tok (e.g. "else"/"import"/
// the "map" in map[K]V - see the SemanticTokens doc comment for the full
// list and why). covered is every start offset collectNodeTokens already
// emitted a token for, checked here so a keyword captured both ways isn't
// emitted twice - a coverage check against what was actually emitted, not a
// name-based allowlist, so it doesn't silently stop covering some other
// keyword-shaped gap that appears later.
func collectLexicalExtras(name, src string, covered map[lexer.Pos]bool, out *[]rawToken) {
	file := lexer.NewFile(name, src)
	lx := lexer.New(file)
	for tok := range lx.All() {
		for _, tr := range file.Trivia(tok.LeadingTrivia) {
			if tr.Kind != lexer.TriviaKinds.LineComment && tr.Kind != lexer.TriviaKinds.BlockComment {
				continue
			}
			appendSpanToken(file, tr.Start, tr.End, semTokComment, 0, out)
		}
		if tok.Keyword != "" && !covered[tok.Start] {
			appendSpanToken(file, tok.Start, tok.End, semTokKeyword, 0, out)
		}
		if tok.Lexeme == enums.Lexemes.EOF {
			break
		}
	}
}

// appendSpanToken converts a byte span into a rawToken, skipping it
// entirely if it crosses a line boundary - the LSP spec requires every
// semantic token to be single-line (this can only actually happen for a
// multi-line block comment; every other token kind this package classifies
// is inherently single-line already).
func appendSpanToken(file *lexer.File, start, end lexer.Pos, typeIdx, modifiers int, out *[]rawToken) {
	startPos := byteOffsetToPosition(file, start)
	endPos := byteOffsetToPosition(file, end)
	if startPos.Line != endPos.Line {
		return
	}
	*out = append(*out, rawToken{
		line:      int(startPos.Line),
		char:      int(startPos.Character),
		length:    int(endPos.Character - startPos.Character),
		typeIdx:   typeIdx,
		modifiers: modifiers,
	})
}
