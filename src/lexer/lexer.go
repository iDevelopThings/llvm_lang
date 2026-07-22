package lexer

import (
	"fmt"
	"iter"

	"llvm_lang/src/enums"
)

// Lexer scans a File's source into tokens on demand: the parser pulls one
// Token at a time via Next/Peek rather than the lexer eagerly tokenizing the
// whole file into a slice up front. That keeps memory proportional to the
// source buffer plus a one-token lookahead, lets the parser stop early
// (error recovery, speculative parses) without paying to tokenize the rest
// of the file, and needs no synchronization (unlike a channel-based
// producer/consumer split, which would add goroutine overhead on top of the
// cgo overhead this project already carries).
type Lexer struct {
	file *File
	src  string
	pos  int32

	// insertSemi tracks Go-style automatic semicolon insertion: true when
	// the last real token emitted is one after which a newline terminates
	// the statement (identifier, literal, break/continue/return/true/false,
	// or a closing ), ], }, ++, --).
	insertSemi bool

	buffered bool
	bufTok   Token

	// triviaMark is the index into file.trivia where the leading trivia of
	// the *next* emitted token begins - everything appended to file.trivia
	// from here on belongs to it, until it's emitted and the mark advances.
	triviaMark int32

	errs []Error
}

// New creates a Lexer over file. file.Src is scanned from the start.
func New(file *File) *Lexer {
	return &Lexer{
		file: file,
		src:  file.Src,
	}
}

// File returns the source file being lexed.
func (l *Lexer) File() *File { return l.file }

// Errors returns the lexical errors accumulated so far, in encounter order.
// Next keeps returning tokens (an Illegal token, or a best-effort span) past
// an error rather than stopping, so a caller can decide whether to abort or
// collect further diagnostics.
func (l *Lexer) Errors() []Error { return l.errs }

func (l *Lexer) errorf(pos int32, format string, a ...any) {
	l.errs = append(l.errs, Error{
		Pos: Pos(pos),
		Msg: fmt.Sprintf(format, a...),
	})
}

// byteAt returns the byte at pos+rel, or 0 past the end of source.
func (l *Lexer) byteAt(rel int32) byte {
	i := l.pos + rel
	if int(i) >= len(l.src) {
		return 0
	}
	return l.src[i]
}

func (l *Lexer) peek() byte  { return l.byteAt(0) }
func (l *Lexer) peek2() byte { return l.byteAt(1) }

func (l *Lexer) advanceByte() byte {
	c := l.src[l.pos]
	l.pos++
	return c
}

// Peek returns the next token without consuming it. A second call to Peek
// (with no intervening Next) returns the same token.
func (l *Lexer) Peek() Token {
	if !l.buffered {
		l.bufTok = l.scan()
		l.buffered = true
	}
	return l.bufTok
}

// Next consumes and returns the next token, ending with an unbounded run of
// Lexeme.EOF once the source is exhausted.
func (l *Lexer) Next() Token {
	if l.buffered {
		l.buffered = false
		return l.bufTok
	}
	return l.scan()
}

// All is a convenience iterator over every token through EOF (inclusive).
// The parser-facing interface is Next/Peek; All exists for tests, tools, and
// anywhere a one-shot token dump is simpler than manual iteration.
func (l *Lexer) All() iter.Seq[Token] {
	return func(yield func(Token) bool) {
		for {
			tok := l.Next()
			if !yield(tok) {
				return
			}
			if tok.Lexeme == enums.Lexemes.EOF {
				return
			}
		}
	}
}

// scan skips whitespace/comments (handling automatic semicolon insertion
// along the way) and returns the next real token, or EOF.
func (l *Lexer) scan() Token {
	if tok, ok := l.skipTrivia(); ok {
		return tok
	}
	if int(l.pos) >= len(l.src) {
		pos := Pos(l.pos)
		lex := enums.Lexemes.EOF
		if l.insertSemi {
			lex = enums.Lexemes.Semicolon
		}
		l.insertSemi = false
		tok := Token{
			Lexeme: lex,
			Start:  pos,
			End:    pos,
		}
		return l.attachLeadingTrivia(tok)
	}

	switch c := l.src[l.pos]; {
	case isIdentStart(c):
		return l.scanIdentifier()
	case isDigit(c):
		return l.scanNumber()
	case c == '"':
		return l.scanString()
	default:
		return l.scanOperator()
	}
}

// skipTrivia advances past whitespace and comments, recording each as a
// Trivia run so it isn't simply discarded (see Token.LeadingTrivia). It
// returns a virtual Semicolon token (ok=true) the moment a newline - or a
// newline-containing block comment - follows an ASI-eligible token;
// otherwise it returns ok=false once positioned at the next real token (or
// EOF).
func (l *Lexer) skipTrivia() (Token, bool) {
	wsStart := l.pos
	flushWS := func() {
		if l.pos > wsStart {
			l.file.addTrivia(TriviaKindWhitespace, wsStart, l.pos)
		}
		wsStart = l.pos
	}
	for {
		switch c := l.peek(); {
		case c == ' ' || c == '\t' || c == '\r':
			l.pos++
		case c == '\n':
			l.pos++
			l.file.markLine(l.pos)
			if l.insertSemi {
				l.insertSemi = false
				flushWS()
				// Zero-width, at l.pos: the newline byte itself was just
				// recorded as Whitespace trivia above, so the semicolon
				// must not also span it - otherwise reconstructing source
				// from trivia + token text double-counts that byte.
				semi := Token{
					Lexeme: enums.Lexemes.Semicolon,
					Start:  Pos(l.pos),
					End:    Pos(l.pos),
				}
				return l.attachLeadingTrivia(semi), true
			}
		case c == '/' && l.peek2() == '/':
			flushWS()
			start := l.pos
			l.skipLineComment()
			l.file.addTrivia(TriviaKindLineComment, start, l.pos)
			wsStart = l.pos
		case c == '/' && l.peek2() == '*':
			flushWS()
			start := l.pos
			sawNewline := l.skipBlockComment()
			l.file.addTrivia(TriviaKindBlockComment, start, l.pos)
			wsStart = l.pos
			if sawNewline && l.insertSemi {
				l.insertSemi = false
				semi := Token{
					Lexeme: enums.Lexemes.Semicolon,
					Start:  Pos(l.pos),
					End:    Pos(l.pos),
				}
				return l.attachLeadingTrivia(semi), true
			}
		default:
			flushWS()
			return Token{}, false
		}
	}
}

func (l *Lexer) skipLineComment() {
	l.pos += 2 // "//"
	for l.peek() != '\n' && int(l.pos) < len(l.src) {
		l.pos++
	}
}

// skipBlockComment consumes a /* ... */ comment and reports whether it
// contained a newline - Go-style ASI treats such a comment as a line break.
func (l *Lexer) skipBlockComment() bool {
	start := l.pos
	l.pos += 2 // "/*"
	sawNewline := false
	for {
		if int(l.pos) >= len(l.src) {
			l.errorf(start, "unterminated block comment")
			return sawNewline
		}
		switch c := l.src[l.pos]; {
		case c == '\n':
			sawNewline = true
			l.pos++
			l.file.markLine(l.pos)
		case c == '*' && l.peek2() == '/':
			l.pos += 2
			return sawNewline
		default:
			l.pos++
		}
	}
}

// finish applies the ASI rule for the just-scanned token before returning it.
func (l *Lexer) finish(tok Token) Token {
	l.insertSemi = asiEligible(tok)
	return l.attachLeadingTrivia(tok)
}

// attachLeadingTrivia gives tok everything accumulated in file.trivia since
// the previous token was emitted, then advances the mark past it so the
// next token starts accumulating fresh trivia.
func (l *Lexer) attachLeadingTrivia(tok Token) Token {
	end := int32(len(l.file.trivia))
	tok.LeadingTrivia = Range{
		Start: l.triviaMark,
		Count: end - l.triviaMark,
	}
	l.triviaMark = end
	return tok
}

// asiEligible reports whether a newline immediately after tok should be
// turned into a virtual semicolon (mirrors Go's automatic semicolon rule).
func asiEligible(t Token) bool {
	switch t.Lexeme {
	case enums.Lexemes.Number,
		enums.Lexemes.String,
		enums.Lexemes.RightParen,
		enums.Lexemes.RightBracket,
		enums.Lexemes.RightBrace,
		enums.Lexemes.PlusPlus,
		enums.Lexemes.MinusMinus:
		return true
	case enums.Lexemes.Identifier:
		switch t.Keyword {
		case "",
			enums.Keywords.Break,
			enums.Keywords.Continue,
			enums.Keywords.Return,
			enums.Keywords.True,
			enums.Keywords.False:
			return true
		default:
			return false
		}
	default:
		return false
	}
}

func isIdentStart(c byte) bool {
	return c == '_' || (c|0x20 >= 'a' && c|0x20 <= 'z')
}

func isIdentPart(c byte) bool {
	return isIdentStart(c) || isDigit(c)
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }
