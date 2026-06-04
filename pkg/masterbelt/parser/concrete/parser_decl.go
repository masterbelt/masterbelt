package concrete

import (
	"github.com/masterbelt/masterbelt/pkg/source/cst"
	"github.com/masterbelt/masterbelt/pkg/source/token"
)

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

// parseTypeClause parses ": Type", where Type is a full type expression (the
// same grammar a type declaration uses), so a constant may be annotated with a
// generic type like list<int>. The cursor sits on the colon.
func (p *parser) parseTypeClause() *cst.Node {
	children := []cst.Green{p.bump()} // ":"
	if startsType(p.peekSignificant()) {
		p.skipTrivia(&children)
		children = append(children, p.parseTypeExpr())
	} else {
		p.report(newExpectedTypeDiagnostic(p.lastStart, 0))
	}
	return cst.NewNode(cst.TypeClause, children)
}

// parseInitializer parses "= Expr". The cursor sits on the equals sign.
func (p *parser) parseInitializer() *cst.Node {
	children := []cst.Green{p.bump()} // "="
	if startsExpr(p.peekSignificant()) {
		p.skipTrivia(&children)
		children = append(children, p.parseExpr(precLowest))
	} else {
		p.report(newExpectedExpressionDiagnostic(p.lastStart, 0))
	}
	return cst.NewNode(cst.Initializer, children)
}

// --- type declarations ------------------------------------------------------

// parseTypeDecl parses a type declaration, prepending the already-collected
// leading trivia:
//
//	[pub] type Name [GenericParams] "=" TypeExpr [ImplBlock]
//
// As in parseConstDecl every expected element is optional in the parse, so a
// malformed declaration records a diagnostic and is simply absent from the tree
// while the surrounding structure (and losslessness) is preserved.
func (p *parser) parseTypeDecl(lead []cst.Green) *cst.Node {
	children := lead

	if p.kind() == token.Pub {
		children = append(children, p.bump())
	}
	p.skipTrivia(&children)
	children = append(children, p.bump()) // "type" (guaranteed by the dispatcher)

	// The declared name is an identifier, or the null keyword — null is a
	// builtin type and may be declared (type null = builtin).
	if k := p.peekSignificant(); k == token.Ident || k == token.Null {
		p.skipTrivia(&children)
		children = append(children, p.bump()) // the declared name
	} else {
		p.report(newExpectedIdentifierDiagnostic(p.lastStart, 0))
	}

	if p.peekSignificant() == token.Lt {
		p.skipTrivia(&children)
		children = append(children, p.parseGenericParams())
	}

	if p.peekSignificant() == token.Assign {
		p.skipTrivia(&children)
		children = append(children, p.bump()) // "="
		if startsType(p.peekSignificant()) {
			p.skipTrivia(&children)
			children = append(children, p.parseTypeExpr())
		} else {
			p.report(newExpectedTypeDiagnostic(p.lastStart, 0))
		}
	} else {
		p.report(newExpectedAssignDiagnostic(p.lastStart, 0))
	}

	if p.peekSignificant() == token.Impl {
		p.skipTrivia(&children)
		children = append(children, p.parseImplBlock())
	}

	return cst.NewNode(cst.TypeDecl, children)
}

// parseGenericParams parses a "<...>" type-parameter list on the declaration
// side: "<" GenericParam ( "," GenericParam )* ">". The cursor sits on "<".
func (p *parser) parseGenericParams() *cst.Node {
	children := []cst.Green{p.bump()} // "<"
	for p.peekSignificant() == token.Ident {
		p.skipTrivia(&children)
		children = append(children, p.parseGenericParam())
		if p.peekSignificant() == token.Comma {
			p.skipTrivia(&children)
			children = append(children, p.bump()) // ","
			continue
		}
		break
	}
	if p.peekSignificant() == token.Gt {
		p.skipTrivia(&children)
		children = append(children, p.bump()) // ">"
	} else {
		p.report(newExpectedTypeDiagnostic(p.lastStart, 0))
	}
	return cst.NewNode(cst.GenericParams, children)
}

// parseGenericParam parses one type parameter: Ident [ ":" TypeExpr ], where the
// optional ":" introduces a constraint (which may itself be a union).
func (p *parser) parseGenericParam() *cst.Node {
	children := []cst.Green{p.bump()} // the parameter name
	if p.peekSignificant() == token.Colon {
		p.skipTrivia(&children)
		children = append(children, p.bump()) // ":"
		if startsType(p.peekSignificant()) {
			p.skipTrivia(&children)
			children = append(children, p.parseTypeExpr())
		} else {
			p.report(newExpectedTypeDiagnostic(p.lastStart, 0))
		}
	}
	return cst.NewNode(cst.GenericParam, children)
}

// --- implementations and method bodies --------------------------------------

// parseImplBlock parses an implementation block: impl "{" MethodDecl* "}". The
// cursor sits on "impl".
func (p *parser) parseImplBlock() *cst.Node {
	children := []cst.Green{p.bump()} // "impl"
	if p.peekSignificant() == token.LBrace {
		p.skipTrivia(&children)
		children = append(children, p.bump()) // "{"
	} else {
		p.report(newUnexpectedTokenDiagnostic(p.cur().Offset, p.cur().Width, p.kind().String()))
		return cst.NewNode(cst.ImplBlock, children)
	}
	for {
		switch {
		case p.peekSignificant() == token.RBrace:
			p.skipTrivia(&children)
			children = append(children, p.bump()) // "}"
			return cst.NewNode(cst.ImplBlock, children)
		case p.peekSignificant() == token.EOF:
			return cst.NewNode(cst.ImplBlock, children)
		case startsMethod(p.peekSignificant()):
			p.skipTrivia(&children)
			children = append(children, p.parseMethodDecl())
		default:
			p.skipTrivia(&children)
			p.report(newUnexpectedTokenDiagnostic(p.cur().Offset, p.cur().Width, p.kind().String()))
			children = append(children, p.bump())
		}
	}
}

// parseMethodDecl parses a method inside an impl block:
//
//	[pub] [extern] [fn] Ident ParamList ":" TypeExpr [Block]
//
// fn is optional (some methods omit it) and the body Block is absent for an
// extern method. The cursor sits on the first of pub/extern/fn/Ident.
func (p *parser) parseMethodDecl() *cst.Node {
	var children []cst.Green
	if p.kind() == token.Pub {
		children = append(children, p.bump())
	}
	if p.peekSignificant() == token.Extern {
		p.skipTrivia(&children)
		children = append(children, p.bump())
	}
	if p.peekSignificant() == token.Fn {
		p.skipTrivia(&children)
		children = append(children, p.bump())
	}
	if p.peekSignificant() == token.Ident {
		p.skipTrivia(&children)
		children = append(children, p.bump()) // the method name
	} else {
		p.report(newExpectedIdentifierDiagnostic(p.lastStart, 0))
	}
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
	if p.peekSignificant() == token.LBrace {
		p.skipTrivia(&children)
		children = append(children, p.parseBlock())
	}
	return cst.NewNode(cst.MethodDecl, children)
}

// parseParamList parses a parenthesized parameter list:
// "(" [ Param ( "," Param )* ] ")". requireType says whether each parameter
// must carry a ":" TypeExpr annotation — true for method declarations and
// function types (their signatures are the source of types), false for
// function literals (the annotation may be inferred from context). The cursor
// sits on "(".
func (p *parser) parseParamList(requireType bool) *cst.Node {
	children := []cst.Green{p.bump()} // "("
	if p.peekSignificant() == token.Ident {
		for {
			p.skipTrivia(&children)
			children = append(children, p.parseParam(requireType))
			if p.peekSignificant() == token.Comma {
				p.skipTrivia(&children)
				children = append(children, p.bump()) // ","
				// A comma promises another parameter; without the check a
				// truncated list ("fn(x,") would bump EOF as the next name and
				// run the cursor off the token slice.
				if p.peekSignificant() == token.Ident {
					continue
				}
				p.report(newExpectedIdentifierDiagnostic(p.lastStart, 0))
			}
			break
		}
	}
	if p.peekSignificant() == token.RParen {
		p.skipTrivia(&children)
		children = append(children, p.bump()) // ")"
	} else {
		p.report(newUnexpectedTokenDiagnostic(p.cur().Offset, p.cur().Width, p.kind().String()))
	}
	return cst.NewNode(cst.ParamList, children)
}

// parseParam parses one parameter: Ident [":" TypeExpr]. When requireType is
// true a missing annotation is reported; a ":" always promises a type, so a
// dangling colon is reported either way.
func (p *parser) parseParam(requireType bool) *cst.Node {
	children := []cst.Green{p.bump()} // the parameter name
	if p.peekSignificant() == token.Colon {
		p.skipTrivia(&children)
		children = append(children, p.bump()) // ":"
		if startsType(p.peekSignificant()) {
			p.skipTrivia(&children)
			children = append(children, p.parseTypeExpr())
		} else {
			p.report(newExpectedTypeDiagnostic(p.lastStart, 0))
		}
	} else if requireType {
		p.report(newExpectedTypeDiagnostic(p.lastStart, 0))
	}
	return cst.NewNode(cst.Param, children)
}

// startsMethod reports whether kind can begin a method declaration inside an
// impl block.
func startsMethod(kind token.Kind) bool {
	switch kind {
	case token.Pub, token.Extern, token.Fn, token.Ident:
		return true
	default:
		return false
	}
}
