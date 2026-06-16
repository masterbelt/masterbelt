// Package semantic resolves names and infers types for a masterbelt program,
// producing the resolved IR (package source/ir).
//
// Operators have already been desugared to method calls by the AST layer, so
// 1 + 2 arrives as 1.add(2). Typing and evaluation are therefore uniform: every
// expression is a literal, a value reference, or a method call, and a call's
// type comes from the method's signature (package types) while its value comes
// from the method's native implementation (the builtin registry's intrinsics).
//
// The semantic facts a program needs — the symbol table, each constant's type,
// and each constant's evaluated value — are expressed as a small set of pure
// queries (the queries interface). assemble turns those queries plus the AST
// into the IR and diagnostics. Two query implementations share that one
// assembler: a direct one (this file), used by Analyze for a full recompute and
// as the oracle, and an incremental, memoizing one backed by the query database
// (engine.go), used by Program. Because both feed the same assembler, the
// incremental result is identical to the full one.
//
// The package is split by concern: this file holds the analysis entry points
// and the use-graph and cycle helpers; queries.go holds the query interface and
// the direct implementation; assemble.go builds the IR; eval.go folds constants;
// lower.go binds names for the AST-to-IR walk (package lower); resolve.go
// resolves type and function declarations; check.go runs the expression and
// method-body diagnostics; positions.go anchors diagnostics to source; engine.go
// and program.go are the incremental façade.
package semantic

import (
	"github.com/masterbelt/masterbelt/pkg/belt/builtin"
	"github.com/masterbelt/masterbelt/pkg/belt/parser/abstract"
	"github.com/masterbelt/masterbelt/pkg/belt/types"
	"github.com/masterbelt/masterbelt/pkg/diagnostic"
	"github.com/masterbelt/masterbelt/pkg/source/ast"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// Analyze resolves and types one standalone file, returning the IR module and
// the semantic diagnostics. It recomputes everything from scratch; it is the
// single-file reference analysis and the oracle the incremental Program is
// checked against.
func Analyze(doc *abstract.Document) (*ir.Module, []diagnostic.Diagnostic) {
	file := doc.File()
	files := map[FileID]*ast.File{soleFileID: file}
	q := newDirectQueries(files, nil, universe())
	return assemble(soleFileID, file, positionsOf(doc.Concrete().Tree()), q, constShells(files), q.fnShells)
}

// AnalyzeProgram resolves and types a whole program from scratch: every file
// reachable from the entry, with the use targets the project layer resolved
// for each. It is the reference analysis and the oracle the incremental
// Program is checked against.
func AnalyzeProgram(docs map[FileID]*abstract.Document, uses map[FileID]map[*ast.UseDecl]FileID) (map[FileID]*ir.Module, map[FileID][]diagnostic.Diagnostic) {
	files := make(map[FileID]*ast.File, len(docs))
	for id, doc := range docs {
		files[id] = doc.File()
	}
	q := newDirectQueries(files, uses, universe())
	shells := constShells(files)

	modules := make(map[FileID]*ir.Module, len(docs))
	diags := make(map[FileID][]diagnostic.Diagnostic, len(docs))
	for id, doc := range docs {
		modules[id], diags[id] = assemble(id, doc.File(), positionsOf(doc.Concrete().Tree()), q, shells, q.fnShells)
	}
	return modules, diags
}

// constShells creates the identity ir.Const for every declaration across the
// program — references, including cross-file ones, bind to the same objects
// the owning module publishes, which is what makes the IR one pointer graph.
func constShells(files map[FileID]*ast.File) map[*ast.ConstDecl]*ir.Const {
	shells := map[*ast.ConstDecl]*ir.Const{}
	for id, f := range files {
		if f == nil {
			continue
		}
		module := moduleSegment(id)
		for _, decl := range f.Decls {
			shells[decl] = &ir.Const{Name: decl.Name, Anchor: declAnchor(module, decl.Name), Public: decl.Public, Doc: decl.Doc, Syntax: decl}
		}
	}
	return shells
}

// funcShells creates the identity ir.Function for every function declaration
// across the program, exactly as constShells does for constants: FuncCall
// values bind to the same objects the owning module publishes.
func funcShells(files map[FileID]*ast.File) map[*ast.FuncDecl]*ir.Function {
	shells := map[*ast.FuncDecl]*ir.Function{}
	for id, f := range files {
		if f == nil {
			continue
		}
		module := moduleSegment(id)
		for _, fd := range f.Funcs {
			shells[fd] = &ir.Function{Name: fd.Name, Anchor: declAnchor(module, fd.Name), Public: fd.Public, Doc: fd.Doc, Syntax: fd}
		}
	}
	return shells
}

// cyclicDecls returns the declarations caught in a type-inference cycle. A
// declaration's type depends on the types of the value references in its
// initializer, unless an annotation fixes it; the result is a general directed
// graph (an expression may reference several names), so its cycles are found
// with a coloured depth-first search.
func cyclicDecls(fileID FileID, file *ast.File, q queries) map[*ast.ConstDecl]bool {
	// The walk stays within the file: an identifier of another file's decl
	// would resolve in the wrong scope here, and a cross-file inference cycle
	// necessarily rides a module cycle, which checkUses reports (the engine's
	// runtime cycle guard keeps such types finite, as Invalid).
	own := make(map[*ast.ConstDecl]bool, len(file.Decls))
	for _, decl := range file.Decls {
		own[decl] = true
	}
	deps := func(decl *ast.ConstDecl) []*ast.ConstDecl {
		if decl.Type != nil || decl.Value == nil {
			return nil // an annotation breaks the inheritance chain
		}
		var out []*ast.ConstDecl
		ast.WalkValueIdents(decl.Value, func(id *ast.Identifier) {
			if t := q.resolve(fileID, id); t != nil && own[t] {
				out = append(out, t)
			}
		})
		return out
	}

	const (
		white = iota
		gray
		black
	)
	color := map[*ast.ConstDecl]int{}
	cyclic := map[*ast.ConstDecl]bool{}
	var stack []*ast.ConstDecl

	var dfs func(decl *ast.ConstDecl)
	dfs = func(decl *ast.ConstDecl) {
		color[decl] = gray
		stack = append(stack, decl)
		for _, dep := range deps(decl) {
			switch color[dep] {
			case white:
				dfs(dep)
			case gray:
				// Back edge: everything from dep to the top of the stack is on
				// the cycle.
				for i := len(stack) - 1; i >= 0; i-- {
					cyclic[stack[i]] = true
					if stack[i] == dep {
						break
					}
				}
			}
		}
		stack = stack[:len(stack)-1]
		color[decl] = black
	}

	for _, decl := range file.Decls {
		if color[decl] == white {
			dfs(decl)
		}
	}
	return cyclic
}

// checkUses reports the problems of a file's use declarations: a path that
// resolved to no file (use_not_found), a selectively imported name the target
// does not export (not_exported), and an import that can reach back to this
// file (cyclic_module — reported, Go style, on each edge that closes a cycle).
func checkUses(fileID FileID, file *ast.File, q queries, at func(ast.Node) span, diags *diagnostic.List) {
	uses := q.usesOf(fileID)
	for _, u := range file.Uses {
		if u.Path == "" {
			continue // already a parse diagnostic
		}
		s := at(u)
		target, ok := uses[u]
		if !ok {
			diags.Add(newUseNotFoundDiagnostic(s.offset, s.width, u.Path))
			continue
		}
		for _, name := range u.Names {
			exp := q.exportsOf(target)
			if _, isConst := exp.consts[name]; isConst {
				continue
			}
			if _, isType := exp.types[name]; isType {
				continue
			}
			if _, isFunc := exp.funcs[name]; isFunc {
				continue
			}
			diags.Add(newNotExportedDiagnostic(s.offset, s.width, name, u.Path))
		}
		if q.reachableFrom(target)[fileID] {
			diags.Add(newCyclicModuleDiagnostic(s.offset, s.width, u.Path))
		}
	}
}

// computeReachable walks the use graph from from, collecting every file it
// reaches (from itself included, so a self-import closes a cycle trivially).
// It is the shared rule behind the reachableFrom query: the walk reads usesOf
// through q, so the memoizing engine records every visited file's input as a
// dependency and the set recomputes only when a file it covers changes.
func computeReachable(q queries, from FileID) map[FileID]bool {
	reached := map[FileID]bool{from: true}
	queue := []FileID{from}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		for _, next := range q.usesOf(id) {
			if !reached[next] {
				reached[next] = true
				queue = append(queue, next)
			}
		}
	}
	return reached
}

// walkRefsEnum visits the value references of an expression: every
// value-position identifier through onIdent, except that a namespace member
// access (geo.Origin) is one unit visited through onMember — its receiver names
// a namespace, not a value, and is skipped — a type member access
// (Rarity.Common, int8.Max) is one unit visited through onTypeMember — its
// receiver names a type, not a value — and a call's callee that names a type (a
// conversion) or a top-level function is skipped too: it refers to the type or
// function, not to a value declaration. The walk is pre-order, so a call marks
// its callee before the callee itself is visited. A nil onTypeMember disables
// the type-member reading (the receiver would then be visited as an ordinary
// name). The decisions layer on the shared ast.WalkExprs traversal, so the
// skeleton lives in one place.
func walkRefsEnum(fileID FileID, e ast.Expr, q queries, onIdent func(*ast.Identifier), onMember func(*ast.MemberExpr), onTypeMember func(*ast.MemberExpr)) {
	funcCallee := map[*ast.Identifier]bool{}
	funcMemberCallee := map[*ast.MemberExpr]bool{}
	staticCallee := map[*ast.MemberExpr]bool{}
	ast.WalkExprs(e, func(e ast.Expr) bool {
		switch e := e.(type) {
		case *ast.CallExpr:
			classifyRefCallee(fileID, e, q, funcCallee, funcMemberCallee, staticCallee)
		case *ast.Identifier:
			if !funcCallee[e] {
				onIdent(e)
			}
		case *ast.MemberExpr:
			return walkRefsMember(fileID, e, q, onMember, onTypeMember, funcMemberCallee, staticCallee)
		}
		return true
	})
}

// classifyRefCallee marks a call's callee that names a value-less target — a
// conversion (the callee names a type) or a top-level function by name, a
// namespace function, or a static fn — so the reference walk skips it: it refers
// to the type or function, not to a value declaration.
func classifyRefCallee(fileID FileID, e *ast.CallExpr, q queries, funcCallee map[*ast.Identifier]bool, funcMemberCallee, staticCallee map[*ast.MemberExpr]bool) {
	switch callee := e.Callee.(type) {
	case *ast.Identifier:
		if _, isType := q.universe(fileID)[callee.Name]; isType {
			funcCallee[callee] = true // a conversion's callee names a type
		} else if len(q.resolveFunc(fileID, callee)) > 0 {
			funcCallee[callee] = true
		}
	case *ast.MemberExpr:
		if len(q.resolveFuncMember(fileID, callee)) > 0 {
			funcMemberCallee[callee] = true
		} else if recv, ok := callee.Receiver.(*ast.Identifier); ok && isTypeName(fileID, recv, q) {
			// A call whose callee is a member access on a type name is a static
			// fn call (Celsius.freezing()): the member is not an enum member or
			// associated constant, so it must be exempt from the type-member
			// reference check below — whether the static fn exists is the type
			// checker's unknown_static finding. A metatype method call (Level ==
			// long, desugared to Level.eql(long)) is exempt the same way: it calls
			// the reified type value's equality, not a member of the type itself.
			switch {
			case types.ResolveMember(q.universe(fileID)[recv.Name], callee.Member.Name).Kind == types.MemberStatic:
				staticCallee[callee] = true
			case types.IsMetatypeMethod(q.universe(fileID)[builtin.NameType], callee.Member.Name):
				staticCallee[callee] = true
			case isMasterDef(q.universe(fileID)[recv.Name]):
				// A master in value position is its relation, so a call of a name that
				// is not one of its static fns is a relation method (Cards.where(...),
				// Cards.count()): it calls a method of the relation value, not a member
				// of the master type, so it is exempt from the type-member check the
				// same way a static or metatype call is.
				staticCallee[callee] = true
			}
		} else if isQualifiedTypeReceiver(fileID, callee.Receiver, q) &&
			types.IsMetatypeMethod(q.universe(fileID)[builtin.NameType], callee.Member.Name) {
			// A metatype method call on a bare qualified type value (geo.Item ==
			// geo.Item, desugared to geo.Item.eql(geo.Item)) calls the reified type
			// value's equality, not a member of the qualified type itself, so it is
			// exempt from the type-member reference check exactly as the local
			// Level.eql(long) form above is.
			staticCallee[callee] = true
		}
	}
}

// walkRefsMember classifies a member access for the reference walk: a receiver
// naming a type is a type-member reference (an enum member or associated
// constant) consumed as one unit, a receiver naming a namespace is a namespace
// access consumed as one unit, and any other receiver descends as an ordinary
// expression. It returns whether the walk should descend into the receiver.
func walkRefsMember(fileID FileID, e *ast.MemberExpr, q queries, onMember, onTypeMember func(*ast.MemberExpr), funcMemberCallee, staticCallee map[*ast.MemberExpr]bool) bool {
	recv, ok := e.Receiver.(*ast.Identifier)
	if !ok {
		// A receiver that is itself a namespace-qualified type name (geo.Item) makes
		// this a qualified type-member reference (geo.Item.id), validated as one unit
		// exactly as a bare-name type member is.
		if onTypeMember != nil && isQualifiedTypeReceiver(fileID, e.Receiver, q) {
			if !staticCallee[e] {
				onTypeMember(e)
			}
			return false
		}
		return true
	}
	if onTypeMember != nil && isTypeName(fileID, recv, q) {
		if !staticCallee[e] {
			onTypeMember(e)
		}
		return false
	}
	if isNamespace(fileID, recv, q) {
		if !funcMemberCallee[e] {
			onMember(e)
		}
		return false
	}
	return true
}

// isQualifiedTypeReceiver reports whether recv is a namespace-qualified type name
// (geo.Item) — a member access whose receiver is a namespace import and whose
// member is one of its exported types — so a further member off it (geo.Item.id)
// is a qualified type-member reference rather than an ordinary expression.
func isQualifiedTypeReceiver(fileID FileID, recv ast.Expr, q queries) bool {
	m, ok := recv.(*ast.MemberExpr)
	if !ok {
		return false
	}
	ns, ok := m.Receiver.(*ast.Identifier)
	if !ok {
		return false
	}
	// A value of the namespace's name shadows the import (geo a local or const), so
	// geo.Item is then a value field read, not a qualified type.
	if q.resolve(fileID, ns) != nil {
		return false
	}
	return qualifiedFrom(q, q.importsOf(fileID))(ns.Name, m.Member.Name) != nil
}

// isTypeName reports whether an identifier names a type in its file — and no
// value, since a local or imported value shadows a type name in value position.
// It covers both an enum (its members) and any other type (its associated
// constants).
func isTypeName(fileID FileID, id *ast.Identifier, q queries) bool {
	if q.resolve(fileID, id) != nil {
		return false
	}
	_, ok := q.universe(fileID)[id.Name]
	return ok
}

// isMasterDef reports whether a definition is a master — the kind whose value-
// position form is its relation, so a non-static member call on it is a relation
// method rather than a member of the type.
func isMasterDef(def *ir.TypeDef) bool {
	return def != nil && def.Master != nil
}

// isNamespace reports whether an identifier names a namespace import in its
// file — and no value, since locals and imported values shadow namespaces.
func isNamespace(fileID FileID, id *ast.Identifier, q queries) bool {
	if q.resolve(fileID, id) != nil {
		return false
	}
	_, ok := q.importsOf(fileID).namespaces[id.Name]
	return ok
}
