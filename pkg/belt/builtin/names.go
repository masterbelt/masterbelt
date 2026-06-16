package builtin

// The canonical names of the registry's primitive types. The registry is
// keyed by name, and every layer that asks "is this the list type?" or
// "look up nint" spells the name — through these constants, so a typo is a
// compile error rather than a silently failed lookup. The registration in
// builtin.go and the prelude's declarations are the same vocabulary; the
// agreement test pins them together.
const (
	NameNint     = "nint"
	NameNuint    = "nuint"
	NameBool     = "bool"
	NameString   = "string"
	NameList     = "list"
	NameMap      = "map"
	NameRange    = "range"
	NameDatetime = "datetime"
	NameDuration = "duration"
	NameError    = "error"
	NameNull     = "null"
	// NameType is the metatype: the type of a reified type value (type : type).
	// It is opaque — no value range, no operators — and declared in no prelude
	// file (its name is the `type` keyword a declaration head reserves), so it is
	// registered as a definition but not a Names() primitive and carries no
	// NativeType.
	NameType = "type"
	// The query-algebra types (query/column mode): columns<M> is a query binding's
	// columns, a field access off it reads column<M, T>, and a comparison of
	// columns yields predicate<M>. They are `= builtin` with no NativeType — their
	// operators lower to SQL rather than folding — so, like the metatype, the layer
	// that asks "is this the column type?" spells the name through these constants.
	NameColumn    = "column"
	NamePredicate = "predicate"
	NameColumns   = "columns"
	NameRelation  = "relation"
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
