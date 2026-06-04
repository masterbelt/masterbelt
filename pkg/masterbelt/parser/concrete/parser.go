// Package concrete builds the concrete syntax tree (package source/cst) from a
// lexer token stream.
//
// The grammar is small and recursive-descent:
//
//	File          := ( ConstDecl | TypeDecl | Error )*
//	ConstDecl     := [pub] const Ident [TypeClause] [Initializer]
//	TypeDecl      := [pub] type Ident [GenericParams] "=" TypeExpr [ImplBlock]
//	GenericParams := "<" GenericParam ( "," GenericParam )* ">"
//	GenericParam  := Ident [ ":" TypeExpr ]
//	TypeClause    := ":" TypeRef
//	Initializer   := "=" Expr
//	TypeExpr      := PrimaryType ( "|" PrimaryType )*
//	PrimaryType   := TypeName | RecordType | FuncType
//	TypeName      := ( Ident [GenericArgs] ) | "self" | "null"
//	GenericArgs   := "<" TypeExpr ( "," TypeExpr )* ">"
//	RecordType    := "{" Field* "}"
//	Field         := Ident ":" TypeExpr
//	FuncType      := fn ParamList ":" TypeExpr
//	ImplBlock     := impl "{" MethodDecl* "}"
//	MethodDecl    := [pub] [extern] [fn] Ident ParamList ":" TypeExpr [Block]
//	ParamList     := "(" [ Param ( "," Param )* ] ")"
//	Param         := Ident ":" TypeExpr
//	Block         := "{" Stmt* "}"
//	Stmt          := ReturnStmt | Expr
//	ReturnStmt    := return Expr
//	Expr          := OrExpr
//	OrExpr        := AndExpr ( "||" AndExpr )*
//	AndExpr       := CmpExpr ( "&&" CmpExpr )*
//	CmpExpr       := AddExpr ( ( "==" | "!=" | "<" | "<=" | ">" | ">=" ) AddExpr )*
//	AddExpr       := MulExpr ( ( "+" | "-" ) MulExpr )*
//	MulExpr       := Unary ( ( "*" | "/" | "%" ) Unary )*
//	Unary         := ( "+" | "-" | "!" ) Unary | Postfix
//	Postfix       := Operand ( "." Ident | "(" [ Expr ( "," Expr )* ] ")" )*
//	Operand       := Literal | NameRef | "self"
//	TypeRef       := Ident
//	NameRef       := Ident
//	Literal       := Int | "true" | "false" | "null"
//
// The binary levels are parsed by precedence climbing (parseExpr) rather than a
// function per level; the binaryPrec table is the single source of operator
// precedence. Comparisons are left-associative here (Go forbids chaining them),
// which keeps the parser uniform and defers "bool < int" to the type checker.
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
	case p.kind() == token.Pub || p.kind() == token.Const || p.kind() == token.Type:
		if p.declKind() == token.Type {
			return []cst.Green{p.parseTypeDecl(lead)}, false
		}
		return []cst.Green{p.parseConstDecl(lead)}, false
	default:
		return []cst.Green{p.parseError(lead)}, false
	}
}

// declKind reports which declaration keyword begins the construct at the cursor
// — Const or Type — looking past an optional leading pub. For malformed input
// (a lone pub) it returns whatever significant kind follows, and the caller
// falls back to the const parser, which reports the missing keyword.
func (p *parser) declKind() token.Kind {
	i := p.pos
	for isTrivia(p.toks[i].Kind) {
		i++
	}
	if p.toks[i].Kind == token.Pub {
		i++
		for isTrivia(p.toks[i].Kind) {
			i++
		}
	}
	return p.toks[i].Kind
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

// --- type expressions -------------------------------------------------------

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
		children = append(children, p.parseParamList())
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
		children = append(children, p.parseParamList())
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
// "(" [ Param ( "," Param )* ] ")". The cursor sits on "(".
func (p *parser) parseParamList() *cst.Node {
	children := []cst.Green{p.bump()} // "("
	if p.peekSignificant() == token.Ident {
		for {
			p.skipTrivia(&children)
			children = append(children, p.parseParam())
			if p.peekSignificant() == token.Comma {
				p.skipTrivia(&children)
				children = append(children, p.bump()) // ","
				continue
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

// parseParam parses one parameter: Ident ":" TypeExpr.
func (p *parser) parseParam() *cst.Node {
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
	} else {
		p.report(newExpectedTypeDiagnostic(p.lastStart, 0))
	}
	return cst.NewNode(cst.Param, children)
}

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

// parseOperand parses an atom: a literal (integer, boolean, or null), a NameRef,
// or the self receiver. The cursor sits on the operand token — startsExpr gates
// every call site, so the default arm is defensive and consumes nothing.
func (p *parser) parseOperand() cst.Green {
	switch p.kind() {
	case token.Int, token.True, token.False, token.Null:
		return cst.NewNode(cst.Literal, []cst.Green{p.bump()})
	case token.Ident:
		return cst.NewNode(cst.NameRef, []cst.Green{p.bump()})
	case token.Self:
		return cst.NewNode(cst.SelfExpr, []cst.Green{p.bump()})
	default:
		p.report(newExpectedExpressionDiagnostic(p.lastStart, 0))
		return cst.NewNode(cst.Error, nil)
	}
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
		case token.EOF, token.Pub, token.Const, token.Type:
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
	case token.Int, token.Ident, token.True, token.False, token.Null, token.Self,
		token.Plus, token.Minus, token.Bang:
		return true
	default:
		return false
	}
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
