package project

import (
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/masterbelt/masterbelt/internal/belttest"
	"github.com/masterbelt/masterbelt/pkg/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/project/config"
	"github.com/masterbelt/masterbelt/pkg/source"
)

func TestFindRoot(t *testing.T) {
	root := t.TempDir()
	belttest.WriteFile(t, root, "masterbelt.toml", "entry = \"main.belt\"\n")
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
	belttest.WriteFile(t, root, "masterbelt.toml", "entry = \"src/main.belt\"\n")
	belttest.WriteFile(t, root, "src/main.belt", "const MaxLevel: int64 = 100\n")

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
	belttest.WriteFile(t, root, "masterbelt.toml", "entry = \"main.belt\"\n")
	belttest.WriteFile(t, root, "main.belt", "const A = 1\n")
	belttest.WriteFile(t, root, "sub/keep", "") // a directory beneath the root to open from

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
	belttest.WriteFile(t, root, "masterbelt.toml", "entry = \"./src/main.belt\"\n")
	belttest.WriteFile(t, root, "src/main.belt", "const A = 1\n")

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
	belttest.WriteFile(t, root, "masterbelt.toml", "entry = \"src/main.belt\"\n\n[profile.editor]\nentry = \"src/editor.belt\"\n")
	belttest.WriteFile(t, root, "src/main.belt", "const A = 1\n")
	belttest.WriteFile(t, root, "src/editor.belt", "const E = 2\n")

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
	belttest.WriteFile(t, root, "masterbelt.toml", "entry = \"main.belt\"\n")
	belttest.WriteFile(t, root, "main.belt", "const A = 1\n")

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
	belttest.WriteFile(t, root, "masterbelt.toml", "entry = \"main.belt\"\n\n[profile.editor]\nentry = \"editor.belt\"\n")
	belttest.WriteFile(t, root, "main.belt", "const A = 1\n")

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
	belttest.WriteFile(t, root, "masterbelt.toml", "[profile.editor]\nentry = \"editor.belt\"\n")
	belttest.WriteFile(t, root, "editor.belt", "const E = 1\n")

	proj, diags := Open(root)
	if proj != nil {
		t.Fatalf("Open() = %+v, want nil", proj)
	}
	assertSingleError(t, diags, config.CodeMissingEntry)

	if proj, diags := OpenProfile(root, "editor"); proj == nil || diags.Len() != 0 {
		t.Errorf("OpenProfile(editor) = %v, %v; want the project", proj, diags.Items())
	}
}

// useTargets renders a file's resolved Uses table as "path->target" pairs,
// sorted, for compact assertions.
func useTargets(f *File) []string {
	var out []string
	for u, target := range f.Uses {
		out = append(out, u.Path+"->"+string(target))
	}
	sort.Strings(out)
	return out
}

func TestOpenClosesOverUses(t *testing.T) {
	root := t.TempDir()
	belttest.WriteFile(t, root, "masterbelt.toml", "entry = \"main.belt\"\n")
	belttest.WriteFile(t, root, "main.belt", "use geo from \"geometry.belt\"\nconst A = 1\n")
	belttest.WriteFile(t, root, "geometry.belt", "use { C } from \"palette.belt\"\npub const Origin = 0\n")
	belttest.WriteFile(t, root, "palette.belt", "pub const C = 2\n")

	proj, diags := Open(root)
	if diags.Len() != 0 {
		t.Fatalf("Open() diagnostics = %v, want none", diags.Items())
	}

	// The set is the closure of the entry's imports, ordered by id.
	var ids []string
	for _, f := range proj.Files() {
		ids = append(ids, string(f.ID))
	}
	if got, want := strings.Join(ids, ","), "geometry.belt,main.belt,palette.belt"; got != want {
		t.Fatalf("Files() = %s, want %s", got, want)
	}

	// Each file's Uses table records where its imports resolved.
	if got := useTargets(proj.EntryFile()); !slices.Equal(got, []string{"geometry.belt->geometry.belt"}) {
		t.Errorf("entry Uses = %v", got)
	}
	if got := useTargets(proj.File("geometry.belt")); !slices.Equal(got, []string{"palette.belt->palette.belt"}) {
		t.Errorf("geometry Uses = %v", got)
	}
	if f := proj.File("palette.belt"); f.AST == nil || len(f.Uses) != 0 {
		t.Errorf("palette = %+v, want parsed with no uses", f)
	}
}

func TestOpenUseCycle(t *testing.T) {
	// A use cycle terminates the closure; both files load, both tables wire.
	// Reporting the cycle is the semantic layer's job.
	root := t.TempDir()
	belttest.WriteFile(t, root, "masterbelt.toml", "entry = \"a.belt\"\n")
	belttest.WriteFile(t, root, "a.belt", "use b from \"b.belt\"\n")
	belttest.WriteFile(t, root, "b.belt", "use a from \"a.belt\"\n")

	proj, diags := Open(root)
	if diags.Len() != 0 || len(proj.Files()) != 2 {
		t.Fatalf("Open() = %v files, %v; want 2 files", len(proj.Files()), diags.Items())
	}
	if got := useTargets(proj.File("b.belt")); !slices.Equal(got, []string{"a.belt->a.belt"}) {
		t.Errorf("b Uses = %v", got)
	}
}

func TestOpenUnresolvableUse(t *testing.T) {
	root := t.TempDir()
	belttest.WriteFile(t, root, "masterbelt.toml", "entry = \"main.belt\"\n")
	belttest.WriteFile(t, root, "main.belt", "use ghost from \"missing.belt\"\nconst A = 1\n")

	proj, diags := Open(root)
	if diags.Len() != 0 {
		t.Fatalf("Open() diagnostics = %v, want none (the use site reports later)", diags.Items())
	}
	if len(proj.Files()) != 1 {
		t.Errorf("Files() = %d, want just the entry", len(proj.Files()))
	}
	if got := useTargets(proj.EntryFile()); len(got) != 0 {
		t.Errorf("entry Uses = %v, want empty (unresolvable)", got)
	}
}

func TestOpenUseEscapesRoot(t *testing.T) {
	root := t.TempDir()
	belttest.WriteFile(t, root, "masterbelt.toml", "entry = \"main.belt\"\n")
	belttest.WriteFile(t, root, "main.belt", "use out from \"../outside.belt\"\n")
	belttest.WriteFile(t, filepath.Dir(root), "outside.belt", "pub const X = 1\n")

	proj, _ := Open(root)
	if len(proj.Files()) != 1 || len(proj.EntryFile().Uses) != 0 {
		t.Errorf("escape was followed: files = %d, uses = %v", len(proj.Files()), useTargets(proj.EntryFile()))
	}
}

func TestOpenUseRelativeToImporter(t *testing.T) {
	// Use paths are relative to the importing file, not the root; ".." may
	// move between directories as long as it stays inside the root.
	root := t.TempDir()
	belttest.WriteFile(t, root, "masterbelt.toml", "entry = \"src/main.belt\"\n")
	belttest.WriteFile(t, root, "src/main.belt", "use geo from \"geometry.belt\"\nuse util from \"../lib/util.belt\"\n")
	belttest.WriteFile(t, root, "src/geometry.belt", "pub const Origin = 0\n")
	belttest.WriteFile(t, root, "lib/util.belt", "pub const U = 1\n")

	proj, diags := Open(root)
	if diags.Len() != 0 {
		t.Fatalf("Open() diagnostics = %v, want none", diags.Items())
	}
	want := []string{"../lib/util.belt->lib/util.belt", "geometry.belt->src/geometry.belt"}
	if got := useTargets(proj.EntryFile()); !slices.Equal(got, want) {
		t.Errorf("entry Uses = %v, want %v", got, want)
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
	belttest.WriteFile(t, root, "masterbelt.toml", "entry = @broken\n")

	proj, diags := Open(root)
	if proj != nil {
		t.Fatalf("Open() = %+v, want nil", proj)
	}
	assertSingleError(t, diags, config.CodeInvalid)
}

func TestOpenMissingEntryKey(t *testing.T) {
	root := t.TempDir()
	belttest.WriteFile(t, root, "masterbelt.toml", "name = \"mygame\"\n")

	proj, diags := Open(root)
	if proj != nil {
		t.Fatalf("Open() = %+v, want nil", proj)
	}
	assertSingleError(t, diags, config.CodeMissingEntry)
}

func TestOpenEntryNotFound(t *testing.T) {
	root := t.TempDir()
	belttest.WriteFile(t, root, "masterbelt.toml", "entry = \"src/main.belt\"\n")

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

// fileIDList flattens the project's file ids for compact assertions.
func fileIDList(p *Project) string {
	var ids []string
	for _, f := range p.Files() {
		ids = append(ids, string(f.ID))
	}
	return strings.Join(ids, ",")
}

func TestResyncPrunesOrphans(t *testing.T) {
	root := t.TempDir()
	belttest.WriteFile(t, root, "masterbelt.toml", "entry = \"main.belt\"\n")
	belttest.WriteFile(t, root, "main.belt", "use geo from \"geometry.belt\"\nconst A = 1\n")
	belttest.WriteFile(t, root, "geometry.belt", "use { C } from \"palette.belt\"\npub const Origin = 0\n")
	belttest.WriteFile(t, root, "palette.belt", "pub const C = 2\n")

	proj, diags := Open(root)
	if diags.Len() != 0 {
		t.Fatalf("Open() diagnostics = %v, want none", diags.Items())
	}
	if got := fileIDList(proj); got != "geometry.belt,main.belt,palette.belt" {
		t.Fatalf("Files() = %s, want the full closure", got)
	}

	// Editing the entry to drop the import orphans geometry and, through it,
	// palette: both leave the set on Resync.
	entry := proj.EntryFile()
	entry.AST.Edit(source.Edit{Start: 0, End: entry.AST.Buffer().Len(), NewText: []byte("const A = 1\n")})
	proj.Resync("main.belt")
	if got := fileIDList(proj); got != "main.belt" {
		t.Errorf("Files() after the edit = %s, want just the entry", got)
	}

	// Restoring the import reloads the subtree from disk.
	entry.AST.Edit(source.Edit{Start: 0, End: entry.AST.Buffer().Len(), NewText: []byte("use geo from \"geometry.belt\"\nconst A = 1\n")})
	proj.Resync("main.belt")
	if got := fileIDList(proj); got != "geometry.belt,main.belt,palette.belt" {
		t.Errorf("Files() after restoring = %s, want the full closure again", got)
	}
}

func TestIncludePinsAndReleaseUnpins(t *testing.T) {
	root := t.TempDir()
	belttest.WriteFile(t, root, "masterbelt.toml", "entry = \"main.belt\"\n")
	belttest.WriteFile(t, root, "main.belt", "const A = 1\n")
	belttest.WriteFile(t, root, "lib/util.belt", "pub const U = 2\n")

	proj, diags := Open(root)
	if diags.Len() != 0 {
		t.Fatalf("Open() diagnostics = %v, want none", diags.Items())
	}

	// Include pins a file no use reaches: edits elsewhere never prune it.
	if f := proj.Include("lib/util.belt"); f == nil {
		t.Fatal("Include(lib/util.belt) = nil, want the file")
	}
	proj.Resync("main.belt")
	if got := fileIDList(proj); got != "lib/util.belt,main.belt" {
		t.Errorf("Files() = %s, want the pinned file to survive Resync", got)
	}

	// Release drops the pin; nothing reaches the file, so it leaves the set.
	proj.Release("lib/util.belt")
	if got := fileIDList(proj); got != "main.belt" {
		t.Errorf("Files() after Release = %s, want just the entry", got)
	}

	// The entry itself is never released.
	proj.Release("main.belt")
	if got := fileIDList(proj); got != "main.belt" {
		t.Errorf("Files() after releasing the entry = %s, want the entry kept", got)
	}
}

func TestCandidateImports(t *testing.T) {
	root := t.TempDir()
	belttest.WriteFile(t, root, "masterbelt.toml", "entry = \"src/main.belt\"\n")
	belttest.WriteFile(t, root, "src/main.belt", "const A = 1\n")
	belttest.WriteFile(t, root, "src/geo.belt", "pub const G = 1\n")
	belttest.WriteFile(t, root, "lib/util.belt", "pub const U = 2\n")
	belttest.WriteFile(t, root, ".cache/junk.belt", "const J = 3\n") // hidden: never offered
	belttest.WriteFile(t, root, "notes.txt", "not a module\n")

	proj, diags := Open(root)
	if diags.Len() != 0 {
		t.Fatalf("Open() diagnostics = %v, want none", diags.Items())
	}

	got := strings.Join(proj.CandidateImports("src/main.belt"), ",")
	want := "../lib/util.belt,geo.belt"
	if got != want {
		t.Fatalf("CandidateImports(src/main.belt) = %s, want %s", got, want)
	}

	// Every candidate resolves back to a real target — the inverse and the
	// rule agree.
	for _, c := range proj.CandidateImports("src/main.belt") {
		if _, ok := resolveUse("src/main.belt", c); !ok {
			t.Errorf("candidate %q does not resolve", c)
		}
	}

	// From the project root the same files render without the climb.
	if got := strings.Join(proj.CandidateImports("entry.belt"), ","); got != "lib/util.belt,src/geo.belt,src/main.belt" {
		t.Errorf("CandidateImports(entry.belt) = %s", got)
	}
}
