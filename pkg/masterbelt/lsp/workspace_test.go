package lsp

import (
	"github.com/masterbelt/masterbelt/pkg/masterbelt/parser/abstract"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/semantic"
	protocol "github.com/owenrumney/go-lsp/lsp"
)

// testView analyzes src as a standalone single-file program — the way the
// server treats a file outside any project — for driving the feature
// implementations directly.
func testView(src string) view {
	ws := &workspace{
		prog: semantic.NewProgram(),
		open: map[semantic.FileID]protocol.DocumentURI{},
	}
	id := semantic.FileID("test.belt")
	ws.prog.SetFile(id, abstract.NewDocument([]byte(src)), nil)
	ws.prog.Refresh()

	uri := protocol.DocumentURI("file:///test.belt")
	ws.open[id] = uri
	return view{ws: ws, id: id, uri: uri}
}
