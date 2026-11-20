#!/bin/sh
# Install factorymeter: the newest release for this system, checked against
# the checksums of the release.
#
#   curl -fsSL https://marshal-org.github.io/factorymeter/install.sh | sh
#
# It downloads two files from the release on github.com, the binary and
# checksums.txt, and puts the binary in ~/.local/bin, or in /usr/local/bin when
# ~/.local/bin is not on your PATH and /usr/local/bin is yours to write. It
# needs no token and changes nothing else. Later, "factorymeter upgrade"
# replaces it in place.
#
# Settings, all optional:
#   FACTORYMETER_VERSION=v0.5.0          a release instead of the newest
#   FACTORYMETER_INSTALL_DIR=~/bin       where to put the binary
set -eu

base="${FACTORYMETER_RELEASES_URL:-https://github.com/marshal-org/factorymeter}"
version="${FACTORYMETER_VERSION:-}"
dir="${FACTORYMETER_INSTALL_DIR:-}"

say() { printf '%s\n' "$*"; }
fail() {
  printf 'factorymeter install: %s\n' "$*" >&2
  exit 1
}

case "$(uname -s)" in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  MINGW* | MSYS* | CYGWIN*) os=windows ;;
  *) fail "no release for $(uname -s). Build it from source: go install github.com/marshal-org/factorymeter/cmd/factorymeter@latest" ;;
esac
case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  arm64 | aarch64) arch=arm64 ;;
  *) fail "no release for $(uname -m). Build it from source: go install github.com/marshal-org/factorymeter/cmd/factorymeter@latest" ;;
esac
asset="factorymeter-$os-$arch"
name=factorymeter
if [ "$os" = windows ]; then
  [ "$arch" = amd64 ] || fail "no release for windows/$arch"
  asset="$asset.exe"
  name=factorymeter.exe
fi

if command -v curl > /dev/null 2>&1; then
  fetch() { curl -fsSL -o "$2" "$1"; }
  latest() { curl -fsSI "$base/releases/latest" | tr -d '\r' | sed -n 's/^[Ll]ocation: *//p' | tail -n 1; }
elif command -v wget > /dev/null 2>&1; then
  fetch() { wget -q -O "$2" "$1"; }
  latest() { wget -q -S --max-redirect=0 -O /dev/null "$base/releases/latest" 2>&1 | tr -d '\r' | sed -n 's/^ *[Ll]ocation: *//p' | tail -n 1; }
else
  fail "it needs curl or wget"
fi
if command -v sha256sum > /dev/null 2>&1; then
  sha() { sha256sum "$1" | cut -d ' ' -f 1; }
elif command -v shasum > /dev/null 2>&1; then
  sha() { shasum -a 256 "$1" | cut -d ' ' -f 1; }
else
  fail "it needs sha256sum or shasum to check the download"
fi

if [ -z "$version" ]; then
  version="$(latest)"
  version="${version##*/}"
fi
case "$version" in
  v[0-9]*.[0-9]*.[0-9]*) ;;
  [0-9]*.[0-9]*.[0-9]*) version="v$version" ;;
  *) fail "cannot tell the newest release from $base/releases/latest" ;;
esac

tmp="$(mktemp -d 2> /dev/null || mktemp -d -t factorymeter)"
trap 'rm -rf "$tmp"' EXIT INT TERM
url="$base/releases/download/$version"
say "Downloading factorymeter $version for $os/$arch"
fetch "$url/checksums.txt" "$tmp/checksums.txt" || fail "release $version has no checksums.txt"
fetch "$url/$asset" "$tmp/$asset" || fail "release $version has no file $asset"
want="$(awk -v f="$asset" '$2 == f || $2 == "*" f { print $1 }' "$tmp/checksums.txt")"
[ -n "$want" ] || fail "checksums.txt of $version has no sum for $asset"
[ "$(sha "$tmp/$asset")" = "$want" ] || fail "the download does not match the checksum of release $version. Nothing was installed"
chmod 755 "$tmp/$asset"
if [ "$os" != windows ]; then
  "$tmp/$asset" --version 2> /dev/null | grep -q "$version" || fail "the downloaded binary does not run on this system, or is not factorymeter $version. Nothing was installed"
fi

on_path() {
  case ":$PATH:" in
    *":$1:"*) return 0 ;;
  esac
  return 1
}
if [ -z "$dir" ]; then
  dir="$HOME/.local/bin"
  if ! on_path "$dir" && [ -d /usr/local/bin ] && [ -w /usr/local/bin ] && on_path /usr/local/bin; then
    dir=/usr/local/bin
  fi
fi
mkdir -p "$dir" || fail "cannot make $dir"
[ -w "$dir" ] || fail "cannot write to $dir. Set FACTORYMETER_INSTALL_DIR to a directory you own"
# Copied next to the target and renamed, so a factorymeter that is running
# keeps its file until it ends.
cp "$tmp/$asset" "$dir/.$name.new"
mv -f "$dir/.$name.new" "$dir/$name"

say "Installed factorymeter $version to $dir/$name. It matches the checksum of the release."
if ! on_path "$dir"; then
  say ""
  say "$dir is not on your PATH. Add it, then open a new terminal:"
  say "  echo 'export PATH=\"$dir:\$PATH\"' >> ~/.profile"
fi
say ""
say "Next:"
say "  factorymeter init       answer a few questions, get a factorymeter.yml"
say "  factorymeter sync       read your history"
say "  factorymeter serve      see the line in the browser"
say "Later, factorymeter upgrade gets the newest release."
