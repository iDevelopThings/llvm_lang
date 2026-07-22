package lexer

import "llvm_lang/src/enums"

// keywordLookup maps a keyword's exact source spelling (e.g. "var") to its
// enums.Keyword, built once from the generated metadata rather than
// hand-duplicated here. Built eagerly at package init: the table is tiny
// (one entry per keyword) and this keeps every scanIdentifier call down to a
// single map read, with no per-lookup allocation - unlike enums.Keywords.Parse,
// which lowercases/trims for case-insensitive config-style parsing and
// allocates an error on every miss, unsuitable for a hot path where most
// identifiers are not keywords.
var keywordLookup = buildKeywordLookup()

func buildKeywordLookup() map[string]enums.Keyword {
	infos := enums.Keywords.Infos()
	m := make(map[string]enums.Keyword, len(infos))
	for _, info := range infos {
		m[info.String] = info.Keyword
	}
	return m
}

func (l *Lexer) scanIdentifier() Token {
	start := l.pos
	l.pos++
	for isIdentPart(l.peek()) {
		l.pos++
	}
	tok := Token{
		Lexeme: enums.Lexemes.Identifier,
		Start:  Pos(start),
		End:    Pos(l.pos),
	}
	if kw, ok := keywordLookup[l.src[start:l.pos]]; ok {
		tok.Keyword = kw
	}
	return l.finish(tok)
}

// scanNumber scans an integer or decimal float literal (digits, an optional
// fractional part, an optional e/E exponent). Hex/octal/binary prefixes and
// digit-separator underscores aren't handled yet - not needed by the
// language as specified so far, easy to add when they are.
func (l *Lexer) scanNumber() Token {
	start := l.pos
	for isDigit(l.peek()) {
		l.pos++
	}
	if l.peek() == '.' && isDigit(l.peek2()) {
		l.pos++
		for isDigit(l.peek()) {
			l.pos++
		}
	}
	if c := l.peek(); c == 'e' || c == 'E' {
		next := l.byteAt(1)
		signed := (next == '+' || next == '-') && isDigit(l.byteAt(2))
		if isDigit(next) || signed {
			l.pos++
			if c2 := l.peek(); c2 == '+' || c2 == '-' {
				l.pos++
			}
			for isDigit(l.peek()) {
				l.pos++
			}
		}
	}
	tok := Token{
		Lexeme: enums.Lexemes.Number,
		Start:  Pos(start),
		End:    Pos(l.pos),
	}
	return l.finish(tok)
}

// scanString scans a double-quoted string literal, including its quotes, in
// the token span. Escape decoding is deferred to File.StringValue so a token
// whose value is never needed (e.g. discarded during error recovery) never
// pays for it.
func (l *Lexer) scanString() Token {
	start := l.pos
	l.pos++ // opening quote
	for {
		if int(l.pos) >= len(l.src) {
			l.errorf(start, "unterminated string literal")
			tok := Token{
				Lexeme: enums.Lexemes.String,
				Start:  Pos(start),
				End:    Pos(l.pos),
			}
			return l.finish(tok)
		}
		switch c := l.src[l.pos]; c {
		case '\n':
			l.errorf(start, "unterminated string literal")
			tok := Token{
				Lexeme: enums.Lexemes.String,
				Start:  Pos(start),
				End:    Pos(l.pos),
			}
			return l.finish(tok)
		case '\\':
			l.pos++
			if int(l.pos) < len(l.src) {
				l.pos++ // consume the escaped char, whatever it is
			}
		case '"':
			l.pos++
			tok := Token{
				Lexeme: enums.Lexemes.String,
				Start:  Pos(start),
				End:    Pos(l.pos),
			}
			return l.finish(tok)
		default:
			l.pos++
		}
	}
}

func (l *Lexer) scanOperator() Token {
	start := l.pos
	c := l.advanceByte()
	lex, ok := l.matchOperator(c)
	if !ok {
		l.errorf(start, "unexpected character %q", c)
		illegal := Token{
			Lexeme: enums.Lexemes.Illegal,
			Start:  Pos(start),
			End:    Pos(l.pos),
		}
		return l.finish(illegal)
	}
	tok := Token{
		Lexeme: lex,
		Start:  Pos(start),
		End:    Pos(l.pos),
	}
	return l.finish(tok)
}

// matchOperator resolves punctuation, given its first byte c already
// consumed; it consumes a further byte itself when a longer operator
// matches. A handwritten switch compiles to a jump table and needs no
// lookup structure over the generated Lexeme metadata.
func (l *Lexer) matchOperator(c byte) (enums.Lexeme, bool) {
	// two consumes next if it matches, returning ifYes; otherwise ifNo.
	two := func(next byte, ifYes, ifNo enums.Lexeme) enums.Lexeme {
		if l.peek() == next {
			l.pos++
			return ifYes
		}
		return ifNo
	}
	switch c {
	case '+':
		if l.peek() == '+' {
			l.pos++
			return enums.Lexemes.PlusPlus, true
		}
		return two('=', enums.Lexemes.PlusEqual, enums.Lexemes.Plus), true
	case '-':
		if l.peek() == '-' {
			l.pos++
			return enums.Lexemes.MinusMinus, true
		}
		return two('=', enums.Lexemes.MinusEqual, enums.Lexemes.Minus), true
	case '*':
		return two('=', enums.Lexemes.AsteriskEqual, enums.Lexemes.Asterisk), true
	case '/':
		return two('=', enums.Lexemes.SlashEqual, enums.Lexemes.Slash), true
	case '%':
		return enums.Lexemes.Percent, true
	case '^':
		return enums.Lexemes.Caret, true
	case '(':
		return enums.Lexemes.LeftParen, true
	case ')':
		return enums.Lexemes.RightParen, true
	case '{':
		return enums.Lexemes.LeftBrace, true
	case '}':
		return enums.Lexemes.RightBrace, true
	case '[':
		return enums.Lexemes.LeftBracket, true
	case ']':
		return enums.Lexemes.RightBracket, true
	case ',':
		return enums.Lexemes.Comma, true
	case '.':
		return enums.Lexemes.Dot, true
	case ':':
		return two('=', enums.Lexemes.ColonEqual, enums.Lexemes.Colon), true
	case ';':
		return enums.Lexemes.Semicolon, true
	case '=':
		return two('=', enums.Lexemes.EqualEqual, enums.Lexemes.Equal), true
	case '<':
		return two('=', enums.Lexemes.LessThanEqual, enums.Lexemes.LessThan), true
	case '>':
		return two('=', enums.Lexemes.GreaterThanEqual, enums.Lexemes.GreaterThan), true
	case '!':
		return two('=', enums.Lexemes.NotEqual, enums.Lexemes.Not), true
	case '&':
		return two('&', enums.Lexemes.And, enums.Lexemes.Ampersand), true
	case '|':
		return two('|', enums.Lexemes.Or, enums.Lexemes.Pipe), true
	case '?':
		return enums.Lexemes.Question, true
	default:
		return 0, false
	}
}
