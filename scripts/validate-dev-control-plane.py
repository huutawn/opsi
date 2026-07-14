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
KNOWN_HOSTS_PATH = DEPLOY / "secrets" / "ssh_known_hosts"
GITHUB_APP_KEY_PATH = DEPLOY / "secrets" / "github-app-private-key.pem"
PLACEHOLDER_PREFIXES = ("REPLACE_WITH_",)
PLACEHOLDER_VALUES = {"CHANGE_ME", "EXAMPLE_SECRET"}
SHA256 = re.compile(r"^[0-9a-f]{64}$")
K3S_VERSION = re.compile(r"^v[0-9]+\.[0-9]+\.[0-9]+\+k3s[0-9]+$")
GITHUB_CALLBACK_PATH = "/v1/auth/browser/callback"
GITHUB_APP_CONTAINER_KEY_PATH = "/run/secrets/github-app-private-key.pem"
LEGACY_CLOUD_AUTH_ENV_PREFIX = "OPSI_CLOUD_" + "AUTH_"

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
    "OPSI_CLOUD_GITHUB_APP_CLIENT_ID",
    "OPSI_CLOUD_GITHUB_APP_CLIENT_SECRET",
    "OPSI_CLOUD_GITHUB_APP_CALLBACK_URL",
    "OPSI_CLOUD_GITHUB_APP_ID",
    "OPSI_CLOUD_GITHUB_APP_PRIVATE_KEY_PATH",
    "OPSI_CLOUD_GITHUB_APP_WEBHOOK_SECRET",
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


def effective_port(parsed) -> int | None:
    if parsed.port is not None:
        return parsed.port
    if parsed.scheme == "https":
        return 443
    if parsed.scheme == "http":
        return 80
    return None


def validate_github_app_config(env: dict[str, str], production: bool, public_base_url: str) -> None:
    client_id = env.get("OPSI_CLOUD_GITHUB_APP_CLIENT_ID", "").strip()
    client_secret = env.get("OPSI_CLOUD_GITHUB_APP_CLIENT_SECRET", "")
    callback_url = env.get("OPSI_CLOUD_GITHUB_APP_CALLBACK_URL", "")

    if any(character.isspace() or ord(character) < 32 or ord(character) == 127 for character in client_id):
        fail("OPSI_CLOUD_GITHUB_APP_CLIENT_ID must not contain whitespace or control characters")
    if any(ord(character) < 32 or ord(character) == 127 for character in client_secret):
        fail("OPSI_CLOUD_GITHUB_APP_CLIENT_SECRET must not contain control characters")
    if bool(client_id) != bool(client_secret):
        fail("OPSI_CLOUD_GITHUB_APP_CLIENT_ID and OPSI_CLOUD_GITHUB_APP_CLIENT_SECRET must be configured together")
    if production and (not client_id or not client_secret or not callback_url):
        fail("production requires GitHub App Client ID, Client Secret, and callback URL")
    if client_id and not callback_url:
        fail("OPSI_CLOUD_GITHUB_APP_CALLBACK_URL is required when GitHub user authorization is enabled")
    if callback_url:
        callback = urlparse(callback_url)
        if not callback.scheme or not callback.hostname:
            fail("OPSI_CLOUD_GITHUB_APP_CALLBACK_URL must be an absolute URL")
        try:
            callback.port
        except ValueError:
            fail("OPSI_CLOUD_GITHUB_APP_CALLBACK_URL contains an invalid port")
        if callback.username or callback.password or "?" in callback_url or "#" in callback_url:
            fail("OPSI_CLOUD_GITHUB_APP_CALLBACK_URL must not contain user info, query, or fragment")
        if callback.path != GITHUB_CALLBACK_PATH:
            fail(f"OPSI_CLOUD_GITHUB_APP_CALLBACK_URL path must be {GITHUB_CALLBACK_PATH}")
        if production:
            if callback.scheme != "https":
                fail("production GitHub App callback URL must use HTTPS")
            public = urlparse(public_base_url)
            try:
                public.port
            except ValueError:
                fail("cloud.public_base_url contains an invalid port")
            if (
                public.scheme != callback.scheme
                or public.hostname != callback.hostname
                or effective_port(public) != effective_port(callback)
            ):
                fail("production GitHub App callback URL must match cloud.public_base_url scheme and host")
        elif callback.scheme == "http" and callback.hostname not in {"127.0.0.1", "localhost"}:
            fail("development GitHub App callback URL must use HTTPS or loopback HTTP")
        elif callback.scheme not in {"http", "https"}:
            fail("GitHub App callback URL must use HTTP(S)")

    app_id = env.get("OPSI_CLOUD_GITHUB_APP_ID", "")
    private_key_path = env.get("OPSI_CLOUD_GITHUB_APP_PRIVATE_KEY_PATH", "")
    webhook_secret = env.get("OPSI_CLOUD_GITHUB_APP_WEBHOOK_SECRET", "")
    installation_fields = (bool(app_id), bool(private_key_path), bool(webhook_secret))
    installation_enabled = any(installation_fields)
    if installation_enabled and not all(installation_fields):
        fail("GitHub App ID, private-key path, and webhook secret must be configured together")
    if production and not installation_enabled:
        fail("production requires GitHub App installation authentication and webhook configuration")
    if not installation_enabled:
        return
    try:
        parsed_app_id = int(app_id, 10)
    except ValueError:
        fail("OPSI_CLOUD_GITHUB_APP_ID must be a positive integer")
    if parsed_app_id <= 0:
        fail("OPSI_CLOUD_GITHUB_APP_ID must be a positive integer")
    if private_key_path != GITHUB_APP_CONTAINER_KEY_PATH:
        fail(f"OPSI_CLOUD_GITHUB_APP_PRIVATE_KEY_PATH must be {GITHUB_APP_CONTAINER_KEY_PATH}")
    require_secret(webhook_secret, "OPSI_CLOUD_GITHUB_APP_WEBHOOK_SECRET")


def validate_permissions(path: pathlib.Path) -> None:
    mode = stat.S_IMODE(path.stat().st_mode)
    if mode & 0o077:
        fail(f"{path.relative_to(ROOT)} must not be readable or writable by group/other (use chmod 0600)")


def validate_known_hosts_file(path: pathlib.Path) -> bool:
    try:
        info = path.lstat()
    except FileNotFoundError:
        fail(f"missing runtime file: {path.relative_to(ROOT)}")
    if stat.S_ISLNK(info.st_mode):
        fail(f"{path.relative_to(ROOT)} must not be a symlink")
    if not stat.S_ISREG(info.st_mode):
        fail(f"{path.relative_to(ROOT)} must be a regular file")
    mode = stat.S_IMODE(info.st_mode)
    if mode & 0o022:
        fail(f"{path.relative_to(ROOT)} must not be group/world writable")
    return info.st_size > 0


def validate_github_app_key_file(path: pathlib.Path, enabled: bool) -> None:
    try:
        info = path.lstat()
    except FileNotFoundError:
        fail(f"missing runtime file: {path.relative_to(ROOT)}")
    if stat.S_ISLNK(info.st_mode):
        fail(f"{path.relative_to(ROOT)} must not be a symlink")
    if not stat.S_ISREG(info.st_mode):
        fail(f"{path.relative_to(ROOT)} must be a regular file")
    if stat.S_IMODE(info.st_mode) & 0o022:
        fail(f"{path.relative_to(ROOT)} must not be group/world writable")
    if stat.S_IMODE(info.st_mode) & 0o015:
        fail(f"{path.relative_to(ROOT)} must not grant group execute or world access")
    if enabled and info.st_size == 0:
        fail(f"{path.relative_to(ROOT)} must not be empty when GitHub App installation integration is enabled")


def main() -> int:
    env_text = read_text(ENV_PATH)
    cloud_text = read_text(CLOUD_PATH)
    worker_text = read_text(WORKER_PATH)
    for path in (ENV_PATH, CLOUD_PATH, WORKER_PATH):
        validate_permissions(path)

    env = parse_env(env_text)
    cloud = parse_json(CLOUD_PATH, cloud_text)
    worker = parse_json(WORKER_PATH, worker_text)

    legacy_names = sorted(name for name in env if name.startswith(LEGACY_CLOUD_AUTH_ENV_PREFIX))
    if legacy_names:
        fail(f"legacy variable {legacy_names[0]} is no longer supported; use OPSI_CLOUD_GITHUB_APP_*")
    legacy_auth = cloud.get("auth")
    if isinstance(legacy_auth, dict) and legacy_auth:
        fail("legacy auth config is no longer supported; use github_app")

    if any(is_placeholder(value) for value in env.values()):
        fail(f"placeholder remains in {ENV_PATH.relative_to(ROOT)}")
    for path, value in ((CLOUD_PATH, cloud), (WORKER_PATH, worker)):
        if contains_placeholder(value):
            fail(f"placeholder remains in {path.relative_to(ROOT)}")

    for name in CLOUD_ENV_NAMES:
        if name not in env:
            fail(f".env is missing {name}")

    cloud_production = parse_bool(env["OPSI_CLOUD_PRODUCTION"], "OPSI_CLOUD_PRODUCTION")
    validate_github_app_config(
        env,
        cloud_production,
        env.get("OPSI_CLOUD_PUBLIC_BASE_URL", ""),
    )
    validate_github_app_key_file(
        GITHUB_APP_KEY_PATH,
        bool(env.get("OPSI_CLOUD_GITHUB_APP_ID", "")),
    )
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

    warning_reasons: list[str] = []
    k3s_version = require_string(worker, "k3s_version", "bootstrap-worker")
    if not K3S_VERSION.fullmatch(k3s_version):
        fail("bootstrap-worker.k3s_version must match vX.Y.Z+k3sN")
    if k3s_version == "v0.0.0+k3s0":
        warning_reasons.append("K3s version is pinned")

    parse_http_url(require_string(worker, "k3s_installer_url", "bootstrap-worker"), "bootstrap-worker.k3s_installer_url")
    k3s_digest = require_string(worker, "k3s_installer_sha256", "bootstrap-worker")
    if not SHA256.fullmatch(k3s_digest):
        fail("bootstrap-worker.k3s_installer_sha256 must be exactly 64 lowercase hexadecimal characters")
    if k3s_digest == "0" * 64:
        warning_reasons.append("K3s installer checksum is real")

    agent_cloud = parse_http_url(require_string(worker, "agent_cloud_url", "bootstrap-worker"), "bootstrap-worker.agent_cloud_url")
    install_url = parse_http_url(require_string(worker, "agent_install_url", "bootstrap-worker"), "bootstrap-worker.agent_install_url")
    agent_artifact_ready = install_url.hostname != "example.invalid"
    digest = require_string(worker, "agent_install_sha256", "bootstrap-worker")
    if not SHA256.fullmatch(digest):
        fail("bootstrap-worker.agent_install_sha256 must be exactly 64 lowercase hexadecimal characters")
    if digest == "0" * 64:
        agent_artifact_ready = False
    if not agent_artifact_ready:
        warning_reasons.append("Agent artifact URL/checksum are real")

    known_hosts_config = require_string(worker, "ssh_known_hosts_path", "bootstrap-worker")
    if known_hosts_config != "/etc/opsi/ssh_known_hosts":
        fail("bootstrap-worker.ssh_known_hosts_path must be /etc/opsi/ssh_known_hosts")
    if not validate_known_hosts_file(KNOWN_HOSTS_PATH):
        warning_reasons.append("known_hosts contains the target host key")

    if agent_cloud.hostname in {"127.0.0.1", "localhost", "::1", "cloud"}:
        warning_reasons.append("agent_cloud_url is reachable from the target")

    if warning_reasons:
        print("warning: remote bootstrap cannot run until:", file=sys.stderr)
        for reason in warning_reasons:
            print(f"- {reason}", file=sys.stderr)

    print("development control-plane configuration is structurally valid")
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except ValidationError as exc:
        print(f"configuration validation failed: {exc}", file=sys.stderr)
        raise SystemExit(1)
