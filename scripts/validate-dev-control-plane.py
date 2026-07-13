#!/usr/bin/env python3
"""Validate the development control-plane runtime configuration without printing secrets."""

from __future__ import annotations

import json
import os
import pathlib
import re
import stat
import sys
from urllib.parse import unquote, urlparse

ROOT = pathlib.Path(__file__).resolve().parents[1]
DEPLOY = ROOT / "deploy" / "dev-control-plane"
ENV_PATH = DEPLOY / ".env"
CLOUD_PATH = DEPLOY / "config" / "cloud.json"
WORKER_PATH = DEPLOY / "config" / "bootstrap-worker.json"
PLACEHOLDER = re.compile(r"REPLACE_WITH_|EXAMPLE_SECRET|CHANGE_ME")
SHA256 = re.compile(r"^[0-9a-fA-F]{64}$")


class ValidationError(Exception):
    pass


def fail(message: str) -> None:
    raise ValidationError(message)


def read_text(path: pathlib.Path) -> str:
    try:
        return path.read_text(encoding="utf-8")
    except FileNotFoundError:
        fail(f"missing runtime file: {path.relative_to(ROOT)}")
    except OSError as exc:
        fail(f"cannot read {path.relative_to(ROOT)}: {exc}")


def parse_env(text: str) -> dict[str, str]:
    values: dict[str, str] = {}
    for number, raw in enumerate(text.splitlines(), 1):
        line = raw.strip()
        if not line or line.startswith("#"):
            continue
        if "=" not in line:
            fail(f"invalid .env line {number}")
        key, value = line.split("=", 1)
        key = key.strip()
        if not key:
            fail(f"empty .env key on line {number}")
        values[key] = value.strip()
    return values


def parse_json(path: pathlib.Path, text: str) -> dict[str, object]:
    try:
        value = json.loads(text)
    except json.JSONDecodeError as exc:
        fail(f"invalid JSON in {path.relative_to(ROOT)} at line {exc.lineno}, column {exc.colno}")
    if not isinstance(value, dict):
        fail(f"top-level JSON must be an object: {path.relative_to(ROOT)}")
    return value


def require_string(mapping: dict[str, object], key: str, source: str) -> str:
    value = mapping.get(key)
    if not isinstance(value, str) or not value.strip():
        fail(f"{source}.{key} must be a non-empty string")
    return value.strip()


def require_secret(value: str, name: str, minimum: int = 32) -> None:
    if len(value.encode("utf-8")) < minimum:
        fail(f"{name} must contain at least {minimum} bytes")


def parse_http_url(raw: str, name: str, https_only: bool = False):
    parsed = urlparse(raw)
    allowed = {"https"} if https_only else {"http", "https"}
    if parsed.scheme not in allowed or not parsed.hostname:
        expected = "HTTPS" if https_only else "HTTP(S)"
        fail(f"{name} must be an absolute {expected} URL")
    if parsed.username or parsed.password or parsed.fragment:
        fail(f"{name} must not contain user info or a fragment")
    return parsed


def validate_permissions(path: pathlib.Path) -> None:
    mode = stat.S_IMODE(path.stat().st_mode)
    if mode & 0o077:
        fail(f"{path.relative_to(ROOT)} must not be readable or writable by group/other (use chmod 0600)")


def main() -> int:
    env_text = read_text(ENV_PATH)
    cloud_text = read_text(CLOUD_PATH)
    worker_text = read_text(WORKER_PATH)
    for path, text in ((ENV_PATH, env_text), (CLOUD_PATH, cloud_text), (WORKER_PATH, worker_text)):
        if PLACEHOLDER.search(text):
            fail(f"placeholder remains in {path.relative_to(ROOT)}")
        validate_permissions(path)

    env = parse_env(env_text)
    cloud = parse_json(CLOUD_PATH, cloud_text)
    worker = parse_json(WORKER_PATH, worker_text)

    if bool(cloud.get("production")) or bool(worker.get("production")):
        fail("deploy/dev-control-plane is an HTTP-only development package; production mode requires a separate HTTPS deployment")

    for name in ("POSTGRES_DB", "POSTGRES_USER", "POSTGRES_PASSWORD", "OPSI_DEV_BIND_ADDRESS", "OPSI_DEV_HTTP_PORT"):
        if not env.get(name):
            fail(f".env is missing {name}")

    if env["OPSI_DEV_BIND_ADDRESS"] != "127.0.0.1":
        fail("the HTTP-only development package must bind to 127.0.0.1; use a separate HTTPS edge for public access")
    try:
        port = int(env["OPSI_DEV_HTTP_PORT"])
    except ValueError:
        fail("OPSI_DEV_HTTP_PORT must be an integer")
    if not 1 <= port <= 65535:
        fail("OPSI_DEV_HTTP_PORT must be between 1 and 65535")

    database_url = require_string(cloud, "database_url", "cloud")
    db = urlparse(database_url)
    if db.scheme not in {"postgres", "postgresql"} or db.hostname != "postgres" or db.port != 5432:
        fail("cloud.database_url must target the Compose postgres service on port 5432")
    if unquote(db.username or "") != env["POSTGRES_USER"]:
        fail("POSTGRES_USER does not match cloud.database_url")
    if unquote(db.password or "") != env["POSTGRES_PASSWORD"]:
        fail("POSTGRES_PASSWORD does not match cloud.database_url")
    if db.path.lstrip("/") != env["POSTGRES_DB"]:
        fail("POSTGRES_DB does not match cloud.database_url")
    require_secret(env["POSTGRES_PASSWORD"], "POSTGRES_PASSWORD")

    public_base_url = require_string(cloud, "public_base_url", "cloud").rstrip("/")
    expected_public_url = f"http://127.0.0.1:{port}"
    if public_base_url != expected_public_url:
        fail(f"cloud.public_base_url must be {expected_public_url} for this development package")

    cloud_worker_token = require_string(cloud, "bootstrap_worker_token", "cloud")
    worker_token = require_string(worker, "bootstrap_worker_token", "bootstrap-worker")
    if cloud_worker_token != worker_token:
        fail("bootstrap_worker_token does not match between Cloud and Bootstrap Worker")
    require_secret(cloud_worker_token, "bootstrap_worker_token")
    require_secret(require_string(cloud, "bootstrap_secret_key", "cloud"), "bootstrap_secret_key")

    alerts = cloud.get("alerts")
    if not isinstance(alerts, dict):
        fail("cloud.alerts must be an object")
    require_secret(require_string(alerts, "internal_token", "cloud.alerts"), "alerts.internal_token")

    internal_cloud = parse_http_url(require_string(worker, "cloud_url", "bootstrap-worker"), "bootstrap-worker.cloud_url")
    if internal_cloud.hostname != "cloud" or internal_cloud.port != 9800:
        fail("bootstrap-worker.cloud_url must target http://cloud:9800 inside Compose")

    agent_cloud = parse_http_url(require_string(worker, "agent_cloud_url", "bootstrap-worker"), "bootstrap-worker.agent_cloud_url")
    install_url = parse_http_url(require_string(worker, "agent_install_url", "bootstrap-worker"), "bootstrap-worker.agent_install_url")
    if install_url.hostname == "example.invalid":
        print(
            "warning: agent_install_url is a placeholder; control-plane smoke tests can run, but remote bootstrap cannot install the Agent",
            file=sys.stderr,
        )
    digest = require_string(worker, "agent_install_sha256", "bootstrap-worker")
    if not SHA256.fullmatch(digest):
        fail("bootstrap-worker.agent_install_sha256 must be exactly 64 hexadecimal characters")

    if agent_cloud.hostname in {"127.0.0.1", "localhost", "::1", "cloud"}:
        print(
            "warning: agent_cloud_url is local/internal; control-plane smoke tests can run, but a remote VPS bootstrap cannot register its Agent",
            file=sys.stderr,
        )

    print("development control-plane configuration is structurally valid")
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except ValidationError as exc:
        print(f"configuration validation failed: {exc}", file=sys.stderr)
        raise SystemExit(1)
