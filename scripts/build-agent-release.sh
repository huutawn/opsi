#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
AGENT_VERSION="${AGENT_VERSION:-}"
AGENT_COMMIT="${AGENT_COMMIT:-}"
OUT_DIR="${OUT_DIR:-dist/agent}"

if [[ -z "$AGENT_VERSION" ]]; then
  echo "AGENT_VERSION is required" >&2
  exit 2
fi
if [[ "$AGENT_VERSION" =~ [[:space:]/] ]]; then
  echo "AGENT_VERSION must not contain whitespace or slash" >&2
  exit 2
fi
if [[ ! "$AGENT_COMMIT" =~ ^[0-9a-fA-F]{40}$ ]]; then
  echo "AGENT_COMMIT must be exactly 40 hexadecimal characters" >&2
  exit 2
fi

if [[ "$OUT_DIR" != /* ]]; then
  OUT_DIR="$ROOT/$OUT_DIR"
fi
parent="$(dirname "$OUT_DIR")"
mkdir -p "$parent"

stage="$(mktemp -d "$parent/.agent-release.XXXXXX")"
backup=""
cleanup() {
  if [[ -n "$stage" ]]; then
    rm -rf "$stage"
  fi
  if [[ -n "$backup" ]]; then
    rm -rf "$backup"
  fi
}
trap cleanup EXIT

binary="$stage/opsi-agent-linux-amd64"
(
  cd "$ROOT/agent"
  env \
    CGO_ENABLED=0 \
    GOOS=linux \
    GOARCH=amd64 \
    GOCACHE="${GOCACHE:-/tmp/opsi-go-cache}" \
    GOTOOLCHAIN="${GOTOOLCHAIN:-local}" \
    go build \
      -trimpath \
      -buildvcs=false \
      -ldflags "-X=main.version=$AGENT_VERSION -X=main.commit=$AGENT_COMMIT -buildid= -s -w" \
      -o "$binary" \
      ./cmd/opsi-agent
)

if [[ ! -x "$binary" ]]; then
  echo "release binary is missing or not executable" >&2
  exit 1
fi

(
  cd "$stage"
  sha256sum opsi-agent-linux-amd64 > checksums.txt
)
sha256="$(awk '{print $1}' "$stage/checksums.txt")"
if [[ ! "$sha256" =~ ^[0-9a-f]{64}$ ]]; then
  echo "invalid SHA-256 output" >&2
  exit 1
fi

RELEASE_ROOT="$stage" \
RELEASE_VERSION="$AGENT_VERSION" \
RELEASE_COMMIT="$AGENT_COMMIT" \
RELEASE_SHA256="$sha256" \
python3 <<'PY'
import json
import os
from pathlib import Path

root = Path(os.environ["RELEASE_ROOT"])
metadata = {
    "schema_version": 1,
    "name": "opsi-agent",
    "version": os.environ["RELEASE_VERSION"],
    "commit": os.environ["RELEASE_COMMIT"],
    "os": "linux",
    "arch": "amd64",
    "binary": "opsi-agent-linux-amd64",
    "sha256": os.environ["RELEASE_SHA256"],
}
(root / "release.json").write_text(json.dumps(metadata, indent=2) + "\n")
PY

(
  cd "$stage"
  sha256sum --check checksums.txt
)
RELEASE_ROOT="$stage" python3 <<'PY'
import hashlib
import json
import os
from pathlib import Path

root = Path(os.environ["RELEASE_ROOT"])
binary = root / "opsi-agent-linux-amd64"
metadata = json.loads((root / "release.json").read_text())
digest = hashlib.sha256(binary.read_bytes()).hexdigest()
expected_keys = [
    "schema_version",
    "name",
    "version",
    "commit",
    "os",
    "arch",
    "binary",
    "sha256",
]
if list(metadata) != expected_keys:
    raise SystemExit("release.json fields or order are invalid")
if metadata["sha256"] != digest:
    raise SystemExit("release.json checksum does not match binary")
PY

if [[ -e "$OUT_DIR" ]]; then
  backup="$(mktemp -d "$parent/.agent-release-old.XXXXXX")"
  rmdir "$backup"
  mv "$OUT_DIR" "$backup"
fi
if ! mv "$stage" "$OUT_DIR"; then
  if [[ -n "$backup" ]]; then
    mv "$backup" "$OUT_DIR"
    backup=""
  fi
  exit 1
fi
stage=""
if [[ -n "$backup" ]]; then
  rm -rf "$backup"
  backup=""
fi

echo "Agent release artifact: $OUT_DIR"
