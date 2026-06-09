// This file lowers type-expression CST nodes into ast type expressions: named,
// builtin, union, record, and function types, together with the generic
// parameters and arguments they carry.

package abstract

import (
	"github.com/masterbelt/masterbelt/pkg/source"
	"github.com/masterbelt/masterbelt/pkg/source/ast"
	"github.com/masterbelt/masterbelt/pkg/source/cst"
	"github.com/masterbelt/masterbelt/pkg/source/token"
)

// isTypeExprKind reports whether a CST node kind is a type-expression node.
func isTypeExprKind(k cst.Kind) bool {
	switch k {
	case cst.TypeName, cst.UnionType, cst.RecordType, cst.FuncType, cst.BuiltinType:
		return true
	default:
		return false
	}
}

// lowerGenericParams lowers a GenericParams node to its type parameters.
func lowerGenericParams(t cst.Tree, buf source.Buffer) []*ast.TypeParam {
	var params []*ast.TypeParam
	for _, child := range t.Children() {
		if node, ok := child.Node(); ok && node.Kind() == cst.GenericParam {
			params = append(params, lowerGenericParam(child, buf))
		}
	}
	return params
}

// lowerGenericParam lowers one type parameter: its name and optional constraint.
func lowerGenericParam(t cst.Tree, buf source.Buffer) *ast.TypeParam {
	green, _ := t.Node()
	var name string
	var constraint ast.TypeExpr
	for _, child := range t.Children() {
		if tok, ok := child.Token(); ok {
			if tok.Kind() == token.Ident && name == "" {
				name = child.Text(buf)
			}
			continue
		}
		if node, ok := child.Node(); ok && isTypeExprKind(node.Kind()) {
			constraint = lowerTypeExpr(child, buf)
		}
	}
	return ast.NewTypeParam(name, constraint, green)
}

// lowerTypeExpr lowers a positioned type-expression CST node to its ast.TypeExpr.
func lowerTypeExpr(t cst.Tree, buf source.Buffer) ast.TypeExpr {
	node, ok := t.Node()
	if !ok {
		return nil
	}
	switch node.Kind() {
	case cst.TypeName:
		return lowerTypeName(t, buf, node)
	case cst.UnionType:
		return lowerUnionType(t, buf, node)
	case cst.RecordType:
		return lowerRecordType(t, buf, node)
	case cst.FuncType:
		return lowerFuncType(t, buf, node)
	case cst.BuiltinType:
		return lowerBuiltinType(t, buf, node)
	default:
		return nil
	}
}

// lowerBuiltinType lowers a BuiltinType node (its optional generic arguments).
func lowerBuiltinType(t cst.Tree, buf source.Buffer, node *cst.Node) ast.TypeExpr {
	var args []ast.TypeExpr
	for _, child := range t.Children() {
		if n, ok := child.Node(); ok && n.Kind() == cst.GenericArgs {
			args = lowerGenericArgs(child, buf)
		}
	}
	return ast.NewBuiltinType(args, node)
}

// lowerTypeName lowers a TypeName node: an identifier — qualified by a
// namespace import as in geo.Point — with optional generic arguments, or the
// self/null type keyword. A dangling qualifier (geo.) keeps its namespace and
// leaves the name empty, as elsewhere for recovered parts.
func lowerTypeName(t cst.Tree, buf source.Buffer, node *cst.Node) ast.TypeExpr {
	var idents []string
	var namespace, name string
	var dotted bool
	var args []ast.TypeExpr
	for _, child := range t.Children() {
		if tok, ok := child.Token(); ok {
			switch tok.Kind() {
			case token.Ident:
				idents = append(idents, child.Text(buf))
			case token.Dot:
				dotted = true
			case token.Self:
				name = "self"
			case token.Null:
				name = "null"
			case token.Type:
				name = "type" // the metatype, a builtin type name (type : type)
			default:
				// Any other token (the generic-argument angle brackets and
				// commas) names no part of the type: it is skipped.
			}
			continue
		}
		if n, ok := child.Node(); ok && n.Kind() == cst.GenericArgs {
			args = lowerGenericArgs(child, buf)
		}
	}
	switch {
	case len(idents) >= 2:
		namespace, name = idents[0], idents[1]
	case len(idents) == 1 && dotted:
		namespace = idents[0] // geo. — the qualified name is missing
	case len(idents) == 1:
		name = idents[0]
	}
	return ast.NewNamedType(namespace, name, args, node)
}

// lowerGenericArgs lowers a GenericArgs node to its type arguments.
func lowerGenericArgs(t cst.Tree, buf source.Buffer) []ast.TypeExpr {
	var args []ast.TypeExpr
	for _, child := range t.Children() {
		if node, ok := child.Node(); ok && isTypeExprKind(node.Kind()) {
			args = append(args, lowerTypeExpr(child, buf))
		}
	}
	return args
}

// lowerUnionType lowers a UnionType node to its member types.
func lowerUnionType(t cst.Tree, buf source.Buffer, node *cst.Node) ast.TypeExpr {
	var members []ast.TypeExpr
	for _, child := range t.Children() {
		if n, ok := child.Node(); ok && isTypeExprKind(n.Kind()) {
			members = append(members, lowerTypeExpr(child, buf))
		}
	}
	return ast.NewUnionType(members, node)
}

// lowerRecordType lowers a RecordType node to its fields.
func lowerRecordType(t cst.Tree, buf source.Buffer, node *cst.Node) ast.TypeExpr {
	var fields []*ast.FieldDef
	for _, child := range t.Children() {
		if n, ok := child.Node(); ok && n.Kind() == cst.Field {
			fields = append(fields, lowerField(child, buf))
		}
	}
	return ast.NewRecordType(fields, node)
}

// lowerField lowers one record field: its name and type.
func lowerField(t cst.Tree, buf source.Buffer) *ast.FieldDef {
	green, _ := t.Node()
	var name string
	var typ ast.TypeExpr
	for _, child := range t.Children() {
		if tok, ok := child.Token(); ok {
			if isNameToken(tok.Kind()) && name == "" {
				name = child.Text(buf)
			}
			continue
		}
		if n, ok := child.Node(); ok && isTypeExprKind(n.Kind()) {
			typ = lowerTypeExpr(child, buf)
		}
	}
	return ast.NewFieldDef(name, typ, green)
}

// lowerFuncType lowers a FuncType node: its parameter list and result type.
func lowerFuncType(t cst.Tree, buf source.Buffer, node *cst.Node) ast.TypeExpr {
	var params []*ast.ParamDef
	var result ast.TypeExpr
	for _, child := range t.Children() {
		n, ok := child.Node()
		if !ok {
			continue
		}
		switch {
		case n.Kind() == cst.ParamList:
			params = lowerParamList(child, buf)
		case isTypeExprKind(n.Kind()):
			result = lowerTypeExpr(child, buf)
		}
	}
	return ast.NewFuncType(params, result, node)
}
