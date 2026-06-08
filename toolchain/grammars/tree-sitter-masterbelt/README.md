# tree-sitter-masterbelt

The [tree-sitter](https://tree-sitter.github.io/) grammar for the masterbelt
language. It opens the editors that require tree-sitter — Zed, Neovim, Helix,
Emacs (treesit), and GitHub's web highlighting — for which a TextMate grammar
does not suffice.

## How it is built (single source, no second lexer)

masterbelt does not hand-maintain a second parser. The grammar is split so the
lexer truth has exactly one home:

- **`lexical.js` is generated** by `toolchain/internal/editorgen` from
  `pkg/source/token` — the same source the VS Code TextMate grammar is
  generated from. It carries the keyword table, operator spellings, comment
  markers, and literal regexes. **Do not edit it.**
- **`grammar.js` is hand-written.** It `require`s `lexical.js` and builds the
  structural rules (declarations, expressions, statements) on top. The
  structural layer is tree-sitter's own; it is kept honest against the real
  parser by the CST snapshot test (C-2 plan §3), not by being generated.
- **`src/` is generated** by `tree-sitter generate` from `grammar.js`. The
  generated `src/parser.c` is committed so consumers (editors) need no
  tree-sitter CLI — only this repo's developers do.

## Regenerating

From the repo root:

```sh
make generate          # runs editorgen (lexical.js) then tree-sitter generate (src/)
make verify-generated  # fails if the committed output is stale
```

`tree-sitter generate` needs the tree-sitter CLI; it is pinned as this
package's `devDependency` and installed by `pnpm install` at the workspace root
(this package is a member of the pnpm workspace). The C-2 plan's §5 records this
as the one new development dependency.

## Distribution (C-3)

The source lives here; editors consume it from a **dedicated mirror repo**,
`masterbelt/tree-sitter-masterbelt`, because the tree-sitter ecosystem expects
`grammar.js` and `src/parser.c` at a repository root (this package is a
subdirectory). The mirror is generated, never hand-edited.

- `make publish-tree-sitter` assembles the standalone package tree (this
  directory flattened to a root, the dev-only test harness dropped, the MIT
  license and a consumer README added) into `dist/tree-sitter-masterbelt`.
- The `tree-sitter` workflow's `publish` job (a manual dispatch) regenerates,
  verifies, assembles, and — when a token is configured — syncs the tree to the
  mirror, pushing its rolling default branch and an immutable `v<version>-<sha>`
  tag for editors to pin (never a moving reference).

**Bring-up (one-time, human):**

1. Create the empty `masterbelt/tree-sitter-masterbelt` repository.
2. Authorize the push with an **SSH deploy key scoped to that repo alone** — not
   an account-wide token:
   - `ssh-keygen -t ed25519 -C tree-sitter-masterbelt-deploy -f ts_deploy -N ''`
   - On the mirror repo: *Settings → Deploy keys → Add* `ts_deploy.pub`, **Allow
     write access** checked.
   - On this repo: add the private half (`ts_deploy`) as the secret
     `TREE_SITTER_DEPLOY_KEY`. (Delete the local key files afterward.)

   A deploy key only grants write to that one mirror, so a leak's blast radius
   is the generated mirror (regenerated from source); revoke by removing the
   deploy key. Without the secret the publish job still runs but only uploads
   the assembled tree as an artifact — it never fails.
3. Run the `tree-sitter` workflow via *Run workflow* (workflow_dispatch) to do
   the first sync, and verify the mirror.
4. Add the `tree-sitter` check to this repo's branch protection so the grammar
   gate is required, alongside `go` and `vscode`.

The npm package is named `@masterbelt/tree-sitter-masterbelt` so it can later be
published to GitHub Packages (and public npm); that, with the node/rust
bindings, is C-3 M5.3 — optional and after the editor mirror, which needs only
the committed `src/parser.c`.
