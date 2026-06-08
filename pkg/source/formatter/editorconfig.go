package formatter

import (
	"strings"

	"mvdan.cc/editorconfig"
)

// Resolve returns the Layout for the file at path, reading the nearest
// .editorconfig — walking up to the first root=true, exactly as every
// EditorConfig tool does. Only the substrate properties masterbelt honours are
// consulted: indent_style / indent_size / tab_width drive the indent unit, and
// end_of_line drives the terminator. Every other property, and any of these a
// config leaves unset, falls back to the matching field of fallback.
//
// masterbelt never parses .editorconfig itself; resolution is delegated to
// mvdan.cc/editorconfig (which the official core test suite vets), and this code
// only translates the resolved section into a Layout. A missing or unreadable
// config is not an error here — it simply yields the fallback.
//
// fallback stacks a lower-priority source beneath the config so precedence is
// always .editorconfig > fallback: the CLI passes DefaultLayout (the house
// style), and the LSP passes the editor's FormattingOptions folded onto the
// house style, giving .editorconfig > editor options > house default.
func Resolve(path string, fallback Layout) Layout {
	sec, err := editorconfig.Find(path, nil)
	if err != nil {
		return fallback
	}
	return Layout{
		Indent:    indentUnit(sec, fallback.Indent),
		EndOfLine: endOfLine(sec, fallback.EndOfLine),
	}
}

// indentUnit renders the section's indent_style/indent_size into one level of
// indentation, or returns fallback when the section does not pin the style (or
// pins space without a usable size).
func indentUnit(sec editorconfig.Section, fallback string) string {
	switch sec.Get("indent_style") {
	case "tab":
		return "\t"
	case "space":
		if n := sec.IndentSize(); n > 0 {
			return strings.Repeat(" ", n)
		}
		return fallback
	default:
		return fallback
	}
}

// endOfLine maps the section's end_of_line to a terminator, or returns fallback
// when it is unset.
func endOfLine(sec editorconfig.Section, fallback string) string {
	switch sec.Get("end_of_line") {
	case "lf":
		return "\n"
	case "crlf":
		return "\r\n"
	case "cr":
		return "\r"
	default:
		return fallback
	}
}
