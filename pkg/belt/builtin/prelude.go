package builtin

import (
	"bytes"
	"embed"
	"io/fs"
	"sort"
)

// preludeFS holds the prelude fragments: the masterbelt source that declares
// the builtin primitives (their types and operator-method signatures) in the
// language itself. The belt/ files are an editorial split of one logical file —
// the builtins cross-reference too densely to be modules — and PreludeSource
// joins them back together. Package semantic loads and validates the result
// against the registry, so the in-language declarations and the native
// descriptors here cannot drift.
//
//go:embed belt/*.belt
var preludeFS embed.FS

// PreludeEntry is the prelude's file name: the single source PreludeSource
// assembles. Analyses treat every file as implicitly importing its exports:
// `use * from "builtin.belt"`, as it were.
const PreludeEntry = "builtin.belt"

// PreludeSource returns the prelude as the one file it is: the belt/ fragments
// concatenated in name order, each under a banner comment naming it. The order
// is cosmetic — the resolver runs multiple passes within a file, so fragments
// reference one another freely, forward or backward.
func PreludeSource() []byte {
	entries, err := fs.ReadDir(preludeFS, "belt")
	if err != nil {
		// The files are embedded at build time, so a read error is a programming
		// error, not a runtime condition.
		panic("builtin: reading embedded prelude: " + err.Error())
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	var buf bytes.Buffer
	for _, name := range names {
		content, err := preludeFS.ReadFile("belt/" + name)
		if err != nil {
			panic("builtin: reading embedded prelude file: " + err.Error())
		}
		if buf.Len() > 0 {
			buf.WriteByte('\n')
		}
		buf.WriteString("// ===== " + name + " =====\n")
		buf.Write(bytes.TrimRight(content, "\n"))
		buf.WriteByte('\n')
	}
	return buf.Bytes()
}
