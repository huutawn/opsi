#!/usr/bin/env python3
"""Validate immutable control-plane images and their combined release manifest."""

from __future__ import annotations

import argparse
import datetime as dt
import json
import pathlib
import re
import stat
import sys

SCHEMA = "opsi.control_plane_release.v1"
SOURCE_REPOSITORY = "huutawn/opsi"
SOURCE_URL = "https://github.com/huutawn/opsi"
PLATFORM = "linux/amd64"
COMPONENTS = {
    "cloud": ("ghcr.io/huutawn/opsi-cloud", "cloud"),
    "bootstrap_worker": ("ghcr.io/huutawn/opsi-bootstrap-worker", "bootstrap-worker"),
}
DIGEST_RE = re.compile(r"^sha256:[0-9a-f]{64}$")
REVISION_RE = re.compile(r"^[0-9a-f]{40}$")
CREATED_RE = re.compile(r"^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$")
ROOT_KEYS = {
    "schema_version",
    "source_repository",
    "source_revision",
    "workflow_run_id",
    "created_at",
    "platform",
    "components",
}
COMPONENT_KEYS = {"repository", "target", "image_digest", "image_reference"}
SENSITIVE_KEY_RE = re.compile(r"token|password|credential|secret|authorization|private[_-]?key", re.IGNORECASE)


class ReleaseError(Exception):
    pass


def require_digest(value: object) -> str:
    if not isinstance(value, str) or not DIGEST_RE.fullmatch(value):
        raise ReleaseError("digest must be sha256 followed by 64 lowercase hex characters")
    return value


def require_revision(value: object) -> str:
    if not isinstance(value, str) or not REVISION_RE.fullmatch(value):
        raise ReleaseError("source revision must be a full 40-character lowercase hex commit")
    return value


def require_created_at(value: object) -> str:
    if not isinstance(value, str) or not CREATED_RE.fullmatch(value):
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
            raise ReleaseError(f"JSON contains duplicate field {key}")
        result[key] = value
    return result


def reject_sensitive_keys(value: object) -> None:
    if isinstance(value, dict):
        for key, item in value.items():
            if SENSITIVE_KEY_RE.search(str(key)):
                raise ReleaseError(f"secret-looking field is forbidden: {key}")
            reject_sensitive_keys(item)
    elif isinstance(value, list):
        for item in value:
            reject_sensitive_keys(item)


def require_exact_keys(value: object, expected: set[str], label: str) -> dict[str, object]:
    if not isinstance(value, dict):
        raise ReleaseError(f"{label} must be one JSON object")
    keys = set(value)
    if keys != expected:
        raise ReleaseError(
            f"{label} fields mismatch: missing={sorted(expected - keys)}, unknown={sorted(keys - expected)}"
        )
    return value


def validate_manifest(value: object) -> dict[str, object]:
    reject_sensitive_keys(value)
    manifest = require_exact_keys(value, ROOT_KEYS, "release manifest")
    if manifest["schema_version"] != SCHEMA:
        raise ReleaseError("unsupported release manifest schema")
    if manifest["source_repository"] != SOURCE_REPOSITORY:
        raise ReleaseError("release manifest source repository mismatch")
    require_revision(manifest["source_revision"])
    if not isinstance(manifest["workflow_run_id"], str) or not manifest["workflow_run_id"].isdigit():
        raise ReleaseError("workflow_run_id must be a decimal GitHub run ID")
    require_created_at(manifest["created_at"])
    if manifest["platform"] != PLATFORM:
        raise ReleaseError(f"release manifest platform must be {PLATFORM}")

    components = require_exact_keys(manifest["components"], set(COMPONENTS), "release components")
    for name, (repository, target) in COMPONENTS.items():
        component = require_exact_keys(components[name], COMPONENT_KEYS, f"{name} component")
        if component["repository"] != repository or component["target"] != target:
            raise ReleaseError(f"{name} repository or target mismatch")
        digest = require_digest(component["image_digest"])
        if component["image_reference"] != f"{repository}@{digest}":
            raise ReleaseError(f"{name} image reference must use its canonical repository and digest")
    return manifest


def load_json(path: pathlib.Path, label: str, maximum: int = 65536) -> object:
    try:
        info = path.lstat()
    except OSError as exc:
        raise ReleaseError(f"{label} is unavailable: {exc}") from exc
    if not stat.S_ISREG(info.st_mode) or stat.S_ISLNK(info.st_mode) or not 0 < info.st_size <= maximum:
        raise ReleaseError(f"{label} must be a regular non-symlink file no larger than {maximum} bytes")
    try:
        return json.loads(path.read_text(encoding="utf-8"), object_pairs_hook=no_duplicate_object)
    except (OSError, UnicodeError, json.JSONDecodeError) as exc:
        raise ReleaseError(f"cannot parse {label}: {exc}") from exc


def load_manifest(path: pathlib.Path) -> dict[str, object]:
    return validate_manifest(load_json(path, "release manifest"))


def component_values(manifest: dict[str, object], name: str) -> dict[str, object]:
    if name not in COMPONENTS:
        raise ReleaseError(f"unknown component: {name}")
    return manifest["components"][name]  # type: ignore[index,return-value]


def create_manifest(args: argparse.Namespace) -> None:
    manifest = validate_manifest(
        {
            "schema_version": SCHEMA,
            "source_repository": SOURCE_REPOSITORY,
            "source_revision": args.source_revision,
            "workflow_run_id": args.workflow_run_id,
            "created_at": args.created_at,
            "platform": PLATFORM,
            "components": {
                name: {
                    "repository": repository,
                    "target": target,
                    "image_digest": getattr(args, f"{name}_digest"),
                    "image_reference": f"{repository}@{getattr(args, f'{name}_digest')}",
                }
                for name, (repository, target) in COMPONENTS.items()
            },
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


def verify_image_inspect(value: object, component_name: str, revision: str, image_tag: str) -> str:
    revision = require_revision(revision)
    if component_name not in COMPONENTS:
        raise ReleaseError(f"unknown component: {component_name}")
    repository, target = COMPONENTS[component_name]
    expected_tag = f"{repository}:{revision}"
    if image_tag != expected_tag:
        raise ReleaseError(f"image tag must be {expected_tag}")
    if not isinstance(value, list) or len(value) != 1 or not isinstance(value[0], dict):
        raise ReleaseError("docker inspect must contain exactly one image")
    image = value[0]
    if image.get("Os") != "linux" or image.get("Architecture") != "amd64":
        raise ReleaseError("image platform must be linux/amd64")
    tags = image.get("RepoTags")
    if not isinstance(tags, list) or expected_tag not in tags:
        raise ReleaseError("image does not carry the expected immutable revision tag")
    digests = image.get("RepoDigests")
    if not isinstance(digests, list):
        raise ReleaseError("image repository digests are missing")
    matches = [item[len(repository) + 1 :] for item in digests if isinstance(item, str) and item.startswith(repository + "@")]
    if len(matches) != 1:
        raise ReleaseError("image must resolve to one canonical repository digest")
    digest = require_digest(matches[0])
    config = image.get("Config")
    labels = config.get("Labels") if isinstance(config, dict) else None
    if not isinstance(labels, dict):
        raise ReleaseError("image labels are missing")
    expected_labels = {
        "org.opencontainers.image.source": SOURCE_URL,
        "org.opencontainers.image.revision": revision,
        "org.opencontainers.image.version": revision,
        "io.opsi.control-plane.component": component_name,
        "io.opsi.control-plane.target": target,
    }
    for key, expected in expected_labels.items():
        if labels.get(key) != expected:
            raise ReleaseError(f"image label mismatch: {key}")
    require_created_at(labels.get("org.opencontainers.image.created"))
    return digest


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser()
    commands = parser.add_subparsers(dest="command", required=True)
    manifest = commands.add_parser("manifest")
    manifest.add_argument("--source-revision", required=True)
    manifest.add_argument("--workflow-run-id", required=True)
    manifest.add_argument("--created-at", required=True)
    manifest.add_argument("--cloud-digest", required=True)
    manifest.add_argument("--bootstrap-worker-digest", dest="bootstrap_worker_digest", required=True)
    manifest.add_argument("--output", required=True)
    validate = commands.add_parser("validate")
    validate.add_argument("--manifest", required=True)
    extract = commands.add_parser("extract")
    extract.add_argument("--manifest", required=True)
    extract.add_argument("--component", choices=COMPONENTS, required=True)
    extract.add_argument("--field", choices=("image_digest", "image_reference"), required=True)
    verify = commands.add_parser("verify-image")
    verify.add_argument("--component", choices=COMPONENTS, required=True)
    verify.add_argument("--source-revision", required=True)
    verify.add_argument("--image-tag", required=True)
    verify.add_argument("--inspect-json", required=True)
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    if args.command == "manifest":
        create_manifest(args)
    elif args.command == "validate":
        load_manifest(pathlib.Path(args.manifest))
    elif args.command == "extract":
        print(component_values(load_manifest(pathlib.Path(args.manifest)), args.component)[args.field])
    else:
        value = load_json(pathlib.Path(args.inspect_json), "docker image inspection", 1024 * 1024)
        print(verify_image_inspect(value, args.component, args.source_revision, args.image_tag))
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (OSError, ReleaseError) as exc:
        print(f"control plane release failed: {exc}", file=sys.stderr)
        raise SystemExit(1)
