# masterbelt for VS Code

Language support for the masterbelt language:

- **Syntax highlighting** — a TextMate grammar (`syntaxes/masterbelt.tmLanguage.json`); no server needed.
- **Diagnostics, document symbols, and formatting** — served by the masterbelt
  language server (`masterbelt lsp`) over stdio, via `vscode-languageclient`.

## Requirements

The `masterbelt` binary must be on your `PATH` (build it with `make build` at the
repository root, which produces `bin/masterbelt`), or point the extension at it:

```jsonc
// settings.json
"masterbelt.server.path": "/absolute/path/to/bin/masterbelt"
```

## Develop

Install dependencies once with `pnpm install` at the repo root (this is a
workspace package). Then, from this directory:

```sh
pnpm run build        # bundle src/extension.ts -> dist/extension.js
pnpm run watch        # rebuild on change
pnpm run check-types  # type-check only
pnpm test             # tokenize a sample with the generated grammar
```

Open the **repository root** in VS Code and press <kbd>F5</kbd> ("Run masterbelt
Extension"). The launch config (the repo-root `.vscode/launch.json`):

- builds both the language server (`make build` -> `bin/masterbelt`) and the
  extension bundle first (the repo-root `.vscode/tasks.json`);
- opens `pkg/belt/testdata/examples/` (and `0001-const.belt`) in the
  development host, so you immediately see highlighting and diagnostics — run
  **Format Document** to format;
- points the extension at the freshly built `bin/masterbelt` via the
  `MASTERBELT_SERVER_PATH` environment variable, so it works regardless of which
  folder the development host opened.

After changing the **server** (Go), rerun the build and reload the development
host ("Developer: Reload Window") so it respawns `masterbelt lsp`; the extension
does not rebuild Go. Changes to the **extension** (TypeScript) only need
`pnpm run watch` plus a reload.

Set `"masterbelt.trace.server": "verbose"` to log the LSP traffic in the
"masterbelt" output channel.

### Developing in WSL

Everything runs inside WSL: the extension is a workspace extension, so the
development host and the `masterbelt lsp` process both run on the WSL side. Open
the repository from WSL (`code .` in the WSL shell, or "Reopen in WSL"), and
build the **Linux** binary there (`make build`). The F5 flow above needs no
Windows-side setup.

For a non-development install, build `bin/masterbelt`, then either put it on
`PATH` or set `"masterbelt.server.path"` to its absolute path.
