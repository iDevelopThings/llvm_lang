package lexer

// TriviaKind classifies a run of skipped source text: whitespace/newlines,
// or a comment. It's a small lexer-internal classification (not a
// cross-cutting language enum like Lexeme/Keyword), so it stays a plain Go
// type rather than going through enum_codegen.
type TriviaKind uint8

const (
	TriviaWhitespace TriviaKind = iota
	TriviaLineComment
	TriviaBlockComment
)

func (k TriviaKind) String() string {
	switch k {
	case TriviaWhitespace:
		return "Whitespace"
	case TriviaLineComment:
		return "LineComment"
	case TriviaBlockComment:
		return "BlockComment"
	default:
		return "Unknown"
	}
}

// Trivia is one contiguous run of skipped source text - whitespace or a
// comment - kept so the exact source can be reconstructed from tokens, and
// so comments are available to anything that needs them (an eventual
// formatter, hover, doc-comment extraction), instead of being silently
// discarded during lexing.
type Trivia struct {
	Kind  TriviaKind
	Start Pos
	End   Pos
}
