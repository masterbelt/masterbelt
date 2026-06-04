package concrete

import (
	"github.com/masterbelt/masterbelt/pkg/masterbelt/source/cst"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/source/token"
)

// parseBlock parses a brace-delimited statement block: "{" Stmt* "}". The cursor
// sits on "{".
func (p *parser) parseBlock() *cst.Node {
	children := []cst.Green{p.bump()} // "{"
	for {
		switch p.peekSignificant() {
		case token.RBrace:
			p.skipTrivia(&children)
			children = append(children, p.bump()) // "}"
			return cst.NewNode(cst.Block, children)
		case token.EOF:
			return cst.NewNode(cst.Block, children)
		default:
			p.skipTrivia(&children)
			children = append(children, p.parseStmt())
		}
	}
}

// parseStmt parses a single statement: a return statement or a bare expression
// statement. The cursor sits on the statement's first significant token.
func (p *parser) parseStmt() cst.Green {
	switch {
	case p.kind() == token.Return:
		return p.parseReturnStmt()
	case startsExpr(p.kind()):
		return p.parseExpr(precLowest)
	default:
		p.report(newUnexpectedTokenDiagnostic(p.cur().Offset, p.cur().Width, p.kind().String()))
		return p.bump()
	}
}

// parseReturnStmt parses "return Expr". The cursor sits on "return".
func (p *parser) parseReturnStmt() *cst.Node {
	children := []cst.Green{p.bump()} // "return"
	if startsExpr(p.peekSignificant()) {
		p.skipTrivia(&children)
		children = append(children, p.parseExpr(precLowest))
	} else {
		p.report(newExpectedExpressionDiagnostic(p.lastStart, 0))
	}
	return cst.NewNode(cst.ReturnStmt, children)
}

// parseExpr parses an expression whose operators bind at least as tightly as
// minPrec, by precedence climbing. The cursor sits on the first significant
// token of the expression. It returns the single green node for the whole
// expression — a Literal/NameRef for an atom, or a BinaryExpr/UnaryExpr tree.
func (p *parser) parseExpr(minPrec int) cst.Green {
	left := p.parseUnary()
	for {
		prec, ok := binaryPrec[p.peekSignificant()]
		if !ok || prec < minPrec {
			return left
		}
		children := []cst.Green{left}
		p.skipTrivia(&children)
		op := p.cur() // the binary operator (cursor is on it)
		children = append(children, p.bump())
		if startsExpr(p.peekSignificant()) {
			p.skipTrivia(&children)
			children = append(children, p.parseExpr(prec+1)) // prec+1 ⇒ left-associative
		} else {
			p.report(newExpectedOperandDiagnostic(p.lastStart, 0, op.Kind.Symbol()))
		}
		left = cst.NewNode(cst.BinaryExpr, children)
	}
}

// parseUnary parses a chain of prefix operators ending in a postfix expression,
// or a bare postfix expression. The cursor sits on the first significant token.
func (p *parser) parseUnary() cst.Green {
	if !unaryOps[p.kind()] {
		return p.parsePostfix()
	}
	op := p.cur() // the unary operator (cursor is on it)
	children := []cst.Green{p.bump()}
	if startsExpr(p.peekSignificant()) {
		p.skipTrivia(&children)
		children = append(children, p.parseUnary())
	} else {
		p.report(newExpectedOperandDiagnostic(p.lastStart, 0, op.Kind.Symbol()))
	}
	return cst.NewNode(cst.UnaryExpr, children)
}

// parsePostfix parses an operand followed by any chain of member accesses and
// calls — receiver.member and callee(args) — applied left to right, so self.id
// and int32(self.id) form left-leaning MemberExpr/CallExpr trees.
func (p *parser) parsePostfix() cst.Green {
	left := p.parseOperand()
	for {
		switch p.peekSignificant() {
		case token.Dot:
			children := []cst.Green{left}
			p.skipTrivia(&children)
			children = append(children, p.bump()) // "."
			if p.peekSignificant() == token.Ident {
				p.skipTrivia(&children)
				children = append(children, p.bump()) // the member name
			} else {
				p.report(newExpectedIdentifierDiagnostic(p.lastStart, 0))
			}
			left = cst.NewNode(cst.MemberExpr, children)
		case token.LParen:
			children := []cst.Green{left}
			p.skipTrivia(&children)
			children = append(children, p.bump()) // "("
			p.parseCallArgs(&children)
			left = cst.NewNode(cst.CallExpr, children)
		default:
			return left
		}
	}
}

// parseCallArgs parses a call's argument list up to and including the closing
// ")", appending the argument expressions and punctuation to *children. The
// cursor sits just past the opening "(".
func (p *parser) parseCallArgs(children *[]cst.Green) {
	if startsExpr(p.peekSignificant()) {
		for {
			p.skipTrivia(children)
			*children = append(*children, p.parseExpr(precLowest))
			if p.peekSignificant() == token.Comma {
				p.skipTrivia(children)
				*children = append(*children, p.bump()) // ","
				continue
			}
			break
		}
	}
	if p.peekSignificant() == token.RParen {
		p.skipTrivia(children)
		*children = append(*children, p.bump()) // ")"
	} else {
		p.report(newUnexpectedTokenDiagnostic(p.cur().Offset, p.cur().Width, p.kind().String()))
	}
}

// parseOperand parses an atom: a literal (integer, string, boolean, or null), a
// collection literal ("[...]"), a NameRef, or the self receiver. The cursor sits
// on the operand token — startsExpr gates every call site, so the default arm is
// defensive and consumes nothing.
func (p *parser) parseOperand() cst.Green {
	switch p.kind() {
	case token.Int, token.String, token.True, token.False, token.Null:
		return cst.NewNode(cst.Literal, []cst.Green{p.bump()})
	case token.LBracket:
		return p.parseCollectionLiteral()
	case token.Ident:
		return cst.NewNode(cst.NameRef, []cst.Green{p.bump()})
	case token.Self:
		return cst.NewNode(cst.SelfExpr, []cst.Green{p.bump()})
	default:
		p.report(newExpectedExpressionDiagnostic(p.lastStart, 0))
		return cst.NewNode(cst.Error, nil)
	}
}

// parseCollectionLiteral parses a list or map literal:
//
//	CollectionLit := "[" [ Element ( "," Element )* [","] ] "]"
//	Element       := Expr [ ":" Expr ]
//
// An element with a ":" is a map entry (key ":" value); without one it is a list
// element. The first element decides which: once a ":" follows it the literal is
// a map, otherwise a list. An empty "[]" is neither — its kind is left to the
// type checker, which resolves it from the annotation. The cursor sits on "[".
func (p *parser) parseCollectionLiteral() *cst.Node {
	children := []cst.Green{p.bump()} // "["
	if !startsExpr(p.peekSignificant()) {
		p.closeBracket(&children)
		return cst.NewNode(cst.CollectionLit, children)
	}

	p.skipTrivia(&children)
	first := p.parseExpr(precLowest)
	if p.peekSignificant() == token.Colon {
		return p.parseMapRest(children, first)
	}
	return p.parseListRest(children, first)
}

// parseListRest parses the remaining list elements after the first, then the
// closing "]". first is the already-parsed first element.
func (p *parser) parseListRest(children []cst.Green, first cst.Green) *cst.Node {
	children = append(children, first)
	for p.peekSignificant() == token.Comma {
		p.skipTrivia(&children)
		children = append(children, p.bump()) // ","
		if !startsExpr(p.peekSignificant()) {
			break // a trailing comma, or recovery
		}
		p.skipTrivia(&children)
		children = append(children, p.parseExpr(precLowest))
	}
	p.closeBracket(&children)
	return cst.NewNode(cst.CollectionLit, children)
}

// parseMapRest parses the remaining map entries after the first key, then the
// closing "]". firstKey is the already-parsed key of the first entry, whose ":"
// the cursor now sits on.
func (p *parser) parseMapRest(children []cst.Green, firstKey cst.Green) *cst.Node {
	children = append(children, p.finishMapEntry(firstKey))
	for p.peekSignificant() == token.Comma {
		p.skipTrivia(&children)
		children = append(children, p.bump()) // ","
		if !startsExpr(p.peekSignificant()) {
			break // a trailing comma, or recovery
		}
		p.skipTrivia(&children)
		children = append(children, p.finishMapEntry(p.parseExpr(precLowest)))
	}
	p.closeBracket(&children)
	return cst.NewNode(cst.CollectionLit, children)
}

// finishMapEntry builds a MapEntry from an already-parsed key: it consumes the
// ":" and parses the value. The cursor sits just past the key — on the ":" for a
// well-formed entry. A missing ":" (a malformed entry, e.g. a bare element in a
// map) is reported, leaving the entry with only its key so recovery is local and
// the closing "]" is not mistaken for the separator.
func (p *parser) finishMapEntry(key cst.Green) *cst.Node {
	entry := []cst.Green{key}
	if p.peekSignificant() != token.Colon {
		p.report(newUnexpectedTokenDiagnostic(p.cur().Offset, p.cur().Width, p.kind().String()))
		return cst.NewNode(cst.MapEntry, entry)
	}
	p.skipTrivia(&entry)
	entry = append(entry, p.bump()) // ":"
	if startsExpr(p.peekSignificant()) {
		p.skipTrivia(&entry)
		entry = append(entry, p.parseExpr(precLowest))
	} else {
		p.report(newExpectedExpressionDiagnostic(p.lastStart, 0))
	}
	return cst.NewNode(cst.MapEntry, entry)
}

// closeBracket consumes the closing "]" of a collection literal, or reports an
// unexpected token when it is absent.
func (p *parser) closeBracket(children *[]cst.Green) {
	if p.peekSignificant() == token.RBracket {
		p.skipTrivia(children)
		*children = append(*children, p.bump()) // "]"
	} else {
		p.report(newUnexpectedTokenDiagnostic(p.cur().Offset, p.cur().Width, p.kind().String()))
	}
}

// precLowest is the minimum binary precedence; parseExpr starts here so it
// accepts every operator.
const precLowest = 1

// binaryPrec maps each binary operator to its precedence — higher binds tighter
// — and is the single source of operator precedence. A token absent from the
// table is not a binary operator, which ends the precedence-climbing loop.
var binaryPrec = map[token.Kind]int{
	token.PipePipe: 1,
	token.AmpAmp:   2,
	token.EqEq:     3,
	token.BangEq:   3,
	token.Lt:       3,
	token.LtEq:     3,
	token.Gt:       3,
	token.GtEq:     3,
	token.Plus:     4,
	token.Minus:    4,
	token.Star:     5,
	token.Slash:    5,
	token.Percent:  5,
}

// unaryOps is the set of prefix operators.
var unaryOps = map[token.Kind]bool{
	token.Plus:  true,
	token.Minus: true,
	token.Bang:  true,
}

// startsExpr reports whether kind can begin an expression. The parser checks it
// before committing to an operand so a missing one is reported (and leaves
// trailing trivia for the next construct) rather than mis-parsed.
func startsExpr(kind token.Kind) bool {
	switch kind {
	case token.Int, token.String, token.Ident, token.True, token.False, token.Null, token.Self,
		token.LBracket, token.Plus, token.Minus, token.Bang:
		return true
	default:
		return false
	}
}
