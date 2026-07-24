package lsp

import (
	"llvm_lang/src/ast"
	"llvm_lang/src/lexer"

	protocol "github.com/tliron/glsp/protocol_3_16"
)

// byteOffsetToPosition converts a byte offset into file's source text into
// an LSP Position - UTF-16 code units, not bytes (the LSP spec's own
// position encoding; see the "position" section of the spec) - distinct
// from lexer.File.Position, which resolves a byte offset to a 1-based
// byte-column Position meant only for this compiler's own terminal
// diagnostics (see diag.FormatSnippet).
//
// Deliberately stays in src/lsp, not lexer.File: UTF-16 encoding is an
// LSP-protocol concern with no reason to leak into the lexer's own
// terminal-diagnostic-only Position (which only ever needs a byte column) -
// see AGENTS.md's Architecture section.
func byteOffsetToPosition(file *lexer.File, pos lexer.Pos) protocol.Position {
	p := file.Position(pos)
	lineText := file.Line(p.Line)

	byteCol := p.Column - 1 // Position.Column is 1-based; we need a 0-based byte index into lineText
	if byteCol < 0 {
		byteCol = 0
	}
	if byteCol > len(lineText) {
		byteCol = len(lineText)
	}

	return protocol.Position{
		Line:      protocol.UInteger(p.Line - 1), // LSP lines are 0-based
		Character: protocol.UInteger(utf16Len(lineText[:byteCol])),
	}
}

// utf16Len counts s's length in UTF-16 code units - 1 per rune, except a
// rune outside the Basic Multilingual Plane (>= U+10000), which LSP (like
// JavaScript/UTF-16 generally) counts as a surrogate pair, i.e. 2 units.
func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		if r >= 0x10000 {
			n += 2
		} else {
			n++
		}
	}
	return n
}

// positionToByteOffset converts an LSP Position (UTF-16 line/character) back
// to a byte offset into src - the reverse of byteOffsetToPosition, used
// whenever a request (hover, definition, ...) hands this package a cursor
// position to resolve against the parsed Tree. Reuses glsp's own
// Position.IndexIn, which already implements the identical UTF-16-aware
// walk (see protocol.Position's own doc comment) - no reason to hand-roll a
// second version of the same conversion in the opposite direction.
func positionToByteOffset(src string, pos protocol.Position) lexer.Pos {
	return lexer.Pos(pos.IndexIn(src))
}

// spanToRange converts an ast.Span (byte offsets) to a protocol.Range
// (UTF-16 positions) - the "build a Range for this node's span" idiom
// every request handler that returns a Location/Hover/DocumentSymbol needs.
func spanToRange(file *lexer.File, span ast.Span) protocol.Range {
	return protocol.Range{
		Start: byteOffsetToPosition(file, span.Start),
		End:   byteOffsetToPosition(file, span.End),
	}
}
