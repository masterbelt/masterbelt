package diagnostic

import "testing"

func TestFieldStringers(t *testing.T) {
	if got := Rune('#').String(); got != "'#'" {
		t.Errorf("Rune('#') = %q, want %q", got, "'#'")
	}
	if got := Rune('あ').String(); got != "'あ'" {
		t.Errorf("Rune('あ') = %q, want %q", got, "'あ'")
	}
	if got := Int(42).String(); got != "42" {
		t.Errorf("Int(42) = %q, want %q", got, "42")
	}
	if got := Str("hi").String(); got != "hi" {
		t.Errorf("Str(\"hi\") = %q, want %q", got, "hi")
	}
}
