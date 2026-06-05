package concrete

import (
	"github.com/masterbelt/masterbelt/pkg/source/cst"
	"github.com/masterbelt/masterbelt/pkg/source/token"
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

// parseStmt parses a single statement: a let declaration, a return statement, a
// switch statement, an if statement, an assignment, or a bare expression
// statement. The cursor sits on the statement's first significant token.
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
	case p.kind() == token.If:
		return p.parseIfStmt()
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
		p.report(newUnexpectedTokenDiagnostic(p.cur().Offset, p.cur().Width, p.kind().String()))
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
		p.report(newUnexpectedTokenDiagnostic(p.cur().Offset, p.cur().Width, p.kind().String()))
	}
	return cst.NewNode(cst.IfStmt, children)
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
	children := []cst.Green{p.bump()} // "switch"
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
		p.report(newUnexpectedTokenDiagnostic(p.cur().Offset, p.cur().Width, p.kind().String()))
		return cst.NewNode(cst.SwitchStmt, children)
	}
	p.skipTrivia(&children)
	children = append(children, p.bump()) // "{"
	for {
		switch p.peekSignificant() {
		case token.RBrace:
			p.skipTrivia(&children)
			children = append(children, p.bump()) // "}"
			return cst.NewNode(cst.SwitchStmt, children)
		case token.EOF, token.Pub, token.Const, token.Type, token.Enum, token.Use, token.Assert:
			// Unterminated: report the missing "}" and stop before the next
			// declaration so recovery stays local, exactly as the record literal
			// does.
			p.report(newUnexpectedTokenDiagnostic(p.lastStart, 0, p.peekSignificant().String()))
			return cst.NewNode(cst.SwitchStmt, children)
		default:
			p.skipTrivia(&children)
			children = append(children, p.parseSwitchArm())
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
		p.report(newUnexpectedTokenDiagnostic(p.cur().Offset, p.cur().Width, p.kind().String()))
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

// startsStmt reports whether kind can begin a statement: a let, a return, a
// switch, an if, or any expression (which may continue into an assignment).
func startsStmt(kind token.Kind) bool {
	return kind == token.Let || kind == token.Return || kind == token.Switch || kind == token.If || startsExpr(kind)
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
// TernaryExpr tree.
//
// The ternary "?:" binds looser than every binary operator, so it is read here,
// after the binary climb: a "?" following the binary expression opens the
// conditional, whose then/else are themselves full expressions — which makes it
// right-associative (a ? b : c ? d : e groups as a ? b : (c ? d : e)). A "?"
// that surfaces inside the binary climb (the right operand of "+", say) is left
// for this level, so a > b ? a : b is (a > b) ? a : b.
func (p *parser) parseExpr() cst.Green {
	left := p.parseBinary(precLowest)
	if p.peekSignificant() == token.Question {
		return p.parseTernaryTail(left)
	}
	return left
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
		p.report(newUnexpectedTokenDiagnostic(p.cur().Offset, p.cur().Width, p.kind().String()))
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
	p.bracketed(func() {
		if startsExpr(p.peekSignificant()) {
			for {
				p.skipTrivia(children)
				*children = append(*children, p.parseExpr())
				if p.peekSignificant() == token.Comma {
					p.skipTrivia(children)
					*children = append(*children, p.bump()) // ","
					continue
				}
				break
			}
		}
	})
	if p.peekSignificant() == token.RParen {
		p.skipTrivia(children)
		*children = append(*children, p.bump()) // ")"
	} else {
		p.report(newUnexpectedTokenDiagnostic(p.cur().Offset, p.cur().Width, p.kind().String()))
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
	case token.Int, token.String, token.DatetimeLit, token.DurationLit, token.True, token.False, token.Null:
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
			switch p.peekSignificant() {
			case token.RBrace:
				p.skipTrivia(&children)
				children = append(children, p.bump()) // "}"
				node = cst.NewNode(cst.RecordLit, children)
				return
			case token.EOF, token.Pub, token.Const, token.Type, token.Use, token.Assert:
				// Unterminated: report the missing "}" and stop before the next
				// declaration so recovery stays local. The diagnostic anchors at the
				// last consumed token to stay inside this construct (see lastStart);
				// the leaves are still lossless.
				p.report(newUnexpectedTokenDiagnostic(p.lastStart, 0, p.peekSignificant().String()))
				node = cst.NewNode(cst.RecordLit, children)
				return
			case token.Ident:
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

// parseRecordField parses one field initializer: Ident ":" Expr. A missing ":"
// or value is reported, leaving the field with what was parsed so recovery is
// local and the closing "}" is not swallowed. The cursor sits on the field name.
func (p *parser) parseRecordField() *cst.Node {
	children := []cst.Green{p.bump()} // the field name
	if p.peekSignificant() != token.Colon {
		p.report(newUnexpectedTokenDiagnostic(p.cur().Offset, p.cur().Width, p.kind().String()))
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
		p.report(newUnexpectedTokenDiagnostic(p.cur().Offset, p.cur().Width, p.kind().String()))
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
			p.skipTrivia(&children)
			children = append(children, p.parseExpr())
		default:
			p.report(newExpectedExpressionDiagnostic(p.lastStart, 0))
		}
	case token.LBrace:
		p.skipTrivia(&children)
		children = append(children, p.parseBlock())
	default:
		p.report(newExpectedFuncBodyDiagnostic(p.cur().Offset, p.cur().Width))
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
		p.report(newUnexpectedTokenDiagnostic(p.cur().Offset, p.cur().Width, p.kind().String()))
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
	case token.Int, token.String, token.DatetimeLit, token.DurationLit,
		token.Ident, token.True, token.False, token.Null, token.Self,
		token.LBracket, token.LBrace, token.Plus, token.Minus, token.Bang,
		token.Fn, token.LParen, token.Await:
		return true
	default:
		return false
	}
}
