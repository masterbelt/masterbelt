// Package sample is treegen's own test fixture: a miniature tree package
// exercising every field kind the generator classifies — scalars, enums,
// excluded fields, concrete and interface nodes, and both list forms.
package sample

// Node is the marker interface: implementers get MarshalText.
type Node interface{ node() }

// Expr is a sealed sub-interface used as a field type.
type Expr interface {
	Node
	expr()
}

// Kind is a named int (an enum) carried inline.
type Kind int

// Root is the unmarshal root, with one field of every kind.
type Root struct {
	Name   string
	Flag   bool
	Tags   []string
	Num    int
	Kind   Kind
	Value  Expr
	Single *Leaf
	Items  []*Leaf
	Mixed  []Expr
	Skip   int `tree:"-"`
	hidden int //nolint:unused // pins that unexported fields are excluded by construction
}

func (*Root) node() {}

// Leaf is a plain node that also implements Expr.
type Leaf struct {
	Text string
}

func (*Leaf) node() {}
func (*Leaf) expr() {}

// Pair is an auxiliary struct reachable only through a field, implementing
// nothing — it must still get a codec.
type Pair struct {
	Key   Expr
	Value Expr
}

// Wrap pulls Pair into the model.
type Wrap struct {
	Entries []*Pair
}

func (*Wrap) node() {}
func (*Wrap) expr() {}
