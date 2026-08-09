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

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT HUP INT TERM

select_latest_beta() {
  awk '
    /"tag_name"[[:space:]]*:/ {
      tag=$0
      sub(/^.*"tag_name"[[:space:]]*:[[:space:]]*"/, "", tag)
      sub(/".*$/, "", tag)
      prerelease=0
    }
    /"prerelease"[[:space:]]*:[[:space:]]*true/ { prerelease=1 }
    /"published_at"[[:space:]]*:/ {
      published=$0
      sub(/^.*"published_at"[[:space:]]*:[[:space:]]*"/, "", published)
      sub(/".*$/, "", published)
      if (prerelease && tag ~ /^v[0-9]+\.[0-9]+\.[0-9]+-beta\.[0-9]+$/ && published > latest) {
        latest=published
        selected=tag
      }
    }
    END { print selected }
  '
}

version=${OPSI_VERSION:-}
if [ -z "$version" ]; then
  if [ "${OPSI_INSTALLER_SELF_TEST:-0}" = 1 ]; then
    version=$(printf '%s\n' \
      '[{"tag_name":"v0.1.0-beta.1","prerelease":true,"published_at":"2026-08-01T00:00:00Z"},' \
      '{"tag_name":"agent-test","prerelease":true,"published_at":"2026-08-09T00:00:00Z"},' \
      '{"tag_name":"v0.1.0-beta.2","prerelease":true,"published_at":"2026-08-08T00:00:00Z"}]' \
      | select_latest_beta)
    [ "$version" = "v0.1.0-beta.2" ] || die "latest beta resolver self-test failed"
  else
    curl --fail --silent --show-error --location --proto '=https' --tlsv1.2 \
      -H 'Accept: application/vnd.github+json' \
      -H 'X-GitHub-Api-Version: 2022-11-28' \
      'https://api.github.com/repos/huutawn/opsi/releases?per_page=100' \
      -o "$tmp/releases.json"
    version=$(select_latest_beta <"$tmp/releases.json")
    [ -n "$version" ] || die "no published beta release found"
  fi
fi
case "$version" in ""|.*|-*|*..*|*[!0-9A-Za-z._-]*) die "invalid version";; esac
default_base=https://github.com/huutawn/opsi/releases/download/$version
base=${OPSI_RELEASE_BASE_URL:-$default_base}
case "$base" in https://*) ;; *) die "release URL must use HTTPS";; esac
if [ -n "${OPSI_RELEASE_BASE_URL:-}" ] && [ "${OPSI_INSTALLER_SELF_TEST:-0}" != 1 ] && [ "${OPSI_ALLOW_UNSAFE_CUSTOM_MIRROR:-0}" != 1 ]; then
  die "custom release mirrors require OPSI_ALLOW_UNSAFE_CUSTOM_MIRROR=1; artifact and checksum trust moves to that mirror"
fi
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
  mkdir -p "$tmp/bundle/opsi-ui"
  printf '#!/bin/sh\nexit 0\n' >"$tmp/bundle/opsi"
  printf '<!doctype html><title>Opsi</title>\n' >"$tmp/bundle/opsi-ui/index.html"
  chmod 0755 "$tmp/bundle/opsi"
  archive="opsi-$version-$os-$arch.tar.gz"
  tar -C "$tmp/bundle" -czf "$tmp/$archive" opsi opsi-ui
  (cd "$tmp" && (sha256sum "$archive" 2>/dev/null || shasum -a 256 "$archive") >checksums.txt)
  verify_checksum "$tmp/$archive" "$tmp/checksums.txt"
  sed 's/^[0-9a-fA-F]*/0000000000000000000000000000000000000000000000000000000000000000/' "$tmp/checksums.txt" >"$tmp/bad.txt"
  if (verify_checksum "$tmp/$archive" "$tmp/bad.txt" 2>/dev/null); then die "checksum self-test failed"; fi
  if [ "$install_dir" = "/" ]; then die "unsafe installation directory"; fi
  printf '%s\n' "installer self-test passed"
  exit 0
fi

archive="opsi-$version-$os-$arch.tar.gz"
curl --fail --silent --show-error --location --proto '=https' --tlsv1.2 "$base/$archive" -o "$tmp/$archive"
curl --fail --silent --show-error --location --proto '=https' --tlsv1.2 "$base/checksums.txt" -o "$tmp/checksums.txt"
verify_checksum "$tmp/$archive" "$tmp/checksums.txt"

if tar -tzf "$tmp/$archive" | awk '
  /^\// { bad=1 }
  /(^|\/)\.\.($|\/)/ { bad=1 }
  !/^(opsi|opsi-ui(\/.*)?)$/ { bad=1 }
  END { exit bad }
'; then :; else die "archive contains unsafe or unexpected paths"; fi
mkdir "$tmp/unpacked"
tar -C "$tmp/unpacked" -xzf "$tmp/$archive"
[ -f "$tmp/unpacked/opsi" ] || die "archive is missing the opsi binary"
[ -f "$tmp/unpacked/opsi-ui/index.html" ] || die "archive is missing Local UI assets"

mkdir -p "$install_dir"
[ ! -L "$install_dir" ] || die "unsafe installation directory"
target="$install_dir/opsi"
[ ! -L "$target" ] || die "unsafe installation target"
if [ -e "$target" ] && [ ! -f "$target" ]; then die "unsafe installation target"; fi
ui_target="$install_dir/opsi-ui"
[ ! -L "$ui_target" ] || die "unsafe UI installation target"
if [ -e "$ui_target" ] && [ ! -d "$ui_target" ]; then die "unsafe UI installation target"; fi
mode=$(umask)
umask 077
staged=$(mktemp "$install_dir/.opsi.new.XXXXXX")
staged_ui=$(mktemp -d "$install_dir/.opsi-ui.new.XXXXXX")
trap 'rm -f "${staged:-/nonexistent}"; rm -rf "${staged_ui:-/nonexistent}" "$tmp"' EXIT HUP INT TERM
cp "$tmp/unpacked/opsi" "$staged"
cp -R "$tmp/unpacked/opsi-ui/." "$staged_ui/"
chmod 0755 "$staged"
mv -f "$staged" "$target"
staged=
if [ -e "$ui_target" ]; then rm -rf "$ui_target"; fi
mv "$staged_ui" "$ui_target"
staged_ui=
umask "$mode"
printf '%s\n' "installed opsi $version and Local UI to $install_dir"
if [ "${OPSI_INSTALL_SELF_TEST:-0}" = 1 ]; then
  "$target" version >/dev/null || die "installed opsi version check failed"
fi
