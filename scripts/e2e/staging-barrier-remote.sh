#!/usr/bin/env bash
set -euo pipefail
export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin LC_ALL=C
unset BASH_ENV ENV CDPATH GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE GIT_SSH GIT_SSH_COMMAND DOCKER_HOST SSH_AUTH_SOCK
umask 077

python3 /dev/fd/3 "$0" 3<<'PY'
import datetime
import hashlib
import json
import os
import pathlib
import re
import stat
import subprocess
import sys
import tempfile

MAX_MESSAGE = 4096
REQUEST_SCHEMA = "opsi.e2e.staging-barrier-request/v1"
RECEIPT_SCHEMA = "opsi.e2e.staging-barrier-receipt/v1"
STATE_SCHEMA = "opsi.e2e.staging-barrier-state/v1"
PHASES = {"preflight", "prepare", "start", "status", "restart", "restore", "abort"}
IDENTIFIER = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$")
REVISION = re.compile(r"^[0-9a-f]{40}$")
DIGEST = re.compile(r"^sha256:[0-9a-f]{64}$")
SAFE_PATH = re.compile(r"^/[A-Za-z0-9._/-]{1,1023}$")
SAFE_HOST = re.compile(r"^[A-Za-z0-9](?:[A-Za-z0-9.-]{0,251}[A-Za-z0-9])?$")
CHILD_ENV = {"PATH": "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL": "C"}


class BarrierError(Exception):
    pass


def no_duplicates(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise BarrierError("request contains duplicate fields")
        result[key] = value
    return result


def require_path(raw, label):
    if not isinstance(raw, str) or not SAFE_PATH.fullmatch(raw) or ".." in pathlib.PurePosixPath(raw).parts:
        raise BarrierError(f"{label} is invalid")
    path = pathlib.Path(raw)
    if str(path) != os.path.normpath(raw):
        raise BarrierError(f"{label} is not normalized")
    return path


def reject_symlink_components(path, label):
    current = pathlib.Path(path.anchor)
    for part in path.parts[1:]:
        current /= part
        try:
            info = current.lstat()
        except OSError as exc:
            raise BarrierError(f"{label} is unavailable") from exc
        if stat.S_ISLNK(info.st_mode):
            raise BarrierError(f"{label} contains a symlink")


def run(command, label, timeout=180):
    try:
        result = subprocess.run(
            command,
            text=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            timeout=timeout,
            env=CHILD_ENV,
            check=False,
        )
    except (OSError, subprocess.TimeoutExpired) as exc:
        raise BarrierError(f"{label} failed") from exc
    if len(result.stdout.encode()) > MAX_MESSAGE or len(result.stderr.encode()) > MAX_MESSAGE:
        raise BarrierError(f"{label} output exceeded the bound")
    if result.returncode or result.stderr:
        raise BarrierError(f"{label} failed")
    return result.stdout.strip()


def strict_json(raw, label):
    try:
        value = json.loads(raw.decode("utf-8"), object_pairs_hook=no_duplicates)
    except (UnicodeDecodeError, json.JSONDecodeError, BarrierError) as exc:
        raise BarrierError(f"{label} is malformed") from exc
    if not isinstance(value, dict):
        raise BarrierError(f"{label} must be one JSON object")
    return value


def validate_request():
    raw = sys.stdin.buffer.read(MAX_MESSAGE + 1)
    if not 1 <= len(raw) <= MAX_MESSAGE:
        raise BarrierError("request size is invalid")
    request = strict_json(raw, "request")
    keys = {
        "schema_version", "phase", "source_revision", "run_id", "staging_host",
        "repository_directory", "repository_identity", "compose_directory",
        "worker_digest", "session_id", "expected_state",
    }
    if set(request) != keys or request.get("schema_version") != REQUEST_SCHEMA:
        raise BarrierError("request schema is invalid")
    if request.get("phase") not in PHASES:
        raise BarrierError("request phase is invalid")
    if not isinstance(request.get("source_revision"), str) or not REVISION.fullmatch(request["source_revision"]):
        raise BarrierError("request revision is invalid")
    if not isinstance(request.get("run_id"), str) or not IDENTIFIER.fullmatch(request["run_id"]):
        raise BarrierError("request run ID is invalid")
    if not isinstance(request.get("staging_host"), str) or not SAFE_HOST.fullmatch(request["staging_host"]):
        raise BarrierError("request staging host is invalid")
    if not isinstance(request.get("repository_identity"), str) or not re.fullmatch(r"[0-9a-f]{64}", request["repository_identity"]):
        raise BarrierError("request repository identity is invalid")
    if not isinstance(request.get("worker_digest"), str) or not DIGEST.fullmatch(request["worker_digest"]):
        raise BarrierError("request Worker digest is invalid")
    if not isinstance(request.get("session_id"), str) or (request["session_id"] and not IDENTIFIER.fullmatch(request["session_id"])):
        raise BarrierError("request session ID is invalid")
    if not isinstance(request.get("expected_state"), str) or request["expected_state"] not in {
        "any", "absent", "worker_quiesced", "session_created", "armed",
        "barrier_started", "replay_started", "normal_restored"
    }:
        raise BarrierError("request expected state is invalid")
    if (request["phase"] == "status") != (request["expected_state"] == "any"):
        raise BarrierError("status is the only phase that accepts any state")
    request["repository_directory"] = require_path(request["repository_directory"], "repository directory")
    request["compose_directory"] = require_path(request["compose_directory"], "Compose directory")
    return request


def validate_repository(request, invoked_helper):
    repository = request["repository_directory"]
    compose = request["compose_directory"]
    reject_symlink_components(repository, "repository directory")
    reject_symlink_components(compose, "Compose directory")
    if not repository.is_dir() or not compose.is_dir():
        raise BarrierError("repository or Compose directory is unavailable")
    expected_helper = repository / "scripts/e2e/staging-barrier-remote.sh"
    reject_symlink_components(expected_helper, "remote helper")
    if pathlib.Path(invoked_helper) != expected_helper or not expected_helper.is_file():
        raise BarrierError("remote helper path is invalid")
    if compose != repository / "deploy/staging-control-plane":
        raise BarrierError("Compose directory is not canonical")
    revision = request["source_revision"]
    if run(["git", "-C", str(repository), "rev-parse", "HEAD"], "repository HEAD") != revision:
        raise BarrierError("repository revision mismatch")
    run(["git", "-C", str(repository), "diff", "--quiet", "--exit-code"], "tracked worktree check")
    run(["git", "-C", str(repository), "diff", "--cached", "--quiet", "--exit-code"], "index check")
    tracked = run(["git", "-C", str(repository), "ls-files", "--error-unmatch", "scripts/e2e/staging-barrier-remote.sh"], "helper tracking check")
    if tracked != "scripts/e2e/staging-barrier-remote.sh":
        raise BarrierError("remote helper is not tracked")
    helper_blob = run(["git", "-C", str(repository), "hash-object", str(expected_helper)], "helper hash")
    committed_blob = run(["git", "-C", str(repository), "rev-parse", revision + ":scripts/e2e/staging-barrier-remote.sh"], "helper blob lookup")
    if helper_blob != committed_blob or not re.fullmatch(r"[0-9a-f]{40}", helper_blob):
        raise BarrierError("remote helper blob mismatch")
    origin = run(["git", "-C", str(repository), "remote", "get-url", "origin"], "repository identity")
    if hashlib.sha256(origin.encode()).hexdigest() != request["repository_identity"]:
        raise BarrierError("repository identity mismatch")
    for relative in (
        "compose.yaml", ".env", "compose.e2e-bootstrap-barrier.yaml",
        "config/bootstrap-worker.json", "scripts/e2e/bootstrap-worker-barrier.sh",
        "scripts/bootstrap-worker-release.py",
    ):
        path = compose / relative if not relative.startswith("scripts/") else repository / relative
        reject_symlink_components(path, relative)
        if not path.is_file():
            raise BarrierError(f"required staging path is missing: {relative}")
    host = run(["hostname", "-f"], "staging host identity")
    if not SAFE_HOST.fullmatch(host) or host != request["staging_host"]:
        raise BarrierError("staging host identity mismatch")
    return helper_blob


def compose_prefix(compose):
    return [
        "docker", "compose", "--project-name", "opsi-staging",
        "--project-directory", str(compose), "--env-file", str(compose / ".env"),
        "-f", str(compose / "compose.yaml"),
    ]


def validate_compose(compose, digest):
    services = run(compose_prefix(compose) + ["config", "--services"], "Compose validation").splitlines()
    if sorted(services) != ["bootstrap-worker", "cloud", "postgres", "reverse-proxy"]:
        raise BarrierError("Compose service set is invalid")
    env = (compose / ".env").read_text(encoding="utf-8")
    project = [line.split("=", 1)[1].strip() for line in env.splitlines() if line.startswith("COMPOSE_PROJECT_NAME=")]
    worker = [line.split("=", 1)[1].strip() for line in env.splitlines() if line.startswith("OPSI_BOOTSTRAP_WORKER_IMAGE=")]
    expected = "ghcr.io/huutawn/opsi-bootstrap-worker@" + digest
    if project != ["opsi-staging"] or worker != [expected]:
        raise BarrierError("Compose project or Worker binding is invalid")


def running_worker(compose, digest):
    running = run(compose_prefix(compose) + ["ps", "-q", "bootstrap-worker"], "running Worker lookup")
    existing = run(compose_prefix(compose) + ["ps", "-a", "-q", "bootstrap-worker"], "Worker lookup")
    if not IDENTIFIER.fullmatch(running) or running != existing:
        raise BarrierError("expected exactly one running Worker")
    image = run(["docker", "inspect", "--format", "{{.Config.Image}}", running], "Worker image lookup")
    if image != "ghcr.io/huutawn/opsi-bootstrap-worker@" + digest:
        raise BarrierError("running Worker digest mismatch")
    return running


def state_path(compose, revision, run_id):
    digest = hashlib.sha256((revision + "\0" + run_id).encode()).hexdigest()[:32]
    return compose / "barrier-state" / f"orchestration-{digest}.json"


def read_state(path, request, helper_blob, allow_absent=False):
    if not path.exists():
        if allow_absent:
            return None
        raise BarrierError("remote barrier state is absent")
    info = path.lstat()
    if stat.S_ISLNK(info.st_mode) or not stat.S_ISREG(info.st_mode) or info.st_uid != os.geteuid() or stat.S_IMODE(info.st_mode) != 0o600 or not 1 <= info.st_size <= MAX_MESSAGE:
        raise BarrierError("remote barrier state file is invalid")
    state = strict_json(path.read_bytes(), "remote barrier state")
    keys = {
        "schema_version", "source_revision", "run_id", "repository_directory",
        "repository_identity", "compose_directory", "staging_host", "worker_digest",
        "helper_blob", "phase", "session_id",
        "pre_container_id", "current_container_id",
    }
    if set(state) != keys or state.get("schema_version") != STATE_SCHEMA:
        raise BarrierError("remote barrier state schema is invalid")
    expected = {
        "source_revision": request["source_revision"], "run_id": request["run_id"],
        "repository_directory": str(request["repository_directory"]),
        "repository_identity": request["repository_identity"],
        "compose_directory": str(request["compose_directory"]),
        "staging_host": request["staging_host"], "worker_digest": request["worker_digest"],
        "helper_blob": helper_blob,
    }
    if any(state.get(key) != value for key, value in expected.items()):
        raise BarrierError("remote barrier state identity mismatch")
    if state.get("phase") not in {
        "worker_quiesced", "session_created", "armed", "barrier_started",
        "replay_started", "normal_restored",
    }:
        raise BarrierError("remote barrier state phase is invalid")
    for key in ("session_id", "pre_container_id", "current_container_id"):
        if not isinstance(state.get(key), str) or (state[key] and not IDENTIFIER.fullmatch(state[key])):
            raise BarrierError("remote barrier state identity is invalid")
    return state


def write_state(path, state):
    path.parent.mkdir(mode=0o700, exist_ok=True)
    parent = path.parent.lstat()
    if stat.S_ISLNK(parent.st_mode) or not stat.S_ISDIR(parent.st_mode) or parent.st_uid != os.geteuid() or stat.S_IMODE(parent.st_mode) != 0o700:
        raise BarrierError("remote barrier state directory is invalid")
    data = (json.dumps(state, separators=(",", ":"), sort_keys=True) + "\n").encode()
    if len(data) > MAX_MESSAGE:
        raise BarrierError("remote barrier state is oversized")
    fd, temporary = tempfile.mkstemp(prefix=".orchestration-", dir=path.parent)
    try:
        os.fchmod(fd, 0o600)
        with os.fdopen(fd, "wb") as output:
            output.write(data)
            output.flush()
            os.fsync(output.fileno())
        os.replace(temporary, path)
        directory = os.open(path.parent, os.O_RDONLY | os.O_DIRECTORY)
        try:
            os.fsync(directory)
        finally:
            os.close(directory)
    finally:
        try:
            os.unlink(temporary)
        except FileNotFoundError:
            pass
    if path.read_bytes() != data:
        raise BarrierError("remote barrier state publication verification failed")


def delete_state(path):
    path.unlink()
    directory = os.open(path.parent, os.O_RDONLY | os.O_DIRECTORY)
    try:
        os.fsync(directory)
    finally:
        os.close(directory)


def marker_status(repository, compose, session_id, run_id):
    if not session_id:
        return "absent"
    return run(
        [str(repository / "scripts/e2e/bootstrap-worker-barrier.sh"), "status", "--state-dir", str(compose / "barrier-state"), "--session-id", session_id, "--run-id", run_id],
        "barrier marker status",
    )


def release(repository, compose, operation, digest, session_id="", run_id=""):
    command = [
        "python3", str(repository / "scripts/bootstrap-worker-release.py"), operation,
        "--expected-current-digest", digest, "--compose-project", "opsi-staging",
        "--compose-directory", str(compose), "--service", "bootstrap-worker",
        "--health-timeout", "180",
    ]
    if operation == "deploy":
        command[3:3] = ["--image", "ghcr.io/huutawn/opsi-bootstrap-worker@" + digest, "--force-recreate-same-image"]
        command.extend(["--compose-file", "compose.e2e-bootstrap-barrier.yaml"])
    elif operation == "barrier-replay":
        command.extend(["--compose-file", "compose.e2e-bootstrap-barrier.yaml", "--barrier-session-id", session_id, "--barrier-run-id", run_id])
    return run(command, f"Worker {operation}")


def output_value(output, key):
    values = [line.split("=", 1)[1] for line in output.splitlines() if line.startswith(key + "=")]
    if len(values) != 1 or not IDENTIFIER.fullmatch(values[0]):
        raise BarrierError(f"Worker {key} evidence is invalid")
    return values[0]


def receipt(request, helper_blob, before, after, container_before, container_after, marker, result):
    value = {
        "schema_version": RECEIPT_SCHEMA,
        "source_revision": request["source_revision"],
        "run_id": request["run_id"],
        "phase": request["phase"],
        "staging_host": request["staging_host"],
        "repository_directory": str(request["repository_directory"]),
        "repository_identity": request["repository_identity"],
        "compose_directory": str(request["compose_directory"]),
        "helper_blob": helper_blob,
        "state_before": before,
        "state_after": after,
        "worker_digest": request["worker_digest"],
        "session_id": request["session_id"],
        "worker_container_before": container_before,
        "worker_container_after": container_after,
        "marker_state": marker,
        "result": result,
        "timestamp": datetime.datetime.now(datetime.timezone.utc).replace(microsecond=0).isoformat().replace("+00:00", "Z"),
    }
    encoded = json.dumps(value, separators=(",", ":"), sort_keys=True)
    if len(encoded.encode()) > MAX_MESSAGE:
        raise BarrierError("receipt is oversized")
    print(encoded)


def main():
    request = validate_request()
    repository = request["repository_directory"]
    compose = request["compose_directory"]
    helper_blob = validate_repository(request, sys.argv[1])
    validate_compose(compose, request["worker_digest"])
    path = state_path(compose, request["source_revision"], request["run_id"])
    state = read_state(path, request, helper_blob, allow_absent=True)
    before = state["phase"] if state else "absent"
    if request["expected_state"] != "any" and before != request["expected_state"]:
        raise BarrierError("remote barrier state does not match the requested transition")
    phase = request["phase"]
    if phase == "preflight":
        if state:
            raise BarrierError("preflight requires no existing run state")
        if (compose / "config/bootstrap-worker.e2e.json").exists():
            raise BarrierError("preflight found stale barrier configuration")
        container = running_worker(compose, request["worker_digest"])
        receipt(request, helper_blob, "absent", "absent", container, container, "absent", "preflight-ok")
        return
    if phase == "prepare":
        if state:
            raise BarrierError("prepare cannot reuse remote state")
        output = release(repository, compose, "barrier-quiesce", request["worker_digest"])
        container = output_value(output, "quiesced_container_id")
        state = {
            "schema_version": STATE_SCHEMA, "source_revision": request["source_revision"],
            "run_id": request["run_id"], "repository_directory": str(repository),
            "repository_identity": request["repository_identity"], "compose_directory": str(compose),
            "staging_host": request["staging_host"], "worker_digest": request["worker_digest"],
            "helper_blob": helper_blob, "phase": "worker_quiesced", "session_id": "",
            "pre_container_id": container, "current_container_id": container,
        }
        try:
            write_state(path, state)
        except Exception as primary:
            try:
                release(repository, compose, "barrier-restore", request["worker_digest"])
            except BarrierError:
                raise BarrierError("remote state publication failed; pre-session restoration also failed") from primary
            raise
        receipt(request, helper_blob, "absent", "worker_quiesced", container, container, "absent", "worker-quiesced")
        return
    if phase == "status" and not state:
        receipt(request, helper_blob, "absent", "absent", "", "", "absent", "status-ok")
        return
    if not state:
        raise BarrierError("remote barrier state is absent")
    if phase == "start":
        if state["phase"] not in {"worker_quiesced", "session_created"} or not request["session_id"]:
            raise BarrierError("start transition is invalid")
        if state["session_id"] and state["session_id"] != request["session_id"]:
            raise BarrierError("start session identity mismatch")
        if state["phase"] == "worker_quiesced":
            state.update(phase="session_created", session_id=request["session_id"])
            write_state(path, state)
        if state["phase"] == "session_created":
            config = compose / "config/bootstrap-worker.e2e.json"
            if config.exists():
                raise BarrierError("existing barrier config requires manual reconciliation")
            run(
                [str(repository / "scripts/e2e/bootstrap-worker-barrier.sh"), "configure", "--source-config", str(compose / "config/bootstrap-worker.json"), "--output-config", str(config), "--session-id", request["session_id"], "--run-id", request["run_id"]],
                "barrier configuration",
            )
            run(
                [str(repository / "scripts/e2e/bootstrap-worker-barrier.sh"), "arm", "--state-dir", str(compose / "barrier-state"), "--session-id", request["session_id"], "--run-id", request["run_id"]],
                "barrier arm",
            )
            state.update(phase="armed")
            write_state(path, state)
        if state["phase"] != "armed" or marker_status(repository, compose, request["session_id"], request["run_id"]) != "armed":
            raise BarrierError("start requires an armed marker")
        output = release(repository, compose, "deploy", request["worker_digest"])
        container = output_value(output, "container_id")
        state.update(phase="barrier_started", session_id=request["session_id"], current_container_id=container)
        write_state(path, state)
        receipt(request, helper_blob, before, "barrier_started", state["pre_container_id"], container, marker_status(repository, compose, request["session_id"], request["run_id"]), "barrier-started")
        return
    if request["session_id"] != state["session_id"] and not (
        phase == "status" and state["phase"] == "worker_quiesced" and not state["session_id"]
    ):
        raise BarrierError("session identity mismatch")
    marker = marker_status(repository, compose, state["session_id"], request["run_id"])
    if phase == "status":
        receipt(request, helper_blob, state["phase"], state["phase"], state["current_container_id"], state["current_container_id"], marker, "status-ok")
    elif phase == "restart":
        if state["phase"] != "barrier_started" or marker != "reached":
            raise BarrierError("restart requires reached barrier state")
        output = release(repository, compose, "barrier-replay", request["worker_digest"], state["session_id"], request["run_id"])
        container = output_value(output, "container_id")
        previous = state["current_container_id"]
        state.update(phase="replay_started", current_container_id=container)
        write_state(path, state)
        receipt(request, helper_blob, "barrier_started", "replay_started", previous, container, marker_status(repository, compose, state["session_id"], request["run_id"]), "replay-started")
    elif phase == "restore":
        if state["phase"] != "replay_started" or marker != "completed":
            raise BarrierError("restore requires completed barrier evidence")
        output = release(repository, compose, "barrier-restore", request["worker_digest"])
        container = output_value(output, "container_id")
        previous = state["current_container_id"]
        state.update(phase="normal_restored", current_container_id=container)
        write_state(path, state)
        receipt(request, helper_blob, "replay_started", "normal_restored", previous, container, marker, "normal-restored")
    elif phase == "abort":
        if state["phase"] != "worker_quiesced" or state["session_id"] or request["session_id"]:
            raise BarrierError("abort is limited to pre-session state")
        output = release(repository, compose, "barrier-restore", request["worker_digest"])
        container = output_value(output, "container_id")
        previous = state["current_container_id"]
        state.update(phase="normal_restored", current_container_id=container)
        write_state(path, state)
        delete_state(path)
        receipt(request, helper_blob, "worker_quiesced", "absent", previous, container, "absent", "pre-session-aborted")
    else:
        raise BarrierError("phase is not valid for existing state")


try:
    main()
except (BarrierError, OSError, UnicodeError) as exc:
    message = str(exc).replace("\n", " ")[:512]
    print(f"staging barrier remote failed: {message}", file=sys.stderr)
    raise SystemExit(1)
PY
