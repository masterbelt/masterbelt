package semantic

import (
	"strings"

	"github.com/masterbelt/masterbelt/pkg/source/ast"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// This file holds the one place anchors are made: the stable,
// position-independent address every declaration carries. An anchor is
//
//	belt:<module>/<name>[#<member>]
//
// where module is the file's path with the .belt extension dropped, name the
// declaration's name, and member (for a method or associated constant) the
// member's name beneath its owning type. The string is derived purely from the
// name and the file path — never from a source position — so it is the same
// across edits that shift line numbers, and an incremental recompute reproduces
// it exactly (it cannot fork the engine's early cutoff). It does change when a
// declaration is renamed or its file moved: an anchor is stable against
// position edits, not against renames.

// anchorScheme is the prefix every anchor carries, naming the addressing scheme.
const anchorScheme = "belt:"

// stdScheme is the file-id prefix the bundled standard library carries: a std
// module imported as `use { max } from "std:math"` loads under FileID
// "std:math". It is the one bit two semantic concerns read off a file id — the
// builtin-surface trust channel (builtin_surface.go) and the anchor module
// segment below — so the scheme the loader (pkg/belt/std) stamps and the prefix
// the core interprets meet here. The compiler core does not import the std
// package; the convention is shared, not the dependency.
const stdScheme = "std:"

// moduleSegment derives an anchor's module segment from a FileID: the
// project-root-relative path with the .belt extension dropped. The sole file of
// an ad-hoc single-file analysis (soleFileID, the empty string) yields the
// empty segment, so its declarations anchor as "belt:/Name"; the prelude file
// ("builtin.belt") yields the reserved "builtin" segment. A std module's file id
// (std:math) maps to the reserved "std/" top segment (std/math), so max anchors
// at belt:std/math/max — slash-formed exactly like a project's multi-segment
// path, so every downstream consumer (declAnchor, ByAnchor, EnclosingDecl) is
// unchanged.
func moduleSegment(id FileID) string {
	if rest, ok := strings.CutPrefix(string(id), stdScheme); ok {
		return "std/" + rest
	}
	return strings.TrimSuffix(string(id), ".belt")
}

// declAnchor builds a top-level declaration's anchor: the scheme prefix, the
// module segment, and the name. An unnamed declaration (name == "") has no
// anchor — it cannot be referenced — so the result is the empty string.
func declAnchor(module, name string) string {
	if name == "" {
		return ""
	}
	return anchorScheme + module + "/" + name
}

// memberAnchor builds a type member's anchor (a method or an associated
// constant): the owning type's anchor, a "#", and the member name. A member of
// an unnamed type (parent == "") or an unnamed member has no anchor.
func memberAnchor(parent, member string) string {
	if parent == "" || member == "" {
		return ""
	}
	return parent + "#" + member
}

// ByAnchor returns every declaration in the program with the given anchor, in
// file-id order — an overload set spread across files holds several, a "(sig)"
// suffix narrows it to one, and an address nothing carries returns nil. It is
// the resolution side of anchoring: the common address a structure-editing or
// MCP request names a declaration by, rather than a file-and-position the next
// edit invalidates. The returned values are the concrete IR nodes; see
// ir.Module.ByAnchor for the matching rule.
func (p *Program) ByAnchor(anchor string) []any {
	var out []any
	for _, id := range p.Files() {
		if m := p.modules[id]; m != nil {
			out = append(out, m.ByAnchor(anchor)...)
		}
	}
	return out
}

// EnclosingDecl returns the anchor of the smallest declaration in file whose
// source range contains the byte offset — a top-level constant, type, or
// function, or a method or associated constant nested in a type — and ok=false
// when the offset lies in no declaration (or the file has no module). It is how
// a position-only fact, a diagnostic's offset above all, is given a stable
// address at the boundary where it is serialized (the check JSON), without the
// diagnostic itself ever carrying one.
func (p *Program) EnclosingDecl(file FileID, offset int) (string, bool) {
	module := p.modules[file]
	doc := p.docs[file]
	if module == nil || doc == nil {
		return "", false
	}
	positions := positionsOf(doc.Concrete().Tree())

	best, bestWidth := "", -1
	consider := func(anchor string, n ast.Node) {
		if anchor == "" {
			return
		}
		s := spanOf(positions, n)
		if s.width == 0 || offset < s.offset || offset >= s.offset+s.width {
			return
		}
		if bestWidth < 0 || s.width < bestWidth {
			best, bestWidth = anchor, s.width
		}
	}

	for _, c := range module.Consts {
		if c.Syntax != nil {
			consider(c.Anchor, c.Syntax)
		}
	}
	for _, t := range module.Types {
		switch {
		case t.Syntax != nil:
			consider(t.Anchor, t.Syntax)
		case t.EnumSyntax != nil:
			consider(t.Anchor, t.EnumSyntax)
		case t.InterfaceSyntax != nil:
			consider(t.Anchor, t.InterfaceSyntax)
		case t.MasterSyntax != nil:
			consider(t.Anchor, t.MasterSyntax)
		}
		for _, m := range t.Methods {
			if m.Syntax != nil {
				consider(m.Anchor, m.Syntax)
			}
		}
		for _, ac := range t.Consts {
			if ac.Syntax != nil {
				consider(ac.Anchor, ac.Syntax)
			}
		}
	}
	for _, f := range module.Funcs {
		if f.Syntax != nil {
			consider(f.Anchor, f.Syntax)
		}
	}
	return best, best != ""
}

// stampTypeAnchors fills the anchors of a file's resolved type definitions and
// the members beneath them, given the file's module segment. A type anchors at
// belt:module/Name; each of its methods and associated constants anchors at the
// type's anchor with the member name appended. An unnamed type and everything
// under it stay anchorless. It is the single channel type anchors are made
// through, called once per file as its definitions are built.
func stampTypeAnchors(module string, defs []*ir.TypeDef) {
	for _, def := range defs {
		def.Anchor = declAnchor(module, def.Name)
		for _, m := range def.Methods {
			m.Anchor = memberAnchor(def.Anchor, m.Name)
		}
		for _, c := range def.Consts {
			c.Anchor = memberAnchor(def.Anchor, c.Name)
		}
	}
}
