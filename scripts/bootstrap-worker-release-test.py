#!/usr/bin/env python3
"""Self-checks for the canonical Bootstrap Worker release path."""

from __future__ import annotations

import importlib.util
import json
import os
import pathlib
import subprocess
import sys
import tempfile
import textwrap
import unittest

ROOT = pathlib.Path(__file__).resolve().parents[1]
HELPER = ROOT / "scripts/bootstrap-worker-release.py"
WORKFLOW = ROOT / ".github/workflows/publish-bootstrap-worker.yml"
SPEC = importlib.util.spec_from_file_location("bootstrap_worker_release", HELPER)
assert SPEC and SPEC.loader
release = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(release)

DIGEST_A = "sha256:" + "a" * 64
DIGEST_B = "sha256:" + "b" * 64
REF_A = release.IMAGE_REPOSITORY + "@" + DIGEST_A
REF_B = release.IMAGE_REPOSITORY + "@" + DIGEST_B
REVISION = "c" * 40


def manifest(**changes: str) -> dict[str, str]:
    value = {
        "schema_version": release.SCHEMA,
        "repository": release.IMAGE_REPOSITORY,
        "source_repository": release.SOURCE_REPOSITORY,
        "source_revision": REVISION,
        "image_digest": DIGEST_B,
        "image_reference": REF_B,
        "platform": release.PLATFORM,
        "workflow_run_id": "123456",
        "created_at": "2026-08-01T12:34:56Z",
    }
    value.update(changes)
    return value


class ManifestTests(unittest.TestCase):
    def test_valid_manifest_is_strict_and_credential_free(self) -> None:
        value = release.validate_manifest(manifest())
        self.assertEqual(set(value), release.MANIFEST_KEYS)
        self.assertFalse(any("token" in key or "password" in key or "credential" in key for key in value))

    def test_digest_and_revision_must_be_full_lowercase_values(self) -> None:
        with self.assertRaises(release.ReleaseError):
            release.validate_manifest(manifest(image_digest="sha256:" + "A" * 64, image_reference=release.IMAGE_REPOSITORY + "@sha256:" + "A" * 64))
        with self.assertRaises(release.ReleaseError):
            release.validate_manifest(manifest(source_revision="c" * 39))

    def test_unknown_malformed_and_repository_mismatch_are_rejected(self) -> None:
        unknown = manifest()
        unknown["credential"] = "must-not-exist"
        with self.assertRaises(release.ReleaseError):
            release.validate_manifest(unknown)
        with self.assertRaises(release.ReleaseError):
            release.validate_manifest(manifest(repository="ghcr.io/huutawn/other"))
        with tempfile.TemporaryDirectory() as raw:
            path = pathlib.Path(raw) / "release.json"
            path.write_text("{not-json", encoding="utf-8")
            with self.assertRaises(release.ReleaseError):
                release.load_manifest(path)

    def test_manifest_command_writes_the_workflow_artifact(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            output = pathlib.Path(raw) / "bootstrap-worker-release.json"
            result = subprocess.run(
                [
                    sys.executable,
                    str(HELPER),
                    "manifest",
                    "--source-revision",
                    REVISION,
                    "--image-digest",
                    DIGEST_B,
                    "--workflow-run-id",
                    "123456",
                    "--created-at",
                    "2026-08-01T12:34:56Z",
                    "--output",
                    str(output),
                ],
                text=True,
                capture_output=True,
                check=False,
            )
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(release.load_manifest(output)["image_reference"], REF_B)


class WorkflowTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.workflow = WORKFLOW.read_text(encoding="utf-8")

    def test_publish_is_manual_official_repository_only(self) -> None:
        self.assertIn("workflow_dispatch:", self.workflow)
        self.assertNotIn("pull_request:", self.workflow)
        self.assertIn('test "$GITHUB_REPOSITORY" = huutawn/opsi', self.workflow)
        self.assertIn('test "$GITHUB_REF" = refs/heads/developer', self.workflow)
        self.assertIn('test "$SOURCE_REVISION" = "$GITHUB_SHA"', self.workflow)
        self.assertIn('test "$CONFIRMATION" = publish-bootstrap-worker', self.workflow)

    def test_publish_identity_is_immutable_and_permissions_are_scoped(self) -> None:
        self.assertNotIn(":latest", self.workflow)
        self.assertEqual(self.workflow.count("packages: write"), 1)
        self.assertIn("jobs:\n  publish:", self.workflow)
        self.assertIn("group: publish-bootstrap-worker-${{ github.sha }}", self.workflow)
        self.assertIn("revision tag already exists; refusing to overwrite it", self.workflow)
        self.assertIn("ghcr.io/huutawn/opsi-bootstrap-worker", self.workflow)
        self.assertIn("^sha256:[0-9a-f]{64}$", self.workflow)

    def test_build_uses_cloud_dockerfile_and_repository_root_context(self) -> None:
        start = self.workflow.index("          docker build \\\n")
        end = self.workflow.index("          docker push", start)
        build_block = self.workflow[start:end]

        self.assertIn("--file cloud/Dockerfile \\\n", build_block)
        self.assertIn("--platform \"$PLATFORM\" \\\n", build_block)
        self.assertIn("--target bootstrap-worker \\\n", build_block)
        self.assertIn('--tag "$image_tag" \\\n            .\n', build_block)
        self.assertNotIn("--file Dockerfile", build_block)
        self.assertTrue((ROOT / "cloud/Dockerfile").is_file())
        self.assertFalse((ROOT / "Dockerfile").exists())

    def test_workflow_emits_provenance_manifest_after_push(self) -> None:
        self.assertLess(self.workflow.index("docker push"), self.workflow.index("Create release manifest"))
        for label in ("image.source", "image.revision", "image.version", "image.created"):
            self.assertIn(label, self.workflow)
        self.assertIn("actions/upload-artifact@ea165f8d65b6e75b540449e92b4886f43607fa02", self.workflow)


class RuntimeTests(unittest.TestCase):
    def setUp(self) -> None:
        self.temp = tempfile.TemporaryDirectory()
        self.root = pathlib.Path(self.temp.name)
        self.compose = self.root / "staging-control-plane"
        self.compose.mkdir()
        (self.compose / "compose.yaml").write_text("services:\n  bootstrap-worker: {}\n", encoding="utf-8")
        (self.compose / "compose.e2e-bootstrap-barrier.yaml").write_text("services:\n  bootstrap-worker: {}\n", encoding="utf-8")
        self.bin = self.root / "bin"
        self.bin.mkdir()
        self.log = self.root / "commands.log"
        self.state = self.root / "recreated"
        self.write_fake_tools()

    def tearDown(self) -> None:
        self.temp.cleanup()

    def write_fake_tools(self) -> None:
        docker = self.bin / "docker"
        docker.write_text(
            textwrap.dedent(
                r"""
                #!/usr/bin/env bash
                set -eu
                printf '%s\n' "$*" >> "$FAKE_LOG"
                if test "$1" = compose; then
                  case " $* " in
                    *" ps -q bootstrap-worker "*) printf '%s\n' worker-container ;;
                    *" up -d --no-deps --force-recreate bootstrap-worker "*)
                      test "${FAKE_COMPOSE_UP_EXIT:-0}" = 0 || exit "$FAKE_COMPOSE_UP_EXIT"
                      : > "$FAKE_STATE"
                      ;;
                    *) exit 9 ;;
                  esac
                elif test "$1" = inspect; then
                  case " $* " in
                    *State.Health*) printf '%s\n' "${FAKE_HEALTH:-healthy}" ;;
                    *) printf '%s\n' "sha256:$(printf '1%.0s' {1..64})" ;;
                  esac
                elif test "$1" = image; then
                  reference=$FAKE_RUNNING_IMAGE
                  if test -e "$FAKE_STATE"; then
                    reference=$(sed -n 's/^OPSI_BOOTSTRAP_WORKER_IMAGE=//p' "$FAKE_COMPOSE_DIR/.env")
                  fi
                  printf '["%s"]\n' "$reference"
                elif test "$1" = pull; then
                  exit 0
                else
                  exit 9
                fi
                """
            ).lstrip(),
            encoding="utf-8",
        )
        curl = self.bin / "curl"
        curl.write_text("#!/usr/bin/env bash\nexit \"${FAKE_CURL_EXIT:-0}\"\n", encoding="utf-8")
        docker.chmod(0o755)
        curl.chmod(0o755)

    def write_env(self, reference: str) -> None:
        (self.compose / ".env").write_text(
            "COMPOSE_PROJECT_NAME=opsi-staging\n" + f"OPSI_BOOTSTRAP_WORKER_IMAGE={reference}\n",
            encoding="utf-8",
        )

    def write_manifest(self, value: dict[str, str] | None = None) -> pathlib.Path:
        path = self.root / "release.json"
        path.write_text(json.dumps(value or manifest()), encoding="utf-8")
        return path

    def command(self, operation: str, target: pathlib.Path | str, expected: str, *extra: str) -> list[str]:
        command = [sys.executable, str(HELPER), operation]
        if operation == "deploy":
            command.extend(("--manifest", str(target)))
        else:
            command.extend(("--to", str(target)))
        command.extend(
            (
                "--expected-current-digest",
                expected,
                "--compose-project",
                "opsi-staging",
                "--compose-directory",
                str(self.compose),
                "--service",
                "bootstrap-worker",
                "--health-timeout",
                "1",
            )
        )
        command.extend(extra)
        return command

    def run_helper(self, command: list[str], **changes: str) -> subprocess.CompletedProcess[str]:
        env = os.environ.copy()
        env.update(
            {
                "PATH": str(self.bin) + os.pathsep + env["PATH"],
                "FAKE_LOG": str(self.log),
                "FAKE_STATE": str(self.state),
                "FAKE_COMPOSE_DIR": str(self.compose),
                "FAKE_RUNNING_IMAGE": changes.pop("FAKE_RUNNING_IMAGE", REF_A),
            }
        )
        env.update(changes)
        return subprocess.run(command, text=True, capture_output=True, env=env, check=False)

    def test_mutable_image_reference_is_rejected(self) -> None:
        self.write_env(REF_A)
        command = self.command("rollback", release.IMAGE_REPOSITORY + ":candidate", DIGEST_A)
        result = self.run_helper(command)
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(self.log.exists())

    def test_expected_current_mismatch_fails_before_mutation(self) -> None:
        self.write_env(REF_A)
        result = self.run_helper(self.command("deploy", self.write_manifest(), DIGEST_B))
        self.assertNotEqual(result.returncode, 0)
        log = self.log.read_text(encoding="utf-8")
        self.assertNotIn("pull ", log)
        self.assertNotIn(" up ", log)
        self.assertIn(REF_A, (self.compose / ".env").read_text(encoding="utf-8"))

    def test_deploy_targets_only_worker_and_persists_binding_with_barrier(self) -> None:
        self.write_env(REF_A)
        command = self.command(
            "deploy",
            self.write_manifest(),
            DIGEST_A,
            "--compose-file",
            "compose.e2e-bootstrap-barrier.yaml",
        )
        result = self.run_helper(command)
        self.assertEqual(result.returncode, 0, result.stderr)
        log = self.log.read_text(encoding="utf-8")
        self.assertIn("pull " + REF_B, log)
        self.assertIn("up -d --no-deps --force-recreate bootstrap-worker", log)
        self.assertIn("compose.e2e-bootstrap-barrier.yaml", log)
        self.assertNotIn(" cloud", log)
        self.assertNotIn(" postgres", log)
        self.assertNotIn(" reverse-proxy", log)
        self.assertIn("OPSI_BOOTSTRAP_WORKER_IMAGE=" + REF_B, (self.compose / ".env").read_text(encoding="utf-8"))
        self.assertTrue(list(self.compose.glob(".env.bootstrap-worker-release.*.bak")))

    def test_worker_and_cloud_health_failures_return_nonzero(self) -> None:
        self.write_env(REF_A)
        unhealthy = self.run_helper(self.command("deploy", self.write_manifest(), DIGEST_A), FAKE_HEALTH="unhealthy")
        self.assertNotEqual(unhealthy.returncode, 0)

        self.state.unlink(missing_ok=True)
        self.write_env(REF_A)
        self.log.unlink(missing_ok=True)
        cloud_down = self.run_helper(self.command("deploy", self.write_manifest(), DIGEST_A), FAKE_CURL_EXIT="22")
        self.assertNotEqual(cloud_down.returncode, 0)

    def test_failed_recreate_restores_old_persisted_binding(self) -> None:
        self.write_env(REF_A)
        failed = self.run_helper(
            self.command("deploy", self.write_manifest(), DIGEST_A),
            FAKE_COMPOSE_UP_EXIT="17",
        )
        self.assertNotEqual(failed.returncode, 0)
        self.assertIn("binding_restored=" + REF_A, failed.stdout)
        self.assertIn("OPSI_BOOTSTRAP_WORKER_IMAGE=" + REF_A, (self.compose / ".env").read_text(encoding="utf-8"))

    def test_rollback_uses_previous_digest_and_health_failure_is_nonzero(self) -> None:
        self.write_env(REF_B)
        result = self.run_helper(self.command("rollback", REF_A, DIGEST_B), FAKE_RUNNING_IMAGE=REF_B)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("rollback_target=" + REF_B, result.stdout)
        self.assertIn("pull " + REF_A, self.log.read_text(encoding="utf-8"))
        self.assertIn("OPSI_BOOTSTRAP_WORKER_IMAGE=" + REF_A, (self.compose / ".env").read_text(encoding="utf-8"))

        self.state.unlink(missing_ok=True)
        self.write_env(REF_B)
        self.log.unlink(missing_ok=True)
        failed = self.run_helper(
            self.command("rollback", REF_A, DIGEST_B),
            FAKE_RUNNING_IMAGE=REF_B,
            FAKE_HEALTH="unhealthy",
        )
        self.assertNotEqual(failed.returncode, 0)


if __name__ == "__main__":
    unittest.main(verbosity=2)
