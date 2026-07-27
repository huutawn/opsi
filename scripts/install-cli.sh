#!/bin/sh
set -eu

die() { printf '%s\n' "$1" >&2; exit 1; }
case "${OSTYPE:-}" in *msys*|*cygwin*) die "unsupported operating system";; esac

os=$(uname -s | tr '[:upper:]' '[:lower:]')
arch=$(uname -m)
case "$os/$arch" in
  linux/x86_64) os=linux; arch=amd64;;
  linux/aarch64|linux/arm64) os=linux; arch=arm64;;
  darwin/x86_64) os=darwin; arch=amd64;;
  darwin/arm64) os=darwin; arch=arm64;;
  *) die "unsupported OS or architecture";;
esac

version=${OPSI_VERSION:-}
[ -n "$version" ] || die "OPSI_VERSION is required"
case "$version" in *..*|*/*|*' '*|*'	'*) die "invalid version";; esac
base=${OPSI_RELEASE_BASE_URL:-https://github.com/huutawn/opsi/releases/download/$version}
case "$base" in https://*) ;; *) die "release URL must use HTTPS";; esac
install_dir=${OPSI_INSTALL_DIR:-${HOME:-.}/.local/bin}
case "$install_dir" in /*) ;; *) die "installation directory must be absolute";; esac
[ "$install_dir" != "/" ] || die "unsafe installation directory"

verify_checksum() {
  file=$1
  checksums=$2
  expected=$(awk -v name="$(basename "$file")" '$2 == name {print $1; exit}' "$checksums")
  [ -n "$expected" ] || die "checksum entry is missing"
  case "$expected" in *[!0-9a-fA-F]*) die "invalid checksum";; esac
  actual=$(sha256sum "$file" 2>/dev/null | awk '{print $1}' || shasum -a 256 "$file" | awk '{print $1}')
  [ "$actual" = "$expected" ] || die "checksum mismatch"
}

if [ "${OPSI_INSTALLER_SELF_TEST:-0}" = 1 ]; then
	tmp=$(mktemp -d)
	trap 'rm -rf "$tmp"' EXIT HUP INT TERM
	printf 'opsi\n' >"$tmp/opsi-$version-$os-$arch"
	(cd "$tmp" && (sha256sum "opsi-$version-$os-$arch" 2>/dev/null || shasum -a 256 "opsi-$version-$os-$arch") >checksums.txt)
  verify_checksum "$tmp/opsi-$version-$os-$arch" "$tmp/checksums.txt"
  sed 's/^[0-9a-fA-F]*/0000000000000000000000000000000000000000000000000000000000000000/' "$tmp/checksums.txt" >"$tmp/bad.txt"
  if (verify_checksum "$tmp/opsi-$version-$os-$arch" "$tmp/bad.txt" 2>/dev/null); then die "checksum self-test failed"; fi
  if [ "$install_dir" = "/" ]; then die "unsafe installation directory"; fi
  printf '%s\n' "installer self-test passed"
  exit 0
fi

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT HUP INT TERM
binary="opsi-$version-$os-$arch"
curl --fail --silent --show-error --location --proto '=https' --tlsv1.2 "$base/$binary" -o "$tmp/$binary"
curl --fail --silent --show-error --location --proto '=https' --tlsv1.2 "$base/checksums.txt" -o "$tmp/checksums.txt"
verify_checksum "$tmp/$binary" "$tmp/checksums.txt"

mkdir -p "$install_dir"
[ ! -L "$install_dir" ] || die "unsafe installation directory"
target="$install_dir/opsi"
[ ! -L "$target" ] || die "unsafe installation target"
if [ -e "$target" ] && [ ! -f "$target" ]; then die "unsafe installation target"; fi
mode=$(umask)
umask 077
staged=$(mktemp "$install_dir/.opsi.new.XXXXXX")
trap 'rm -f "${staged:-/nonexistent}"; rm -rf "$tmp"' EXIT HUP INT TERM
cp "$tmp/$binary" "$staged"
chmod 0755 "$staged"
mv -f "$staged" "$target"
staged=
umask "$mode"
printf '%s\n' "installed opsi $version to $target"
if [ "${OPSI_INSTALL_SELF_TEST:-0}" = 1 ]; then
  "$target" version >/dev/null || die "installed opsi version check failed"
fi
