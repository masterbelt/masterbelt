package concrete

import (
	"github.com/masterbelt/masterbelt/pkg/source/cst"
	"github.com/masterbelt/masterbelt/pkg/source/token"
)

// parseBlock parses a brace-delimited statement block: "{" Stmt* "}". The cursor
// sits on "{".
//
// The body is a fresh statement context, so the record-literal restriction an
// enclosing if condition or switch scrutinee carries (noRecordLit) is lifted for
// its statements — a function literal inside such a head parses its body right
// here, and without the reset a "{" statement in that body would parse as a
// zero-width error expression, which the statement loop would re-encounter
// forever. bracketed restores the restriction for the head's continuation after
// the closing "}".
func (p *parser) parseBlock() *cst.Node {
	children := []cst.Green{p.bump()} // "{"
	p.bracketed(func() {
		for {
			switch p.peekSignificant() {
			case token.RBrace:
				p.skipTrivia(&children)
				children = append(children, p.bump()) // "}"
				return
			case token.EOF:
				return
			default:
				p.skipTrivia(&children)
				before := p.pos
				children = append(children, p.parseStmt())
				if p.pos == before {
					// Progress guard: a statement parse that consumed no token
					// (a defensive recovery path) must not spin this loop — take
					// the offending token as a raw leaf and move on. Losslessness
					// holds either way.
					children = append(children, p.bump())
				}
			}
		}
	})
	return cst.NewNode(cst.Block, children)
}

// parseStmt parses a single statement: a let declaration, a return statement, a
// switch statement, a match statement, an if statement, a for statement, an
// assignment, or a bare expression statement. The cursor sits on the statement's
// first significant token.
//
// An assignment and a bare expression statement share a first token (both can
// begin with an identifier), so they are told apart after the fact: the leading
// expression is parsed, and a following "=" turns it into an assignment whose
// target is that expression. Keeping assignment a statement here — never a level
// of parseExpr — is what keeps "=" out of pure value positions (a const
// initializer, a where clause), where only "==" may appear.
func (p *parser) parseStmt() cst.Green {
	switch {
	case p.kind() == token.Let:
		return p.parseLetStmt()
	case p.kind() == token.Return:
		return p.parseReturnStmt()
	case p.kind() == token.Switch:
		return p.parseSwitchStmt()
	case p.kind() == token.Match:
		return p.parseMatchStmt()
	case p.kind() == token.If:
		return p.parseIfStmt()
	case p.kind() == token.For:
		return p.parseForStmt()
	case p.kind() == token.Assert:
		return p.parseAssertStmt()
	case startsExpr(p.kind()):
		target := p.parseExpr()
		if p.peekSignificant() == token.Assign {
			return p.parseAssignTail(target)
		}
		return target
	default:
		p.report(newUnexpectedTokenDiagnostic(p.cur().Offset, p.cur().Width, p.kind().String()))
		return p.bump()
	}
}

// parseLetStmt parses a mutable block-local binding:
//
//	LetStmt := let Ident [TypeClause] "=" Expr
//
// The binding is initialized in place (no "let x: T" without a value), so the
// "=" and its expression are required; a missing one is reported and left out,
// keeping recovery local. The optional type annotation reuses the constant's
// type clause (": TypeExpr"). The cursor sits on "let".
func (p *parser) parseLetStmt() *cst.Node {
	children := []cst.Green{p.bump()} // "let"
	if p.peekSignificant() == token.Ident {
		p.skipTrivia(&children)
		children = append(children, p.bump()) // the bound name
	} else {
		p.report(newExpectedIdentifierDiagnostic(p.lastStart, 0))
	}
	if p.peekSignificant() == token.Colon {
		p.skipTrivia(&children)
		children = append(children, p.parseTypeClause())
	}
	// A missing "= value" is reported by the semantic layer as missing_initializer
	// (let is initialized in place), not here — that diagnostic names the binding
	// and suggests the fix, so the parser leaves the value absent and recovers.
	if p.peekSignificant() == token.Assign {
		p.skipTrivia(&children)
		children = append(children, p.parseInitializer())
	}
	return cst.NewNode(cst.LetStmt, children)
}

// parseAssignTail completes an assignment from its already-parsed target — the
// leading expression a "=" follows: it consumes the "=" and parses the
// right-hand expression. The cursor sits on "=". The target's validity (a let
// local, not a const, a parameter, or immutable data) is left to the semantic
// layer; the parser accepts any expression target and round-trips it.
func (p *parser) parseAssignTail(target cst.Green) *cst.Node {
	children := []cst.Green{target}
	p.skipTrivia(&children)
	children = append(children, p.bump()) // "="
	if startsExpr(p.peekSignificant()) {
		p.skipTrivia(&children)
		children = append(children, p.parseExpr())
	} else {
		p.report(newExpectedExpressionDiagnostic(p.lastStart, 0))
	}
	return cst.NewNode(cst.AssignStmt, children)
}

// parseAssertStmt parses a statement-form assertion:
//
//	assert Expr
//
// Unlike the top-level AssertDecl — a compile-time, closed assertion folded once
// — an assert statement stands inside a block and is evaluated where it runs, so
// its condition may read the self and locals in scope (a master's validate each
// block runs it once per row). assert is a real keyword here, so it is taken
// directly. As elsewhere the expression is optional in the parse: a missing one
// is reported and left absent. The cursor sits on "assert".
func (p *parser) parseAssertStmt() *cst.Node {
	children := []cst.Green{p.bump()} // "assert"
	if startsExpr(p.peekSignificant()) {
		p.skipTrivia(&children)
		children = append(children, p.parseExpr())
	} else {
		p.report(newExpectedExpressionDiagnostic(p.lastStart, 0))
	}
	return cst.NewNode(cst.AssertStmt, children)
}

// parseIfStmt parses an if statement:
//
//	IfStmt := if Expr Block [ else ( IfStmt | Block ) ]
//
// The condition is an ordinary expression followed by a mandatory brace block,
// and an optional else branch is either another if (the else-if chain) or a
// block. if is a control statement, not an expression: it yields no value, so it
// never appears in expression position. The cursor sits on "if".
func (p *parser) parseIfStmt() *cst.Node {
	children := []cst.Green{p.bump()} // "if"
	if startsExpr(p.peekSignificant()) {
		p.skipTrivia(&children)
		// The condition's "{" opens the then-block, not a record literal: parse
		// it with the record-literal reading suppressed, exactly as a switch's
		// scrutinee does (the restriction Rust and Go put on an if condition).
		p.noRecordLit = true
		children = append(children, p.parseExpr())
		p.noRecordLit = false
	} else {
		p.report(newExpectedExpressionDiagnostic(p.lastStart, 0))
	}
	if p.peekSignificant() != token.LBrace {
		p.reportUnexpected()
		return cst.NewNode(cst.IfStmt, children)
	}
	p.skipTrivia(&children)
	children = append(children, p.parseBlock()) // the then-block
	if p.peekSignificant() != token.Else {
		return cst.NewNode(cst.IfStmt, children)
	}
	p.skipTrivia(&children)
	children = append(children, p.bump()) // "else"
	switch p.peekSignificant() {
	case token.If:
		p.skipTrivia(&children)
		children = append(children, p.parseIfStmt()) // else-if chain
	case token.LBrace:
		p.skipTrivia(&children)
		children = append(children, p.parseBlock())
	default:
		p.reportUnexpected()
	}
	return cst.NewNode(cst.IfStmt, children)
}

// parseForStmt parses a collection-iteration statement:
//
//	ForStmt := for Ident ( "of" | "in" ) Expr Block
//
// The loop variable is a plain identifier; "of" binds the value, "in" the key.
// The iterated expression is an ordinary expression followed by a mandatory
// brace block. As with an if condition or a switch scrutinee, the expression's
// "{" opens the loop body, not a record literal, so it is parsed with the
// record-literal reading suppressed. for is a control statement, not an
// expression: it yields no value. The cursor sits on "for".
func (p *parser) parseForStmt() *cst.Node {
	children := []cst.Green{p.bump()} // "for"
	if p.peekSignificant() == token.Ident {
		p.skipTrivia(&children)
		children = append(children, p.bump()) // the loop variable
	} else {
		p.report(newExpectedIdentifierDiagnostic(p.lastStart, 0))
	}
	switch p.peekSignificant() {
	case token.Of, token.In:
		p.skipTrivia(&children)
		children = append(children, p.bump()) // "of" or "in"
	default:
		p.reportUnexpected()
		return cst.NewNode(cst.ForStmt, children)
	}
	if startsExpr(p.peekSignificant()) {
		p.skipTrivia(&children)
		// The iterated expression's "{" opens the loop body, not a record
		// literal: parse it with the record-literal reading suppressed, exactly
		// as an if condition and a switch scrutinee do.
		p.noRecordLit = true
		children = append(children, p.parseExpr())
		p.noRecordLit = false
	} else {
		p.report(newExpectedExpressionDiagnostic(p.lastStart, 0))
	}
	if p.peekSignificant() != token.LBrace {
		p.reportUnexpected()
		return cst.NewNode(cst.ForStmt, children)
	}
	p.skipTrivia(&children)
	children = append(children, p.parseBlock()) // the loop body
	return cst.NewNode(cst.ForStmt, children)
}

// parseSwitchStmt parses a switch statement:
//
//	SwitchStmt := switch Expr "{" ( SwitchArm ( ("," | NL) SwitchArm )* )? "}"
//
// The scrutinee is an ordinary expression; the arms follow in a brace block,
// separated by commas and/or newlines (a newline is trivia, so the comma is
// optional, mirroring the enum-member and record-literal lists). The cursor
// sits on "switch".
func (p *parser) parseSwitchStmt() *cst.Node {
	return p.parseDispatchStmt(cst.SwitchStmt, (*parser).parseSwitchArm)
}

// parseDispatchStmt parses the shared shape of the two dispatch statements —
// switch (value arms) and match (type arms): the keyword, a scrutinee with
// the record-literal reading suppressed (its "{" opens the arm block), and a
// braced arm list with the unterminated-construct recovery the record literal
// uses. kind picks the node; parseArm parses one arm at the cursor.
func (p *parser) parseDispatchStmt(kind cst.Kind, parseArm func(*parser) *cst.Node) *cst.Node {
	children := []cst.Green{p.bump()} // the keyword
	if startsExpr(p.peekSignificant()) {
		p.skipTrivia(&children)
		// The scrutinee's "{" opens the arm block, not a record literal: parse
		// it with the record-literal reading suppressed (a parenthesized
		// scrutinee re-enables it inside the parens).
		p.noRecordLit = true
		children = append(children, p.parseExpr())
		p.noRecordLit = false
	} else {
		p.report(newExpectedExpressionDiagnostic(p.lastStart, 0))
	}
	if p.peekSignificant() != token.LBrace {
		p.reportUnexpected()
		return cst.NewNode(kind, children)
	}
	p.skipTrivia(&children)
	children = append(children, p.bump()) // "{"
	for {
		switch {
		case p.peekSignificant() == token.RBrace:
			p.skipTrivia(&children)
			children = append(children, p.bump()) // "}"
			return cst.NewNode(kind, children)
		case p.atUnterminatedConstructStop():
			// Unterminated: report the missing "}" and stop at EOF or before the
			// next File-level declaration so recovery stays local, exactly as the
			// record literal does. atUnterminatedConstructStop mirrors the File
			// dispatcher's declaration-starter set (beginsDeclaration), so the
			// boundary always matches where a real declaration can begin.
			p.report(newUnexpectedTokenDiagnostic(p.lastStart, 0, p.peekSignificant().String()))
			return cst.NewNode(kind, children)
		default:
			p.skipTrivia(&children)
			before := p.pos
			children = append(children, parseArm(p))
			if p.pos == before {
				// Progress guard — see parseBlock: an arm parse that consumed no
				// token must not spin this loop.
				children = append(children, p.bump())
			}
			if p.peekSignificant() == token.Comma {
				p.skipTrivia(&children)
				children = append(children, p.bump()) // ","
			}
		}
	}
}

// parseSwitchArm parses one arm of a switch:
//
//	SwitchArm := ( Expr ( "," Expr )* | "_" ) "->" ( Stmt | Block )
//
// The value patterns are ordinary expressions (the wildcard "_" is a NameRef
// the later layers special-case), one or more separated by commas, and the
// body after "->" is a single statement or a brace block — the same two body
// forms a function literal's arrow accepts. The cursor sits on the arm's first
// significant token.
func (p *parser) parseSwitchArm() *cst.Node {
	var children []cst.Green
	if !startsExpr(p.peekSignificant()) {
		p.report(newExpectedExpressionDiagnostic(p.cur().Offset, 0))
		return cst.NewNode(cst.SwitchArm, []cst.Green{p.bump()})
	}
	children = append(children, p.parseExpr())
	// Comma-separated value patterns. Every comma before "->" is a value
	// separator: the arm-separator comma only appears after the body. A comma
	// not followed by another expression is recovery (a trailing one), so the
	// loop stops and the missing "->" is reported below.
	for p.peekSignificant() == token.Comma {
		p.skipTrivia(&children)
		children = append(children, p.bump()) // ","
		if !startsExpr(p.peekSignificant()) {
			break
		}
		p.skipTrivia(&children)
		children = append(children, p.parseExpr())
	}
	if p.peekSignificant() != token.Arrow {
		p.reportUnexpected()
		return cst.NewNode(cst.SwitchArm, children)
	}
	p.skipTrivia(&children)
	children = append(children, p.bump()) // "->"
	switch {
	case p.peekSignificant() == token.LBrace:
		p.skipTrivia(&children)
		children = append(children, p.parseBlock())
	case startsStmt(p.peekSignificant()):
		p.skipTrivia(&children)
		children = append(children, p.parseStmt())
	default:
		p.report(newExpectedExpressionDiagnostic(p.lastStart, 0))
	}
	return cst.NewNode(cst.SwitchArm, children)
}

// parseMatchStmt parses a match statement:
//
//	MatchStmt := match Expr "{" ( MatchArm ( ("," | NL) MatchArm )* )? "}"
//
// The scrutinee is an ordinary expression; the type-pattern arms follow in a
// brace block, separated by commas and/or newlines (a newline is trivia, so the
// comma is optional, the same separator rule the switch arms use). The cursor
// sits on "match". Its driving loop mirrors parseSwitchStmt — the only
// difference is the arm grammar (a type pattern, not value patterns).
func (p *parser) parseMatchStmt() *cst.Node {
	return p.parseDispatchStmt(cst.MatchStmt, (*parser).parseMatchArm)
}

// parseMatchArm parses one arm of a match:
//
//	MatchArm := MatchPattern "->" ( Stmt | Block )
//
// The pattern is a member type with an optional binding name (or the wildcard
// "_"), and the body after "->" is a single statement or a brace block — the
// same two body forms the switch arm and a function literal's arrow accept. The
// cursor sits on the arm's first significant token.
func (p *parser) parseMatchArm() *cst.Node {
	if !startsMatchPattern(p.peekSignificant()) {
		p.report(newExpectedTypeDiagnostic(p.cur().Offset, 0))
		return cst.NewNode(cst.MatchArm, []cst.Green{p.bump()})
	}
	children := []cst.Green{p.parseMatchPattern()}
	if p.peekSignificant() != token.Arrow {
		p.reportUnexpected()
		return cst.NewNode(cst.MatchArm, children)
	}
	p.skipTrivia(&children)
	children = append(children, p.bump()) // "->"
	switch {
	case p.peekSignificant() == token.LBrace:
		p.skipTrivia(&children)
		children = append(children, p.parseBlock())
	case startsStmt(p.peekSignificant()):
		p.skipTrivia(&children)
		children = append(children, p.parseStmt())
	default:
		p.report(newExpectedExpressionDiagnostic(p.lastStart, 0))
	}
	return cst.NewNode(cst.MatchArm, children)
}

// parseMatchPattern parses one arm's type pattern:
//
//	MatchPattern := ( PrimaryType [Ident] ) | "_"
//
// A pattern is a single (non-union) member type with an optional binding name —
// Coin c, null, int v, error e — or the wildcard "_", which is a bare
// identifier the later layers special-case (mirroring the switch wildcard). The
// type is a primary type (parsePrimaryType), so a union written in an arm parses
// only its first member here and the stray "|" is reported by the arrow check.
// The cursor sits on the pattern's first significant token.
func (p *parser) parseMatchPattern() *cst.Node {
	children := []cst.Green{p.parsePrimaryType()}
	// An optional binding name follows the type, unless the next token is the
	// "->" that ends the pattern (a bare type / the wildcard binds nothing).
	if p.peekSignificant() == token.Ident {
		p.skipTrivia(&children)
		children = append(children, p.bump()) // the binding name
	}
	return cst.NewNode(cst.MatchPattern, children)
}

// startsMatchPattern reports whether kind can begin a match arm's type pattern —
// the same set a primary type begins with (a named type, self/null, a record or
// function type, builtin), which also admits the wildcard "_" (an Ident).
func startsMatchPattern(kind token.Kind) bool {
	return startsType(kind)
}

// startsStmt reports whether kind can begin a statement: a let, a return, a
// switch, a match, an if, an assert, or any expression (which may continue into
// an assignment). It gates the single-statement body a switch or match arm takes
// after "->", so every statement form parseStmt accepts must appear here or it
// is rejected there.
func startsStmt(kind token.Kind) bool {
	return kind == token.Let || kind == token.Return || kind == token.Switch || kind == token.Match || kind == token.If || kind == token.Assert || startsExpr(kind)
}

// parseReturnStmt parses "return Expr". The cursor sits on "return".
func (p *parser) parseReturnStmt() *cst.Node {
	children := []cst.Green{p.bump()} // "return"
	if startsExpr(p.peekSignificant()) {
		p.skipTrivia(&children)
		children = append(children, p.parseExpr())
	} else {
		p.report(newExpectedExpressionDiagnostic(p.lastStart, 0))
	}
	return cst.NewNode(cst.ReturnStmt, children)
}

// parseExpr parses a whole expression. The cursor sits on the first significant
// token of the expression. It returns the single green node for the whole
// expression — a Literal/NameRef for an atom, or a BinaryExpr/UnaryExpr/
// RangeExpr/TernaryExpr tree.
//
// The two loosest operators are read here, after the binary climb, in this
// order of looseness (loosest last): the range operator (".." / "...") binds
// looser than every binary operator and tighter than the ternary, so a range
// bound is a full binary expression (0..n + 1 is 0..(n+1), not (0..n)+1) yet a
// whole range can be a ternary branch and operand. The range is non-associative:
// a chain a..b..c is a parse error (a second range operator after one range is
// not its own operand), unlike the left-associative binary operators and the
// right-associative ternary.
//
// The ternary "?:" is the one operator looser than the range, so it wraps it: a
// "?" following the (possibly range) expression opens the conditional, whose
// then/else are themselves full expressions — which makes it right-associative
// (a ? b : c ? d : e groups as a ? b : (c ? d : e)).
func (p *parser) parseExpr() cst.Green {
	left := p.parseBinary(precLowest)
	if k := p.peekSignificant(); k == token.DotDot || k == token.DotDotDot {
		left = p.parseRangeTail(left)
	}
	if p.peekSignificant() == token.Question {
		return p.parseTernaryTail(left)
	}
	return left
}

// parseRangeTail completes a range literal from its already-parsed lower operand:
// it consumes the ".." or "..." operator and parses the upper operand as a binary
// expression (parseBinary), so an arithmetic bound binds tighter than the range
// (0..n + 1 is 0..(n + 1)) while a range never reaches into a ternary or another
// range. The range is non-associative: a second ".."/"..." after the operand is a
// chain (a..b..c), reported as an unexpected operand rather than parsed — the
// range operator is not in binaryPrec, so the binary climb never consumes it, and
// this tail consumes exactly one. The cursor sits on the range operator.
func (p *parser) parseRangeTail(lower cst.Green) cst.Green {
	children := []cst.Green{lower}
	p.skipTrivia(&children)
	op := p.cur() // the range operator (cursor is on it)
	children = append(children, p.bump())
	if startsExpr(p.peekSignificant()) {
		p.skipTrivia(&children)
		children = append(children, p.parseBinary(precLowest))
	} else {
		p.report(newExpectedOperandDiagnostic(p.lastStart, 0, op.Kind.Symbol()))
	}
	// A chained range a..b..c: the upper operand stops before the second operator
	// (it is not a binary operator, so parseBinary leaves it), and a range is not
	// its own operand. Report the stray operator and leave it for the caller's
	// recovery so the node round-trips and the statement/argument loop moves past
	// it; the report anchors at the last consumed token, inside this construct.
	if k := p.peekSignificant(); k == token.DotDot || k == token.DotDotDot {
		p.report(newUnexpectedTokenDiagnostic(p.lastStart, 0, k.String()))
	}
	return cst.NewNode(cst.RangeExpr, children)
}

// parseBinary parses the binary-operator layer by precedence climbing: an
// operand (parseUnary) followed by any run of operators binding at least as
// tightly as minPrec. It is the core parseExpr drives, leaving the looser
// ternary to its caller.
func (p *parser) parseBinary(minPrec int) cst.Green {
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
			children = append(children, p.parseBinary(prec+1)) // prec+1 ⇒ left-associative
		} else {
			p.report(newExpectedOperandDiagnostic(p.lastStart, 0, op.Kind.Symbol()))
		}
		left = cst.NewNode(cst.BinaryExpr, children)
	}
}

// parseTernaryTail completes a ternary from its already-parsed condition: it
// consumes "?", parses the then-branch, expects ":", and parses the else-branch.
// Both branches are full expressions (parseExpr), so a chained ternary nests on
// the right. The cursor sits on "?". A missing branch or ":" is reported,
// leaving the node with what was parsed so recovery stays local.
func (p *parser) parseTernaryTail(cond cst.Green) cst.Green {
	children := []cst.Green{cond}
	p.skipTrivia(&children)
	children = append(children, p.bump()) // "?"
	if startsExpr(p.peekSignificant()) {
		p.skipTrivia(&children)
		children = append(children, p.parseExpr()) // the then-branch
	} else {
		p.report(newExpectedExpressionDiagnostic(p.lastStart, 0))
	}
	if p.peekSignificant() != token.Colon {
		p.reportUnexpected()
		return cst.NewNode(cst.TernaryExpr, children)
	}
	p.skipTrivia(&children)
	children = append(children, p.bump()) // ":"
	if startsExpr(p.peekSignificant()) {
		p.skipTrivia(&children)
		children = append(children, p.parseExpr()) // the else-branch (right-associative)
	} else {
		p.report(newExpectedExpressionDiagnostic(p.lastStart, 0))
	}
	return cst.NewNode(cst.TernaryExpr, children)
}

// parseUnary parses a chain of prefix operators ending in a postfix expression,
// or a bare postfix expression. The cursor sits on the first significant token.
func (p *parser) parseUnary() cst.Green {
	if p.kind() == token.Await {
		// await Expr: the explicit suspension point. It binds like a prefix
		// operator — over the whole postfix chain — but keeps its own node:
		// it marks a suspension rather than desugaring to a method call.
		children := []cst.Green{p.bump()} // "await"
		if startsExpr(p.peekSignificant()) {
			p.skipTrivia(&children)
			children = append(children, p.parseUnary())
		} else {
			p.report(newExpectedOperandDiagnostic(p.lastStart, 0, "await"))
		}
		return cst.NewNode(cst.AwaitExpr, children)
	}
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

// memberNameAhead reports whether a member name follows the just-consumed ".".
// A plain identifier is a member name even across a newline — the leading-dot
// continuation a method chain takes (foo\n  .bar). A keyword is a member name
// only on the dot's own line: a reserved word on the next line heads a new
// declaration (const, type, pub, …), and an incomplete "x." at a line's end must
// not swallow it. So item.type reads `type` as the member, while `C.` at end of
// line leaves the member missing and the following `const B = …` intact.
func (p *parser) memberNameAhead() bool {
	k := p.peekSignificant()
	if k == token.Ident {
		return true
	}
	return k.Keyword() && p.nextOnLine(p.pos) == k
}

// parsePostfix parses an operand followed by any chain of member accesses,
// calls, and index accesses — receiver.member, callee(args), and receiver[i] —
// applied left to right, so self.id, int32(self.id), and xs[0][1] form
// left-leaning MemberExpr/CallExpr/IndexExpr trees. An index "[" is read only
// here, after an operand: the leading "[" of a collection literal is an operand
// (parseOperand), so the two never collide.
func (p *parser) parsePostfix() cst.Green {
	left := p.parseOperand()
	for {
		switch p.peekSignificant() {
		case token.Dot:
			children := []cst.Green{left}
			p.skipTrivia(&children)
			children = append(children, p.bump()) // "."
			if p.memberNameAhead() {
				p.skipTrivia(&children)
				children = append(children, p.bump()) // the member name (a keyword reads as a name here)
			} else {
				p.report(newExpectedIdentifierDiagnostic(p.lastStart, 0))
			}
			left = cst.NewNode(cst.MemberExpr, children)
		case token.LParen:
			children := make([]cst.Green, 0, 2)
			children = append(children, left)
			p.skipTrivia(&children)
			children = append(children, p.bump()) // "("
			p.parseCallArgs(&children)
			left = cst.NewNode(cst.CallExpr, children)
		case token.LBracket:
			left = p.parseIndexTail(left)
		default:
			return left
		}
	}
}

// parseIndexTail completes an index access from its already-parsed receiver: it
// consumes "[", parses the index expression, and expects "]". The "[" opens a
// bracketed context, so the record-literal restriction an if condition or switch
// scrutinee carries is lifted for the index (a record-keyed lookup parses), then
// restored for the postfix continuation. The cursor sits on "[".
func (p *parser) parseIndexTail(receiver cst.Green) *cst.Node {
	children := []cst.Green{receiver}
	p.skipTrivia(&children)
	children = append(children, p.bump()) // "["
	p.bracketed(func() {
		if startsExpr(p.peekSignificant()) {
			p.skipTrivia(&children)
			children = append(children, p.parseExpr())
		} else {
			p.report(newExpectedExpressionDiagnostic(p.lastStart, 0))
		}
	})
	p.closeBracket(&children)
	return cst.NewNode(cst.IndexExpr, children)
}

// parseCallArgs parses a call's argument list up to and including the closing
// ")", appending the argument expressions and punctuation to *children. The
// cursor sits just past the opening "(".
func (p *parser) parseCallArgs(children *[]cst.Green) {
	p.bracketed(func() {
		if startsExpr(p.peekSignificant()) {
			for {
				p.skipTrivia(children)
				*children = append(*children, p.parseExpr())
				if p.peekSignificant() == token.Comma {
					p.skipTrivia(children)
					*children = append(*children, p.bump()) // ","
					if startsExpr(p.peekSignificant()) {
						continue // another argument follows the comma
					}
					// A trailing comma: the loop ends and ")" follows.
				}
				break
			}
		}
	})
	if p.peekSignificant() == token.RParen {
		p.skipTrivia(children)
		*children = append(*children, p.bump()) // ")"
	} else {
		p.reportUnexpected()
	}
}

// parseOperand parses an atom: a literal (integer, string, boolean, or null), a
// collection literal ("[...]"), a record literal ("Name{...}" or "{...}"), a
// NameRef, the self receiver, or a parenthesized grouping. The cursor sits on
// the operand token — startsExpr gates every call site, so the default arm is
// defensive and consumes nothing.
func (p *parser) parseOperand() cst.Green {
	// The record-literal restriction holds for the whole top-level expression
	// (the if condition / switch scrutinee), so a "{" that follows any operand of
	// it — not only the leftmost — opens the control block rather than a record
	// literal. It is re-enabled the moment a bracketed context (parens, call
	// arguments, a collection, a record's field values) makes "{" unambiguous
	// again; those contexts clear the flag as they recurse.
	noRecordLit := p.noRecordLit
	switch p.kind() {
	case token.Int, token.BinInt, token.OctInt, token.HexInt, token.String, token.DatetimeLit, token.DurationLit, token.True, token.False, token.Null:
		return cst.NewNode(cst.Literal, []cst.Green{p.bump()})
	case token.LBracket:
		return p.parseCollectionLiteral()
	case token.LBrace:
		if noRecordLit {
			// The "{" opens a switch's arm block, not an inferred record literal:
			// stop here so the switch parser takes it.
			p.report(newExpectedExpressionDiagnostic(p.lastStart, 0))
			return cst.NewNode(cst.Error, nil)
		}
		return p.parseRecordLit(nil) // the inferred form: "{" with no type name
	case token.Ident:
		name := p.bump()
		if !noRecordLit && p.peekSignificant() == token.LBrace {
			return p.parseRecordLit([]cst.Green{name}) // the typed form: Name "{"
		}
		return cst.NewNode(cst.NameRef, []cst.Green{name})
	case token.Self:
		return cst.NewNode(cst.SelfExpr, []cst.Green{p.bump()})
	case token.Type:
		// The `type` keyword names the metatype as a value (const t = type; type :
		// type). It reads as a name reference here — a value-expression position
		// never begins the `type Foo =` declaration the keyword otherwise heads —
		// and the semantic layer reifies it to a type value, like any type name.
		return cst.NewNode(cst.NameRef, []cst.Green{p.bump()})
	case token.Fn:
		return p.parseFuncLit()
	case token.LParen:
		return p.parseParenExpr()
	default:
		p.report(newExpectedExpressionDiagnostic(p.lastStart, 0))
		return cst.NewNode(cst.Error, nil)
	}
}

// parseRecordLit parses a record literal's field block:
//
//	RecordLit   := [Ident] "{" ( RecordField [","] )* "}"
//	RecordField := Ident ":" Expr
//
// children holds the already-consumed type name for the typed form (Point{...})
// and is nil for the inferred form ({...}), whose type comes from the expected
// type. Fields separate with commas and/or newlines — the comma after a field
// is optional because a newline (trivia) separates just as well, mirroring the
// record type. The cursor sits before "{" (on it, or on the trivia run ahead of
// it for the typed form).
func (p *parser) parseRecordLit(children []cst.Green) *cst.Node {
	p.skipTrivia(&children)
	children = append(children, p.bump()) // "{"
	var node *cst.Node
	// The field values are inside the braces, so a "{" there is again a record
	// literal, not a control block: the head-expression restriction is lifted for
	// the body and restored on return.
	p.bracketed(func() {
		for {
			switch {
			case p.peekSignificant() == token.RBrace:
				p.skipTrivia(&children)
				children = append(children, p.bump()) // "}"
				node = cst.NewNode(cst.RecordLit, children)
				return
			case p.recordFieldKeyword():
				// A field whose name is a keyword read as an identifier (type:, for:):
				// recognized only when the ":" follows, so a leaked declaration the
				// unterminated-recovery below stops at is not mistaken for a field.
				p.skipTrivia(&children)
				children = append(children, p.parseRecordField())
				if p.peekSignificant() == token.Comma {
					p.skipTrivia(&children)
					children = append(children, p.bump()) // ","
				}
			case p.atUnterminatedConstructStop():
				// Unterminated: report the missing "}" and stop at EOF or before the
				// next File-level declaration so recovery stays local. The boundary
				// follows beginsDeclaration (see atUnterminatedConstructStop), so a
				// following enum/interface/fn declaration is not swallowed
				// token-by-token. The diagnostic anchors at the last consumed token to
				// stay inside this construct (see lastStart); the leaves are still
				// lossless.
				p.report(newUnexpectedTokenDiagnostic(p.lastStart, 0, p.peekSignificant().String()))
				node = cst.NewNode(cst.RecordLit, children)
				return
			case p.peekSignificant() == token.Ident:
				p.skipTrivia(&children)
				children = append(children, p.parseRecordField())
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
	})
	return node
}

// recordFieldKeyword reports whether the cursor begins a record-literal field
// whose name is a keyword read as an identifier — a reserved word (type, for, …)
// immediately followed by the ":" that opens its value. The trailing-colon check
// keeps a leaked declaration (an unterminated literal running into `type Foo =`)
// out of the field path, so the unterminated-recovery loop still stops at the
// declaration boundary; a plain-Ident field is handled by its own arm, where a
// keyword can never reach. The lookahead reads only token kinds, so the boundary
// context-free property the incremental Document relies on holds.
func (p *parser) recordFieldKeyword() bool {
	i := p.nextSignificantIndex(p.pos)
	if !p.toks[i].Kind.Keyword() {
		return false
	}
	return p.toks[p.nextSignificantIndex(i+1)].Kind == token.Colon
}

// parseRecordField parses one field initializer: Ident ":" Expr. A missing ":"
// or value is reported, leaving the field with what was parsed so recovery is
// local and the closing "}" is not swallowed. The cursor sits on the field name.
func (p *parser) parseRecordField() *cst.Node {
	children := []cst.Green{p.bump()} // the field name
	if p.peekSignificant() != token.Colon {
		p.reportUnexpected()
		return cst.NewNode(cst.RecordField, children)
	}
	p.skipTrivia(&children)
	children = append(children, p.bump()) // ":"
	if startsExpr(p.peekSignificant()) {
		p.skipTrivia(&children)
		children = append(children, p.parseExpr())
	} else {
		p.report(newExpectedExpressionDiagnostic(p.lastStart, 0))
	}
	return cst.NewNode(cst.RecordField, children)
}

// parseParenExpr parses a parenthesized grouping: "(" Expr ")". The node exists
// only to override operator precedence — lowering unwraps it to the inner
// expression. The cursor sits on "(".
func (p *parser) parseParenExpr() *cst.Node {
	children := []cst.Green{p.bump()} // "("
	p.bracketed(func() {
		if startsExpr(p.peekSignificant()) {
			p.skipTrivia(&children)
			children = append(children, p.parseExpr())
		} else {
			p.report(newExpectedExpressionDiagnostic(p.lastStart, 0))
		}
	})
	if p.peekSignificant() == token.RParen {
		p.skipTrivia(&children)
		children = append(children, p.bump()) // ")"
	} else {
		p.reportUnexpected()
	}
	return cst.NewNode(cst.ParenExpr, children)
}

// parseFuncLit parses a function-literal expression: fn ParamList [":" TypeExpr]
// ( "->" Expr | Block ). It is the value form of a function type (parseFuncType)
// — same header, but with a body — and is the only way to construct a value of a
// function type. The body comes in two forms: an arrow body, "->" followed by a
// single expression (an implicit return), and a brace block for statement
// bodies. An arrow followed by "{" is rejected with a pointer to drop the arrow:
// the two forms stay disjoint. Unlike a function type or a method declaration,
// the literal's parameter and result annotations are optional: a checking
// context (the expected type) may supply them, so the parser accepts their
// absence and leaves the complaint to the type checker. The cursor sits on "fn".
func (p *parser) parseFuncLit() *cst.Node {
	children := []cst.Green{p.bump()} // "fn"
	if p.peekSignificant() == token.LParen {
		p.skipTrivia(&children)
		children = append(children, p.parseParamList(false))
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
	}
	switch p.peekSignificant() {
	case token.Arrow:
		p.skipTrivia(&children)
		children = append(children, p.bump()) // "->"
		switch {
		case p.peekSignificant() == token.LBrace:
			// "fn(x) -> { ... }": an arrow body must be an expression. Report
			// it, then parse the block anyway so recovery stays local and the
			// rest of the literal round-trips.
			p.skipTrivia(&children)
			p.report(newArrowBlockBodyDiagnostic(p.cur().Offset, p.cur().Width))
			children = append(children, p.parseBlock())
		case startsExpr(p.peekSignificant()):
			// The arrow body is a fresh expression context, not part of the
			// enclosing if condition / switch scrutinee: a record literal here is
			// unambiguous, so lift the head's noRecordLit restriction for it (the
			// block body does the same through parseBlock). Without this a typed
			// record literal in the body of a lambda used in an if/switch head
			// mis-parses — the "{" is read as the control block instead.
			p.skipTrivia(&children)
			p.bracketed(func() {
				children = append(children, p.parseExpr())
			})
		default:
			p.report(newExpectedExpressionDiagnostic(p.lastStart, 0))
		}
	case token.LBrace:
		p.skipTrivia(&children)
		children = append(children, p.parseBlock())
	default:
		p.report(newExpectedFuncBodyDiagnostic(p.lastStart, 0))
	}
	return cst.NewNode(cst.FuncLit, children)
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
	var node *cst.Node
	p.bracketed(func() {
		if !startsExpr(p.peekSignificant()) {
			p.closeBracket(&children)
			node = cst.NewNode(cst.CollectionLit, children)
			return
		}
		p.skipTrivia(&children)
		first := p.parseExpr()
		if p.peekSignificant() == token.Colon {
			node = p.parseMapRest(children, first)
			return
		}
		node = p.parseListRest(children, first)
	})
	return node
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
		children = append(children, p.parseExpr())
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
		children = append(children, p.finishMapEntry(p.parseExpr()))
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
		p.reportUnexpected()
		return cst.NewNode(cst.MapEntry, entry)
	}
	p.skipTrivia(&entry)
	entry = append(entry, p.bump()) // ":"
	if startsExpr(p.peekSignificant()) {
		p.skipTrivia(&entry)
		entry = append(entry, p.parseExpr())
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
		p.reportUnexpected()
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

// nameLike reports whether kind can stand in for an identifier at a position the
// grammar makes unambiguous: a member name after ".", a record field name, a
// function parameter name. Any keyword qualifies there (item.type, record { type:
// SkillKind }, fn f(for: int)) — the position never begins a keyword construct,
// so reading the reserved word as a name introduces no ambiguity. The let/loop
// binding names and the declaration names keep requiring a plain Ident, where a
// keyword would collide with the construct it heads (type Foo = ...).
func nameLike(kind token.Kind) bool {
	return kind == token.Ident || kind.Keyword()
}

// startsExpr reports whether kind can begin an expression. The parser checks it
// before committing to an operand so a missing one is reported (and leaves
// trailing trivia for the next construct) rather than mis-parsed.
func startsExpr(kind token.Kind) bool {
	switch kind {
	case token.Int, token.BinInt, token.OctInt, token.HexInt, token.String, token.DatetimeLit, token.DurationLit,
		token.Ident, token.True, token.False, token.Null, token.Self, token.Type,
		token.LBracket, token.LBrace, token.Plus, token.Minus, token.Bang,
		token.Fn, token.LParen, token.Await:
		return true
	default:
		return false
	}
}
