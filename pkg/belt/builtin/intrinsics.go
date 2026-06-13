// This file holds the native implementations of the primitives' extern
// methods: the Intrinsic type and its dispatch entry, the factories that
// build the integer, boolean, string, datetime, and duration intrinsics from
// typed operand functions, and the per-type implementation maps.

package builtin

import (
	"math"
	"math/big"
	"unicode/utf8"

	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// Intrinsic is the native implementation of an extern method: it computes the
// method's value from the receiver and argument constants (all guaranteed
// non-nil by the caller), or returns nil when the operation has no value — a
// type-incorrect program, or a division by zero.
type Intrinsic func(recv *ir.Constant, args []*ir.Constant) *ir.Constant

// intrinsicEntry is one native implementation of an extern method: the
// function and the argument-kind signature it dispatches on. A nil kinds list
// marks a kind-agnostic implementation — the match for any arguments when no
// exact signature claims them (every un-overloaded method registers this way).
type intrinsicEntry struct {
	kinds []ir.ConstKind
	fn    Intrinsic
}

// unaryInt is a nullary-argument integer intrinsic (pos, neg).
func unaryInt(f func(a *big.Int) *ir.Constant) Intrinsic {
	return func(r *ir.Constant, args []*ir.Constant) *ir.Constant {
		if len(args) != 0 || r.Kind != ir.ConstInt {
			return nil
		}
		return f(r.Int)
	}
}

// binaryInt is a one-argument integer intrinsic over two integer operands.
func binaryInt(f func(a, b *big.Int) *ir.Constant) Intrinsic {
	return func(r *ir.Constant, args []*ir.Constant) *ir.Constant {
		if len(args) != 1 || r.Kind != ir.ConstInt || args[0].Kind != ir.ConstInt {
			return nil
		}
		return f(r.Int, args[0].Int)
	}
}

// binaryBool is a one-argument boolean intrinsic over two boolean operands.
func binaryBool(f func(a, b bool) *ir.Constant) Intrinsic {
	return func(r *ir.Constant, args []*ir.Constant) *ir.Constant {
		if len(args) != 1 || r.Kind != ir.ConstBool || args[0].Kind != ir.ConstBool {
			return nil
		}
		return f(r.Bool, args[0].Bool)
	}
}

// binaryStr is a one-argument string intrinsic over two string operands.
func binaryStr(f func(a, b string) *ir.Constant) Intrinsic {
	return func(r *ir.Constant, args []*ir.Constant) *ir.Constant {
		if len(args) != 1 || r.Kind != ir.ConstString || args[0].Kind != ir.ConstString {
			return nil
		}
		return f(r.Str, args[0].Str)
	}
}

func integerIntrinsics() map[string]Intrinsic {
	return map[string]Intrinsic{
		"pos": unaryInt(func(a *big.Int) *ir.Constant { return ir.IntConstant(new(big.Int).Set(a)) }),
		"neg": unaryInt(func(a *big.Int) *ir.Constant { return ir.IntConstant(new(big.Int).Neg(a)) }),
		"add": binaryInt(func(a, b *big.Int) *ir.Constant { return ir.IntConstant(new(big.Int).Add(a, b)) }),
		"sub": binaryInt(func(a, b *big.Int) *ir.Constant { return ir.IntConstant(new(big.Int).Sub(a, b)) }),
		"mul": binaryInt(func(a, b *big.Int) *ir.Constant { return ir.IntConstant(new(big.Int).Mul(a, b)) }),
		"div": binaryInt(func(a, b *big.Int) *ir.Constant {
			if b.Sign() == 0 {
				return nil
			}
			return ir.IntConstant(new(big.Int).Quo(a, b))
		}),
		"rem": binaryInt(func(a, b *big.Int) *ir.Constant {
			if b.Sign() == 0 {
				return nil
			}
			return ir.IntConstant(new(big.Int).Rem(a, b))
		}),
		OpEql:  binaryInt(func(a, b *big.Int) *ir.Constant { return ir.BoolConstant(a.Cmp(b) == 0) }),
		OpNeq:  binaryInt(func(a, b *big.Int) *ir.Constant { return ir.BoolConstant(a.Cmp(b) != 0) }),
		OpLt:   binaryInt(func(a, b *big.Int) *ir.Constant { return ir.BoolConstant(a.Cmp(b) < 0) }),
		OpLteq: binaryInt(func(a, b *big.Int) *ir.Constant { return ir.BoolConstant(a.Cmp(b) <= 0) }),
		OpGt:   binaryInt(func(a, b *big.Int) *ir.Constant { return ir.BoolConstant(a.Cmp(b) > 0) }),
		OpGteq: binaryInt(func(a, b *big.Int) *ir.Constant { return ir.BoolConstant(a.Cmp(b) >= 0) }),
	}
}

func booleanIntrinsics() map[string]Intrinsic {
	return map[string]Intrinsic{
		"not":  notBool,
		"anan": binaryBool(func(a, b bool) *ir.Constant { return ir.BoolConstant(a && b) }),
		"oror": binaryBool(func(a, b bool) *ir.Constant { return ir.BoolConstant(a || b) }),
		OpEql:  binaryBool(func(a, b bool) *ir.Constant { return ir.BoolConstant(a == b) }),
		OpNeq:  binaryBool(func(a, b bool) *ir.Constant { return ir.BoolConstant(a != b) }),
	}
}

func notBool(r *ir.Constant, args []*ir.Constant) *ir.Constant {
	if len(args) != 0 || r.Kind != ir.ConstBool {
		return nil
	}
	return ir.BoolConstant(!r.Bool)
}

// errorIntrinsics evaluates the error methods: message yields the message the
// error value carries.
func errorIntrinsics() map[string]Intrinsic {
	return map[string]Intrinsic{
		"message": func(r *ir.Constant, args []*ir.Constant) *ir.Constant {
			if len(args) != 0 || r.Kind != ir.ConstError {
				return nil
			}
			return ir.StringConstant(r.Str)
		},
	}
}

// stringIntrinsics evaluates the string operators and the introspection
// substrate. add concatenates and the comparisons use Go's lexicographic byte
// ordering; len/at/slice/chars/bytes read the contents — by Unicode codepoint
// (rune) for the first four, by UTF-8 byte for the last — so the std string
// module's split/trim/case folding can be written in pure belt on top of them.
func stringIntrinsics() map[string]Intrinsic {
	return map[string]Intrinsic{
		"add":   binaryStr(func(a, b string) *ir.Constant { return ir.StringConstant(a + b) }),
		"len":   stringLen,
		"at":    stringAt,
		"slice": stringSlice,
		"chars": stringChars,
		"bytes": stringBytes,
		OpEql:   binaryStr(func(a, b string) *ir.Constant { return ir.BoolConstant(a == b) }),
		OpNeq:   binaryStr(func(a, b string) *ir.Constant { return ir.BoolConstant(a != b) }),
		OpLt:    binaryStr(func(a, b string) *ir.Constant { return ir.BoolConstant(a < b) }),
		OpLteq:  binaryStr(func(a, b string) *ir.Constant { return ir.BoolConstant(a <= b) }),
		OpGt:    binaryStr(func(a, b string) *ir.Constant { return ir.BoolConstant(a > b) }),
		OpGteq:  binaryStr(func(a, b string) *ir.Constant { return ir.BoolConstant(a >= b) }),
	}
}

// stringLen folds string.len() to the number of Unicode codepoints (runes) —
// the rune count, not the byte length, so a multi-byte character counts once.
func stringLen(r *ir.Constant, args []*ir.Constant) *ir.Constant {
	if len(args) != 0 || r.Kind != ir.ConstString {
		return nil
	}
	return ir.IntConstant(big.NewInt(int64(utf8.RuneCountInString(r.Str))))
}

// stringAt folds string.at(i) to the i-th rune (0-based) as a length-1 string,
// or to an index-out-of-range error when i falls outside the rune sequence — a
// read can miss, so the result is a value to branch on (string | error), the
// same shape a list's get carries.
func stringAt(r *ir.Constant, args []*ir.Constant) *ir.Constant {
	if len(args) != 1 || r.Kind != ir.ConstString || args[0].Kind != ir.ConstInt {
		return nil
	}
	runes := []rune(r.Str)
	if !args[0].Int.IsInt64() {
		return ir.ErrorConstant("index out of range")
	}
	i := args[0].Int.Int64()
	if i < 0 || i >= int64(len(runes)) {
		return ir.ErrorConstant("index out of range")
	}
	return ir.StringConstant(string(runes[i]))
}

// stringSlice folds string.slice(start, end) to the substring of the runes in
// [start, end) — 0-based, half-open — or to an index-out-of-range error when a
// bound falls outside the sequence or start is past end. It carries the same
// fallible-read shape at does (string | error).
func stringSlice(r *ir.Constant, args []*ir.Constant) *ir.Constant {
	if len(args) != 2 || r.Kind != ir.ConstString || args[0].Kind != ir.ConstInt || args[1].Kind != ir.ConstInt {
		return nil
	}
	if !args[0].Int.IsInt64() || !args[1].Int.IsInt64() {
		return ir.ErrorConstant("index out of range")
	}
	runes := []rune(r.Str)
	n := int64(len(runes))
	start, end := args[0].Int.Int64(), args[1].Int.Int64()
	if start < 0 || end > n || start > end {
		return ir.ErrorConstant("index out of range")
	}
	return ir.StringConstant(string(runes[start:end]))
}

// stringChars folds string.chars() to the list of the string's runes, each a
// length-1 string in order — the decode the std string module's split, trim,
// and case folding are built on.
func stringChars(r *ir.Constant, args []*ir.Constant) *ir.Constant {
	if len(args) != 0 || r.Kind != ir.ConstString {
		return nil
	}
	runes := []rune(r.Str)
	out := make([]ir.ConstEntry, len(runes))
	for i, c := range runes {
		out[i] = ir.ConstEntry{Value: ir.StringConstant(string(c))}
	}
	return ir.CollectionConstantOf(out, ir.CollList)
}

// stringBytes folds string.bytes() to the list of the string's UTF-8 bytes, in
// order.
func stringBytes(r *ir.Constant, args []*ir.Constant) *ir.Constant {
	if len(args) != 0 || r.Kind != ir.ConstString {
		return nil
	}
	bs := []byte(r.Str)
	out := make([]ir.ConstEntry, len(bs))
	for i, b := range bs {
		out[i] = ir.ConstEntry{Value: ir.IntConstant(big.NewInt(int64(b)))}
	}
	return ir.CollectionConstantOf(out, ir.CollList)
}

// binaryMillis is a one-argument intrinsic over two millisecond-carrying
// operands of the given kinds — the building block of the datetime/duration
// operators, whose overloads differ exactly in the argument's kind.
func binaryMillis(recvKind, argKind ir.ConstKind, f func(a, b int64) *ir.Constant) Intrinsic {
	return func(r *ir.Constant, args []*ir.Constant) *ir.Constant {
		if len(args) != 1 || r.Kind != recvKind || args[0].Kind != argKind {
			return nil
		}
		return f(r.Millis, args[0].Millis)
	}
}

// addMillis sums two millisecond values, reporting false on int64 overflow —
// the operation then has no value, like a division by zero.
func addMillis(a, b int64) (int64, bool) {
	c := a + b
	if (b > 0 && c < a) || (b < 0 && c > a) {
		return 0, false
	}
	return c, true
}

// subMillis subtracts two millisecond values, reporting false on overflow.
func subMillis(a, b int64) (int64, bool) {
	c := a - b
	if (b < 0 && c < a) || (b > 0 && c > a) {
		return 0, false
	}
	return c, true
}

// checkedMillis composes an overflow-checked millisecond operation with the
// constructor of its result kind: the intrinsic body of every datetime and
// duration arithmetic overload.
func checkedMillis(op func(a, b int64) (int64, bool), build func(int64) *ir.Constant) func(a, b int64) *ir.Constant {
	return func(a, b int64) *ir.Constant {
		v, ok := op(a, b)
		if !ok {
			return nil
		}
		return build(v)
	}
}

// millisComparisons are the comparison intrinsics shared by datetime and
// duration: both order by their millisecond value.
func millisComparisons(kind ir.ConstKind) map[string]Intrinsic {
	return map[string]Intrinsic{
		OpEql:  binaryMillis(kind, kind, func(a, b int64) *ir.Constant { return ir.BoolConstant(a == b) }),
		OpNeq:  binaryMillis(kind, kind, func(a, b int64) *ir.Constant { return ir.BoolConstant(a != b) }),
		OpLt:   binaryMillis(kind, kind, func(a, b int64) *ir.Constant { return ir.BoolConstant(a < b) }),
		OpLteq: binaryMillis(kind, kind, func(a, b int64) *ir.Constant { return ir.BoolConstant(a <= b) }),
		OpGt:   binaryMillis(kind, kind, func(a, b int64) *ir.Constant { return ir.BoolConstant(a > b) }),
		OpGteq: binaryMillis(kind, kind, func(a, b int64) *ir.Constant { return ir.BoolConstant(a >= b) }),
	}
}

// mulDuration scales a duration by an integer constant. A factor outside
// int64, or a product that overflows, has no value.
func mulDuration(r *ir.Constant, args []*ir.Constant) *ir.Constant {
	if len(args) != 1 || r.Kind != ir.ConstDuration || args[0].Kind != ir.ConstInt {
		return nil
	}
	if !args[0].Int.IsInt64() {
		return nil
	}
	n := args[0].Int.Int64()
	if n != 0 && (r.Millis == math.MinInt64 && n == -1 || n == math.MinInt64 && r.Millis == -1) {
		return nil // the one product the division check below cannot probe
	}
	product := r.Millis * n
	if n != 0 && product/n != r.Millis {
		return nil
	}
	return ir.DurationConstant(product)
}
