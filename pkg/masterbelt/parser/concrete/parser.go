// Package concrete builds the concrete syntax tree (package source/cst) from a
// lexer token stream.
//
// The grammar is small and recursive-descent:
//
//	File        := ( ConstDecl | Error )*
//	ConstDecl   := [pub] const Ident [TypeClause] [Initializer]
//	TypeClause  := ":" TypeRef
//	Initializer := "=" ( Literal | NameRef )
//	TypeRef     := Ident
//	NameRef     := Ident
//	Literal     := Int
//
// Two properties make the parser usable as the front half of an incremental
// pipeline (see Document):
//
//   - Lossless. Trivia (whitespace, newlines, comments) is never discarded; it
//     is attached as leading children of the construct that follows it, so an
//     in-order walk of the tree's leaves reproduces the source exactly.
//
//   - Boundary context-free. A File child is parsed in a state that depends only
//     on the tokens it covers, never on the children before it. So parsing can
//     restart at any File-child boundary and, over identical bytes, produce
//     identical children — which is what lets Document reuse the unedited
//     declarations on either side of a change.
//
// The package is organised as:
//
//	parser.go    the recursive-descent parser over a token slice
//	document.go  the incremental Document (reparse on edit)
package concrete

import (
	"github.com/masterbelt/masterbelt/pkg/masterbelt/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/lexer"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/source/cst"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/source/token"
)

// Parse lexes and parses src into a File node and the parse diagnostics. It is
// the one-shot entry point; use Document for incremental editing. Lexer-phase
// diagnostics are reported separately by the lexer and are not returned here.
func Parse(src []byte) (*cst.Node, []diagnostic.Diagnostic) {
	return parseTokens(lexer.NewDocument(src).Tokens())
}

// parseTokens parses toks — a lexer token stream terminated by a single EOF
// token — into a File node and the diagnostics found.
func parseTokens(toks []token.Token) (*cst.Node, []diagnostic.Diagnostic) {
	p := newParser(toks)
	root := p.parseFile()
	return root, p.diags.Items()
}

// parser is a recursive-descent parser over a fixed token slice. It carries no
// state beyond the cursor and the diagnostic sink, so a parse can be started at
// any token index (Document does this to reparse a window).
type parser struct {
	toks  []token.Token
	pos   int
	diags *diagnostic.List

	// lastStart is the start offset of the most recently consumed token. A
	// "missing element" diagnostic is anchored here rather than at the cursor:
	// the cursor sits at the next construct's boundary, and a zero-width
	// diagnostic there would be ambiguous to attribute when an edit reuses the
	// declarations on either side. Anchoring to a token already inside the
	// current declaration keeps every diagnostic strictly within the construct
	// that produced it, which is what makes the incremental diagnostic splice
	// (see Document) clean.
	lastStart int
}

func newParser(toks []token.Token) *parser {
	return &parser{toks: toks, diags: &diagnostic.List{}}
}

// kind reports the kind of the token at the cursor. The slice always ends with
// EOF, so the cursor never runs off the end.
func (p *parser) kind() token.Kind { return p.toks[p.pos].Kind }

// cur returns the token at the cursor.
func (p *parser) cur() token.Token { return p.toks[p.pos] }

// atEOF reports whether the cursor sits on the terminating EOF token.
func (p *parser) atEOF() bool { return p.kind() == token.EOF }

// bump consumes the token at the cursor and returns it as a green leaf.
func (p *parser) bump() cst.Green {
	t := p.toks[p.pos]
	p.lastStart = t.Offset
	p.pos++
	return cst.NewToken(t.Kind, t.Width)
}

// skipTrivia consumes a run of trivia tokens, appending each as a leaf to
// *children. The cursor stops on the next significant token (or EOF).
func (p *parser) skipTrivia(children *[]cst.Green) {
	for isTrivia(p.kind()) {
		*children = append(*children, p.bump())
	}
}

// peekSignificant returns the kind of the next non-trivia token without moving
// the cursor. The parser looks past trivia before committing to consume it, so
// that trailing trivia is left for the following construct rather than being
// swallowed by an absent optional element.
func (p *parser) peekSignificant() token.Kind {
	for i := p.pos; ; i++ {
		if !isTrivia(p.toks[i].Kind) {
			return p.toks[i].Kind
		}
	}
}

// parseFile parses the whole token slice into a File node.
func (p *parser) parseFile() *cst.Node {
	var children []cst.Green
	for {
		batch, done := p.nextChildren()
		children = append(children, batch...)
		if done {
			break
		}
	}
	return cst.NewNode(cst.File, children)
}

// nextChildren parses the next File-level batch at the cursor and reports
// whether it was the final one. A batch is:
//
//   - a single ConstDecl node (when pub/const follows the leading trivia),
//   - a single Error node (when some other significant token follows), or
//   - the file's trailing trivia followed by the EOF leaf (done == true).
//
// Leading trivia is folded into the declaration or error node it precedes, so a
// non-final batch is always exactly one node; only the final EOF batch carries
// loose trivia leaves. Document relies on this to check for realignment after
// each non-final batch.
func (p *parser) nextChildren() (batch []cst.Green, done bool) {
	var lead []cst.Green
	p.skipTrivia(&lead)

	switch {
	case p.atEOF():
		lead = append(lead, p.bump()) // the EOF leaf
		return lead, true
	case p.kind() == token.Pub || p.kind() == token.Const:
		return []cst.Green{p.parseConstDecl(lead)}, false
	default:
		return []cst.Green{p.parseError(lead)}, false
	}
}

// parseConstDecl parses a constant declaration, prepending the already-collected
// leading trivia. Every expected element is optional in the parse (a missing one
// records a diagnostic and is simply absent from the tree) so that recovery is
// local and the tree stays lossless on malformed input.
func (p *parser) parseConstDecl(lead []cst.Green) *cst.Node {
	children := lead

	if p.kind() == token.Pub {
		children = append(children, p.bump())
	}

	if p.peekSignificant() == token.Const {
		p.skipTrivia(&children)
		children = append(children, p.bump())
	} else {
		p.report(newExpectedConstDiagnostic(p.lastStart, 0))
	}

	if p.peekSignificant() == token.Ident {
		p.skipTrivia(&children)
		children = append(children, p.bump()) // the declared name
	} else {
		p.report(newExpectedIdentifierDiagnostic(p.lastStart, 0))
	}

	if p.peekSignificant() == token.Colon {
		p.skipTrivia(&children)
		children = append(children, p.parseTypeClause())
	}

	if p.peekSignificant() == token.Assign {
		p.skipTrivia(&children)
		children = append(children, p.parseInitializer())
	} else {
		p.report(newExpectedAssignDiagnostic(p.lastStart, 0))
	}

	return cst.NewNode(cst.ConstDecl, children)
}

// parseTypeClause parses ": Type". The cursor sits on the colon.
func (p *parser) parseTypeClause() *cst.Node {
	children := []cst.Green{p.bump()} // ":"
	if p.peekSignificant() == token.Ident {
		p.skipTrivia(&children)
		children = append(children, cst.NewNode(cst.TypeRef, []cst.Green{p.bump()}))
	} else {
		p.report(newExpectedTypeDiagnostic(p.lastStart, 0))
	}
	return cst.NewNode(cst.TypeClause, children)
}

// parseInitializer parses "= Expr". The cursor sits on the equals sign.
func (p *parser) parseInitializer() *cst.Node {
	children := []cst.Green{p.bump()} // "="
	switch p.peekSignificant() {
	case token.Int:
		p.skipTrivia(&children)
		children = append(children, cst.NewNode(cst.Literal, []cst.Green{p.bump()}))
	case token.Ident:
		p.skipTrivia(&children)
		children = append(children, cst.NewNode(cst.NameRef, []cst.Green{p.bump()}))
	default:
		p.report(newExpectedExpressionDiagnostic(p.lastStart, 0))
	}
	return cst.NewNode(cst.Initializer, children)
}

// parseError consumes a run of significant tokens that begin no declaration,
// folding in the interleaving trivia, until the next declaration starter
// (pub/const) or EOF. The trivia that precedes that stopping token is left
// behind to become the next construct's leading trivia. A single diagnostic is
// reported at the first offending token.
func (p *parser) parseError(lead []cst.Green) *cst.Node {
	children := lead
	reported := false
	for {
		switch p.peekSignificant() {
		case token.EOF, token.Pub, token.Const:
			return cst.NewNode(cst.Error, children)
		}
		p.skipTrivia(&children)
		if !reported {
			p.report(newUnexpectedTokenDiagnostic(p.cur().Offset, p.cur().Width, p.kind().String()))
			reported = true
		}
		children = append(children, p.bump())
	}
}

// report records a parse diagnostic.
func (p *parser) report(d diagnostic.Diagnostic) {
	p.diags.Add(d)
}

// isTrivia reports whether k is a trivia kind — one the grammar skips over but
// the tree still retains for losslessness.
func isTrivia(k token.Kind) bool {
	switch k {
	case token.Whitespace, token.Newline, token.LineComment, token.DocComment, token.BlockComment:
		return true
	default:
		return false
	}
}
