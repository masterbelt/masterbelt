---
name: add-language-feature
description: >-
  Drive a masterbelt language change through the compiler pipeline one layer at
  a time — write a .belt example, then the lexer, parser/CST, parser/AST,
  semantic, and LSP/editor support — pausing for the user to review each step
  before it is committed. Use when adding or changing masterbelt syntax, a
  token, a literal, an operator, a keyword, a type, or any language feature that
  flows through the compiler; or when the user asks to add to / extend the
  language or its grammar.
argument-hint: "[feature, e.g. 'tuple literals' or 'a float primitive']"
---

# Add a masterbelt language feature

masterbelt is a small compiled language whose front end is a layered, incremental
pipeline. A language change flows down the layers in a fixed order, and each
layer is proven by a golden snapshot of a shared `.belt` example. This skill
walks a change through those layers **one commit per layer, with a user review
gate before every commit.**

The order is deliberate — each layer consumes the one above it, so building
bottom-up would mean editing against types that don't exist yet:

1. **example** — the `.belt` that defines the feature (the north star)
2. **lexer** — bytes → tokens
3. **parser / CST** — tokens → lossless concrete tree
4. **parser / AST** — concrete tree → typed syntax
5. **semantic** — resolve, type, evaluate, lower to IR; make it build & analyze
6. **LSP / editor** — surface the feature in the editor

Skip a layer the change genuinely doesn't touch (a pure type-rule change may
start at semantic), but never skip the example or the review gate.

## The review gate (non-negotiable)

This is the point of the skill. For **every** stage:

1. **Implement** the stage's change.
2. **Make it green** — see *Verify* below. Regenerate any affected golden
   snapshots and read the diffs; the snapshot diff is the proof of what the
   layer now does.
3. **Present for review and STOP.** End your turn with:
   - one-paragraph summary of what changed and why,
   - `git diff` of the stage's files (and the snapshot diffs — these are the
     semantic proof),
   - the verify results (tests/vet green),
   - what the next stage will be.
   Then explicitly ask the user to **approve the commit or request changes**.
   Do **not** run more tools or commit until the user responds.
4. **On approval, commit** (one commit, this layer only — see *Commit*). On
   change requests, revise and re-present. Then move to the next stage.

Never commit a stage the user hasn't approved. Never batch two stages into one
commit.

## Stage 0 — Branch & scope

- Restate the change in one or two sentences and list which layers it touches.
- Branch; never work on `main`: `git checkout -b feat/<short-name>`.

## Stage 1 — Example (the north star)

The shared examples live in `pkg/masterbelt/testdata/examples/NNNN-<name>.belt`
(numbered in sequence). Every layer's `example_test.go` runs all of them and
compares its output against a committed snapshot under that layer's
`testdata/examples/`:

| layer | snapshot |
|---|---|
| lexer | `pkg/masterbelt/lexer/testdata/examples/<name>.belt.tokens` |
| parser/concrete | `.../parser/concrete/testdata/examples/<name>.belt.cst` |
| parser/abstract | `.../parser/abstract/testdata/examples/<name>.belt.ast` |
| semantic | `.../semantic/testdata/examples/<name>.belt.ir` |

- Add a minimal, representative `NNNN-<name>.belt` exercising the new feature.
- Regenerate **all** snapshots so the suite stays green and captures current
  (pre-support) behavior: `go test ./... -update`. Early layers will snapshot
  lex/parse errors or an empty IR — that is expected; later stages turn those
  diffs into the real feature.
- Review artifact: the `.belt` file plus the generated snapshots.

## Stage 2 — Lexer

- New tokens: add the `Kind` in `pkg/masterbelt/source/token/token.go` (and its
  name/keyword mapping there), then recognize it in `pkg/masterbelt/lexer/`.
- Run `make generate` if you added a token/keyword — it regenerates the editor
  grammar (and diagnostics); review the generated diff.
- Refresh and review the lexer snapshot: `go test ./pkg/masterbelt/lexer/ -update`.

## Stage 3 — Parser / CST

- New node kinds: add the `Kind` in `pkg/masterbelt/source/cst/cst.go` (and its
  `kindNames` entry).
- Parse it in `pkg/masterbelt/parser/concrete/` — the parser is split by
  concern: `parser_decl.go` (declarations), `parser_type.go` (type expressions),
  `parser_expr.go` (statements/expressions/precedence), `parser.go` (driver).
  Update the grammar comment at the top of `parser.go`.
- Keep it **lossless** (trivia attached as leading children) and **boundary
  context-free** (a File child parses from its tokens alone) — the incremental
  `Document` depends on both; `document_test.go` fuzzes it.
- Refresh/review snapshot: `go test ./pkg/masterbelt/parser/concrete/ -update`.

## Stage 4 — Parser / AST

- New AST nodes: `pkg/masterbelt/source/ast/`. Lower CST → AST in
  `pkg/masterbelt/parser/abstract/`. Operators are desugared to method calls
  here (e.g. `1 + 2` → `1.add(2)`), which keeps the later layers uniform.
- Refresh/review snapshot: `go test ./pkg/masterbelt/parser/abstract/ -update`.

## Stage 5 — Semantic (make it build & analyze)

This is where the program becomes resolved, typed, and evaluated. Touch only the
layers the feature needs; the dependency direction is strictly one-way (see
*Architecture map*).

- **IR data** — `pkg/masterbelt/source/ir/` (`ir.go` value graph, `type.go`
  types, `constant.go` evaluated values). Add `dump.go` rendering for any new
  node so the `.ir` snapshot stays meaningful (Dump is the oracle).
- **Type rules** — `pkg/masterbelt/types/` (pure algebra) and
  `pkg/masterbelt/types/infer/` (AST→type: `Expr`/`Decl`/`Check`/`Body`;
  `TypeResolver` for type expressions). Const and method-body paths share one
  walk — extend the shared logic, not a second copy.
- **Lowering** — `pkg/masterbelt/lower/` (AST→IR graph; context goes through a
  `Binder`).
- **Evaluation** — `pkg/masterbelt/eval/` (AST→`ir.Constant`, constant folding).
- **Primitives/prelude** — to add a builtin type or operator: register it in
  `pkg/masterbelt/builtin/builtin.go` and declare it in
  `pkg/masterbelt/builtin/belt/*.belt`; the prelude test validates the two agree.
- **Diagnostics & assembly** — `pkg/masterbelt/semantic/` (`assemble` emits
  diagnostics; `check.go` runs them; `engine.go`/`document.go` are the
  incremental façade). New diagnostic codes are generated — edit the source of
  `diagnostic_gen.go`'s generator, then `make generate`.
- **Guard rails**: keep `go test ./pkg/masterbelt/semantic/` green —
  `TestExamples` (the `ir.Dump` oracle: incremental == full `Analyze`),
  `TestEarlyCutoff`/`TestEarlyCutoffValue` (an edit must not over-invalidate),
  and `TestDocumentFuzz`. The value query must not depend on the type query.
- Refresh/review snapshot: `go test ./pkg/masterbelt/semantic/ -update`. The
  `.ir` diff is the heart of the review — it shows the resolved, typed program.

## Stage 6 — LSP / editor

- `pkg/masterbelt/lsp/` adapts `semantic.Document` to LSP. Likely touch points:
  `semantic.go` (`classifyToken` — colour the new syntax), `completion.go`
  (offer new names/keywords; `typeContextAt` gates value vs type), `hover.go`,
  and `convert.go` (document symbols). Add a handler + capability in `server.go`
  only for a genuinely new request.
- Editor assets are generated: run `make generate` and review the regenerated
  VSCode TextMate grammar (`toolchain/editors/vscode/...`, via
  `internal/editorgen`). The grammar's keyword table is derived from the token
  table, so a new keyword flows through automatically.
- Test in `pkg/masterbelt/lsp/` mirroring the existing `*_test.go`.

## Verify (run before every review)

```
make fmt        # gofmt; then confirm `gofmt -l <pkgs>` prints nothing
make vet
go test ./...   # must be green
```

When you changed an example or a layer's output, regenerate and **read** the
snapshots first: `go test ./... -update` (or the single package), then re-run
`go test ./...`. When you changed tokens/keywords/diagnostics, run
`make generate` and review the generated diff. Treat a regenerated snapshot as a
change to review, never as something to accept blindly.

## Commit (only after approval)

- One commit per stage, that stage's files only.
- Conventional Commits (enforced by commitlint): type from
  `build|chore|ci|docs|feat|fix|perf|refactor|revert|style|test`, with a scope,
  e.g. `feat(lexer): tokenize the pipe operator`,
  `feat(semantic): type and fold tuple literals`.
- The commit body must end with the required trailer:
  `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`.
- Practical gotcha: avoid backticks in a `-m` string (the shell runs them). Use
  single quotes, or write the message to a file and `git commit -F`.

## Architecture map (where things live)

One-way dependency flow (lower layers never import higher):

```
lexer ────────────────► source/token
parser/concrete ──────► source/cst, source/token
parser/abstract ──────► source/ast, source/cst
semantic ─► types/infer ─► types ─► source/ir ─► source/ast
        ├─► eval  ──────────────────► source/ir
        ├─► lower ──────────────────► source/ir
        └─► builtin (prelude: builtin/belt/*.belt)
lsp ──────────────────► semantic (+ source/*, diagnostic)
```

- `source/token` — token kinds.
- `lexer` — bytes → tokens, incrementally (re-lex only the edited window).
- `source/cst` — lossless concrete tree; `Kind` + `kindNames`.
- `parser/concrete` — tokens → CST; `parser{,_decl,_type,_expr}.go`; incremental
  `Document`.
- `source/ast` — position-independent typed syntax.
- `parser/abstract` — CST → AST; operators desugared to method calls; incremental
  `Document`.
- `source/ir` — resolved, typed IR (`Type`, `Constant`, value graph) + `Dump`
  (the snapshot oracle).
- `types` — pure type algebra (`MethodResult`, `Unify`, `Fits`, `Assignable`).
- `types/infer` — AST→type and type-expression resolution.
- `eval` — constant folding. `lower` — AST→IR graph.
- `builtin` — native primitive registry; `belt/*.belt` is the prelude.
- `semantic` — orchestration: query engine (incremental memoization), `Document`
  (the editor façade), `assemble` (IR + diagnostics). `Analyze` is the reference
  oracle the incremental engine is checked against — keep them sharing `assemble`.
- `lsp` — the language server over `semantic.Document`.
- `toolchain/editors/vscode` + `internal/editorgen` — editor assets via
  `go generate`.
