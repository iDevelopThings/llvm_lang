package lexer

//go:generate go run ../../cmd/enum_codegen -in ./trivia_kind.yml

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
