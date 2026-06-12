package master_test

import (
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

// The master data layer sits one level above belt and below the concrete formats
// and code generators. That layering is a one-way rule:
//
//	format/* , codegen/* → master → belt → (source, diagnostic)
//
// and it is enforced here, by scanning the real import graph, rather than by
// convention or a linter a //nolint could slip past. Two rules carry it:
//
//  1. the master core (the pkg/master package itself, not its sub-packages) may
//     import only the resolved IR it reads and the diagnostics it reports — so a
//     concrete format's or a code generator's dependency, and the belt compiler
//     engine, can never leak into the core; and
//  2. nothing in the layers at or below master — pkg/source/*, pkg/belt/*,
//     pkg/diagnostic — may import the master layer, so the arrow never reverses.
//
// The integration layer (cmd/*, and a future pkg/masterbelt) is above master and
// may import it; the sub-packages under pkg/master/format and pkg/master/codegen
// are the implementers and may import the core. Neither is constrained here.

const (
	mod        = "github.com/masterbelt/masterbelt"
	masterCore = mod + "/pkg/master"
	irPkg      = mod + "/pkg/source/ir"
	diagPkg    = mod + "/pkg/diagnostic"
)

// masterCoreAllows is the closed set of in-repo packages the master core may
// import. Widening it is a deliberate layering decision, not an accident, so the
// set lives here in one place rather than being inferred.
var masterCoreAllows = map[string]bool{irPkg: true, diagPkg: true}

func isInternal(p string) bool { return p == mod || strings.HasPrefix(p, mod+"/") }

func isMaster(p string) bool {
	return p == masterCore || strings.HasPrefix(p, masterCore+"/")
}

// isBelowMaster reports whether p is in a layer master sits above — the belt
// language, the source text model, or the diagnostics — none of which may import
// the master layer.
func isBelowMaster(p string) bool {
	for _, layer := range []string{"/pkg/source", "/pkg/belt", "/pkg/diagnostic"} {
		if p == mod+layer || strings.HasPrefix(p, mod+layer+"/") {
			return true
		}
	}
	return false
}

// layerViolation returns the reason the import from → to breaks the layering, or
// "" when it is allowed. Imports of the standard library and external modules
// (anything outside this module) are always allowed.
func layerViolation(from, to string) string {
	if !isInternal(to) {
		return ""
	}
	if from == masterCore && !masterCoreAllows[to] {
		return "the master core may import only the resolved IR and diagnostics, not " + to
	}
	if isBelowMaster(from) && isMaster(to) {
		return "a layer below master must not import the master layer (the arrow would reverse)"
	}
	return ""
}

// TestLayerRule pins the rule itself: that it flags the violations it must catch
// and clears the imports it must allow. This is what proves the gate goes red —
// without it, TestImportBoundary scanning a clean tree could pass while the rule
// quietly checked nothing.
func TestLayerRule(t *testing.T) {
	formatPkg := masterCore + "/format/csv"
	codegenPkg := masterCore + "/codegen/go"
	beltPkg := mod + "/pkg/belt/semantic"

	cases := []struct {
		name      string
		from, to  string
		violation bool
	}{
		{"core imports a concrete format", masterCore, formatPkg, true},
		{"core imports a code generator", masterCore, codegenPkg, true},
		{"core imports the belt engine", masterCore, beltPkg, true},
		{"core imports another source package", masterCore, mod + "/pkg/source/ast", true},
		{"belt reaches back into master", beltPkg, masterCore, true},
		{"source reaches back into master", mod + "/pkg/source/ir", masterCore, true},
		{"diagnostic reaches back into master", diagPkg, masterCore, true},
		{"belt reaches into a master sub-package", beltPkg, formatPkg, true},

		{"core imports the IR it reads", masterCore, irPkg, false},
		{"core imports diagnostics", masterCore, diagPkg, false},
		{"core imports the standard library", masterCore, "fmt", false},
		{"core imports an external module", masterCore, "golang.org/x/tools/go/packages", false},
		{"a format implements the core", formatPkg, masterCore, false},
		{"the integration layer imports master", mod + "/cmd/masterbelt", masterCore, false},
		{"belt imports source, as it may", beltPkg, irPkg, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := layerViolation(c.from, c.to) != ""
			if got != c.violation {
				t.Errorf("layerViolation(%q, %q) flagged=%v, want %v", c.from, c.to, got, c.violation)
			}
		})
	}
}

// TestImportBoundary applies the rule to the real import graph: every in-repo
// package's own (non-test) imports must satisfy the layering. A violation here
// means the arrow has been broken — a dependency leaked into the master core, or
// a lower layer reached up into master.
func TestImportBoundary(t *testing.T) {
	cfg := &packages.Config{Mode: packages.NeedName | packages.NeedImports}
	pkgs, err := packages.Load(cfg, mod+"/...")
	if err != nil {
		t.Fatalf("loading packages: %v", err)
	}
	loaded := 0
	for _, p := range pkgs {
		if len(p.Errors) > 0 {
			t.Fatalf("loading %s: %v", p.PkgPath, p.Errors)
		}
		if !isInternal(p.PkgPath) {
			continue
		}
		loaded++
		for imp := range p.Imports {
			if reason := layerViolation(p.PkgPath, imp); reason != "" {
				t.Errorf("%s imports %s: %s", p.PkgPath, imp, reason)
			}
		}
	}
	if loaded == 0 {
		t.Fatal("no in-repo packages were scanned; the boundary gate checked nothing")
	}
}
