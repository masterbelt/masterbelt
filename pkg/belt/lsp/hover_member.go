package lsp

import (
	"strings"

	protocol "github.com/owenrumney/go-lsp/lsp"

	"github.com/masterbelt/masterbelt/pkg/belt/types"
	"github.com/masterbelt/masterbelt/pkg/belt/types/infer"
	"github.com/masterbelt/masterbelt/pkg/source/ast"
	"github.com/masterbelt/masterbelt/pkg/source/cst"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
	"github.com/masterbelt/masterbelt/pkg/source/token"
)

// Hover cards for the members of a type: a method access or declaration, a
// method parameter, and the record field a receiver reads. Resolving the
// receiver's type (receiverTypeOf, paramTypeAt, fieldOf) and rendering a
// method's signature (methodSignature, methodSignatureSubst) live here too.

// methodParamHover describes the method parameter denoted at offset: its name
// in the signature's parameter list, or a reference to it inside the method's
// body. The type comes from the module's resolved signature — each resolved
// method carries its declaration (ir.Method.Syntax), so the pairing holds
// across overloads — and renders as the checker sees it. Function literals
// nest inside method bodies and their parameters shadow the method's —
// lambdaParamHover runs first.
func methodParamHover(doc view, offset int, trees map[cst.Green]cst.Tree) *protocol.Hover {
	buf := doc.Buffer()
	tok, name, ok := identAt(doc.AST().Concrete().Tree(), buf, offset)
	if !ok {
		return nil
	}

	for _, def := range doc.Module().Types {
		for _, irm := range def.Methods {
			if irm.Syntax == nil {
				continue
			}
			mt, found := trees[irm.Syntax.Syntax()]
			if !found || !within(mt, offset) {
				continue
			}
			for _, p := range irm.Params {
				if p.Name != name || p.Type == nil || p.Type == ir.Invalid {
					continue
				}
				r := toRange(buf, tok.Offset(), tok.End())
				return &protocol.Hover{
					Contents: protocol.MarkupContent{
						Kind:  protocol.Markdown,
						Value: "```masterbelt\n" + name + ": " + p.Type.String() + "\n```",
					},
					Range: &r,
				}
			}
		}
	}
	return nil
}

// methodSignature renders one method as it is declared: modifiers, name,
// parameters, and result, in source syntax.
func methodSignature(m *ir.Method) string {
	return methodSignatureSubst(m, nil)
}

// typeParamListSubst renders a generic parameter list with the receiver's solved
// type arguments substituted into each bound — so a method bound that mentions the
// receiver's own parameter (Box<int>.pick<U: wrapper<T>>) shows the pinned type
// (wrapper<int>), not the unbound owner variable. The parameter name itself is the
// method's own and stays unsolved; a nil or empty subst renders the bounds as
// declared, exactly as typeParamList does.
func typeParamListSubst(params []*ir.TypeParam, subst map[string]ir.Type) string {
	if len(params) == 0 {
		return ""
	}
	parts := make([]string, len(params))
	for i, p := range params {
		parts[i] = p.Name
		if p.Bound != nil {
			bound := p.Bound
			if len(subst) > 0 {
				bound = types.Substitute(bound, subst)
			}
			parts[i] += ": " + bound.String()
		}
	}
	return "<" + strings.Join(parts, ", ") + ">"
}

// methodSignatureSubst is methodSignature with the receiver's solved type
// arguments substituted in, so list<int8>.map shows fn(item: int8).
func methodSignatureSubst(m *ir.Method, subst map[string]ir.Type) string {
	render := func(t ir.Type) string {
		if t == nil {
			return ""
		}
		if len(subst) > 0 {
			t = types.Substitute(t, subst)
		}
		return t.String()
	}
	var b strings.Builder
	if m.Public {
		b.WriteString("pub ")
	}
	if m.Extern {
		b.WriteString("extern ")
	}
	// The accessor/static modifier leads the signature, the way it is written:
	// get name / set name / static fn name. An ordinary method carries none.
	switch m.Kind {
	case ir.MethodGetter:
		b.WriteString("get ")
	case ir.MethodSetter:
		b.WriteString("set ")
	case ir.MethodStatic:
		b.WriteString("static fn ")
	default:
		// An ordinary method (MethodNormal) carries no leading modifier:
		// nothing is written before its effects and name.
	}
	for _, eff := range m.Effects {
		b.WriteString(eff + " ")
	}
	b.WriteString(m.Name)
	// A generic method's own type parameters and their bounds, rendered as declared
	// (sum<T: numeric>) the way a function's signature does. The method's own variable
	// shows unsolved (the receiver pins the receiver's variables, not the method's),
	// but a bound that mentions the receiver's parameter takes its substitution too —
	// Box<int>.pick<U: wrapper<T>> shows U: wrapper<int>, not the unbound owner T.
	b.WriteString(typeParamListSubst(m.TypeParams, subst))
	b.WriteString("(")
	for i, p := range m.Params {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(p.Name)
		if p.Type != nil {
			b.WriteString(": " + render(p.Type))
		}
	}
	b.WriteString(")")
	if m.Result != nil {
		b.WriteString(": " + render(m.Result))
	}
	return b.String()
}

// memberHover describes the member access at offset: the method the
// receiver's type binds for the name — its signature with the receiver's
// generic arguments substituted in, and its doc — or the record field it
// reads, with its type.
func memberHover(doc view, offset int, trees map[cst.Green]cst.Tree) *protocol.Hover {
	leaf, parent, ok := leafAt(doc.AST().Concrete().Tree(), offset)
	if !ok {
		return nil
	}
	if k, isTok := leaf.TokenKind(); !isTok || k != token.Ident {
		return nil
	}
	if pk, isNode := parent.Kind(); !isNode || pk != cst.MemberExpr {
		return nil
	}
	parentNode, _ := parent.Node()

	// The AST member access backing this node. Operators desugar to synthetic
	// member accesses, but those share their operator's CST node, never a
	// MemberExpr node — only an access written in the source matches here.
	var member *ast.MemberExpr
	forEachExpr(doc.AST().File(), func(e ast.Expr) {
		if m, ok := e.(*ast.MemberExpr); ok && m.Syntax() == parentNode {
			member = m
		}
	})
	if member == nil {
		return nil
	}

	recv := receiverTypeOf(doc, member.Receiver, trees, offset, doc.ExprTypes())
	if recv == nil || recv == ir.Invalid {
		return nil
	}
	name := member.Member.Name
	r := toRange(doc.Buffer(), leaf.Offset(), leaf.End())

	if ms, subst, ok := doc.MethodCandidates(recv, name); ok {
		return memberMethodHover(ms, subst, r)
	}
	if f, ok := memberFieldOf(recv, name); ok {
		return memberFieldHover(f, r)
	}
	if g, subst, ok := receiverGetter(doc, recv, name); ok {
		return memberGetterHover(g, subst, r)
	}
	return nil
}

// memberMethodDefinition resolves a member-access method call — Cards.zero() (a
// master's static fn), Cards.expensive() (a scope entry, desugared to one), x.inc()
// (a value method), a getter or setter — to the declaration locations of the methods
// it names. It is the go-to-definition twin of memberHover: it resolves the receiver's
// type and the methods of that name the same way, then locates each method's
// declaration (every overload, in its own file). A method built outside a source
// declaration — a relation builtin assembled from the prelude, which carries no
// navigable view — contributes no location, so a query method like count resolves to
// nothing rather than a phantom position.
func memberMethodDefinition(doc view, offset int, trees map[cst.Green]cst.Tree) ([]protocol.Location, bool) {
	member, ok := memberAccessAt(doc, offset)
	if !ok {
		return nil, false
	}
	// A member access used as a call's callee — Cards.zero(), x.inc(), Type.eql(...) —
	// resolves through the checker's lowered call, so the value-binding, metatype, and
	// type-parameter rules the checker applied carry over instead of being re-derived.
	// A member access that is not a call (a field or getter read, a setter write) is a
	// distinct member space, so it never navigates a read to a method declaration.
	if call, ok := callWithCallee(doc, member); ok {
		if m, ok := doc.ResolvedMethodCall(call); ok {
			if locs := methodDeclLocations(doc, []*ir.Method{m}); len(locs) > 0 {
				return locs, true
			}
		}
		return nil, false
	}
	// A member read that is not a call resolves to a getter through the receiver's type;
	// a plain field read has no getter, so it navigates nowhere rather than to a
	// same-named setter or method.
	if recv := receiverTypeOf(doc, member.Receiver, trees, offset, doc.ExprTypes()); recv != nil && recv != ir.Invalid {
		if locs := methodDeclLocations(doc, accessorMethods(doc, recv, member.Member.Name, ir.MethodGetter)); len(locs) > 0 {
			return locs, true
		}
		return nil, false
	}
	// A member write resolves to a setter. The checker does not type an assignment
	// target's receiver (c.fahrenheit = v), so its type is read off the lowered setter
	// call — the one member access whose receiver receiverTypeOf cannot settle.
	if recv, ok := doc.AssignTargetReceiverType(member); ok {
		if locs := methodDeclLocations(doc, accessorMethods(doc, recv, member.Member.Name, ir.MethodSetter)); len(locs) > 0 {
			return locs, true
		}
	}
	return nil, false
}

// callWithCallee returns the call expression whose callee is member — so a member
// access is resolved as a method only when it is actually called — or false when the
// member access is a read or the left of a write, not a call.
func callWithCallee(doc view, member *ast.MemberExpr) (*ast.CallExpr, bool) {
	var call *ast.CallExpr
	forEachExpr(doc.AST().File(), func(e ast.Expr) {
		if c, ok := e.(*ast.CallExpr); ok {
			if m, ok := c.Callee.(*ast.MemberExpr); ok && m.Syntax() == member.Syntax() {
				call = c
			}
		}
	})
	return call, call != nil
}

// accessorMethods returns the accessor methods of the given kind named name that recv
// binds — the getter a member read (value.name) navigates to, or the setter a write
// (value.name = x) does. Filtering by the access kind keeps a read off the setter and a
// write off the getter; an ordinary method is reached only through a call, and a plain
// field is not a method, so neither is returned here.
func accessorMethods(doc view, recv ir.Type, name string, kind ir.MethodKind) []*ir.Method {
	ms, _, ok := doc.ReceiverMethods(recv)
	if !ok {
		return nil
	}
	var accessors []*ir.Method
	for _, m := range ms {
		if m.Kind == kind && m.Name == name {
			accessors = append(accessors, m)
		}
	}
	return accessors
}

// methodDeclLocations maps methods to the locations of their declaration names,
// every overload in its own file, skipping any method built outside a source
// declaration — a relation builtin assembled from the prelude carries no navigable
// view, so it contributes nothing rather than a phantom location.
func methodDeclLocations(doc view, ms []*ir.Method) []protocol.Location {
	var locs []protocol.Location
	for _, m := range ms {
		if m.Syntax == nil {
			continue
		}
		locs = append(locs, declLocation(doc.viewOfType(m.Owner))(m.Syntax.Syntax())...)
	}
	return locs
}

// memberMethodHover builds the method card for a member access: a single
// signature with its doc below, or — for an overloaded name — every signature
// listed each under its own doc comment, reading like the impl block itself.
func memberMethodHover(ms []*ir.Method, subst map[string]ir.Type, r protocol.Range) *protocol.Hover {
	var b strings.Builder
	if len(ms) == 1 {
		// The common single-signature card: the signature, its doc below.
		b.WriteString("```masterbelt\n")
		b.WriteString(methodSignatureSubst(ms[0], subst))
		b.WriteString("\n```")
		if len(ms[0].Doc) > 0 {
			b.WriteString("\n\n")
			b.WriteString(strings.Join(ms[0].Doc, "\n"))
		}
	} else {
		// An overloaded name lists every signature, each under its own doc
		// comment — the card reads like the impl block itself.
		b.WriteString("```masterbelt\n")
		for i, m := range ms {
			if i > 0 {
				b.WriteString("\n")
			}
			for _, doc := range m.Doc {
				b.WriteString("/// " + doc + "\n")
			}
			b.WriteString(methodSignatureSubst(m, subst))
			b.WriteString("\n")
		}
		b.WriteString("```")
	}
	return &protocol.Hover{
		Contents: protocol.MarkupContent{Kind: protocol.Markdown, Value: b.String()},
		Range:    &r,
	}
}

// memberFieldHover builds the field card for a member access: the field it
// reads, with its type.
func memberFieldHover(f ir.Field, r protocol.Range) *protocol.Hover {
	return &protocol.Hover{
		Contents: protocol.MarkupContent{
			Kind:  protocol.Markdown,
			Value: "```masterbelt\n" + f.Name + ": " + f.Type.String() + "\n```",
		},
		Range: &r,
	}
}

// memberGetterHover builds the card for a getter read (value.name): its
// declaration card, the same shape a method's is — the signature with the
// receiver's generic arguments substituted, its doc below.
func memberGetterHover(g *ir.Method, subst map[string]ir.Type, r protocol.Range) *protocol.Hover {
	var b strings.Builder
	b.WriteString("```masterbelt\n")
	b.WriteString(methodSignatureSubst(g, subst))
	b.WriteString("\n```")
	if len(g.Doc) > 0 {
		b.WriteString("\n\n")
		b.WriteString(strings.Join(g.Doc, "\n"))
	}
	return &protocol.Hover{
		Contents: protocol.MarkupContent{Kind: protocol.Markdown, Value: b.String()},
		Range:    &r,
	}
}

// receiverGetter finds the getter named name on the receiver's type, with the
// receiver's substitution, for a getter-read hover. It reads the kind-tagged
// methods from ReceiverMethods (which returns every kind) and keeps the getter.
func receiverGetter(doc view, recv ir.Type, name string) (*ir.Method, map[string]ir.Type, bool) {
	methods, subst, ok := doc.ReceiverMethods(recv)
	if !ok {
		return nil, nil, false
	}
	for _, m := range methods {
		if m.Kind == ir.MethodGetter && m.Name == name {
			return m, subst, true
		}
	}
	return nil, nil, false
}

// receiverTypeOf resolves the type a member access's receiver has. Where the
// checker type-checked the receiver, its own settled type is the answer
// (doc.exprType): a master in a body reads as its relation, a relation chain
// carries its result type, a shadowing local or type parameter wins — every scope
// rule the checker already applies, read rather than re-derived. The remaining
// cases are the ones the checker leaves untyped: self (the enclosing impl's type),
// a constant or a parameter a bare name denotes, a namespace member's imported
// constant, and a chained field read on an inner receiver the checker did not type.
// Anything else falls back to top-level inference.
func receiverTypeOf(doc view, e ast.Expr, trees map[cst.Green]cst.Tree, offset int, exprTypes map[ast.Expr]ir.Type) ir.Type {
	switch e := e.(type) {
	case *ast.SelfExpr:
		// The checker's settled type for self wins: in a master's static fn self is the
		// master's relation, not the row, so self. offers the relation's query methods
		// rather than the row's surface. Every other body settles self to the row (or
		// the enclosing type), the same the owner fallback gives, so this only changes
		// the relation-self case; an uncovered self falls back to the enclosing owner.
		if t := settledType(exprTypes, e); t != ir.Invalid {
			return t
		}
		if def := enclosingMethodOwner(doc, trees, offset); def != nil {
			return &ir.Named{Def: def}
		}
		return nil
	case *ast.Identifier:
		// The checker's settled type wins, as it does for every other receiver form: a
		// lambda parameter shadows a same-named module constant in value position, and the
		// checker types the body's reference as the parameter, so its settled type offers
		// the binding's members rather than the constant doc.Resolve would return first.
		if t := settledType(exprTypes, e); t != ir.Invalid {
			return t
		}
		if c := doc.Resolve(e); c != nil {
			return c.Type
		}
		if t := paramTypeAt(doc, e.Name, trees, offset); t != nil {
			return t
		}
	case *ast.MemberExpr:
		// The checker's settled type wins: a namespace-qualified master (deck.Cards)
		// reads as its relation even when the namespace also exports a same-named const,
		// which ResolveMember returns first and would mask the relation — the checker
		// resolves the qualified type over the const (it shadows by the namespace name,
		// not the member), so the editor reads its settled type before the const.
		if t := settledType(exprTypes, e); t != ir.Invalid {
			return t
		}
		if c := doc.ResolveMember(e); c != nil {
			return c.Type
		}
		// Otherwise a chained field read on an inner receiver the checker left untyped.
		if inner := receiverTypeOf(doc, e.Receiver, trees, offset, exprTypes); inner != nil {
			if f, ok := memberFieldOf(inner, e.Member.Name); ok {
				return f.Type
			}
		}
		return nil
	}
	// The checker's settled type for the receiver, read before the top-level
	// fallback for every remaining form — a bare master (its relation in a body), a
	// relation chain (its result, including a self-returning or overloaded one), a
	// ternary over relations — each typed by the body walk the const-scope fallback
	// cannot reproduce. A name the checker leaves untyped (a body-local a let shadows,
	// a constant) falls through, honoured by its own scope rule above or the fallback.
	if t := settledType(exprTypes, e); t != ir.Invalid {
		return t
	}
	if t := doc.TypeOfExpr(e); t != ir.Invalid {
		return t
	}
	return nil
}

// settledType reads the type the checker settled for an expression node from the
// per-request map, or ir.Invalid when the checker typed it with no usable type (a
// master in a constant initializer, a name a local shadows, an unresolved form). The
// map is built once per request and threaded through the receiver resolver, so a
// chained receiver does not rebuild it per lookup.
func settledType(exprTypes map[ast.Expr]ir.Type, e ast.Expr) ir.Type {
	if t, ok := exprTypes[e]; ok && t != nil {
		return t
	}
	return ir.Invalid
}

// enclosingMethodOwner returns the type definition whose impl method body spans
// offset — what self denotes there — across every kind that carries methods: a
// type, an enum, or a master's per-row impl. It reads the resolved methods off
// the IR definitions (each method links back to its declaration syntax), so a
// master method, indexed nowhere in file.Types, resolves the same as a type's.
func enclosingMethodOwner(doc view, trees map[cst.Green]cst.Tree, offset int) *ir.TypeDef {
	for _, def := range doc.Module().Types {
		for _, m := range def.Methods {
			if m.Syntax == nil {
				continue
			}
			if t, ok := trees[m.Syntax.Syntax()]; ok && within(t, offset) {
				return def
			}
		}
		// A master's per-row validate checks read the row through self, so an
		// offset inside a validate clause resolves self to the master the same way
		// a row method body does — completion and hover then see the row's fields.
		if def.MasterSyntax != nil {
			for _, c := range def.MasterSyntax.Validations {
				if t, ok := trees[c.Syntax()]; ok && within(t, offset) {
					return def
				}
			}
		}
	}
	return nil
}

// paramTypeAt resolves name as a parameter of what encloses offset: the
// innermost function literal first (its parameters shadow the method's),
// then the method's signature. A self-typed parameter resolves to the
// enclosing impl's type, so its methods bind through it.
func paramTypeAt(doc view, name string, trees map[cst.Green]cst.Tree, offset int) ir.Type {
	if t := funcLitParamTypeAt(doc, name, trees, offset); t != nil {
		return t
	}
	if t := methodParamTypeAt(doc, name, trees, offset); t != nil {
		return t
	}
	return funcParamTypeAt(doc, name, trees, offset)
}

// funcLitParamTypeAt resolves name as a parameter of the innermost function
// literal enclosing offset — its parameters shadow the method's, so the
// enclosing literals are scanned innermost-first.
func funcLitParamTypeAt(doc view, name string, trees map[cst.Green]cst.Tree, offset int) ir.Type {
	litTypes := doc.FuncLitTypes()
	var enclosing []*ast.FuncLit
	forEachFuncLit(doc, func(lit *ast.FuncLit) {
		if t, ok := trees[lit.Syntax()]; ok && within(t, offset) {
			enclosing = append(enclosing, lit)
		}
	})
	for i := len(enclosing) - 1; i >= 0; i-- {
		lit := enclosing[i]
		ft := litTypes[lit]
		if ft == nil {
			continue
		}
		for j, p := range lit.Params {
			if p.Name == name && j < len(ft.Params) && ft.Params[j] != ir.Invalid {
				return ft.Params[j]
			}
		}
	}
	return nil
}

// methodParamTypeAt resolves name as a parameter of the method whose body
// spans offset. A self-typed parameter resolves to the enclosing impl's type,
// so its methods bind through it.
func methodParamTypeAt(doc view, name string, trees map[cst.Green]cst.Tree, offset int) ir.Type {
	for _, def := range doc.Module().Types {
		for _, irm := range def.Methods {
			if irm.Syntax == nil {
				continue
			}
			mt, ok := trees[irm.Syntax.Syntax()]
			if !ok || !within(mt, offset) {
				continue
			}
			for _, p := range irm.Params {
				if p.Name != name || p.Type == nil || p.Type == ir.Invalid {
					continue
				}
				if _, isSelf := p.Type.(*ir.SelfType); isSelf {
					return &ir.Named{Def: def}
				}
				return p.Type
			}
		}
	}
	return nil
}

// funcParamTypeAt resolves name as a parameter of the top-level function body
// spanning offset. A generic function's parameter is a bare TypeVar in the
// resolved signature, so the type-parameter bounds are rebound onto it
// (BindTypeParamBounds) — a `c: T` where T: foldable then surfaces the bound
// interface's methods on c.
func funcParamTypeAt(doc view, name string, trees map[cst.Green]cst.Tree, offset int) ir.Type {
	for _, f := range doc.Module().Funcs {
		if f.Syntax == nil {
			continue
		}
		ft, ok := trees[f.Syntax.Syntax()]
		if !ok || !within(ft, offset) {
			continue
		}
		bindBounds := infer.BindTypeParamBounds(f.TypeParams)
		for _, p := range f.Params {
			if p.Name != name || p.Type == nil || p.Type == ir.Invalid {
				continue
			}
			return types.Substitute(p.Type, bindBounds)
		}
	}
	return nil
}

// fieldOf returns the record field a type carries under name — directly, or
// through a named type's record body. Like recordOf, it does not look through a
// master: the record-literal paths keep a master opaque. The value member-access
// read paths use memberFieldOf, which projects the master row.
func fieldOf(t ir.Type, name string) (ir.Field, bool) {
	switch t := t.(type) {
	case *ir.Record:
		for _, f := range t.Fields {
			if f.Name == name {
				return f, true
			}
		}
	case *ir.Named:
		if t.Def != nil {
			return fieldOf(t.Def.Body, name)
		}
	}
	return ir.Field{}, false
}

// memberFieldOf returns the record field a value member access reads under name,
// through the same types.RecordOf the checker's member access uses — so it
// follows a named alias, instantiates a generic application, and projects a
// master's row, including a master reached through an alias. A master row field
// is readable through a value (s.field) though the master is not constructible,
// so this read path projects the row while fieldOf keeps the literal paths opaque.
func memberFieldOf(t ir.Type, name string) (ir.Field, bool) {
	if rec := types.RecordOf(t); rec != nil {
		return fieldOf(rec, name)
	}
	return ir.Field{}, false
}

// methodDeclHover describes the method declared at offset — the cursor on its
// name in an impl block — as its resolved signature and doc.
func methodDeclHover(doc view, offset int, trees map[cst.Green]cst.Tree) *protocol.Hover {
	leaf, parent, ok := leafAt(doc.AST().Concrete().Tree(), offset)
	if !ok {
		return nil
	}
	if k, isTok := leaf.TokenKind(); !isTok || k != token.Ident {
		return nil
	}
	if pk, isNode := parent.Kind(); !isNode || pk != cst.MethodDecl {
		return nil
	}

	for _, def := range doc.Module().Types {
		for _, irm := range def.Methods {
			if irm.Syntax == nil {
				continue
			}
			mt, found := trees[irm.Syntax.Syntax()]
			if !found || !within(mt, offset) {
				continue
			}
			var b strings.Builder
			b.WriteString("```masterbelt\n")
			b.WriteString(methodSignature(irm))
			b.WriteString("\n```")
			if len(irm.Doc) > 0 {
				b.WriteString("\n\n")
				b.WriteString(strings.Join(irm.Doc, "\n"))
			}
			r := toRange(doc.Buffer(), leaf.Offset(), leaf.End())
			return &protocol.Hover{
				Contents: protocol.MarkupContent{Kind: protocol.Markdown, Value: b.String()},
				Range:    &r,
			}
		}
	}
	return nil
}

// enumMemberHover describes an enum member access at offset (Rarity.Common):
// the qualified member name and its base value, rendered as
// `Rarity.Common = 1` with the member's doc comments beneath. It returns nil
// when offset is not on an enum member access (the other hover paths handle
// methods, fields, and values).
func enumMemberHover(doc view, offset int) *protocol.Hover {
	member, ok := memberAccessAt(doc, offset)
	if !ok {
		return nil
	}
	recv, ok := member.Receiver.(*ast.Identifier)
	if !ok || doc.Resolve(recv) != nil {
		return nil // a value shadowing the type name is a value access
	}
	def := lookupEnumType(doc, recv.Name)
	if def == nil {
		return nil
	}
	idx := -1
	for i, m := range def.Enum.Members {
		if m.Name == member.Member.Name {
			idx = i
			break
		}
	}
	if idx < 0 {
		return nil
	}
	m := def.Enum.Members[idx]
	var b strings.Builder
	b.WriteString("```masterbelt\n")
	b.WriteString(def.Name)
	b.WriteString(".")
	b.WriteString(m.Name)
	if m.Value != nil {
		b.WriteString(" = ")
		b.WriteString(m.Value.String())
	}
	b.WriteString("\n```")

	node, _ := memberNodeAt(doc, offset)
	rng := cst.Root(node)
	r := toRange(doc.Buffer(), rng.Offset(), rng.End())
	return &protocol.Hover{
		Contents: protocol.MarkupContent{Kind: protocol.Markdown, Value: b.String()},
		Range:    &r,
	}
}

// memberIsCallee reports whether a member access is the callee of a call in the
// file — X.m used as X.m(...). It distinguishes a member read (a value) from a
// member call (a method, static fn, or relation method), so a value-position hover
// (an associated constant) does not claim a called member the call-aware hover owns.
func memberIsCallee(doc view, member *ast.MemberExpr) bool {
	found := false
	forEachExpr(doc.AST().File(), func(e ast.Expr) {
		if call, ok := e.(*ast.CallExpr); ok && call.Callee == member {
			found = true
		}
	})
	return found
}

// assocConstHover describes an associated-constant access at offset (int8.Max,
// Level.Max): the qualified name with its type, its folded value, and the
// constant's doc comments beneath — rendered as `int8.Max: int = 127`. It
// returns nil when offset is not on an associated-constant access (the enum and
// value hover paths claim their own forms first).
func assocConstHover(doc view, offset int) *protocol.Hover {
	member, ok := memberAccessAt(doc, offset)
	if !ok {
		return nil
	}
	// A called member (Cards.sum(...)) is a method, static fn, or relation method,
	// not a value read: the checker resolves a master's relation method over a
	// same-named associated constant when the name is called, so the const hover must
	// not claim it — the call-aware member hover does. A bare read (Cards.sum) is the
	// constant and stays here.
	if memberIsCallee(doc, member) {
		return nil
	}
	recv, ok := member.Receiver.(*ast.Identifier)
	if !ok || doc.Resolve(recv) != nil {
		return nil // a value shadowing the type name is a value access
	}
	def := lookupTypeName(doc, recv.Name)
	if def == nil {
		return nil
	}
	var c *ir.AssocConst
	for _, ac := range def.Consts {
		if ac.Name == member.Member.Name {
			c = ac
			break
		}
	}
	if c == nil {
		return nil
	}
	var b strings.Builder
	b.WriteString("```masterbelt\n")
	b.WriteString(def.Name)
	b.WriteString(".")
	b.WriteString(c.Name)
	if c.Type != nil && c.Type != ir.Invalid {
		b.WriteString(": ")
		b.WriteString(c.Type.String())
	}
	if c.Value != nil {
		b.WriteString(" = ")
		b.WriteString(c.Value.String())
	}
	b.WriteString("\n```")
	if len(c.Doc) > 0 {
		b.WriteString("\n\n")
		b.WriteString(strings.Join(c.Doc, "\n"))
	}

	node, _ := memberNodeAt(doc, offset)
	rng := cst.Root(node)
	r := toRange(doc.Buffer(), rng.Offset(), rng.End())
	return &protocol.Hover{
		Contents: protocol.MarkupContent{Kind: protocol.Markdown, Value: b.String()},
		Range:    &r,
	}
}

// staticCallHover describes a static fn call's callee at offset (Celsius.freezing):
// each static fn overload of that name on the type, its signature and doc — the
// Type.name(...) twin of the method hover card. It returns nil when offset is not
// on a static-fn member access (the enum, associated-constant, and value hover
// paths claim their forms first).
func staticCallHover(doc view, offset int) *protocol.Hover {
	member, ok := memberAccessAt(doc, offset)
	if !ok {
		return nil
	}
	recv, ok := member.Receiver.(*ast.Identifier)
	if !ok || doc.Resolve(recv) != nil {
		return nil // a value shadowing the type name is a value access
	}
	def := lookupTypeName(doc, recv.Name)
	if def == nil {
		return nil
	}
	var statics []*ir.Method
	for _, m := range def.Methods {
		if m.Kind == ir.MethodStatic && m.Name == member.Member.Name {
			statics = append(statics, m)
		}
	}
	if len(statics) == 0 {
		return nil
	}
	var b strings.Builder
	b.WriteString("```masterbelt\n")
	for i, m := range statics {
		if i > 0 {
			b.WriteString("\n")
		}
		for _, d := range m.Doc {
			b.WriteString("/// " + d + "\n")
		}
		b.WriteString(methodSignatureSubst(m, nil))
		b.WriteString("\n")
	}
	b.WriteString("```")

	node, _ := memberNodeAt(doc, offset)
	rng := cst.Root(node)
	r := toRange(doc.Buffer(), rng.Offset(), rng.End())
	return &protocol.Hover{
		Contents: protocol.MarkupContent{Kind: protocol.Markdown, Value: b.String()},
		Range:    &r,
	}
}
