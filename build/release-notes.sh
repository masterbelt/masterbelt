#!/bin/sh
# release-notes.sh prints the markdown body for the rolling nightly release: the
# build's identity, the per-archive checksums, and an honest scope note. It reads
# the version from a built archive's name and the checksums from DIST_DIR, so it
# runs after `make dist` (or against the downloaded artifacts).
set -eu

root="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
out="$root/${DIST_DIR:-dist}"

version="$(basename "$out"/masterbelt-linux-amd64-*.tar.gz .tar.gz | sed 's/^masterbelt-linux-amd64-//')"
commit="$(git -C "$root" rev-parse HEAD)"
date="$(git -C "$root" show -s --format=%cI HEAD)"

cat <<EOF
Automated nightly build — **not a stable release.**

| | |
|---|---|
| version | \`$version\` |
| commit | \`$commit\` |
| date | $date |

### Checksums (SHA-256)

\`\`\`
$(cat "$out/SHA256SUMS")
\`\`\`

### Scope

Built on linux with CGO disabled. **Run-confirmed on linux/amd64 only**; the
other targets are build-confirmed (checksums above). macOS binaries are
**unsigned and unnotarized** — Gatekeeper will warn. This is a rolling build: the
\`nightly\` tag and these assets are overwritten every run, so pin a build by its
commit or checksum, not by the tag.
EOF
