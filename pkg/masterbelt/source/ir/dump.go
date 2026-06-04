package ir

import (
	"fmt"
	"strings"
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
	return b.String()
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
	if t.Body != nil {
		fmt.Fprintf(b, "    body %s\n", t.Body)
	}
	for _, m := range t.Methods {
		dumpMethod(b, m)
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
	for _, p := range m.Params {
		fmt.Fprintf(b, "      param %s: %s\n", p.Name, typeString(p.Type))
	}
	if m.Result != nil {
		fmt.Fprintf(b, "      result %s\n", m.Result)
	}
	if m.Body != nil {
		fmt.Fprintf(b, "      body %s\n", dumpValue(m.Body))
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
	case *BoolLiteral:
		return fmt.Sprintf("BoolLiteral %v", x.Value)
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
	case *SelfValue:
		return "self"
	case *ParamRef:
		return fmt.Sprintf("ParamRef %q", x.Name)
	case *FieldAccess:
		return fmt.Sprintf("%s.%s", dumpValue(x.Receiver), x.Field)
	case *Conversion:
		return fmt.Sprintf("%s(%s)", x.Type, dumpValue(x.Value))
	case *NullValue:
		return "null"
	default:
		return "<none>"
	}
}
