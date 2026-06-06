package ir

import (
	"fmt"
	"strings"

	"github.com/masterbelt/masterbelt/pkg/source/ast"
)

// Dump renders a Module as a stable, diffable text tree. It reads only resolved
// fields, so two Modules dump identically exactly when they are semantically
// equal — making Dump both the snapshot format and the oracle the incremental
// analyzer is checked against.
func Dump(m *Module) string {
	var b strings.Builder
	b.WriteString("Module\n")
	for _, c := range m.Consts {
		dumpConst(&b, c)
	}
	for _, t := range m.Types {
		dumpTypeDef(&b, t)
	}
	for _, f := range m.Funcs {
		dumpFunction(&b, f)
	}
	for _, a := range m.Asserts {
		dumpAssert(&b, a)
	}
	return b.String()
}

// dumpFunction renders one top-level function: its signature and lowered body,
// in the same shape a method dumps under its type.
func dumpFunction(b *strings.Builder, f *Function) {
	mod := ""
	if f.Public {
		mod += " pub"
	}
	if f.Extern {
		mod += " extern"
	}
	fmt.Fprintf(b, "  Func %q%s\n", f.Name, mod)
	for _, doc := range f.Doc {
		fmt.Fprintf(b, "    doc %q\n", doc)
	}
	if len(f.Effects) > 0 {
		fmt.Fprintf(b, "    effects %s\n", strings.Join(f.Effects, " "))
	}
	for _, p := range f.TypeParams {
		if p.Bound != nil {
			fmt.Fprintf(b, "    typeparam %q: %s\n", p.Name, p.Bound)
		} else {
			fmt.Fprintf(b, "    typeparam %q\n", p.Name)
		}
	}
	for _, p := range f.Params {
		fmt.Fprintf(b, "    param %s: %s\n", p.Name, typeString(p.Type))
	}
	if f.Result != nil {
		fmt.Fprintf(b, "    result %s\n", f.Result)
	}
	for _, s := range f.Body {
		dumpStmt(b, s)
	}
}

// dumpAssert renders one assertion's outcome: its canonical condition, its
// folded value, and the power-assert diagram (indented as a block), so the
// snapshot proves what every assertion evaluated to.
func dumpAssert(b *strings.Builder, a *Assert) {
	fmt.Fprintf(b, "  Assert %q\n", a.Cond)
	for _, doc := range a.Doc {
		fmt.Fprintf(b, "    doc %q\n", doc)
	}
	fmt.Fprintf(b, "    eval %s\n", a.Eval)
	for line := range strings.SplitSeq(a.Diagram, "\n") {
		fmt.Fprintf(b, "      %s\n", line)
	}
}

func dumpTypeDef(b *strings.Builder, t *TypeDef) {
	mod := ""
	if t.Public {
		mod = " pub"
	}
	fmt.Fprintf(b, "  TypeDef %q%s\n", t.Name, mod)
	for _, doc := range t.Doc {
		fmt.Fprintf(b, "    doc %q\n", doc)
	}
	for _, p := range t.Params {
		if p.Bound != nil {
			fmt.Fprintf(b, "    param %q: %s\n", p.Name, p.Bound)
		} else {
			fmt.Fprintf(b, "    param %q\n", p.Name)
		}
	}
	if t.Enum != nil {
		fmt.Fprintf(b, "    enum %s\n", t.Enum.Base)
		for _, m := range t.Enum.Members {
			fmt.Fprintf(b, "    member %q = %s\n", m.Name, m.Value.String())
		}
	}
	if t.Interface != nil {
		b.WriteString("    interface\n")
		for _, name := range t.Interface.Required {
			fmt.Fprintf(b, "    required %q\n", name)
		}
		for _, name := range t.Interface.Provided {
			fmt.Fprintf(b, "    provided %q\n", name)
		}
	}
	for _, i := range t.Impls {
		fmt.Fprintf(b, "    impl %s\n", typeString(i))
	}
	if t.Body != nil {
		fmt.Fprintf(b, "    body %s\n", t.Body)
	}
	if t.Where != nil {
		// The canonical surface form (ast.Render inverts the operator
		// desugaring), so the snapshot reads like the declaration.
		fmt.Fprintf(b, "    where %s\n", ast.Render(t.Where))
	}
	for _, c := range t.Consts {
		dumpAssocConst(b, c)
	}
	for _, m := range t.Methods {
		dumpMethod(b, m)
	}
}

// dumpAssocConst renders one associated constant of a type: its name, modifiers,
// resolved type, and folded value (the builtin marker for a registry-supplied
// one). The value is the proof the snapshot carries — it is what TypeName.Name
// folds to.
func dumpAssocConst(b *strings.Builder, c *AssocConst) {
	mod := ""
	if c.Public {
		mod = " pub"
	}
	if c.Builtin {
		mod += " builtin"
	}
	fmt.Fprintf(b, "    const %q%s\n", c.Name, mod)
	for _, doc := range c.Doc {
		fmt.Fprintf(b, "      doc %q\n", doc)
	}
	fmt.Fprintf(b, "      type %s\n", typeString(c.Type))
	if c.Value != nil {
		fmt.Fprintf(b, "      value %s\n", c.Value)
	}
}

func dumpMethod(b *strings.Builder, m *Method) {
	mod := ""
	if m.Public {
		mod += " pub"
	}
	if m.Extern {
		mod += " extern"
	}
	fmt.Fprintf(b, "    method %q%s\n", m.Name, mod)
	for _, doc := range m.Doc {
		fmt.Fprintf(b, "      doc %q\n", doc)
	}
	if len(m.Effects) > 0 {
		fmt.Fprintf(b, "      effects %s\n", strings.Join(m.Effects, " "))
	}
	for _, p := range m.Params {
		fmt.Fprintf(b, "      param %s: %s\n", p.Name, typeString(p.Type))
	}
	if m.Result != nil {
		fmt.Fprintf(b, "      result %s\n", m.Result)
	}
	for _, s := range m.Body {
		dumpStmt(b, s)
	}
}

func dumpStmt(b *strings.Builder, s Stmt) {
	dumpStmtAt(b, s, "      ")
}

// dumpStmtAt renders one resolved statement at the given indent. A switch's arm
// bodies nest one level deeper, so the indent threads through rather than being
// fixed — the dump mirrors the control structure the analyzer resolved.
func dumpStmtAt(b *strings.Builder, s Stmt, indent string) {
	switch s := s.(type) {
	case *Return:
		fmt.Fprintf(b, "%sreturn %s\n", indent, dumpValue(s.Value))
	case *ExprStmt:
		fmt.Fprintf(b, "%sexpr %s\n", indent, dumpValue(s.Value))
	case *Let:
		fmt.Fprintf(b, "%slet %q: %s = %s\n", indent, s.Name, s.Type, dumpValue(s.Value))
	case *Assign:
		fmt.Fprintf(b, "%sassign %q = %s\n", indent, s.Name, dumpValue(s.Value))
	case *Switch:
		fmt.Fprintf(b, "%sswitch %s\n", indent, dumpValue(s.Scrutinee))
		for _, arm := range s.Arms {
			values := make([]string, len(arm.Values))
			for i, v := range arm.Values {
				values[i] = dumpValue(v)
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
	case *Match:
		fmt.Fprintf(b, "%smatch %s\n", indent, dumpValue(s.Scrutinee))
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
	case *If:
		dumpIfAt(b, s, indent)
	default:
		// The snapshot oracle must render every lowered statement kind; a new
		// one panics here rather than dumping as nothing.
		panic(unhandledStmt(s))
	}
}

// dumpMatchPattern renders a resolved match arm's type pattern: its member type
// and, when present, its narrowed binding name (Coin c, null, int v).
func dumpMatchPattern(arm MatchArm) string {
	pat := typeStringOrMissing(arm.Type)
	if arm.Name != "" {
		pat += " " + arm.Name
	}
	return pat
}

// typeStringOrMissing renders a resolved type, or "<missing>" for a nil one (an
// arm whose member type did not resolve), so the dump never panics on a hole.
func typeStringOrMissing(t Type) string {
	if t == nil {
		return "<missing>"
	}
	return t.String()
}

// dumpIfAt renders a resolved if and its else-if chain at the given indent. The
// then body and each else branch nest one level deeper, and the chain renders
// flat as a ladder ("if cond", "else if cond", "else"), mirroring the control
// structure the analyzer resolved.
func dumpIfAt(b *strings.Builder, s *If, indent string) {
	fmt.Fprintf(b, "%sif %s\n", indent, dumpValue(s.Cond))
	for _, bs := range s.Then {
		dumpStmtAt(b, bs, indent+"    ")
	}
	for cur := s; ; {
		switch {
		case cur.ElseIf != nil:
			cur = cur.ElseIf
			fmt.Fprintf(b, "%selse if %s\n", indent, dumpValue(cur.Cond))
			for _, bs := range cur.Then {
				dumpStmtAt(b, bs, indent+"    ")
			}
		case cur.Else != nil:
			fmt.Fprintf(b, "%selse\n", indent)
			for _, bs := range cur.Else {
				dumpStmtAt(b, bs, indent+"    ")
			}
			return
		default:
			return
		}
	}
}

// dumpStmtInline renders a statement compactly on one line, for a function
// literal that appears inside an enclosing value. (dumpStmt is the multi-line,
// indented form used for a method body.)
func dumpStmtInline(s Stmt) string {
	switch s := s.(type) {
	case *Return:
		return "(return " + dumpValue(s.Value) + ")"
	case *ExprStmt:
		return "(expr " + dumpValue(s.Value) + ")"
	case *Let:
		return fmt.Sprintf("(let %s: %s = %s)", s.Name, s.Type, dumpValue(s.Value))
	case *Assign:
		return fmt.Sprintf("(assign %s = %s)", s.Name, dumpValue(s.Value))
	case *Switch:
		parts := []string{"switch " + dumpValue(s.Scrutinee)}
		for _, arm := range s.Arms {
			values := make([]string, len(arm.Values))
			for i, v := range arm.Values {
				values[i] = dumpValue(v)
			}
			body := make([]string, len(arm.Body))
			for i, bs := range arm.Body {
				body[i] = dumpStmtInline(bs)
			}
			parts = append(parts, "(arm "+strings.Join(values, ", ")+" "+strings.Join(body, " ")+")")
		}
		if s.Else != nil {
			body := make([]string, len(s.Else))
			for i, bs := range s.Else {
				body[i] = dumpStmtInline(bs)
			}
			parts = append(parts, "(else "+strings.Join(body, " ")+")")
		}
		return "(" + strings.Join(parts, " ") + ")"
	case *Match:
		parts := []string{"match " + dumpValue(s.Scrutinee)}
		for _, arm := range s.Arms {
			body := make([]string, len(arm.Body))
			for i, bs := range arm.Body {
				body[i] = dumpStmtInline(bs)
			}
			parts = append(parts, "(arm "+dumpMatchPattern(arm)+" "+strings.Join(body, " ")+")")
		}
		if s.Else != nil {
			body := make([]string, len(s.Else))
			for i, bs := range s.Else {
				body[i] = dumpStmtInline(bs)
			}
			parts = append(parts, "(else "+strings.Join(body, " ")+")")
		}
		return "(" + strings.Join(parts, " ") + ")"
	case *If:
		parts := []string{"if " + dumpValue(s.Cond)}
		then := make([]string, len(s.Then))
		for i, bs := range s.Then {
			then[i] = dumpStmtInline(bs)
		}
		parts = append(parts, "(then "+strings.Join(then, " ")+")")
		for cur := s; ; {
			if cur.ElseIf != nil {
				cur = cur.ElseIf
				inner := make([]string, len(cur.Then))
				for i, bs := range cur.Then {
					inner[i] = dumpStmtInline(bs)
				}
				parts = append(parts, "(elseif "+dumpValue(cur.Cond)+" "+strings.Join(inner, " ")+")")
				continue
			}
			if cur.Else != nil {
				body := make([]string, len(cur.Else))
				for i, bs := range cur.Else {
					body[i] = dumpStmtInline(bs)
				}
				parts = append(parts, "(else "+strings.Join(body, " ")+")")
			}
			break
		}
		return "(" + strings.Join(parts, " ") + ")"
	default:
		// Every ir.Stmt has an inline form above; a kind added later panics
		// rather than rendering as the empty string and vanishing from the
		// snapshot oracle.
		panic(unhandledStmt(s))
	}
}

func dumpConst(b *strings.Builder, c *Const) {
	mod := ""
	if c.Public {
		mod = " pub"
	}
	fmt.Fprintf(b, "  Const %q%s\n", c.Name, mod)
	for _, doc := range c.Doc {
		fmt.Fprintf(b, "    doc %q\n", doc)
	}
	fmt.Fprintf(b, "    type %s\n", c.Type)
	fmt.Fprintf(b, "    value %s\n", dumpValue(c.Value))
	if c.Eval != nil {
		fmt.Fprintf(b, "    eval %s\n", c.Eval)
	}
}

func dumpValue(v Value) string {
	switch x := v.(type) {
	case *IntLiteral:
		return fmt.Sprintf("IntLiteral %q", x.Text)
	case *StringLiteral:
		return fmt.Sprintf("StringLiteral %q", x.Value)
	case *BoolLiteral:
		return fmt.Sprintf("BoolLiteral %v", x.Value)
	case *DatetimeLiteral:
		return fmt.Sprintf("DatetimeLiteral %q", x.Text)
	case *DurationLiteral:
		return fmt.Sprintf("DurationLiteral %q", x.Text)
	case *CollectionLiteral:
		parts := make([]string, len(x.Entries))
		for i, e := range x.Entries {
			if e.Key != nil {
				parts[i] = dumpValue(e.Key) + ": " + dumpValue(e.Value)
			} else {
				parts[i] = dumpValue(e.Value)
			}
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case *RecordValue:
		// TypeName{f: v, ...} for the typed form, {f: v, ...} for the inferred.
		parts := make([]string, len(x.Fields))
		for i, f := range x.Fields {
			parts[i] = f.Name + ": " + dumpValue(f.Value)
		}
		return x.TypeName + "{" + strings.Join(parts, ", ") + "}"
	case *Reference:
		name := "<unresolved>"
		if x.Target != nil {
			name = x.Target.Name
		}
		return fmt.Sprintf("Reference -> %q", name)
	case *Call:
		// receiver.method(arg, arg) with each operand rendered recursively.
		args := make([]string, len(x.Args))
		for i, a := range x.Args {
			args[i] = dumpValue(a)
		}
		return fmt.Sprintf("%s.%s(%s)", dumpValue(x.Receiver), x.Method, strings.Join(args, ", "))
	case *FuncCall:
		name := "<unresolved>"
		if x.Target != nil {
			name = x.Target.Name
		}
		args := make([]string, len(x.Args))
		for i, a := range x.Args {
			args[i] = dumpValue(a)
		}
		return fmt.Sprintf("%s(%s)", name, strings.Join(args, ", "))
	case *FuncLiteral:
		parts := []string{"fn(" + strings.Join(x.Params, ", ") + ")"}
		for _, s := range x.Body {
			parts = append(parts, dumpStmtInline(s))
		}
		return "(" + strings.Join(parts, " ") + ")"
	case *SelfValue:
		return "self"
	case *ParamRef:
		return fmt.Sprintf("ParamRef %q", x.Name)
	case *LocalRef:
		return fmt.Sprintf("LocalRef %q", x.Name)
	case *FieldAccess:
		return fmt.Sprintf("%s.%s", dumpValue(x.Receiver), x.Field)
	case *Conversion:
		return fmt.Sprintf("%s(%s)", x.Type, dumpValue(x.Value))
	case *Await:
		return "await " + dumpValue(x.Value)
	case *Ternary:
		return fmt.Sprintf("(%s ? %s : %s)", dumpValue(x.Cond), dumpValue(x.Then), dumpValue(x.Else))
	case *NullValue:
		return "null"
	case *EnumMemberValue:
		name := "<unresolved>"
		if x.Def != nil {
			name = x.Def.Name
			if x.Def.Enum != nil && x.Index >= 0 && x.Index < len(x.Def.Enum.Members) {
				name += "." + x.Def.Enum.Members[x.Index].Name
			}
		}
		return name
	case *AssocConstValue:
		name := "<unresolved>"
		if x.Def != nil {
			name = x.Def.Name
			if x.Index >= 0 && x.Index < len(x.Def.Consts) {
				name += "." + x.Def.Consts[x.Index].Name
			}
		}
		return name
	default:
		return "<none>"
	}
}
