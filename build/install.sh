#!/bin/sh
# install.sh downloads the latest masterbelt nightly CLI for this OS/arch from
# the GitHub release, verifies its checksum, and installs the binary:
#
#   curl -fsSL https://raw.githubusercontent.com/masterbelt/masterbelt/main/build/install.sh | sh
#
# This is an unsigned nightly build. Override via env: MASTERBELT_REPO
# (owner/repo) and MASTERBELT_INSTALL_DIR (target dir, default /usr/local/bin).
set -eu

repo="${MASTERBELT_REPO:-masterbelt/masterbelt}"
dir="${MASTERBELT_INSTALL_DIR:-/usr/local/bin}"

os="$(uname -s | tr '[:upper:]' '[:lower:]')"
case "$os" in
	linux | darwin) ;;
	*)
		echo "masterbelt: unsupported OS '$os' — this installer handles linux and macOS; on Windows use the .zip from the release." >&2
		exit 1
		;;
esac

arch="$(uname -m)"
case "$arch" in
	x86_64 | amd64) arch=amd64 ;;
	aarch64 | arm64) arch=arm64 ;;
	*)
		echo "masterbelt: unsupported architecture '$arch'." >&2
		exit 1
		;;
esac

# The nightly assets have deterministic names, so the URLs are fixed — no
# release-API lookup needed. The rolling `nightly` tag always points at the
# latest build, and each archive is masterbelt-<os>-<arch>.tar.gz.
base="https://github.com/$repo/releases/download/nightly"
asset_url="$base/masterbelt-$os-$arch.tar.gz"
sums_url="$base/SHA256SUMS"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
file="$(basename "$asset_url")"

echo "masterbelt: downloading $file…"
curl -fsSL "$asset_url" -o "$tmp/$file"
curl -fsSL "$sums_url" -o "$tmp/SHA256SUMS"

echo "masterbelt: verifying checksum…"
want="$(awk -v f="$file" '$2 == f {print $1}' "$tmp/SHA256SUMS")"
if [ -z "$want" ]; then
	echo "masterbelt: $file is not listed in SHA256SUMS." >&2
	exit 1
fi
if command -v sha256sum >/dev/null 2>&1; then
	got="$(sha256sum "$tmp/$file" | awk '{print $1}')"
else
	got="$(shasum -a 256 "$tmp/$file" | awk '{print $1}')"
fi
if [ "$want" != "$got" ]; then
	echo "masterbelt: checksum mismatch for $file" >&2
	exit 1
fi

tar -xzf "$tmp/$file" -C "$tmp"
if [ -w "$dir" ] || [ "$(id -u)" = 0 ]; then
	install -m 0755 "$tmp/masterbelt" "$dir/masterbelt"
else
	echo "masterbelt: $dir is not writable — installing with sudo."
	sudo install -m 0755 "$tmp/masterbelt" "$dir/masterbelt"
fi

echo "masterbelt: installed to $dir/masterbelt"
"$dir/masterbelt" version || true
