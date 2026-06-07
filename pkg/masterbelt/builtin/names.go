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
