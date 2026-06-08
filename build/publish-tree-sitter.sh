#!/bin/sh
# publish-tree-sitter.sh assembles the standalone tree-sitter-masterbelt package
# tree from the in-repo grammar (toolchain/grammars/tree-sitter-masterbelt),
# flattening the subdirectory to the package root that the dedicated
# distribution repo — masterbelt/tree-sitter-masterbelt — expects: editors look
# for grammar.js and src/parser.c at the repo root (Zed's grammar rev, Helix's
# [[grammar]] git source, nvim-treesitter's parser registration all assume it).
#
# It is the one assembly path CI and a developer share (like dist.sh): the
# output of `make publish-tree-sitter` is exactly what the publish workflow
# syncs to the dedicated repo. The source stays the single truth (C-2 §8-5); the
# dedicated repo is a derived, read-only mirror, so this only ever COPIES out —
# it never edits the source, and it omits the dev-only files (the CST-pin test
# and its helper reach into the main repo's testdata and cannot run standalone).
#
# It uses GNU coreutils behaviour (the linux build host), like dist.sh.
set -eu

root="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
cd "$root"

src="toolchain/grammars/tree-sitter-masterbelt"
out="${TS_DIST_DIR:-dist/tree-sitter-masterbelt}"

# The published files: the hand-written grammar, the generated lexical layer and
# parser, the highlight queries, and the manifests. Anything not listed here
# (test/, cst-skeleton.mjs, the source .gitignore) is deliberately left behind.
files="grammar.js lexical.js package.json tree-sitter.json"
dirs="src queries"

for f in $files; do
	[ -f "$src/$f" ] || { echo "publish-tree-sitter: missing $src/$f" >&2; exit 1; }
done
for d in $dirs; do
	[ -d "$src/$d" ] || { echo "publish-tree-sitter: missing $src/$d/" >&2; exit 1; }
done
[ -f "$src/src/parser.c" ] || { echo "publish-tree-sitter: $src/src/parser.c not generated — run 'make generate'" >&2; exit 1; }

rm -rf "$out"
mkdir -p "$out"

for f in $files; do
	cp "$src/$f" "$out/$f"
done
for d in $dirs; do
	cp -R "$src/$d" "$out/$d"
done

# Stamp the published version to masterbelt's own — the grammar is generated in
# lockstep with the language, so it ships under the same version, not a separate
# hand-set one — and make the package.json publish-ready (the committed source
# stays a private 0.1.0 placeholder, like the vscode package). The real,
# commit-derived version is read from the CLI here (the single source,
# internal/version) so a developer's `make publish-tree-sitter` and CI produce
# the identical stamp. Overridable via TS_VERSION (e.g. to stub it in a test).
version="${TS_VERSION:-$(go run -buildvcs=true ./cmd/masterbelt --version | awk '{print $3}')}"
case "$version" in
	''|*[!0-9A-Za-z.+-]*)
		echo "publish-tree-sitter: failed to extract a valid version (got: '$version') from ./cmd/masterbelt --version; set TS_VERSION explicitly or update parser" >&2
		exit 1
		;;
esac
node - "$out" "$version" <<'NODE'
const fs = require('fs');
const [dir, version] = process.argv.slice(2);
for (const [file, set] of [
  ['package.json', (j) => {
    j.version = version;
    // Publish-ready: drop the workspace `private` guard and point npm at GitHub
    // Packages (the @masterbelt scope's registry). The wasm — built into this
    // tree before publish — is the web-tree-sitter consumption path; the source
    // and committed parser ship alongside it.
    delete j.private;
    j.publishConfig = { registry: 'https://npm.pkg.github.com' };
    j.keywords = ['tree-sitter', 'masterbelt', 'parser', 'grammar'];
    j.files = ['grammar.js', 'lexical.js', 'tree-sitter.json', 'src/', 'queries/', '*.wasm'];
  }],
  ['tree-sitter.json', (j) => {
    if (!j.metadata || typeof j.metadata !== 'object') j.metadata = {};
    j.metadata.version = version;
  }],
]) {
  const path = dir + '/' + file;
  const json = JSON.parse(fs.readFileSync(path, 'utf8'));
  set(json);
  fs.writeFileSync(path, JSON.stringify(json, null, 2) + '\n');
}
NODE

# The repo's single MIT license, copied in (not a second committed copy that
# could drift) — the same approach the vsix package and dist.sh take.
cp LICENSE "$out/LICENSE"

# A .gitignore for the dedicated repo: it carries no node_modules (consumers
# build src/parser.c directly; no tree-sitter CLI needed — C-2 §5).
printf 'node_modules/\n' >"$out/.gitignore"

# A consumer-facing README that makes clear this is a generated mirror, records
# the source commit it was cut from, and shows how each editor pins it.
rev="$(git rev-parse HEAD)"
cat >"$out/README.md" <<EOF
# tree-sitter-masterbelt

The [tree-sitter](https://tree-sitter.github.io/) grammar for the
[masterbelt](https://github.com/masterbelt/masterbelt) language.

> **This repository is a generated mirror — do not edit it here.** The source is
> \`toolchain/grammars/tree-sitter-masterbelt\` in the masterbelt monorepo; this
> tree is assembled and published from there (\`build/publish-tree-sitter.sh\`).
> Version \`$version\`, cut from masterbelt commit \`$rev\` — the grammar ships
> under the same version as the language it tracks.

The committed \`src/parser.c\` means consumers need no tree-sitter CLI — only a C
compiler, which the editors invoke for you.

## Using it

Pin a tag or commit (never a moving branch — a moving reference breaks
reproducibility):

- **Neovim** (nvim-treesitter): register \`masterbelt\` pointing at this repo's
  URL and a fixed revision, then \`:TSInstall masterbelt\`.
- **Helix** (\`languages.toml\`): a \`[[grammar]]\` with \`source.git\` = this repo and
  \`source.rev\` = a fixed revision, plus the matching \`[[language]]\`.
- **Zed**: a language extension referencing this repo's grammar at a fixed rev.
- **GitHub**: registered through Linguist alongside the \`.belt\` file type.

The highlight queries live in \`queries/highlights.scm\` (nvim-treesitter capture
names, which GitHub also reads); \`queries/helix\` and \`queries/zed\` hold the
variants whose capture vocabulary differs.
EOF

echo "assembled tree-sitter-masterbelt $version -> $out (from $rev)"
