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

The grammar ships on the **nightly**, under the same commit-derived version as
the CLI, to two channels:

- **A dedicated git mirror**, `masterbelt/tree-sitter-masterbelt` — editors pin
  it, because the tree-sitter ecosystem expects `grammar.js` and `src/parser.c`
  at a repository root (this package is a subdirectory). Generated, never
  hand-edited.
- **GitHub Packages** (`@masterbelt/tree-sitter-masterbelt`) — the npm package
  with a prebuilt **WebAssembly** module, for `web-tree-sitter` consumers (no
  native build).

How it is produced:

- `make publish-tree-sitter` assembles the standalone package tree (this
  directory flattened to a root, the dev-only test harness dropped, the MIT
  license and a consumer README added, the package.json made publish-ready)
  into `dist/tree-sitter-masterbelt`, stamped with masterbelt's own
  commit-derived version. `make tree-sitter-wasm` then builds the wasm into it
  (needs Docker).
- The **nightly** `tree-sitter` job verifies, runs the grammar tests,
  assembles, and: (1) when the App is configured, syncs the tree to the git
  mirror — rolling default branch plus an immutable `v<version>` tag (the
  version carries the commit SHA, so it never moves) for editors to pin;
  (2) builds the wasm and publishes the npm package to GitHub Packages with the
  built-in token. The mirror is synced before the wasm is built, so the editor
  channel never depends on Docker or npm.

**Bring-up (one-time, human):**

1. Create the empty `masterbelt/tree-sitter-masterbelt` repository.
2. Authorize the push with a **GitHub App** — the org disallows deploy keys and
   recommends an App, which is the stronger choice anyway (the workflow mints a
   short-lived, repo-scoped installation token at run time; nothing long-lived
   travels):
   - Install the masterbelt GitHub App on `tree-sitter-masterbelt` with
     **Contents: write** permission.
   - On this repo, add two secrets from the App: `TREE_SITTER_APP_CLIENT_ID`
     (the App's **Client ID**) and `TREE_SITTER_APP_PRIVATE_KEY` (a generated
     private key, the full PEM).

   Each run mints a token scoped to that one repo and to Contents alone
   (`permission-contents: write`), expiring in ~1h and revoked at job end —
   even the App's own broader permissions do not travel. Without the secrets
   the nightly's `tree-sitter` job still runs but only uploads the assembled
   tree as an artifact — it never fails.
3. Trigger the **nightly** workflow via *Run workflow* (or wait for the
   schedule) to do the first sync, and verify the mirror. The grammar then
   re-syncs on every nightly that changes it.
4. Add the `tree-sitter` check to this repo's branch protection so the grammar
   gate is required, alongside `go` and `vscode`.

The GitHub Packages publish needs no extra secret — it uses the workflow's
built-in token (the job has `packages: write`) and runs only on the canonical
repo. It is independent of the App, so it works even before the mirror is set
up.

The npm package on GitHub Packages carries the wasm for `web-tree-sitter`
(verified against `web-tree-sitter` at the CLI's own version). Load it with the
module path:

```js
import { Parser, Language } from 'web-tree-sitter';
await Parser.init();
const lang = await Language.load(
  require.resolve('@masterbelt/tree-sitter-masterbelt/tree-sitter-masterbelt.wasm'),
);
const parser = new Parser();
parser.setLanguage(lang);
```

Installing from GitHub Packages needs an `.npmrc` pointing the `@masterbelt`
scope at `https://npm.pkg.github.com` with a token (GitHub Packages requires
authentication to install, even for public packages).

Native node/rust bindings are not shipped (the committed `src/parser.c` lets a
consumer build them); web-tree-sitter is the supported JS path.
