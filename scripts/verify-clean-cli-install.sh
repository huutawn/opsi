#!/bin/sh
set -eu

die() { printf '%s\n' "$1" >&2; exit 1; }

version=${1:?version is required}
revision=${2:?revision is required}
release_dir=${3:-dist/cli}
case "$release_dir" in /*) ;; *) release_dir=$PWD/$release_dir;; esac
[ -f "$release_dir/checksums.txt" ] || die "release checksums are missing"
command -v openssl >/dev/null 2>&1 || die "openssl is required"
command -v python3 >/dev/null 2>&1 || die "python3 is required"

tmp=$(mktemp -d)
release_pid=
start_pid=
cleanup() {
  [ -z "$start_pid" ] || kill "$start_pid" 2>/dev/null || :
  [ -z "$release_pid" ] || kill "$release_pid" 2>/dev/null || :
  rm -rf "$tmp"
}
trap cleanup EXIT HUP INT TERM

mkdir -p "$tmp/home" "$tmp/install" "$tmp/mirror" "$tmp/work"
cp "$release_dir"/* "$tmp/mirror/"
cp scripts/install-cli.sh "$tmp/mirror/install-cli.sh"
openssl req -x509 -newkey rsa:2048 -sha256 -days 1 -nodes \
  -subj '/CN=127.0.0.1' -addext 'subjectAltName=IP:127.0.0.1' \
  -keyout "$tmp/key.pem" -out "$tmp/cert.pem" >/dev/null 2>&1

free_port() {
  python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1]); s.close()'
}

release_port=$(free_port)
(cd "$tmp/mirror" && openssl s_server -quiet -accept "127.0.0.1:$release_port" -cert "$tmp/cert.pem" -key "$tmp/key.pem" -WWW) >"$tmp/release.log" 2>&1 &
release_pid=$!
for attempt in 1 2 3 4 5 6 7 8 9 10; do
  CURL_CA_BUNDLE="$tmp/cert.pem" curl --fail --silent "https://127.0.0.1:$release_port/checksums.txt" >/dev/null 2>&1 && break
  [ "$attempt" -eq 10 ] || sleep 1
done
CURL_CA_BUNDLE="$tmp/cert.pem" curl --fail --silent "https://127.0.0.1:$release_port/checksums.txt" >/dev/null

(cd "$tmp/work" && \
  CURL_CA_BUNDLE="$tmp/cert.pem" curl --fail --silent --show-error "https://127.0.0.1:$release_port/install-cli.sh" | \
  env -i HOME="$tmp/home" PATH="$PATH" CURL_CA_BUNDLE="$tmp/cert.pem" \
    OPSI_VERSION="$version" OPSI_INSTALL_DIR="$tmp/install" \
    OPSI_RELEASE_BASE_URL="https://127.0.0.1:$release_port" \
    OPSI_ALLOW_UNSAFE_CUSTOM_MIRROR=1 sh)

"$tmp/install/opsi" version --json >"$tmp/version.json"
ui_port=$(free_port)
(cd "$tmp/work" && HOME="$tmp/home" "$tmp/install/opsi" start --addr "127.0.0.1:$ui_port") >"$tmp/start.log" 2>&1 &
start_pid=$!
for attempt in 1 2 3 4 5 6 7 8 9 10; do
  curl --fail --silent "http://127.0.0.1:$ui_port/health" >/dev/null 2>&1 && break
  [ "$attempt" -eq 10 ] || sleep 1
done
curl --fail --silent "http://127.0.0.1:$ui_port/health" | grep -q '"status":"ok"'
curl --fail --silent "http://127.0.0.1:$ui_port/" | grep -q '<title>Opsi'
curl --fail --silent "http://127.0.0.1:$ui_port/api/local/settings" >"$tmp/settings.json"
curl --fail --silent "http://127.0.0.1:$ui_port/api/local/session" >"$tmp/session.json"

python3 - "$version" "$revision" "$tmp/version.json" "$tmp/settings.json" "$tmp/session.json" <<'PY'
import json
import sys

version, revision, version_path, settings_path, session_path = sys.argv[1:]
with open(version_path, encoding="utf-8") as source:
    cli = json.load(source)
with open(settings_path, encoding="utf-8") as source:
    settings = json.load(source)
with open(session_path, encoding="utf-8") as source:
    session = json.load(source)

assert cli == {"revision": revision, "version": version}, cli
assert settings["version"] == version and settings["revision"] == revision, settings
assert settings["cloud_authority"] == "https://opsidev.site", settings
assert settings["cloud_configured"] is True, settings
assert settings["agent_configured"] is False, settings
assert settings["config_selected"] is False, settings
assert session["agent_connected"] == "not connected", session
PY

printf '%s\n' "clean artifact-only install smoke passed"
