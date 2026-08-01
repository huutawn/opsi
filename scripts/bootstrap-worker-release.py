#!/usr/bin/env python3
"""Create Bootstrap Worker release manifests and deploy one staging Worker."""

from __future__ import annotations

import argparse
import datetime as dt
import fcntl
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
DIGEST_RE = re.compile(r"^sha256:[0-9a-f]{64}$")
REVISION_RE = re.compile(r"^[0-9a-f]{40}$")
CREATED_RE = re.compile(r"^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$")
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


def current_image_reference(compose: list[str]) -> str:
    container = run(compose + ["ps", "-q", SERVICE])
    if not container or "\n" in container:
        raise ReleaseError("expected exactly one running Bootstrap Worker container")
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


def release_target_locked(
    args: argparse.Namespace, target: str, action: str, expected: str, compose: list[str]
) -> None:
    running = current_image_reference(compose)
    binding, lines, index = read_binding(args._env_file)
    if running != expected:
        raise ReleaseError(f"running Worker digest mismatch: expected {expected}, found {running}")
    if binding != expected:
        raise ReleaseError(f"runtime image binding mismatch: expected {expected}, found {binding}")
    print(f"rollback_target={expected}")
    if target == expected:
        wait_for_health(compose, args.health_timeout)
        run(["curl", "--fail", "--silent", "--show-error", "--max-time", "15", CLOUD_HEALTH_URL])
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
    add_runtime_arguments(deploy)
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
    else:
        release_target(args, args.to, "rollback")
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (OSError, ReleaseError) as exc:
        print(f"bootstrap worker release failed: {exc}", file=sys.stderr)
        raise SystemExit(1)
