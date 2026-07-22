package lexer

// Range is a run of int32-indexed entries in some other shared array (a
// token's leading trivia in File.trivia, later a tree node's children in a
// shared child arena). Reused instead of a Go slice so a Token/Node stays a
// fixed-size value with no embedded slice header.
type Range struct {
	Start int32
	Count int32
}

// End is the exclusive end index of the range.
func (r Range) End() int32 { return r.Start + r.Count }
