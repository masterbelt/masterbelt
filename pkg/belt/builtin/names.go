package builtin

// The canonical names of the registry's primitive types. The registry is
// keyed by name, and every layer that asks "is this the list type?" or
// "look up nint" spells the name — through these constants, so a typo is a
// compile error rather than a silently failed lookup. The registration in
// builtin.go and the prelude's declarations are the same vocabulary; the
// agreement test pins them together.
const (
	NameNint     = "nint"
	NameBool     = "bool"
	NameString   = "string"
	NameList     = "list"
	NameMap      = "map"
	NameRange    = "range"
	NameDatetime = "datetime"
	NameDuration = "duration"
	NameError    = "error"
	// NameType is the metatype: the type of a reified type value (type : type).
	// It is opaque — no value range, no operators — and declared in no prelude
	// file (its name is the `type` keyword a declaration head reserves), so it is
	// registered as a definition but not a Names() primitive and carries no
	// NativeType.
	NameType = "type"
)

// The canonical operator-method names every operand type shares: the
// comparison set the operators desugar to (1 == 2 is 1.eql(2)). The
// arithmetic names appear once per registration table and stay literal; the
// comparisons are the vocabulary equality folding and enum contracts repeat,
// so they get constants.
const (
	OpEql  = "eql"
	OpNeq  = "neq"
	OpLt   = "lt"
	OpLteq = "lteq"
	OpGt   = "gt"
	OpGteq = "gteq"
)
