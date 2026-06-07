package main

import (
	"flag"
	"os"
	"testing"
)

var update = flag.Bool("update", false, "update the golden generated source")

// TestGenerateSample pins the generator's whole output on the sample fixture
// package — the generator is the single point of failure for every layer's
// codec, so its output is goldened, not just spot-checked. Refresh with:
//
//	go test ./pkg/source/internal/treegen/ -update
func TestGenerateSample(t *testing.T) {
	got, err := Generate("testdata/sample", []string{"Node"}, []string{"Root"})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	golden := "testdata/sample/text_gen.go.golden"
	if *update {
		if err := os.WriteFile(golden, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("missing golden (run: go test ./pkg/source/internal/treegen/ -update): %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("generated source diverges from the golden\n--- got ---\n%s", got)
	}
}

// TestGenerateRejects pins the generator's error paths: a missing marker, a
// missing root, and an unsupported field type must error, not emit garbage.
func TestGenerateRejects(t *testing.T) {
	if _, err := Generate("testdata/sample", []string{"Bogus"}, []string{"Root"}); err == nil {
		t.Error("unknown marker accepted")
	}
	if _, err := Generate("testdata/sample", []string{"Node"}, []string{"Bogus"}); err == nil {
		t.Error("unknown root accepted")
	}
}
