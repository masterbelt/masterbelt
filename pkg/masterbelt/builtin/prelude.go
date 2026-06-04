package builtin

import (
	"embed"
	"io/fs"
	"sort"
)

// preludeFS holds the prelude: the masterbelt source files that declare the
// builtin primitives (their types and operator-method signatures) in the
// language itself. Package semantic loads and validates them against the
// registry, so the in-language declarations and the native descriptors here
// cannot drift.
//
//go:embed belt/*.belt
var preludeFS embed.FS

// The prelude is itself a masterbelt project: its manifest names the entry —
// the barrel that re-exports every prelude module — and PreludeEntry mirrors
// it (the prelude test pins the two together). Analyses treat every file as
// implicitly importing the barrel's exports: `use * from "builtin.belt"`, as
// it were.
//
//go:embed belt/masterbelt.toml
var preludeManifest []byte

// PreludeEntry is the prelude project's entry file: the barrel whose exported
// surface every analyzed file implicitly imports.
const PreludeEntry = "builtin.belt"

// PreludeManifest returns the prelude project's manifest.
func PreludeManifest() []byte { return preludeManifest }

// PreludeSource is one prelude file: its base name and contents.
type PreludeSource struct {
	Name    string
	Content []byte
}

// PreludeSources returns the prelude source files, sorted by name for a stable
// load order.
func PreludeSources() []PreludeSource {
	entries, err := fs.ReadDir(preludeFS, "belt")
	if err != nil {
		// The files are embedded at build time, so a read error is a programming
		// error, not a runtime condition.
		panic("builtin: reading embedded prelude: " + err.Error())
	}
	out := make([]PreludeSource, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		content, err := preludeFS.ReadFile("belt/" + e.Name())
		if err != nil {
			panic("builtin: reading embedded prelude file: " + err.Error())
		}
		out = append(out, PreludeSource{Name: e.Name(), Content: content})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
