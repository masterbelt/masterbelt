package diagnostic

import "fmt"

// Locale selects the language a message is rendered in. It is the base name of
// a messages/<locale>.csv table (e.g. "en", "ja").
type Locale string

// DefaultLocale is the fallback language; every code is guaranteed to have a
// message in it.
const DefaultLocale Locale = "en"

// Render renders code's message in locale from the given field values. The
// per-code, per-locale templates are compiled into the generated renderers map
// (catalog_gen.go), so nothing is parsed at run time and locales remain
// swappable. A locale with no entry for the code falls back to DefaultLocale; an
// unknown code falls back to the code itself.
func Render(locale Locale, code Code, fields map[string]fmt.Stringer) string {
	if render, ok := renderers[code]; ok {
		return render(locale, fields)
	}
	return string(code)
}
