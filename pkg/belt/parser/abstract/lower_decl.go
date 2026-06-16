// This file lowers top-level and member declarations — use, const, assert,
// function, and type declarations together with the impl methods they carry —
// from their CST nodes into the matching ast declaration nodes.

package abstract

import (
	"github.com/masterbelt/masterbelt/pkg/source"
	"github.com/masterbelt/masterbelt/pkg/source/ast"
	"github.com/masterbelt/masterbelt/pkg/source/cst"
	"github.com/masterbelt/masterbelt/pkg/source/token"
)

// lowerUseDecl lowers a positioned UseDecl CST node into an ast.UseDecl. The
// target's shape decides which field is set: a direct Ident is a namespace
// import, a UseList child carries the selective names, and a Star leaf marks
// the wildcard. The path is decoded from its string literal.
func lowerUseDecl(t cst.Tree, buf source.Buffer) *ast.UseDecl {
	green, _ := t.Node()

	var (
		public    bool
		namespace string
		names     []string
		star      bool
		path      string
	)

	for _, child := range t.Children() {
		if tok, ok := child.Token(); ok {
			switch tok.Kind() {
			case token.Pub:
				public = true
			case token.Ident:
				// The only direct Ident child is the namespace name; the
				// selective names are nested in the UseList node.
				namespace = child.Text(buf)
			case token.Star:
				star = true
			case token.String:
				path = decodeString(child.Text(buf))
			default:
				// Any other token (the "use"/"from" keywords, the dot, commas)
				// sets no field of the import: it is skipped.
			}
			continue
		}

		if node, _ := child.Node(); node.Kind() == cst.UseList {
			names = lowerUseList(child, buf)
		}
	}

	return ast.NewUseDecl(public, namespace, names, star, path, green)
}

// lowerUseList lowers a selective-import list to its names, in source order.
func lowerUseList(t cst.Tree, buf source.Buffer) []string {
	var names []string
	for _, child := range t.Children() {
		if tok, ok := child.Token(); ok && tok.Kind() == token.Ident {
			names = append(names, child.Text(buf))
		}
	}
	return names
}

// lowerConstDecl lowers a positioned ConstDecl CST node into an ast.ConstDecl.
// It reads identifier and literal text from buf at the node's resolved offsets;
// the resulting strings are baked into the AST node, so the node no longer
// depends on the buffer or on where the declaration sits.
func lowerConstDecl(t cst.Tree, buf source.Buffer) *ast.ConstDecl {
	green, _ := t.Node()

	var (
		doc    []string
		public bool
		name   string
		typ    ast.TypeExpr
		value  ast.Expr
	)

	for _, child := range t.Children() {
		if tok, ok := child.Token(); ok {
			switch tok.Kind() {
			case token.Pub:
				public = true
			case token.DocComment:
				doc = append(doc, docText(child.Text(buf)))
			case token.Ident:
				// The only direct Ident child of a ConstDecl is the declared
				// name; the type and value identifiers are nested in TypeClause
				// and Initializer nodes.
				name = child.Text(buf)
			default:
				// Any other token (the "const" keyword, ":" or "=") sets no
				// field of the constant: it is skipped.
			}
			continue
		}

		node, _ := child.Node()
		switch node.Kind() {
		case cst.TypeClause:
			typ = lowerTypeClause(child, buf)
		case cst.Initializer:
			value = lowerInitializer(child, buf)
		default:
			// Any other child node is neither the annotation nor the value
			// of the constant: it contributes nothing.
		}
	}

	return ast.NewConstDecl(doc, public, name, typ, value, green)
}

// lowerAssertDecl lowers a positioned AssertDecl CST node into an
// ast.AssertDecl: its doc-comment lines and the asserted expression, nil when
// the expression is missing (a recovered "assert").
func lowerAssertDecl(t cst.Tree, buf source.Buffer) *ast.AssertDecl {
	green, _ := t.Node()

	var doc []string
	var cond ast.Expr
	for _, child := range t.Children() {
		if tok, ok := child.Token(); ok {
			if tok.Kind() == token.DocComment {
				doc = append(doc, docText(child.Text(buf)))
			}
			continue
		}
		if node, _ := child.Node(); isExprKind(node.Kind()) {
			cond = lowerExpr(child, buf)
		}
	}
	return ast.NewAssertDecl(doc, cond, green)
}

// lowerFuncDecl lowers a positioned FuncDecl CST node into an ast.FuncDecl:
// its modifiers, name, parameters, result type, and body. The two body forms
// normalize here — and only here — exactly as a function literal's do: an
// arrow body ("->" Expr) becomes a single implicit return, so inference,
// lowering, and evaluation see one body shape. The kinds keep the children
// apart: the result type is a type-expression node, the arrow body an
// expression node.
func lowerFuncDecl(t cst.Tree, buf source.Buffer) *ast.FuncDecl {
	green, _ := t.Node()
	var (
		doc        []string
		public     bool
		extern     bool
		effects    []string
		name       string
		typeParams []*ast.TypeParam
		params     []*ast.ParamDef
		result     ast.TypeExpr
		body       []ast.Stmt
	)
	for _, child := range t.Children() {
		if tok, ok := child.Token(); ok {
			switch tok.Kind() {
			case token.Pub:
				public = true
			case token.Extern:
				extern = true
			case token.Io, token.Async, token.Nondet:
				effects = append(effects, child.Text(buf))
			case token.DocComment:
				doc = append(doc, docText(child.Text(buf)))
			case token.Ident:
				// The only direct Ident child is the declared name; the
				// parameter and type names are nested in their own nodes.
				name = child.Text(buf)
			default:
				// Any other token (the "fn" keyword, parens, the "->" arrow)
				// sets no field of the function: it is skipped.
			}
			continue
		}
		node, _ := child.Node()
		switch {
		case node.Kind() == cst.GenericParams:
			typeParams = lowerGenericParams(child, buf)
		case node.Kind() == cst.ParamList:
			params = lowerParamList(child, buf)
		case node.Kind() == cst.Block:
			body = lowerBlock(child, buf)
		case isTypeExprKind(node.Kind()):
			result = lowerTypeExpr(child, buf)
		case isExprKind(node.Kind()):
			body = []ast.Stmt{ast.NewReturnStmt(lowerExpr(child, buf), node)}
		}
	}
	return ast.NewFuncDecl(doc, public, extern, effects, name, typeParams, params, result, body, green)
}

// lowerTypeClause lowers a ": Type" clause to its type expression, or nil when
// the type is missing (a recovered "const x: = 1"). The annotation is a full
// type expression, lowered the same way a type declaration's is.
func lowerTypeClause(t cst.Tree, buf source.Buffer) ast.TypeExpr {
	for _, child := range t.Children() {
		if node, ok := child.Node(); ok && isTypeExprKind(node.Kind()) {
			return lowerTypeExpr(child, buf)
		}
	}
	return nil
}

// lowerInitializer lowers an "= Expr" clause to its expression, or nil when the
// expression is missing (a recovered "const x =").
func lowerInitializer(t cst.Tree, buf source.Buffer) ast.Expr {
	for _, child := range t.Children() {
		if node, ok := child.Node(); ok && isExprKind(node.Kind()) {
			return lowerExpr(child, buf)
		}
	}
	return nil
}

// lowerTypeDecl lowers a positioned TypeDecl CST node into an ast.TypeDecl.
func lowerTypeDecl(t cst.Tree, buf source.Buffer) *ast.TypeDecl {
	green, _ := t.Node()

	var (
		doc     []string
		public  bool
		name    string
		params  []*ast.TypeParam
		body    ast.TypeExpr
		where   ast.Expr
		methods []*ast.MethodDecl
		consts  []*ast.ConstDecl
		impls   []ast.TypeExpr
	)
	for _, child := range t.Children() {
		if tok, ok := child.Token(); ok {
			switch tok.Kind() {
			case token.Pub:
				public = true
			case token.DocComment:
				doc = append(doc, docText(child.Text(buf)))
			case token.Ident, token.Null:
				// The declared name: an identifier, or the null keyword (null is
				// a builtin type, declarable as `type null = builtin`). Generic
				// parameters and the body's names are nested in their own nodes.
				name = child.Text(buf)
			default:
				// Any other token (the "type" keyword, "=", "where") sets no
				// field of the type declaration: it is skipped.
			}
			continue
		}
		node, _ := child.Node()
		switch {
		case node.Kind() == cst.GenericParams:
			params = lowerGenericParams(child, buf)
		case node.Kind() == cst.WhereClause:
			where = lowerWhereClause(child, buf)
		case node.Kind() == cst.ImplBlock:
			// A type may carry several impl blocks (an inherent one and one per
			// interface). Their methods and consts flatten together; each tagged
			// block's interface name joins Impls.
			ms, cs, iface := lowerImpl(child, buf)
			methods = append(methods, ms...)
			consts = append(consts, cs...)
			if iface != nil {
				impls = append(impls, iface)
			}
		case isTypeExprKind(node.Kind()):
			body = lowerTypeExpr(child, buf)
		}
	}
	return ast.NewTypeDecl(doc, public, name, params, body, where, methods, consts, impls, green)
}

// lowerMasterDecl lowers a positioned MasterDecl CST node into an
// ast.MasterDecl: its modifiers, name, the row record body (reusing the
// type-body lowering — record type, where-refinement, and impl members), and
// the primary-key columns.
func lowerMasterDecl(t cst.Tree, buf source.Buffer) *ast.MasterDecl {
	green, _ := t.Node()

	var (
		doc         []string
		public      bool
		name        string
		seenBody    bool
		record      ast.TypeExpr
		where       ast.Expr
		methods     []*ast.MethodDecl
		consts      []*ast.ConstDecl
		impls       []ast.TypeExpr
		primary     []string
		sources     []*ast.SourceEntry
		validations []*ast.ValidateClause
	)
	for _, child := range t.Children() {
		if tok, ok := child.Token(); ok {
			switch tok.Kind() {
			case token.Pub:
				public = true
			case token.DocComment:
				doc = append(doc, docText(child.Text(buf)))
			case token.LBrace:
				seenBody = true
			case token.Ident:
				// The declared name is the identifier between the master keyword
				// (wrapped in a MasterKeyword node) and the body brace. An
				// identifier the member recovery appends directly under the master
				// — a stray token in a malformed body — comes after the brace and
				// must not overwrite the name.
				if !seenBody {
					name = child.Text(buf)
				}
			default:
				// Any other token (the closing brace) sets no field of the master:
				// it is skipped.
			}
			continue
		}
		node, _ := child.Node()
		switch node.Kind() {
		case cst.MasterRecord:
			record, where, methods, consts, impls = lowerMasterRecord(child, buf)
		case cst.MasterPrimary:
			primary = lowerMasterPrimary(child, buf)
		case cst.MasterSource:
			sources = append(sources, lowerMasterSource(child, buf)...)
		case cst.MasterValidate:
			validations = append(validations, lowerMasterValidate(child, buf)...)
		default:
			// Any other child node (the MasterKeyword wrapping the master keyword)
			// contributes no field of the master.
		}
	}
	return ast.NewMasterDecl(doc, public, name, record, where, methods, consts, impls, primary, sources, validations, green)
}

// lowerMasterValidate lowers a master's validate member to its clauses, in
// declaration order. Each ValidateClause child becomes one ast.ValidateClause;
// the validate keyword (a MasterKeyword node) and the braces carry no clause.
func lowerMasterValidate(t cst.Tree, buf source.Buffer) []*ast.ValidateClause {
	var clauses []*ast.ValidateClause
	for _, child := range t.Children() {
		if node, ok := child.Node(); ok && node.Kind() == cst.ValidateClause {
			clauses = append(clauses, lowerValidateClause(child, buf))
		}
	}
	return clauses
}

// lowerValidateClause lowers one clause of a validate block: its scope keyword
// (each, a per-row check — all, per-table, is a later concern) and the statement
// body the keyword introduces. The scope is read from the MasterKeyword's text;
// the body is the Block's statements, lowered like any statement body.
func lowerValidateClause(t cst.Tree, buf source.Buffer) *ast.ValidateClause {
	green, _ := t.Node()
	perRow := true
	var body []ast.Stmt
	for _, child := range t.Children() {
		node, ok := child.Node()
		if !ok {
			continue
		}
		switch node.Kind() {
		case cst.MasterKeyword:
			perRow = masterKeywordText(child, buf) != "all"
		case cst.Block:
			body = lowerBlock(child, buf)
		default:
			// No other child carries the clause's scope or body.
		}
	}
	return ast.NewValidateClause(perRow, body, green)
}

// masterKeywordText returns the text of the context keyword a MasterKeyword node
// wraps (master, record, primary, source, validate, each, or all).
func masterKeywordText(t cst.Tree, buf source.Buffer) string {
	for _, child := range t.Children() {
		if _, ok := child.Token(); ok {
			return child.Text(buf)
		}
	}
	return ""
}

// lowerMasterSource lowers a master's source member to its entries, in
// declaration order. Each SourceEntry child becomes one ast.SourceEntry; the
// source keyword (a MasterKeyword node) and the braces carry no entry.
func lowerMasterSource(t cst.Tree, buf source.Buffer) []*ast.SourceEntry {
	var entries []*ast.SourceEntry
	for _, child := range t.Children() {
		if node, ok := child.Node(); ok && node.Kind() == cst.SourceEntry {
			entries = append(entries, lowerSourceEntry(child, buf))
		}
	}
	return entries
}

// lowerSourceEntry lowers one entry of a source block: the format name (the first
// Ident), the locator (the String token, with its quoting decoded), and the
// options (the RecordLit expression, when present). The format name and locator
// are read as their decoded values; the options are lowered as the record-literal
// expression a later type-check reads.
func lowerSourceEntry(t cst.Tree, buf source.Buffer) *ast.SourceEntry {
	green, _ := t.Node()
	var (
		format  string
		locator string
		options ast.Expr
	)
	for _, child := range t.Children() {
		if tok, ok := child.Token(); ok {
			switch tok.Kind() {
			case token.Ident:
				if format == "" {
					format = child.Text(buf)
				}
			case token.String:
				locator = decodeString(child.Text(buf))
			default:
				// Whitespace and newline trivia carry nothing.
			}
			continue
		}
		if node, ok := child.Node(); ok && node.Kind() == cst.RecordLit {
			options = lowerExpr(child, buf)
		}
	}
	return ast.NewSourceEntry(format, locator, options, green)
}

// lowerMasterRecord lowers a master's record member — the row type and the
// members of its impl blocks — reusing the type-declaration body lowering: the
// type expression is the record type, a WhereClause its refinement over a row,
// and each ImplBlock its methods, associated constants, and interface tag. The
// record keyword (a MasterKeyword node) carries no field. The lowering mirrors
// the body handling in lowerTypeDecl, so a master's rows and a type share one
// path.
func lowerMasterRecord(t cst.Tree, buf source.Buffer) (record ast.TypeExpr, where ast.Expr, methods []*ast.MethodDecl, consts []*ast.ConstDecl, impls []ast.TypeExpr) {
	for _, child := range t.Children() {
		node, ok := child.Node()
		if !ok {
			continue
		}
		switch {
		case node.Kind() == cst.WhereClause:
			where = lowerWhereClause(child, buf)
		case node.Kind() == cst.ImplBlock:
			ms, cs, iface := lowerImpl(child, buf)
			methods = append(methods, ms...)
			consts = append(consts, cs...)
			if iface != nil {
				impls = append(impls, iface)
			}
		case isTypeExprKind(node.Kind()):
			record = lowerTypeExpr(child, buf)
		}
	}
	return record, where, methods, consts, impls
}

// lowerMasterPrimary lowers a master's primary member to its key column names,
// in declaration order: each direct Ident child is one column (the primary
// keyword is wrapped in a MasterKeyword node, and the parentheses and commas of
// a composite key are non-Ident tokens), so the keyword and punctuation are
// read apart from the columns.
func lowerMasterPrimary(t cst.Tree, buf source.Buffer) []string {
	var keys []string
	for _, child := range t.Children() {
		if tok, ok := child.Token(); ok && tok.Kind() == token.Ident {
			keys = append(keys, child.Text(buf))
		}
	}
	return keys
}

// lowerEnumDecl lowers a positioned EnumDecl CST node into an ast.EnumDecl: its
// modifiers, name, optional base-type annotation (a TypeClause, lowered the
// same way a const's is), members in declaration order, and the methods of its
// impl block. The base is the only direct type-expression child; the member
// values live inside their EnumMember nodes.
func lowerEnumDecl(t cst.Tree, buf source.Buffer) *ast.EnumDecl {
	green, _ := t.Node()

	var (
		doc     []string
		public  bool
		name    string
		base    ast.TypeExpr
		members []*ast.EnumMember
		methods []*ast.MethodDecl
		consts  []*ast.ConstDecl
		impls   []ast.TypeExpr
	)
	for _, child := range t.Children() {
		if tok, ok := child.Token(); ok {
			switch tok.Kind() {
			case token.Pub:
				public = true
			case token.DocComment:
				doc = append(doc, docText(child.Text(buf)))
			case token.Ident:
				// The only direct Ident child of an EnumDecl is the declared
				// name; the base type sits in a TypeClause and the member names
				// in their EnumMember nodes.
				name = child.Text(buf)
			default:
				// Any other token (the "enum" keyword, the braces) sets no
				// field of the enum: it is skipped.
			}
			continue
		}
		node, _ := child.Node()
		switch node.Kind() {
		case cst.TypeClause:
			base = lowerTypeClause(child, buf)
		case cst.EnumMember:
			members = append(members, lowerEnumMember(child, buf))
		case cst.ImplBlock:
			ms, cs, iface := lowerImpl(child, buf)
			methods = append(methods, ms...)
			consts = append(consts, cs...)
			if iface != nil {
				impls = append(impls, iface)
			}
		default:
			// Any other child node is not part of the enum (base, member, or
			// impl block): it contributes nothing.
		}
	}
	return ast.NewEnumDecl(doc, public, name, base, members, methods, consts, impls, green)
}

// lowerEnumMember lowers one EnumMember node: its name and the optional "=
// ConstExpr" value (nil when the initializer is omitted).
func lowerEnumMember(t cst.Tree, buf source.Buffer) *ast.EnumMember {
	green, _ := t.Node()
	var name string
	var value ast.Expr
	for _, child := range t.Children() {
		if tok, ok := child.Token(); ok {
			if tok.Kind() == token.Ident && name == "" {
				name = child.Text(buf)
			}
			continue
		}
		if node, _ := child.Node(); node.Kind() == cst.Initializer {
			value = lowerInitializer(child, buf)
		}
	}
	return ast.NewEnumMember(name, value, green)
}

// lowerWhereClause lowers a "where Expr" clause to its predicate expression, or
// nil when the predicate is missing (a recovered "type T = int8 where").
func lowerWhereClause(t cst.Tree, buf source.Buffer) ast.Expr {
	for _, child := range t.Children() {
		if node, ok := child.Node(); ok && isExprKind(node.Kind()) {
			return lowerExpr(child, buf)
		}
	}
	return nil
}

// lowerImpl lowers an ImplBlock node to its method declarations, its associated
// constants (the ConstDecl items), and its optional interface tag — the
// TypeName after impl that names the interface this block implements (impl
// foldable<int> { ... }), or nil for a bare inherent impl. The methods and
// consts separate here since the later layers treat a method and a type-scoped
// constant differently; the interface tag is collected on the type so the
// nominal-satisfaction check can read which interfaces the type opts into.
func lowerImpl(t cst.Tree, buf source.Buffer) (methods []*ast.MethodDecl, consts []*ast.ConstDecl, iface ast.TypeExpr) {
	for _, child := range t.Children() {
		n, ok := child.Node()
		if !ok {
			continue
		}
		switch {
		case n.Kind() == cst.MethodDecl:
			methods = append(methods, lowerMethod(child, buf))
		case n.Kind() == cst.ConstDecl:
			consts = append(consts, lowerImplConst(child, buf))
		case isTypeExprKind(n.Kind()):
			// The only type-expression child of an impl block is its interface
			// tag (the TypeName after impl).
			iface = lowerTypeExpr(child, buf)
		}
	}
	return methods, consts, iface
}

// lowerImplConst lowers an associated-constant ConstDecl node inside an impl
// block. It mirrors lowerConstDecl, with the one extra form a top-level const
// cannot have: a `= builtin` initializer, whose Initializer wraps a BuiltinType
// rather than an expression. Such a constant carries no Value — its value comes
// from the builtin registry — and is marked Builtin.
func lowerImplConst(t cst.Tree, buf source.Buffer) *ast.ConstDecl {
	green, _ := t.Node()

	var (
		doc     []string
		public  bool
		name    string
		typ     ast.TypeExpr
		value   ast.Expr
		builtin bool
	)

	for _, child := range t.Children() {
		if tok, ok := child.Token(); ok {
			switch tok.Kind() {
			case token.Pub:
				public = true
			case token.DocComment:
				doc = append(doc, docText(child.Text(buf)))
			case token.Ident:
				name = child.Text(buf)
			default:
				// Any other token (the "const" keyword, ":" or "=") sets no
				// field of the associated constant: it is skipped.
			}
			continue
		}
		node, _ := child.Node()
		switch node.Kind() {
		case cst.TypeClause:
			typ = lowerTypeClause(child, buf)
		case cst.Initializer:
			if initializerIsBuiltin(child) {
				builtin = true
			} else {
				value = lowerInitializer(child, buf)
			}
		default:
			// Any other child node is neither the annotation nor the
			// initializer of the associated constant: it contributes nothing.
		}
	}

	return ast.NewAssocConstDecl(doc, public, name, typ, value, builtin, green)
}

// initializerIsBuiltin reports whether an Initializer node is the `= builtin`
// form: it wraps a BuiltinType rather than an expression.
func initializerIsBuiltin(t cst.Tree) bool {
	for _, child := range t.Children() {
		if node, ok := child.Node(); ok && node.Kind() == cst.BuiltinType {
			return true
		}
	}
	return false
}

// lowerMethod lowers a MethodDecl node: its modifiers, effects, name,
// parameters, result type, and statement body.
func lowerMethod(t cst.Tree, buf source.Buffer) *ast.MethodDecl {
	green, _ := t.Node()
	var (
		doc        []string
		public     bool
		extern     bool
		kind       = ast.MethodNormal
		effects    []string
		name       string
		typeParams []*ast.TypeParam
		params     []*ast.ParamDef
		result     ast.TypeExpr
		body       []ast.Stmt
	)
	for _, child := range t.Children() {
		if tok, ok := child.Token(); ok {
			switch tok.Kind() {
			case token.Pub:
				public = true
			case token.Extern:
				extern = true
			case token.Io, token.Async, token.Nondet:
				effects = append(effects, child.Text(buf))
			case token.DocComment:
				doc = append(doc, docText(child.Text(buf)))
			case token.Ident:
				if name == "" {
					name = child.Text(buf)
				}
			default:
				// A keyword in the name position — `fn where(...)` — is a usable
				// method name, read the same as an Ident (the parser accepts it
				// there). A declaration marker (fn, and the effects below in their own
				// cases) is structural, not a name; pub/extern have their own cases
				// above. So only a non-marker keyword in the name slot sets the name.
				if name == "" && tok.Kind().Keyword() && !tok.Kind().MethodMarker() {
					name = child.Text(buf)
				}
			}
			continue
		}
		node, _ := child.Node()
		switch {
		case node.Kind() == cst.Modifier:
			// The accessor/static modifier the parser recognized. Its kind is read
			// from the context-keyword identifier it wraps, and it is consumed here
			// — never as the method name — so the name picks up the next Ident.
			kind = modifierKind(child, buf)
		case node.Kind() == cst.GenericParams:
			typeParams = lowerGenericParams(child, buf)
		case node.Kind() == cst.ParamList:
			params = lowerParamList(child, buf)
		case node.Kind() == cst.Block:
			body = lowerBlock(child, buf)
		case isTypeExprKind(node.Kind()):
			result = lowerTypeExpr(child, buf)
		}
	}
	return ast.NewMethodDecl(doc, public, extern, kind, effects, name, typeParams, params, result, body, green)
}

// modifierKind reads the method kind from a Modifier node: the get/set/static
// context keyword it wraps. An unrecognized text (which the parser does not
// produce) reads as MethodNormal.
func modifierKind(t cst.Tree, buf source.Buffer) ast.MethodKind {
	for _, child := range t.Children() {
		tok, ok := child.Token()
		if !ok || tok.Kind() != token.Ident {
			continue
		}
		switch child.Text(buf) {
		case "get":
			return ast.MethodGetter
		case "set":
			return ast.MethodSetter
		case "static":
			return ast.MethodStatic
		}
	}
	return ast.MethodNormal
}

// lowerInterfaceDecl lowers a positioned InterfaceDecl CST node into an
// ast.InterfaceDecl: its modifiers, name, generic parameters, parents
// (supertraits), and members.
func lowerInterfaceDecl(t cst.Tree, buf source.Buffer) *ast.InterfaceDecl {
	green, _ := t.Node()
	var (
		doc     []string
		public  bool
		name    string
		params  []*ast.TypeParam
		parents []ast.TypeExpr
		members []*ast.InterfaceMember
	)
	for _, child := range t.Children() {
		if tok, ok := child.Token(); ok {
			switch tok.Kind() {
			case token.Pub:
				public = true
			case token.DocComment:
				doc = append(doc, docText(child.Text(buf)))
			case token.Ident:
				// The only direct Ident child is the declared name; the generic
				// parameters, parents, and member names are nested in their own nodes.
				name = child.Text(buf)
			default:
				// Any other token (the "interface" keyword, the braces) sets no
				// field of the interface: it is skipped.
			}
			continue
		}
		node, _ := child.Node()
		switch node.Kind() {
		case cst.GenericParams:
			params = lowerGenericParams(child, buf)
		case cst.InterfaceParents:
			parents = lowerInterfaceParents(child, buf)
		case cst.InterfaceMember:
			members = append(members, lowerInterfaceMember(child, buf))
		default:
			// Any other child node is not a generic-parameter list, a parents
			// clause, or a member: it contributes nothing.
		}
	}
	return ast.NewInterfaceDecl(doc, public, name, params, parents, members, green)
}

// lowerInterfaceParents lowers an InterfaceParents node into its parent type
// expressions: each TypeName child (a named interface, possibly applied) is one
// parent, in declaration order. The colon and commas are trivia to the lowering.
func lowerInterfaceParents(t cst.Tree, buf source.Buffer) []ast.TypeExpr {
	var parents []ast.TypeExpr
	for _, child := range t.Children() {
		if node, ok := child.Node(); ok && isTypeExprKind(node.Kind()) {
			parents = append(parents, lowerTypeExpr(child, buf))
		}
	}
	return parents
}

// lowerInterfaceMember lowers one InterfaceMember node: its modifiers, name,
// explicit type variables, parameters, result type, and optional default body.
// A member with a ParamList is a method requirement; one without is a readable-
// member requirement (Name: T). A member with a Block is a provided method (its
// body the default); one without is a required member.
func lowerInterfaceMember(t cst.Tree, buf source.Buffer) *ast.InterfaceMember {
	green, _ := t.Node()
	var (
		doc        []string
		public     bool
		name       string
		typeParams []*ast.TypeParam
		params     []*ast.ParamDef
		result     ast.TypeExpr
		body       []ast.Stmt
		hasParams  bool
		hasBody    bool
		static     bool
	)
	for _, child := range t.Children() {
		if tok, ok := child.Token(); ok {
			switch tok.Kind() {
			case token.Pub:
				public = true
			case token.DocComment:
				doc = append(doc, docText(child.Text(buf)))
			case token.Ident:
				if name == "" {
					name = child.Text(buf)
				}
			default:
				// A keyword in the name position names the member the same as an
				// Ident (where(): nint); a declaration marker is structural, not a
				// name. The fn keyword and the punctuation set no field either.
				if name == "" && tok.Kind().Keyword() && !tok.Kind().MethodMarker() {
					name = child.Text(buf)
				}
			}
			continue
		}
		node, _ := child.Node()
		switch {
		case node.Kind() == cst.Modifier:
			// The only interface modifier is static (a static-fn requirement); its
			// keyword precedes the name, so it is read here and never mistaken for it.
			static = modifierKind(child, buf) == ast.MethodStatic
		case node.Kind() == cst.GenericParams:
			typeParams = lowerGenericParams(child, buf)
		case node.Kind() == cst.ParamList:
			params = lowerParamList(child, buf)
			hasParams = true
		case node.Kind() == cst.Block:
			body = lowerBlock(child, buf)
			hasBody = true
		case isTypeExprKind(node.Kind()):
			result = lowerTypeExpr(child, buf)
		}
	}
	// No parameter list distinguishes a readable-member requirement (Name: T) from
	// a nullary method requirement (Name(): T), which both lower to empty Params.
	// A static modifier with no parameter list (static Name: T) sets both Readable
	// and Static, which the semantic layer rejects — a static requirement needs a
	// parameter list. hasBody records a written block even when empty, which lowers
	// to a nil Body.
	return ast.NewInterfaceMember(doc, public, name, !hasParams, static, hasBody, typeParams, params, result, body, green)
}
