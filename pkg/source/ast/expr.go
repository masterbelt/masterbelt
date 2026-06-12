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

// IntBase is the radix an integer literal was written in. It records the
// literal's spelling — the 0b/0o/0x prefix, or none — so a consumer such as a
// formatter can reproduce or normalize the prefix without re-parsing Text. The
// numeric value Text denotes is independent of it.
type IntBase int

// The integer-literal radices, one per accepted prefix; see each comment.
const (
	BaseDecimal IntBase = iota // no prefix, e.g. 100
	BaseBinary                 // 0b prefix, e.g. 0b1010
	BaseOctal                  // 0o prefix, e.g. 0o17
	BaseHex                    // 0x prefix, e.g. 0xFF
)

func (b IntBase) String() string {
	switch b {
	case BaseBinary:
		return "binary"
	case BaseOctal:
		return "octal"
	case BaseHex:
		return "hexadecimal"
	default:
		return "decimal"
	}
}

// IntLit is an integer literal. Its Text is the literal as written (including
// any 0b/0o/0x prefix) and Base records that radix; no numeric parsing or range
// checking happens at this layer.
type IntLit struct {
	Text   string
	Base   IntBase
	syntax *cst.Node
}

// Syntax returns the green CST node this literal was lowered from.
func (l *IntLit) Syntax() *cst.Node { return l.syntax }
func (l *IntLit) node()             {}
func (l *IntLit) expr()             {}

// NewIntLit builds an IntLit node of the given radix.
func NewIntLit(text string, base IntBase, syntax *cst.Node) *IntLit {
	return &IntLit{Text: text, Base: base, syntax: syntax}
}

// DatetimeLit is a datetime literal: a D-prefixed ISO-8601 instant
// (D2009-03-31T23:59:59.000Z, offsets allowed). Its Text is the literal as
// written; the normalization to a UTC epoch-millisecond value happens where
// the constant folds, exactly as IntLit's numeric parsing does.
type DatetimeLit struct {
	Text   string
	syntax *cst.Node
}

// Syntax returns the green CST node this literal was lowered from.
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

// Syntax returns the green CST node this literal was lowered from.
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

// Syntax returns the green CST node this literal was lowered from.
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

// Syntax returns the green CST node this literal was lowered from.
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

// Syntax returns the green CST node this literal was lowered from.
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

// Syntax returns the green CST node this literal was lowered from.
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

// Syntax returns the green CST node this field initializer was lowered from.
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

// Syntax returns the green CST node this literal was lowered from.
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

// Syntax returns the green CST node this expression was lowered from.
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

// Syntax returns the green CST node this identifier was lowered from.
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

// Syntax returns the green CST node this expression was lowered from.
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

// Syntax returns the green CST node this expression was lowered from.
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

// Syntax returns the green CST node this expression was lowered from.
func (a *AwaitExpr) Syntax() *cst.Node { return a.syntax }
func (a *AwaitExpr) node()             {}
func (a *AwaitExpr) expr()             {}

// NewAwaitExpr builds an AwaitExpr node.
func NewAwaitExpr(value Expr, syntax *cst.Node) *AwaitExpr {
	return &AwaitExpr{Value: value, syntax: syntax}
}

// TernaryExpr is a conditional value expression, cond ? then : else: it yields
// Then when Cond holds and Else otherwise. Unlike an if (a statement, which
// drives control flow by returning), the ternary is an expression — it produces
// a value, so it appears wherever a value is wanted. It is kept as its own node
// rather than desugared to a method call: only the taken branch is evaluated, a
// laziness a uniform call could not express. Any of the three is nil when the
// source omitted it and the parser recovered.
type TernaryExpr struct {
	Cond   Expr // the boolean condition
	Then   Expr // the value when the condition holds
	Else   Expr // the value otherwise
	syntax *cst.Node
}

// Syntax returns the green CST node this expression was lowered from.
func (t *TernaryExpr) Syntax() *cst.Node { return t.syntax }
func (t *TernaryExpr) node()             {}
func (t *TernaryExpr) expr()             {}

// NewTernaryExpr builds a TernaryExpr node.
func NewTernaryExpr(cond, then, els Expr, syntax *cst.Node) *TernaryExpr {
	return &TernaryExpr{Cond: cond, Then: then, Else: els, syntax: syntax}
}

// RangeExpr is a range literal: the surface syntax for the range builtin. Lower
// and Upper are the two bound expressions as written — any integer expression —
// and HalfOpen distinguishes the half-open "..." form (the larger end excluded)
// from the closed ".." form (both ends included). It keeps its own node rather
// than desugaring to a range(...) call: the direction (ascending or descending)
// and the half-open trim both depend on the bound *values*, which only the
// evaluator knows, so the desugaring to range(start, end, step) is deferred to
// the fold — a literal whose bounds fold matches the range(...) it equals
// exactly. Either bound is nil when the source omitted it and the parser
// recovered.
type RangeExpr struct {
	Lower    Expr // the bound on the left of the operator
	Upper    Expr // the bound on the right of the operator
	HalfOpen bool // the "..." form (max excluded); false for the closed ".." form
	syntax   *cst.Node
}

// Syntax returns the green CST node this expression was lowered from.
func (r *RangeExpr) Syntax() *cst.Node { return r.syntax }
func (r *RangeExpr) node()             {}
func (r *RangeExpr) expr()             {}

// NewRangeExpr builds a RangeExpr node.
func NewRangeExpr(lower, upper Expr, halfOpen bool, syntax *cst.Node) *RangeExpr {
	return &RangeExpr{Lower: lower, Upper: upper, HalfOpen: halfOpen, syntax: syntax}
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

// Syntax returns the green CST node this literal was lowered from.
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
	case *TernaryExpr:
		WalkExprs(e.Cond, fn)
		WalkExprs(e.Then, fn)
		WalkExprs(e.Else, fn)
	case *RangeExpr:
		WalkExprs(e.Lower, fn)
		WalkExprs(e.Upper, fn)
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

// WalkBodyExprs calls fn for every top-level expression of a statement body —
// a return value, an expression statement, a let initializer, an assignment's
// target and value, a switch's scrutinee and arm value patterns, and an if's
// condition — descending (recursively) through the statement bodies a switch
// and an if introduce, so a walk over a body reaches every expression in its
// control flow. It yields the top expression of each statement; the caller
// descends into an expression's operands with WalkExprs (or its own recursion,
// e.g. one that also enters function-literal bodies).
//
// This is the one statement-walk skeleton the editor and the semantic layer
// share, so a new statement form is wired into every walk by adding its
// expressions here, exactly once — the companion of WalkExprs for statements.
func WalkBodyExprs(body []Stmt, fn func(Expr)) {
	for _, stmt := range body {
		// A flat dispatch over the exhaustive statement set: every Stmt
		// implementer has a case, and a new form reaches the default panic
		// rather than being silently dropped from this shared skeleton (and so
		// from every walk layered on it). Each non-trivial case delegates to a
		// per-statement-kind helper so the dispatch stays readable.
		switch stmt := stmt.(type) {
		case *ReturnStmt:
			if stmt.Value != nil {
				fn(stmt.Value)
			}
		case *ExprStmt:
			if stmt.X != nil {
				fn(stmt.X)
			}
		case *LetStmt:
			if stmt.Value != nil {
				fn(stmt.Value)
			}
		case *AssignStmt:
			walkAssignExprs(stmt, fn)
		case *SwitchStmt:
			walkSwitchExprs(stmt, fn)
		case *MatchStmt:
			walkMatchExprs(stmt, fn)
		case *IfStmt:
			walkIfExprs(stmt, fn)
		case *ForStmt:
			if stmt.Iter != nil {
				fn(stmt.Iter)
			}
			WalkBodyExprs(stmt.Body, fn)
		default:
			// Every Stmt implementer must have a case above. A new statement
			// form reaches here and panics rather than being silently dropped
			// from this shared skeleton (and so from every walk layered on it).
			panic(unhandledStmt(stmt))
		}
	}
}

// walkAssignExprs calls fn for an assignment's target and value — the target as
// well, so a member-access target (self.x = ...), which the parser accepts, is
// reached too.
func walkAssignExprs(stmt *AssignStmt, fn func(Expr)) {
	if stmt.Target != nil {
		fn(stmt.Target)
	}
	if stmt.Value != nil {
		fn(stmt.Value)
	}
}

// walkSwitchExprs calls fn for a switch's scrutinee, each arm's value patterns,
// and (recursively) the expressions of every arm body, the else body, and the
// after-else arms.
func walkSwitchExprs(stmt *SwitchStmt, fn func(Expr)) {
	if stmt.Scrutinee != nil {
		fn(stmt.Scrutinee)
	}
	for _, arm := range stmt.Arms {
		for _, v := range arm.Values {
			fn(v)
		}
		WalkBodyExprs(arm.Body, fn)
	}
	WalkBodyExprs(stmt.Else, fn)
	for _, arm := range stmt.AfterElse {
		for _, v := range arm.Values {
			fn(v)
		}
		WalkBodyExprs(arm.Body, fn)
	}
}

// walkMatchExprs calls fn for a match's scrutinee and (recursively) the
// expressions of every arm body, the else body, and the after-else arms.
func walkMatchExprs(stmt *MatchStmt, fn func(Expr)) {
	if stmt.Scrutinee != nil {
		fn(stmt.Scrutinee)
	}
	for _, arm := range stmt.Arms {
		WalkBodyExprs(arm.Body, fn)
	}
	WalkBodyExprs(stmt.Else, fn)
	for _, arm := range stmt.AfterElse {
		WalkBodyExprs(arm.Body, fn)
	}
}

// walkIfExprs calls fn for every top-level expression of an if statement — its
// condition and (recursively) the top expressions of its then body, its
// else-if chain, and its else body.
func walkIfExprs(s *IfStmt, fn func(Expr)) {
	if s.Cond != nil {
		fn(s.Cond)
	}
	WalkBodyExprs(s.Then, fn)
	if s.ElseIf != nil {
		walkIfExprs(s.ElseIf, fn)
	}
	WalkBodyExprs(s.Else, fn)
}
