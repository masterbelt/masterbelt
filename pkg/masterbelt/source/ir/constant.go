package ir

import (
	"math/big"
	"strconv"
)

// ConstKind distinguishes the two kinds of evaluated constant value.
type ConstKind int

const (
	ConstInt  ConstKind = iota // an arbitrary-precision integer (Constant.Int)
	ConstBool                  // a boolean (Constant.Bool)
)

// Constant is the evaluated value of a constant expression: an arbitrary-
// precision integer or a boolean. A nil *Constant means "could not be
// evaluated" — a missing initializer, an undefined reference, a cycle, a type
// error, or a division by zero.
type Constant struct {
	Kind ConstKind
	Int  *big.Int // valid when Kind == ConstInt
	Bool bool     // valid when Kind == ConstBool
}

// IntConstant builds an integer constant.
func IntConstant(n *big.Int) *Constant { return &Constant{Kind: ConstInt, Int: n} }

// BoolConstant builds a boolean constant.
func BoolConstant(b bool) *Constant { return &Constant{Kind: ConstBool, Bool: b} }

// String renders the constant's value: the integer, or "true"/"false".
func (c *Constant) String() string {
	if c == nil {
		return "<unevaluated>"
	}
	if c.Kind == ConstBool {
		return strconv.FormatBool(c.Bool)
	}
	return c.Int.String()
}
