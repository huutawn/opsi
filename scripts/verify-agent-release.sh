#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BUILDER="$ROOT/scripts/build-agent-release.sh"
TEST_VERSION="0.0.0-reproducibility-test"
TEST_COMMIT="0000000000000000000000000000000000000000"
work="$(mktemp -d "${TMPDIR:-/tmp}/opsi-agent-release-verify.XXXXXX")"
cleanup() {
  rm -rf "$work"
}
trap cleanup EXIT

out1="$work/output-one"
out2="$work/output-two"
cache1="$work/go-cache-one"
cache2="$work/go-cache-two"
mkdir -p "$cache1" "$cache2"

env \
  AGENT_VERSION="$TEST_VERSION" \
  AGENT_COMMIT="$TEST_COMMIT" \
  OUT_DIR="$out1" \
  GOCACHE="$cache1" \
  GOTOOLCHAIN="${GOTOOLCHAIN:-local}" \
  "$BUILDER"
env \
  AGENT_VERSION="$TEST_VERSION" \
  AGENT_COMMIT="$TEST_COMMIT" \
  OUT_DIR="$out2" \
  GOCACHE="$cache2" \
  GOTOOLCHAIN="${GOTOOLCHAIN:-local}" \
  "$BUILDER"

for name in opsi-agent-linux-amd64 checksums.txt release.json; do
  cmp "$out1/$name" "$out2/$name"
done

for output in "$out1" "$out2"; do
  test -x "$output/opsi-agent-linux-amd64"
  (
    cd "$output"
    sha256sum --check checksums.txt
  )
  RELEASE_ROOT="$output" python3 <<'PY'
import hashlib
import json
import os
from pathlib import Path

root = Path(os.environ["RELEASE_ROOT"])
binary = root / "opsi-agent-linux-amd64"
metadata = json.loads((root / "release.json").read_text())
digest = hashlib.sha256(binary.read_bytes()).hexdigest()
assert metadata == {
    "schema_version": 1,
    "name": "opsi-agent",
    "version": "0.0.0-reproducibility-test",
    "commit": "0" * 40,
    "os": "linux",
    "arch": "amd64",
    "binary": "opsi-agent-linux-amd64",
    "sha256": digest,
}
PY
done

echo "Agent release reproducibility verified byte-for-byte"
