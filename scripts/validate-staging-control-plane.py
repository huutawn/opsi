#!/usr/bin/env python3
"""Validate the production-like staging control-plane without printing secrets."""

from __future__ import annotations

import argparse
import json
import pathlib
import re
import stat
import sys
from urllib.parse import urlparse

ROOT = pathlib.Path(__file__).resolve().parents[1]
DEPLOY = ROOT / "deploy" / "staging-control-plane"
CALLBACK_PATH = "/v1/auth/browser/callback"
SHA256 = re.compile(r"^[0-9a-f]{64}$")
PLACEHOLDER_MARKERS = ("REPLACE_WITH_", "CHANGE_ME", "EXAMPLE_SECRET")
PRIVATE_KEY_MARKER = "PRIVATE" + " KEY"

REQUIRED_FILES = (
    "compose.yaml",
    "Caddyfile",
    ".env.example",
    "config/cloud.example.json",
    "config/bootstrap-worker.example.json",
)
REQUIRED_SECRET_NAMES = (
    "postgres-password",
    "database-url",
    "smtp-password",
    "alerts-internal-token",
    "bootstrap-worker-token",
    "bootstrap-secret-key",
    "github-app-client-secret",
    "github-app-private-key",
    "github-app-webhook-secret",
    "ssh-known-hosts",
    "origin-certificate",
    "origin-private-key",
)
SECRET_FILE_NAMES = {
    "postgres-password": "postgres-password",
    "database-url": "database-url",
    "smtp-password": "smtp-password",
    "alerts-internal-token": "alerts-internal-token",
    "bootstrap-worker-token": "bootstrap-worker-token",
    "bootstrap-secret-key": "bootstrap-secret-key",
    "github-app-client-secret": "github-app-client-secret",
    "github-app-private-key": "github-app-private-key.pem",
    "github-app-webhook-secret": "github-app-webhook-secret",
    "ssh-known-hosts": "ssh_known_hosts",
    "origin-certificate": "origin-certificate.pem",
    "origin-private-key": "origin-private-key.pem",
}


class ValidationError(Exception):
    pass


def fail(message: str) -> None:
    raise ValidationError(message)


def parse_env(text: str) -> dict[str, str]:
    values: dict[str, str] = {}
    for number, raw in enumerate(text.splitlines(), 1):
        line = raw.strip()
        if not line or line.startswith("#"):
            continue
        if "=" not in line:
            fail(f"invalid environment line {number}")
        name, value = line.split("=", 1)
        values[name.strip()] = value.strip()
    return values


def parse_json(text: str, name: str) -> dict[str, object]:
    try:
        value = json.loads(text)
    except json.JSONDecodeError as exc:
        fail(f"invalid JSON in {name} at line {exc.lineno}, column {exc.colno}")
    if not isinstance(value, dict):
        fail(f"{name} must contain a JSON object")
    return value


def parse_bool(value: str, name: str) -> bool:
    if value.lower() == "true":
        return True
    if value.lower() == "false":
        return False
    fail(f"{name} must be true or false")


def is_placeholder(value: str) -> bool:
    upper = value.strip().upper()
    return (
        any(marker in upper for marker in PLACEHOLDER_MARKERS)
        or "example.invalid" in value.lower()
        or (len(value.strip()) == 64 and not value.strip().strip("0"))
    )


def effective_port(parsed) -> int | None:
    if parsed.port is not None:
        return parsed.port
    return 443 if parsed.scheme == "https" else 80 if parsed.scheme == "http" else None


def public_path_denied(raw_target: str) -> bool:
    raw_path = raw_target.split("?", 1)[0]
    if re.search(r"(?i)%[0-9a-f]{2}", raw_path):
        return True
    normalized = "/" + "/".join(part for part in raw_path.split("/") if part not in {"", "."})
    while "/../" in normalized:
        normalized = re.sub(r"/[^/]+/\.\./", "/", normalized, count=1)
    normalized = normalized.rstrip("/") or "/"
    return normalized == "/metrics" or normalized == "/internal" or normalized.startswith("/internal/") or normalized == "/api/internal" or normalized.startswith("/api/internal/")


def require_https(raw: str, name: str, allow_placeholder: bool) -> object:
    parsed = urlparse(raw)
    if parsed.scheme != "https" or not parsed.hostname or parsed.username or parsed.password or parsed.fragment:
        fail(f"{name} must be an absolute HTTPS URL without user info or fragment")
    if not allow_placeholder and is_placeholder(raw):
        fail(f"{name} must not use a placeholder")
    return parsed


def service_block(compose: str, service: str) -> str:
    match = re.search(rf"(?ms)^  {re.escape(service)}:\n(.*?)(?=^  [a-zA-Z0-9_-]+:\n|^[a-zA-Z0-9_-]+:\n|\Z)", compose)
    if not match:
        fail(f"compose service {service} is missing")
    return match.group(1)


def require_hardening(block: str, service: str, require_user: bool) -> None:
    checks = {
        "read_only filesystem": r"(?m)^    (?:read_only: true|<<: \*service-hardening)$",
        "tmpfs": r"(?m)^    (?:tmpfs:|<<: \*service-hardening)$",
        "no-new-privileges": r"no-new-privileges:true|<<: \*service-hardening",
        "capability drop": r"(?m)^    (?:cap_drop:|<<: \*service-hardening)$",
        "healthcheck": r"(?m)^    healthcheck:$",
        "restart policy": r"(?m)^    (?:restart:|<<: \*service-hardening)",
        "bounded logging": r"(?m)^    (?:logging:|<<: \*service-hardening)",
    }
    if require_user:
        checks["explicit non-root user"] = r'(?m)^    user: "1000:1000"$'
    for label, pattern in checks.items():
        if not re.search(pattern, block):
            fail(f"{service} is missing {label}")


def validate_source_texts(
    env_text: str,
    compose: str,
    caddy: str,
    cloud_text: str,
    worker_text: str,
    dev_compose: str = "",
) -> None:
    env = parse_env(env_text)
    cloud = parse_json(cloud_text, "cloud.example.json")
    worker = parse_json(worker_text, "bootstrap-worker.example.json")
    if not isinstance(cloud.get("routes"), list):
        fail("cloud example must define routes as an array")

    expected_flags = {
        "OPSI_CLOUD_PRODUCTION": True,
        "OPSI_CLOUD_ENABLE_DEBUG_UI": False,
        "OPSI_CLOUD_REQUIRE_AGENT_SIGNATURES": True,
        "OPSI_CLOUD_OTP_DEV_ECHO": False,
    }
    for name, expected in expected_flags.items():
        if name not in env or parse_bool(env[name], name) is not expected:
            fail(f"staging requires {name}={str(expected).lower()}")

    public = require_https(env.get("OPSI_CLOUD_PUBLIC_BASE_URL", ""), "OPSI_CLOUD_PUBLIC_BASE_URL", True)
    callback = require_https(env.get("OPSI_CLOUD_GITHUB_APP_CALLBACK_URL", ""), "OPSI_CLOUD_GITHUB_APP_CALLBACK_URL", True)
    if callback.path != CALLBACK_PATH or callback.query:
        fail(f"OPSI_CLOUD_GITHUB_APP_CALLBACK_URL path must be {CALLBACK_PATH} without query")
    if (callback.hostname, effective_port(callback)) != (public.hostname, effective_port(public)):
        fail("GitHub callback scheme and host must match the public base URL")

    if env.get("COMPOSE_PROJECT_NAME") != "opsi-staging":
        fail("staging must use COMPOSE_PROJECT_NAME=opsi-staging")
    if "dev-control-plane" in compose or "opsi-dev" in compose:
        fail("staging compose must not reference the development profile")
    if "staging-control-plane" in dev_compose or "opsi-staging" in dev_compose:
        fail("development compose must not reference the staging profile")

    for image_name in ("OPSI_CLOUD_IMAGE", "OPSI_BOOTSTRAP_WORKER_IMAGE"):
        image = env.get(image_name, "")
        if not image:
            fail(f"{image_name} is required")
        if image.lower().endswith(":latest") or ":latest@" in image.lower():
            fail(f"{image_name} must not use latest")
        if not is_placeholder(image) and "@sha256:" not in image and not re.search(r":[^/]+$", image):
            fail(f"{image_name} must use a pinned version or digest")
    for image in re.findall(r"(?m)^    image:\s+(.+)$", compose):
        if image.strip().lower().endswith(":latest"):
            fail("compose images must not use latest")

    blocks = {name: service_block(compose, name) for name in ("postgres", "cloud", "bootstrap-worker", "reverse-proxy")}
    for name in ("postgres", "cloud", "bootstrap-worker"):
        if re.search(r"(?m)^    ports:$", blocks[name]):
            fail(f"{name} must not publish a host port")
    if not re.search(r"(?m)^    ports:$", blocks["reverse-proxy"]):
        fail("only reverse-proxy may publish public ports")
    for name in ("postgres", "cloud", "bootstrap-worker", "reverse-proxy"):
        require_hardening(blocks[name], name, name in {"cloud", "bootstrap-worker", "reverse-proxy"})
    if "NET_BIND_SERVICE" not in blocks["reverse-proxy"]:
        fail("reverse-proxy must retain only the official Caddy binary NET_BIND_SERVICE capability")

    for secret in REQUIRED_SECRET_NAMES:
        if not re.search(rf"(?m)^  {re.escape(secret)}:$", compose):
            fail(f"top-level secret {secret} is missing")
    if ":rw" in compose:
        fail("private-key mounts must be read-only")
    for service, required in {
        "cloud": ("database-url", "bootstrap-worker-token", "bootstrap-secret-key", "github-app-private-key"),
        "bootstrap-worker": ("bootstrap-worker-token", "ssh-known-hosts"),
        "reverse-proxy": ("origin-certificate", "origin-private-key"),
    }.items():
        for secret in required:
            if secret not in blocks[service]:
                fail(f"{service} must mount {secret} as an individual Compose secret")

    for token in (
        ":8443",
        "tls /run/secrets/origin-certificate.pem /run/secrets/origin-private-key.pem",
        "/internal",
        "/internal/*",
        "/api/internal",
        "/api/internal/*",
        "/metrics",
        "/metrics/*",
        "%[0-9a-f]{2}",
        "respond @internal 404",
        "respond @encoded_path 404",
    ):
        if token not in caddy:
            fail(f"Caddy public route protection is missing {token}")
    for target in (
        "/internal",
        "/internal/",
        "/internal/bootstrap-worker/lease?wait=1",
        "/internal/alerts/deliver",
        "/api/internal",
        "/api/internal/worker/",
        "/metrics",
        "/metrics/",
        "/%69nternal/bootstrap-worker/lease",
        "/api/%69nternal/alerts",
    ):
        if not public_path_denied(target):
            fail(f"public route policy does not deny {target}")
    if ":8080" not in caddy or "remote_ip 127.0.0.1 ::1" not in caddy or "redir https://{host}{uri} 308" not in caddy:
        fail("Caddy HTTP listener must provide health and controlled HTTPS redirect")

    if worker.get("production") is not True:
        fail("staging bootstrap-worker production must be true")
    if worker.get("cloud_url") != "http://cloud:9800":
        fail("staging worker must use the isolated cloud:9800 backend endpoint")
    if worker.get("bootstrap_worker_token_file") != "/run/secrets/bootstrap-worker-token":
        fail("staging worker token must come from its read-only secret file")
    if "bootstrap_worker_token" in worker:
        fail("staging worker config must not contain an inline worker token")


def validate_runtime_values(env: dict[str, str], worker: dict[str, object], secrets: dict[str, str]) -> None:
    for name, expected in {
        "OPSI_CLOUD_PRODUCTION": True,
        "OPSI_CLOUD_ENABLE_DEBUG_UI": False,
        "OPSI_CLOUD_REQUIRE_AGENT_SIGNATURES": True,
        "OPSI_CLOUD_OTP_DEV_ECHO": False,
    }.items():
        if parse_bool(env.get(name, ""), name) is not expected:
            fail(f"runtime staging requires {name}={str(expected).lower()}")
    public = require_https(env.get("OPSI_CLOUD_PUBLIC_BASE_URL", ""), "OPSI_CLOUD_PUBLIC_BASE_URL", False)
    callback = require_https(env.get("OPSI_CLOUD_GITHUB_APP_CALLBACK_URL", ""), "OPSI_CLOUD_GITHUB_APP_CALLBACK_URL", False)
    if callback.path != CALLBACK_PATH or callback.query or (callback.hostname, effective_port(callback)) != (public.hostname, effective_port(public)):
        fail("runtime GitHub callback must match the HTTPS public origin and callback path")
    for image_name in ("OPSI_CLOUD_IMAGE", "OPSI_BOOTSTRAP_WORKER_IMAGE"):
        image = env.get(image_name, "")
        if is_placeholder(image) or image.lower().endswith(":latest"):
            fail(f"runtime {image_name} must be immutable and non-placeholder")
        if "@sha256:" not in image and not re.search(r":[^/]+$", image):
            fail(f"runtime {image_name} must use a pinned version or digest")

    if worker.get("production") is not True:
        fail("runtime bootstrap-worker production must be true")
    require_https(str(worker.get("agent_cloud_url", "")), "bootstrap-worker.agent_cloud_url", False)
    if worker.get("cloud_url") != "http://cloud:9800":
        fail("runtime bootstrap-worker.cloud_url must use the isolated backend")
    if worker.get("bootstrap_worker_token_file") != "/run/secrets/bootstrap-worker-token":
        fail("runtime worker token must be file-backed")
    for name in ("k3s_installer_url", "agent_install_url"):
        require_https(str(worker.get(name, "")), f"bootstrap-worker.{name}", False)
    for name in ("k3s_installer_sha256", "agent_install_sha256"):
        digest = str(worker.get(name, ""))
        if not SHA256.fullmatch(digest) or not digest.strip("0") or is_placeholder(digest):
            fail(f"bootstrap-worker.{name} must be a non-zero SHA-256 digest")

    for name in REQUIRED_SECRET_NAMES:
        if name not in secrets or not secrets[name]:
            fail(f"runtime secret {name} is missing or empty")
    for name in (
        "postgres-password",
        "alerts-internal-token",
        "bootstrap-worker-token",
        "bootstrap-secret-key",
        "github-app-client-secret",
        "github-app-webhook-secret",
    ):
        value = secrets[name].rstrip("\r\n")
        if len(value.encode()) < 32 or is_placeholder(value):
            fail(f"runtime secret {name} must be at least 32 bytes and non-placeholder")
    smtp_password = secrets["smtp-password"].rstrip("\r\n")
    if not smtp_password or is_placeholder(smtp_password):
        fail("runtime secret smtp-password must be non-empty and non-placeholder")
    database_url = secrets["database-url"].rstrip("\r\n")
    database = urlparse(database_url)
    if database.scheme not in {"postgres", "postgresql"} or database.hostname != "postgres" or is_placeholder(database_url):
        fail("runtime database-url must target the Compose postgres service and be non-placeholder")
    if "BEGIN CERTIFICATE" not in secrets["origin-certificate"]:
        fail("runtime origin certificate is not PEM certificate data")
    for name in ("origin-private-key", "github-app-private-key"):
        if PRIVATE_KEY_MARKER not in secrets[name] or is_placeholder(secrets[name]):
            fail(f"runtime {name} is not private-key PEM data")
    if not secrets["ssh-known-hosts"].strip():
        fail("runtime ssh-known-hosts must not be empty")


def validate_permissions(path: pathlib.Path, private: bool = False, owner_uid: int | None = None) -> None:
    info = path.lstat()
    if stat.S_ISLNK(info.st_mode) or not stat.S_ISREG(info.st_mode):
        fail(f"{path.relative_to(ROOT)} must be a regular non-symlink file")
    mode = stat.S_IMODE(info.st_mode)
    if mode & 0o022:
        fail(f"{path.relative_to(ROOT)} must not be group/world writable")
    if private and mode & 0o077:
        fail(f"{path.relative_to(ROOT)} must use mode 0600 or stricter")
    if owner_uid is not None and info.st_uid != owner_uid:
        fail(f"{path.relative_to(ROOT)} must be owned by UID {owner_uid} for the non-root container")


def read_required(path: pathlib.Path) -> str:
    try:
        return path.read_text(encoding="utf-8")
    except FileNotFoundError:
        fail(f"missing file: {path.relative_to(ROOT)}")
    except OSError as exc:
        fail(f"cannot read {path.relative_to(ROOT)}: {exc}")


def validate_source() -> None:
    for relative in REQUIRED_FILES:
        if not (DEPLOY / relative).is_file():
            fail(f"missing staging source file: deploy/staging-control-plane/{relative}")
    validate_source_texts(
        read_required(DEPLOY / ".env.example"),
        read_required(DEPLOY / "compose.yaml"),
        read_required(DEPLOY / "Caddyfile"),
        read_required(DEPLOY / "config/cloud.example.json"),
        read_required(DEPLOY / "config/bootstrap-worker.example.json"),
        read_required(ROOT / "deploy/dev-control-plane/compose.yaml"),
    )


def validate_runtime() -> None:
    env_path = DEPLOY / ".env"
    cloud_path = DEPLOY / "config/cloud.json"
    worker_path = DEPLOY / "config/bootstrap-worker.json"
    validate_permissions(env_path, private=True)
    validate_permissions(cloud_path, owner_uid=1000)
    validate_permissions(worker_path, owner_uid=1000)
    env = parse_env(read_required(env_path))
    parse_json(read_required(cloud_path), "cloud.json")
    worker = parse_json(read_required(worker_path), "bootstrap-worker.json")
    secrets: dict[str, str] = {}
    for name, filename in SECRET_FILE_NAMES.items():
        path = DEPLOY / "secrets" / filename
        validate_permissions(
            path,
            private=name not in {"origin-certificate", "ssh-known-hosts"},
            owner_uid=None if name == "postgres-password" else 1000,
        )
        secrets[name] = read_required(path)
    validate_runtime_values(env, worker, secrets)


def main() -> int:
    parser = argparse.ArgumentParser()
    mode = parser.add_mutually_exclusive_group(required=True)
    mode.add_argument("--source", action="store_true", help="validate committed staging examples and policy")
    mode.add_argument("--runtime", action="store_true", help="validate gitignored runtime files without printing values")
    args = parser.parse_args()
    if args.source:
        validate_source()
        print("staging control-plane source configuration is structurally valid")
    else:
        validate_source()
        validate_runtime()
        print("staging control-plane runtime configuration is valid")
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (ValidationError, OSError) as exc:
        print(f"configuration validation failed: {exc}", file=sys.stderr)
        raise SystemExit(1)
