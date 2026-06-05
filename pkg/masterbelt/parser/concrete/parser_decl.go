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
//	[pub] type Name [GenericParams] "=" TypeExpr [WhereClause] [ImplBlock]
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

	if p.peekSignificant() == token.Where {
		p.skipTrivia(&children)
		children = append(children, p.parseWhereClause())
	}

	if p.peekSignificant() == token.Impl {
		p.skipTrivia(&children)
		children = append(children, p.parseImplBlock())
	}

	return cst.NewNode(cst.TypeDecl, children)
}

// --- enum declarations ------------------------------------------------------

// parseEnumDecl parses an enum declaration, prepending the already-collected
// leading trivia:
//
//	[pub] enum Name [":" TypeExpr] "{" ( EnumMember ( ("," | NL) EnumMember )* )? "}" [ImplBlock]
//
// The optional ": TypeExpr" names the base type (a plain type reference; the
// allowed set — the integer family and string — is checked in semantic). The
// members are separated by a comma or a newline (newlines are trivia, so a
// member simply follows another), each an identifier with an optional
// "= ConstExpr" initializer. An enum may carry a trailing impl block, exactly
// as a nominal type can. As elsewhere every expected element is optional in the
// parse: a missing one records a diagnostic and is simply absent from the tree
// while losslessness is preserved. The cursor sits on pub or enum.
func (p *parser) parseEnumDecl(lead []cst.Green) *cst.Node {
	children := lead

	if p.kind() == token.Pub {
		children = append(children, p.bump())
	}
	p.skipTrivia(&children)
	children = append(children, p.bump()) // "enum" (guaranteed by the dispatcher)

	if p.peekSignificant() == token.Ident {
		p.skipTrivia(&children)
		children = append(children, p.bump()) // the declared name
	} else {
		p.report(newExpectedIdentifierDiagnostic(p.lastStart, 0))
	}

	// The optional base-type annotation, written exactly like a const's: a
	// ":" introducing a full type expression.
	if p.peekSignificant() == token.Colon {
		p.skipTrivia(&children)
		children = append(children, p.parseTypeClause())
	}

	if p.peekSignificant() == token.LBrace {
		p.skipTrivia(&children)
		children = append(children, p.bump()) // "{"
	} else {
		p.report(newUnexpectedTokenDiagnostic(p.cur().Offset, p.cur().Width, p.kind().String()))
		return cst.NewNode(cst.EnumDecl, children)
	}

	// The members. A comma between two members is optional (a newline serves
	// as well), and a trailing comma is tolerated — the loop re-checks for an
	// identifier rather than promising one after a comma.
	for {
		switch p.peekSignificant() {
		case token.RBrace:
			p.skipTrivia(&children)
			children = append(children, p.bump()) // "}"
			goto afterMembers
		case token.EOF:
			goto afterMembers
		case token.Ident:
			var lead []cst.Green
			p.skipTrivia(&lead)
			children = append(children, p.parseEnumMember(lead))
			if p.peekSignificant() == token.Comma {
				p.skipTrivia(&children)
				children = append(children, p.bump()) // ","
			}
		default:
			p.skipTrivia(&children)
			p.report(newUnexpectedTokenDiagnostic(p.cur().Offset, p.cur().Width, p.kind().String()))
			children = append(children, p.bump())
		}
	}
afterMembers:

	if p.peekSignificant() == token.Impl {
		p.skipTrivia(&children)
		children = append(children, p.parseImplBlock())
	}

	return cst.NewNode(cst.EnumDecl, children)
}

// parseEnumMember parses one enum member, prepending the already-collected
// leading trivia: Ident [Initializer]. The optional "= ConstExpr" gives the
// member its value (the same Initializer node a const uses). The cursor sits on
// the member's identifier.
func (p *parser) parseEnumMember(lead []cst.Green) *cst.Node {
	children := lead
	children = append(children, p.bump()) // the member name (guaranteed an Ident)
	if p.peekSignificant() == token.Assign {
		p.skipTrivia(&children)
		children = append(children, p.parseInitializer())
	}
	return cst.NewNode(cst.EnumMember, children)
}

// parseWhereClause parses the refinement predicate of a type declaration:
// where Expr. The cursor sits on "where".
func (p *parser) parseWhereClause() *cst.Node {
	children := []cst.Green{p.bump()} // "where"
	if startsExpr(p.peekSignificant()) {
		p.skipTrivia(&children)
		children = append(children, p.parseExpr(precLowest))
	} else {
		p.report(newExpectedExpressionDiagnostic(p.lastStart, 0))
	}
	return cst.NewNode(cst.WhereClause, children)
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

// --- use declarations ---------------------------------------------------------

// parseUseDecl parses a cross-file import, prepending the already-collected
// leading trivia:
//
//	[pub] use ( Ident | UseList | "*" ) from String
//
// The target is a namespace name, a selective-import list, or the wildcard "*".
// As elsewhere every expected element is optional in the parse: a malformed
// declaration records a diagnostic and the element is simply absent from the
// tree, keeping recovery local and the tree lossless.
func (p *parser) parseUseDecl(lead []cst.Green) *cst.Node {
	children := lead

	if p.kind() == token.Pub {
		children = append(children, p.bump())
	}
	p.skipTrivia(&children)
	children = append(children, p.bump()) // "use" (guaranteed by the dispatcher)

	switch p.peekSignificant() {
	case token.Ident: // namespace import: use geo from "..."
		p.skipTrivia(&children)
		children = append(children, p.bump())
	case token.LBrace: // selective import: use { a, b } from "..."
		p.skipTrivia(&children)
		children = append(children, p.parseUseList())
	case token.Star: // wildcard import: use * from "..."
		p.skipTrivia(&children)
		children = append(children, p.bump())
	default:
		p.report(newExpectedIdentifierDiagnostic(p.lastStart, 0))
	}

	if p.peekSignificant() == token.From {
		p.skipTrivia(&children)
		children = append(children, p.bump()) // "from"
	} else {
		p.report(newExpectedFromDiagnostic(p.lastStart, 0))
	}

	if p.peekSignificant() == token.String {
		p.skipTrivia(&children)
		children = append(children, p.bump()) // the source path
	} else {
		p.report(newExpectedPathDiagnostic(p.lastStart, 0))
	}

	return cst.NewNode(cst.UseDecl, children)
}

// parseUseList parses the selective-import list: "{" Ident ("," Ident)* "}".
// The cursor sits on "{". An empty list is reported — importing nothing has no
// purpose — and, as in parseParamList, a comma promises another name.
func (p *parser) parseUseList() *cst.Node {
	children := []cst.Green{p.bump()} // "{"
	if p.peekSignificant() == token.Ident {
		for {
			p.skipTrivia(&children)
			children = append(children, p.bump()) // an imported name
			if p.peekSignificant() == token.Comma {
				p.skipTrivia(&children)
				children = append(children, p.bump()) // ","
				if p.peekSignificant() == token.Ident {
					continue
				}
				p.report(newExpectedIdentifierDiagnostic(p.lastStart, 0))
			}
			break
		}
	} else {
		p.report(newExpectedIdentifierDiagnostic(p.lastStart, 0))
	}
	if p.peekSignificant() == token.RBrace {
		p.skipTrivia(&children)
		children = append(children, p.bump()) // "}"
	} else {
		p.report(newUnexpectedTokenDiagnostic(p.cur().Offset, p.cur().Width, p.kind().String()))
	}
	return cst.NewNode(cst.UseList, children)
}

// --- assert declarations ------------------------------------------------------

// parseAssertDecl parses a compile-time assertion, prepending the already-
// collected leading trivia:
//
//	assert Expr
//
// An assertion has no name and no visibility (pub does not apply). As elsewhere
// the expression is optional in the parse: a missing one records a diagnostic
// and is simply absent from the tree.
func (p *parser) parseAssertDecl(lead []cst.Green) *cst.Node {
	children := lead
	children = append(children, p.bump()) // "assert" (guaranteed by the dispatcher)

	if startsExpr(p.peekSignificant()) {
		p.skipTrivia(&children)
		children = append(children, p.parseExpr(precLowest))
	} else {
		p.report(newExpectedExpressionDiagnostic(p.lastStart, 0))
	}

	return cst.NewNode(cst.AssertDecl, children)
}

// --- top-level functions ------------------------------------------------------

// parseFuncDecl parses a top-level function declaration, prepending the
// already-collected leading trivia:
//
//	[pub] [extern] fn Effect* Ident ParamList ":" TypeExpr ( Block | "->" Expr )
//
// A function is a method without a receiver: the same header (the result type
// is required at the top level), with the function literal's two body forms —
// a statement block, or "->" followed by a single expression (an implicit
// return). The effect list (io, async, nondet) sits between fn and the name.
// An extern function declares a native a target supplies — the root of an
// effect — and carries no body. An arrow followed by "{" is rejected with a
// pointer to drop the arrow, exactly as in a function literal. As elsewhere
// every expected element is optional in the parse: a missing one records a
// diagnostic and is simply absent from the tree. The cursor sits on pub,
// extern, or fn.
func (p *parser) parseFuncDecl(lead []cst.Green) *cst.Node {
	children := lead
	if p.kind() == token.Pub {
		children = append(children, p.bump())
	}
	extern := false
	if p.peekSignificant() == token.Extern {
		p.skipTrivia(&children)
		children = append(children, p.bump())
		extern = true
	}
	p.skipTrivia(&children)
	children = append(children, p.bump()) // "fn" (guaranteed by the dispatcher)
	for p.peekSignificant().Effect() {
		p.skipTrivia(&children)
		children = append(children, p.bump()) // an effect keyword
	}
	if p.peekSignificant() == token.Ident {
		p.skipTrivia(&children)
		children = append(children, p.bump()) // the declared name
	} else {
		p.report(newExpectedIdentifierDiagnostic(p.lastStart, 0))
	}
	if p.peekSignificant() == token.LParen {
		p.skipTrivia(&children)
		children = append(children, p.parseParamList(true))
	} else {
		p.report(newExpectedParamListDiagnostic(p.cur().Offset, p.cur().Width))
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
	switch p.peekSignificant() {
	case token.Arrow:
		p.skipTrivia(&children)
		children = append(children, p.bump()) // "->"
		switch {
		case p.peekSignificant() == token.LBrace:
			// "-> { ... }": an arrow body must be an expression. Report it,
			// then parse the block anyway so recovery stays local.
			p.skipTrivia(&children)
			p.report(newArrowBlockBodyDiagnostic(p.cur().Offset, p.cur().Width))
			children = append(children, p.parseBlock())
		case startsExpr(p.peekSignificant()):
			p.skipTrivia(&children)
			children = append(children, p.parseExpr(precLowest))
		default:
			p.report(newExpectedExpressionDiagnostic(p.lastStart, 0))
		}
	case token.LBrace:
		p.skipTrivia(&children)
		children = append(children, p.parseBlock())
	default:
		// An extern function has no body: its implementation is the target's.
		if !extern {
			p.report(newExpectedFuncBodyDiagnostic(p.cur().Offset, p.cur().Width))
		}
	}
	return cst.NewNode(cst.FuncDecl, children)
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
			// The leading trivia belongs to the method (its doc comment most
			// of all), exactly as a top-level declaration's does.
			var lead []cst.Green
			p.skipTrivia(&lead)
			children = append(children, p.parseMethodDecl(lead))
		default:
			p.skipTrivia(&children)
			p.report(newUnexpectedTokenDiagnostic(p.cur().Offset, p.cur().Width, p.kind().String()))
			children = append(children, p.bump())
		}
	}
}

// parseMethodDecl parses a method inside an impl block, prepending the
// already-collected leading trivia:
//
//	[doc] [pub] [extern] [fn] Effect* Ident ParamList ":" TypeExpr [Block]
//
// fn is optional (some methods omit it), the effect list (io, async, nondet)
// sits before the name, and the body Block is absent for an extern method.
// The cursor sits on the first of pub/extern/fn/an effect/Ident.
func (p *parser) parseMethodDecl(lead []cst.Green) *cst.Node {
	children := lead
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
	for p.peekSignificant().Effect() {
		p.skipTrivia(&children)
		children = append(children, p.bump()) // an effect keyword
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
		return kind.Effect() // a method may begin with its effect list
	}
}
