// This file is the test-local curated rendering of the AST: the compact,
// human-edited dump the lowering tests assert against (the s-expr inline
// expressions, the source-like type forms). It is deliberately NOT the text
// representation contract — that is ast's generated format v2
// (ast.File.MarshalText), which carries every field; this DSL exists so a
// test can pin one aspect of a lowering in one readable line. It moved here
// from package ast when the exact format replaced it as the snapshot oracle.

package abstract

import (
	"fmt"
	"strings"
	"testing"

	"github.com/masterbelt/masterbelt/pkg/source/ast"
)

// Dump renders a ast.File as a stable, diffable text tree. It reads only the
// resolved fields (no positions, no buffer), so two Files dump to the same text
// exactly when they are structurally equal — which makes Dump both the snapshot
// format and the oracle an incremental lowering is checked against.
func dumpAST(f *ast.File) string {
	var b strings.Builder
	b.WriteString("File\n")
	for _, d := range f.Uses {
		dumpUseDecl(&b, d)
	}
	for _, d := range f.Decls {
		dumpConstDecl(&b, d)
	}
	for _, d := range f.Types {
		dumpTypeDecl(&b, d)
	}
	for _, d := range f.Enums {
		dumpEnumDecl(&b, d)
	}
	for _, d := range f.Interfaces {
		dumpInterfaceDecl(&b, d)
	}
	for _, d := range f.Funcs {
		dumpFuncDecl(&b, d)
	}
	for _, d := range f.Asserts {
		dumpAssertDecl(&b, d)
	}
	return b.String()
}

func dumpUseDecl(b *strings.Builder, d *ast.UseDecl) {
	b.WriteString("  UseDecl\n")
	if d.Public {
		b.WriteString("    pub\n")
	}
	switch {
	case d.Star:
		b.WriteString("    target *\n")
	case d.Namespace != "":
		fmt.Fprintf(b, "    namespace %q\n", d.Namespace)
	default:
		for _, n := range d.Names {
			fmt.Fprintf(b, "    name %q\n", n)
		}
	}
	fmt.Fprintf(b, "    from %q\n", d.Path)
}

func dumpConstDecl(b *strings.Builder, d *ast.ConstDecl) {
	b.WriteString("  ConstDecl\n")
	for _, doc := range d.Doc {
		fmt.Fprintf(b, "    doc %q\n", doc)
	}
	if d.Public {
		b.WriteString("    pub\n")
	}
	fmt.Fprintf(b, "    name %q\n", d.Name)
	if d.Type != nil {
		fmt.Fprintf(b, "    type %s\n", dumpType(d.Type))
	}
	if d.Value != nil {
		fmt.Fprintf(b, "    value %s\n", dumpExpr(d.Value))
	}
}

func dumpFuncDecl(b *strings.Builder, d *ast.FuncDecl) {
	b.WriteString("  FuncDecl\n")
	for _, doc := range d.Doc {
		fmt.Fprintf(b, "    doc %q\n", doc)
	}
	if d.Public {
		b.WriteString("    pub\n")
	}
	if d.Extern {
		b.WriteString("    extern\n")
	}
	if len(d.Effects) > 0 {
		fmt.Fprintf(b, "    effects %s\n", strings.Join(d.Effects, " "))
	}
	fmt.Fprintf(b, "    name %q\n", d.Name)
	for _, p := range d.TypeParams {
		if p.Constraint != nil {
			fmt.Fprintf(b, "    typeparam %s: %s\n", p.Name, dumpType(p.Constraint))
		} else {
			fmt.Fprintf(b, "    typeparam %s\n", p.Name)
		}
	}
	for _, p := range d.Params {
		fmt.Fprintf(b, "    param %s: %s\n", p.Name, dumpType(p.Type))
	}
	if d.Result != nil {
		fmt.Fprintf(b, "    result %s\n", dumpType(d.Result))
	}
	for _, s := range d.Body {
		dumpStmt(b, s)
	}
}

func dumpAssertDecl(b *strings.Builder, d *ast.AssertDecl) {
	b.WriteString("  AssertDecl\n")
	for _, doc := range d.Doc {
		fmt.Fprintf(b, "    doc %q\n", doc)
	}
	fmt.Fprintf(b, "    cond %s\n", dumpExpr(d.Cond))
}

func dumpExpr(e ast.Expr) string {
	switch x := e.(type) {
	case nil:
		return "<missing>"
	case *ast.IntLit:
		return fmt.Sprintf("IntLit %q", x.Text)
	case *ast.StringLit:
		return fmt.Sprintf("StringLit %q", x.Value)
	case *ast.DatetimeLit:
		return fmt.Sprintf("DatetimeLit %q", x.Text)
	case *ast.DurationLit:
		return fmt.Sprintf("DurationLit %q", x.Text)
	case *ast.BoolLit:
		return fmt.Sprintf("BoolLit %v", x.Value)
	case *ast.NullLit:
		return "NullLit"
	case *ast.CollectionLit:
		return dumpCollection(x)
	case *ast.RecordLit:
		return dumpRecord(x)
	case *ast.SelfExpr:
		return "self"
	case *ast.Identifier:
		return fmt.Sprintf("Identifier %q", x.Name)
	case *ast.MemberExpr:
		return fmt.Sprintf("(. %s %s)", dumpExpr(x.Receiver), x.Member.Name)
	case *ast.CallExpr:
		return dumpCall(x)
	case *ast.AwaitExpr:
		return "(await " + dumpExpr(x.Value) + ")"
	case *ast.TernaryExpr:
		return "(? " + dumpExpr(x.Cond) + " " + dumpExpr(x.Then) + " " + dumpExpr(x.Else) + ")"
	case *ast.RangeExpr:
		op := ".."
		if x.HalfOpen {
			op = "..."
		}
		return "(" + op + " " + dumpExpr(x.Lower) + " " + dumpExpr(x.Upper) + ")"
	case *ast.FuncLit:
		return dumpFuncLit(x)
	default:
		return "Expr(?)"
	}
}

// dumpCollection renders a collection literal: a label (map/list/collection)
// followed by each entry, "key: value" for a map entry and a bare value
// otherwise, wrapped in parentheses.
func dumpCollection(x *ast.CollectionLit) string {
	label := "collection"
	if x.IsMap() {
		label = "map"
	} else if len(x.Entries) > 0 {
		label = "list"
	}
	parts := []string{label}
	for _, e := range x.Entries {
		if e.Key != nil {
			parts = append(parts, dumpExpr(e.Key)+": "+dumpExpr(e.Value))
		} else {
			parts = append(parts, dumpExpr(e.Value))
		}
	}
	return "(" + strings.Join(parts, " ") + ")"
}

// dumpRecord renders a record literal: the "record" label (with the type name
// when one is written) followed by each "name: value" field, parenthesized.
func dumpRecord(x *ast.RecordLit) string {
	label := "record"
	if x.TypeName != "" {
		label = "record " + x.TypeName
	}
	parts := make([]string, 0, 1+len(x.Fields))
	parts = append(parts, label)
	for _, f := range x.Fields {
		parts = append(parts, f.Name+": "+dumpExpr(f.Value))
	}
	return "(" + strings.Join(parts, " ") + ")"
}

// dumpCall renders an explicit call: "call", the callee, then each argument,
// parenthesized.
func dumpCall(x *ast.CallExpr) string {
	parts := make([]string, 0, 2+len(x.Arguments))
	parts = append(parts, "call", dumpExpr(x.Callee))
	for _, a := range x.Arguments {
		parts = append(parts, dumpExpr(a))
	}
	return "(" + strings.Join(parts, " ") + ")"
}

// dumpFuncLit renders a function literal: the "fn(params): result" signature
// followed by its body statements in inline form, parenthesized.
func dumpFuncLit(x *ast.FuncLit) string {
	params := make([]string, len(x.Params))
	for i, p := range x.Params {
		params[i] = p.Name + ": " + dumpType(p.Type)
	}
	parts := make([]string, 0, 1+len(x.Body))
	parts = append(parts, "fn("+strings.Join(params, ", ")+"): "+dumpType(x.Result))
	for _, s := range x.Body {
		parts = append(parts, dumpStmtInline(s))
	}
	return "(" + strings.Join(parts, " ") + ")"
}

// dumpStmtInline renders a statement compactly on one line, for a function
// literal dumped as part of an enclosing expression. (dumpStmt is the
// multi-line, indented form used for a method body.) Every statement kind has
// an inline form so a control-flow statement carried by a lambda body — an if,
// a switch — is visible in the snapshot oracle rather than silently dropped; a
// kind added later panics here, forcing the form to be defined.
func dumpStmtInline(s ast.Stmt) string {
	switch s := s.(type) {
	case *ast.ReturnStmt:
		return "(return " + dumpExpr(s.Value) + ")"
	case *ast.ExprStmt:
		return "(expr " + dumpExpr(s.X) + ")"
	case *ast.LetStmt:
		if s.Type != nil {
			return "(let " + s.Name + ": " + dumpType(s.Type) + " = " + dumpExpr(s.Value) + ")"
		}
		return "(let " + s.Name + " = " + dumpExpr(s.Value) + ")"
	case *ast.AssignStmt:
		return "(assign " + dumpExpr(s.Target) + " = " + dumpExpr(s.Value) + ")"
	case *ast.SwitchStmt:
		return dumpSwitchInline(s)
	case *ast.MatchStmt:
		return dumpMatchInline(s)
	case *ast.IfStmt:
		return dumpIfInline(s)
	case *ast.ForStmt:
		return dumpForInline(s)
	case *ast.AssertStmt:
		return "(assert " + dumpExpr(s.Cond) + ")"
	default:
		panic(ast.UnhandledStmt(s))
	}
}

// dumpInlineBody renders a statement body as a space-joined run of inline
// statements, e.g. "(return ...) (let ...)".
func dumpInlineBody(body []ast.Stmt) string {
	parts := make([]string, len(body))
	for i, s := range body {
		parts[i] = dumpStmtInline(s)
	}
	return strings.Join(parts, " ")
}

// dumpSwitchInline renders a switch compactly: its scrutinee, each arm's value
// patterns and body, the wildcard else body, and any unreachable arms.
func dumpSwitchInline(s *ast.SwitchStmt) string {
	var b strings.Builder
	b.WriteString("(switch " + dumpExpr(s.Scrutinee))
	for _, arm := range s.Arms {
		b.WriteString(" (arm " + dumpArmValues(arm) + " " + dumpInlineBody(arm.Body) + ")")
	}
	if s.Else != nil {
		b.WriteString(" (else " + dumpInlineBody(s.Else) + ")")
	}
	for _, arm := range s.AfterElse {
		b.WriteString(" (unreachable-arm " + dumpArmValues(arm) + " " + dumpInlineBody(arm.Body) + ")")
	}
	b.WriteString(")")
	return b.String()
}

// dumpMatchInline renders a match compactly: its scrutinee, each arm's type
// pattern (with its binding) and body, the wildcard else body, and any
// unreachable arms.
func dumpMatchInline(s *ast.MatchStmt) string {
	var b strings.Builder
	b.WriteString("(match " + dumpExpr(s.Scrutinee))
	for _, arm := range s.Arms {
		b.WriteString(" (arm " + dumpMatchPattern(arm) + " " + dumpInlineBody(arm.Body) + ")")
	}
	if s.Else != nil {
		b.WriteString(" (else " + dumpInlineBody(s.Else) + ")")
	}
	for _, arm := range s.AfterElse {
		b.WriteString(" (unreachable-arm " + dumpMatchPattern(arm) + " " + dumpInlineBody(arm.Body) + ")")
	}
	b.WriteString(")")
	return b.String()
}

// dumpMatchPattern renders a match arm's type pattern: its member type and, when
// present, its binding name (Coin c, null, int v).
func dumpMatchPattern(arm *ast.MatchArm) string {
	pat := dumpType(arm.Type)
	if arm.Bind != "" {
		pat += " " + arm.Bind
	}
	return pat
}

// dumpArmValues renders a switch arm's value patterns, comma-joined.
func dumpArmValues(arm *ast.SwitchArm) string {
	values := make([]string, len(arm.Values))
	for i, v := range arm.Values {
		values[i] = dumpExpr(v)
	}
	return strings.Join(values, ", ")
}

// dumpIfInline renders an if compactly: its condition, then body, else-if chain,
// and else body.
func dumpIfInline(s *ast.IfStmt) string {
	out := "(if " + dumpExpr(s.Cond) + " (then " + dumpInlineBody(s.Then) + ")"
	if s.ElseIf != nil {
		out += " (else-if " + dumpIfInline(s.ElseIf) + ")"
	}
	if s.Else != nil {
		out += " (else " + dumpInlineBody(s.Else) + ")"
	}
	return out + ")"
}

// dumpForInline renders a for compactly: its loop variable, of/in, the iterated
// expression, and the loop body.
func dumpForInline(s *ast.ForStmt) string {
	return "(for " + s.Var + " " + s.Kind.String() + " " + dumpExpr(s.Iter) + " (body " + dumpInlineBody(s.Body) + "))"
}

func dumpTypeDecl(b *strings.Builder, d *ast.TypeDecl) {
	b.WriteString("  TypeDecl\n")
	for _, doc := range d.Doc {
		fmt.Fprintf(b, "    doc %q\n", doc)
	}
	if d.Public {
		b.WriteString("    pub\n")
	}
	fmt.Fprintf(b, "    name %q\n", d.Name)
	for _, p := range d.Params {
		if p.Constraint != nil {
			fmt.Fprintf(b, "    param %s: %s\n", p.Name, dumpType(p.Constraint))
		} else {
			fmt.Fprintf(b, "    param %s\n", p.Name)
		}
	}
	if d.Body != nil {
		fmt.Fprintf(b, "    body %s\n", dumpType(d.Body))
	}
	if d.Where != nil {
		fmt.Fprintf(b, "    where %s\n", dumpExpr(d.Where))
	}
	for _, i := range d.Impls {
		fmt.Fprintf(b, "    impl %s\n", dumpType(i))
	}
	for _, c := range d.Consts {
		dumpAssocConst(b, c)
	}
	for _, m := range d.Methods {
		dumpMethod(b, m)
	}
}

// dumpAssocConst renders one associated constant of an impl block: its name, an
// optional type annotation and value, and the builtin marker when the value
// comes from the registry (`= builtin`).
func dumpAssocConst(b *strings.Builder, c *ast.ConstDecl) {
	fmt.Fprintf(b, "    const %q\n", c.Name)
	for _, doc := range c.Doc {
		fmt.Fprintf(b, "      doc %q\n", doc)
	}
	if c.Public {
		b.WriteString("      pub\n")
	}
	if c.Type != nil {
		fmt.Fprintf(b, "      type %s\n", dumpType(c.Type))
	}
	if c.Builtin {
		b.WriteString("      builtin\n")
	}
	if c.Value != nil {
		fmt.Fprintf(b, "      value %s\n", dumpExpr(c.Value))
	}
}

func dumpEnumDecl(b *strings.Builder, d *ast.EnumDecl) {
	b.WriteString("  EnumDecl\n")
	for _, doc := range d.Doc {
		fmt.Fprintf(b, "    doc %q\n", doc)
	}
	if d.Public {
		b.WriteString("    pub\n")
	}
	fmt.Fprintf(b, "    name %q\n", d.Name)
	if d.Base != nil {
		fmt.Fprintf(b, "    base %s\n", dumpType(d.Base))
	}
	for _, m := range d.Members {
		if m.Value != nil {
			fmt.Fprintf(b, "    member %q = %s\n", m.Name, dumpExpr(m.Value))
		} else {
			fmt.Fprintf(b, "    member %q\n", m.Name)
		}
	}
	for _, i := range d.Impls {
		fmt.Fprintf(b, "    impl %s\n", dumpType(i))
	}
	for _, c := range d.Consts {
		dumpAssocConst(b, c)
	}
	for _, m := range d.Methods {
		dumpMethod(b, m)
	}
}

// dumpInterfaceDecl renders one interface declaration: its modifiers, name,
// generic parameters, parents (supertraits), and members (required and
// provided, distinguished by whether the member carries a default body).
func dumpInterfaceDecl(b *strings.Builder, d *ast.InterfaceDecl) {
	b.WriteString("  InterfaceDecl\n")
	for _, doc := range d.Doc {
		fmt.Fprintf(b, "    doc %q\n", doc)
	}
	if d.Public {
		b.WriteString("    pub\n")
	}
	fmt.Fprintf(b, "    name %q\n", d.Name)
	for _, p := range d.Params {
		if p.Constraint != nil {
			fmt.Fprintf(b, "    param %s: %s\n", p.Name, dumpType(p.Constraint))
		} else {
			fmt.Fprintf(b, "    param %s\n", p.Name)
		}
	}
	for _, parent := range d.Parents {
		fmt.Fprintf(b, "    parent %s\n", dumpType(parent))
	}
	for _, m := range d.Members {
		dumpInterfaceMember(b, m)
	}
}

// dumpInterfaceMember renders one interface member: required or provided (the
// keyword reflects whether it carries a default body), then its signature and,
// for a provided member, its body.
func dumpInterfaceMember(b *strings.Builder, m *ast.InterfaceMember) {
	kind := "required"
	if m.Provided() {
		kind = "provided"
	}
	fmt.Fprintf(b, "    %s %q\n", kind, m.Name)
	for _, doc := range m.Doc {
		fmt.Fprintf(b, "      doc %q\n", doc)
	}
	if m.Public {
		b.WriteString("      pub\n")
	}
	for _, p := range m.TypeParams {
		fmt.Fprintf(b, "      typeparam %s\n", p.Name)
	}
	for _, p := range m.Params {
		fmt.Fprintf(b, "      param %s: %s\n", p.Name, dumpType(p.Type))
	}
	if m.Result != nil {
		fmt.Fprintf(b, "      result %s\n", dumpType(m.Result))
	}
	for _, s := range m.Body {
		dumpStmt(b, s)
	}
}

func dumpMethod(b *strings.Builder, m *ast.MethodDecl) {
	fmt.Fprintf(b, "    method %q\n", m.Name)
	for _, doc := range m.Doc {
		fmt.Fprintf(b, "      doc %q\n", doc)
	}
	if m.Public {
		b.WriteString("      pub\n")
	}
	if m.Extern {
		b.WriteString("      extern\n")
	}
	if m.Kind != ast.MethodNormal {
		fmt.Fprintf(b, "      kind %s\n", m.Kind)
	}
	if len(m.Effects) > 0 {
		fmt.Fprintf(b, "      effects %s\n", strings.Join(m.Effects, " "))
	}
	for _, p := range m.TypeParams {
		fmt.Fprintf(b, "      typeparam %s\n", p.Name)
	}
	for _, p := range m.Params {
		fmt.Fprintf(b, "      param %s: %s\n", p.Name, dumpType(p.Type))
	}
	if m.Result != nil {
		fmt.Fprintf(b, "      result %s\n", dumpType(m.Result))
	}
	for _, s := range m.Body {
		dumpStmt(b, s)
	}
}

func dumpStmt(b *strings.Builder, s ast.Stmt) {
	dumpStmtAt(b, s, "      ")
}

// dumpStmtAt renders one statement at the given indent. A switch's arm bodies
// nest one level deeper, so the indent threads through rather than being fixed,
// keeping the dump a faithful, diffable picture of the control structure.
func dumpStmtAt(b *strings.Builder, s ast.Stmt, indent string) {
	switch s := s.(type) {
	case *ast.ReturnStmt:
		fmt.Fprintf(b, "%sreturn %s\n", indent, dumpExpr(s.Value))
	case *ast.ExprStmt:
		fmt.Fprintf(b, "%sexpr %s\n", indent, dumpExpr(s.X))
	case *ast.LetStmt:
		if s.Type != nil {
			fmt.Fprintf(b, "%slet %q: %s = %s\n", indent, s.Name, dumpType(s.Type), dumpExpr(s.Value))
		} else {
			fmt.Fprintf(b, "%slet %q = %s\n", indent, s.Name, dumpExpr(s.Value))
		}
	case *ast.AssignStmt:
		fmt.Fprintf(b, "%sassign %s = %s\n", indent, dumpExpr(s.Target), dumpExpr(s.Value))
	case *ast.SwitchStmt:
		dumpSwitchAt(b, s, indent)
	case *ast.MatchStmt:
		dumpMatchAt(b, s, indent)
	case *ast.IfStmt:
		dumpIfAt(b, s, indent)
	case *ast.ForStmt:
		dumpForAt(b, s, indent)
	default:
		// The snapshot oracle must render every statement kind; a new one panics
		// here rather than dumping as nothing and masking the regression.
		panic(ast.UnhandledStmt(s))
	}
}

// dumpSwitchAt renders a switch statement at the given indent: its scrutinee,
// each value arm and its body, an optional else block, and any unreachable arms
// after the else. Arm bodies nest one level deeper.
func dumpSwitchAt(b *strings.Builder, s *ast.SwitchStmt, indent string) {
	fmt.Fprintf(b, "%sswitch %s\n", indent, dumpExpr(s.Scrutinee))
	for _, arm := range s.Arms {
		values := make([]string, len(arm.Values))
		for i, v := range arm.Values {
			values[i] = dumpExpr(v)
		}
		fmt.Fprintf(b, "%s  arm %s\n", indent, strings.Join(values, ", "))
		for _, bs := range arm.Body {
			dumpStmtAt(b, bs, indent+"    ")
		}
	}
	if s.Else != nil {
		fmt.Fprintf(b, "%s  else\n", indent)
		for _, bs := range s.Else {
			dumpStmtAt(b, bs, indent+"    ")
		}
	}
	for _, arm := range s.AfterElse {
		values := make([]string, len(arm.Values))
		for i, v := range arm.Values {
			values[i] = dumpExpr(v)
		}
		fmt.Fprintf(b, "%s  unreachable-arm %s\n", indent, strings.Join(values, ", "))
		for _, bs := range arm.Body {
			dumpStmtAt(b, bs, indent+"    ")
		}
	}
}

// dumpMatchAt renders a match statement at the given indent: its scrutinee, each
// type-pattern arm and its body, an optional else block, and any unreachable arms
// after the else. Arm bodies nest one level deeper.
func dumpMatchAt(b *strings.Builder, s *ast.MatchStmt, indent string) {
	fmt.Fprintf(b, "%smatch %s\n", indent, dumpExpr(s.Scrutinee))
	for _, arm := range s.Arms {
		fmt.Fprintf(b, "%s  arm %s\n", indent, dumpMatchPattern(arm))
		for _, bs := range arm.Body {
			dumpStmtAt(b, bs, indent+"    ")
		}
	}
	if s.Else != nil {
		fmt.Fprintf(b, "%s  else\n", indent)
		for _, bs := range s.Else {
			dumpStmtAt(b, bs, indent+"    ")
		}
	}
	for _, arm := range s.AfterElse {
		fmt.Fprintf(b, "%s  unreachable-arm %s\n", indent, dumpMatchPattern(arm))
		for _, bs := range arm.Body {
			dumpStmtAt(b, bs, indent+"    ")
		}
	}
}

// dumpForAt renders a for statement at the given indent: its variable, loop kind,
// and iterated expression, with the body nested one level deeper.
func dumpForAt(b *strings.Builder, s *ast.ForStmt, indent string) {
	fmt.Fprintf(b, "%sfor %s %s %s\n", indent, s.Var, s.Kind.String(), dumpExpr(s.Iter))
	for _, bs := range s.Body {
		dumpStmtAt(b, bs, indent+"    ")
	}
}

// dumpIfAt renders an if statement and its else-if chain at the given indent.
// The then body and each else branch nest one level deeper, so the dump mirrors
// the control structure: "if cond" then its body, "else if cond" for a chained
// if (rendered flat, not re-indented, to read as a chain), and "else" for a
// plain else block.
func dumpIfAt(b *strings.Builder, s *ast.IfStmt, indent string) {
	fmt.Fprintf(b, "%sif %s\n", indent, dumpExpr(s.Cond))
	for _, bs := range s.Then {
		dumpStmtAt(b, bs, indent+"    ")
	}
	switch {
	case s.ElseIf != nil:
		fmt.Fprintf(b, "%selse if %s\n", indent, dumpExpr(s.ElseIf.Cond))
		for _, bs := range s.ElseIf.Then {
			dumpStmtAt(b, bs, indent+"    ")
		}
		// Continue the chain in place: the nested if's own else renders at the
		// same indent, so an else-if chain reads as a flat ladder.
		dumpIfChainTail(b, s.ElseIf, indent)
	case s.Else != nil:
		fmt.Fprintf(b, "%selse\n", indent)
		for _, bs := range s.Else {
			dumpStmtAt(b, bs, indent+"    ")
		}
	}
}

// dumpIfChainTail renders the else branch of a chained if (the "else if" or
// "else" that follows an already-rendered "else if cond" head and body),
// keeping the whole chain at one indent.
func dumpIfChainTail(b *strings.Builder, s *ast.IfStmt, indent string) {
	switch {
	case s.ElseIf != nil:
		fmt.Fprintf(b, "%selse if %s\n", indent, dumpExpr(s.ElseIf.Cond))
		for _, bs := range s.ElseIf.Then {
			dumpStmtAt(b, bs, indent+"    ")
		}
		dumpIfChainTail(b, s.ElseIf, indent)
	case s.Else != nil:
		fmt.Fprintf(b, "%selse\n", indent)
		for _, bs := range s.Else {
			dumpStmtAt(b, bs, indent+"    ")
		}
	}
}

// dumpType renders a type expression in a source-like form: int8, Optional<T>,
// A | B, { id: int8 }, fn(src: T): R.
func dumpType(t ast.TypeExpr) string {
	switch t := t.(type) {
	case nil:
		return "<missing>"
	case *ast.NamedType:
		name := t.Name
		if t.Namespace != "" {
			name = t.Namespace + "." + t.Name
		}
		if len(t.Args) == 0 {
			return name
		}
		args := make([]string, len(t.Args))
		for i, a := range t.Args {
			args[i] = dumpType(a)
		}
		return name + "<" + strings.Join(args, ", ") + ">"
	case *ast.UnionType:
		parts := make([]string, len(t.Members))
		for i, m := range t.Members {
			parts[i] = dumpType(m)
		}
		return strings.Join(parts, " | ")
	case *ast.RecordType:
		parts := make([]string, len(t.Fields))
		for i, f := range t.Fields {
			parts[i] = f.Name + ": " + dumpType(f.Type)
		}
		return "{ " + strings.Join(parts, ", ") + " }"
	case *ast.FuncType:
		params := make([]string, len(t.Params))
		for i, p := range t.Params {
			params[i] = p.Name + ": " + dumpType(p.Type)
		}
		return "fn(" + strings.Join(params, ", ") + "): " + dumpType(t.Result)
	case *ast.BuiltinType:
		if len(t.Args) == 0 {
			return "builtin"
		}
		args := make([]string, len(t.Args))
		for i, a := range t.Args {
			args[i] = dumpType(a)
		}
		return "builtin<" + strings.Join(args, ", ") + ">"
	default:
		return "Type(?)"
	}
}

// TestDumpInlineNeverEmpty checks the funcLit snapshot path renders every
// statement kind as something non-empty, so a kind cannot silently vanish from
// the curated test-DSL rendering (the snapshot-masking failure mode). The four kinds with
// a dedicated inline form render their own shape; the rest take the visible
// "(stmt T)" marker.
func TestDumpInlineNeverEmpty(t *testing.T) {
	for _, s := range ast.StmtKinds() {
		if got := dumpStmtInline(s); got == "" {
			t.Errorf("dumpStmtInline(%T) = %q; a statement must never dump as empty", s, got)
		}
	}
}
