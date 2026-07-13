#!/usr/bin/env python3
"""Validate the development control-plane runtime configuration without printing secrets."""

from __future__ import annotations

import json
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
PLACEHOLDER_PREFIXES = ("REPLACE_WITH_",)
PLACEHOLDER_VALUES = {"CHANGE_ME", "EXAMPLE_SECRET"}
SHA256 = re.compile(r"^[0-9a-fA-F]{64}$")

CLOUD_ENV_NAMES = (
    "OPSI_CLOUD_TTL",
    "OPSI_CLOUD_DATABASE_URL",
    "OPSI_CLOUD_PUBLIC_BASE_URL",
    "OPSI_CLOUD_PRODUCTION",
    "OPSI_CLOUD_ENABLE_DEBUG_UI",
    "OPSI_CLOUD_REQUIRE_AGENT_SIGNATURES",
    "OPSI_CLOUD_OTP_DEV_ECHO",
    "OPSI_CLOUD_OTP_OUTBOX_PATH",
    "OPSI_CLOUD_SMTP_HOST",
    "OPSI_CLOUD_SMTP_PORT",
    "OPSI_CLOUD_SMTP_USERNAME",
    "OPSI_CLOUD_SMTP_PASSWORD",
    "OPSI_CLOUD_SMTP_FROM",
    "OPSI_CLOUD_ALERTS_WEBHOOK_URL",
    "OPSI_CLOUD_ALERTS_MIN_SEVERITY",
    "OPSI_CLOUD_ALERTS_OUTBOX_PATH",
    "OPSI_CLOUD_ALERTS_INTERNAL_TOKEN",
    "OPSI_CLOUD_BOOTSTRAP_WORKER_TOKEN",
    "OPSI_CLOUD_BOOTSTRAP_SECRET_KEY",
    "OPSI_CLOUD_AUTH_PROVIDER",
    "OPSI_CLOUD_AUTH_CLIENT_ID",
    "OPSI_CLOUD_AUTH_CLIENT_SECRET",
    "OPSI_CLOUD_AUTH_AUTH_URL",
    "OPSI_CLOUD_AUTH_TOKEN_URL",
    "OPSI_CLOUD_AUTH_USERINFO_URL",
    "OPSI_CLOUD_AUTH_REDIRECT_URL",
    "OPSI_CLOUD_AUTH_SCOPES",
)


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


def is_placeholder(value: str) -> bool:
    candidate = value.strip()
    parts = re.split(r"[/@:?#=&]", candidate)
    return any(
        part in PLACEHOLDER_VALUES or part.startswith(PLACEHOLDER_PREFIXES)
        for part in parts
    )


def contains_placeholder(value: object) -> bool:
    if isinstance(value, str):
        return is_placeholder(value)
    if isinstance(value, list):
        return any(contains_placeholder(item) for item in value)
    if isinstance(value, dict):
        return any(contains_placeholder(item) for item in value.values())
    return False


def parse_bool(value: str, name: str) -> bool:
    if value in {"1", "t", "T", "true", "TRUE", "True"}:
        return True
    if value in {"0", "f", "F", "false", "FALSE", "False"}:
        return False
    fail(f"{name} must be a valid boolean")


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
    for path in (ENV_PATH, CLOUD_PATH, WORKER_PATH):
        validate_permissions(path)

    env = parse_env(env_text)
    cloud = parse_json(CLOUD_PATH, cloud_text)
    worker = parse_json(WORKER_PATH, worker_text)

    if any(is_placeholder(value) for value in env.values()):
        fail(f"placeholder remains in {ENV_PATH.relative_to(ROOT)}")
    for path, value in ((CLOUD_PATH, cloud), (WORKER_PATH, worker)):
        if contains_placeholder(value):
            fail(f"placeholder remains in {path.relative_to(ROOT)}")

    for name in CLOUD_ENV_NAMES:
        if name not in env:
            fail(f".env is missing {name}")

    cloud_production = parse_bool(env["OPSI_CLOUD_PRODUCTION"], "OPSI_CLOUD_PRODUCTION")
    worker_production = worker.get("production", False)
    if not isinstance(worker_production, bool):
        fail("bootstrap-worker.production must be a boolean")
    if cloud_production or worker_production:
        fail("deploy/dev-control-plane is an HTTP-only development package; production mode requires a separate HTTPS deployment")

    for name in (
        "POSTGRES_DB",
        "POSTGRES_USER",
        "POSTGRES_PASSWORD",
        "OPSI_DEV_BIND_ADDRESS",
        "OPSI_DEV_HTTP_PORT",
        "OPSI_CLOUD_DATABASE_URL",
        "OPSI_CLOUD_PUBLIC_BASE_URL",
        "OPSI_CLOUD_BOOTSTRAP_WORKER_TOKEN",
        "OPSI_CLOUD_BOOTSTRAP_SECRET_KEY",
        "OPSI_CLOUD_ALERTS_INTERNAL_TOKEN",
    ):
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

    database_url = env.get("OPSI_CLOUD_DATABASE_URL", "")
    if not database_url:
        fail(".env is missing OPSI_CLOUD_DATABASE_URL")
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

    public_base_url = env.get("OPSI_CLOUD_PUBLIC_BASE_URL", "").rstrip("/")
    if not public_base_url:
        fail(".env is missing OPSI_CLOUD_PUBLIC_BASE_URL")
    expected_public_url = f"http://127.0.0.1:{port}"
    if public_base_url != expected_public_url:
        fail(f"cloud.public_base_url must be {expected_public_url} for this development package")

    cloud_worker_token = env.get("OPSI_CLOUD_BOOTSTRAP_WORKER_TOKEN", "")
    if not cloud_worker_token:
        fail(".env is missing OPSI_CLOUD_BOOTSTRAP_WORKER_TOKEN")
    worker_token = require_string(worker, "bootstrap_worker_token", "bootstrap-worker")
    if cloud_worker_token != worker_token:
        fail("bootstrap_worker_token does not match between Cloud and Bootstrap Worker")
    require_secret(cloud_worker_token, "bootstrap_worker_token")
    require_secret(
        env.get("OPSI_CLOUD_BOOTSTRAP_SECRET_KEY", ""),
        "OPSI_CLOUD_BOOTSTRAP_SECRET_KEY",
    )
    require_secret(
        env.get("OPSI_CLOUD_ALERTS_INTERNAL_TOKEN", ""),
        "OPSI_CLOUD_ALERTS_INTERNAL_TOKEN",
    )

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
