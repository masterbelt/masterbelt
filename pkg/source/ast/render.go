package ast

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Render renders an expression in surface syntax, inverting the operator
// desugaring: a call of an operator method renders as the operator form it
// lowered from (1.add(2) back to 1 + 2), parenthesized only where precedence
// demands. Diagnostics quote an expression back to the user through it — the
// analysis works over the position-independent AST, so the original source
// text is out of reach, and this canonical form is stable under whitespace
// and comment edits besides.
//
// The operator spellings and precedences mirror the desugaring in
// parser/abstract and the precedence table in parser/concrete; a round-trip
// test in parser/abstract pins the three to one another.
func Render(e Expr) string {
	r := &renderer{}
	r.expr(e, 0)
	return r.b.String()
}

// Anchor is one value-bearing sub-expression of a rendered expression and the
// rune column its value reads best under: an identifier or self anchors at its
// own start, a member access and a method call at the member name, and an
// operator form at the operator symbol — the spot a power-assert diagram
// points its pipe at.
type Anchor struct {
	Expr Expr
	Col  int // rune column within the rendered text
}

// RenderTrace renders e exactly as Render does and additionally returns the
// anchors of its value-bearing sub-expressions, in render order. Literals and
// collection literals carry no anchor — their value is their own spelling.
func RenderTrace(e Expr) (string, []Anchor) {
	r := &renderer{trace: true}
	r.expr(e, 0)
	return r.b.String(), r.anchors
}

// binaryOps maps each binary operator method to its surface spelling and
// precedence (higher binds tighter, matching parser/concrete's binaryPrec).
var binaryOps = map[string]struct {
	sym  string
	prec int
}{
	"oror": {"||", 1},
	"anan": {"&&", 2},
	"eql":  {"==", 3},
	"neq":  {"!=", 3},
	"lt":   {"<", 3},
	"lteq": {"<=", 3},
	"gt":   {">", 3},
	"gteq": {">=", 3},
	"add":  {"+", 4},
	"sub":  {"-", 4},
	"mul":  {"*", 5},
	"div":  {"/", 5},
	"rem":  {"%", 5},
}

// unaryOps maps each prefix operator method to its surface spelling.
var unaryOps = map[string]string{"pos": "+", "neg": "-", "not": "!"}

// A prefix operator binds tighter than any binary operator, and a postfix
// member access or call tighter still — an operator-formed receiver needs the
// grouping parentheses there.
const (
	precUnary   = 6
	precPostfix = 7
)

// renderer accumulates the rendered text, tracking the rune column so anchors
// can be recorded as the writes happen.
type renderer struct {
	b       strings.Builder
	col     int // runes written so far
	trace   bool
	anchors []Anchor
}

func (r *renderer) str(s string) {
	r.b.WriteString(s)
	r.col += utf8.RuneCountInString(s)
}

// anchor records e's value anchor at the current column.
func (r *renderer) anchor(e Expr) {
	if r.trace {
		r.anchors = append(r.anchors, Anchor{Expr: e, Col: r.col})
	}
}

// expr renders e, parenthesizing it when its own binding is looser than min —
// the binding the enclosing context requires.
func (r *renderer) expr(e Expr, min int) {
	switch x := e.(type) {
	case nil:
		r.str("<missing>")
	case *IntLit:
		r.str(x.Text)
	case *StringLit:
		r.str(fmt.Sprintf("%q", x.Value))
	case *BoolLit:
		if x.Value {
			r.str("true")
		} else {
			r.str("false")
		}
	case *NullLit:
		r.str("null")
	case *SelfExpr:
		r.anchor(x)
		r.str("self")
	case *Identifier:
		r.anchor(x)
		r.str(x.Name)
	case *CollectionLit:
		r.str("[")
		for i, entry := range x.Entries {
			if i > 0 {
				r.str(", ")
			}
			if entry.Key != nil {
				r.expr(entry.Key, 0)
				r.str(": ")
			}
			r.expr(entry.Value, 0)
		}
		r.str("]")
	case *MemberExpr:
		r.expr(x.Receiver, precPostfix)
		r.str(".")
		r.anchor(x)
		r.str(x.Member.Name)
	case *CallExpr:
		r.call(x, min)
	case *FuncLit:
		r.funcLit(x)
	default:
		r.str("<expr>")
	}
}

// call renders a call expression: an operator method back as its operator
// form, anything else as callee(args). The call's value anchors at the
// operator symbol or the callee's name — the callee itself is not a value, so
// it records no anchor of its own.
func (r *renderer) call(x *CallExpr, min int) {
	if m, ok := x.Callee.(*MemberExpr); ok {
		if op, ok := binaryOps[m.Member.Name]; ok && len(x.Arguments) == 1 {
			paren := op.prec < min
			if paren {
				r.str("(")
			}
			r.expr(m.Receiver, op.prec)
			r.str(" ")
			r.anchor(x)
			r.str(op.sym + " ")
			// prec+1 on the right keeps the rendering left-associative,
			// exactly as the parser is.
			r.expr(x.Arguments[0], op.prec+1)
			if paren {
				r.str(")")
			}
			return
		}
		if sym, ok := unaryOps[m.Member.Name]; ok && len(x.Arguments) == 0 {
			paren := precUnary < min
			if paren {
				r.str("(")
			}
			r.anchor(x)
			r.str(sym)
			r.expr(m.Receiver, precUnary)
			if paren {
				r.str(")")
			}
			return
		}
		r.expr(m.Receiver, precPostfix)
		r.str(".")
		r.anchor(x)
		r.str(m.Member.Name)
	} else if id, ok := x.Callee.(*Identifier); ok {
		// A conversion (Level(50)): the name is a type, not a value.
		r.anchor(x)
		r.str(id.Name)
	} else {
		r.expr(x.Callee, precPostfix)
		r.anchor(x)
	}
	r.str("(")
	for i, a := range x.Arguments {
		if i > 0 {
			r.str(", ")
		}
		r.expr(a, 0)
	}
	r.str(")")
}

// funcLit renders a function literal on one line — statements joined by ";" —
// since the rendering is quoted inside a diagnostic message.
func (r *renderer) funcLit(x *FuncLit) {
	r.str("fn(")
	for i, p := range x.Params {
		if i > 0 {
			r.str(", ")
		}
		r.str(p.Name)
		if p.Type != nil {
			r.str(": " + dumpType(p.Type))
		}
	}
	r.str(")")
	if x.Result != nil {
		r.str(": " + dumpType(x.Result))
	}
	if len(x.Body) == 0 {
		r.str(" {}")
		return
	}
	r.str(" {")
	for i, s := range x.Body {
		if i > 0 {
			r.str(";")
		}
		switch s := s.(type) {
		case *ReturnStmt:
			r.str(" return ")
			r.expr(s.Value, 0)
		case *ExprStmt:
			r.str(" ")
			r.expr(s.X, 0)
		}
	}
	r.str(" }")
}
