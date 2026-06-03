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

From this directory (with dependencies installed via `pnpm install` at the repo
root, which includes this workspace package):

```sh
pnpm run build        # bundle src/extension.ts -> dist/extension.js
pnpm run watch        # rebuild on change
pnpm run check-types  # type-check only
```

Then open this folder in VS Code and press <kbd>F5</kbd> ("Run masterbelt
Extension") to launch an Extension Development Host. Open any `.belt` file
(e.g. `pkg/masterbelt/testdata/examples/0001-const.belt`) to see highlighting
and diagnostics; run **Format Document** to format.

Set `"masterbelt.trace.server": "verbose"` to log the LSP traffic in the
"masterbelt" output channel.
