#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage:
  scripts/e2e/bootstrap-worker-barrier.sh arm --state-dir DIR --session-id ID --run-id ID
  scripts/e2e/bootstrap-worker-barrier.sh status --state-dir DIR --session-id ID --run-id ID
  scripts/e2e/bootstrap-worker-barrier.sh disarm --state-dir DIR --session-id ID --run-id ID

This helper manages only the staging install_k3s crash-barrier marker. It does
not contact Cloud, the Agent VPS, or any public endpoint.
EOF
}

[ "$#" -ge 1 ] || { usage >&2; exit 2; }
COMMAND="$1"
shift
STATE_DIR=""
SESSION_ID=""
RUN_ID=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --state-dir) STATE_DIR="${2:?missing value for --state-dir}"; shift 2 ;;
    --session-id) SESSION_ID="${2:?missing value for --session-id}"; shift 2 ;;
    --run-id) RUN_ID="${2:?missing value for --run-id}"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown argument: $1" >&2; usage >&2; exit 2 ;;
  esac
done

case "$COMMAND" in arm|status|disarm) ;; *) echo "unknown command: $COMMAND" >&2; usage >&2; exit 2 ;; esac

python3 - "$COMMAND" "$STATE_DIR" "$SESSION_ID" "$RUN_ID" <<'PY'
import hashlib
import json
import os
import pathlib
import re
import stat
import sys

command, raw_dir, session_id, run_id = sys.argv[1:]
identifier = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$")
if not raw_dir or not os.path.isabs(raw_dir):
    raise SystemExit("state directory must be absolute")
if not identifier.fullmatch(session_id) or not identifier.fullmatch(run_id):
    raise SystemExit("session and run IDs must be bounded safe identifiers")
state_dir = pathlib.Path(raw_dir)
info = state_dir.stat()
if not stat.S_ISDIR(info.st_mode) or info.st_mode & 0o077:
    raise SystemExit("state directory must be a private directory")
name = "install_k3s-" + hashlib.sha256((session_id + "\0" + run_id).encode()).hexdigest()[:32] + ".json"
path = state_dir / name
base = {
    "version": 1,
    "environment": "e2e",
    "session_id": session_id,
    "run_id": run_id,
    "step": "install_k3s",
    "boundary": "after_execute_before_checkpoint",
}
if command == "arm":
    payload = {**base, "state": "armed"}
    temp = state_dir / ("." + name + ".tmp")
    fd = os.open(temp, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    try:
        with os.fdopen(fd, "w") as marker:
            json.dump(payload, marker, separators=(",", ":"))
            marker.flush()
            os.fsync(marker.fileno())
        os.replace(temp, path)
        directory_fd = os.open(state_dir, os.O_RDONLY)
        try:
            os.fsync(directory_fd)
        finally:
            os.close(directory_fd)
    finally:
        try:
            temp.unlink()
        except FileNotFoundError:
            pass
    print(path)
elif command == "status":
    if path.is_symlink():
        raise SystemExit("barrier marker must not be a symlink")
    if not path.exists():
        print("absent")
        raise SystemExit(0)
    info = path.lstat()
    if not stat.S_ISREG(info.st_mode) or info.st_mode & 0o077:
        raise SystemExit("barrier marker must be a private regular file")
    payload = json.loads(path.read_text())
    if any(payload.get(key) != value for key, value in base.items()):
        raise SystemExit("barrier marker target mismatch")
    print(payload.get("state", "invalid"))
elif command == "disarm":
    if path.is_symlink():
        raise SystemExit("barrier marker must not be a symlink")
    if path.exists():
        info = path.lstat()
        if not stat.S_ISREG(info.st_mode) or info.st_mode & 0o077:
            raise SystemExit("barrier marker must be a private regular file")
        path.unlink()
    print("disarmed")
PY
