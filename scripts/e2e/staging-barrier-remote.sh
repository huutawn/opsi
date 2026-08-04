#!/usr/bin/env bash
set -euo pipefail
export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin LC_ALL=C
unset BASH_ENV ENV CDPATH GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE GIT_SSH GIT_SSH_COMMAND DOCKER_HOST SSH_AUTH_SOCK
umask 077

python3 /dev/fd/3 "$0" 3<<'PY'
import datetime
import hashlib
import ipaddress
import json
import os
import pathlib
import re
import stat
import subprocess
import sys
import tempfile

MAX_MESSAGE = 4096
REQUEST_SCHEMA = "opsi.e2e.staging-barrier-request/v2"
RECEIPT_SCHEMA = "opsi.e2e.staging-barrier-receipt/v2"
STATE_SCHEMA = "opsi.e2e.staging-barrier-state/v2"
PHASES = {"preflight", "prepare", "start", "status", "restart", "restore", "abort"}
STATES = {"absent", "worker_quiesced", "session_created", "armed", "barrier_started", "replay_started", "normal_restored"}
IDENTIFIER = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$")
REVISION = re.compile(r"^[0-9a-f]{40}$")
DIGEST = re.compile(r"^sha256:[0-9a-f]{64}$")
SAFE_PATH = re.compile(r"^/[A-Za-z0-9._/-]{1,1023}$")
HOST_LABEL = re.compile(r"^[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?$")
CHILD_ENV = {"PATH": "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL": "C"}
SERVICES = ("cloud", "postgres", "reverse-proxy")


class BarrierError(Exception):
    pass


def no_duplicates(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise BarrierError("JSON contains duplicate fields")
        result[key] = value
    return result


def valid_hostname(value):
    if not isinstance(value, str) or not 1 <= len(value) <= 253 or value.endswith("."):
        return False
    labels = value.split(".")
    return all(HOST_LABEL.fullmatch(label) for label in labels)


def valid_endpoint(value):
    if not isinstance(value, str) or any(ord(char) < 33 or ord(char) == 127 for char in value):
        return False
    try:
        return ipaddress.ip_address(value).version == 4
    except ValueError:
        return False if re.fullmatch(r"[0-9.]+", value) else valid_hostname(value)


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
        "schema_version", "phase", "source_revision", "run_id", "staging_endpoint",
        "staging_hostname", "repository_directory", "repository_identity",
        "compose_directory", "expected_helper_blob", "worker_digest", "session_id",
        "expected_state",
    }
    if set(request) != keys or request.get("schema_version") != REQUEST_SCHEMA:
        raise BarrierError("request schema is invalid")
    if request.get("phase") not in PHASES:
        raise BarrierError("request phase is invalid")
    if not isinstance(request.get("source_revision"), str) or not REVISION.fullmatch(request["source_revision"]):
        raise BarrierError("request revision is invalid")
    if not isinstance(request.get("run_id"), str) or not IDENTIFIER.fullmatch(request["run_id"]):
        raise BarrierError("request run ID is invalid")
    if not valid_endpoint(request.get("staging_endpoint")):
        raise BarrierError("request staging endpoint is invalid")
    if not valid_hostname(request.get("staging_hostname")):
        raise BarrierError("request staging hostname is invalid")
    if not isinstance(request.get("repository_identity"), str) or not re.fullmatch(r"[0-9a-f]{64}", request["repository_identity"]):
        raise BarrierError("request repository identity is invalid")
    if not isinstance(request.get("expected_helper_blob"), str) or not REVISION.fullmatch(request["expected_helper_blob"]):
        raise BarrierError("request helper blob is invalid")
    if not isinstance(request.get("worker_digest"), str) or not DIGEST.fullmatch(request["worker_digest"]):
        raise BarrierError("request Worker digest is invalid")
    if not isinstance(request.get("session_id"), str) or (request["session_id"] and not IDENTIFIER.fullmatch(request["session_id"])):
        raise BarrierError("request session ID is invalid")
    if not isinstance(request.get("expected_state"), str) or request["expected_state"] not in STATES | {"any"}:
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
    if not repository.is_dir() or not compose.is_dir() or compose != repository / "deploy/staging-control-plane":
        raise BarrierError("repository or canonical Compose directory is unavailable")
    revision = request["source_revision"]
    if run(["git", "-C", str(repository), "rev-parse", "HEAD"], "repository HEAD") != revision:
        raise BarrierError("repository revision mismatch")
    run(["git", "-C", str(repository), "diff", "--quiet", "--exit-code"], "tracked worktree check")
    run(["git", "-C", str(repository), "diff", "--cached", "--quiet", "--exit-code"], "index check")
    tracking = run(["git", "-C", str(repository), "ls-files", "-v", "--", "scripts/e2e/staging-barrier-remote.sh"], "helper tracking check")
    if tracking != "H scripts/e2e/staging-barrier-remote.sh":
        raise BarrierError("remote helper tracking flags are unsafe")
    committed_blob = run(["git", "-C", str(repository), "rev-parse", revision + ":scripts/e2e/staging-barrier-remote.sh"], "helper blob lookup")
    if committed_blob != request["expected_helper_blob"]:
        raise BarrierError("expected helper blob mismatch")
    executed_blob = run(["git", "hash-object", invoked_helper], "executed helper hash")
    if executed_blob != committed_blob:
        raise BarrierError("executed helper blob mismatch")
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
    hostname = run(["hostname", "-f"], "staging hostname identity")
    if not valid_hostname(hostname) or hostname != request["staging_hostname"]:
        raise BarrierError("staging hostname identity mismatch")
    return committed_blob, executed_blob


def compose_prefix(compose, barrier=False):
    command = [
        "docker", "compose", "--project-name", "opsi-staging",
        "--project-directory", str(compose), "--env-file", str(compose / ".env"),
        "-f", str(compose / "compose.yaml"),
    ]
    if barrier:
        command.extend(["-f", str(compose / "compose.e2e-bootstrap-barrier.yaml")])
    return command


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


def compose_ids(compose, service, running):
    arguments = ["ps"] + ([] if running else ["-a"]) + ["-q", service]
    output = run(compose_prefix(compose) + arguments, f"{service} container lookup")
    values = output.splitlines() if output else []
    if len(values) > 1 or any(not IDENTIFIER.fullmatch(value) for value in values):
        raise BarrierError(f"{service} container identity is invalid")
    return values


def dependency_containers(compose):
    result = {}
    for service in SERVICES:
        existing = compose_ids(compose, service, False)
        running = compose_ids(compose, service, True)
        if len(existing) != 1 or running != existing:
            raise BarrierError(f"{service} runtime is not exactly one running container")
        result[service] = existing[0]
    return result


def worker_runtime(compose, digest):
    existing = compose_ids(compose, "bootstrap-worker", False)
    running = compose_ids(compose, "bootstrap-worker", True)
    if running and running != existing:
        raise BarrierError("running Worker identity contradicts existing Worker identity")
    if not existing:
        return {"container": "", "running": False, "health": "absent", "profile": "absent"}
    container = existing[0]
    expected_image = "ghcr.io/huutawn/opsi-bootstrap-worker@" + digest
    configured_image = run(["docker", "inspect", "--format", "{{.Config.Image}}", container], "Worker configured image lookup")
    image_id = run(["docker", "inspect", "--format", "{{.Image}}", container], "Worker image ID lookup")
    try:
        repo_digests = json.loads(run(["docker", "image", "inspect", "--format", "{{json .RepoDigests}}", image_id], "Worker RepoDigest lookup"))
    except json.JSONDecodeError as exc:
        raise BarrierError("Worker RepoDigest evidence is malformed") from exc
    if not isinstance(repo_digests, list):
        raise BarrierError("Worker RepoDigest evidence is invalid")
    matches = [value for value in repo_digests if isinstance(value, str) and value.startswith("ghcr.io/huutawn/opsi-bootstrap-worker@")]
    if configured_image != expected_image or matches != [expected_image]:
        raise BarrierError("Worker digest mismatch")
    try:
        mounts = json.loads(run(["docker", "inspect", "--format", "{{json .Mounts}}", container], "Worker mount lookup"))
    except json.JSONDecodeError as exc:
        raise BarrierError("Worker mount evidence is malformed") from exc
    if not isinstance(mounts, list) or any(not isinstance(item, dict) for item in mounts):
        raise BarrierError("Worker mount evidence is invalid")
    destinations = {item.get("Destination"): item for item in mounts}
    config = destinations.get("/etc/opsi/bootstrap-worker.json", {})
    barrier = destinations.get("/var/lib/opsi/bootstrap-barrier")
    normal_source = os.path.realpath(compose / "config/bootstrap-worker.json")
    barrier_source = os.path.realpath(compose / "config/bootstrap-worker.e2e.json")
    state_source = os.path.realpath(compose / "barrier-state")
    if os.path.realpath(config.get("Source", "")) == normal_source and barrier is None and config.get("RW") is False:
        profile = "normal"
    elif (
        os.path.realpath(config.get("Source", "")) == barrier_source
        and config.get("RW") is False
        and isinstance(barrier, dict)
        and os.path.realpath(barrier.get("Source", "")) == state_source
        and barrier.get("RW") is True
    ):
        profile = "barrier"
    else:
        raise BarrierError("Worker runtime profile is invalid")
    is_running = bool(running)
    health = "stopped"
    if is_running:
        health = run(
            ["docker", "inspect", "--format", "{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}", container],
            "Worker health lookup",
        )
    return {"container": container, "running": is_running, "health": health, "profile": profile}


def state_path(compose, revision, run_id):
    digest = hashlib.sha256((revision + "\0" + run_id).encode()).hexdigest()[:32]
    return compose / "barrier-state" / f"orchestration-{digest}.json"


def read_state(path, request, committed_blob, executed_blob, allow_absent=False):
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
        "repository_identity", "compose_directory", "staging_endpoint", "staging_hostname",
        "worker_digest", "expected_helper_blob", "executed_helper_blob", "phase", "session_id",
        "pre_container_id", "current_container_id", "dependency_containers",
    }
    if set(state) != keys or state.get("schema_version") != STATE_SCHEMA:
        raise BarrierError("remote barrier state schema is invalid")
    expected = {
        "source_revision": request["source_revision"], "run_id": request["run_id"],
        "repository_directory": str(request["repository_directory"]),
        "repository_identity": request["repository_identity"],
        "compose_directory": str(request["compose_directory"]),
        "staging_endpoint": request["staging_endpoint"], "staging_hostname": request["staging_hostname"],
        "worker_digest": request["worker_digest"], "expected_helper_blob": committed_blob,
        "executed_helper_blob": executed_blob,
    }
    if any(state.get(key) != value for key, value in expected.items()):
        raise BarrierError("remote barrier state identity mismatch")
    if state.get("phase") not in STATES - {"absent"}:
        raise BarrierError("remote barrier state phase is invalid")
    for key in ("session_id", "pre_container_id", "current_container_id"):
        if not isinstance(state.get(key), str) or (state[key] and not IDENTIFIER.fullmatch(state[key])):
            raise BarrierError("remote barrier state identity is invalid")
    dependencies = state.get("dependency_containers")
    if not isinstance(dependencies, dict) or set(dependencies) != set(SERVICES) or any(not IDENTIFIER.fullmatch(value) for value in dependencies.values()):
        raise BarrierError("remote dependency identity state is invalid")
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


def validate_barrier_config(compose, session_id, run_id):
    path = compose / "config/bootstrap-worker.e2e.json"
    reject_symlink_components(path, "barrier configuration")
    info = path.lstat()
    if not stat.S_ISREG(info.st_mode) or not 1 <= info.st_size <= 65536:
        raise BarrierError("barrier configuration is invalid")
    config = strict_json(path.read_bytes(), "barrier configuration")
    barrier = config.get("staging_crash_barrier")
    if not isinstance(barrier, dict) or barrier.get("session_id") != session_id or barrier.get("run_id") != run_id:
        raise BarrierError("barrier configuration identity mismatch")


def validate_runtime(request, state, phase):
    compose = request["compose_directory"]
    runtime = worker_runtime(compose, request["worker_digest"])
    dependencies = dependency_containers(compose)
    if state and dependencies != state["dependency_containers"]:
        raise BarrierError("staging dependency container identity changed")
    marker = marker_status(request["repository_directory"], compose, state["session_id"], request["run_id"]) if state else "absent"
    if phase == "absent":
        if state or (compose / "config/bootstrap-worker.e2e.json").exists():
            raise BarrierError("absent state contains stale barrier evidence")
        expected = ("normal", True, "healthy")
    elif phase in {"worker_quiesced", "session_created", "armed"}:
        if not state or runtime["container"] != state["pre_container_id"] or state["current_container_id"] != state["pre_container_id"]:
            raise BarrierError("quiesced Worker identity mismatch")
        expected = ("normal", False, "stopped")
        if phase == "worker_quiesced" and (state["session_id"] or marker != "absent" or (compose / "config/bootstrap-worker.e2e.json").exists()):
            raise BarrierError("worker_quiesced state contains premature session evidence")
        if phase == "session_created" and (not state["session_id"] or marker != "absent" or (compose / "config/bootstrap-worker.e2e.json").exists()):
            raise BarrierError("session_created runtime evidence is invalid")
        if phase == "armed":
            validate_barrier_config(compose, state["session_id"], request["run_id"])
            if marker != "armed":
                raise BarrierError("armed state lacks an exact armed marker")
    elif phase == "barrier_started":
        if not state or runtime["container"] != state["current_container_id"] or state["current_container_id"] == state["pre_container_id"]:
            raise BarrierError("barrier Worker identity mismatch")
        expected = ("barrier", True, "healthy")
        validate_barrier_config(compose, state["session_id"], request["run_id"])
        if marker not in {"armed", "reached"}:
            raise BarrierError("barrier_started marker state is invalid")
    elif phase == "replay_started":
        if not state or runtime["container"] != state["current_container_id"] or state["current_container_id"] == state["pre_container_id"]:
            raise BarrierError("replay Worker identity mismatch")
        expected = ("barrier", True, "healthy")
        validate_barrier_config(compose, state["session_id"], request["run_id"])
        if marker not in {"reached", "consumed", "completed"}:
            raise BarrierError("replay_started marker state is invalid")
    elif phase == "normal_restored":
        if not state or runtime["container"] != state["current_container_id"] or state["current_container_id"] == state["pre_container_id"] or marker != "completed":
            raise BarrierError("normal restoration runtime evidence is invalid")
        expected = ("normal", True, "healthy")
    else:
        raise BarrierError("runtime phase is invalid")
    if (runtime["profile"], runtime["running"], runtime["health"]) != expected:
        raise BarrierError(f"{phase} durable state contradicts current Worker runtime")
    return {**runtime, "marker": marker, "dependencies": dependencies}


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


def receipt(request, committed_blob, executed_blob, before, after, container_before, observed, result):
    value = {
        "schema_version": RECEIPT_SCHEMA,
        "source_revision": request["source_revision"],
        "run_id": request["run_id"],
        "phase": request["phase"],
        "staging_endpoint": request["staging_endpoint"],
        "staging_hostname": request["staging_hostname"],
        "repository_directory": str(request["repository_directory"]),
        "repository_identity": request["repository_identity"],
        "compose_directory": str(request["compose_directory"]),
        "expected_helper_blob": committed_blob,
        "executed_helper_blob": executed_blob,
        "state_before": before,
        "state_after": after,
        "worker_digest": request["worker_digest"],
        "session_id": request["session_id"],
        "worker_container_before": container_before,
        "worker_container_after": observed["container"],
        "worker_profile": observed["profile"],
        "worker_running": observed["running"],
        "worker_health": observed["health"],
        "dependency_containers": observed["dependencies"],
        "marker_state": observed["marker"],
        "result": result,
        "timestamp": datetime.datetime.now(datetime.timezone.utc).replace(microsecond=0).isoformat().replace("+00:00", "Z"),
    }
    encoded = json.dumps(value, separators=(",", ":"), sort_keys=True)
    if len(encoded.encode()) > MAX_MESSAGE:
        raise BarrierError("receipt is oversized")
    print(encoded)


def new_state(request, committed_blob, executed_blob, container, dependencies):
    return {
        "schema_version": STATE_SCHEMA,
        "source_revision": request["source_revision"],
        "run_id": request["run_id"],
        "repository_directory": str(request["repository_directory"]),
        "repository_identity": request["repository_identity"],
        "compose_directory": str(request["compose_directory"]),
        "staging_endpoint": request["staging_endpoint"],
        "staging_hostname": request["staging_hostname"],
        "worker_digest": request["worker_digest"],
        "expected_helper_blob": committed_blob,
        "executed_helper_blob": executed_blob,
        "phase": "worker_quiesced",
        "session_id": "",
        "pre_container_id": container,
        "current_container_id": container,
        "dependency_containers": dependencies,
    }


def main():
    request = validate_request()
    repository = request["repository_directory"]
    compose = request["compose_directory"]
    committed_blob, executed_blob = validate_repository(request, sys.argv[1])
    validate_compose(compose, request["worker_digest"])
    path = state_path(compose, request["source_revision"], request["run_id"])
    state = read_state(path, request, committed_blob, executed_blob, allow_absent=True)
    before = state["phase"] if state else "absent"
    if request["expected_state"] != "any" and before != request["expected_state"]:
        raise BarrierError("remote barrier state does not match the requested transition")
    phase = request["phase"]
    if phase == "preflight":
        observed = validate_runtime(request, None, "absent")
        receipt(request, committed_blob, executed_blob, "absent", "absent", observed["container"], observed, "preflight-ok")
        return
    if phase == "prepare":
        before_runtime = validate_runtime(request, None, "absent")
        output = release(repository, compose, "barrier-quiesce", request["worker_digest"])
        container = output_value(output, "quiesced_container_id")
        if container != before_runtime["container"]:
            raise BarrierError("quiesced Worker identity changed")
        state = new_state(request, committed_blob, executed_blob, container, before_runtime["dependencies"])
        observed = validate_runtime(request, state, "worker_quiesced")
        try:
            write_state(path, state)
        except Exception as primary:
            try:
                release(repository, compose, "barrier-restore", request["worker_digest"])
            except BarrierError:
                raise BarrierError("remote state publication failed; pre-session restoration also failed") from primary
            raise
        receipt(request, committed_blob, executed_blob, "absent", "worker_quiesced", container, observed, "worker-quiesced")
        return
    if phase == "status" and not state:
        observed = validate_runtime(request, None, "absent")
        receipt(request, committed_blob, executed_blob, "absent", "absent", observed["container"], observed, "status-ok")
        return
    if not state:
        raise BarrierError("remote barrier state is absent")
    if phase == "status":
        if request["session_id"] != state["session_id"] and not (state["phase"] == "worker_quiesced" and not request["session_id"]):
            raise BarrierError("session identity mismatch")
        observed = validate_runtime(request, state, state["phase"])
        receipt(request, committed_blob, executed_blob, state["phase"], state["phase"], observed["container"], observed, "status-ok")
        return
    if phase == "start":
        if state["phase"] not in {"worker_quiesced", "session_created"} or not request["session_id"]:
            raise BarrierError("start transition is invalid")
        validate_runtime(request, state, state["phase"])
        if state["session_id"] and state["session_id"] != request["session_id"]:
            raise BarrierError("start session identity mismatch")
        if state["phase"] == "worker_quiesced":
            state.update(phase="session_created", session_id=request["session_id"])
            write_state(path, state)
            validate_runtime(request, state, "session_created")
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
            validate_runtime(request, state, "armed")
        output = release(repository, compose, "deploy", request["worker_digest"])
        container = output_value(output, "container_id")
        previous = state["current_container_id"]
        state.update(phase="barrier_started", current_container_id=container)
        write_state(path, state)
        observed = validate_runtime(request, state, "barrier_started")
        receipt(request, committed_blob, executed_blob, before, "barrier_started", previous, observed, "barrier-started")
        return
    if request["session_id"] != state["session_id"]:
        raise BarrierError("session identity mismatch")
    if phase == "restart":
        observed_before = validate_runtime(request, state, "barrier_started")
        if observed_before["marker"] != "reached":
            raise BarrierError("restart requires reached barrier state")
        output = release(repository, compose, "barrier-replay", request["worker_digest"], state["session_id"], request["run_id"])
        container = output_value(output, "container_id")
        previous = state["current_container_id"]
        state.update(phase="replay_started", current_container_id=container)
        write_state(path, state)
        observed = validate_runtime(request, state, "replay_started")
        receipt(request, committed_blob, executed_blob, "barrier_started", "replay_started", previous, observed, "replay-started")
    elif phase == "restore":
        observed_before = validate_runtime(request, state, "replay_started")
        if observed_before["marker"] != "completed":
            raise BarrierError("restore requires completed barrier evidence")
        output = release(repository, compose, "barrier-restore", request["worker_digest"])
        container = output_value(output, "container_id")
        previous = state["current_container_id"]
        state.update(phase="normal_restored", current_container_id=container)
        write_state(path, state)
        observed = validate_runtime(request, state, "normal_restored")
        receipt(request, committed_blob, executed_blob, "replay_started", "normal_restored", previous, observed, "normal-restored")
    elif phase == "abort":
        validate_runtime(request, state, "worker_quiesced")
        output = release(repository, compose, "barrier-restore", request["worker_digest"])
        container = output_value(output, "container_id")
        previous = state["current_container_id"]
        state.update(phase="normal_restored", current_container_id=container)
        observed = worker_runtime(compose, request["worker_digest"])
        dependencies = dependency_containers(compose)
        if dependencies != state["dependency_containers"] or (observed["profile"], observed["running"], observed["health"]) != ("normal", True, "healthy"):
            raise BarrierError("pre-session restoration runtime evidence is invalid")
        delete_state(path)
        observed.update(marker="absent", dependencies=dependencies)
        receipt(request, committed_blob, executed_blob, "worker_quiesced", "absent", previous, observed, "pre-session-aborted")
    else:
        raise BarrierError("phase is not valid for existing state")


try:
    main()
except (BarrierError, OSError, UnicodeError) as exc:
    message = str(exc).replace("\n", " ")[:512]
    print(f"staging barrier remote failed: {message}", file=sys.stderr)
    raise SystemExit(1)
PY
