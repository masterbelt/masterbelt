package diagnostic

import "strconv"

// A diagnostic's Fields are fmt.Stringer so they can be rendered into a message
// and inspected by tooling. These wrappers adapt the primitive field types used
// by code.csv; the generator maps each declared field type to one of them.

// Rune is a rune field value. It renders quoted, e.g. 'x'.
type Rune rune

func (r Rune) String() string { return strconv.QuoteRune(rune(r)) }

// Int is an integer field value.
type Int int

func (i Int) String() string { return strconv.Itoa(int(i)) }

// Str is a string field value.
type Str string

func (s Str) String() string { return string(s) }
