#!/bin/sh
# dist.sh cross-builds the masterbelt CLI for every release target, archives
# each one deterministically, and writes a SHA256SUMS manifest over them into
# dist/. It is the one build path CI and a developer share, so the artifacts a
# nightly ships can be reproduced locally with `make dist`.
#
# The binaries are pure Go (CGO disabled), so every target cross-compiles on a
# single host. The version is the binary's own — read from Go's build info — so
# every cross build of one commit carries the same string. Archives are made
# reproducible: entries sorted, ownership zeroed, and mtimes pinned to the
# commit time (SOURCE_DATE_EPOCH) rather than the build clock.
#
# It uses GNU tar/find/coreutils behaviour (the linux build host); it is not
# meant to run on a BSD/macOS host.
set -eu

root="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
cd "$root"

# Output directory, overridable to match the Makefile's DIST_DIR.
dir="${DIST_DIR:-dist}"
out="$root/$dir"
rm -rf "$out"
mkdir -p "$out"

# Archive mtimes are the commit time, not the build clock — the determinism
# input the whole reproducible-build story hangs on.
SOURCE_DATE_EPOCH="$(git show -s --format=%ct HEAD)"
export SOURCE_DATE_EPOCH CGO_ENABLED=0

build_flags="-trimpath -buildvcs=true"
ldflags="-s -w -buildid="

# The version the binary reports: build it natively so we can run it, then reuse
# the string for every target's archive name (it is commit-derived, so identical
# across targets).
native="$(mktemp -d)"
# shellcheck disable=SC2086
go build $build_flags -ldflags "$ldflags" -o "$native/masterbelt" ./cmd/masterbelt
version="$("$native/masterbelt" --version | awk '{print $3}')"
rm -rf "$native"
echo "masterbelt $version"

for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64; do
	os="${target%/*}"
	arch="${target#*/}"
	bin="masterbelt"
	[ "$os" = windows ] && bin="masterbelt.exe"

	stage="$(mktemp -d)"
	# shellcheck disable=SC2086
	GOOS="$os" GOARCH="$arch" go build $build_flags -ldflags "$ldflags" -o "$stage/$bin" ./cmd/masterbelt
	[ -f LICENSE ] && cp LICENSE "$stage/"
	[ -f README.md ] && cp README.md "$stage/"
	# Pin every staged file's mtime so the archive does not embed the build time.
	find "$stage" -exec touch --no-dereference --date="@$SOURCE_DATE_EPOCH" {} +

	name="masterbelt-$os-$arch-$version"
	if [ "$os" = windows ]; then
		# zip stores per-file mtimes (pinned above); -X drops extra fields, and a
		# sorted file list fixes the entry order.
		( cd "$stage" && find . -type f -printf '%P\n' | LC_ALL=C sort | zip -X -q -9 "$out/$name.zip" -@ )
	else
		tar --sort=name --owner=0 --group=0 --numeric-owner --mtime="@$SOURCE_DATE_EPOCH" \
			-C "$stage" -cf - . | gzip -n -9 >"$out/$name.tar.gz"
	fi
	rm -rf "$stage"
	echo "  $name"
done

# One checksum manifest over every archive, in sorted order with bare names.
( cd "$out" && find . -maxdepth 1 -type f ! -name SHA256SUMS -printf '%P\n' | LC_ALL=C sort | xargs sha256sum >SHA256SUMS )
echo "wrote $dir/SHA256SUMS"
