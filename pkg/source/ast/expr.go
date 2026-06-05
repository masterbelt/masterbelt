package ast

import "github.com/masterbelt/masterbelt/pkg/source/cst"

// Expr is an initializer expression: a literal (IntLit/StringLit/BoolLit/
// NullLit), an identifier (Identifier), a member access (MemberExpr), or a call
// (CallExpr). Operators desugar to method calls — 1 + 2 becomes 1.add(2) — so no
// operator node here.
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

// DatetimeLit is a datetime literal: a D-prefixed ISO-8601 instant
// (D2009-03-31T23:59:59.000Z, offsets allowed). Its Text is the literal as
// written; the normalization to a UTC epoch-millisecond value happens where
// the constant folds, exactly as IntLit's numeric parsing does.
type DatetimeLit struct {
	Text   string
	syntax *cst.Node
}

func (l *DatetimeLit) Syntax() *cst.Node { return l.syntax }
func (l *DatetimeLit) node()             {}
func (l *DatetimeLit) expr()             {}

// NewDatetimeLit builds a DatetimeLit node.
func NewDatetimeLit(text string, syntax *cst.Node) *DatetimeLit {
	return &DatetimeLit{Text: text, syntax: syntax}
}

// DurationLit is a duration literal: concatenated digit+unit groups
// (3w4d5h6m7s8ms). Its Text is the literal as written; the totalling into
// milliseconds happens where the constant folds.
type DurationLit struct {
	Text   string
	syntax *cst.Node
}

func (l *DurationLit) Syntax() *cst.Node { return l.syntax }
func (l *DurationLit) node()             {}
func (l *DurationLit) expr()             {}

// NewDurationLit builds a DurationLit node.
func NewDurationLit(text string, syntax *cst.Node) *DurationLit {
	return &DurationLit{Text: text, syntax: syntax}
}

// StringLit is a string literal. Value is the decoded string — the surrounding
// quotes removed and the escape sequences (\n \r \t \0 \\ \" and \u{...})
// interpreted — so it holds the literal's actual value rather than its source
// spelling. (Contrast IntLit, which keeps its raw text because an integer's
// representation depends on the type it flows into; a string's value does not.)
type StringLit struct {
	Value  string
	syntax *cst.Node
}

func (l *StringLit) Syntax() *cst.Node { return l.syntax }
func (l *StringLit) node()             {}
func (l *StringLit) expr()             {}

// NewStringLit builds a StringLit node.
func NewStringLit(value string, syntax *cst.Node) *StringLit {
	return &StringLit{Value: value, syntax: syntax}
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

// CollectionLit is a list or map literal. A list's entries each carry only a
// Value; a map's entries each carry a Key and a Value. An empty literal has no
// entries, so its kind (list vs map) is not fixed by the syntax — the type
// checker resolves it from the annotation. The parser never mixes the two forms,
// so either every entry has a Key or none does.
type CollectionLit struct {
	Entries []*CollectionEntry
	syntax  *cst.Node
}

func (l *CollectionLit) Syntax() *cst.Node { return l.syntax }
func (l *CollectionLit) node()             {}
func (l *CollectionLit) expr()             {}

// IsMap reports whether the literal is a map (its entries have keys). An empty
// literal is not a map (nor a list); its kind comes from the annotation.
func (l *CollectionLit) IsMap() bool {
	return len(l.Entries) > 0 && l.Entries[0].Key != nil
}

// NewCollectionLit builds a CollectionLit node.
func NewCollectionLit(entries []*CollectionEntry, syntax *cst.Node) *CollectionLit {
	return &CollectionLit{Entries: entries, syntax: syntax}
}

// CollectionEntry is one entry of a collection literal: a Value, and for a map a
// Key (nil for a list element). Either component may be nil when the source was
// malformed and the parser recovered.
type CollectionEntry struct {
	Key   Expr // nil for a list element
	Value Expr
}

// RecordLit is a record literal: the value form of a record type. The typed
// form names the type it builds (Point{ x: 1 }, TypeName "Point"); the
// inferred form ({ x: 1 }, TypeName "") leaves the type to the checking
// context, exactly as an empty collection takes its kind from the annotation.
type RecordLit struct {
	TypeName string       // the named record type, or "" for the inferred form
	Fields   []*FieldInit // the field initializers, in source order
	syntax   *cst.Node
}

func (l *RecordLit) Syntax() *cst.Node { return l.syntax }
func (l *RecordLit) node()             {}
func (l *RecordLit) expr()             {}

// NewRecordLit builds a RecordLit node.
func NewRecordLit(typeName string, fields []*FieldInit, syntax *cst.Node) *RecordLit {
	return &RecordLit{TypeName: typeName, Fields: fields, syntax: syntax}
}

// FieldInit is one field initializer of a record literal: a name and its
// value. Value is nil when the source was malformed and the parser recovered.
type FieldInit struct {
	Name   string
	Value  Expr
	syntax *cst.Node
}

func (f *FieldInit) Syntax() *cst.Node { return f.syntax }
func (f *FieldInit) node()             {}

// NewFieldInit builds a FieldInit node.
func NewFieldInit(name string, value Expr, syntax *cst.Node) *FieldInit {
	return &FieldInit{Name: name, Value: value, syntax: syntax}
}

// NullLit is the null literal.
type NullLit struct {
	syntax *cst.Node
}

func (l *NullLit) Syntax() *cst.Node { return l.syntax }
func (l *NullLit) node()             {}
func (l *NullLit) expr()             {}

// NewNullLit builds a NullLit node.
func NewNullLit(syntax *cst.Node) *NullLit {
	return &NullLit{syntax: syntax}
}

// SelfExpr is the self receiver inside a method body.
type SelfExpr struct {
	syntax *cst.Node
}

func (s *SelfExpr) Syntax() *cst.Node { return s.syntax }
func (s *SelfExpr) node()             {}
func (s *SelfExpr) expr()             {}

// NewSelfExpr builds a SelfExpr node.
func NewSelfExpr(syntax *cst.Node) *SelfExpr {
	return &SelfExpr{syntax: syntax}
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

// AwaitExpr is an await expression: the explicit suspension point that
// consumes the async effect at a call site (await fetch(url)). It carries the
// awaited value and adds nothing to its type.
type AwaitExpr struct {
	Value  Expr // the awaited expression (nil if recovered away)
	syntax *cst.Node
}

func (a *AwaitExpr) Syntax() *cst.Node { return a.syntax }
func (a *AwaitExpr) node()             {}
func (a *AwaitExpr) expr()             {}

// NewAwaitExpr builds an AwaitExpr node.
func NewAwaitExpr(value Expr, syntax *cst.Node) *AwaitExpr {
	return &AwaitExpr{Value: value, syntax: syntax}
}

// FuncLit is a function-literal expression: fn(Params): Result { Body }. It is
// the value form of a FuncType (it carries a statement body) and the only way to
// construct a value of a function type. Its Params, Result, and Body reuse the
// same nodes a method declaration is built from.
type FuncLit struct {
	Params []*ParamDef
	Result TypeExpr // the declared result type, or nil if missing
	Body   []Stmt   // the statement body
	syntax *cst.Node
}

func (l *FuncLit) Syntax() *cst.Node { return l.syntax }
func (l *FuncLit) node()             {}
func (l *FuncLit) expr()             {}

// NewFuncLit builds a FuncLit node.
func NewFuncLit(params []*ParamDef, result TypeExpr, body []Stmt, syntax *cst.Node) *FuncLit {
	return &FuncLit{Params: params, Result: result, Body: body, syntax: syntax}
}

// WalkExprs visits every expression node of e in pre-order — the node itself,
// then its operands: a member access's receiver, a call's callee and
// arguments, a collection's keys and values. The callback may return false to
// skip a node's operands. It never descends into a FuncLit body: a lambda
// introduces its own parameter scope, so its inner expressions are not part
// of the enclosing expression's reference structure.
//
// This is the one traversal skeleton — name resolution and the editor's
// occurrence walks all layer on it — so a new expression form is wired into
// every walk by adding its operands here, exactly once.
func WalkExprs(e Expr, fn func(Expr) bool) {
	if e == nil || !fn(e) {
		return
	}
	switch e := e.(type) {
	case *MemberExpr:
		WalkExprs(e.Receiver, fn)
	case *CallExpr:
		WalkExprs(e.Callee, fn)
		for _, a := range e.Arguments {
			WalkExprs(a, fn)
		}
	case *AwaitExpr:
		WalkExprs(e.Value, fn)
	case *CollectionLit:
		for _, entry := range e.Entries {
			if entry.Key != nil {
				WalkExprs(entry.Key, fn)
			}
			if entry.Value != nil {
				WalkExprs(entry.Value, fn)
			}
		}
	case *RecordLit:
		// Only the field values are expressions; the field names name the
		// record's fields, and the type name names a type, not a value.
		for _, field := range e.Fields {
			if field.Value != nil {
				WalkExprs(field.Value, fn)
			}
		}
	}
}

// WalkValueIdents calls fn for every value-position identifier in e — the
// operands of the expression — but not the member names operators desugared to
// (those are method names, not references to declarations). It is how name
// resolution and the editor reach the references inside an expression.
func WalkValueIdents(e Expr, fn func(*Identifier)) {
	WalkExprs(e, func(e Expr) bool {
		if id, ok := e.(*Identifier); ok {
			fn(id)
		}
		return true
	})
}
