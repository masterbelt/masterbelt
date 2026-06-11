#!/bin/sh
# publish-tree-sitter.sh assembles the standalone tree-sitter-masterbelt package
# tree from the in-repo grammar (toolchain/grammars/tree-sitter-masterbelt),
# flattening the subdirectory to the package root that the dedicated
# distribution repo — masterbelt/tree-sitter-masterbelt — expects: editors look
# for grammar.js and src/parser.c at the repo root (Zed's grammar rev, Helix's
# [[grammar]] git source, nvim-treesitter's parser registration all assume it),
# and the language bindings (Go/Swift consume the repo directly, Rust/Python/npm
# publish from it) expect their manifests there too.
#
# It is the one assembly path CI and a developer share (like dist.sh): the
# output of `make publish-tree-sitter` is exactly what the publish workflow
# syncs to the dedicated repo. The source stays the single truth; the dedicated
# repo is a derived, read-only mirror, so this only ever COPIES out —
# it never edits the source, and it omits the dev-only files (the CST-pin test
# and its helper reach into the main repo's testdata and cannot run standalone).
#
# The language bindings are NOT committed in the source — they are pure
# boilerplate keyed off tree-sitter.json (the grammar name and repository), so
# they are materialised here by `tree-sitter init` into the assembled tree only,
# keeping the monorepo free of a nested go.mod / Cargo.toml / pyproject.toml. The
# mirror is where Go and Swift resolve them (`go get …/tree-sitter-masterbelt`,
# SwiftPM at a tag), and the sync commits and tags the assembled tree there, so
# the git-native bindings land in git exactly where their consumers look.
#
# It uses GNU coreutils behaviour (the linux build host), like dist.sh.
set -eu

root="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
cd "$root"

src="toolchain/grammars/tree-sitter-masterbelt"
out="${TS_DIST_DIR:-dist/tree-sitter-masterbelt}"

# The dedicated mirror's URL. Baked into the assembled tree-sitter.json so
# `tree-sitter init` derives every binding manifest's identity from it — the Go
# module path, the Cargo `repository`, the Python `Homepage` — pointing at where
# each package actually resolves (the mirror), not at the monorepo subdirectory.
mirror_url="https://github.com/masterbelt/tree-sitter-masterbelt"

# The tree-sitter CLI, pinned as the grammar package's devDependency. It scaffolds
# the bindings (`init`); the same binary builds the wasm in `make tree-sitter-wasm`.
ts_bin="$root/$src/node_modules/.bin/tree-sitter"
[ -x "$ts_bin" ] || { echo "publish-tree-sitter: $ts_bin missing — run 'pnpm install'" >&2; exit 1; }

# The published source files: the hand-written grammar, the generated lexical
# layer and parser, the highlight queries, the manifests, and the mirror's own
# release workflows (.github — the mirror self-publishes to npm/crates.io/PyPI on
# the tags this sync pushes). Anything not listed here (test/, cst-skeleton.mjs,
# the source .gitignore) is deliberately left behind; the bindings are added by
# `tree-sitter init` below, not copied.
files="grammar.js lexical.js package.json tree-sitter.json"
dirs="src queries .github"

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

# The repo's single MIT license, copied in before init (Cargo's `include` lists
# it) — not a second committed copy that could drift, the same approach the vsix
# package and dist.sh take.
cp LICENSE "$out/LICENSE"

# Resolve the distribution version. The grammar ships in lockstep with the
# language, so it carries masterbelt's own commit-derived version — read from the
# CLI here (the single source, internal/version) so a developer's
# `make publish-tree-sitter` and CI produce the identical stamp. TS_VERSION is an
# explicit override (e.g. to stub it in a test) and is used verbatim, bypassing
# the transform below.
#
# The CLI version is `<line>.<patch>+<sha>` (e.g. 0.1.20260611+abc1234). That
# `+sha` build metadata is fine for an editor pin but is rejected by Go module
# versions, PyPI (PEP 440 local labels), and crates.io, so the distribution
# version drops it: a nightly (8-digit-date patch — see version.channelFor)
# becomes a prerelease `<line>.<patch>-nightly.<sha>` that sorts below the
# eventual release and keeps the sha for traceability without using `+`; a stable
# version is the clean `<line>.<patch>` with the metadata stripped.
if [ -n "${TS_VERSION:-}" ]; then
	version="$TS_VERSION"
else
	raw="$(go run -buildvcs=true ./cmd/masterbelt --version | awk '{print $3}')"
	version="$(node - "$raw" <<'NODE'
const raw = process.argv[2] || '';
const [base, meta] = raw.split('+');
const patch = (base.split('-')[0].split('.')[2] || '');
const nightly = /^[0-9]{8}$/.test(patch); // an 8-digit date patch is a nightly
let v = base; // stable / dev: the clean version, metadata dropped
if (nightly) v = meta ? `${base}-nightly.${meta}` : `${base}-nightly`;
process.stdout.write(v);
NODE
)"
fi
case "$version" in
	# Accept a SemVer-like token for package stamping: non-empty and only
	# [0-9A-Za-z.-] characters. Build metadata ('+') is intentionally excluded —
	# the transform above strips it, and it is invalid for the registry targets.
	''|*[!0-9A-Za-z.-]*)
		echo "publish-tree-sitter: invalid version '$version'; expected non-empty [0-9A-Za-z.-] (clean SemVer, e.g. 0.1.20260611-nightly.abc1234 or 1.2.3). Set TS_VERSION explicitly or update parser." >&2
		exit 1
		;;
esac

# Patch the assembled tree-sitter.json BEFORE init: point its repository link at
# the mirror (so the generated binding manifests carry the mirror's identity) and
# stamp the version. The committed source stays a private 0.1.0 placeholder, like
# the vscode package; the publish-ready values live only in the assembled tree.
node - "$out" "$version" "$mirror_url" <<'NODE'
const fs = require('fs');
const [dir, version, mirror] = process.argv.slice(2);
const path = dir + '/tree-sitter.json';
const j = JSON.parse(fs.readFileSync(path, 'utf8'));
if (!j.metadata || typeof j.metadata !== 'object') j.metadata = {};
if (!j.metadata.links || typeof j.metadata.links !== 'object') j.metadata.links = {};
j.metadata.version = version;
j.metadata.links.repository = mirror;
fs.writeFileSync(path, JSON.stringify(j, null, 2) + '\n');
NODE

# Materialise the language bindings into the assembled tree. `init` reads the
# patched tree-sitter.json, so go.mod's module path, Cargo's `repository`, and
# pyproject's `Homepage` all come out as the mirror — no post-init URL patching.
# It scaffolds for the bindings enabled in tree-sitter.json (node/go/python/rust/
# swift/c).
( cd "$out" && "$ts_bin" init >/dev/null )

# init leaves build scaffolding a published library tree should not carry.
rm -rf "$out/target" "$out/Cargo.lock" "$out/node_modules"

# The Node binding's loader. init (cli 0.26.9) writes an ESM bindings/node/index.js
# (import / top-level await), which would force "type": "module" on the package
# and break the CommonJS grammar.js / lexical.js. The grammar stays CommonJS and
# untouched, so replace the loader with a CommonJS one that loads the prebuilt
# addon via node-gyp-build (no node-gyp on the consumer when a prebuild matches),
# and drop init's ESM dev test (not published).
rm -f "$out/bindings/node/binding_test.js"
cat >"$out/bindings/node/index.js" <<'JS'
const { join } = require("node:path");
const { readFileSync } = require("node:fs");

const root = join(__dirname, "..", "..");
const binding = require("node-gyp-build")(root);

try {
  binding.nodeTypeInfo = require(join(root, "src", "node-types.json"));
} catch (_) {
  // node-types.json is optional metadata; ignore when absent.
}

Object.defineProperty(binding, "HIGHLIGHTS_QUERY", {
  configurable: true,
  enumerable: true,
  get() {
    delete binding.HIGHLIGHTS_QUERY;
    try {
      binding.HIGHLIGHTS_QUERY = readFileSync(join(root, "queries", "highlights.scm"), "utf8");
    } catch (_) {
      // queries are optional; leave undefined when absent.
    }
    return binding.HIGHLIGHTS_QUERY;
  },
});

module.exports = binding;
JS

# Make package.json publish-ready, shaped like an npm-published tree-sitter
# grammar (the tree-sitter-ruby model): the Node native binding is the primary
# entry (a prebuilt addon loaded by node-gyp-build — the mirror's release-npm
# workflow builds the prebuilds), with the wasm shipped alongside for
# web-tree-sitter and advertised through `exports`. init stamps the version into
# the Rust and Python manifests itself (it reads tree-sitter.json's
# metadata.version, patched above), so they need no touching here — we only assert
# it took. The publish registry is chosen per-publish by the mirror workflow (npm
# + GitHub Packages), so no publishConfig is pinned here.
node - "$out" "$version" "$mirror_url" <<'NODE'
const fs = require('fs');
const [dir, version, mirror] = process.argv.slice(2);

// init derives these from tree-sitter.json's metadata.version; assert, so a
// future CLI change that drops that linkage fails loudly instead of shipping a
// 0.1.0 placeholder to crates.io / PyPI.
for (const f of ['Cargo.toml', 'pyproject.toml']) {
  const t = fs.readFileSync(dir + '/' + f, 'utf8');
  if (!t.includes(`version = "${version}"`)) {
    throw new Error(`${f}: tree-sitter init did not stamp version ${version} (got: ${(t.match(/^version = .*/m) || ['?'])[0]})`);
  }
}

const pkgPath = dir + '/package.json';
const pkg = JSON.parse(fs.readFileSync(pkgPath, 'utf8'));
pkg.version = version;
delete pkg.private;
delete pkg.publishConfig;
pkg.description = 'masterbelt grammar for tree-sitter';
// Object form with a git+…git URL — GitHub Packages reads repository.url to
// connect the published package to its repository; a bare string is not picked up.
pkg.repository = { type: 'git', url: `git+${mirror}.git` };
pkg.keywords = ['incremental', 'parsing', 'tree-sitter', 'masterbelt', 'parser', 'grammar'];

// The Node native binding is the package entry; node-gyp-build loads the prebuilt
// addon for the host platform (built by the release-npm prebuild matrix), falling
// back to a source build only when no prebuild matches.
pkg.main = 'bindings/node';
pkg.types = 'bindings/node';
pkg.dependencies = { 'node-addon-api': '^8.2.2', 'node-gyp-build': '^4.8.2' };

// The tree-sitter runtime peer must be able to load this parser's ABI. Runtimes
// are not forward-compatible — a parser generated for a newer ABI than the
// runtime supports fails at setLanguage — so the peer floor is tied to the
// committed parser's LANGUAGE_VERSION. The grammar is generated at ABI 14
// (`tree-sitter generate --abi 14`) because that is the newest ABI the whole
// runtime matrix supports — notably go-tree-sitter, whose latest release tops out
// at ABI 14. Every published Node runtime from tree-sitter@0.21.1 up loads ABI 14.
// Assert the ABI matches, so a change to the generated parser fails the assembly
// here instead of shipping a peer range that cannot load it.
const EXPECTED_ABI = 14; // every tree-sitter Node runtime >= 0.21.1 loads ABI 14
const abiMatch = fs.readFileSync(dir + '/src/parser.c', 'utf8').match(/#define\s+LANGUAGE_VERSION\s+(\d+)/);
const abi = abiMatch ? Number(abiMatch[1]) : NaN;
if (abi !== EXPECTED_ABI) {
  throw new Error(`src/parser.c ABI ${abi} != ${EXPECTED_ABI}: align the tree-sitter peer range (and the generate --abi flag) with the parser ABI and bump EXPECTED_ABI`);
}
pkg.peerDependencies = { 'tree-sitter': '>=0.21.1' };
pkg.peerDependenciesMeta = { 'tree-sitter': { optional: true } };
pkg.devDependencies = Object.assign({}, pkg.devDependencies, { prebuildify: '^6.0.1' });
pkg.scripts = Object.assign({}, pkg.scripts, { install: 'node-gyp-build' });
pkg.files = [
  'grammar.js', 'lexical.js', 'tree-sitter.json',
  'binding.gyp', 'bindings/node/*', 'prebuilds/**',
  'queries/*', 'src/**', '*.wasm',
];
// Advertise both entries explicitly — the native binding (`.`) and the wasm —
// while keeping the other shipped files resolvable (exports otherwise hides them).
pkg.exports = {
  '.': { types: './bindings/node/index.d.ts', default: './bindings/node/index.js' },
  './tree-sitter-masterbelt.wasm': './tree-sitter-masterbelt.wasm',
  './grammar.js': './grammar.js',
  './tree-sitter.json': './tree-sitter.json',
  './queries/*': './queries/*',
  './src/*': './src/*',
  './package.json': './package.json',
};
fs.writeFileSync(pkgPath, JSON.stringify(pkg, null, 2) + '\n');
NODE

# A .gitignore for the mirror: it carries no build outputs (consumers build
# src/parser.c themselves — Go via cgo, Rust via cc, Python as a C extension).
cat >"$out/.gitignore" <<'EOF'
node_modules/
target/
Cargo.lock
__pycache__/
*.egg-info/
/build/
/dist/
*.wasm
EOF

# A consumer-facing README that makes clear this is a generated mirror, records
# the source commit it was cut from, and shows how each channel consumes it.
rev="$(git rev-parse HEAD)"
cat >"$out/README.md" <<EOF
# tree-sitter-masterbelt

The [tree-sitter](https://tree-sitter.github.io/) grammar for the [masterbelt](https://github.com/masterbelt/masterbelt) language.

> **This repository is a generated mirror — do not edit it here.** The source is \`toolchain/grammars/tree-sitter-masterbelt\` in the masterbelt monorepo; this tree is assembled and published from there (\`build/publish-tree-sitter.sh\`). Version \`$version\`, cut from masterbelt commit \`$rev\` — the grammar ships under the same version as the language it tracks.

The committed \`src/parser.c\` means consumers need no tree-sitter CLI — only a C compiler, which the editors and bindings invoke for you.

## Editors

Pin a tag or commit (never a moving branch — a moving reference breaks reproducibility):

- **Neovim** (nvim-treesitter): register \`masterbelt\` pointing at this repo's URL and a fixed revision, then \`:TSInstall masterbelt\`.
- **Helix** (\`languages.toml\`): a \`[[grammar]]\` with \`source.git\` = this repo and \`source.rev\` = a fixed revision, plus the matching \`[[language]]\`.
- **Zed**: a language extension referencing this repo's grammar at a fixed rev.
- **GitHub**: registered through Linguist alongside the \`.belt\` file type.

The highlight queries live in \`queries/highlights.scm\` (nvim-treesitter capture names, which GitHub also reads); \`queries/helix\` and \`queries/zed\` hold the variants whose capture vocabulary differs.

## Language bindings

- **Go** — \`go get github.com/masterbelt/tree-sitter-masterbelt@v$version\` (\`bindings/go\`, cgo over \`src/parser.c\`).
- **Rust** — \`tree-sitter-masterbelt\` on crates.io (\`bindings/rust\`).
- **Python** — \`tree-sitter-masterbelt\` on PyPI (\`bindings/python\`).
- **Swift** — a SwiftPM package at this repo + tag (\`Package.swift\`).
- **JavaScript** — \`@masterbelt/tree-sitter-masterbelt\` (npm + GitHub Packages): a prebuilt native addon for Node (used with the \`tree-sitter\` runtime) plus the WebAssembly build for \`web-tree-sitter\`.
EOF

echo "assembled tree-sitter-masterbelt $version -> $out (from $rev)"
