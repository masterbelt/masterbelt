package lsp

import (
	"bytes"

	protocol "github.com/owenrumney/go-lsp/lsp"

	"github.com/masterbelt/masterbelt/pkg/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/source"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// codeActions offers the actions available in the requested range: the explicit
// type-annotation refactor for an un-annotated constant, and a delete quick-fix
// for each lint finding (unused declaration, unreachable code) it overlaps.
func codeActions(doc view, startOff, endOff int) []protocol.CodeAction {
	actions := annotationActions(doc, startOff, endOff)
	return append(actions, deleteActions(doc, startOff, endOff)...)
}

// annotationActions offers, for each un-annotated constant overlapping the
// requested range whose type the analyzer inferred, a refactor that writes the
// inferred type as an explicit annotation (": <type>" after the name). It is
// the explicit counterpart of the inlay type hint.
func annotationActions(doc view, startOff, endOff int) []protocol.CodeAction {
	buf := doc.Buffer()
	trees := doc.Trees()
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

// deleteActions offers a delete quick-fix for each lint finding the requested
// range overlaps. The edit removes the dead declaration or statements whole
// lines and all, so accepting it leaves no blank line behind; a reformat (the
// formatter) settles whatever spacing the removal exposes.
func deleteActions(doc view, startOff, endOff int) []protocol.CodeAction {
	buf := doc.Buffer()
	kind := protocol.CodeActionQuickFix

	var actions []protocol.CodeAction
	for _, d := range doc.Lint() {
		title, ok := deleteTitle(d.Code)
		if !ok || d.End() < startOff || endOff < d.Offset {
			continue
		}
		start, end := lineSpan(buf, d.Offset, d.End())
		actionKind := kind
		actions = append(actions, protocol.CodeAction{
			Title:       title,
			Kind:        &actionKind,
			Diagnostics: []protocol.Diagnostic{toDiagnostic(buf, d)},
			Edit: &protocol.WorkspaceEdit{
				Changes: map[protocol.DocumentURI][]protocol.TextEdit{
					doc.uri: {{Range: toRange(buf, start, end), NewText: ""}},
				},
			},
		})
	}
	return actions
}

// deleteTitle is the quick-fix title for a deletable lint code, or ok=false for
// a diagnostic that deleting does not fix.
func deleteTitle(code diagnostic.Code) (string, bool) {
	switch code {
	case "belt.lint.unused_declaration":
		return "Delete unused declaration", true
	case "belt.lint.unreachable_code":
		return "Delete unreachable code", true
	default:
		return "", false
	}
}

// lineSpan widens [start, end) to whole lines — from the start of start's line
// to the start of the line after end's — so a delete removes the lines entirely
// (indentation and trailing newline included) rather than hollowing them out. A
// declaration's node span can begin at the trivia before it (a leading
// newline), so start advances to its first content byte first; otherwise the
// line above would be swept in.
func lineSpan(buf source.Buffer, start, end int) (int, int) {
	content := buf.Slice(start, end)
	start += len(content) - len(bytes.TrimLeft(content, " \t\r\n"))
	lineStart := 0
	if i := bytes.LastIndexByte(buf.Slice(0, start), '\n'); i >= 0 {
		lineStart = i + 1
	}
	lineEnd := buf.Len()
	if i := bytes.IndexByte(buf.Slice(end, buf.Len()), '\n'); i >= 0 {
		lineEnd = end + i + 1
	}
	return lineStart, lineEnd
}
