#!/usr/bin/env python3
"""Focused negative tests for the staging control-plane validator."""

from __future__ import annotations

import copy
import importlib.util
import json
import pathlib
import sys
import unittest

sys.dont_write_bytecode = True

ROOT = pathlib.Path(__file__).resolve().parents[1]
MODULE_PATH = ROOT / "scripts" / "validate-staging-control-plane.py"
SPEC = importlib.util.spec_from_file_location("staging_validator", MODULE_PATH)
assert SPEC and SPEC.loader
validator = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(validator)


class StagingValidatorTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        deploy = ROOT / "deploy" / "staging-control-plane"
        cls.env_text = (deploy / ".env.example").read_text()
        cls.compose = (deploy / "compose.yaml").read_text()
        cls.caddy = (deploy / "Caddyfile").read_text()
        cls.cloud_text = (deploy / "config/cloud.example.json").read_text()
        cls.worker_text = (deploy / "config/bootstrap-worker.example.json").read_text()
        cls.dev_compose = (ROOT / "deploy/dev-control-plane/compose.yaml").read_text()

    def assert_source_rejected(self, **changes: str) -> None:
        values = {
            "env_text": self.env_text,
            "compose": self.compose,
            "caddy": self.caddy,
            "cloud_text": self.cloud_text,
            "worker_text": self.worker_text,
            "dev_compose": self.dev_compose,
        }
        values.update(changes)
        with self.assertRaises(validator.ValidationError):
            validator.validate_source_texts(**values)

    def test_sanitized_source_examples_pass(self) -> None:
        validator.validate_source_texts(
            self.env_text, self.compose, self.caddy, self.cloud_text, self.worker_text, self.dev_compose
        )

    def test_http_public_base_url_rejected(self) -> None:
        self.assert_source_rejected(env_text=self.env_text.replace("https://example.invalid", "http://example.invalid", 1))

    def test_http_callback_rejected(self) -> None:
        self.assert_source_rejected(env_text=self.env_text.replace("OPSI_CLOUD_GITHUB_APP_CALLBACK_URL=https://", "OPSI_CLOUD_GITHUB_APP_CALLBACK_URL=http://"))

    def test_callback_host_mismatch_rejected(self) -> None:
        self.assert_source_rejected(env_text=self.env_text.replace("https://example.invalid/v1/auth", "https://other.invalid/v1/auth"))

    def test_production_false_rejected(self) -> None:
        self.assert_source_rejected(env_text=self.env_text.replace("OPSI_CLOUD_PRODUCTION=true", "OPSI_CLOUD_PRODUCTION=false"))

    def test_agent_signatures_false_rejected(self) -> None:
        self.assert_source_rejected(env_text=self.env_text.replace("OPSI_CLOUD_REQUIRE_AGENT_SIGNATURES=true", "OPSI_CLOUD_REQUIRE_AGENT_SIGNATURES=false"))

    def test_otp_echo_true_rejected(self) -> None:
        self.assert_source_rejected(env_text=self.env_text.replace("OPSI_CLOUD_OTP_DEV_ECHO=false", "OPSI_CLOUD_OTP_DEV_ECHO=true"))

    def test_missing_tls_certificate_mount_rejected(self) -> None:
        compose = self.compose.replace("      - source: origin-certificate\n        target: origin-certificate.pem\n", "")
        self.assert_source_rejected(compose=compose)

    def test_writable_private_key_mount_rejected(self) -> None:
        compose = self.compose.replace(
            "      - source: origin-private-key\n        target: origin-private-key.pem\n",
            "      - ./secrets/origin-private-key.pem:/run/secrets/origin-private-key.pem:rw\n",
        )
        self.assert_source_rejected(compose=compose)

    def test_postgres_public_port_rejected(self) -> None:
        compose = self.compose.replace("    environment:\n      POSTGRES_DB:", "    ports:\n      - \"5432:5432\"\n    environment:\n      POSTGRES_DB:", 1)
        self.assert_source_rejected(compose=compose)

    def test_non_internal_backend_network_rejected(self) -> None:
        self.assert_source_rejected(compose=self.compose.replace("  backend:\n    internal: true", "  backend:"))

    def test_missing_internal_http_opt_in_rejected(self) -> None:
        worker = self.worker_text.replace('  "allow_insecure_internal_cloud_url": true,\n', "")
        self.assert_source_rejected(worker_text=worker)

    def test_internal_endpoint_exposure_rejected(self) -> None:
        self.assert_source_rejected(caddy=self.caddy.replace("/api/internal/*", "/api/not-internal/*"))

    def test_http_health_route_ordering_required(self) -> None:
        ordered = """\
:8080 {
\troute {
\t\t@health {
\t\t\tpath /health
\t\t\tremote_ip 127.0.0.1 ::1
\t\t}
\t\trespond @health 200
\t\tredir https://{host}{uri} 308
\t}
}
"""
        unordered = """\
:8080 {
\t@health {
\t\tpath /health
\t\tremote_ip 127.0.0.1 ::1
\t}
\trespond @health 200
\tredir https://{host}{uri} 308
}
"""
        self.assertIn(ordered, self.caddy)
        self.assert_source_rejected(caddy=self.caddy.replace(ordered, unordered, 1))

    def test_internal_path_variants_are_denied(self) -> None:
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
            with self.subTest(target=target):
                self.assertTrue(validator.public_path_denied(target))

    def test_latest_image_rejected(self) -> None:
        self.assert_source_rejected(env_text=self.env_text.replace("ghcr.io/opsi-dev/opsi-cloud@sha256:REPLACE_WITH_64_HEX_IMAGE_DIGEST", "ghcr.io/opsi-dev/opsi-cloud:latest", 1))

    def test_placeholder_runtime_secret_rejected(self) -> None:
        env, worker, secrets = self.valid_runtime_fixture()
        secrets["bootstrap-secret-key"] = "REPLACE_WITH_BOOTSTRAP_SECRET_KEY_1234"
        with self.assertRaises(validator.ValidationError):
            validator.validate_runtime_values(env, worker, secrets)

    def test_sanitized_runtime_fixture_passes(self) -> None:
        env, worker, secrets = self.valid_runtime_fixture()
        validator.validate_runtime_values(env, worker, secrets)

    def test_database_url_password_mismatch_rejected(self) -> None:
        env, worker, secrets = self.valid_runtime_fixture()
        secrets["database-url"] = secrets["database-url"].replace("database%2Fpassword", "different-password", 1)
        with self.assertRaisesRegex(validator.ValidationError, "password must match postgres-password"):
            validator.validate_runtime_values(env, worker, secrets)

    def test_database_url_username_mismatch_rejected(self) -> None:
        env, worker, secrets = self.valid_runtime_fixture()
        secrets["database-url"] = secrets["database-url"].replace("postgres://opsi:", "postgres://other:", 1)
        with self.assertRaisesRegex(validator.ValidationError, "username must match POSTGRES_USER"):
            validator.validate_runtime_values(env, worker, secrets)

    def test_database_url_database_mismatch_rejected(self) -> None:
        env, worker, secrets = self.valid_runtime_fixture()
        secrets["database-url"] = secrets["database-url"].replace("/opsi?", "/other?")
        with self.assertRaisesRegex(validator.ValidationError, "database must match POSTGRES_DB"):
            validator.validate_runtime_values(env, worker, secrets)

    def valid_runtime_fixture(self):
        env = validator.parse_env(self.env_text)
        env.update(
            {
                "OPSI_CLOUD_IMAGE": "ghcr.io/opsi-dev/opsi-cloud@sha256:" + "1" * 64,
                "OPSI_BOOTSTRAP_WORKER_IMAGE": "ghcr.io/opsi-dev/opsi-bootstrap-worker@sha256:" + "2" * 64,
                "OPSI_CLOUD_PUBLIC_BASE_URL": "https://staging.example.test",
                "OPSI_CLOUD_GITHUB_APP_CALLBACK_URL": "https://staging.example.test/v1/auth/browser/callback",
                "OPSI_CLOUD_SMTP_HOST": "smtp.example.test",
                "OPSI_CLOUD_SMTP_USERNAME": "opsi-staging",
                "OPSI_CLOUD_SMTP_FROM": "opsi@example.test",
                "OPSI_CLOUD_GITHUB_APP_CLIENT_ID": "client-id",
                "OPSI_CLOUD_GITHUB_APP_ID": "12345",
            }
        )
        worker = json.loads(self.worker_text)
        worker.update(
            {
                "agent_cloud_url": "https://staging.example.test",
                "k3s_version": "v1.32.5+k3s1",
                "k3s_installer_url": "https://downloads.example.test/k3s-install.sh",
                "k3s_installer_sha256": "3" * 64,
                "agent_install_url": "https://downloads.example.test/opsi-agent",
                "agent_install_sha256": "4" * 64,
            }
        )
        postgres_password = "database/password?with%encoded+chars-12345"
        secrets = {name: "s" * 40 for name in validator.REQUIRED_SECRET_NAMES}
        secrets.update(
            {
                "postgres-password": postgres_password,
                "database-url": "postgres://opsi:database%2Fpassword%3Fwith%25encoded+chars-12345@postgres:5432/opsi?sslmode=disable",
                "origin-certificate": "-----BEGIN CERTIFICATE-----\nsanitized-test\n-----END CERTIFICATE-----\n",
                "origin-private-key": "-----BEGIN " + validator.PRIVATE_KEY_MARKER + "-----\nsanitized-test\n-----END " + validator.PRIVATE_KEY_MARKER + "-----\n",
                "github-app-private-key": "-----BEGIN " + validator.PRIVATE_KEY_MARKER + "-----\nsanitized-test\n-----END " + validator.PRIVATE_KEY_MARKER + "-----\n",
                "ssh-known-hosts": "host.example.test ssh-ed25519 sanitized-test\n",
            }
        )
        return copy.deepcopy(env), copy.deepcopy(worker), copy.deepcopy(secrets)


if __name__ == "__main__":
    unittest.main(verbosity=2)
