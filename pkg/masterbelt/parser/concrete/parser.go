// Package concrete builds the concrete syntax tree (package source/cst) from a
// lexer token stream.
//
// The grammar is small and recursive-descent:
//
//	File          := ( ConstDecl | TypeDecl | EnumDecl | InterfaceDecl | UseDecl | AssertDecl | FuncDecl | Error )*
//	ConstDecl     := [pub] const Ident [TypeClause] [Initializer]
//	TypeDecl      := [pub] type Ident [GenericParams] "=" TypeExpr [WhereClause] [ImplBlock]
//	EnumDecl      := [pub] enum Ident [":" TypeExpr] "{" ( EnumMember ( ("," | NL) EnumMember )* )? "}" [ImplBlock]
//	EnumMember    := Ident [Initializer]
//	InterfaceDecl := [pub] interface Ident [GenericParams] "{" ( InterfaceMember ( ("," | NL) InterfaceMember )* )? "}"
//	InterfaceMember := [pub] Ident [GenericParams] ParamList ":" TypeExpr [Block]
//	UseDecl       := [pub] use UseTarget from String
//	AssertDecl    := assert Expr
//	FuncDecl      := [pub] [extern] fn Effect* Ident ParamList ":" TypeExpr ( Block | "->" Expr )
//	Effect        := io | async | nondet
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
//	ImplBlock     := impl [TypeName] "{" ( MethodDecl | AssocConst )* "}"
//	AssocConst    := [pub] const Ident [TypeClause] "=" ( Expr | "builtin" )
//	MethodDecl    := [pub] [extern] [fn] Effect* Ident [GenericParams] ParamList ":" TypeExpr [Block]
//	ParamList     := "(" [ Param ( "," Param )* ] ")"
//	Param         := Ident ":" TypeExpr
//	Block         := "{" Stmt* "}"
//	Stmt          := LetStmt | ReturnStmt | SwitchStmt | MatchStmt | IfStmt | ForStmt | AssignStmt | Expr
//	LetStmt       := let Ident [TypeClause] "=" Expr
//	AssignStmt    := Expr "=" Expr
//	ReturnStmt    := return Expr
//	SwitchStmt    := switch Expr "{" ( SwitchArm ( ("," | NL) SwitchArm )* )? "}"
//	SwitchArm     := ( Expr ( "," Expr )* | "_" ) "->" ( Stmt | Block )
//	MatchStmt     := match Expr "{" ( MatchArm ( ("," | NL) MatchArm )* )? "}"
//	MatchArm      := MatchPattern "->" ( Stmt | Block )
//	MatchPattern  := ( PrimaryType [Ident] ) | "_"
//	IfStmt        := if Expr Block [ else ( IfStmt | Block ) ]
//	ForStmt       := for Ident ( "of" | "in" ) Expr Block
//	Expr          := TernaryExpr
//	TernaryExpr   := OrExpr [ "?" Expr ":" Expr ]
//	OrExpr        := AndExpr ( "||" AndExpr )*
//	AndExpr       := CmpExpr ( "&&" CmpExpr )*
//	CmpExpr       := AddExpr ( ( "==" | "!=" | "<" | "<=" | ">" | ">=" ) AddExpr )*
//	AddExpr       := MulExpr ( ( "+" | "-" ) MulExpr )*
//	MulExpr       := Unary ( ( "*" | "/" | "%" ) Unary )*
//	Unary         := ( "+" | "-" | "!" ) Unary | AwaitExpr | Postfix
//	AwaitExpr     := await Unary
//	Postfix       := Operand ( "." Ident | "(" [ Expr ( "," Expr )* ] ")" | "[" Expr "]" )*
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
// The ternary "?:" is the one operator looser than them all and the only
// right-associative one: parseExpr reads it after the binary climb, and only at
// the outermost (lowest-precedence) call, so a > b ? a : b groups as
// (a > b) ? a : b and a ? b : c ? d : e as a ? b : (c ? d : e).
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

	// noRecordLit suppresses the "Ident {" / "{" record-literal reading of an
	// operand, so the "{" that opens a switch's arm block or an if's then-block is
	// not mistaken for a record literal on the scrutinee or condition (the same
	// restriction Rust/Go put on the head expression of an if/switch). It is set
	// while parsing that head expression and holds across all of its operands, so
	// the "{" after a binary condition (if x < lo { ... }) is recognized too. A
	// bracketed context — parens, call arguments, a collection, a record's field
	// values — clears it for its inner expressions through bracketed, since a "{"
	// is unambiguous again inside brackets, and restores it on the way out.
	noRecordLit bool

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

// bracketed runs fn — the parse of a bracketed context's inner content (a
// parenthesized group, a collection, a call's arguments, a record's field
// values) — with the noRecordLit restriction lifted, then restores it. Inside
// brackets a "{" can only be a record literal, never a control block, so the
// restriction that an if condition or a switch scrutinee carries does not reach
// the nested expressions; restoring it keeps the restriction in force for the
// postfix continuation back at the head-expression level.
func (p *parser) bracketed(fn func()) {
	saved := p.noRecordLit
	p.noRecordLit = false
	fn()
	p.noRecordLit = saved
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
		if batchWidth(batch) == 0 {
			// Progress guard: a zero-width non-final batch (a recovery bug in
			// some declaration parser — see beginsDeclaration) must not spin
			// this loop. Take one token as a raw leaf so the parse advances;
			// Document.Edit applies the same guard, so the incremental tree
			// stays identical to a full parse.
			children = append(children, p.bump())
		}
	}
	return cst.NewNode(cst.File, children)
}

// batchWidth sums the widths of a nextChildren batch.
func batchWidth(batch []cst.Green) int {
	w := 0
	for _, g := range batch {
		w += g.Width()
	}
	return w
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
	case p.kind() == token.Pub || p.kind() == token.Const || p.kind() == token.Type || p.kind() == token.Enum || p.kind() == token.Interface || p.kind() == token.Use || p.kind() == token.Extern:
		switch p.declKind() {
		case token.Type:
			return []cst.Green{p.parseTypeDecl(lead)}, false
		case token.Enum:
			return []cst.Green{p.parseEnumDecl(lead)}, false
		case token.Interface:
			return []cst.Green{p.parseInterfaceDecl(lead)}, false
		case token.Use:
			return []cst.Green{p.parseUseDecl(lead)}, false
		case token.Fn:
			// pub fn — declaration intent even when the name is missing; the
			// function parser reports the absent name.
			return []cst.Green{p.parseFuncDecl(lead)}, false
		case token.Extern:
			// extern declares a function only when fn follows; anything else
			// is a stray run the error node captures losslessly.
			if p.externBeginsFunc() {
				return []cst.Green{p.parseFuncDecl(lead)}, false
			}
			return []cst.Green{p.parseError(lead)}, false
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
// function declaration — i.e. an identifier follows it (fn name(...)),
// possibly after an effect list (fn io async name(...)). A bare fn begins a
// function literal or a function type, never a declaration.
func (p *parser) fnBeginsDecl(i int) bool {
	i++
	for isTrivia(p.toks[i].Kind) || p.toks[i].Kind.Effect() {
		i++
	}
	return p.toks[i].Kind == token.Ident
}

// externBeginsFunc reports whether the construct at the cursor — an optional
// pub, then extern — is followed by fn, i.e. begins an extern function
// declaration.
func (p *parser) externBeginsFunc() bool {
	i := p.nextSignificantIndex(p.pos)
	if p.toks[i].Kind == token.Pub {
		i = p.nextSignificantIndex(i + 1)
	}
	if p.toks[i].Kind != token.Extern {
		return false
	}
	return p.toks[p.nextSignificantIndex(i+1)].Kind == token.Fn
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
// — Const, Type, Enum, Interface, or Use — looking past an optional leading pub. For malformed
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

// beginsDeclaration reports whether the significant token at the cursor begins
// a construct nextChildren parses as a declaration — one that must end an error
// run. It mirrors nextChildren's dispatch exactly: const/type/enum/interface/
// use/assert always do; fn only as a declaration (fn name, not a stray
// literal); extern only as [pub] extern fn; and pub always, except the one
// shape the dispatcher itself routes back to the error parser — pub extern
// without fn. Keeping this predicate in lockstep with the dispatch is what
// guarantees parseError consumes at least one token (nextChildren only calls it
// on a token this predicate rejects), so the File-level loops always progress.
func (p *parser) beginsDeclaration() bool {
	switch p.peekSignificant() {
	case token.Const, token.Type, token.Enum, token.Interface, token.Use, token.Assert:
		return true
	case token.Fn:
		return p.fnBeginsDecl(p.nextSignificantIndex(p.pos))
	case token.Extern:
		return p.externBeginsFunc()
	case token.Pub:
		if p.declKind() == token.Extern {
			return p.externBeginsFunc()
		}
		return true
	default:
		return false
	}
}

// atUnterminatedConstructStop reports whether the parser sits at a point where
// an unterminated brace construct (a record literal, a switch body) must stop
// its recovery loop: EOF, or the start of a File-level declaration. It reuses
// beginsDeclaration — the same predicate parseError and the File loop use — so
// the recovery boundary always matches where a real declaration can begin,
// rather than a hand-maintained keyword list that drifts out of sync. Routing
// the conditional starters (fn only as a declaration, extern only as extern fn)
// through beginsDeclaration is what keeps a `fn` literal or an expression-level
// extern from falsely stopping the loop.
func (p *parser) atUnterminatedConstructStop() bool {
	return p.peekSignificant() == token.EOF || p.beginsDeclaration()
}

// parseError consumes a run of significant tokens that begin no declaration,
// folding in the interleaving trivia, until the next declaration starter
// (beginsDeclaration) or EOF. The trivia that precedes that stopping token is
// left behind to become the next construct's leading trivia. A single
// diagnostic is reported at the first offending token.
func (p *parser) parseError(lead []cst.Green) *cst.Node {
	children := lead
	reported := false
	for {
		if p.peekSignificant() == token.EOF || p.beginsDeclaration() {
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

// reportUnexpected reports that the next significant token was unexpected,
// anchoring the diagnostic at the last consumed token (p.lastStart) rather than
// at the offending token itself.
//
// It is used by the recovery paths that give up at an unexpected token and leave
// it unconsumed for an enclosing construct (or the File-level loop) to handle —
// the missing "{" of an enum/interface/impl block, the missing ")" of an
// argument list, the missing ":" of a record field or map entry, and so on. The
// offending token they stop at can be the start of the next File child (a
// declaration keyword, or any token an Error node captures, or EOF). Anchoring
// the diagnostic there would place it exactly on a File-child boundary, where it
// is indistinguishable from a diagnostic the following child produces at its own
// start. That ambiguity breaks the incremental diagnostic splice (see Document),
// which partitions diagnostics by offset across reused/reparsed regions and would
// double-count — or drop — a boundary-anchored one. Anchoring strictly inside the
// construct keeps every diagnostic attributable to exactly one File child, the
// invariant spliceDiags relies on.
func (p *parser) reportUnexpected() {
	p.report(newUnexpectedTokenDiagnostic(p.lastStart, 0, p.peekSignificant().String()))
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
