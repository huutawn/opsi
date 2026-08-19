#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

if [[ $# -ne 2 ]]; then
  echo "usage: $0 <full-source-revision> <output-directory>" >&2
  exit 2
fi

revision="$1"
output="$2"
if [[ ! "$revision" =~ ^[0-9a-f]{40}$ ]]; then
  echo "source revision must be exactly 40 lowercase hexadecimal characters" >&2
  exit 2
fi
if [[ "$(git -C "$ROOT" rev-parse HEAD)" != "$revision" ]]; then
  echo "source revision does not match the checked-out revision" >&2
  exit 1
fi
if [[ ! "$(go env GOVERSION)" =~ ^go1\.26\.[45] ]]; then
  echo "Go 1.26.4 or 1.26.5 is required" >&2
  exit 1
fi

if [[ "$output" != /* ]]; then
  output="$ROOT/$output"
fi
if [[ -e "$output" ]]; then
  echo "output directory already exists: $output" >&2
  exit 1
fi

parent="$(dirname "$output")"
mkdir -p "$parent"
stage="$(mktemp -d "$parent/.agent-release.XXXXXX")"
trap 'rm -rf "$stage"' EXIT

version="agent-$revision"
binary="$stage/opsi-agent-linux-amd64"
(
  cd "$ROOT/agent"
  env \
    CGO_ENABLED=0 \
    GOOS=linux \
    GOARCH=amd64 \
    go build \
      -trimpath \
      -buildvcs=false \
      -ldflags "-X=main.version=$version -X=main.commit=$revision -buildid= -s -w" \
      -o "$binary" \
      ./cmd/opsi-agent
)

(
  cd "$stage"
  sha256sum opsi-agent-linux-amd64 > checksums.txt
)
sha256="$(awk '{print $1}' "$stage/checksums.txt")"

RELEASE_ROOT="$stage" RELEASE_REVISION="$revision" RELEASE_VERSION="$version" RELEASE_SHA256="$sha256" python3 <<'PY'
import json
import os
from pathlib import Path

root = Path(os.environ["RELEASE_ROOT"])
metadata = {
    "schema_version": "opsi.agent_release.v1",
    "source_revision": os.environ["RELEASE_REVISION"],
    "version": os.environ["RELEASE_VERSION"],
    "os": "linux",
    "arch": "amd64",
    "binary": "opsi-agent-linux-amd64",
    "sha256": os.environ["RELEASE_SHA256"],
}
(root / "release.json").write_text(json.dumps(metadata, indent=2) + "\n", encoding="utf-8")
PY

"$ROOT/scripts/verify-agent-release.sh" "$stage" "$revision"
mv "$stage" "$output"
stage=""
trap - EXIT

echo "Agent release artifact: $output"
