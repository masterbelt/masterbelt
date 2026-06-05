// Package concrete builds the concrete syntax tree (package source/cst) from a
// lexer token stream.
//
// The grammar is small and recursive-descent:
//
//	File          := ( ConstDecl | TypeDecl | UseDecl | AssertDecl | FuncDecl | Error )*
//	ConstDecl     := [pub] const Ident [TypeClause] [Initializer]
//	TypeDecl      := [pub] type Ident [GenericParams] "=" TypeExpr [WhereClause] [ImplBlock]
//	UseDecl       := [pub] use UseTarget from String
//	AssertDecl    := assert Expr
//	FuncDecl      := [pub] fn Ident ParamList ":" TypeExpr ( Block | "->" Expr )
//	UseTarget     := Ident | UseList | "*"
//	UseList       := "{" Ident ( "," Ident )* "}"
//	GenericParams := "<" GenericParam ( "," GenericParam )* ">"
//	GenericParam  := Ident [ ":" TypeExpr ]
//	TypeClause    := ":" TypeExpr
//	Initializer   := "=" Expr
//	WhereClause   := where Expr
//	TypeExpr      := PrimaryType ( "|" PrimaryType )*
//	PrimaryType   := TypeName | RecordType | FuncType
//	TypeName      := ( Ident ["." Ident] [GenericArgs] ) | "self" | "null"
//	GenericArgs   := "<" TypeExpr ( "," TypeExpr )* ">"
//	RecordType    := "{" ( Field [","] )* "}"
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
//	Operand       := Literal | CollectionLit | RecordLit | NameRef | "self" | FuncLit | ParenExpr
//	ParenExpr     := "(" Expr ")"
//	FuncLit       := fn LitParamList [":" TypeExpr] ( "->" Expr | Block )
//	LitParamList  := "(" [ LitParam ( "," LitParam )* ] ")"
//	LitParam      := Ident [":" TypeExpr]
//	CollectionLit := "[" [ Element ( "," Element )* [","] ] "]"
//	Element       := Expr [ ":" Expr ]
//	RecordLit     := [Ident] "{" ( RecordField [","] )* "}"
//	RecordField   := Ident ":" Expr
//	NameRef       := Ident
//	Literal       := Int | String | DatetimeLit | DurationLit | "true" | "false" | "null"
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
//	parser.go       the driver, cursor, file batching, and error recovery
//	parser_decl.go  constant and type declarations (incl. impl blocks, methods)
//	parser_type.go  type expressions (unions, records, generics, func types)
//	parser_expr.go  statements, expressions (precedence climbing), and literals
//	document.go     the incremental Document (reparse on edit)
package concrete

import (
	"github.com/masterbelt/masterbelt/pkg/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/masterbelt/lexer"
	"github.com/masterbelt/masterbelt/pkg/source/cst"
	"github.com/masterbelt/masterbelt/pkg/source/token"
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
	case p.kind() == token.Pub || p.kind() == token.Const || p.kind() == token.Type || p.kind() == token.Use:
		switch p.declKind() {
		case token.Type:
			return []cst.Green{p.parseTypeDecl(lead)}, false
		case token.Use:
			return []cst.Green{p.parseUseDecl(lead)}, false
		case token.Fn:
			// pub fn — declaration intent even when the name is missing; the
			// function parser reports the absent name.
			return []cst.Green{p.parseFuncDecl(lead)}, false
		default:
			return []cst.Green{p.parseConstDecl(lead)}, false
		}
	case p.kind() == token.Fn:
		if p.fnBeginsDecl(p.pos) {
			return []cst.Green{p.parseFuncDecl(lead)}, false
		}
		// A bare fn with no name is a stray function literal, not a declaration.
		return []cst.Green{p.parseError(lead)}, false
	case p.kind() == token.Assert:
		return []cst.Green{p.parseAssertDecl(lead)}, false
	default:
		return []cst.Green{p.parseError(lead)}, false
	}
}

// fnBeginsDecl reports whether the fn keyword at token index i begins a
// function declaration — i.e. an identifier follows it (fn name(...)). A bare
// fn begins a function literal or a function type, never a declaration.
func (p *parser) fnBeginsDecl(i int) bool {
	i++
	for isTrivia(p.toks[i].Kind) {
		i++
	}
	return p.toks[i].Kind == token.Ident
}

// nextSignificantIndex returns the index of the next non-trivia token at or
// after i.
func (p *parser) nextSignificantIndex(i int) int {
	for isTrivia(p.toks[i].Kind) {
		i++
	}
	return i
}

// declKind reports which declaration keyword begins the construct at the cursor
// — Const, Type, or Use — looking past an optional leading pub. For malformed
// input (a lone pub) it returns whatever significant kind follows, and the
// caller falls back to the const parser, which reports the missing keyword.
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

// parseError consumes a run of significant tokens that begin no declaration,
// folding in the interleaving trivia, until the next declaration starter
// (pub/const/type/use/assert, or fn followed by a name) or EOF. The trivia
// that precedes that stopping token is left behind to become the next
// construct's leading trivia. A single diagnostic is reported at the first
// offending token.
func (p *parser) parseError(lead []cst.Green) *cst.Node {
	children := lead
	reported := false
	for {
		switch p.peekSignificant() {
		case token.EOF, token.Pub, token.Const, token.Type, token.Use, token.Assert:
			return cst.NewNode(cst.Error, children)
		case token.Fn:
			// fn stops the error run only as a declaration (fn name); a bare
			// fn is part of the stray expression being skipped.
			if p.fnBeginsDecl(p.nextSignificantIndex(p.pos)) {
				return cst.NewNode(cst.Error, children)
			}
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
