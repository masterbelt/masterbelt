# tree-sitter-masterbelt

The [tree-sitter](https://tree-sitter.github.io/) grammar for the masterbelt
language. It opens the editors that require tree-sitter — Zed, Neovim, Helix,
Emacs (treesit), and GitHub's web highlighting — for which a TextMate grammar
does not suffice, and it backs the language bindings published to npm, Go,
crates.io, PyPI, and Swift Package Manager.

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
  parser by the CST snapshot test, not by being generated.
- **`src/` is generated** by `tree-sitter generate` from `grammar.js`. The
  generated `src/parser.c` is committed so consumers (editors, bindings) need no
  tree-sitter CLI — only this repo's developers do.

## Regenerating

From the repo root:

```sh
make generate          # runs editorgen (lexical.js) then tree-sitter generate (src/)
make verify-generated  # fails if the committed output is stale
```

`tree-sitter generate` needs the tree-sitter CLI; it is pinned as this
package's `devDependency` and installed by `pnpm install` at the workspace root
(this package is a member of the pnpm workspace).

## Distribution

The grammar is published from a **dedicated mirror repository**,
`masterbelt/tree-sitter-masterbelt`. The mirror is a *generated* tree — it is
assembled by `build/publish-tree-sitter.sh` and synced by the nightly; do not
edit it there. The tree-sitter ecosystem expects `grammar.js`, `src/parser.c`,
and each binding's manifest at a repository **root** (this package is a
subdirectory in the monorepo), so the mirror is that flattened root.

### Bindings are materialised at publish, not committed here

The per-language bindings are pure boilerplate keyed off `tree-sitter.json` (the
grammar name and the repository link) — there is nothing to review in them that
the grammar name does not determine. So they are **not** committed in this
source tree; `build/publish-tree-sitter.sh` runs `tree-sitter init` to generate
`go.mod`, `Cargo.toml`, `pyproject.toml`, `Package.swift`, and `bindings/` into
the assembled tree only, which keeps the monorepo free of a nested Go module and
the other manifests. The single thing the source carries is the `bindings` flags
in `tree-sitter.json` (go/python/rust/swift/c on, node off).

### Channels

| Channel | Consumed as | Builds `parser.c` |
| --- | --- | --- |
| **Editors** | the mirror git repo, pinned at a tag/rev | the committed `src/parser.c` |
| **Go** | `go get github.com/masterbelt/tree-sitter-masterbelt` (`bindings/go`) | cgo, at build |
| **Swift** | a SwiftPM package at the mirror repo + tag | SwiftPM, at build |
| **Rust** | `tree-sitter-masterbelt` on crates.io | the `cc` crate, from source |
| **Python** | `tree-sitter-masterbelt` on PyPI (prebuilt wheels) | cibuildwheel / sdist |
| **JavaScript** | `@masterbelt/tree-sitter-masterbelt` on npm and GitHub Packages | the WebAssembly build |

Go and Swift are **git-native** — they resolve straight from the mirror repo and
a tag, so the sync that pushes the assembled tree and the tag is all they need.
The npm package ships the WebAssembly module (for `web-tree-sitter`) plus
`grammar.js` and the committed parser source; **no native node addon is shipped**
(there is no node-gyp step). A consumer who wants a native build has `src/` to
build from.

### Versioning

The distribution version is masterbelt's own, in **clean SemVer** (the CLI's
`+sha` build metadata is dropped, because Go module versions, PyPI, and crates.io
all reject it):

- **nightly** → `<line>.<date>-nightly.<sha>` (e.g. `0.1.20260611-nightly.abc1234`)
  — a prerelease, so it sorts below the eventual release and is excluded from
  `^`-ranges and `@latest`; the sha rides in the prerelease identifier (not as
  `+` metadata), keeping each nightly unique and traceable.
- **stable** → `<line>.<date>` or `1.0.0` — the clean release.

The mirror is tagged `v<version>`; that tag is what every channel pins.

### How publishing works

The monorepo's nightly only **assembles, syncs, and tags** — it carries no
registry secret and needs no Docker. The mirror then publishes itself: the
assembled tree includes the mirror's own `.github/workflows`, and the `v<version>`
tag the nightly pushes triggers them:

- `release-npm` — on **every** tag: builds the wasm and publishes to npm and
  GitHub Packages. A nightly (prerelease) tag goes to the `nightly` dist-tag, a
  stable tag to `latest`.
- `release-crates` — on **stable** tags only (crates.io versions are immutable;
  nightlies would pile up permanently): `cargo publish`.
- `release-pypi` — on **stable** tags only: cibuildwheel wheels + an sdist,
  published with Trusted Publishing.

So Go, Swift, and the editors track the nightly off the git mirror; npm gets a
nightly prerelease and the stable release; crates.io and PyPI get stable releases
only.

## Bring-up (one-time, human)

1. Create the empty `masterbelt/tree-sitter-masterbelt` repository.
2. Authorize the sync with a **GitHub App** (the org disallows deploy keys, and
   an App is the stronger choice: the nightly mints a short-lived, repo-scoped
   installation token at run time; nothing long-lived travels):
   - Install the masterbelt GitHub App on `tree-sitter-masterbelt` with
     **Contents: write** and **Workflows: write** (Workflows because the synced
     tree carries the mirror's own `.github/workflows`).
   - On **this** repo, add the App's secrets: `TREE_SITTER_APP_CLIENT_ID` (the
     App's Client ID) and `TREE_SITTER_APP_PRIVATE_KEY` (a generated private key,
     the full PEM). Without them the nightly's `tree-sitter` job still runs but
     only uploads the assembled tree as an artifact — it never fails.
3. Register the package names and configure **Trusted Publishing (OIDC) on each
   registry** — no registry token is ever created or stored; each registry mints
   a short-lived credential for the matching workflow at publish time:
   - npm: claim the `@masterbelt` scope on npmjs and add the mirror's
     `release-npm` workflow as a trusted publisher. (GitHub Packages needs no
     setup — the workflow's built-in token has `packages: write`.)
   - crates.io: claim `tree-sitter-masterbelt` and add `release-crates` as a
     trusted publisher.
   - PyPI: create the project and add `release-pypi` (with its `pypi`
     environment) as a trusted publisher.

   The very first publish of a brand-new package name may need a one-time manual
   bootstrap where a registry has no pending-publisher flow — done locally by the
   human, nothing stored in the repo.
4. Trigger the **nightly** workflow via *Run workflow* (or wait for the schedule)
   to do the first sync, then verify the mirror and the release runs it triggers.
5. Add the `tree-sitter` check to this repo's branch protection so the grammar
   gate is required, alongside `go` and `vscode`.

> The only long-lived secret in the whole pipeline is the GitHub App private key
> (on this repo). It is a GitHub-only signing key: it mints short-lived tokens
> scoped to the mirror repo's Contents and Workflows and can publish to no package
> registry — so even a leak cannot push a package anywhere. Every registry
> publish authenticates with OIDC, storing nothing.

## Using the WebAssembly build

```js
import { Parser, Language } from 'web-tree-sitter';
await Parser.init();
const lang = await Language.load(
  require.resolve('@masterbelt/tree-sitter-masterbelt/tree-sitter-masterbelt.wasm'),
);
const parser = new Parser();
parser.setLanguage(lang);
```

Installing the npm package from GitHub Packages needs an `.npmrc` pointing the
`@masterbelt` scope at `https://npm.pkg.github.com` with a token (GitHub Packages
requires authentication to install). The same package is on public npm, which
does not.
