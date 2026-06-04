package ir

import (
	"math/big"
	"strconv"
)

// ConstKind distinguishes the kinds of evaluated constant value.
type ConstKind int

const (
	ConstInt    ConstKind = iota // an arbitrary-precision integer (Constant.Int)
	ConstBool                    // a boolean (Constant.Bool)
	ConstString                  // a string (Constant.Str)
)

// Constant is the evaluated value of a constant expression: an arbitrary-
// precision integer, a boolean, or a string. A nil *Constant means "could not
// be evaluated" — a missing initializer, an undefined reference, a cycle, a type
// error, or a division by zero.
type Constant struct {
	Kind ConstKind
	Int  *big.Int // valid when Kind == ConstInt
	Bool bool     // valid when Kind == ConstBool
	Str  string   // valid when Kind == ConstString
}

// IntConstant builds an integer constant.
func IntConstant(n *big.Int) *Constant { return &Constant{Kind: ConstInt, Int: n} }

// BoolConstant builds a boolean constant.
func BoolConstant(b bool) *Constant { return &Constant{Kind: ConstBool, Bool: b} }

// StringConstant builds a string constant.
func StringConstant(s string) *Constant { return &Constant{Kind: ConstString, Str: s} }

// String renders the constant's value: the integer, "true"/"false", or the
// quoted string.
func (c *Constant) String() string {
	if c == nil {
		return "<unevaluated>"
	}
	switch c.Kind {
	case ConstBool:
		return strconv.FormatBool(c.Bool)
	case ConstString:
		return strconv.Quote(c.Str)
	default:
		return c.Int.String()
	}
}
