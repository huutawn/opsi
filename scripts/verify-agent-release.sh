#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BUILDER="$ROOT/scripts/build-agent-release.sh"

verify_directory() {
  local directory="$1"
  local revision="$2"
  RELEASE_ROOT="$directory" RELEASE_REVISION="$revision" python3 <<'PY'
import hashlib
import json
import os
import pathlib
import re
import stat
import subprocess

root = pathlib.Path(os.environ["RELEASE_ROOT"])
revision = os.environ["RELEASE_REVISION"]
if not re.fullmatch(r"[0-9a-f]{40}", revision):
    raise SystemExit("source revision must be exactly 40 lowercase hexadecimal characters")

expected_names = {"opsi-agent-linux-amd64", "checksums.txt", "release.json"}
if not root.is_dir() or {path.name for path in root.iterdir()} != expected_names:
    raise SystemExit("release directory must contain exactly the three required assets")
if any(path.is_symlink() or not path.is_file() for path in root.iterdir()):
    raise SystemExit("release assets must be regular files")

binary = root / "opsi-agent-linux-amd64"
if not stat.S_IMODE(binary.stat().st_mode) & stat.S_IXUSR:
    raise SystemExit("Agent binary is not executable")
data = binary.read_bytes()
if len(data) < 20 or data[:4] != b"\x7fELF" or data[4:6] != b"\x02\x01" or int.from_bytes(data[18:20], "little") != 62:
    raise SystemExit("Agent binary is not Linux amd64 ELF")
if revision.encode() not in data:
    raise SystemExit("Agent binary does not embed the full source revision")

digest = hashlib.sha256(data).hexdigest()
checksum = (root / "checksums.txt").read_text(encoding="ascii")
if checksum != f"{digest}  opsi-agent-linux-amd64\n":
    raise SystemExit("checksums.txt is not exact or does not match the binary")

def reject_duplicates(pairs):
    value = {}
    for key, item in pairs:
        if key in value:
            raise ValueError(f"duplicate release.json field: {key}")
        value[key] = item
    return value

try:
    metadata = json.loads((root / "release.json").read_text(encoding="utf-8"), object_pairs_hook=reject_duplicates)
except (json.JSONDecodeError, UnicodeDecodeError, ValueError) as error:
    raise SystemExit(f"invalid release.json: {error}") from error

expected = {
    "schema_version": "opsi.agent_release.v1",
    "source_revision": revision,
    "version": f"agent-{revision}",
    "os": "linux",
    "arch": "amd64",
    "binary": "opsi-agent-linux-amd64",
    "sha256": digest,
}
if metadata != expected or (root / "release.json").read_text(encoding="utf-8") != json.dumps(expected, indent=2) + "\n":
    raise SystemExit("release.json is not the exact expected manifest")

result = subprocess.run([binary, "--version"], text=True, capture_output=True, check=False)
expected_version = f"opsi-agent version=agent-{revision} commit={revision}\n"
if result.returncode != 0 or result.stdout != expected_version or result.stderr:
    raise SystemExit("opsi-agent --version does not match release metadata")
PY
}

if [[ $# -eq 2 ]]; then
  verify_directory "$1" "$2"
  exit 0
fi
if [[ $# -ne 0 ]]; then
  echo "usage: $0 [release-directory full-source-revision]" >&2
  exit 2
fi

revision="$(git -C "$ROOT" rev-parse HEAD)"
work="$(mktemp -d "${TMPDIR:-/tmp}/opsi-agent-release-verify.XXXXXX")"
trap 'rm -rf "$work"' EXIT

for build in one two; do
  mkdir -p "$work/cache-$build"
  GOCACHE="$work/cache-$build" GOTOOLCHAIN="${GOTOOLCHAIN:-go1.26.4}" \
    "$BUILDER" "$revision" "$work/output-$build"
  verify_directory "$work/output-$build" "$revision"
done

for asset in opsi-agent-linux-amd64 checksums.txt release.json; do
  cmp "$work/output-one/$asset" "$work/output-two/$asset"
done

echo "Agent release reproducibility verified byte-for-byte"
