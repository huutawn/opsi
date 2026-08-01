#!/usr/bin/env python3
"""Create Bootstrap Worker release manifests and deploy one staging Worker."""

from __future__ import annotations

import argparse
import datetime as dt
import fcntl
import hashlib
import json
import os
import pathlib
import re
import shutil
import stat
import subprocess
import sys
import tempfile
import time

SCHEMA = "opsi.bootstrap-worker.release/v1"
SOURCE_REPOSITORY = "huutawn/opsi"
IMAGE_REPOSITORY = "ghcr.io/huutawn/opsi-bootstrap-worker"
PLATFORM = "linux/amd64"
COMPOSE_PROJECT = "opsi-staging"
SERVICE = "bootstrap-worker"
CLOUD_HEALTH_URL = "https://opsidev.site/health"
BARRIER_OVERRIDE = "compose.e2e-bootstrap-barrier.yaml"
BARRIER_CONFIG = pathlib.Path("config/bootstrap-worker.e2e.json")
BARRIER_STATE_DIR = pathlib.Path("barrier-state")
BARRIER_STEP = "install_k3s"
BARRIER_BOUNDARY = "after_execute_before_checkpoint"
DIGEST_RE = re.compile(r"^sha256:[0-9a-f]{64}$")
REVISION_RE = re.compile(r"^[0-9a-f]{40}$")
CREATED_RE = re.compile(r"^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$")
BARRIER_ID_RE = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$")
MANIFEST_KEYS = {
    "schema_version",
    "repository",
    "source_repository",
    "source_revision",
    "image_digest",
    "image_reference",
    "platform",
    "workflow_run_id",
    "created_at",
}


class ReleaseError(Exception):
    pass


def require_digest(value: str) -> str:
    if not DIGEST_RE.fullmatch(value):
        raise ReleaseError("digest must be sha256 followed by 64 lowercase hex characters")
    return value


def require_revision(value: str) -> str:
    if not REVISION_RE.fullmatch(value):
        raise ReleaseError("source revision must be a full 40-character lowercase hex commit")
    return value


def require_image_reference(value: str) -> str:
    prefix = IMAGE_REPOSITORY + "@"
    if not value.startswith(prefix):
        raise ReleaseError(f"image reference must use {IMAGE_REPOSITORY} by digest")
    require_digest(value[len(prefix) :])
    return value


def require_created_at(value: str) -> str:
    if not CREATED_RE.fullmatch(value):
        raise ReleaseError("created_at must be UTC RFC3339 without fractional seconds")
    try:
        dt.datetime.strptime(value, "%Y-%m-%dT%H:%M:%SZ")
    except ValueError as exc:
        raise ReleaseError("created_at is not a valid UTC timestamp") from exc
    return value


def no_duplicate_object(pairs: list[tuple[str, object]]) -> dict[str, object]:
    result: dict[str, object] = {}
    for key, value in pairs:
        if key in result:
            raise ReleaseError(f"release manifest contains duplicate field {key}")
        result[key] = value
    return result


def validate_manifest(value: object) -> dict[str, str]:
    if not isinstance(value, dict):
        raise ReleaseError("release manifest must contain one JSON object")
    keys = set(value)
    if keys != MANIFEST_KEYS:
        missing = sorted(MANIFEST_KEYS - keys)
        unknown = sorted(keys - MANIFEST_KEYS)
        raise ReleaseError(f"release manifest fields mismatch: missing={missing}, unknown={unknown}")
    if not all(isinstance(value[key], str) for key in MANIFEST_KEYS):
        raise ReleaseError("every release manifest field must be a string")
    manifest = value  # type: ignore[assignment]
    if manifest["schema_version"] != SCHEMA:
        raise ReleaseError("unsupported release manifest schema")
    if manifest["repository"] != IMAGE_REPOSITORY:
        raise ReleaseError("release manifest image repository mismatch")
    if manifest["source_repository"] != SOURCE_REPOSITORY:
        raise ReleaseError("release manifest source repository mismatch")
    require_revision(manifest["source_revision"])
    require_digest(manifest["image_digest"])
    require_image_reference(manifest["image_reference"])
    if manifest["image_reference"] != f"{IMAGE_REPOSITORY}@{manifest['image_digest']}":
        raise ReleaseError("release manifest image reference does not match its digest")
    if manifest["platform"] != PLATFORM:
        raise ReleaseError(f"release manifest platform must be {PLATFORM}")
    if not manifest["workflow_run_id"].isdigit():
        raise ReleaseError("workflow_run_id must be a decimal GitHub run ID")
    require_created_at(manifest["created_at"])
    return manifest


def load_manifest(path: pathlib.Path) -> dict[str, str]:
    info = path.lstat()
    if not stat.S_ISREG(info.st_mode) or stat.S_ISLNK(info.st_mode) or info.st_size > 65536:
        raise ReleaseError("release manifest must be a regular non-symlink file no larger than 64 KiB")
    try:
        value = json.loads(path.read_text(encoding="utf-8"), object_pairs_hook=no_duplicate_object)
    except (OSError, UnicodeError, json.JSONDecodeError) as exc:
        raise ReleaseError(f"cannot parse release manifest: {exc}") from exc
    return validate_manifest(value)


def create_manifest(args: argparse.Namespace) -> None:
    manifest = validate_manifest(
        {
            "schema_version": SCHEMA,
            "repository": IMAGE_REPOSITORY,
            "source_repository": SOURCE_REPOSITORY,
            "source_revision": args.source_revision,
            "image_digest": args.image_digest,
            "image_reference": f"{IMAGE_REPOSITORY}@{args.image_digest}",
            "platform": PLATFORM,
            "workflow_run_id": args.workflow_run_id,
            "created_at": args.created_at,
        }
    )
    output = pathlib.Path(args.output)
    output.parent.mkdir(parents=True, exist_ok=True)
    try:
        with output.open("x", encoding="utf-8") as handle:
            json.dump(manifest, handle, indent=2, sort_keys=True)
            handle.write("\n")
    except FileExistsError as exc:
        raise ReleaseError(f"refusing to overwrite existing manifest: {output}") from exc


def run(command: list[str]) -> str:
    result = subprocess.run(command, text=True, capture_output=True, check=False)
    if result.returncode != 0:
        detail = (result.stderr or result.stdout).strip()[-4096:]
        raise ReleaseError(f"command failed ({' '.join(command)}): {detail}")
    return result.stdout.strip()


def compose_prefix(args: argparse.Namespace) -> list[str]:
    if args.compose_project != COMPOSE_PROJECT:
        raise ReleaseError(f"compose project must be {COMPOSE_PROJECT}")
    if args.service != SERVICE:
        raise ReleaseError(f"service must be {SERVICE}")
    directory = pathlib.Path(args.compose_directory).resolve()
    base = directory / "compose.yaml"
    env_file = directory / ".env"
    if not base.is_file() or not env_file.is_file():
        raise ReleaseError("compose directory must contain compose.yaml and runtime .env")
    command = [
        "docker",
        "compose",
        "--project-name",
        COMPOSE_PROJECT,
        "--project-directory",
        str(directory),
        "--env-file",
        str(env_file),
        "-f",
        str(base),
    ]
    for raw in args.compose_file:
        extra = pathlib.Path(raw)
        if not extra.is_absolute():
            extra = directory / extra
        extra = extra.resolve()
        if extra == base or not extra.is_file():
            raise ReleaseError(f"invalid additional compose file: {raw}")
        command.extend(("-f", str(extra)))
    args._directory = directory
    args._env_file = env_file
    return command


def current_container_id(compose: list[str], include_stopped: bool = False) -> str:
    command = compose + ["ps"]
    if include_stopped:
        command.append("-a")
    container = run(command + ["-q", SERVICE])
    if not container or "\n" in container:
        state = "existing" if include_stopped else "running"
        raise ReleaseError(f"expected exactly one {state} Bootstrap Worker container")
    return container


def current_image_reference(compose: list[str], container: str | None = None) -> str:
    container = container or current_container_id(compose)
    image_id = run(["docker", "inspect", "--format", "{{.Image}}", container])
    raw = run(["docker", "image", "inspect", "--format", "{{json .RepoDigests}}", image_id])
    try:
        references = json.loads(raw)
    except json.JSONDecodeError as exc:
        raise ReleaseError("docker returned malformed image digest metadata") from exc
    if not isinstance(references, list):
        raise ReleaseError("docker returned malformed image digest metadata")
    matches = [ref for ref in references if isinstance(ref, str) and ref.startswith(IMAGE_REPOSITORY + "@")]
    if len(matches) != 1:
        raise ReleaseError("running Worker image has no unique canonical repository digest")
    return require_image_reference(matches[0])


def load_private_json(path: pathlib.Path, label: str, maximum: int = 65536) -> dict[str, object]:
    try:
        info = path.lstat()
    except OSError as exc:
        raise ReleaseError(f"{label} is unavailable: {exc}") from exc
    if stat.S_ISLNK(info.st_mode) or not stat.S_ISREG(info.st_mode) or info.st_mode & 0o077:
        raise ReleaseError(f"{label} must be a private regular non-symlink file")
    if info.st_size < 1 or info.st_size > maximum:
        raise ReleaseError(f"{label} size is invalid")
    try:
        value = json.loads(path.read_text(encoding="utf-8"), object_pairs_hook=no_duplicate_object)
    except (OSError, UnicodeError, json.JSONDecodeError) as exc:
        raise ReleaseError(f"cannot parse {label}: {exc}") from exc
    if not isinstance(value, dict):
        raise ReleaseError(f"{label} must contain one JSON object")
    return value


def contains_placeholder(value: object) -> bool:
    if isinstance(value, str):
        return "REPLACE_WITH" in value
    if isinstance(value, dict):
        return any(contains_placeholder(item) for item in value.values())
    if isinstance(value, list):
        return any(contains_placeholder(item) for item in value)
    return False


def validate_same_image_barrier(
    args: argparse.Namespace, marker_state: str = "armed", expected_session: str | None = None, expected_run: str | None = None
) -> tuple[str, str]:
    if len(args.compose_file) != 1:
        raise ReleaseError(f"same-image barrier recreation requires exactly {BARRIER_OVERRIDE}")
    raw = pathlib.Path(args.compose_file[0])
    if ".." in raw.parts:
        raise ReleaseError("barrier compose override must not contain traversal")
    override = raw if raw.is_absolute() else args._directory / raw
    expected_override = args._directory / BARRIER_OVERRIDE
    if override.absolute() != expected_override:
        raise ReleaseError(f"same-image barrier recreation requires exactly {BARRIER_OVERRIDE}")
    try:
        override_info = override.lstat()
    except OSError as exc:
        raise ReleaseError(f"barrier compose override is unavailable: {exc}") from exc
    if stat.S_ISLNK(override_info.st_mode) or not stat.S_ISREG(override_info.st_mode):
        raise ReleaseError("barrier compose override must be a regular non-symlink file")

    config = load_private_json(args._directory / BARRIER_CONFIG, "run-specific barrier config")
    if contains_placeholder(config):
        raise ReleaseError("run-specific barrier config contains a placeholder")
    if config.get("production") is not False:
        raise ReleaseError("production barrier configuration is forbidden")
    if config.get("allow_insecure_internal_cloud_url") is not False:
        raise ReleaseError("barrier configuration must disable insecure internal Cloud URLs")
    if not isinstance(config.get("cloud_url"), str) or not config["cloud_url"]:
        raise ReleaseError("barrier configuration must preserve cloud_url")
    barrier = config.get("staging_crash_barrier")
    expected_keys = {"enabled", "environment", "session_id", "run_id", "step", "boundary", "state_dir"}
    if not isinstance(barrier, dict) or set(barrier) != expected_keys:
        raise ReleaseError("run-specific barrier config is malformed")
    session_id = barrier.get("session_id")
    run_id = barrier.get("run_id")
    if not isinstance(session_id, str) or not BARRIER_ID_RE.fullmatch(session_id):
        raise ReleaseError("run-specific barrier session_id is invalid")
    if not isinstance(run_id, str) or not BARRIER_ID_RE.fullmatch(run_id):
        raise ReleaseError("run-specific barrier run_id is invalid")
    if (
        barrier.get("enabled") is not True
        or barrier.get("environment") != "e2e"
        or barrier.get("step") != BARRIER_STEP
        or barrier.get("boundary") != BARRIER_BOUNDARY
        or barrier.get("state_dir") != "/var/lib/opsi/bootstrap-barrier"
    ):
        raise ReleaseError("run-specific barrier config target is invalid")
    if expected_session is not None and session_id != expected_session:
        raise ReleaseError("barrier session does not match the requested operation")
    if expected_run is not None and run_id != expected_run:
        raise ReleaseError("barrier run does not match the requested operation")

    marker_name = "install_k3s-" + hashlib.sha256((session_id + "\0" + run_id).encode()).hexdigest()[:32] + ".json"
    marker = load_private_json(args._directory / BARRIER_STATE_DIR / marker_name, "barrier marker", 4096)
    expected_marker = {
        "version": 1,
        "environment": "e2e",
        "session_id": session_id,
        "run_id": run_id,
        "step": BARRIER_STEP,
        "boundary": BARRIER_BOUNDARY,
        "state": marker_state,
    }
    if marker_state == "reached":
        if set(marker) != set(expected_marker) | {"process_id"} or not isinstance(marker.get("process_id"), str) or not marker["process_id"]:
            raise ReleaseError("barrier marker must be reached with a process_id")
        marker = {key: marker[key] for key in expected_marker}
    if marker != expected_marker:
        raise ReleaseError(f"barrier marker must be {marker_state} for the configured session and run")
    return session_id, run_id


def read_binding(env_file: pathlib.Path) -> tuple[str, list[str], int]:
    lines = env_file.read_text(encoding="utf-8").splitlines(keepends=True)
    indexes = [i for i, line in enumerate(lines) if line.startswith("OPSI_BOOTSTRAP_WORKER_IMAGE=")]
    if len(indexes) != 1:
        raise ReleaseError("runtime .env must contain exactly one OPSI_BOOTSTRAP_WORKER_IMAGE binding")
    index = indexes[0]
    value = lines[index].split("=", 1)[1].strip()
    return require_image_reference(value), lines, index


def update_binding(env_file: pathlib.Path, lines: list[str], index: int, target: str) -> pathlib.Path:
    timestamp = dt.datetime.now(dt.timezone.utc).strftime("%Y%m%dT%H%M%SZ")
    backup_fd, backup_name = tempfile.mkstemp(
        prefix=f".env.bootstrap-worker-release.{timestamp}.", suffix=".bak", dir=env_file.parent
    )
    os.close(backup_fd)
    backup = pathlib.Path(backup_name)
    shutil.copy2(env_file, backup)
    newline = "\n" if lines[index].endswith("\n") else ""
    lines[index] = f"OPSI_BOOTSTRAP_WORKER_IMAGE={target}{newline}"
    descriptor, temporary = tempfile.mkstemp(prefix=".env.bootstrap-worker-release.", dir=env_file.parent)
    try:
        os.fchmod(descriptor, stat.S_IMODE(env_file.stat().st_mode))
        with os.fdopen(descriptor, "w", encoding="utf-8") as handle:
            handle.writelines(lines)
            handle.flush()
            os.fsync(handle.fileno())
        os.replace(temporary, env_file)
        directory_fd = os.open(env_file.parent, os.O_RDONLY)
        try:
            os.fsync(directory_fd)
        finally:
            os.close(directory_fd)
    except Exception:
        try:
            os.unlink(temporary)
        except FileNotFoundError:
            pass
        raise
    return backup


def wait_for_health(compose: list[str], timeout: int) -> None:
    if timeout <= 0:
        raise ReleaseError("health timeout must be positive")
    deadline = time.monotonic() + timeout
    while True:
        container = run(compose + ["ps", "-q", SERVICE])
        status = run(
            [
                "docker",
                "inspect",
                "--format",
                "{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}",
                container,
            ]
        )
        if status == "healthy":
            return
        if status == "unhealthy" or time.monotonic() >= deadline:
            raise ReleaseError(f"Bootstrap Worker did not become healthy (status={status})")
        time.sleep(min(2, max(0, deadline - time.monotonic())))


def release_target(args: argparse.Namespace, target: str, action: str) -> None:
    target = require_image_reference(target)
    expected = f"{IMAGE_REPOSITORY}@{require_digest(args.expected_current_digest)}"
    compose = compose_prefix(args)
    lock_path = args._directory / ".env.bootstrap-worker-release.lock"
    lock_fd = os.open(lock_path, os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW, 0o600)
    try:
        os.fchmod(lock_fd, 0o600)
        try:
            fcntl.flock(lock_fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError as exc:
            raise ReleaseError("another Bootstrap Worker release operation is active") from exc
        release_target_locked(args, target, action, expected, compose)
    finally:
        os.close(lock_fd)


def quiesce_target(args: argparse.Namespace) -> None:
    compose = compose_prefix(args)
    lock_path = args._directory / ".env.bootstrap-worker-release.lock"
    lock_fd = os.open(lock_path, os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW, 0o600)
    try:
        os.fchmod(lock_fd, 0o600)
        try:
            fcntl.flock(lock_fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError as exc:
            raise ReleaseError("another Bootstrap Worker release operation is active") from exc
        before = current_container_id(compose)
        existing = current_container_id(compose, include_stopped=True)
        if before != existing:
            raise ReleaseError("expected exactly one running Bootstrap Worker before quiesce")
        expected = f"{IMAGE_REPOSITORY}@{require_digest(args.expected_current_digest)}"
        if current_image_reference(compose, before) != expected:
            raise ReleaseError("running Worker digest does not match expected quiesce digest")
        binding, _, _ = read_binding(args._env_file)
        if binding != expected:
            raise ReleaseError("runtime image binding does not match expected quiesce digest")
        run(compose + ["stop", SERVICE])
        if run(compose + ["ps", "-q", SERVICE]):
            raise ReleaseError("Bootstrap Worker remained running after quiesce")
        if current_container_id(compose, include_stopped=True) != before:
            raise ReleaseError("Bootstrap Worker container identity changed during quiesce")
        print(f"quiesced_container_id={before}")
    finally:
        os.close(lock_fd)


def release_target_locked(
    args: argparse.Namespace, target: str, action: str, expected: str, compose: list[str]
) -> None:
    force_same = bool(getattr(args, "force_recreate_same_image", False)) or args.command in {"barrier-replay", "barrier-restore"}
    before_container = current_container_id(compose, include_stopped=force_same)
    running = current_image_reference(compose, before_container)
    binding, lines, index = read_binding(args._env_file)
    if running != expected:
        raise ReleaseError(f"running Worker digest mismatch: expected {expected}, found {running}")
    if binding != expected:
        raise ReleaseError(f"runtime image binding mismatch: expected {expected}, found {binding}")
    if force_same and action not in {"deploy", "barrier-replay", "barrier-restore"}:
        raise ReleaseError("same-image barrier recreation is deploy-only")
    if force_same and target != expected:
        raise ReleaseError("same-image barrier recreation requires target digest to equal expected current digest")
    if force_same:
        if args.command == "barrier-replay":
            validate_same_image_barrier(args, "reached", args.barrier_session_id, args.barrier_run_id)
        elif args.command == "barrier-restore":
            if args.compose_file:
                raise ReleaseError("normal Worker restoration must not use a barrier compose override")
        else:
            validate_same_image_barrier(args)
    print(f"rollback_target={expected}")
    if target == expected:
        if force_same:
            run(compose + ["up", "-d", "--no-deps", "--force-recreate", SERVICE])
            after_container = current_container_id(compose)
            if after_container == before_container:
                raise ReleaseError("same-image barrier recreation did not replace the Worker container")
            wait_for_health(compose, args.health_timeout)
            actual = current_image_reference(compose, after_container)
            if actual != target:
                raise ReleaseError(f"running Worker digest mismatch after barrier recreation: expected {target}, found {actual}")
            run(["curl", "--fail", "--silent", "--show-error", "--max-time", "15", CLOUD_HEALTH_URL])
            print(f"previous_container_id={before_container}")
            print(f"container_id={after_container}")
            result = {
                "barrier-replay": "barrier-replay-recreated",
                "barrier-restore": "normal-same-image-restored",
            }.get(args.command, "same-image-barrier-recreated")
            print(f"result={result}")
            print(f"final_image={target}")
            return
        wait_for_health(compose, args.health_timeout)
        actual = current_image_reference(compose)
        if actual != target:
            raise ReleaseError(f"running Worker digest mismatch after no-op deploy: expected {target}, found {actual}")
        run(["curl", "--fail", "--silent", "--show-error", "--max-time", "15", CLOUD_HEALTH_URL])
        print("result=same-image-no-op")
        print(f"final_image={target}")
        return
    run(["docker", "pull", target])
    backup = update_binding(args._env_file, lines, index, target)
    print(f"binding_backup={backup}")
    try:
        run(compose + ["up", "-d", "--no-deps", "--force-recreate", SERVICE])
        wait_for_health(compose, args.health_timeout)
        actual = current_image_reference(compose)
        if actual != target:
            raise ReleaseError(f"running Worker digest mismatch after {action}: expected {target}, found {actual}")
        run(["curl", "--fail", "--silent", "--show-error", "--max-time", "15", CLOUD_HEALTH_URL])
    except ReleaseError as exc:
        try:
            if current_image_reference(compose) == expected:
                current_binding, current_lines, current_index = read_binding(args._env_file)
                if current_binding != expected:
                    update_binding(args._env_file, current_lines, current_index, expected)
                    print(f"binding_restored={expected}")
        except (OSError, ReleaseError):
            pass
        raise ReleaseError(f"{action} failed; rollback target remains {expected}: {exc}") from exc
    print(f"final_image={target}")


def add_runtime_arguments(parser: argparse.ArgumentParser) -> None:
    parser.add_argument("--expected-current-digest", required=True)
    parser.add_argument("--compose-project", required=True)
    parser.add_argument("--compose-directory", required=True)
    parser.add_argument("--compose-file", action="append", default=[], help="additional override relative to the compose directory")
    parser.add_argument("--service", required=True)
    parser.add_argument("--health-timeout", required=True, type=int)


def add_barrier_identity_arguments(parser: argparse.ArgumentParser) -> None:
    parser.add_argument("--barrier-session-id", required=True)
    parser.add_argument("--barrier-run-id", required=True)


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser()
    commands = parser.add_subparsers(dest="command", required=True)
    manifest = commands.add_parser("manifest")
    manifest.add_argument("--source-revision", required=True)
    manifest.add_argument("--image-digest", required=True)
    manifest.add_argument("--workflow-run-id", required=True)
    manifest.add_argument("--created-at", required=True)
    manifest.add_argument("--output", required=True)
    deploy = commands.add_parser("deploy")
    source = deploy.add_mutually_exclusive_group(required=True)
    source.add_argument("--manifest")
    source.add_argument("--image")
    deploy.add_argument("--force-recreate-same-image", action="store_true")
    add_runtime_arguments(deploy)
    replay = commands.add_parser("barrier-replay")
    add_runtime_arguments(replay)
    add_barrier_identity_arguments(replay)
    restore = commands.add_parser("barrier-restore")
    add_runtime_arguments(restore)
    quiesce = commands.add_parser("barrier-quiesce")
    add_runtime_arguments(quiesce)
    rollback = commands.add_parser("rollback")
    rollback.add_argument("--to", required=True)
    add_runtime_arguments(rollback)
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    if args.command == "manifest":
        create_manifest(args)
    elif args.command == "deploy":
        target = load_manifest(pathlib.Path(args.manifest))["image_reference"] if args.manifest else args.image
        release_target(args, target, "deploy")
    elif args.command == "rollback":
        release_target(args, args.to, "rollback")
    elif args.command == "barrier-replay":
        release_target(args, f"{IMAGE_REPOSITORY}@{require_digest(args.expected_current_digest)}", "barrier-replay")
    elif args.command == "barrier-quiesce":
        quiesce_target(args)
    else:
        release_target(args, f"{IMAGE_REPOSITORY}@{require_digest(args.expected_current_digest)}", "barrier-restore")
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (OSError, ReleaseError) as exc:
        print(f"bootstrap worker release failed: {exc}", file=sys.stderr)
        raise SystemExit(1)
