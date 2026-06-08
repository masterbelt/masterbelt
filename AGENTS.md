# AGENTS.md

Guidance for any agent — human or AI — contributing to this repository.

## Commit messages

A commit message is a release note for whoever reads the history later. It records
**what changed and why**, in terms that make sense to someone who never saw the plan
you worked from.

- Conventional-commit subject: `type(scope): summary` — `feat(lsp): …`,
  `fix(semantic): …`, `refactor(ir): …`, `docs: …`, `test: …`, `chore: …`.
- Subject in the imperative, no trailing period, ≤ ~72 chars.
- The body (when one is needed) explains the change and its rationale: the problem,
  the approach, and any consequence a reader should know.
- **Do not** reference internal planning artifacts: no design-doc proposal IDs
  (`A-5`, `F-3`), no section numbers (`§3.5`), no milestone or phase tags
  (`M3`, `P5 gate`), no "per the plan". The history is not a scratchpad.
- Commit in small, self-contained steps; each commit builds and passes its tests.
- AI-assisted commits end with a `Co-Authored-By` trailer. commitlint enforces
  its exact form (the agent-coauthor rule) and reports the value to use, so this
  guide neither spells it out nor risks drifting from it.

## Code comments

A comment explains intent and mechanism to someone reading the code: why this exists,
what invariant it upholds, what pitfall it avoids. It is not a log of where the change
came from.

**Never write, in a comment:**

- design-doc proposal IDs — `A-5`, `B-2`, `D-1`, `E-16`, `F-3`, `P-2`, …
- section references — `§0`, `§3.5`
- milestone / phase tags — `M1`…`M7`, `M-reuse`, `P4 gate`, `P5`
- commit-hash citations — `(b58443a)`

These are provenance notes that mean nothing to a code reader. If the *concept* a tag
points at matters, name the concept in plain words — write "the index-union match
case", not "the E-18 case". If only the provenance matters, drop it.

**Do keep:** the actual explanation, domain vocabulary (oracle, write-back, early
cutoff), and references to real code symbols, files, and external standards (UTF-16,
RFC numbers).

Design-doc IDs and section numbers belong in `tmp/plan/*.md`, never in code or history.
