// This file holds the native implementations of the primitives' extern
// methods: the Intrinsic type and its dispatch entry, the factories that
// build the integer, boolean, string, datetime, and duration intrinsics from
// typed operand functions, and the per-type implementation maps.
package builtin

import (
	"math"
	"math/big"

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
		"eql":  binaryInt(func(a, b *big.Int) *ir.Constant { return ir.BoolConstant(a.Cmp(b) == 0) }),
		"neq":  binaryInt(func(a, b *big.Int) *ir.Constant { return ir.BoolConstant(a.Cmp(b) != 0) }),
		"lt":   binaryInt(func(a, b *big.Int) *ir.Constant { return ir.BoolConstant(a.Cmp(b) < 0) }),
		"lteq": binaryInt(func(a, b *big.Int) *ir.Constant { return ir.BoolConstant(a.Cmp(b) <= 0) }),
		"gt":   binaryInt(func(a, b *big.Int) *ir.Constant { return ir.BoolConstant(a.Cmp(b) > 0) }),
		"gteq": binaryInt(func(a, b *big.Int) *ir.Constant { return ir.BoolConstant(a.Cmp(b) >= 0) }),
	}
}

func booleanIntrinsics() map[string]Intrinsic {
	return map[string]Intrinsic{
		"not":  func(r *ir.Constant, args []*ir.Constant) *ir.Constant { return notBool(r, args) },
		"anan": binaryBool(func(a, b bool) *ir.Constant { return ir.BoolConstant(a && b) }),
		"oror": binaryBool(func(a, b bool) *ir.Constant { return ir.BoolConstant(a || b) }),
		"eql":  binaryBool(func(a, b bool) *ir.Constant { return ir.BoolConstant(a == b) }),
		"neq":  binaryBool(func(a, b bool) *ir.Constant { return ir.BoolConstant(a != b) }),
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

// stringIntrinsics evaluates the string operators: add concatenates, and the
// comparisons use Go's lexicographic byte ordering on the operands.
func stringIntrinsics() map[string]Intrinsic {
	return map[string]Intrinsic{
		"add":  binaryStr(func(a, b string) *ir.Constant { return ir.StringConstant(a + b) }),
		"eql":  binaryStr(func(a, b string) *ir.Constant { return ir.BoolConstant(a == b) }),
		"neq":  binaryStr(func(a, b string) *ir.Constant { return ir.BoolConstant(a != b) }),
		"lt":   binaryStr(func(a, b string) *ir.Constant { return ir.BoolConstant(a < b) }),
		"lteq": binaryStr(func(a, b string) *ir.Constant { return ir.BoolConstant(a <= b) }),
		"gt":   binaryStr(func(a, b string) *ir.Constant { return ir.BoolConstant(a > b) }),
		"gteq": binaryStr(func(a, b string) *ir.Constant { return ir.BoolConstant(a >= b) }),
	}
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
		"eql":  binaryMillis(kind, kind, func(a, b int64) *ir.Constant { return ir.BoolConstant(a == b) }),
		"neq":  binaryMillis(kind, kind, func(a, b int64) *ir.Constant { return ir.BoolConstant(a != b) }),
		"lt":   binaryMillis(kind, kind, func(a, b int64) *ir.Constant { return ir.BoolConstant(a < b) }),
		"lteq": binaryMillis(kind, kind, func(a, b int64) *ir.Constant { return ir.BoolConstant(a <= b) }),
		"gt":   binaryMillis(kind, kind, func(a, b int64) *ir.Constant { return ir.BoolConstant(a > b) }),
		"gteq": binaryMillis(kind, kind, func(a, b int64) *ir.Constant { return ir.BoolConstant(a >= b) }),
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
