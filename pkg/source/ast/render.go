package ast

import (
	"fmt"
	"strings"
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
func Render(e Expr) string { return render(e, 0) }

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

// render renders e, parenthesizing it when its own binding is looser than min
// — the binding the enclosing context requires.
func render(e Expr, min int) string {
	switch x := e.(type) {
	case nil:
		return "<missing>"
	case *IntLit:
		return x.Text
	case *StringLit:
		return fmt.Sprintf("%q", x.Value)
	case *BoolLit:
		if x.Value {
			return "true"
		}
		return "false"
	case *NullLit:
		return "null"
	case *SelfExpr:
		return "self"
	case *Identifier:
		return x.Name
	case *CollectionLit:
		parts := make([]string, len(x.Entries))
		for i, entry := range x.Entries {
			if entry.Key != nil {
				parts[i] = render(entry.Key, 0) + ": " + render(entry.Value, 0)
			} else {
				parts[i] = render(entry.Value, 0)
			}
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case *MemberExpr:
		return render(x.Receiver, precPostfix) + "." + x.Member.Name
	case *CallExpr:
		if m, ok := x.Callee.(*MemberExpr); ok {
			if op, ok := binaryOps[m.Member.Name]; ok && len(x.Arguments) == 1 {
				// prec+1 on the right keeps the rendering left-associative,
				// exactly as the parser is.
				s := render(m.Receiver, op.prec) + " " + op.sym + " " + render(x.Arguments[0], op.prec+1)
				if op.prec < min {
					return "(" + s + ")"
				}
				return s
			}
			if sym, ok := unaryOps[m.Member.Name]; ok && len(x.Arguments) == 0 {
				s := sym + render(m.Receiver, precUnary)
				if precUnary < min {
					return "(" + s + ")"
				}
				return s
			}
		}
		args := make([]string, len(x.Arguments))
		for i, a := range x.Arguments {
			args[i] = render(a, 0)
		}
		return render(x.Callee, precPostfix) + "(" + strings.Join(args, ", ") + ")"
	case *FuncLit:
		params := make([]string, len(x.Params))
		for i, p := range x.Params {
			params[i] = p.Name
			if p.Type != nil {
				params[i] += ": " + dumpType(p.Type)
			}
		}
		var b strings.Builder
		b.WriteString("fn(" + strings.Join(params, ", ") + ")")
		if x.Result != nil {
			b.WriteString(": " + dumpType(x.Result))
		}
		// The body renders on one line — statements joined by "; " — since the
		// rendering is quoted inside a single-line diagnostic message.
		if len(x.Body) == 0 {
			b.WriteString(" {}")
			return b.String()
		}
		b.WriteString(" {")
		for i, s := range x.Body {
			if i > 0 {
				b.WriteString(";")
			}
			switch s := s.(type) {
			case *ReturnStmt:
				b.WriteString(" return " + render(s.Value, 0))
			case *ExprStmt:
				b.WriteString(" " + render(s.X, 0))
			}
		}
		b.WriteString(" }")
		return b.String()
	default:
		return "<expr>"
	}
}
