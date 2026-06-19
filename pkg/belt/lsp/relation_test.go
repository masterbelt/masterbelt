package lsp

import (
	"strings"
	"testing"

	protocol "github.com/owenrumney/go-lsp/lsp"

	"github.com/masterbelt/masterbelt/internal/belttest"
)

// the master the relation-completion tests offer members of.
const relationMaster = "master Cards {\n" +
	"  record { id: int, cost: int } impl {\n" +
	"    pub static fn zero(): nint {\n      return 0\n    }\n" +
	"  }\n" +
	"  primary id\n}\n"

// TestCompletionRelationMethods pins that a bare master name in value position offers
// its relation's query methods (where, count, sum) — the master is its relation —
// alongside its static fns, which are reached through the same name.
func TestCompletionRelationMethods(t *testing.T) {
	src := relationMaster + "fn probe(): nint {\n  return Cards.\n}\n"
	doc := testView(src)
	off := strings.Index(src, "Cards.\n") + len("Cards.")
	items := byLabel(completion(doc, off).Items)
	for _, m := range []string{"where", "count", "sum"} {
		it, ok := items[m]
		if !ok {
			t.Errorf("Cards. should offer the relation method %q: %v", m, labels(items))
			continue
		}
		if it.Kind == nil || *it.Kind != protocol.CompletionItemKindMethod {
			t.Errorf("%s kind = %v, want Method", m, it.Kind)
		}
	}
	// The master's static fn is offered too: a master is both a relation value and a
	// type with static fns.
	if _, ok := items["zero"]; !ok {
		t.Errorf("Cards. should also offer the static fn zero: %v", labels(items))
	}
}

// TestHoverRelationMethod pins that a relation method call on a bare master name
// hovers with the method's signature — the receiver resolves to relation<Cards>, so
// count is a known method, not an unresolved member.
func TestHoverRelationMethod(t *testing.T) {
	src := relationMaster + "fn probe(): nint {\n  return Cards.count()\n}\n"
	doc := testView(src)
	h := hover(doc, strings.Index(src, "Cards.count()")+len("Cards."))
	if h == nil {
		t.Fatal("no hover on the relation method count")
	}
	if !strings.Contains(h.Contents.Value, "count") {
		t.Errorf("hover for count should name it: %q", h.Contents.Value)
	}
}

// TestCompletionStaticFnShadowsRelationMethod pins that a master's static fn named
// like a relation method (count) wins: count is offered once, as the static fn (the
// checker resolves the static call first), while the unshadowed relation methods
// (where, sum) still appear.
func TestCompletionStaticFnShadowsRelationMethod(t *testing.T) {
	src := "master Cards {\n  record { id: int, cost: int } impl {\n" +
		"    pub static fn count(): nint {\n      return 0\n    }\n" +
		"  }\n  primary id\n}\n" +
		"fn probe(): nint {\n  return Cards.\n}\n"
	doc := testView(src)
	off := strings.Index(src, "Cards.\n") + len("Cards.")
	all := completion(doc, off).Items
	n := 0
	var countKind *protocol.CompletionItemKind
	for _, it := range all {
		if it.Label == "count" {
			n++
			countKind = it.Kind
		}
	}
	if n != 1 {
		t.Errorf("count should appear once (the static fn shadows the relation method), got %d", n)
	}
	if countKind == nil || *countKind != protocol.CompletionItemKindFunction {
		t.Errorf("count should be the static fn (Function), got kind %v", countKind)
	}
	if _, ok := byLabel(all)["where"]; !ok {
		t.Errorf("the unshadowed relation method where should still be offered: %v", labels(byLabel(all)))
	}
}

// TestCompletionLocalShadowsMaster pins that a body-local of a master's name shadows
// it: Cards. after let Cards = ... does not advertise the relation methods, since the
// expression refers to the local, not the relation.
func TestCompletionLocalShadowsMaster(t *testing.T) {
	src := "master Cards {\n  record { id: int, cost: int }\n  primary id\n}\n" +
		"fn probe(): nint {\n  let Cards = 5\n  return Cards.\n}\n"
	doc := testView(src)
	off := strings.LastIndex(src, "Cards.") + len("Cards.")
	items := byLabel(completion(doc, off).Items)
	for _, m := range []string{"where", "count", "sum"} {
		if _, ok := items[m]; ok {
			t.Errorf("a local Cards shadows the master; %q must not be offered: %v", m, labels(items))
		}
	}
}

// TestHoverNoRelationInConst pins that a master in a constant initializer is not read
// as a relation: a const cannot evaluate one, so the checker keeps it a metatype and
// hover does not present count as a relation method.
func TestHoverNoRelationInConst(t *testing.T) {
	src := "master Cards {\n  record { id: int, cost: int }\n  primary id\n}\n" +
		"const X = Cards.count()\n"
	doc := testView(src)
	if h := hover(doc, strings.Index(src, "Cards.count()")+len("Cards.")); h != nil {
		t.Errorf("count in a const initializer is not a relation method; want no hover, got %q", h.Contents.Value)
	}
}

// TestHoverChainedRelationMethod pins that a relation method on a chained relation
// resolves: in Cards.where(...).count(), the receiver of count is the where call, so
// its result type (relation<Cards>) must carry through for count to hover.
func TestHoverChainedRelationMethod(t *testing.T) {
	src := relationMaster + "fn probe(): nint {\n  return Cards.where(fn(c) -> c.cost > 0).count()\n}\n"
	doc := testView(src)
	h := hover(doc, strings.LastIndex(src, ".count()")+1)
	if h == nil {
		t.Fatal("no hover on the trailing count of a relation chain")
	}
	if !strings.Contains(h.Contents.Value, "count") {
		t.Errorf("hover should name count: %q", h.Contents.Value)
	}
}

// TestHoverRelationMethodInValidate pins that a master reads as its relation inside a
// validate clause — the checker type-checks validate bodies with a body scope — so a
// relation method there hovers.
func TestHoverRelationMethodInValidate(t *testing.T) {
	src := "master Items {\n  record { id: int, power: int }\n  primary id\n" +
		"  validate {\n    each {\n      assert Items.count() > 0\n    }\n  }\n}\n"
	doc := testView(src)
	h := hover(doc, strings.Index(src, "Items.count()")+len("Items."))
	if h == nil {
		t.Fatal("a master reads as its relation in a validate clause; count should hover")
	}
}

// TestHoverLetAfterUseDoesNotShadow pins that a let shadowing the master is only in
// scope after its declaration: a Cards.count() before a later let Cards still reads as
// the relation, so count hovers.
func TestHoverLetAfterUseDoesNotShadow(t *testing.T) {
	src := relationMaster + "fn probe(): nint {\n  let a = Cards.count()\n  let Cards = 5\n  return a + Cards\n}\n"
	doc := testView(src)
	h := hover(doc, strings.Index(src, "Cards.count()")+len("Cards."))
	if h == nil {
		t.Fatal("a let Cards after the use does not shadow it; count should hover as a relation method")
	}
	if !strings.Contains(h.Contents.Value, "count") {
		t.Errorf("hover should name count: %q", h.Contents.Value)
	}
}

// TestCompletionTypeParamShadowsMaster pins that a type parameter of the enclosing
// generic type named like a master shadows it: inside an impl on Box<Cards>, Cards is
// the type parameter, so its relation methods are not offered.
func TestCompletionTypeParamShadowsMaster(t *testing.T) {
	src := "master Cards {\n  record { id: int }\n  primary id\n}\n" +
		"pub type Box<Cards> = { v: Cards } impl {\n" +
		"  pub fn probe(): nint {\n    let x = Cards.\n    return 0\n  }\n}\n"
	doc := testView(src)
	off := strings.Index(src, "Cards.\n") + len("Cards.")
	items := byLabel(completion(doc, off).Items)
	for _, m := range []string{"where", "count", "sum"} {
		if _, ok := items[m]; ok {
			t.Errorf("the type parameter Cards shadows the master; %q must not be offered: %v", m, labels(items))
		}
	}
}

// TestCompletionAssocConstDoesNotShadowRelationMethod pins that a master's
// associated constant named like a relation method does not drop the method: only a
// static fn shadows a relation method (the checker resolves the static call first),
// while a constant of the same name is a value reached without a call, so both are
// offered — Cards. with a const sum still advertises the sum relation method.
func TestCompletionAssocConstDoesNotShadowRelationMethod(t *testing.T) {
	src := "master Cards {\n  record { id: int, cost: int } impl {\n" +
		"    pub const sum: int = 0\n" +
		"  }\n  primary id\n}\n" +
		"fn probe(): nint {\n  return Cards.\n}\n"
	doc := testView(src)
	off := strings.Index(src, "Cards.\n") + len("Cards.")
	all := completion(doc, off).Items
	found := false
	for _, it := range all {
		if it.Label == "sum" && it.Kind != nil && *it.Kind == protocol.CompletionItemKindMethod {
			found = true
		}
	}
	if !found {
		t.Errorf("a const sum must not shadow the relation method sum; it should still be offered as a Method: %v", labels(byLabel(all)))
	}
}

// TestHoverChainOnSelfReturningMethod pins that a chained call on a self-returning
// method hovers: the receiver of the second inc is the first inc() call, whose result
// the checker settles to the receiver's own type (self resolved), so the chain
// resolves the next method on it. The receiver typing reads the checker's settled
// type, not a re-derivation that would leave a self result unsubstituted.
func TestHoverChainOnSelfReturningMethod(t *testing.T) {
	src := "pub type Counter = int impl {\n" +
		"  pub fn inc(): self {\n    return self\n  }\n}\n" +
		"fn probe(c: Counter): Counter {\n  return c.inc().inc()\n}\n"
	doc := testView(src)
	h := hover(doc, strings.LastIndex(src, ".inc()")+1)
	if h == nil {
		t.Fatal("no hover on the trailing inc of a self-returning chain")
	}
	if !strings.Contains(h.Contents.Value, "inc") {
		t.Errorf("hover should name inc: %q", h.Contents.Value)
	}
}

// TestHoverRelationMethodOnTernaryReceiver pins that a relation method on a ternary
// receiver hovers: the checker settles (flag ? Cards : Cards) to relation<Cards>, and
// the editor reads that settled type for every receiver form, not only a call or a
// bare name, so a chained count resolves on it.
func TestHoverRelationMethodOnTernaryReceiver(t *testing.T) {
	src := relationMaster + "fn probe(flag: bool): nint {\n  return (flag ? Cards : Cards).count()\n}\n"
	doc := testView(src)
	h := hover(doc, strings.LastIndex(src, ".count()")+1)
	if h == nil {
		t.Fatal("no hover on count of a ternary relation receiver")
	}
	if !strings.Contains(h.Contents.Value, "count") {
		t.Errorf("hover should name count: %q", h.Contents.Value)
	}
}

// TestHoverRelationMethodInSwitchScrutinee pins that a relation read in a switch
// scrutinee is typed for the editor: the checker types the scrutinee Cards.count() in
// a body, so count hovers as a relation method even though the switch's own
// diagnostics are suppressed in the editor's type-capture walk.
func TestHoverRelationMethodInSwitchScrutinee(t *testing.T) {
	src := relationMaster + "fn probe(): nint {\n  switch Cards.count() {\n    0 -> return 0\n    _ -> return 1\n  }\n}\n"
	doc := testView(src)
	h := hover(doc, strings.Index(src, "Cards.count()")+len("Cards."))
	if h == nil {
		t.Fatal("no hover on count in a switch scrutinee")
	}
	if !strings.Contains(h.Contents.Value, "count") {
		t.Errorf("hover should name count: %q", h.Contents.Value)
	}
}

// TestHoverRelationMethodInSwitchArmValue pins that a relation read in a switch arm
// value is typed for the editor: the arm value Cards.count() is checked against the
// scrutinee in a body, so count hovers as a relation method even though the switch's
// own diagnostics are suppressed in the editor's type-capture walk.
func TestHoverRelationMethodInSwitchArmValue(t *testing.T) {
	src := relationMaster + "fn probe(n: nint): nint {\n  switch n {\n    Cards.count() -> return 0\n    _ -> return 1\n  }\n}\n"
	doc := testView(src)
	h := hover(doc, strings.Index(src, "Cards.count()")+len("Cards."))
	if h == nil {
		t.Fatal("no hover on count in a switch arm value")
	}
	if !strings.Contains(h.Contents.Value, "count") {
		t.Errorf("hover should name count: %q", h.Contents.Value)
	}
}

// TestHoverGenericRelationMethodShowsTypeParams pins that a generic relation method's
// hover renders its type-parameter list and bound — sum<T: numeric> — so a T in its
// parameters and result is explained, the way a function signature shows its own.
func TestHoverGenericRelationMethodShowsTypeParams(t *testing.T) {
	src := relationMaster + "fn probe(): nint {\n  return Cards.sum(fn(c) -> c.cost)\n}\n"
	doc := testView(src)
	h := hover(doc, strings.Index(src, "Cards.sum(")+len("Cards."))
	if h == nil {
		t.Fatal("no hover on the relation method sum")
	}
	if !strings.Contains(h.Contents.Value, "<T: numeric>") {
		t.Errorf("sum hover should render its type parameter <T: numeric>: %q", h.Contents.Value)
	}
}

// TestHoverCalledMasterMemberPrefersRelationMethod pins that hovering a called master
// member resolves to the relation method, not a same-named associated constant: the
// checker resolves Cards.sum(...) to the relation method (a const only shadows a read,
// not a call), so the constant hover yields to the relation method's signature.
func TestHoverCalledMasterMemberPrefersRelationMethod(t *testing.T) {
	src := "master Cards {\n  record { id: int, cost: int } impl {\n    pub const sum: int = 0\n  }\n  primary id\n}\n" +
		"fn probe(): nint {\n  return Cards.sum(fn(c) -> c.cost)\n}\n"
	doc := testView(src)
	h := hover(doc, strings.Index(src, "Cards.sum(")+len("Cards."))
	if h == nil {
		t.Fatal("no hover on the called master member sum")
	}
	// The relation method's signature names its numeric bound; the associated
	// constant would render `sum: int = 0`, which carries no such word.
	if !strings.Contains(h.Contents.Value, "numeric") {
		t.Errorf("a called master member should hover as the relation method, not the const: %q", h.Contents.Value)
	}
}

// TestHoverSelfRelationMethodInStaticFn pins that self in a master's static fn reads
// as the master's relation in the editor too: self.count() hovers count as a relation
// method, the checker's settled self type winning over the enclosing-owner (row)
// reading so the relation's query methods surface rather than the row's.
func TestHoverSelfRelationMethodInStaticFn(t *testing.T) {
	src := "master Cards {\n  record { id: int, cost: int } impl {\n" +
		"    pub static fn total(): nint {\n      return self.count()\n    }\n" +
		"  }\n  primary id\n}\n"
	doc := testView(src)
	h := hover(doc, strings.Index(src, "self.count()")+len("self."))
	if h == nil {
		t.Fatal("self in a master static fn is its relation; count should hover as a relation method")
	}
	if !strings.Contains(h.Contents.Value, "count") {
		t.Errorf("hover should name the relation method count: %q", h.Contents.Value)
	}
}

// qualifiedRelationMain is the file that queries an imported master's relation.
const qualifiedRelationMain = "use deck from \"cards.belt\"\n" +
	"fn probe(): nint {\n  return deck.Cards.count()\n}\n"

// qualifiedShadowMain queries an imported master whose file also exports a
// same-named constant.
const qualifiedShadowMain = "use deck from \"cards.belt\"\n" +
	"fn probe(): nint {\n  return deck.Cards.count()\n}\n"

// TestHoverQualifiedRelationMethodPastNamespaceConst pins that a same-named constant
// exported alongside a master does not mask the relation: the checker reads
// deck.Cards as the master's relation (it shadows by the namespace name, not the
// member), so the editor reads its settled type before the namespace constant and
// count still hovers as a relation method.
func TestHoverQualifiedRelationMethodPastNamespaceConst(t *testing.T) {
	root := belttest.WriteFiles(t, map[string]string{
		"masterbelt.toml": "entry = \"main.belt\"\n",
		"cards.belt":      "pub master Cards {\n  record { id: int, cost: int }\n  primary id\n}\npub const Cards = 1\n",
		"main.belt":       qualifiedShadowMain,
	})
	s := NewServer()
	uri := openOnDisk(t, s, root, "main.belt")
	v := s.open[uri]
	off := strings.Index(qualifiedShadowMain, "deck.Cards.count()") + len("deck.Cards.")
	h := hover(v, off)
	if h == nil {
		t.Fatal("a same-named const must not mask the imported master's relation; count should hover")
	}
	if !strings.Contains(h.Contents.Value, "count") {
		t.Errorf("hover should name count: %q", h.Contents.Value)
	}
}

// TestHoverQualifiedRelationMethod pins that a master reached through a namespace
// import is its relation in the editor too: deck.Cards.count() hovers count as a
// relation method, the qualified twin of the bare-master reading.
func TestHoverQualifiedRelationMethod(t *testing.T) {
	root := belttest.WriteFiles(t, map[string]string{
		"masterbelt.toml": "entry = \"main.belt\"\n",
		"cards.belt":      "pub master Cards {\n  record { id: int, cost: int }\n  primary id\n}\n",
		"main.belt":       qualifiedRelationMain,
	})
	s := NewServer()
	uri := openOnDisk(t, s, root, "main.belt")
	v := s.open[uri]
	off := strings.Index(qualifiedRelationMain, "deck.Cards.count()") + len("deck.Cards.")
	h := hover(v, off)
	if h == nil {
		t.Fatal("an imported master is its relation; count should hover as a relation method")
	}
	if !strings.Contains(h.Contents.Value, "count") {
		t.Errorf("hover should name count: %q", h.Contents.Value)
	}
}

// TestCompletionQueryColumns pins that a query binding's member access — the c in a
// where/order/sum lambda, typed columns<M> — completes the master's columns: each of
// M's row fields, detailed as the column<M, fieldType> it reads as. It works in a scope
// body and in a validate-all query alike, since both bind columns<M>; a bare master
// relation has no such binding, so its members stay the relation methods.
func TestCompletionQueryColumns(t *testing.T) {
	for _, c := range []struct {
		name, src, anchor string
	}{
		{
			"scope body",
			"master Cards {\n  record { id: int, cost: int }\n  scope {\n    pub expensive() -> where(fn(c) -> c.)\n  }\n  primary id\n}\n",
			"c.)",
		},
		{
			"validate all",
			"master Cards {\n  record { id: int, cost: int }\n  primary id\n  validate { all { assert Cards.where(fn(c) -> c.).count() == 0 } }\n  source { csv \"c.csv\" }\n}\n",
			"c.).count",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			doc := testView(c.src)
			off := strings.Index(c.src, c.anchor) + len("c.")
			items := byLabel(completion(doc, off).Items)
			for _, col := range []string{"id", "cost"} {
				it, ok := items[col]
				if !ok {
					t.Errorf("c. should offer the column %q: %v", col, labels(items))
					continue
				}
				if it.Kind == nil || *it.Kind != protocol.CompletionItemKindField {
					t.Errorf("%s kind = %v, want Field", col, it.Kind)
				}
				if !strings.Contains(it.Detail, "column<Cards, int>") {
					t.Errorf("%s detail = %q, want it to name column<Cards, int>", col, it.Detail)
				}
			}
		})
	}
}

// TestCompletionQueryBindingShadowingType pins that a query binding whose name also
// names a type wins the member access: in where(fn(Rarity) -> Rarity.) the parameter
// Rarity (typed columns<Cards>) shadows the enum Rarity in value position, so its member
// access offers the master's columns, not the enum's members — the columns check runs
// before the receiver-as-type reading.
func TestCompletionQueryBindingShadowingType(t *testing.T) {
	src := "enum Rarity { common, legend }\n" +
		"master Cards {\n  record { id: int, cost: int }\n  primary id\n}\n" +
		"fn probe(): nint {\n  return Cards.where(fn(Rarity) -> Rarity.).count()\n}\n"
	doc := testView(src)
	off := strings.Index(src, "Rarity.)") + len("Rarity.")
	items := byLabel(completion(doc, off).Items)
	for _, col := range []string{"id", "cost"} {
		it, ok := items[col]
		if !ok {
			t.Errorf("the binding Rarity shadows the enum; Rarity. should offer the column %q: %v", col, labels(items))
			continue
		}
		if it.Kind == nil || *it.Kind != protocol.CompletionItemKindField {
			t.Errorf("%s kind = %v, want Field", col, it.Kind)
		}
	}
	for _, m := range []string{"common", "legend"} {
		if _, ok := items[m]; ok {
			t.Errorf("the enum is shadowed by the binding; its member %q must not be offered: %v", m, labels(items))
		}
	}
}

// TestCompletionQueryColumnsGenericAliasRow pins that the column completion mirrors the
// checker's column rule, not a broader record expansion: a master whose row is a generic
// record alias (record Box<int>) is a row form the checker does not lift columns from, so
// a query binding over it offers no columns — never one a where/sum query could not use.
func TestCompletionQueryColumnsGenericAliasRow(t *testing.T) {
	src := "type Box<T> = { value: T }\n" +
		"master Cards {\n  record Box<int>\n  primary value\n}\n" +
		"fn probe(): nint {\n  return Cards.where(fn(c) -> c.).count()\n}\n"
	doc := testView(src)
	off := strings.Index(src, "c.)") + len("c.")
	items := byLabel(completion(doc, off).Items)
	if _, ok := items["value"]; ok {
		t.Errorf("a generic record alias row lifts no columns; value must not be offered: %v", labels(items))
	}
}

// TestCompletionUserColumnsNotMistaken pins that the column completion matches the
// prelude columns builtin by identity, not by name: a file that declares its own
// generic type columns<T> and accesses a member of it gets that type's real fields,
// not a master's row columns, even when the argument is a master.
func TestCompletionUserColumnsNotMistaken(t *testing.T) {
	src := "type columns<T> = { mine: T }\n" +
		"master Cards {\n  record { id: int, cost: int }\n  primary id\n}\n" +
		"fn probe(c: columns<Cards>): int {\n  return c.\n}\n"
	doc := testView(src)
	off := strings.Index(src, "c.\n") + len("c.")
	items := byLabel(completion(doc, off).Items)
	if _, ok := items["mine"]; !ok {
		t.Errorf("a user columns<T> should offer its own field mine: %v", labels(items))
	}
	for _, col := range []string{"id", "cost"} {
		if _, ok := items[col]; ok {
			t.Errorf("a user columns<T> must not be mistaken for the query binding (offered master column %q): %v", col, labels(items))
		}
	}
}
