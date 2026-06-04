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
