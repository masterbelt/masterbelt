package main

// The category table is editorgen's core: the single place that maps the
// lexer's token classes onto highlight categories, so every editor artifact is
// a projection of one source instead of a hand-maintained grammar.
//
// The canonical categories are the semantic token types the language server
// advertises in its legend (pkg/belt/lsp/semantic.go). They are the vocabulary
// that unifies the highlight paths: the LSP's accurate colours, the cold-start
// TextMate scopes, and — next, per the C-2 plan — the tree-sitter captures.
// Each path names the same category in its own convention, so a theme that
// colours the semantic `keyword` and the TextMate keyword.control alike sees no
// shift when the server comes up.
//
// Only the lexically-decidable categories get cold-start projections below: an
// identifier's role (type, reference, declaration) is not a lexical fact, so it
// is left to the semantic tokens and keyword/comment/number/string/operator are
// the whole cold-start subset.

// langName is the language identity shared across every generated artifact: the
// TextMate scope is source.<langName>, each scope ends in ".<langName>", and
// (next) the tree-sitter parser is named <langName>. The file extension is
// .belt. Keeping it in one place is decision §6 of the C-2 plan — one identity
// across all targets.
const langName = "masterbelt"

// The canonical category names — the server's semantic token types
// (pkg/belt/lsp/semantic.go's legend). Every lexCategory's semantic field is
// one of these; canonicalCategories holds the full set for the invariant test.
const (
	catKeyword    = "keyword"
	catComment    = "comment"
	catType       = "type"
	catVariable   = "variable"
	catNumber     = "number"
	catString     = "string"
	catOperator   = "operator"
	catNamespace  = "namespace"
	catProperty   = "property"
	catMethod     = "method"
	catParameter  = "parameter"
	catFunction   = "function"
	catEnumMember = "enumMember"
)

// canonicalCategories is the legend's vocabulary. A lexCategory may only name a
// category in this set, so a cold-start scope can never claim a colour the
// server is unable to also emit. TestCategoriesAreCanonical pins it.
var canonicalCategories = map[string]bool{
	catKeyword: true, catComment: true, catType: true, catVariable: true,
	catNumber: true, catString: true, catOperator: true, catNamespace: true,
	catProperty: true, catMethod: true, catParameter: true, catFunction: true,
	catEnumMember: true,
}

// lexCategory is one cold-start highlight category: the canonical category it
// belongs to and the per-target projections of it. The TextMate scope is one
// such projection; the tree-sitter capture (next) is another. Two consumers,
// one category.
type lexCategory struct {
	// semantic is the canonical category — one of canonicalCategories. It ties
	// the cold-start scope to the accurate colour the server later emits for the
	// same text.
	semantic string
	// tmScopeBase is the TextMate scope this category projects to, without the
	// trailing ".<langName>" the grammar appends (tmScope adds it).
	tmScopeBase string
}

// tmScope is the category's full TextMate scope: the base qualified by the
// language name (keyword.control -> keyword.control.masterbelt).
func (c lexCategory) tmScope() string { return c.tmScopeBase + "." + langName }

// The cold-start categories, keyed by the lexical class they colour. Each pairs
// a canonical semantic category with the TextMate scope VS Code falls back to
// for that semantic token type, so the two layers cannot drift per class.
var (
	// Keywords and the accessor/static context modifiers (get/set/static) both
	// land on keyword.control, the semantic `keyword` fallback.
	lexKeyword = lexCategory{catKeyword, "keyword.control"}

	// The three comment shapes the lexer scans.
	lexCommentDoc   = lexCategory{catComment, "comment.line.documentation"}
	lexCommentLine  = lexCategory{catComment, "comment.line.double-slash"}
	lexCommentBlock = lexCategory{catComment, "comment.block"}

	// The numeric literals. Datetime and duration colour as numbers too (their
	// semantic token is `number`); only the TextMate sub-scope distinguishes
	// them, so a theme without a datetime colour still numbers them.
	lexNumberDatetime = lexCategory{catNumber, "constant.numeric.datetime"}
	lexNumberDuration = lexCategory{catNumber, "constant.numeric.duration"}
	lexNumberInteger  = lexCategory{catNumber, "constant.numeric.integer"}

	// A double-quoted string and the escapes inside it; the escape is part of
	// the string, so it carries the same canonical category.
	lexString       = lexCategory{catString, "string.quoted.double"}
	lexStringEscape = lexCategory{catString, "constant.character.escape"}

	// The operator spellings sit under keyword.operator, their semantic
	// fallback; the range and assignment forms add a sub-scope.
	lexOperatorRange  = lexCategory{catOperator, "keyword.operator.range"}
	lexOperatorAssign = lexCategory{catOperator, "keyword.operator.assignment"}
	lexOperator       = lexCategory{catOperator, "keyword.operator"}
)
