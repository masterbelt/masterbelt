package config

import (
	"strings"
	"testing"

	"github.com/masterbelt/masterbelt/pkg/diagnostic"
)

func TestParse(t *testing.T) {
	src := []byte("name = \"mygame\"\nversion = \"0.1.0\"\nentry = \"src/main.belt\"\n")
	cfg, diags := Parse(src)
	if diags.Len() != 0 {
		t.Fatalf("Parse() diagnostics = %v, want none", diags.Items())
	}
	want := Config{Name: "mygame", Version: "0.1.0", Entry: "src/main.belt"}
	if cfg != want {
		t.Errorf("Parse() = %+v, want %+v", cfg, want)
	}
}

func TestParseEntryOnly(t *testing.T) {
	// Only entry is required; name and version may be omitted.
	cfg, diags := Parse([]byte("entry = \"main.belt\"\n"))
	if diags.Len() != 0 {
		t.Fatalf("Parse() diagnostics = %v, want none", diags.Items())
	}
	if cfg.Entry != "main.belt" {
		t.Errorf("Entry = %q, want %q", cfg.Entry, "main.belt")
	}
}

func TestParseIgnoresUnknownKeys(t *testing.T) {
	// Future manifest sections must not break older toolchains.
	_, diags := Parse([]byte("entry = \"main.belt\"\n[dependencies]\nfoo = \"1.0\"\n"))
	if diags.Len() != 0 {
		t.Errorf("Parse() diagnostics = %v, want none", diags.Items())
	}
}

func TestParseSyntaxError(t *testing.T) {
	src := []byte("entry = \"main.belt\"\nname = @broken\n")
	_, diags := Parse(src)
	d := singleError(t, diags, CodeInvalid)
	// The TOML parser reports a position; the diagnostic anchors to it instead
	// of pointing at the whole file.
	if d.Offset == 0 {
		t.Errorf("Offset = 0, want the parser-reported offset into the manifest")
	}
}

func TestParseSchemaMismatch(t *testing.T) {
	// A well-formed document with a wrongly typed value is just as invalid.
	_, diags := Parse([]byte("entry = 1\n"))
	singleError(t, diags, CodeInvalid)
}

func TestParseMissingEntry(t *testing.T) {
	_, diags := Parse([]byte("name = \"mygame\"\n"))
	d := singleError(t, diags, CodeMissingEntry)
	if d.Offset != 0 || d.Width != 0 {
		t.Errorf("Offset/Width = %d/%d, want 0/0 (the manifest as a whole)", d.Offset, d.Width)
	}
}

func TestParseAbsoluteEntry(t *testing.T) {
	_, diags := Parse([]byte("entry = \"/etc/main.belt\"\n"))
	d := singleError(t, diags, CodeInvalid)
	if !strings.Contains(d.Message, "relative") {
		t.Errorf("Message = %q, want it to explain entry must be relative", d.Message)
	}
}

func TestParseEscapingEntry(t *testing.T) {
	_, diags := Parse([]byte("entry = \"../outside/main.belt\"\n"))
	d := singleError(t, diags, CodeInvalid)
	if !strings.Contains(d.Message, "escape") {
		t.Errorf("Message = %q, want it to explain entry must not escape the root", d.Message)
	}
}

func TestMissing(t *testing.T) {
	d := Missing()
	if d.Code != CodeMissing || d.Severity != diagnostic.Error {
		t.Errorf("Missing() = %v", d)
	}
}

func TestEntryNotFound(t *testing.T) {
	d := EntryNotFound("src/main.belt")
	if d.Code != CodeEntryNotFound || d.Severity != diagnostic.Error {
		t.Errorf("EntryNotFound() = %v", d)
	}
	if !strings.Contains(d.Message, "src/main.belt") {
		t.Errorf("Message = %q, want it to name the entry", d.Message)
	}
}

// singleError asserts diags holds exactly one error diagnostic with the given
// code and returns it.
func singleError(t *testing.T, diags diagnostic.List, code diagnostic.Code) diagnostic.Diagnostic {
	t.Helper()
	if diags.Len() != 1 {
		t.Fatalf("diagnostics = %v, want exactly one", diags.Items())
	}
	d := diags.Items()[0]
	if d.Code != code {
		t.Fatalf("Code = %s, want %s", d.Code, code)
	}
	if d.Severity != diagnostic.Error {
		t.Fatalf("Severity = %s, want error", d.Severity)
	}
	return d
}
