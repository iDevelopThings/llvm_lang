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

// SemanticTokensLegend is this server's fixed token-type legend, advertised
// once at initialize time (see cmd/llvmc-lsp/main.go) and referenced by
// index from every textDocument/semanticTokens/full response. No modifiers
// are used this round - an empty TokenModifiers legend is valid LSP.
func SemanticTokensLegend() protocol.SemanticTokensLegend {
	return protocol.SemanticTokensLegend{
		TokenTypes:     semanticTokenTypeNames,
		TokenModifiers: []string{},
	}
}

// rawToken is one classified token, in absolute line/character terms,
// before being sorted and delta-encoded into the wire format.
type rawToken struct {
	line, char int // both already UTF-16-based (see byteOffsetToPosition)
	length     int // in UTF-16 units, not bytes
	typeIdx    int
}

// SemanticTokens answers a textDocument/semanticTokens/full request: every
// classifiable token in path's source, encoded per the LSP spec's relative
// delta format (deltaLine, deltaStartChar, length, tokenType, tokenModifiers
// per token, sorted by position). nil when path has no analysis yet.
//
// Two independent passes feed the token list: walking the already-parsed
// Tree (keywords, identifiers - refined via Info.Refs when resolved,
// operators, literals - see collectNodeTokens) and a fresh, throwaway
// re-lex of the same source purely for comment trivia (see
// collectCommentTokens - comments aren't represented as ast.Node's at all,
// see ast.Node's own doc comment, so they can't be reached by walking the
// tree). Re-lexing is cheap relative to the rest of this request (same
// order of cost as the original parse - see BENCHMARKS.md) and deliberately
// uses its own fresh *lexer.File rather than the Tree's own File, so it
// can't perturb that File's already-built trivia arena/line table.
func (w *Workspace) SemanticTokens(path string) *protocol.SemanticTokens {
	fa, ok := w.Analysis(path)
	if !ok || fa.Tree == nil {
		return nil
	}

	var raw []rawToken
	collectNodeTokens(fa.Tree, fa.Info, fa.Tree.Root, &raw)
	collectCommentTokens(fa.Tree.File.Name, fa.Tree.File.Src, &raw)

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
			protocol.UInteger(0), // no modifiers this round
		)
		prevLine, prevChar = t.line, t.char
	}

	return &protocol.SemanticTokens{Data: data}
}

// collectNodeTokens walks tree from n, classifying every node's own "main"
// token (see ast.Node's doc comment - most kinds carry one; Block/CallExpr/
// ParenExpr/... deliberately don't, and are skipped via the Start==End zero
// -Token check) into out.
func collectNodeTokens(tree *ast.Tree, info *sema.Info, n ast.NodeIndex, out *[]rawToken) {
	if n == ast.InvalidNode {
		return
	}
	node := &tree.Nodes[n]
	if node.Tok.Start != node.Tok.End {
		if typeIdx, ok := classifyNodeToken(node.Tok, info, n); ok {
			appendSpanToken(tree.File, node.Tok.Start, node.Tok.End, typeIdx, out)
		}
	}
	for _, c := range tree.Children(n) {
		collectNodeTokens(tree, info, c, out)
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
// UnaryExpr operator. Highlighting it would need a third token-collection
// pass (re-lexing for punctuation generally, mirroring
// collectCommentTokens), not just adding it to the operator case below -
// not worth the complexity for one punctuation token this round.
func classifyNodeToken(tok lexer.Token, info *sema.Info, n ast.NodeIndex) (int, bool) {
	if tok.Keyword != "" {
		return semTokKeyword, true
	}
	switch tok.Lexeme {
	case enums.Lexemes.Number:
		return semTokNumber, true
	case enums.Lexemes.String:
		return semTokString, true
	case enums.Lexemes.Identifier:
		return classifyIdentToken(info, n)
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
		return semTokOperator, true
	default:
		return 0, false
	}
}

// classifyIdentToken refines an identifier token via its own resolved
// Symbol (Info.Refs), when Check/Resolve reached it at all - falling back to
// a plain variable classification (this package's own documented fallback,
// see the approved plan) when info is nil, or the node was never resolved
// (an undefined-name error, or a node kind Resolve/Check deliberately never
// populates Refs for).
func classifyIdentToken(info *sema.Info, n ast.NodeIndex) (int, bool) {
	if info == nil {
		return semTokVariable, true
	}
	sym, ok := info.Refs[n]
	if !ok || sym == nil {
		return semTokVariable, true
	}
	switch sym.Kind {
	case sema.SymFunc:
		return semTokFunction, true
	case sema.SymStruct:
		return semTokStruct, true
	case sema.SymEnum:
		return semTokEnum, true
	case sema.SymEnumVariant:
		return semTokEnumMember, true
	case sema.SymField:
		return semTokProperty, true
	case sema.SymBuiltinType:
		return semTokType, true
	case sema.SymPackage:
		return semTokNamespace, true
	case sema.SymConstructor,
		sema.SymDestructor:
		return semTokMethod, true
	case sema.SymParam:
		return semTokParameter, true
	default: // SymVar, SymReceiver, SymBuiltinValue
		return semTokVariable, true
	}
}

// collectCommentTokens re-lexes src (see SemanticTokens' own doc comment for
// why this is a fresh, throwaway File/Lexer rather than reusing the Tree's
// own File) purely to reach comment trivia - never represented as ast.Nodes
// (see ast.Node's own doc comment) - classifying each into out.
func collectCommentTokens(name, src string, out *[]rawToken) {
	file := lexer.NewFile(name, src)
	lx := lexer.New(file)
	for tok := range lx.All() {
		for _, tr := range file.Trivia(tok.LeadingTrivia) {
			if tr.Kind != lexer.TriviaKinds.LineComment && tr.Kind != lexer.TriviaKinds.BlockComment {
				continue
			}
			appendSpanToken(file, tr.Start, tr.End, semTokComment, out)
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
func appendSpanToken(file *lexer.File, start, end lexer.Pos, typeIdx int, out *[]rawToken) {
	startPos := byteOffsetToPosition(file, start)
	endPos := byteOffsetToPosition(file, end)
	if startPos.Line != endPos.Line {
		return
	}
	*out = append(*out, rawToken{
		line:    int(startPos.Line),
		char:    int(startPos.Character),
		length:  int(endPos.Character - startPos.Character),
		typeIdx: typeIdx,
	})
}
