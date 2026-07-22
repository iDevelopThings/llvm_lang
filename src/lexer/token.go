package lexer

import "llvm_lang/src/enums"

// Pos is a byte offset into a File's source text. int32 keeps Token compact;
// source files over 2GB aren't a concern here.
type Pos int32

// Token is a single lexical token: its kind, an optional resolved keyword,
// and the [Start, End) byte span it occupies in the source. The span is the
// only source of the token's text - callers read it back via File.Lexeme (or
// File.StringValue for an escaped string literal) - so Token stays a small,
// fixed-size value with no embedded string data, cheap to copy and to buffer
// for lookahead.
type Token struct {
	Lexeme  enums.Lexeme
	Keyword enums.Keyword // zero value ("") when Lexeme isn't a reserved word
	Start   Pos
	End     Pos

	// LeadingTrivia is the run of whitespace/comments between the end of
	// the previous token and the start of this one (see File.Trivia).
	// Every token owns the trivia before it, right through EOF, so
	// concatenating each token's leading trivia text with its own text, in
	// order, reproduces the source exactly.
	LeadingTrivia Range
}

// Len reports the token's span length in bytes.
func (t Token) Len() int { return int(t.End - t.Start) }
