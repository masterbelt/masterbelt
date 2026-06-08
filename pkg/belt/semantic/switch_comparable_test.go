package semantic

import (
	"strings"
	"testing"
)

// A switch is value-equality dispatch, so its scrutinee must be comparable —
// the same equality-driven discipline that requires a map's key to be
// comparable. These tests pin the scrutinee_not_comparable check: a record or
// union scrutinee (and an unbounded type parameter) is rejected, while every
// comparable scrutinee — a scalar, string, enum, datetime, a nominal type that
// opts into comparable, and a T: comparable type parameter — is accepted.

// switchRecordScrutinee is a switch over a record value: a record is not
// comparable, so it is rejected with a pointer to match.
func TestSwitchRecordScrutineeRejected(t *testing.T) {
	src := "pub type Point = { x: nint }\n" +
		"pub fn f(p: Point): nint {\n" +
		"  switch p {\n    _ -> return 0\n  }\n" +
		"}\n"
	_, diags := analyze(src)
	if !hasCodeSwitch(diags, CodeScrutineeNotComparable) {
		t.Fatalf("a record scrutinee is not comparable; want scrutinee_not_comparable, got %v", codes(diags))
	}
	var msg string
	for _, d := range diags {
		if d.Code == CodeScrutineeNotComparable {
			msg = d.Message
		}
	}
	if !strings.Contains(msg, "match") {
		t.Errorf("the message should point to match for record/union branching, got %q", msg)
	}
}

// A union scrutinee is the canonical case the diagnostic steers toward match:
// branching on a union's member type is exactly what match is for.
func TestSwitchUnionScrutineeRejected(t *testing.T) {
	src := "pub type Coin = { amount: nint }\n" +
		"pub type Level = { rank: nint }\n" +
		"pub type GameValue = Coin | Level\n" +
		"pub fn f(g: GameValue): nint {\n" +
		"  switch g {\n    _ -> return 0\n  }\n" +
		"}\n"
	_, diags := analyze(src)
	if !hasCodeSwitch(diags, CodeScrutineeNotComparable) {
		t.Fatalf("a union scrutinee is not comparable; want scrutinee_not_comparable, got %v", codes(diags))
	}
}

// An unbounded type parameter carries no contract, so a switch over it has no
// guarantee of comparability and is rejected.
func TestSwitchUnboundedTypeVarScrutineeRejected(t *testing.T) {
	src := "pub fn f<T>(a: T, b: T): nint {\n" +
		"  switch a {\n    b -> return 1\n    _ -> return 0\n  }\n" +
		"}\n"
	_, diags := analyze(src)
	if !hasCodeSwitch(diags, CodeScrutineeNotComparable) {
		t.Fatalf("an unbounded type parameter is not comparable; want scrutinee_not_comparable, got %v", codes(diags))
	}
}

// An enum scrutinee is comparable (an enum auto-impls comparable), so it is
// accepted — the existing exhaustive enum switch must stay clean.
func TestSwitchEnumScrutineeOK(t *testing.T) {
	src := rarityEnum + "pub fn color(r: Rarity): string {\n" +
		"  switch r {\n    Common -> return \"w\"\n    Rare -> return \"b\"\n    Legend -> return \"g\"\n  }\n" +
		"}\n"
	_, diags := analyze(src)
	if hasCodeSwitch(diags, CodeScrutineeNotComparable) {
		t.Fatalf("an enum is comparable; want no scrutinee_not_comparable, got %v", codes(diags))
	}
}

// A string scrutinee is comparable.
func TestSwitchStringScrutineeOK(t *testing.T) {
	src := "pub fn f(s: string): nint {\n" +
		"  switch s {\n    \"a\" -> return 1\n    _ -> return 0\n  }\n" +
		"}\n"
	_, diags := analyze(src)
	if hasCodeSwitch(diags, CodeScrutineeNotComparable) {
		t.Fatalf("string is comparable; want no scrutinee_not_comparable, got %v", codes(diags))
	}
}

// An int scrutinee is comparable.
func TestSwitchIntScrutineeOK(t *testing.T) {
	src := "pub fn f(n: nint): nint {\n" +
		"  switch n {\n    0 -> return 1\n    _ -> return 0\n  }\n" +
		"}\n"
	_, diags := analyze(src)
	if hasCodeSwitch(diags, CodeScrutineeNotComparable) {
		t.Fatalf("int is comparable; want no scrutinee_not_comparable, got %v", codes(diags))
	}
}

// A datetime scrutinee is comparable (datetime opts into comparable in the
// prelude).
func TestSwitchDatetimeScrutineeOK(t *testing.T) {
	src := "pub fn f(d: datetime): nint {\n" +
		"  switch d {\n    _ -> return 0\n  }\n" +
		"}\n"
	_, diags := analyze(src)
	if hasCodeSwitch(diags, CodeScrutineeNotComparable) {
		t.Fatalf("datetime is comparable; want no scrutinee_not_comparable, got %v", codes(diags))
	}
}

// A nominal type over int that opts into comparable with an empty impl is
// comparable: the empty impl inherits int's equality.
func TestSwitchNominalComparableScrutineeOK(t *testing.T) {
	src := "pub type Level = int impl comparable {}\n" +
		"pub fn f(l: Level): nint {\n" +
		"  switch l {\n    _ -> return 0\n  }\n" +
		"}\n"
	_, diags := analyze(src)
	if hasCodeSwitch(diags, CodeScrutineeNotComparable) {
		t.Fatalf("a nominal type with impl comparable {} is comparable; want no scrutinee_not_comparable, got %v", codes(diags))
	}
}

// A T: comparable type parameter satisfies the bound by construction, so a
// switch over it in a generic function body is accepted.
func TestSwitchComparableTypeVarScrutineeOK(t *testing.T) {
	src := "pub fn pick<T: comparable>(a: T, b: T): nint {\n" +
		"  switch a {\n    b -> return 1\n    _ -> return 0\n  }\n" +
		"}\n"
	_, diags := analyze(src)
	if hasCodeSwitch(diags, CodeScrutineeNotComparable) {
		t.Fatalf("a T: comparable type parameter is comparable; want no scrutinee_not_comparable, got %v", codes(diags))
	}
}

// A let-bound enum local is comparable the same way the parameter is: the check
// reads the local's settled type, so a switch over it is accepted.
func TestSwitchLetBoundEnumScrutineeOK(t *testing.T) {
	src := rarityEnum + "pub fn color(c: Rarity): string {\n" +
		"  let x = c\n" +
		"  switch x {\n    Common -> return \"w\"\n    Rare -> return \"b\"\n    Legend -> return \"g\"\n  }\n" +
		"}\n"
	_, diags := analyze(src)
	if hasCodeSwitch(diags, CodeScrutineeNotComparable) {
		t.Fatalf("a let-bound enum local is comparable; want no scrutinee_not_comparable, got %v", codes(diags))
	}
}
