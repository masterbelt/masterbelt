package ast

import "github.com/masterbelt/masterbelt/pkg/masterbelt/source/cst"

// Expr is an initializer expression: a literal (IntLit/BoolLit), an identifier
// (Identifier), a member access (MemberExpr), or a call (CallExpr). Operators
// desugar to method calls — 1 + 2 becomes 1.add(2) — so no operator node here.
type Expr interface {
	Node
	expr()
}

// IntLit is an integer literal. Its Text is the literal as written; no numeric
// parsing or range checking happens at this layer.
type IntLit struct {
	Text   string
	syntax *cst.Node
}

func (l *IntLit) Syntax() *cst.Node { return l.syntax }
func (l *IntLit) node()             {}
func (l *IntLit) expr()             {}

// NewIntLit builds an IntLit node.
func NewIntLit(text string, syntax *cst.Node) *IntLit {
	return &IntLit{Text: text, syntax: syntax}
}

// BoolLit is a boolean literal: true or false.
type BoolLit struct {
	Value  bool
	syntax *cst.Node
}

func (l *BoolLit) Syntax() *cst.Node { return l.syntax }
func (l *BoolLit) node()             {}
func (l *BoolLit) expr()             {}

// NewBoolLit builds a BoolLit node.
func NewBoolLit(value bool, syntax *cst.Node) *BoolLit {
	return &BoolLit{Value: value, syntax: syntax}
}

// Identifier is a name occurrence: either a value reference to another
// declaration (the "a" in "const x = a") or the member name of a MemberExpr
// (the method an operator desugars to). Resolving a value reference to its
// target is a job for a later (semantic) layer, not this one.
type Identifier struct {
	Name   string
	syntax *cst.Node
}

func (i *Identifier) Syntax() *cst.Node { return i.syntax }
func (i *Identifier) node()             {}
func (i *Identifier) expr()             {}

// NewIdentifier builds an Identifier node.
func NewIdentifier(name string, syntax *cst.Node) *Identifier {
	return &Identifier{Name: name, syntax: syntax}
}

// MemberExpr is a member access, receiver.member. Operators desugar through it:
// the "+" in 1 + 2 is the member "add" of the receiver 1.
type MemberExpr struct {
	Receiver Expr        // the value the member is accessed on (nil if recovered away)
	Member   *Identifier // the member name (add, sub, lt, neg, ...)
	syntax   *cst.Node
}

func (m *MemberExpr) Syntax() *cst.Node { return m.syntax }
func (m *MemberExpr) node()             {}
func (m *MemberExpr) expr()             {}

// NewMemberExpr builds a MemberExpr node.
func NewMemberExpr(receiver Expr, member *Identifier, syntax *cst.Node) *MemberExpr {
	return &MemberExpr{Receiver: receiver, Member: member, syntax: syntax}
}

// CallExpr is a call of a callee with arguments. The surface syntax has no call
// form yet; it is the desugaring target of the operators — 1 + 2 becomes
// 1.add(2), i.e. CallExpr{Callee: (1).add, Arguments: [2]}, and -x becomes
// x.neg() with no arguments — so the IR types and evaluates every operator
// uniformly as a call of a member.
type CallExpr struct {
	Callee    Expr   // the called value; a MemberExpr for a desugared operator
	Arguments []Expr // arguments: one for a binary operator, none for a unary
	syntax    *cst.Node
}

func (c *CallExpr) Syntax() *cst.Node { return c.syntax }
func (c *CallExpr) node()             {}
func (c *CallExpr) expr()             {}

// NewCallExpr builds a CallExpr node.
func NewCallExpr(callee Expr, arguments []Expr, syntax *cst.Node) *CallExpr {
	return &CallExpr{Callee: callee, Arguments: arguments, syntax: syntax}
}
