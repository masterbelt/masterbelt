package builtin

import (
	"math/big"
	"testing"

	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// strConst and intConst build the operands the string substrate intrinsics take:
// a string receiver and an integer index argument.
func strConst(s string) *ir.Constant { return ir.StringConstant(s) }
func intConst(n int64) *ir.Constant  { return ir.IntConstant(big.NewInt(n)) }

// TestStringLen pins the rune count — not the byte length, so a multi-byte
// character counts once ("héllo" is five runes, six UTF-8 bytes).
func TestStringLen(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int64
	}{
		{"", 0},
		{"abc", 3},
		{"héllo", 5},
		{"あ", 1},
	} {
		got := stringLen(strConst(tc.in), nil)
		if got == nil || got.Kind != ir.ConstInt || got.Int.Int64() != tc.want {
			t.Errorf("len(%q) = %v, want %d", tc.in, got, tc.want)
		}
	}
}

// TestStringAt pins the rune read: an in-range index folds to the rune as a
// length-1 string, an out-of-range or negative one to an index-out-of-range
// error (string | error).
func TestStringAt(t *testing.T) {
	for _, tc := range []struct {
		in    string
		index int64
		want  string
		isErr bool
	}{
		{"héllo", 0, "h", false},
		{"héllo", 1, "é", false},
		{"héllo", 4, "o", false},
		{"héllo", 5, "", true},  // one past the end
		{"héllo", 9, "", true},  // far past the end
		{"héllo", -1, "", true}, // negative
		{"", 0, "", true},       // empty string
	} {
		got := stringAt(strConst(tc.in), []*ir.Constant{intConst(tc.index)})
		if tc.isErr {
			if got == nil || got.Kind != ir.ConstError {
				t.Errorf("at(%q, %d) = %v, want error", tc.in, tc.index, got)
			}
			continue
		}
		if got == nil || got.Kind != ir.ConstString || got.Str != tc.want {
			t.Errorf("at(%q, %d) = %v, want %q", tc.in, tc.index, got, tc.want)
		}
	}
}

// TestStringSlice pins the half-open rune slice [start, end): an in-range pair
// folds to the substring (empty when start == end), an out-of-range or reversed
// pair to an index-out-of-range error.
func TestStringSlice(t *testing.T) {
	for _, tc := range []struct {
		in         string
		start, end int64
		want       string
		isErr      bool
	}{
		{"héllo", 0, 2, "hé", false},
		{"héllo", 1, 5, "éllo", false},
		{"héllo", 0, 5, "héllo", false},
		{"héllo", 2, 2, "", false}, // empty slice, start == end
		{"héllo", 3, 1, "", true},  // reversed
		{"héllo", 0, 9, "", true},  // end past the end
		{"héllo", -1, 2, "", true}, // negative start
	} {
		got := stringSlice(strConst(tc.in), []*ir.Constant{intConst(tc.start), intConst(tc.end)})
		if tc.isErr {
			if got == nil || got.Kind != ir.ConstError {
				t.Errorf("slice(%q, %d, %d) = %v, want error", tc.in, tc.start, tc.end, got)
			}
			continue
		}
		if got == nil || got.Kind != ir.ConstString || got.Str != tc.want {
			t.Errorf("slice(%q, %d, %d) = %v, want %q", tc.in, tc.start, tc.end, got, tc.want)
		}
	}
}

// TestStringChars pins the rune decode: each rune becomes a length-1 string, in
// order, and the empty string decodes to the empty list.
func TestStringChars(t *testing.T) {
	got := stringChars(strConst("héllo"), nil)
	if got == nil || got.Kind != ir.ConstCollection {
		t.Fatalf("chars(héllo) = %v, want a collection", got)
	}
	want := []string{"h", "é", "l", "l", "o"}
	if len(got.Coll) != len(want) {
		t.Fatalf("chars(héllo) has %d entries, want %d", len(got.Coll), len(want))
	}
	for i, e := range got.Coll {
		if e.Key != nil || e.Value.Kind != ir.ConstString || e.Value.Str != want[i] {
			t.Errorf("chars(héllo)[%d] = %v, want %q", i, e.Value, want[i])
		}
	}
	if empty := stringChars(strConst(""), nil); empty == nil || empty.Kind != ir.ConstCollection || len(empty.Coll) != 0 {
		t.Errorf("chars(%q) = %v, want the empty list", "", empty)
	}
}

// TestStringBytes pins the UTF-8 byte view: "héllo" is six bytes because é
// encodes as two (0xC3 0xA9 = 195 169).
func TestStringBytes(t *testing.T) {
	got := stringBytes(strConst("héllo"), nil)
	if got == nil || got.Kind != ir.ConstCollection {
		t.Fatalf("bytes(héllo) = %v, want a collection", got)
	}
	want := []int64{104, 195, 169, 108, 108, 111}
	if len(got.Coll) != len(want) {
		t.Fatalf("bytes(héllo) has %d entries, want %d", len(got.Coll), len(want))
	}
	for i, e := range got.Coll {
		if e.Key != nil || e.Value.Kind != ir.ConstInt || e.Value.Int.Int64() != want[i] {
			t.Errorf("bytes(héllo)[%d] = %v, want %d", i, e.Value, want[i])
		}
	}
}
