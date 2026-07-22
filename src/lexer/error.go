package lexer

import "fmt"

// Error is a lexical error tied to a source position. Formatting a full
// "file:line:col: msg" string is left to the caller (via File.Position),
// since the error itself doesn't hold a *File.
type Error struct {
	Pos Pos
	Msg string
}

func (e Error) Error() string {
	return fmt.Sprintf("offset %d: %s", e.Pos, e.Msg)
}
