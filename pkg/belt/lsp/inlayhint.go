package lsp

import (
	"encoding/json"

	protocol "github.com/owenrumney/go-lsp/lsp"

	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// inlayHints returns a type hint for each constant whose source omits the type
// annotation but whose type the analyzer inferred — rendered just after the
// name, as ": <type>", the annotation the user could have written — and the
// same for each function literal's inferred parameter and result types
// (lambdaHints). Only hints inside [startOff, endOff] (the range the editor
// asked about) are returned.
//
// Each hint carries a TextEdit that inserts the same annotation, so accepting
// the hint materializes it in the source.
func inlayHints(doc view, startOff, endOff int) []protocol.InlayHint {
	buf := doc.Buffer()
	trees := doc.Trees()
	kind := protocol.InlayHintKindType

	var hints []protocol.InlayHint
	for _, c := range doc.Module().Consts {
		// An annotated constant already shows its type; one whose type is (or
		// contains) an unsolved part has nothing useful to show.
		if c.Syntax.Type != nil || ir.HasInvalid(c.Type) {
			continue
		}
		declTree, ok := trees[c.Syntax.Syntax()]
		if !ok {
			continue
		}
		nameTok, ok := nameToken(declTree)
		if !ok {
			continue
		}
		at := nameTok.End()
		if at < startOff || at > endOff {
			continue
		}

		annotation := ": " + c.Type.String()
		label, err := json.Marshal(annotation)
		if err != nil {
			continue
		}
		hints = append(hints, protocol.InlayHint{
			Position:  toPosition(buf, at),
			Label:     label,
			Kind:      &kind,
			TextEdits: []protocol.TextEdit{{Range: toRange(buf, at, at), NewText: annotation}},
		})
	}
	return append(hints, lambdaHints(doc, trees, startOff, endOff)...)
}
