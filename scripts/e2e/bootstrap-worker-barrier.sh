#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage:
  scripts/e2e/bootstrap-worker-barrier.sh configure --source-config FILE --output-config FILE --session-id ID --run-id ID
  scripts/e2e/bootstrap-worker-barrier.sh arm --state-dir DIR --session-id ID --run-id ID
  scripts/e2e/bootstrap-worker-barrier.sh status --state-dir DIR --session-id ID --run-id ID
  scripts/e2e/bootstrap-worker-barrier.sh disarm --state-dir DIR --session-id ID --run-id ID
  scripts/e2e/bootstrap-worker-barrier.sh self-test

This helper creates the run-specific non-production config and manages the
staging install_k3s crash-barrier marker. It does not contact Cloud, the Agent
VPS, or any public endpoint.
EOF
}

[ "$#" -ge 1 ] || { usage >&2; exit 2; }
COMMAND="$1"
shift
if [ "$COMMAND" = self-test ]; then
  [ "$#" -eq 0 ] || { usage >&2; exit 2; }
  python3 - "$0" <<'PY'
import json
import os
import pathlib
import subprocess
import sys
import tempfile

script = pathlib.Path(os.path.abspath(os.path.expanduser(os.path.expandvars(sys.argv[1]))))
session_id, run_id = "boot-self-test", "run-self-test"

def run(*args, ok=True):
    result = subprocess.run([script, *args], text=True, capture_output=True)
    if (result.returncode == 0) != ok:
        raise SystemExit(f"barrier self-test command {args} returned {result.returncode}: {result.stderr}")
    return result

def marker_payload(state, process_id=None, **extra):
    payload = {
        "version": 1,
        "environment": "e2e",
        "session_id": session_id,
        "run_id": run_id,
        "step": "install_k3s",
        "boundary": "after_execute_before_checkpoint",
        "state": state,
        **extra,
    }
    if process_id is not None:
        payload["process_id"] = process_id
    return payload

with tempfile.TemporaryDirectory() as raw_root:
    root = pathlib.Path(raw_root)
    state_dir = root / "state"
    state_dir.mkdir(mode=0o700)
    args = ("--state-dir", str(state_dir), "--session-id", session_id, "--run-id", run_id)
    source = root / "bootstrap-worker.json"
    original = {
        "cloud_url": "http://cloud:9800",
        "allow_insecure_internal_cloud_url": True,
        "production": True,
        "bootstrap_worker_token_file": "/run/secrets/bootstrap-worker-token",
    }
    source.write_text(json.dumps(original))
    output = root / "bootstrap-worker.e2e.json"
    run("configure", "--source-config", str(source), "--output-config", str(output), "--session-id", session_id, "--run-id", run_id)
    configured = json.loads(output.read_text())
    assert output.stat().st_mode & 0o777 == 0o600
    assert configured["production"] is False
    assert configured["allow_insecure_internal_cloud_url"] is False
    assert json.loads(source.read_text()) == original
    assert configured["cloud_url"] == original["cloud_url"]
    assert configured["staging_crash_barrier"]["session_id"] == session_id
    assert configured["staging_crash_barrier"]["run_id"] == run_id
    run("configure", "--source-config", str(source), "--output-config", str(output), "--session-id", session_id, "--run-id", run_id, ok=False)

    source.write_text(json.dumps({"production": True, "bootstrap_worker_token": "must-not-copy"}))
    run("configure", "--source-config", str(source), "--output-config", str(root / "secret.json"), "--session-id", session_id, "--run-id", run_id, ok=False)
    source.write_text(json.dumps({"production": True, "cloud_url": "https://REPLACE_WITH_HOST"}))
    run("configure", "--source-config", str(source), "--output-config", str(root / "placeholder.json"), "--session-id", session_id, "--run-id", run_id, ok=False)

    marker = pathlib.Path(run("arm", *args).stdout.strip())
    original = marker.read_bytes()
    original_inode = marker.stat().st_ino
    assert run("status", *args).stdout.strip() == "armed"
    run("arm", *args, ok=False)
    assert marker.read_bytes() == original and marker.stat().st_ino == original_inode

    for state in ("reached", "consumed"):
        marker.write_text(json.dumps(marker_payload(state, "worker-1"), separators=(",", ":")))
        os.chmod(marker, 0o600)
        content, inode = marker.read_bytes(), marker.stat().st_ino
        run("arm", *args, ok=False)
        run("disarm", *args, ok=False)
        assert marker.read_bytes() == content and marker.stat().st_ino == inode
        assert run("status", *args).stdout.strip() == state

    marker.write_text(json.dumps(marker_payload("completed", "worker-1"), separators=(",", ":")))
    os.chmod(marker, 0o600)
    content, inode = marker.read_bytes(), marker.stat().st_ino
    run("disarm", *args, ok=False)
    assert marker.read_bytes() == content and marker.stat().st_ino == inode

    target_mismatches = {
        "version": 2,
        "environment": "staging",
        "session_id": "other-session",
        "run_id": "other-run",
        "step": "install_agent",
        "boundary": "before_execute",
    }
    invalid = [
        b"{",
        b'{"version":NaN}',
        json.dumps(marker_payload("completed", "worker-1")).encode() + b"{}",
        json.dumps(marker_payload("completed", "worker-1", unknown=True)).encode(),
        json.dumps(marker_payload("invalid", "worker-1")).encode(),
        json.dumps(marker_payload("armed", "worker-1")).encode(),
        json.dumps(marker_payload("reached")).encode(),
        json.dumps(marker_payload("consumed")).encode(),
        json.dumps(marker_payload("completed")).encode(),
        json.dumps({**marker_payload("completed", "worker-1"), "version": True}).encode(),
        b"x" * 4097,
    ]
    invalid.extend(json.dumps({**marker_payload("completed", "worker-1"), key: value}).encode() for key, value in target_mismatches.items())
    for data in invalid:
        marker.write_bytes(data)
        os.chmod(marker, 0o600)
        run("status", *args, ok=False)

    marker.write_text(json.dumps(marker_payload("completed", "worker-1")))
    os.chmod(marker, 0o644)
    run("status", *args, ok=False)
    os.chmod(marker, 0o600)
    assert run("status", *args).stdout.strip() == "completed"

    target = root / "target"
    target.mkdir(mode=0o700)
    symlink = root / "state-link"
    symlink.symlink_to(target, target_is_directory=True)
    symlink_args = ("--state-dir", str(symlink), "--session-id", session_id, "--run-id", run_id)
    for command in ("arm", "status", "disarm"):
        run(command, *symlink_args, ok=False)
    assert not list(target.iterdir())

    outside = root / "outside"
    outside.write_text("preserve")
    marker.unlink()
    marker.symlink_to(outside)
    run("status", *args, ok=False)
    run("disarm", *args, ok=False)
    assert outside.read_text() == "preserve"

print("bootstrap worker barrier self-test passed")
PY
  exit
fi
STATE_DIR=""
SESSION_ID=""
RUN_ID=""
SOURCE_CONFIG=""
OUTPUT_CONFIG=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --state-dir) STATE_DIR="${2:?missing value for --state-dir}"; shift 2 ;;
    --session-id) SESSION_ID="${2:?missing value for --session-id}"; shift 2 ;;
    --run-id) RUN_ID="${2:?missing value for --run-id}"; shift 2 ;;
    --source-config) SOURCE_CONFIG="${2:?missing value for --source-config}"; shift 2 ;;
    --output-config) OUTPUT_CONFIG="${2:?missing value for --output-config}"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown argument: $1" >&2; usage >&2; exit 2 ;;
  esac
done

case "$COMMAND" in configure|arm|status|disarm) ;; *) echo "unknown command: $COMMAND" >&2; usage >&2; exit 2 ;; esac

python3 - "$COMMAND" "$STATE_DIR" "$SESSION_ID" "$RUN_ID" "$SOURCE_CONFIG" "$OUTPUT_CONFIG" <<'PY'
import hashlib
import json
import os
import re
import stat
import sys

command, raw_dir, session_id, run_id, source_config, output_config = sys.argv[1:]
identifier = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$")
if not identifier.fullmatch(session_id) or not identifier.fullmatch(run_id):
    raise SystemExit("session and run IDs must be bounded safe identifiers")
if command == "configure":
    if not os.path.isabs(source_config) or not os.path.isabs(output_config):
        raise SystemExit("config paths must be absolute")
    info = os.lstat(source_config)
    if stat.S_ISLNK(info.st_mode) or not stat.S_ISREG(info.st_mode) or info.st_size < 1 or info.st_size > 65536:
        raise SystemExit("source config must be a bounded regular non-symlink file")
    def no_duplicates(pairs):
        result = {}
        for key, value in pairs:
            if key in result:
                raise SystemExit(f"source config contains duplicate field: {key}")
            result[key] = value
        return result
    with open(source_config, "r", encoding="utf-8") as source:
        config = json.load(source, object_pairs_hook=no_duplicates)
    if not isinstance(config, dict) or config.get("production") is not True or "staging_crash_barrier" in config:
        raise SystemExit("source config must be the normal production Worker config")
    forbidden = {"bootstrap_worker_token", "ssh_private_key", "ssh_password", "password", "pat", "otp_code", "totp_code", "kubeconfig"}
    def validate(value):
        if isinstance(value, dict):
            for key, child in value.items():
                if key.lower() in forbidden and child not in (None, "", False):
                    raise SystemExit("source config contains an inline credential")
                validate(child)
        elif isinstance(value, list):
            for child in value:
                validate(child)
        elif isinstance(value, str) and "REPLACE_WITH" in value:
            raise SystemExit("source config contains a placeholder")
    validate(config)
    if not isinstance(config.get("cloud_url"), str) or not config["cloud_url"]:
        raise SystemExit("source config must contain cloud_url")
    config["production"] = False
    config["allow_insecure_internal_cloud_url"] = False
    config["staging_crash_barrier"] = {
        "enabled": True,
        "environment": "e2e",
        "session_id": session_id,
        "run_id": run_id,
        "step": "install_k3s",
        "boundary": "after_execute_before_checkpoint",
        "state_dir": "/var/lib/opsi/bootstrap-barrier",
    }
    flags = os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW
    fd = os.open(output_config, flags, 0o600)
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as output:
            json.dump(config, output, indent=2, sort_keys=True)
            output.write("\n")
            output.flush()
            os.fsync(output.fileno())
    except Exception:
        try:
            os.unlink(output_config)
        except FileNotFoundError:
            pass
        raise
    directory_fd = os.open(os.path.dirname(output_config), os.O_RDONLY | os.O_DIRECTORY)
    try:
        os.fsync(directory_fd)
    finally:
        os.close(directory_fd)
    print(output_config)
    raise SystemExit(0)
if not raw_dir or not os.path.isabs(raw_dir):
    raise SystemExit("state directory must be absolute")
info = os.lstat(raw_dir)
if stat.S_ISLNK(info.st_mode) or not stat.S_ISDIR(info.st_mode) or info.st_mode & 0o077:
    raise SystemExit("state directory must be a private directory")
directory_fd = os.open(raw_dir, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
info = os.fstat(directory_fd)
if not stat.S_ISDIR(info.st_mode) or info.st_mode & 0o077:
    os.close(directory_fd)
    raise SystemExit("state directory must be a private directory")
name = "install_k3s-" + hashlib.sha256((session_id + "\0" + run_id).encode()).hexdigest()[:32] + ".json"
base = {
    "version": 1,
    "environment": "e2e",
    "session_id": session_id,
    "run_id": run_id,
    "step": "install_k3s",
    "boundary": "after_execute_before_checkpoint",
}
def reject_json_constant(value):
    raise ValueError(value)

try:
    if command == "arm":
        payload = {**base, "state": "armed"}
        temp = "." + name + "." + os.urandom(8).hex() + ".tmp"
        fd = os.open(temp, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600, dir_fd=directory_fd)
        try:
            with os.fdopen(fd, "w") as marker:
                json.dump(payload, marker, separators=(",", ":"))
                marker.flush()
                os.fsync(marker.fileno())
            try:
                os.link(temp, name, src_dir_fd=directory_fd, dst_dir_fd=directory_fd, follow_symlinks=False)
            except FileExistsError:
                raise SystemExit("barrier marker already exists; disarm explicitly before arming")
            os.fsync(directory_fd)
        finally:
            try:
                os.unlink(temp, dir_fd=directory_fd)
            except FileNotFoundError:
                pass
        print(os.path.join(raw_dir, name))
    elif command == "status":
        try:
            fd = os.open(name, os.O_RDONLY | os.O_NOFOLLOW, dir_fd=directory_fd)
        except FileNotFoundError:
            print("absent")
            raise SystemExit(0)
        with os.fdopen(fd, "rb") as marker:
            info = os.fstat(marker.fileno())
            if not stat.S_ISREG(info.st_mode) or info.st_mode & 0o077:
                raise SystemExit("barrier marker must be a private regular file")
            if info.st_size > 4096:
                raise SystemExit("barrier marker is too large")
            data = marker.read(4097)
        if len(data) > 4096:
            raise SystemExit("barrier marker is too large")
        try:
            payload = json.loads(data, parse_constant=reject_json_constant)
        except (UnicodeDecodeError, json.JSONDecodeError, ValueError) as error:
            raise SystemExit(f"invalid barrier marker JSON: {error}")
        if not isinstance(payload, dict):
            raise SystemExit("barrier marker must be a JSON object")
        allowed = {*base, "state", "process_id"}
        if set(payload) - allowed:
            raise SystemExit("barrier marker contains unknown fields")
        if type(payload.get("version")) is not int or any(payload.get(key) != value for key, value in base.items()):
            raise SystemExit("barrier marker target mismatch")
        state = payload.get("state")
        process_id = payload.get("process_id", "")
        if state not in {"armed", "reached", "consumed", "completed"}:
            raise SystemExit("barrier marker state is invalid")
        if not isinstance(process_id, str) or (state == "armed" and process_id) or (state != "armed" and not process_id):
            raise SystemExit("barrier marker process_id is invalid for state")
        print(state)
    elif command == "disarm":
        try:
            fd = os.open(name, os.O_RDONLY | os.O_NOFOLLOW, dir_fd=directory_fd)
        except FileNotFoundError:
            print("disarmed")
            raise SystemExit(0)
        with os.fdopen(fd, "rb") as marker:
            info = os.fstat(marker.fileno())
            if info.st_size > 4096:
                raise SystemExit("barrier marker is too large")
            data = marker.read(4097)
        if not stat.S_ISREG(info.st_mode) or info.st_mode & 0o077:
            raise SystemExit("barrier marker must be a private regular file")
        try:
            payload = json.loads(data, parse_constant=reject_json_constant)
        except (UnicodeDecodeError, json.JSONDecodeError, ValueError) as error:
            raise SystemExit(f"invalid barrier marker JSON: {error}")
        if not isinstance(payload, dict) or set(payload) != {*base, "state"} or any(payload.get(key) != value for key, value in base.items()) or payload.get("state") != "armed":
            raise SystemExit("only the exact armed barrier marker may be disarmed")
        os.unlink(name, dir_fd=directory_fd)
        os.fsync(directory_fd)
        print("disarmed")
finally:
    os.close(directory_fd)
PY
