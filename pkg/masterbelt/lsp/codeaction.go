package lsp

import (
	"github.com/masterbelt/masterbelt/pkg/source/ir"
	protocol "github.com/owenrumney/go-lsp/lsp"
)

// codeActions offers, for each un-annotated constant overlapping the requested
// range whose type the analyzer inferred, a refactor that writes the inferred
// type as an explicit annotation (": <type>" after the name). It is the explicit
// counterpart of the inlay type hint.
func codeActions(doc view, startOff, endOff int) []protocol.CodeAction {
	buf := doc.Buffer()
	trees := positionedTrees(doc.AST().Concrete().Tree())
	kind := protocol.CodeActionRefactorRewrite

	var actions []protocol.CodeAction
	for _, c := range doc.Module().Consts {
		if c.Name == "" || c.Syntax.Type != nil || c.Type == ir.Invalid {
			continue
		}
		declTree, ok := trees[c.Syntax.Syntax()]
		if !ok {
			continue
		}
		// Offer the action only when the request range touches this declaration.
		if declTree.End() < startOff || endOff < declTree.Offset() {
			continue
		}
		nameTok, ok := nameToken(declTree)
		if !ok {
			continue
		}

		at := nameTok.End()
		edit := protocol.TextEdit{Range: toRange(buf, at, at), NewText: ": " + c.Type.String()}
		actionKind := kind
		actions = append(actions, protocol.CodeAction{
			Title: "Add type annotation: " + c.Type.String(),
			Kind:  &actionKind,
			Edit: &protocol.WorkspaceEdit{
				Changes: map[protocol.DocumentURI][]protocol.TextEdit{doc.uri: {edit}},
			},
		})
	}
	return actions
}
