package project

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/masterbelt/masterbelt/pkg/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/project/config"
)

// write creates path under root (and any parent directories) with content.
func write(t *testing.T, root, path, content string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestFindRoot(t *testing.T) {
	root := t.TempDir()
	write(t, root, "masterbelt.toml", "entry = \"main.belt\"\n")
	deep := filepath.Join(root, "src", "deep")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}

	// The manifest marks the root from any directory beneath it.
	for _, dir := range []string{root, filepath.Join(root, "src"), deep} {
		got, ok := FindRoot(dir)
		if !ok || got != root {
			t.Errorf("FindRoot(%q) = %q, %v; want %q, true", dir, got, ok, root)
		}
	}
}

func TestFindRootNotFound(t *testing.T) {
	if got, ok := FindRoot(t.TempDir()); ok {
		t.Errorf("FindRoot() = %q, true; want not found", got)
	}
}

func TestOpen(t *testing.T) {
	root := t.TempDir()
	write(t, root, "masterbelt.toml", "entry = \"src/main.belt\"\n")
	write(t, root, "src/main.belt", "const MaxLevel: int64 = 100\n")

	proj, diags := Open(root)
	if diags.Len() != 0 {
		t.Fatalf("Open() diagnostics = %v, want none", diags.Items())
	}
	if proj.Root != root {
		t.Errorf("Root = %q, want %q", proj.Root, root)
	}
	if proj.Config.Entry != "src/main.belt" {
		t.Errorf("Config.Entry = %q, want %q", proj.Config.Entry, "src/main.belt")
	}
	if proj.Entry != FileID("src/main.belt") {
		t.Errorf("Entry = %q, want %q", proj.Entry, "src/main.belt")
	}

	// P-1: the file set is exactly the entry.
	files := proj.Files()
	if len(files) != 1 || files[0] != proj.EntryFile() {
		t.Fatalf("Files() = %v, want just the entry file", files)
	}
	entry := proj.File(proj.Entry)
	if entry == nil || string(entry.Data) != "const MaxLevel: int64 = 100\n" {
		t.Errorf("entry file = %+v, want the entry's content", entry)
	}
	if entry.Path != filepath.Join(root, "src", "main.belt") {
		t.Errorf("entry.Path = %q", entry.Path)
	}
}

func TestOpenFromSubdirectory(t *testing.T) {
	root := t.TempDir()
	write(t, root, "masterbelt.toml", "entry = \"main.belt\"\n")
	write(t, root, "main.belt", "const A = 1\n")
	write(t, root, "sub/keep", "") // a directory beneath the root to open from

	proj, diags := Open(filepath.Join(root, "sub"))
	if diags.Len() != 0 || proj == nil {
		t.Fatalf("Open(sub) = %v, %v; want the project", proj, diags.Items())
	}
	if proj.Root != root {
		t.Errorf("Root = %q, want %q", proj.Root, root)
	}
}

func TestOpenNormalizesEntry(t *testing.T) {
	root := t.TempDir()
	write(t, root, "masterbelt.toml", "entry = \"./src/main.belt\"\n")
	write(t, root, "src/main.belt", "const A = 1\n")

	proj, diags := Open(root)
	if diags.Len() != 0 || proj == nil {
		t.Fatalf("Open() = %v, %v; want the project", proj, diags.Items())
	}
	// The FileID is the cleaned, "/"-separated root-relative path, not the
	// manifest's spelling.
	if proj.Entry != FileID("src/main.belt") {
		t.Errorf("Entry = %q, want %q", proj.Entry, "src/main.belt")
	}
}

func TestOpenProfile(t *testing.T) {
	root := t.TempDir()
	write(t, root, "masterbelt.toml", "entry = \"src/main.belt\"\n\n[profile.editor]\nentry = \"src/editor.belt\"\n")
	write(t, root, "src/main.belt", "const A = 1\n")
	write(t, root, "src/editor.belt", "const E = 2\n")

	proj, diags := OpenProfile(root, "editor")
	if diags.Len() != 0 {
		t.Fatalf("OpenProfile() diagnostics = %v, want none", diags.Items())
	}
	if proj.Profile != "editor" {
		t.Errorf("Profile = %q, want %q", proj.Profile, "editor")
	}
	if proj.Entry != FileID("src/editor.belt") {
		t.Errorf("Entry = %q, want the editor profile's entry", proj.Entry)
	}
	if got := string(proj.EntryFile().Data); got != "const E = 2\n" {
		t.Errorf("entry data = %q, want the editor source", got)
	}

	// The default profile of the same manifest stays reachable as "".
	proj, diags = OpenProfile(root, "")
	if diags.Len() != 0 || proj.Entry != FileID("src/main.belt") || proj.Profile != "" {
		t.Errorf("OpenProfile(\"\") = %+v, %v; want the default entry", proj, diags.Items())
	}
}

func TestOpenUnknownProfile(t *testing.T) {
	root := t.TempDir()
	write(t, root, "masterbelt.toml", "entry = \"main.belt\"\n")
	write(t, root, "main.belt", "const A = 1\n")

	proj, diags := OpenProfile(root, "editor")
	if proj != nil {
		t.Fatalf("OpenProfile() = %+v, want nil", proj)
	}
	d := assertSingleError(t, diags, config.CodeUnknownProfile)
	if want := "profile editor is not defined in masterbelt.toml"; d.Message != want {
		t.Errorf("Message = %q, want %q", d.Message, want)
	}
}

func TestOpenProfileEntryNotFound(t *testing.T) {
	root := t.TempDir()
	write(t, root, "masterbelt.toml", "entry = \"main.belt\"\n\n[profile.editor]\nentry = \"editor.belt\"\n")
	write(t, root, "main.belt", "const A = 1\n")

	proj, diags := OpenProfile(root, "editor")
	if proj != nil {
		t.Fatalf("OpenProfile() = %+v, want nil", proj)
	}
	d := assertSingleError(t, diags, config.CodeProfileEntryNotFound)
	if want := "entry file not found for profile editor: editor.belt"; d.Message != want {
		t.Errorf("Message = %q, want %q", d.Message, want)
	}
}

func TestOpenDefaultOfProfileOnlyManifest(t *testing.T) {
	// The manifest is valid with named profiles alone; asking for the silent
	// default profile is what fails.
	root := t.TempDir()
	write(t, root, "masterbelt.toml", "[profile.editor]\nentry = \"editor.belt\"\n")
	write(t, root, "editor.belt", "const E = 1\n")

	proj, diags := Open(root)
	if proj != nil {
		t.Fatalf("Open() = %+v, want nil", proj)
	}
	assertSingleError(t, diags, config.CodeMissingEntry)

	if proj, diags := OpenProfile(root, "editor"); proj == nil || diags.Len() != 0 {
		t.Errorf("OpenProfile(editor) = %v, %v; want the project", proj, diags.Items())
	}
}

func TestOpenMissingManifest(t *testing.T) {
	proj, diags := Open(t.TempDir())
	if proj != nil {
		t.Fatalf("Open() = %+v, want nil", proj)
	}
	assertSingleError(t, diags, config.CodeMissing)
}

func TestOpenInvalidManifest(t *testing.T) {
	root := t.TempDir()
	write(t, root, "masterbelt.toml", "entry = @broken\n")

	proj, diags := Open(root)
	if proj != nil {
		t.Fatalf("Open() = %+v, want nil", proj)
	}
	assertSingleError(t, diags, config.CodeInvalid)
}

func TestOpenMissingEntryKey(t *testing.T) {
	root := t.TempDir()
	write(t, root, "masterbelt.toml", "name = \"mygame\"\n")

	proj, diags := Open(root)
	if proj != nil {
		t.Fatalf("Open() = %+v, want nil", proj)
	}
	assertSingleError(t, diags, config.CodeMissingEntry)
}

func TestOpenEntryNotFound(t *testing.T) {
	root := t.TempDir()
	write(t, root, "masterbelt.toml", "entry = \"src/main.belt\"\n")

	proj, diags := Open(root)
	if proj != nil {
		t.Fatalf("Open() = %+v, want nil", proj)
	}
	d := assertSingleError(t, diags, config.CodeEntryNotFound)
	if want := "entry file not found: src/main.belt"; d.Message != want {
		t.Errorf("Message = %q, want %q", d.Message, want)
	}
}

// assertSingleError asserts diags holds exactly one error diagnostic with the
// given code and returns it.
func assertSingleError(t *testing.T, diags diagnostic.List, code diagnostic.Code) diagnostic.Diagnostic {
	t.Helper()
	if diags.Len() != 1 {
		t.Fatalf("diagnostics = %v, want exactly one", diags.Items())
	}
	d := diags.Items()[0]
	if d.Code != code || d.Severity != diagnostic.Error {
		t.Fatalf("diagnostic = %v, want error %s", d, code)
	}
	return d
}
