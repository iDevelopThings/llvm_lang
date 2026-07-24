package lsp

import (
	"llvm_lang/src/diag"
	"llvm_lang/src/lexer"

	protocol "github.com/tliron/glsp/protocol_3_16"
)

// toProtocolDiagnostics translates every diagnostic in bag (from the lexer,
// parser, or sema - see diag.Bag's own doc comment: one uniform diagnostic
// type across every compiler stage) into LSP Diagnostics, positioned against
// file's own source text.
func toProtocolDiagnostics(file *lexer.File, bag *diag.Bag) []protocol.Diagnostic {
	if bag == nil || bag.Len() == 0 {
		return []protocol.Diagnostic{}
	}

	out := make([]protocol.Diagnostic, 0, bag.Len())
	for d := range bag.Seq() {
		sev := protocol.DiagnosticSeverityError
		if d.Severity == diag.SeverityWarning {
			sev = protocol.DiagnosticSeverityWarning
		}

		end := d.End
		if end <= d.Pos {
			// A point diagnostic (no real span) still needs a non-empty
			// range for most editors to render a visible squiggle under
			// it - one byte past Pos, clamped to the file's own length.
			end = d.Pos + 1
			if int(end) > len(file.Src) {
				end = lexer.Pos(len(file.Src))
			}
		}

		out = append(out, protocol.Diagnostic{
			Range: protocol.Range{
				Start: byteOffsetToPosition(file, d.Pos),
				End:   byteOffsetToPosition(file, end),
			},
			Severity: &sev,
			Message:  d.Msg,
		})
	}
	return out
}
