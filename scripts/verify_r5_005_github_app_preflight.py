#!/usr/bin/env python3
"""Sanitized R5-005 GitHub App and installation preflight."""

from __future__ import annotations

import argparse
import base64
import json
import os
import stat
import subprocess
import sys
import time
import urllib.error
import urllib.request
from pathlib import Path
from typing import Any


GITHUB_API = "https://api.github.com"
MAX_RESPONSE_BYTES = 1024 * 1024
DEFAULT_LIFECYCLE_EVENTS = ("installation", "installation_repositories")


class PreflightError(RuntimeError):
    pass


class NoRedirectHandler(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        raise PreflightError(f"GitHub API unexpectedly redirected with status {code}")


def require(condition: bool, message: str) -> None:
    if not condition:
        raise PreflightError(message)


def positive_int(value: Any, name: str) -> int:
    require(isinstance(value, int) and not isinstance(value, bool) and value > 0, f"{name} must be a positive integer")
    return value


def validate_app(app: dict[str, Any], expected_app_id: int) -> dict[str, Any]:
    require(positive_int(app.get("id"), "app.id") == expected_app_id, "configured GitHub App ID does not match GitHub")
    events = app.get("events")
    require(isinstance(events, list) and all(isinstance(event, str) for event in events), "app.events must be a string array")
    require("repository" in events, "GitHub App must manually subscribe to the repository event")
    permissions = app.get("permissions")
    require(permissions == {"metadata": "read"}, "GitHub App permissions must remain Metadata: Read-only")
    return {
        "id": expected_app_id,
        "slug": app.get("slug", ""),
        "manual_events": events,
        "default_lifecycle_events": list(DEFAULT_LIFECYCLE_EVENTS),
        "permissions": permissions,
    }


def validate_hook_config(hook: dict[str, Any], expected_url: str) -> dict[str, Any]:
    require(hook.get("url") == expected_url, "GitHub App webhook URL does not match the public endpoint")
    require(hook.get("content_type") == "json", "GitHub App webhook content type must be json")
    insecure_ssl = hook.get("insecure_ssl")
    require(insecure_ssl in (0, "0"), "GitHub App webhook TLS verification must be enabled")
    return {"url": expected_url, "content_type": "json", "insecure_ssl": "0"}


def validate_installation(installation: dict[str, Any], installation_id: int, owner_id: int) -> dict[str, Any]:
    require(positive_int(installation.get("id"), "installation.id") == installation_id, "installation ID mismatch")
    account = installation.get("account")
    require(isinstance(account, dict), "installation account is missing")
    require(positive_int(account.get("id"), "installation.account.id") == owner_id, "installation account owner ID mismatch")
    require(installation.get("suspended_at") is None, "GitHub App installation is suspended")
    require(installation.get("repository_selection") == "selected", "installation must use selected repositories")
    return {
        "id": installation_id,
        "account": {"id": owner_id, "login": account.get("login", ""), "type": account.get("type", "")},
        "repository_selection": "selected",
        "suspended": False,
    }


def validate_repositories(
    response: dict[str, Any], repository_id: int, full_name: str, owner_id: int, default_branch: str
) -> dict[str, Any]:
    repositories = response.get("repositories")
    require(isinstance(repositories, list), "installation repository response is malformed")
    for repository in repositories:
        if not isinstance(repository, dict) or repository.get("id") != repository_id:
            continue
        owner = repository.get("owner")
        require(isinstance(owner, dict) and owner.get("id") == owner_id, "fixture repository owner ID mismatch")
        require(repository.get("full_name") == full_name, "fixture repository full name mismatch")
        require(repository.get("default_branch") == default_branch, "fixture repository default branch mismatch")
        require(repository.get("archived") is False and repository.get("disabled") is False, "fixture repository is unavailable")
        return {
            "id": repository_id,
            "full_name": full_name,
            "owner_id": owner_id,
            "default_branch": default_branch,
            "private": bool(repository.get("private")),
        }
    raise PreflightError("fixture repository is not included in the selected installation repositories")


def base64url(value: bytes) -> str:
    return base64.urlsafe_b64encode(value).rstrip(b"=").decode("ascii")


def validate_private_key_path(path: Path) -> None:
    require(path.is_absolute(), "GitHub App private-key path must be absolute")
    try:
        metadata = path.lstat()
    except OSError as exc:
        raise PreflightError("GitHub App private key is unavailable") from exc
    require(stat.S_ISREG(metadata.st_mode) and not path.is_symlink(), "GitHub App private key must be a regular non-symlink file")
    require(metadata.st_size > 0, "GitHub App private key is empty")
    require(metadata.st_mode & 0o022 == 0, "GitHub App private key must not be group/world writable")


def mint_app_jwt(app_id: int, private_key: Path) -> str:
    validate_private_key_path(private_key)
    now = int(time.time())
    header = base64url(b'{"alg":"RS256","typ":"JWT"}')
    payload = base64url(json.dumps({"iat": now - 60, "exp": now + 540, "iss": app_id}, separators=(",", ":")).encode())
    unsigned = f"{header}.{payload}"
    try:
        result = subprocess.run(
            ["openssl", "dgst", "-sha256", "-sign", str(private_key)],
            input=unsigned.encode("ascii"),
            stdout=subprocess.PIPE,
            stderr=subprocess.DEVNULL,
            check=True,
        )
    except (OSError, subprocess.CalledProcessError) as exc:
        raise PreflightError("GitHub App JWT signing failed") from exc
    return f"{unsigned}.{base64url(result.stdout)}"


def request_bytes(path: str, bearer: str, method: str = "GET", body: bytes | None = None) -> tuple[int, bytes]:
    request = urllib.request.Request(
        GITHUB_API + path,
        method=method,
        data=body,
        headers={
            "Accept": "application/vnd.github+json",
            "Authorization": f"Bearer {bearer}",
            "Content-Type": "application/json",
            "User-Agent": "opsi-r5-005-preflight",
            "X-GitHub-Api-Version": "2022-11-28",
        },
    )
    try:
        with urllib.request.build_opener(NoRedirectHandler()).open(request, timeout=20) as response:
            raw = response.read(MAX_RESPONSE_BYTES + 1)
            status = response.status
    except PreflightError:
        raise
    except urllib.error.HTTPError as exc:
        raise PreflightError(f"GitHub API request failed with status {exc.code}") from exc
    except (OSError, urllib.error.URLError) as exc:
        raise PreflightError("GitHub API request failed") from exc
    require(len(raw) <= MAX_RESPONSE_BYTES, "GitHub API response exceeds the preflight limit")
    return status, raw


def request_json(path: str, bearer: str, method: str = "GET", body: bytes | None = None) -> dict[str, Any]:
    _, raw = request_bytes(path, bearer, method, body)
    try:
        decoded = json.loads(raw)
    except (UnicodeDecodeError, json.JSONDecodeError) as exc:
        raise PreflightError("GitHub API returned malformed JSON") from exc
    require(isinstance(decoded, dict), "GitHub API response must be a JSON object")
    return decoded


def parse_delivery_payload(value: Any) -> dict[str, Any]:
    if isinstance(value, dict):
        return value
    if not isinstance(value, str) or len(value) > MAX_RESPONSE_BYTES:
        return {}
    try:
        decoded = json.loads(value)
    except json.JSONDecodeError:
        return {}
    return decoded if isinstance(decoded, dict) else {}


def sanitize_delivery(delivery: dict[str, Any]) -> dict[str, Any]:
    safe = {
        "api_id": positive_int(delivery.get("id"), "delivery.id"),
        "guid": delivery.get("guid", ""),
        "event": delivery.get("event", ""),
        "action": delivery.get("action", ""),
        "delivered_at": delivery.get("delivered_at"),
        "redelivery": bool(delivery.get("redelivery")),
        "status_code": delivery.get("status_code"),
    }
    request_payload = parse_delivery_payload((delivery.get("request") or {}).get("payload"))
    installation = request_payload.get("installation")
    if isinstance(installation, dict) and isinstance(installation.get("id"), int):
        safe["installation_id"] = installation["id"]
    repository = request_payload.get("repository")
    if isinstance(repository, dict) and isinstance(repository.get("id"), int):
        safe["repository_id"] = repository["id"]
    for source, target in (("repositories_added", "added_repository_ids"), ("repositories_removed", "removed_repository_ids")):
        repositories = request_payload.get(source)
        if isinstance(repositories, list):
            safe[target] = [item["id"] for item in repositories if isinstance(item, dict) and isinstance(item.get("id"), int)]
    response_payload = parse_delivery_payload((delivery.get("response") or {}).get("payload"))
    safe["response"] = {
        key: response_payload[key]
        for key in ("status", "duplicate", "error", "error_code")
        if key in response_payload
    }
    return safe


def delivery_json(bearer: str, api_id: int) -> dict[str, Any]:
    return sanitize_delivery(request_json(f"/app/hook/deliveries/{api_id}", bearer))


def list_deliveries(bearer: str, event: str, action: str, limit: int) -> dict[str, Any]:
    _, raw = request_bytes(f"/app/hook/deliveries?per_page={limit}", bearer)
    try:
        deliveries = json.loads(raw)
    except (UnicodeDecodeError, json.JSONDecodeError) as exc:
        raise PreflightError("GitHub delivery list returned malformed JSON") from exc
    require(isinstance(deliveries, list), "GitHub delivery list must be a JSON array")
    result = []
    for delivery in deliveries:
        if not isinstance(delivery, dict) or delivery.get("event") != event:
            continue
        if action and delivery.get("action") != action:
            continue
        result.append(sanitize_delivery(delivery))
    return {"deliveries": result}


def redeliver(bearer: str, api_id: int, wait_seconds: int) -> dict[str, Any]:
    original = request_json(f"/app/hook/deliveries/{api_id}", bearer)
    guid = original.get("guid")
    require(isinstance(guid, str) and guid != "", "GitHub delivery GUID is missing")
    _, before_raw = request_bytes("/app/hook/deliveries?per_page=100", bearer)
    try:
        before_deliveries = json.loads(before_raw)
    except (UnicodeDecodeError, json.JSONDecodeError) as exc:
        raise PreflightError("GitHub delivery list returned malformed JSON") from exc
    require(isinstance(before_deliveries, list), "GitHub delivery list must be a JSON array")
    previous_attempts = {
        item.get("id")
        for item in before_deliveries
        if isinstance(item, dict) and item.get("guid") == guid and item.get("redelivery")
    }
    status, _ = request_bytes(f"/app/hook/deliveries/{api_id}/attempts", bearer, method="POST")
    require(status == 202, "GitHub delivery redelivery was not accepted")
    deadline = time.monotonic() + wait_seconds
    while True:
        _, raw = request_bytes("/app/hook/deliveries?per_page=100", bearer)
        try:
            deliveries = json.loads(raw)
        except (UnicodeDecodeError, json.JSONDecodeError) as exc:
            raise PreflightError("GitHub delivery list returned malformed JSON") from exc
        require(isinstance(deliveries, list), "GitHub delivery list must be a JSON array")
        attempts = [
            item
            for item in deliveries
            if isinstance(item, dict)
            and item.get("guid") == guid
            and item.get("redelivery")
            and item.get("id") not in previous_attempts
        ]
        if attempts:
            latest = max(attempts, key=lambda item: str(item.get("delivered_at", "")))
            return delivery_json(bearer, positive_int(latest.get("id"), "delivery.id"))
        if time.monotonic() >= deadline:
            raise PreflightError("GitHub delivery redelivery did not complete before timeout")
        time.sleep(1)


def run_live(args: argparse.Namespace) -> dict[str, Any]:
    jwt = mint_app_jwt(args.app_id, args.private_key)
    app = validate_app(request_json("/app", jwt), args.app_id)
    hook = validate_hook_config(request_json("/app/hook/config", jwt), args.webhook_url)
    installation = validate_installation(
        request_json(f"/app/installations/{args.installation_id}", jwt), args.installation_id, args.owner_id
    )
    token_response = request_json(
        f"/app/installations/{args.installation_id}/access_tokens", jwt, method="POST", body=b"{}"
    )
    installation_token = token_response.get("token")
    require(isinstance(installation_token, str) and 16 <= len(installation_token) <= 4096, "installation token response is invalid")
    repositories = validate_repositories(
        request_json("/installation/repositories?per_page=100", installation_token),
        args.repository_id,
        args.repository_full_name,
        args.owner_id,
        args.default_branch,
    )
    return {
        "app": app,
        "webhook": hook,
        "installation": installation,
        "installation_token_request": "pass",
        "repository": repositories,
    }


def parse_json_file(path: Path) -> dict[str, Any]:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, UnicodeDecodeError, json.JSONDecodeError) as exc:
        raise PreflightError("preflight fixture is not valid JSON") from exc
    require(isinstance(value, dict), "preflight fixture must be a JSON object")
    return value


def parser() -> argparse.ArgumentParser:
    result = argparse.ArgumentParser(description=__doc__)
    subparsers = result.add_subparsers(dest="mode", required=True)
    app_json = subparsers.add_parser("check-app-json", help="validate a captured GitHub /app response")
    app_json.add_argument("--path", type=Path, required=True)
    app_json.add_argument("--app-id", type=int, required=True)

    live = subparsers.add_parser("live", help="run the sanitized live App/installation preflight")
    live.add_argument("--app-id", type=int, required=True)
    live.add_argument("--private-key", type=Path, required=True)
    live.add_argument("--installation-id", type=int, required=True)
    live.add_argument("--owner-id", type=int, required=True)
    live.add_argument("--repository-id", type=int, required=True)
    live.add_argument("--repository-full-name", required=True)
    live.add_argument("--default-branch", default="main")
    live.add_argument("--webhook-url", required=True)

    deliveries = subparsers.add_parser("deliveries", help="list sanitized GitHub App webhook deliveries")
    deliveries.add_argument("--app-id", type=int, required=True)
    deliveries.add_argument("--private-key", type=Path, required=True)
    deliveries.add_argument("--event", choices=("repository", "installation_repositories"), required=True)
    deliveries.add_argument("--action", choices=("", "added", "removed", "created", "deleted", "renamed", "edited"), default="")
    deliveries.add_argument("--limit", type=int, choices=range(1, 101), default=30)

    delivery = subparsers.add_parser("delivery", help="show one sanitized GitHub App webhook delivery")
    delivery.add_argument("--app-id", type=int, required=True)
    delivery.add_argument("--private-key", type=Path, required=True)
    delivery.add_argument("--api-id", type=int, required=True)

    replay = subparsers.add_parser("redeliver", help="redeliver and verify one sanitized GitHub App webhook delivery")
    replay.add_argument("--app-id", type=int, required=True)
    replay.add_argument("--private-key", type=Path, required=True)
    replay.add_argument("--api-id", type=int, required=True)
    replay.add_argument("--wait-seconds", type=int, choices=range(1, 121), default=30)
    return result


def main() -> int:
    args = parser().parse_args()
    try:
        for name in ("app_id", "installation_id", "owner_id", "repository_id"):
            if hasattr(args, name):
                positive_int(getattr(args, name), name)
        if args.mode == "check-app-json":
            output = validate_app(parse_json_file(args.path), args.app_id)
        elif args.mode == "live":
            output = run_live(args)
        else:
            jwt = mint_app_jwt(args.app_id, args.private_key)
            if args.mode == "deliveries":
                output = list_deliveries(jwt, args.event, args.action, args.limit)
            elif args.mode == "delivery":
                output = delivery_json(jwt, positive_int(args.api_id, "api_id"))
            else:
                output = redeliver(jwt, positive_int(args.api_id, "api_id"), args.wait_seconds)
    except PreflightError as exc:
        print(f"R5-005 GitHub App preflight failed: {exc}", file=sys.stderr)
        return 1
    print(json.dumps(output, separators=(",", ":"), sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
