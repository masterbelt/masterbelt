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
			}
			continue
		}

		node, _ := child.Node()
		switch node.Kind() {
		case cst.TypeClause:
			typ = lowerTypeClause(child, buf)
		case cst.Initializer:
			value = lowerInitializer(child, buf)
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
		}
	}
	return ast.NewMethodDecl(doc, public, extern, effects, name, typeParams, params, result, body, green)
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
// A member with a Block is a provided method (its body the default); one without
// is a required method.
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
		}
	}
	return ast.NewInterfaceMember(doc, public, name, typeParams, params, result, body, green)
}
