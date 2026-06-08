// Package std supplies the bundled standard library: masterbelt modules that
// ride the same trusted load channel as the prelude but, unlike it, are
// explicit, opt-in, per-module, and loaded only when a file imports them. A
// `use { max } from "std:math"` resolves through here — the loader (the project
// layer, and the CLI and LSP that bind it) consumes Resolve to turn a std:
// locator into embedded source.
//
// The boundary this package draws is a load channel, not a path: a std module
// is analyzed as an ordinary module (it has exports and is imported), its source
// merely embedded and its file id carrying the std: scheme. That scheme is what
// the semantic layer keys the builtin-surface trust bit on, so a future
// native-backed std module (a float sqrt) rides the same channel with no rule
// change. The package is deliberately thin — embedded source plus a name lookup
// over it — and a peer of pkg/belt/builtin (the prelude and the native
// registry), not a dependency of the compiler core: semantic never imports it.
package std

import (
	"embed"
	"io/fs"
	"sort"
	"strings"
)

// stdFS holds the embedded standard-module sources, all under module/. A module
// is simply a file there: the locator's module name is the file name with .belt
// dropped, so std:math is module/math.belt. There is no separate registry to
// keep in step — dropping a .belt file under module/ adds a module, and the
// diagnostic-free CI pin then analyzes it like the rest.
//
//go:embed module/*.belt
var stdFS embed.FS

// moduleDir is the embed subtree the modules live under, and moduleExt the
// source extension a module name omits.
const (
	moduleDir = "module"
	moduleExt = ".belt"
)

// Scheme is the locator prefix marking a use path as a reference to a bundled
// standard module: `use { max } from "std:math"`. It is also the prefix the
// file id of a loaded std module carries (FileID "std:math"), which the
// semantic layer reads to grant the module the builtin-surface trust bit and to
// map its anchors under the reserved std/ segment.
const Scheme = "std:"

// Resolve returns the embedded source of the standard module named by a std:
// locator with the scheme stripped (Resolve("math") for "std:math"), and
// whether such a module exists. The name is the source file under module/ with
// the extension dropped, so resolution is a single read of module/<name>.belt;
// a name that names no such file — including a malformed one the embed FS
// rejects as an invalid path — is simply absent, so the loader leaves the use
// unresolved exactly as it would a missing file.
func Resolve(name string) ([]byte, bool) {
	src, err := stdFS.ReadFile(moduleDir + "/" + name + moduleExt)
	if err != nil {
		return nil, false
	}
	return src, true
}

// List returns the names of every bundled standard module, sorted — the
// "standard modules you can use" an editor's import completion, the context
// pack, and the CI diagnostic-free pin enumerate. The names are the .belt files
// under module/ with the extension dropped, so the inventory and what Resolve
// serves cannot drift.
func List() []string {
	entries, err := fs.ReadDir(stdFS, moduleDir)
	if err != nil {
		// module/ is embedded at build time, so a read error is a programming
		// error, not a runtime condition.
		panic("std: reading embedded module dir: " + err.Error())
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), moduleExt) {
			continue
		}
		names = append(names, strings.TrimSuffix(e.Name(), moduleExt))
	}
	sort.Strings(names)
	return names
}

// Locator renders a module name as the locator a use writes for it
// (Locator("math") == "std:math") — the inverse of stripping the scheme, for
// callers that have a name and need the import string.
func Locator(name string) string {
	return Scheme + name
}

// IsLocator reports whether a use path names a bundled standard module — it
// carries the std: scheme — without deciding whether that module exists.
func IsLocator(usePath string) bool {
	return strings.HasPrefix(usePath, Scheme)
}
