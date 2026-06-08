package ir

import "strings"

// This file is the read side of semantic anchors: looking a declaration
// up by the stable address the semantic layer stamped onto it. The address
// itself is made in package semantic (the module segment comes from the file
// path); here the IR only matches the strings its nodes already carry.

// ByAnchor returns the module's declarations whose anchor matches the given
// one: a constant, type definition, method, or associated constant by its full
// anchor, and a function by its anchor. The returned values are the concrete IR
// nodes (*Const, *TypeDef, *Method, *AssocConst, *Function), in module order.
//
// A bare name anchor (belt:m/f) names an overload set, so several functions —
// or several methods sharing a name on one type — can carry it and the result
// holds them all. A "(sig)" suffix narrows the set to the individual whose
// parameter signature matches, written as the .ir text writes an overloaded
// reference ("(nint, string)"); the match ignores spaces, so "(nint,string)"
// selects the same one. The result is nil when nothing matches.
func (m *Module) ByAnchor(anchor string) []any {
	base, sig, hasSig := splitAnchorSig(anchor)
	if base == "" {
		return nil // the empty anchor is no declaration's address
	}
	var out []any
	for _, c := range m.Consts {
		if c.Anchor == base {
			out = append(out, c)
		}
	}
	for _, t := range m.Types {
		if t.Anchor == base {
			out = append(out, t)
		}
		for _, method := range t.Methods {
			if method.Anchor == base {
				out = append(out, method)
			}
		}
		for _, ac := range t.Consts {
			if ac.Anchor == base {
				out = append(out, ac)
			}
		}
	}
	for _, f := range m.Funcs {
		if f.Anchor == base {
			out = append(out, f)
		}
	}
	if hasSig {
		out = narrowAnchorBySig(out, sig)
	}
	return out
}

// splitAnchorSig splits an anchor into its base address and an optional "(sig)"
// signature suffix. The signature, when present, starts at the first '(' — past
// the "belt:" scheme and any "#member", neither of which contains one.
func splitAnchorSig(anchor string) (base, sig string, hasSig bool) {
	if i := strings.IndexByte(anchor, '('); i >= 0 {
		return anchor[:i], anchor[i:], true
	}
	return anchor, "", false
}

// narrowAnchorBySig keeps the candidates whose parameter signature matches sig.
// Only a function or a method has a signature; a constant or type definition
// has none, so a signature-qualified anchor never selects one. The comparison
// ignores spaces, so the spaced form paramSig writes and the spaceless form an
// agent might type both match.
func narrowAnchorBySig(candidates []any, sig string) []any {
	want := stripSpaces(sig)
	var out []any
	for _, c := range candidates {
		var got string
		switch d := c.(type) {
		case *Function:
			got = paramSig(d.Params)
		case *Method:
			got = paramSig(d.Params)
		default:
			continue
		}
		if stripSpaces(got) == want {
			out = append(out, c)
		}
	}
	return out
}

// stripSpaces removes every space from s, so a signature compares equal whether
// or not it was written with the spaces paramSig emits.
func stripSpaces(s string) string {
	return strings.ReplaceAll(s, " ", "")
}
