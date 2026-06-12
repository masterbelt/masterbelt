package semantic

import (
	"math/big"
	"testing"

	"github.com/masterbelt/masterbelt/pkg/belt/eval"
	"github.com/masterbelt/masterbelt/pkg/source/ir"
)

// TestEvalEnvRefinement pins what EvalEnv exposes: a layer above the engine can
// take a refined type's folded predicate and run it against a value of its own
// — the master data layer's per-cell refinement check — and get back the same
// verdict the engine would. The env carries the registry and universe the fold
// needs, so a caller never reaches into the engine's internals to do it.
func TestEvalEnvRefinement(t *testing.T) {
	const file = FileID("m.belt")
	p := buildProgram(map[string]string{
		string(file): "type Positive = int where self > 0\n",
	})
	assertClean(t, p, file)

	def := typeDefNamed(t, p, file, "Positive")
	if def.Where == nil {
		t.Fatal("Positive has no folded refinement predicate")
	}
	env := p.EvalEnv(file)

	for _, c := range []struct {
		value int64
		want  bool
	}{{5, true}, {1, true}, {0, false}, {-1, false}} {
		got := eval.GraphPredicate(def.Where, ir.IntConstant(big.NewInt(c.value)), def, env)
		if got == nil || got.Kind != ir.ConstBool {
			t.Fatalf("GraphPredicate(self=%d) = %v, want a bool constant", c.value, got)
		}
		if got.Bool != c.want {
			t.Errorf("Positive holds for %d = %v, want %v", c.value, got.Bool, c.want)
		}
	}
}

func typeDefNamed(t *testing.T, p *Program, file FileID, name string) *ir.TypeDef {
	t.Helper()
	for _, d := range p.Module(file).Types {
		if d.Name == name {
			return d
		}
	}
	t.Fatalf("%s: no type %q in module", file, name)
	return nil
}
