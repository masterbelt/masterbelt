package formatter

// Layout is the per-project formatting substrate an .editorconfig owns: the
// one-level indentation unit and the line terminator. It is deliberately the
// whole of what a project may tune. Everything else the formatter decides —
// token spacing, blank-line collapsing, comment placement, and the
// one-line/multi-line choice — stays masterbelt's and is never configurable, so
// "one canonical spelling" holds within a repository while the substrate is
// free to differ between projects, exactly as EditorConfig intends.
//
// The formatter only consumes a Layout; resolving one from an .editorconfig is a
// separate concern (see Resolve) shared by the CLI and the LSP so the two can
// never format the same file differently.
type Layout struct {
	// Indent is one level of indentation: "\t" for tabs, or N spaces. The
	// structural printer renders nesting depth with it; until that lands the
	// field is resolved and carried but not yet drawn.
	Indent string

	// EndOfLine is the terminator every line break renders as: "\n" (lf),
	// "\r\n" (crlf), or "\r" (cr).
	EndOfLine string
}

// DefaultLayout is the house style used when neither an .editorconfig nor an
// editor option decides otherwise: a two-space indent and LF lines. It matches
// the repository's own [*.belt] .editorconfig, so example and prelude formatting
// does not move whenever a project stays silent.
var DefaultLayout = Layout{Indent: "  ", EndOfLine: "\n"}
