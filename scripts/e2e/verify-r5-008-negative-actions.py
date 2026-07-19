#!/usr/bin/env python3
"""Run bounded BuildRecord negative/replay checks inside GitHub Actions."""

from __future__ import annotations

import argparse
import base64
import json
import os
import re
import sys
import urllib.error
import urllib.parse
import urllib.request


BUILD_RECORD_PATH = "/v1/build-records"
MAX_RESPONSE_BYTES = 64 * 1024
RECORD_ID = re.compile(r"^br-[A-Za-z0-9._-]{1,128}$")


def fail(message: str) -> None:
    raise SystemExit(message)


def oidc_request_url(audience: str) -> str:
    raw = os.environ.get("ACTIONS_ID_TOKEN_REQUEST_URL", "")
    parsed = urllib.parse.urlsplit(raw)
    host = (parsed.hostname or "").lower()
    query = urllib.parse.parse_qs(parsed.query, strict_parsing=True)
    if (
        parsed.scheme != "https"
        or not host.endswith(".actions.githubusercontent.com")
        or host == "actions.githubusercontent.com"
        or parsed.port not in (None, 443)
        or not parsed.path
        or parsed.path == "/"
        or parsed.fragment
        or query != {"api-version": ["2.0"]}
    ):
        fail("GitHub Actions OIDC request URL is outside the expected boundary")
    return urllib.parse.urlunsplit(
        (parsed.scheme, parsed.netloc, parsed.path, urllib.parse.urlencode({"api-version": "2.0", "audience": audience}), "")
    )


def request_oidc(audience: str) -> str:
    request_token = os.environ.get("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "")
    if os.environ.get("GITHUB_ACTIONS") != "true" or not request_token:
        fail("negative verifier must run in a GitHub Actions OIDC job")
    request = urllib.request.Request(
        oidc_request_url(audience),
        headers={"Authorization": "Bearer " + request_token, "Accept": "application/json"},
    )
    with urllib.request.urlopen(request, timeout=15) as response:
        payload = json.loads(response.read(MAX_RESPONSE_BYTES + 1))
    token = payload.get("value", "")
    if not isinstance(token, str) or len(token) > 64 * 1024 or token.count(".") != 2:
        fail("GitHub Actions returned an invalid OIDC token")
    return token


def selected_claims(token: str) -> dict[str, object]:
    encoded = token.split(".")[1]
    encoded += "=" * (-len(encoded) % 4)
    try:
        claims = json.loads(base64.urlsafe_b64decode(encoded))
    except (ValueError, json.JSONDecodeError) as error:
        fail(f"OIDC selected claims are invalid: {type(error).__name__}")
    required = (
        "repository_id",
        "repository_owner_id",
        "ref",
        "sha",
        "event_name",
        "workflow_ref",
        "run_id",
        "run_attempt",
    )
    if not isinstance(claims, dict) or any(not isinstance(claims.get(name), str) or not claims[name] for name in required):
        fail("OIDC selected claims are incomplete")
    return claims


def submission(claims: dict[str, object], args: argparse.Namespace) -> dict[str, object]:
    return {
        "schema_version": "opsi.build_record/v1",
        "service_key": "api",
        "repository_id": int(str(claims["repository_id"])),
        "repository_owner_id": int(str(claims["repository_owner_id"])),
        "ref": claims["ref"],
        "sha": claims["sha"],
        "event_name": claims["event_name"],
        "workflow_ref": claims["workflow_ref"],
        "job_workflow_ref": claims.get("job_workflow_ref", ""),
        "run_id": int(str(claims["run_id"])),
        "run_attempt": int(str(claims["run_attempt"])),
        "config_hash": args.config_hash,
        "plan_hash": args.plan_hash,
        "platform": "linux/amd64",
        "oci_repository": args.oci_repository,
        "oci_digest": args.oci_digest,
        "status": "succeeded",
    }


def post(endpoint: str, token: str, body: dict[str, object]) -> tuple[int, dict[str, object], str]:
    request = urllib.request.Request(
        endpoint,
        data=json.dumps(body, separators=(",", ":"), sort_keys=True).encode(),
        method="POST",
        headers={"Authorization": "Bearer " + token, "Content-Type": "application/json", "Accept": "application/json"},
    )
    try:
        response = urllib.request.urlopen(request, timeout=15)
    except urllib.error.HTTPError as error:
        response = error
    with response:
        raw = response.read(MAX_RESPONSE_BYTES + 1)
        retry_after = response.headers.get("Retry-After", "")
        status = response.status
    if len(raw) > MAX_RESPONSE_BYTES:
        fail("Cloud response exceeded the verifier bound")
    try:
        payload = json.loads(raw)
    except json.JSONDecodeError:
        payload = {}
    return status, payload if isinstance(payload, dict) else {}, retry_after


def expect_error(results: list[dict[str, object]], name: str, actual: tuple[int, dict[str, object], str], status: int, code: str) -> None:
    actual_status, payload, retry_after = actual
    actual_code = payload.get("error_code", "")
    if actual_status != status or actual_code != code:
        fail(f"{name}: expected HTTP {status} {code}, got HTTP {actual_status} {actual_code}")
    results.append({"case": name, "status": actual_status, "error_code": actual_code, "retry_after": retry_after})


def run_forbidden(args: argparse.Namespace) -> list[dict[str, object]]:
    audience = args.cloud_url + BUILD_RECORD_PATH
    token = request_oidc(audience)
    body = submission(selected_claims(token), args)
    results: list[dict[str, object]] = []
    expect_error(results, "workload-forbidden", post(audience, token, body), 403, "BUILD_WORKLOAD_FORBIDDEN")
    return results


def run_suite(args: argparse.Namespace) -> list[dict[str, object]]:
    endpoint = args.cloud_url + BUILD_RECORD_PATH
    results: list[dict[str, object]] = []

    wrong_token = request_oidc(endpoint + "/")
    wrong_body = submission(selected_claims(wrong_token), args)
    expect_error(results, "wrong-audience", post(endpoint, wrong_token, wrong_body), 401, "OIDC_AUTH_INVALID")

    token = request_oidc(endpoint)
    base = submission(selected_claims(token), args)

    changed = dict(base)
    changed["sha"] = "0" * 40
    expect_error(results, "body-sha-mismatch", post(endpoint, token, changed), 403, "BUILD_CLAIM_BODY_MISMATCH")

    changed = dict(base)
    changed["service_key"] = "unbound"
    expect_error(results, "unbound-service", post(endpoint, token, changed), 403, "BUILD_BINDING_INVALID")

    changed = dict(base)
    changed["oci_repository"] = args.oci_repository + "-wrong"
    expect_error(results, "wrong-oci-repository", post(endpoint, token, changed), 403, "BUILD_WORKLOAD_FORBIDDEN")

    changed = dict(base)
    changed["oci_digest"] = "latest"
    expect_error(results, "tag-only-digest", post(endpoint, token, changed), 400, "BUILD_ARTIFACT_INVALID")

    created_status, created, _ = post(endpoint, token, base)
    record = created.get("record", {})
    record_id = record.get("id", "") if isinstance(record, dict) else ""
    if created_status != 201 or created.get("reused") is not False or not isinstance(record_id, str) or not RECORD_ID.match(record_id):
        fail("valid submission did not create a sanitized BuildRecord")
    results.append({"case": "create", "status": created_status, "record_id": record_id, "reused": False})

    replay_status, replay, _ = post(endpoint, token, base)
    replay_record = replay.get("record", {})
    replay_id = replay_record.get("id", "") if isinstance(replay_record, dict) else ""
    if replay_status != 200 or replay.get("reused") is not True or replay_id != record_id:
        fail("exact replay did not reuse the same BuildRecord")
    results.append({"case": "exact-replay", "status": replay_status, "record_id": replay_id, "reused": True})

    changed = dict(base)
    changed["oci_digest"] = args.conflict_digest
    expect_error(results, "changed-payload-conflict", post(endpoint, token, changed), 409, "BUILD_RECORD_CONFLICT")
    return results


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser()
    parser.add_argument("--mode", choices=("suite", "expect-forbidden"), required=True)
    parser.add_argument("--cloud-url", default="https://opsidev.site")
    parser.add_argument("--config-hash", required=True)
    parser.add_argument("--plan-hash", required=True)
    parser.add_argument("--oci-repository", required=True)
    parser.add_argument("--oci-digest", required=True)
    parser.add_argument("--conflict-digest", required=True)
    args = parser.parse_args()
    parsed = urllib.parse.urlsplit(args.cloud_url)
    if parsed.scheme != "https" or not parsed.hostname or parsed.path or parsed.query or parsed.fragment or parsed.username or parsed.password:
        fail("cloud URL must be an exact HTTPS origin")
    for value in (args.config_hash, args.plan_hash):
        if not re.fullmatch(r"[0-9a-f]{64}", value):
            fail("config and plan hashes must be canonical")
    for value in (args.oci_digest, args.conflict_digest):
        if not re.fullmatch(r"sha256:[0-9a-f]{64}", value):
            fail("digests must be immutable sha256 values")
    return args


def main() -> None:
    args = parse_args()
    results = run_suite(args) if args.mode == "suite" else run_forbidden(args)
    json.dump({"results": results}, sys.stdout, separators=(",", ":"), sort_keys=True)
    sys.stdout.write("\n")


if __name__ == "__main__":
    main()
