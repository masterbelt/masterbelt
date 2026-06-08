package std

import (
	"slices"
	"testing"
)

// TestResolveKnownModule pins that a registered module resolves to non-empty
// embedded source, and the name with the scheme already attached is not it (the
// argument is the bare name, scheme stripped).
func TestResolveKnownModule(t *testing.T) {
	src, ok := Resolve("math")
	if !ok {
		t.Fatal("Resolve(math) = _, false, want the embedded module")
	}
	if len(src) == 0 {
		t.Error("Resolve(math) returned empty source")
	}
}

// TestResolveUnknownModule pins that an unregistered name is simply absent — the
// loader leaves the use unresolved, exactly as a missing file.
func TestResolveUnknownModule(t *testing.T) {
	if _, ok := Resolve("nonesuch"); ok {
		t.Error("Resolve(nonesuch) = _, true, want false")
	}
	// The scheme is stripped by the caller; passing it through is not a name.
	if _, ok := Resolve("std:math"); ok {
		t.Error("Resolve(std:math) = _, true; the argument is the bare name")
	}
}

// TestList pins the sorted module inventory the editor and the CI pin enumerate.
func TestList(t *testing.T) {
	got := List()
	if !slices.Contains(got, "math") {
		t.Errorf("List() = %v, want it to contain math", got)
	}
	if !slices.IsSorted(got) {
		t.Errorf("List() = %v, want sorted", got)
	}
}

// TestLocatorRoundTrip pins that Locator is the inverse of stripping the scheme
// and IsLocator recognizes what Locator produces.
func TestLocatorRoundTrip(t *testing.T) {
	if got := Locator("math"); got != "std:math" {
		t.Errorf("Locator(math) = %q, want std:math", got)
	}
	if !IsLocator(Locator("math")) {
		t.Error("IsLocator(Locator(math)) = false, want true")
	}
	if IsLocator("geometry.belt") {
		t.Error("IsLocator(geometry.belt) = true, want false")
	}
}
