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
		children = append(children, p.parseExpr())
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

	// A type may carry several impl blocks: an inherent one and one per
	// interface it implements (each tagged with its interface name).
	for p.peekSignificant() == token.Impl {
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
		p.reportUnexpected()
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

	for p.peekSignificant() == token.Impl {
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

// --- interface declarations -------------------------------------------------

// parseInterfaceDecl parses an interface declaration, prepending the
// already-collected leading trivia:
//
//	[pub] interface Name [GenericParams] [":" TypeName ( "," TypeName )*] "{" ( InterfaceMember ( ("," | NL) InterfaceMember )* )? "}"
//
// An interface is a nominal behaviour: its members are required methods (no
// body, which an implementor must supply) and provided methods (with a body,
// the default an implementor gets for free). Members are separated by a comma
// or a newline, exactly as enum members are. An optional colon after the name
// (or generic parameters) introduces the parent interfaces — the supertraits —
// whose whole contract the child inherits. As elsewhere every expected element
// is optional in the parse: a missing one records a diagnostic and is simply
// absent from the tree while losslessness is preserved. The cursor sits on pub
// or interface.
func (p *parser) parseInterfaceDecl(lead []cst.Green) *cst.Node {
	children := lead

	if p.kind() == token.Pub {
		children = append(children, p.bump())
	}
	p.skipTrivia(&children)
	children = append(children, p.bump()) // "interface" (guaranteed by the dispatcher)

	if p.peekSignificant() == token.Ident {
		p.skipTrivia(&children)
		children = append(children, p.bump()) // the declared name
	} else {
		p.report(newExpectedIdentifierDiagnostic(p.lastStart, 0))
	}

	if p.peekSignificant() == token.Lt {
		p.skipTrivia(&children)
		children = append(children, p.parseGenericParams())
	}

	// The optional parent list: ":" TypeName ( "," TypeName )*. The supertraits
	// the child inherits from, gathered into their own node so the AST lowering
	// reads them apart from the members.
	if p.peekSignificant() == token.Colon {
		p.skipTrivia(&children)
		children = append(children, p.parseInterfaceParents())
	}

	if p.peekSignificant() == token.LBrace {
		p.skipTrivia(&children)
		children = append(children, p.bump()) // "{"
	} else {
		p.reportUnexpected()
		return cst.NewNode(cst.InterfaceDecl, children)
	}

	// The members. A comma between two members is optional (a newline serves
	// as well); a member begins with pub or an identifier (its name).
	for {
		switch {
		case p.peekSignificant() == token.RBrace:
			p.skipTrivia(&children)
			children = append(children, p.bump()) // "}"
			return cst.NewNode(cst.InterfaceDecl, children)
		case p.peekSignificant() == token.EOF:
			return cst.NewNode(cst.InterfaceDecl, children)
		case p.peekSignificant() == token.Pub || p.peekSignificant() == token.Ident:
			var lead []cst.Green
			p.skipTrivia(&lead)
			children = append(children, p.parseInterfaceMember(lead))
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
}

// parseInterfaceMember parses one member of an interface, prepending the
// already-collected leading trivia:
//
//	[pub] Name [GenericParams] ParamList ":" TypeExpr [Block]
//
// A member without a body is a required method (the implementor supplies it); a
// member with a body Block is a provided method (the default). The result type
// is required, mirroring a top-level function. The optional GenericParams give
// a member its own type variables (the A in fold<A>). The cursor sits on pub or
// the member's name.
func (p *parser) parseInterfaceMember(lead []cst.Green) *cst.Node {
	children := lead
	if p.kind() == token.Pub {
		children = append(children, p.bump())
	}
	if p.peekSignificant() == token.Ident {
		p.skipTrivia(&children)
		children = append(children, p.bump()) // the member name
	} else {
		p.report(newExpectedIdentifierDiagnostic(p.lastStart, 0))
	}
	if p.peekSignificant() == token.Lt {
		p.skipTrivia(&children)
		children = append(children, p.parseGenericParams())
	}
	if p.peekSignificant() == token.LParen {
		p.skipTrivia(&children)
		children = append(children, p.parseParamList(true))
	} else {
		p.reportUnexpected()
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
	return cst.NewNode(cst.InterfaceMember, children)
}

// parseInterfaceParents parses the parent-interface list of an interface
// declaration: ":" TypeName ( "," TypeName )*. Each parent is a primary type
// (a named interface, possibly applied — foldable<nint, T>), never a union, so
// it is parsed the way the impl tag's interface name is. As elsewhere a missing
// parent records a diagnostic and is simply absent. The cursor sits on the ":".
func (p *parser) parseInterfaceParents() *cst.Node {
	children := []cst.Green{p.bump()} // ":"
	if startsType(p.peekSignificant()) {
		for {
			p.skipTrivia(&children)
			children = append(children, p.parsePrimaryType())
			if p.peekSignificant() == token.Comma {
				p.skipTrivia(&children)
				children = append(children, p.bump()) // ","
				continue
			}
			break
		}
	} else {
		p.report(newExpectedTypeDiagnostic(p.lastStart, 0))
	}
	return cst.NewNode(cst.InterfaceParents, children)
}

// parseWhereClause parses the refinement predicate of a type declaration:
// where Expr. The cursor sits on "where".
func (p *parser) parseWhereClause() *cst.Node {
	children := []cst.Green{p.bump()} // "where"
	if startsExpr(p.peekSignificant()) {
		p.skipTrivia(&children)
		children = append(children, p.parseExpr())
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
			if p.peekSignificant() != token.Comma {
				break
			}
			p.skipTrivia(&children)
			children = append(children, p.bump()) // ","
			if p.peekSignificant() == token.Ident {
				continue
			}
			if p.peekSignificant() != token.RBrace {
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
		p.reportUnexpected()
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
		children = append(children, p.parseExpr())
	} else {
		p.report(newExpectedExpressionDiagnostic(p.lastStart, 0))
	}

	return cst.NewNode(cst.AssertDecl, children)
}

// --- master declarations ------------------------------------------------------

// parseMasterDecl parses a master declaration, prepending the already-collected
// leading trivia:
//
//	[pub] master Ident "{" ( MasterRecord | MasterPrimary )* "}"
//
// master/record/primary are context keywords — ordinary identifiers the lexer
// leaves plain, recognized only at these positions (the get/set/static
// precedent) and wrapped in a MasterKeyword node so the lowering ignores them
// and the editor colours them. The record member reuses the type-body grammar
// (a type expression with an optional where-refinement and impl blocks), so
// per-row methods and predicates come along for free; the primary member names
// the key column(s). As elsewhere every expected element is optional in the
// parse: a missing one records a diagnostic and is simply absent from the tree
// while losslessness is preserved. The cursor sits on pub or the master keyword.
func (p *parser) parseMasterDecl(lead []cst.Green) *cst.Node {
	children := lead

	if p.kind() == token.Pub {
		children = append(children, p.bump())
	}
	p.skipTrivia(&children)
	children = append(children, p.masterKeyword()) // "master" (guaranteed by the dispatcher)

	if p.peekSignificant() == token.Ident {
		p.skipTrivia(&children)
		children = append(children, p.bump()) // the declared name
	} else {
		p.report(newExpectedIdentifierDiagnostic(p.lastStart, 0))
	}

	if p.peekSignificant() == token.LBrace {
		p.skipTrivia(&children)
		children = append(children, p.bump()) // "{"
	} else {
		p.reportUnexpected()
		return cst.NewNode(cst.MasterDecl, children)
	}

	// The members: a record member and a primary member, each introduced by its
	// context keyword. They are newline-separated like the fields they hold, and
	// either may be absent (a missing one is the semantic layer's concern, not
	// the parser's). The leading trivia of a member (its doc comment most of all)
	// belongs to it, exactly as a top-level declaration's does.
	for {
		switch {
		case p.peekSignificant() == token.RBrace:
			p.skipTrivia(&children)
			children = append(children, p.bump()) // "}"
			return cst.NewNode(cst.MasterDecl, children)
		case p.peekSignificant() == token.EOF:
			return cst.NewNode(cst.MasterDecl, children)
		case p.masterMemberIs("record"):
			var lead []cst.Green
			p.skipTrivia(&lead)
			children = append(children, p.parseMasterRecord(lead))
		case p.masterMemberIs("primary"):
			var lead []cst.Green
			p.skipTrivia(&lead)
			children = append(children, p.parseMasterPrimary(lead))
		default:
			p.skipTrivia(&children)
			p.report(newUnexpectedTokenDiagnostic(p.cur().Offset, p.cur().Width, p.kind().String()))
			children = append(children, p.bump())
		}
	}
}

// parseMasterRecord parses the record member of a master declaration, prepending
// the already-collected leading trivia:
//
//	record TypeExpr [WhereClause] [ImplBlock]*
//
// The body after the record keyword is the type-body grammar a type declaration
// uses, reused wholesale (parseTypeExpr / parseWhereClause / parseImplBlock): a
// type expression — a RecordType in practice — an optional where-refinement over
// self, and any number of impl blocks (an inherent one and one per interface).
// So a master's rows get per-row methods and refinements with no bespoke grammar.
// The cursor sits on the record keyword.
func (p *parser) parseMasterRecord(lead []cst.Green) *cst.Node {
	children := lead
	children = append(children, p.masterKeyword()) // "record"

	// The row type follows, parsed as a full type expression. record/primary are
	// context keywords only at a member's head: in type position they are
	// ordinary identifiers, so a row type may be spelled "primary" (record
	// primary) just as any other name. A row type written as a member keyword
	// therefore parses as that type; the parser does not try to guess that the
	// keyword was meant to open the next member instead.
	if startsType(p.peekSignificant()) {
		p.skipTrivia(&children)
		children = append(children, p.parseTypeExpr())
	} else {
		p.report(newExpectedTypeDiagnostic(p.lastStart, 0))
	}

	if p.peekSignificant() == token.Where {
		p.skipTrivia(&children)
		children = append(children, p.parseWhereClause())
	}

	for p.peekSignificant() == token.Impl {
		p.skipTrivia(&children)
		children = append(children, p.parseImplBlock())
	}

	return cst.NewNode(cst.MasterRecord, children)
}

// parseMasterPrimary parses the primary-key member of a master declaration,
// prepending the already-collected leading trivia:
//
//	primary ( Ident | "(" Ident ( "," Ident )* [","] ")" )
//
// The key is a single column name or a parenthesized, comma-separated list of
// them — a composite key, in declaration order. The key columns are kept as
// direct Ident children (the primary keyword is wrapped in a MasterKeyword
// node), so the lowering reads them apart from the keyword. The cursor sits on
// the primary keyword.
func (p *parser) parseMasterPrimary(lead []cst.Green) *cst.Node {
	children := lead
	children = append(children, p.masterKeyword()) // "primary"

	switch p.peekSignificant() {
	case token.Ident:
		// The single key column. record/primary in key position are ordinary
		// identifiers (a column may be named "primary"), not the next member.
		p.skipTrivia(&children)
		children = append(children, p.bump()) // the single key column
	case token.LParen:
		p.skipTrivia(&children)
		children = append(children, p.bump()) // "("
		// A composite key names at least one column: an empty "()" records the
		// same missing-identifier diagnostic a bare primary without a key does,
		// rather than lowering silently to no key.
		if p.peekSignificant() != token.Ident {
			p.report(newExpectedIdentifierDiagnostic(p.lastStart, 0))
		}
		for p.peekSignificant() == token.Ident {
			p.skipTrivia(&children)
			children = append(children, p.bump()) // a key column
			if p.peekSignificant() != token.Comma {
				break
			}
			p.skipTrivia(&children)
			children = append(children, p.bump()) // ","
		}
		if p.peekSignificant() == token.RParen {
			p.skipTrivia(&children)
			children = append(children, p.bump()) // ")"
		} else {
			p.reportUnexpected()
		}
	default:
		p.report(newExpectedIdentifierDiagnostic(p.lastStart, 0))
	}

	return cst.NewNode(cst.MasterPrimary, children)
}

// masterKeyword consumes the context-keyword identifier at the cursor
// (master/record/primary) and wraps it in a MasterKeyword node, so the AST
// lowering reads it as the construct's keyword rather than as a name, and the
// editor's token classifier colours it as a keyword — the same role the
// Modifier node plays for the get/set/static accessors. The cursor sits on the
// keyword, guaranteed by the dispatcher (master) or masterMemberIs (record,
// primary).
func (p *parser) masterKeyword() cst.Green {
	return cst.NewNode(cst.MasterKeyword, []cst.Green{p.bump()})
}

// masterMemberIs reports whether the next significant token is the context
// keyword kw (record or primary) opening a master member. The lookahead reads
// the identifier's text the same way the get/set/static modifier check does;
// reading bytes a token already covers keeps the boundary context-free property
// the incremental Document relies on.
func (p *parser) masterMemberIs(kw string) bool {
	i := p.nextSignificantIndex(p.pos)
	return p.toks[i].Kind == token.Ident && p.identText(i) == kw
}

// --- top-level functions ------------------------------------------------------

// parseFuncDecl parses a top-level function declaration, prepending the
// already-collected leading trivia:
//
//	[pub] [extern] fn Effect* Ident [GenericParams] ParamList ":" TypeExpr ( Block | "->" Expr )
//
// The optional GenericParams give the function its own type variables, each
// with an optional interface bound (the T in fn f<T: foldable<int>>(...)) — the
// same "<...>" list a type declaration and an interface member carry.
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
	children = p.parseFuncSignature(children)
	children = p.parseFuncBody(children, extern)
	return cst.NewNode(cst.FuncDecl, children)
}

// parseFuncSignature parses the function header after "fn": the effect keywords,
// the declared name, the optional generic parameters, the parameter list, and the
// required result type. It appends each element to children and returns the
// extended slice; a missing element records a diagnostic and is simply absent.
func (p *parser) parseFuncSignature(children []cst.Green) []cst.Green {
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
	if p.peekSignificant() == token.Lt {
		p.skipTrivia(&children)
		children = append(children, p.parseGenericParams())
	}
	if p.peekSignificant() == token.LParen {
		p.skipTrivia(&children)
		children = append(children, p.parseParamList(true))
	} else {
		p.report(newExpectedParamListDiagnostic(p.lastStart, 0))
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
	return children
}

// parseFuncBody parses the function's body — a "->" expression (an implicit
// return), a statement block, or nothing for an extern. It appends to children
// and returns the extended slice; a missing non-extern body records a diagnostic.
func (p *parser) parseFuncBody(children []cst.Green, extern bool) []cst.Green {
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
			children = append(children, p.parseExpr())
		default:
			p.report(newExpectedExpressionDiagnostic(p.lastStart, 0))
		}
	case token.LBrace:
		p.skipTrivia(&children)
		children = append(children, p.parseBlock())
	default:
		// An extern function has no body: its implementation is the target's.
		// The body is missing: anchor at the last consumed token rather than the
		// token that should have opened it, which — when this declaration is
		// otherwise empty-bodied at end of input or before the next declaration —
		// is the start of the following File child (see reportUnexpected).
		if !extern {
			p.report(newExpectedFuncBodyDiagnostic(p.lastStart, 0))
		}
	}
	return children
}

// --- implementations and method bodies --------------------------------------

// parseImplBlock parses an implementation block: impl [TypeName] "{" (MethodDecl |
// ConstDecl)* "}". An item beginning with const — optionally after pub — is an
// associated constant (TypeName.Name); anything else that begins a method is a
// method. The optional TypeName after impl tags the interface this block
// implements (impl foldable<int> { ... }); without it the block is an inherent
// impl. The cursor sits on "impl".
func (p *parser) parseImplBlock() *cst.Node {
	children := []cst.Green{p.bump()} // "impl"
	// An interface-tagged impl names the interface before the brace
	// (impl foldable<int> { ... }); a bare impl goes straight to "{".
	if p.peekSignificant() == token.Ident {
		p.skipTrivia(&children)
		children = append(children, p.parsePrimaryType())
	}
	if p.peekSignificant() == token.LBrace {
		p.skipTrivia(&children)
		children = append(children, p.bump()) // "{"
	} else {
		p.reportUnexpected()
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
		case p.implItemBeginsConst():
			// An associated constant. Its leading trivia (its doc comment most of
			// all) belongs to it, exactly as a method's or a top-level decl's does.
			var lead []cst.Green
			p.skipTrivia(&lead)
			children = append(children, p.parseImplConst(lead))
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

// implItemBeginsConst reports whether the impl item at the cursor is an
// associated constant — const, optionally after a leading pub — rather than a
// method. The lookahead past pub mirrors declKind's at the file level.
func (p *parser) implItemBeginsConst() bool {
	i := p.nextSignificantIndex(p.pos)
	if p.toks[i].Kind == token.Pub {
		i = p.nextSignificantIndex(i + 1)
	}
	return p.toks[i].Kind == token.Const
}

// parseImplConst parses an associated constant inside an impl block, prepending
// the already-collected leading trivia:
//
//	[doc] [pub] const Name [TypeClause] "=" ( Expr | "builtin" )
//
// It reuses the ConstDecl node a top-level constant uses, so the AST lowering
// is shared. The one extra form is "= builtin", which marks a constant whose
// value comes from the builtin registry (the integer bounds int8.Max/Min),
// mirroring a primitive type's "= builtin" body. The cursor sits on pub or
// const.
func (p *parser) parseImplConst(lead []cst.Green) *cst.Node {
	children := lead

	if p.kind() == token.Pub {
		children = append(children, p.bump())
	}
	p.skipTrivia(&children)
	children = append(children, p.bump()) // "const" (guaranteed by the dispatcher)

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
		children = append(children, p.parseImplConstInitializer())
	} else {
		p.report(newExpectedAssignDiagnostic(p.lastStart, 0))
	}

	return cst.NewNode(cst.ConstDecl, children)
}

// parseImplConstInitializer parses an associated constant's "= Expr" or
// "= builtin". The cursor sits on the equals sign. A "builtin" body is the
// marker for a registry-supplied value (the integer bounds); any other form is
// an ordinary expression, exactly as a top-level constant's initializer.
func (p *parser) parseImplConstInitializer() *cst.Node {
	children := []cst.Green{p.bump()} // "="
	switch {
	case p.peekSignificant() == token.Builtin:
		p.skipTrivia(&children)
		children = append(children, p.parseBuiltinType())
	case startsExpr(p.peekSignificant()):
		p.skipTrivia(&children)
		children = append(children, p.parseExpr())
	default:
		p.report(newExpectedExpressionDiagnostic(p.lastStart, 0))
	}
	return cst.NewNode(cst.Initializer, children)
}

// parseMethodDecl parses a method inside an impl block, prepending the
// already-collected leading trivia:
//
//	[doc] [pub] ( Modifier | [extern] [StaticModifier | fn] ) Effect* Ident [GenericParams] ParamList ":" TypeExpr [Block]
//	Modifier := "get" | "set" | StaticModifier
//	StaticModifier := "static" fn
//
// The accessor/static modifiers are context keywords (the lexer leaves them as
// identifiers): a get/set is a modifier only when an identifier — the property
// name — follows it on the same line, so the prelude's get(index)/set(k, v)
// methods stay ordinary; a static is a modifier when fn follows (a missing fn
// is reported with expected_fn and recovered). The accessors exclude extern
// and the bare fn before them (the grammar has no `extern get` or `get fn`);
// extern composes only with static — `extern static fn`, the builtin surface's
// spelling for a static native the registry supplies.
//
// fn is optional on an instance method (some methods omit it), the effect list
// (io, async, nondet) sits before the name, the optional GenericParams declare
// the method's own type variables (the A in fold<A>), and the body Block is
// absent for an extern method. The cursor sits on the first of
// pub/get/set/static/extern/fn/an effect/Ident.
func (p *parser) parseMethodDecl(lead []cst.Green) *cst.Node {
	children := lead
	if p.kind() == token.Pub {
		children = append(children, p.bump())
	}
	if p.methodModifier(&children) {
		// A get/set modifier goes straight to the property name; a static
		// modifier was followed by fn (consumed inside methodModifier), so an
		// effect list may follow. Either way extern and the leading bare fn are
		// excluded, so fall through to the effect list and name.
		return p.finishMethodDecl(children)
	}
	if p.peekSignificant() == token.Extern {
		p.skipTrivia(&children)
		children = append(children, p.bump())
		// extern static fn: the static modifier (and its fn) may follow an
		// extern. The accessors may not — extern get/set stays unparsed, the
		// get/set reading as the method name it would otherwise be.
		if p.staticModifier(&children) {
			return p.finishMethodDecl(children)
		}
	}
	if p.peekSignificant() == token.Fn {
		p.skipTrivia(&children)
		children = append(children, p.bump())
	}
	return p.finishMethodDecl(children)
}

// methodModifier recognizes an accessor or static modifier at the cursor and, if
// it finds one, wraps its context-keyword identifier in a Modifier node appended
// to children (consuming the following fn keyword for a static modifier) and
// reports true. It returns false when the cursor begins an ordinary instance
// method — including a method literally named get/set/static, distinguished by
// what follows the identifier on the same line. The lookahead never crosses a
// newline: impl members are newline-separated, so a get at a line's end is the
// (mis-spelled) name of its own member, not a modifier over the next line's.
func (p *parser) methodModifier(children *[]cst.Green) bool {
	i := p.nextSignificantIndex(p.pos)
	if p.toks[i].Kind != token.Ident {
		return false
	}
	switch p.identText(i) {
	case "get", "set":
		// A modifier only when an identifier (the property name) follows on the
		// same line; otherwise get/set is the method's own name (the prelude's
		// get(i)/set(k, v) take this path, their "(" not an Ident).
		if p.nextOnLine(i+1) != token.Ident {
			return false
		}
		p.skipTrivia(children)
		*children = append(*children, p.modifier())
		return true
	case "static":
		return p.staticModifier(children)
	default:
		return false
	}
}

// staticModifier recognizes the static modifier at the cursor — the context
// keyword static followed by fn — wrapping it in a Modifier node appended to
// children (with the fn consumed) and reporting true. A static not followed by
// fn on its line is an ordinary method named static (false), except `static
// name(...)`, which reads as the modifier with the fn reported missing
// (expected_fn) and recovered. It is the static arm of methodModifier, split
// out because extern composes with it (`extern static fn`) and not with the
// accessors.
func (p *parser) staticModifier(children *[]cst.Green) bool {
	i := p.nextSignificantIndex(p.pos)
	if p.toks[i].Kind != token.Ident || p.identText(i) != "static" {
		return false
	}
	switch p.nextOnLine(i + 1) {
	case token.Fn:
		p.skipTrivia(children)
		*children = append(*children, p.modifier())
		p.skipTrivia(children)
		*children = append(*children, p.bump()) // "fn"
		return true
	case token.Ident:
		// `static name(...)` with the fn forgotten: read static as the
		// modifier and recover, rather than misreading it as a method named
		// static and burying the real signature under cascading errors.
		p.skipTrivia(children)
		*children = append(*children, p.modifier())
		p.report(newExpectedFnDiagnostic(p.lastStart, 0))
		return true
	default:
		// `static(...)` or `static:` — a method literally named static.
		return false
	}
}

// modifier consumes the context-keyword identifier at the cursor and wraps it in
// a Modifier node, so the AST lowering and the editor's token classifier read
// the accessor/static marker from one node rather than re-deriving it.
func (p *parser) modifier() cst.Green {
	return cst.NewNode(cst.Modifier, []cst.Green{p.bump()})
}

// nextOnLine returns the kind of the next significant token at or after index i,
// or token.Newline if a newline is reached first. It is the modifier lookahead's
// same-line peek: a context keyword binds only to what follows it on its own
// line, so a newline (an impl member separator) ends the window.
func (p *parser) nextOnLine(i int) token.Kind {
	for {
		k := p.toks[i].Kind
		if k == token.Newline || !isTrivia(k) {
			return k
		}
		i++
	}
}

// finishMethodDecl parses the remainder of a method declaration after its
// modifiers — the effect list, name, optional generic parameters, parameter
// list, result type, and body — and assembles the MethodDecl node.
func (p *parser) finishMethodDecl(children []cst.Green) *cst.Node {
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
	// Optional method type parameters: the A in fold<A>(...). They join the
	// signature's scope alongside the implicit free-variable resolution.
	if p.peekSignificant() == token.Lt {
		p.skipTrivia(&children)
		children = append(children, p.parseGenericParams())
	}
	if p.peekSignificant() == token.LParen {
		p.skipTrivia(&children)
		children = append(children, p.parseParamList(true))
	} else {
		p.reportUnexpected()
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
	if nameLike(p.peekSignificant()) {
		for {
			p.skipTrivia(&children)
			children = append(children, p.parseParam(requireType))
			if p.peekSignificant() != token.Comma {
				break
			}
			p.skipTrivia(&children)
			children = append(children, p.bump()) // ","
			// A comma promises another parameter unless it is a trailing one
			// before ")"; without the name check a truncated list ("fn(x,")
			// would bump EOF as the next name and run the cursor off the slice.
			// A keyword reads as a parameter name here (fn f(for: int)).
			if nameLike(p.peekSignificant()) {
				continue
			}
			if p.peekSignificant() != token.RParen {
				p.report(newExpectedIdentifierDiagnostic(p.lastStart, 0))
			}
			break
		}
	}
	if p.peekSignificant() == token.RParen {
		p.skipTrivia(&children)
		children = append(children, p.bump()) // ")"
	} else {
		p.reportUnexpected()
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
