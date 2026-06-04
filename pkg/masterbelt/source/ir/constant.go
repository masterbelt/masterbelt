package ir

import (
	"math/big"
	"strconv"
	"strings"
)

// ConstKind distinguishes the kinds of evaluated constant value.
type ConstKind int

const (
	ConstInt        ConstKind = iota // an arbitrary-precision integer (Constant.Int)
	ConstBool                        // a boolean (Constant.Bool)
	ConstString                      // a string (Constant.Str)
	ConstCollection                  // a list or map (Constant.Coll)
)

// Constant is the evaluated value of a constant expression: an arbitrary-
// precision integer, a boolean, a string, or a collection (list/map). A nil
// *Constant means "could not be evaluated" — a missing initializer, an undefined
// reference, a cycle, a type error, or a division by zero.
type Constant struct {
	Kind ConstKind
	Int  *big.Int     // valid when Kind == ConstInt
	Bool bool         // valid when Kind == ConstBool
	Str  string       // valid when Kind == ConstString
	Coll []ConstEntry // valid when Kind == ConstCollection
}

// ConstEntry is one entry of a folded collection constant: a Value, and for a
// map entry a Key (nil for a list element).
type ConstEntry struct {
	Key   *Constant // nil for a list element
	Value *Constant
}

// IntConstant builds an integer constant.
func IntConstant(n *big.Int) *Constant { return &Constant{Kind: ConstInt, Int: n} }

// BoolConstant builds a boolean constant.
func BoolConstant(b bool) *Constant { return &Constant{Kind: ConstBool, Bool: b} }

// StringConstant builds a string constant.
func StringConstant(s string) *Constant { return &Constant{Kind: ConstString, Str: s} }

// CollectionConstant builds a collection constant from its entries. An empty
// slice is the empty list/map; a list's entries have a nil Key.
func CollectionConstant(entries []ConstEntry) *Constant {
	return &Constant{Kind: ConstCollection, Coll: entries}
}

// String renders the constant's value: the integer, "true"/"false", the quoted
// string, or the bracketed collection ([a, b] for a list, ["k": v] for a map).
func (c *Constant) String() string {
	if c == nil {
		return "<unevaluated>"
	}
	switch c.Kind {
	case ConstBool:
		return strconv.FormatBool(c.Bool)
	case ConstString:
		return strconv.Quote(c.Str)
	case ConstCollection:
		parts := make([]string, len(c.Coll))
		for i, e := range c.Coll {
			if e.Key != nil {
				parts[i] = e.Key.String() + ": " + e.Value.String()
			} else {
				parts[i] = e.Value.String()
			}
		}
		return "[" + strings.Join(parts, ", ") + "]"
	default:
		return c.Int.String()
	}
}
