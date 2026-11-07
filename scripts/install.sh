#!/bin/sh
# Copyright (c) The Bogform Authors
# SPDX-License-Identifier: MPL-2.0
#
# Installs Bogform on Linux, macOS and the BSDs:
#
#   curl -fsSL https://raw.githubusercontent.com/bogware/bogform/main/scripts/install.sh | sh
#
# Settings, as environment variables:
#   BOGFORM_VERSION      A version such as 1.15.0. Defaults to the latest stable release.
#   BOGFORM_INSTALL_DIR  Where to put the binary. Defaults to /usr/local/bin if it's writable,
#                        or ~/.local/bin otherwise.
#
# The download is checked against the release's SHA256SUMS. If cosign is installed, the
# checksums' signature is checked too, proving the release was built by bogware/bogform's
# release workflow.

set -eu

repo="bogware/bogform"

fail() {
  echo "bogform install: $*" >&2
  exit 1
}

need() {
  command -v "$1" >/dev/null 2>&1 || fail "this script needs $1"
}

need uname
need tar
if command -v curl >/dev/null 2>&1; then
  fetch() { curl -fsSL --retry 3 -o "$2" "$1"; }
  fetch_stdout() { curl -fsSL --retry 3 "$1"; }
elif command -v wget >/dev/null 2>&1; then
  fetch() { wget -q -O "$2" "$1"; }
  fetch_stdout() { wget -q -O - "$1"; }
else
  fail "this script needs curl or wget"
fi

if command -v sha256sum >/dev/null 2>&1; then
  sha256() { sha256sum "$1" | cut -d' ' -f1; }
elif command -v shasum >/dev/null 2>&1; then
  sha256() { shasum -a 256 "$1" | cut -d' ' -f1; }
else
  fail "this script needs sha256sum or shasum"
fi

os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$os" in
  linux | darwin | freebsd | openbsd) ;;
  sunos) os=solaris ;;
  *) fail "there's no Bogform build for $os. Download one from https://github.com/$repo/releases" ;;
esac

arch=$(uname -m)
case "$arch" in
  x86_64 | amd64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  *) fail "there's no Bogform build for $arch. Download one from https://github.com/$repo/releases" ;;
esac

version="${BOGFORM_VERSION:-}"
if [ -z "$version" ]; then
  version=$(fetch_stdout "https://api.github.com/repos/$repo/releases/latest" |
    sed -n 's/.*"tag_name": *"v\([^"]*\)".*/\1/p' | head -n 1)
  [ -n "$version" ] || fail "couldn't find the latest release. Set BOGFORM_VERSION to choose one."
fi
version="${version#v}"

dir="${BOGFORM_INSTALL_DIR:-}"
if [ -z "$dir" ]; then
  if [ -w /usr/local/bin ]; then
    dir=/usr/local/bin
  else
    dir="$HOME/.local/bin"
  fi
fi

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT INT TERM

archive="bogform_${version}_${os}_${arch}.tar.gz"
sums="bogform_${version}_SHA256SUMS"
base="https://github.com/$repo/releases/download/v$version"

echo "Downloading Bogform $version for ${os}_${arch}..."
fetch "$base/$archive" "$tmp/$archive" || fail "couldn't download $base/$archive"
fetch "$base/$sums" "$tmp/$sums" || fail "couldn't download the checksums"

expected=$(grep " $archive\$" "$tmp/$sums" | cut -d' ' -f1)
[ -n "$expected" ] || fail "$archive isn't listed in the release's checksums"
[ "$(sha256 "$tmp/$archive")" = "$expected" ] || fail "the download doesn't match its checksum"
echo "Checksum verified."

if command -v cosign >/dev/null 2>&1; then
  fetch "$base/$sums.sig" "$tmp/$sums.sig"
  fetch "$base/$sums.pem" "$tmp/$sums.pem"
  cosign verify-blob \
    --certificate "$tmp/$sums.pem" \
    --signature "$tmp/$sums.sig" \
    --certificate-identity-regexp "^https://github.com/$repo/.github/workflows/release.yml@" \
    --certificate-oidc-issuer https://token.actions.githubusercontent.com \
    "$tmp/$sums" >/dev/null 2>&1 || fail "the checksums' signature didn't verify"
  echo "Signature verified."
else
  echo "Install cosign to also verify the release's signature."
fi

tar -xzf "$tmp/$archive" -C "$tmp" bogform
mkdir -p "$dir"
if [ -w "$dir" ]; then
  mv "$tmp/bogform" "$dir/bogform"
else
  echo "Installing to $dir needs sudo."
  sudo mv "$tmp/bogform" "$dir/bogform"
fi
chmod 755 "$dir/bogform" 2>/dev/null || sudo chmod 755 "$dir/bogform"

echo "Installed $("$dir/bogform" version | head -n 1) to $dir/bogform."
case ":$PATH:" in
  *":$dir:"*) ;;
  *) echo "Add $dir to your PATH to run it as \"bogform\"." ;;
esac
