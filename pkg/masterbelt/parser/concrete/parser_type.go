package concrete

import (
	"github.com/masterbelt/masterbelt/pkg/source/cst"
	"github.com/masterbelt/masterbelt/pkg/source/token"
)

// parseTypeExpr parses a type expression: a union of primary types,
// "A | B | ...". A lone primary type is returned directly; only an actual "|"
// produces a UnionType node, mirroring how parseExpr only builds a BinaryExpr
// when an operator is present. The cursor sits on the first type token.
func (p *parser) parseTypeExpr() cst.Green {
	left := p.parsePrimaryType()
	if p.peekSignificant() != token.Pipe {
		return left
	}
	children := []cst.Green{left}
	for p.peekSignificant() == token.Pipe {
		p.skipTrivia(&children)
		children = append(children, p.bump()) // "|"
		if startsType(p.peekSignificant()) {
			p.skipTrivia(&children)
			children = append(children, p.parsePrimaryType())
		} else {
			p.report(newExpectedTypeDiagnostic(p.lastStart, 0))
		}
	}
	return cst.NewNode(cst.UnionType, children)
}

// parsePrimaryType parses a single, non-union type: a named type (with optional
// generic arguments), the self or null type, a record type, or a function type.
// The cursor sits on the type's first token.
func (p *parser) parsePrimaryType() cst.Green {
	switch p.kind() {
	case token.Ident:
		children := []cst.Green{p.bump()} // the type name
		if p.peekSignificant() == token.Lt {
			p.skipTrivia(&children)
			children = append(children, p.parseGenericArgs())
		}
		return cst.NewNode(cst.TypeName, children)
	case token.Self, token.Null:
		return cst.NewNode(cst.TypeName, []cst.Green{p.bump()})
	case token.LBrace:
		return p.parseRecordType()
	case token.Fn:
		return p.parseFuncType()
	case token.Builtin:
		return p.parseBuiltinType()
	default:
		p.report(newExpectedTypeDiagnostic(p.lastStart, 0))
		return cst.NewNode(cst.Error, nil)
	}
}

// parseBuiltinType parses a builtin type body: builtin [GenericArgs]. The cursor
// sits on "builtin".
func (p *parser) parseBuiltinType() *cst.Node {
	children := []cst.Green{p.bump()} // "builtin"
	if p.peekSignificant() == token.Lt {
		p.skipTrivia(&children)
		children = append(children, p.parseGenericArgs())
	}
	return cst.NewNode(cst.BuiltinType, children)
}

// parseGenericArgs parses a "<...>" type-argument list on the application side:
// "<" TypeExpr ( "," TypeExpr )* ">". The cursor sits on "<".
func (p *parser) parseGenericArgs() *cst.Node {
	children := []cst.Green{p.bump()} // "<"
	if startsType(p.peekSignificant()) {
		for {
			p.skipTrivia(&children)
			children = append(children, p.parseTypeExpr())
			if p.peekSignificant() == token.Comma {
				p.skipTrivia(&children)
				children = append(children, p.bump()) // ","
				continue
			}
			break
		}
	}
	if p.peekSignificant() == token.Gt {
		p.skipTrivia(&children)
		children = append(children, p.bump()) // ">"
	} else {
		p.report(newExpectedTypeDiagnostic(p.lastStart, 0))
	}
	return cst.NewNode(cst.GenericArgs, children)
}

// parseRecordType parses an anonymous product type, "{" Field* "}", with fields
// separated by newlines (trivia). The cursor sits on "{".
func (p *parser) parseRecordType() *cst.Node {
	children := []cst.Green{p.bump()} // "{"
	for {
		switch p.peekSignificant() {
		case token.RBrace:
			p.skipTrivia(&children)
			children = append(children, p.bump()) // "}"
			return cst.NewNode(cst.RecordType, children)
		case token.EOF:
			return cst.NewNode(cst.RecordType, children) // unterminated; the leaves are still lossless
		case token.Ident:
			p.skipTrivia(&children)
			children = append(children, p.parseField())
		default:
			p.skipTrivia(&children)
			p.report(newUnexpectedTokenDiagnostic(p.cur().Offset, p.cur().Width, p.kind().String()))
			children = append(children, p.bump())
		}
	}
}

// parseField parses one record field: Ident ":" TypeExpr.
func (p *parser) parseField() *cst.Node {
	children := []cst.Green{p.bump()} // the field name
	if p.peekSignificant() == token.Colon {
		p.skipTrivia(&children)
		children = append(children, p.bump()) // ":"
		if startsType(p.peekSignificant()) {
			p.skipTrivia(&children)
			children = append(children, p.parseTypeExpr())
		} else {
			p.report(newExpectedTypeDiagnostic(p.lastStart, 0))
		}
	} else {
		p.report(newExpectedTypeDiagnostic(p.lastStart, 0))
	}
	return cst.NewNode(cst.Field, children)
}

// parseFuncType parses a function type: fn ParamList ":" TypeExpr. The cursor
// sits on "fn".
func (p *parser) parseFuncType() *cst.Node {
	children := []cst.Green{p.bump()} // "fn"
	if p.peekSignificant() == token.LParen {
		p.skipTrivia(&children)
		children = append(children, p.parseParamList(true))
	} else {
		p.report(newUnexpectedTokenDiagnostic(p.cur().Offset, p.cur().Width, p.kind().String()))
	}
	if p.peekSignificant() == token.Colon {
		p.skipTrivia(&children)
		children = append(children, p.bump()) // ":"
		if startsType(p.peekSignificant()) {
			p.skipTrivia(&children)
			children = append(children, p.parseTypeExpr())
		} else {
			p.report(newExpectedTypeDiagnostic(p.lastStart, 0))
		}
	} else {
		p.report(newExpectedTypeDiagnostic(p.lastStart, 0))
	}
	return cst.NewNode(cst.FuncType, children)
}

// startsType reports whether kind can begin a type expression.
func startsType(kind token.Kind) bool {
	switch kind {
	case token.Ident, token.Self, token.Null, token.LBrace, token.Fn, token.Builtin:
		return true
	default:
		return false
	}
}
