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
	return b.String()
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
	default:
		return "<none>"
	}
}
