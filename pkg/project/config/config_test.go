package config

import (
	"strings"
	"testing"

	"github.com/masterbelt/masterbelt/pkg/diagnostic"
)

func TestParse(t *testing.T) {
	cfg, diags := Parse([]byte("entry = \"src/main.belt\"\n"))
	if diags.Len() != 0 {
		t.Fatalf("Parse() diagnostics = %v, want none", diags.Items())
	}
	if cfg.Entry != "src/main.belt" {
		t.Errorf("Entry = %q, want %q", cfg.Entry, "src/main.belt")
	}
}

func TestParseProfiles(t *testing.T) {
	// Top-level keys form the default profile; [profile.<name>] sections
	// declare named ones.
	src := []byte("entry = \"src/main.belt\"\n\n[profile.editor]\nentry = \"src/editor.belt\"\n")
	cfg, diags := Parse(src)
	if diags.Len() != 0 {
		t.Fatalf("Parse() diagnostics = %v, want none", diags.Items())
	}
	if cfg.Entry != "src/main.belt" {
		t.Errorf("Entry = %q, want the default profile's entry", cfg.Entry)
	}
	if cfg.Profiles["editor"].Entry != "src/editor.belt" {
		t.Errorf("Profiles = %+v, want editor -> src/editor.belt", cfg.Profiles)
	}
}

func TestParseProfilesOnly(t *testing.T) {
	// A manifest may declare only named profiles. Using the default profile
	// is then the error (reported by whoever resolves it), not declaring
	// nothing here.
	cfg, diags := Parse([]byte("[profile.editor]\nentry = \"editor.belt\"\n"))
	if diags.Len() != 0 {
		t.Fatalf("Parse() diagnostics = %v, want none", diags.Items())
	}
	if cfg.Entry != "" {
		t.Errorf("Entry = %q, want empty", cfg.Entry)
	}
}

func TestParseProfileMissingEntry(t *testing.T) {
	// A named profile is an explicit declaration; one without an entry has
	// no purpose.
	_, diags := Parse([]byte("entry = \"main.belt\"\n\n[profile.editor]\n"))
	d := singleError(t, diags, CodeProfileMissingEntry)
	if !strings.Contains(d.Message, "editor") {
		t.Errorf("Message = %q, want it to name the profile", d.Message)
	}
}

func TestParseProfileEscapingEntry(t *testing.T) {
	_, diags := Parse([]byte("entry = \"main.belt\"\n\n[profile.editor]\nentry = \"../outside.belt\"\n"))
	d := singleError(t, diags, CodeInvalid)
	for _, fragment := range []string{`profile "editor"`, "escape"} {
		if !strings.Contains(d.Message, fragment) {
			t.Errorf("Message = %q, want it to contain %q", d.Message, fragment)
		}
	}
}

func TestParseSourceBasePath(t *testing.T) {
	// A [source.<format>] section sets the directory a format resolves its
	// locators against, keyed by the same format name the grammar and the
	// format registry use.
	src := []byte("entry = \"main.belt\"\n\n[source.csv]\nbasePath = \"data/csv\"\n")
	cfg, diags := Parse(src)
	if diags.Len() != 0 {
		t.Fatalf("Parse() diagnostics = %v, want none", diags.Items())
	}
	if got := cfg.Source["csv"].BasePath; got != "data/csv" {
		t.Errorf("Source[csv].BasePath = %q, want %q", got, "data/csv")
	}
}

func TestParseProfileSourceBasePath(t *testing.T) {
	// A named profile carries its own per-format base paths.
	src := []byte("entry = \"main.belt\"\n\n[profile.editor]\nentry = \"editor.belt\"\n\n[profile.editor.source.csv]\nbasePath = \"fixtures\"\n")
	cfg, diags := Parse(src)
	if diags.Len() != 0 {
		t.Fatalf("Parse() diagnostics = %v, want none", diags.Items())
	}
	if got := cfg.Profiles["editor"].Source["csv"].BasePath; got != "fixtures" {
		t.Errorf("Profiles[editor].Source[csv].BasePath = %q, want %q", got, "fixtures")
	}
}

func TestParseSourceBasePathAbsolute(t *testing.T) {
	_, diags := Parse([]byte("entry = \"main.belt\"\n\n[source.csv]\nbasePath = \"/data\"\n"))
	d := singleError(t, diags, CodeInvalid)
	for _, fragment := range []string{`source "csv"`, "relative"} {
		if !strings.Contains(d.Message, fragment) {
			t.Errorf("Message = %q, want it to contain %q", d.Message, fragment)
		}
	}
}

func TestParseSourceBasePathEscaping(t *testing.T) {
	_, diags := Parse([]byte("entry = \"main.belt\"\n\n[source.csv]\nbasePath = \"../shared\"\n"))
	d := singleError(t, diags, CodeInvalid)
	for _, fragment := range []string{`source "csv"`, "escape"} {
		if !strings.Contains(d.Message, fragment) {
			t.Errorf("Message = %q, want it to contain %q", d.Message, fragment)
		}
	}
}

func TestParseSourceBasePathBackslashEscaping(t *testing.T) {
	// A base path authored with Windows separators must obey the same confinement:
	// "..\shared" escapes the root once backslashes are normalized.
	_, diags := Parse([]byte("entry = \"main.belt\"\n\n[source.csv]\nbasePath = \"..\\\\shared\"\n"))
	d := singleError(t, diags, CodeInvalid)
	for _, fragment := range []string{`source "csv"`, "escape"} {
		if !strings.Contains(d.Message, fragment) {
			t.Errorf("Message = %q, want it to contain %q", d.Message, fragment)
		}
	}
}

func TestParseSourceBasePathDriveQualified(t *testing.T) {
	// A Windows drive-qualified path is absolute, not relative, even though its
	// slash form has no leading slash; it must be rejected on every platform.
	_, diags := Parse([]byte("entry = \"main.belt\"\n\n[source.csv]\nbasePath = \"C:\\\\data\"\n"))
	d := singleError(t, diags, CodeInvalid)
	for _, fragment := range []string{`source "csv"`, "relative"} {
		if !strings.Contains(d.Message, fragment) {
			t.Errorf("Message = %q, want it to contain %q", d.Message, fragment)
		}
	}
}

func TestParseDriveQualifiedEntry(t *testing.T) {
	_, diags := Parse([]byte("entry = \"C:\\\\proj\\\\main.belt\"\n"))
	d := singleError(t, diags, CodeInvalid)
	if !strings.Contains(d.Message, "relative") {
		t.Errorf("Message = %q, want it to explain entry must be relative", d.Message)
	}
}

func TestParseProfileSourceBasePathEscaping(t *testing.T) {
	_, diags := Parse([]byte("entry = \"main.belt\"\n\n[profile.editor]\nentry = \"editor.belt\"\n\n[profile.editor.source.csv]\nbasePath = \"../shared\"\n"))
	d := singleError(t, diags, CodeInvalid)
	for _, fragment := range []string{`profile "editor"`, `source "csv"`, "escape"} {
		if !strings.Contains(d.Message, fragment) {
			t.Errorf("Message = %q, want it to contain %q", d.Message, fragment)
		}
	}
}

func TestParseEmptyBasePathIsRoot(t *testing.T) {
	// An unset (or empty) base path means the project root and is not an error.
	cfg, diags := Parse([]byte("entry = \"main.belt\"\n\n[source.csv]\n"))
	if diags.Len() != 0 {
		t.Fatalf("Parse() diagnostics = %v, want none", diags.Items())
	}
	if got := cfg.Source["csv"].BasePath; got != "" {
		t.Errorf("Source[csv].BasePath = %q, want empty (the project root)", got)
	}
}

func TestParseIgnoresUnknownKeys(t *testing.T) {
	// Keys and sections the toolchain does not read must not break it,
	// whichever side is older.
	_, diags := Parse([]byte("name = \"mygame\"\nversion = \"0.1.0\"\nentry = \"main.belt\"\n[dependencies]\nfoo = \"1.0\"\n"))
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
	// A well-formed document with a wrongly typed value is just as invalid,
	// and the parser reports the value's position for it too.
	_, diags := Parse([]byte("entry = 1\n"))
	d := singleError(t, diags, CodeInvalid)
	if d.Offset != 8 {
		t.Errorf("Offset = %d, want 8 (the mistyped value)", d.Offset)
	}
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

func TestMissingEntry(t *testing.T) {
	d := MissingEntry()
	if d.Code != CodeMissingEntry || d.Severity != diagnostic.Error {
		t.Errorf("MissingEntry() = %v", d)
	}
}

func TestUnknownProfile(t *testing.T) {
	d := UnknownProfile("editor")
	if d.Code != CodeUnknownProfile || d.Severity != diagnostic.Error {
		t.Errorf("UnknownProfile() = %v", d)
	}
	if !strings.Contains(d.Message, "editor") {
		t.Errorf("Message = %q, want it to name the profile", d.Message)
	}
}

func TestProfileEntryNotFound(t *testing.T) {
	d := ProfileEntryNotFound("editor", "src/editor.belt")
	if d.Code != CodeProfileEntryNotFound || d.Severity != diagnostic.Error {
		t.Errorf("ProfileEntryNotFound() = %v", d)
	}
	for _, fragment := range []string{"editor", "src/editor.belt"} {
		if !strings.Contains(d.Message, fragment) {
			t.Errorf("Message = %q, want it to contain %q", d.Message, fragment)
		}
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
