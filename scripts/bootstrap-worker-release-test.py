#!/usr/bin/env python3
"""Self-checks for the canonical Bootstrap Worker release path."""

from __future__ import annotations

import importlib.util
import hashlib
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
DOCKERFILE = ROOT / "cloud/Dockerfile"
MAKEFILE = ROOT / "Makefile"
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
        cls.dockerfile = DOCKERFILE.read_text(encoding="utf-8")
        cls.makefile = MAKEFILE.read_text(encoding="utf-8")

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

    def test_isolated_release_checks_are_fail_closed(self) -> None:
        self.assertEqual(self.dockerfile.count("go build -mod=readonly"), 2)
        self.assertIn("-o /out/opsi-cloud ./cmd/opsi-cloud", self.dockerfile)
        self.assertIn("-o /out/opsi-bootstrap-worker ./cmd/opsi-bootstrap-worker", self.dockerfile)

        start = self.makefile.index("verify-bootstrap-worker-release:")
        end = self.makefile.index("\n\nverify:", start)
        gate = self.makefile[start:end]
        self.assertIn("GOWORK=off", gate)
        self.assertIn("go list -mod=readonly -deps", gate)
        self.assertIn("./cmd/opsi-cloud ./cmd/opsi-bootstrap-worker", gate)

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
        (self.compose / "config").mkdir()
        (self.compose / "barrier-state").mkdir(mode=0o700)
        (self.compose / "compose.yaml").write_text("services:\n  bootstrap-worker: {}\n", encoding="utf-8")
        (self.compose / "compose.e2e-bootstrap-barrier.yaml").write_text("services:\n  bootstrap-worker: {}\n", encoding="utf-8")
        (self.compose / "wrong.yaml").write_text("services: {}\n", encoding="utf-8")
        self.bin = self.root / "bin"
        self.bin.mkdir()
        self.log = self.root / "commands.log"
        self.state = self.root / "recreated"
        self.runtime = self.root / "runtime"
        self.runtime.mkdir()
        (self.runtime / "container").write_text("worker-old", encoding="utf-8")
        (self.runtime / "running").touch()
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
                    *" ps -a -q bootstrap-worker "*) cat "$FAKE_RUNTIME/container" ;;
                    *" ps -q bootstrap-worker "*) test ! -e "$FAKE_RUNTIME/running" || cat "$FAKE_RUNTIME/container" ;;
                    *" stop bootstrap-worker "*) rm -f "$FAKE_RUNTIME/running" ;;
                    *" up -d --no-deps --force-recreate bootstrap-worker "*)
                      test "${FAKE_COMPOSE_UP_EXIT:-0}" = 0 || exit "$FAKE_COMPOSE_UP_EXIT"
                      if test "${FAKE_SAME_CONTAINER:-0}" != 1; then
                        printf '%s\n' "worker-new-${FAKE_CONTAINER_SUFFIX:-1}" > "$FAKE_RUNTIME/container"
                      fi
                      : > "$FAKE_RUNTIME/running"
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
                    reference=${FAKE_AFTER_IMAGE:-$(sed -n 's/^OPSI_BOOTSTRAP_WORKER_IMAGE=//p' "$FAKE_COMPOSE_DIR/.env")}
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
        curl.write_text("#!/usr/bin/env bash\nprintf '%s\\n' \"$*\" >> \"$FAKE_LOG\"\nexit \"${FAKE_CURL_EXIT:-0}\"\n", encoding="utf-8")
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

    def write_barrier(self, session_id: str = "boot-test", run_id: str = "run-test", state: str = "armed") -> pathlib.Path:
        config = {
            "cloud_url": "http://cloud:9800",
            "allow_insecure_internal_cloud_url": False,
            "production": False,
            "staging_crash_barrier": {
                "enabled": True,
                "environment": "e2e",
                "session_id": session_id,
                "run_id": run_id,
                "step": "install_k3s",
                "boundary": "after_execute_before_checkpoint",
                "state_dir": "/var/lib/opsi/bootstrap-barrier",
            },
        }
        config_path = self.compose / "config/bootstrap-worker.e2e.json"
        config_path.write_text(json.dumps(config), encoding="utf-8")
        config_path.chmod(0o600)
        marker_name = "install_k3s-" + hashlib.sha256((session_id + "\0" + run_id).encode()).hexdigest()[:32] + ".json"
        marker = self.compose / "barrier-state" / marker_name
        marker.write_text(
            json.dumps(
                {
                    "version": 1,
                    "environment": "e2e",
                    "session_id": session_id,
                    "run_id": run_id,
                    "step": "install_k3s",
                    "boundary": "after_execute_before_checkpoint",
                    "state": state,
                    **({"process_id": "worker-process-1"} if state != "armed" else {}),
                }
            ),
            encoding="utf-8",
        )
        marker.chmod(0o600)
        return marker

    def command(self, operation: str, target: pathlib.Path | str, expected: str, *extra: str) -> list[str]:
        command = [sys.executable, str(HELPER), operation]
        if operation == "deploy":
            option = "--manifest" if isinstance(target, pathlib.Path) else "--image"
            command.extend((option, str(target)))
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
                "FAKE_RUNTIME": str(self.runtime),
                "FAKE_RUNNING_IMAGE": changes.pop("FAKE_RUNNING_IMAGE", REF_A),
            }
        )
        env.update(changes)
        return subprocess.run(command, text=True, capture_output=True, env=env, check=False)

    def force_command(self, target: str = REF_A, override: str = "compose.e2e-bootstrap-barrier.yaml") -> list[str]:
        command = self.command("deploy", target, DIGEST_A, "--force-recreate-same-image")
        if override:
            command.extend(("--compose-file", override))
        return command

    def barrier_command(self, operation: str, session_id: str = "boot-test", run_id: str = "run-test") -> list[str]:
        return [
            sys.executable,
            str(HELPER),
            operation,
            "--expected-current-digest",
            DIGEST_A,
            "--compose-project",
            "opsi-staging",
            "--compose-directory",
            str(self.compose),
            "--service",
            "bootstrap-worker",
            "--health-timeout",
            "1",
            *((["--compose-file", "compose.e2e-bootstrap-barrier.yaml"] if operation == "barrier-replay" else [])),
            *((["--barrier-session-id", session_id, "--barrier-run-id", run_id]) if operation == "barrier-replay" else []),
        ]

    def manifest_a(self) -> pathlib.Path:
        return self.write_manifest(manifest(image_digest=DIGEST_A, image_reference=REF_A))

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

    def test_normal_same_image_deploy_remains_a_noop(self) -> None:
        self.write_env(REF_A)
        before = (self.compose / ".env").read_bytes()
        result = self.run_helper(self.command("deploy", self.manifest_a(), DIGEST_A))
        self.assertEqual(result.returncode, 0, result.stderr)
        log = self.log.read_text(encoding="utf-8")
        self.assertNotIn(" pull ", " " + log)
        self.assertNotIn(" up ", " " + log)
        self.assertEqual((self.compose / ".env").read_bytes(), before)
        self.assertFalse(list(self.compose.glob(".env.bootstrap-worker-release.*.bak")))
        self.assertIn("result=same-image-no-op", result.stdout)

    def test_same_image_force_requires_only_the_canonical_override(self) -> None:
        self.write_env(REF_A)
        self.write_barrier()
        cases = {
            "missing": "",
            "wrong-name": "wrong.yaml",
            "traversal": "../outside.yaml",
            "outside": str(self.root / "outside.yaml"),
            "absolute-canonical": str(self.compose / "compose.e2e-bootstrap-barrier.yaml"),
        }
        (self.root / "outside.yaml").write_text("services: {}\n", encoding="utf-8")
        for name, override in cases.items():
            with self.subTest(name=name):
                self.log.unlink(missing_ok=True)
                result = self.run_helper(self.force_command(override=override))
                self.assertNotEqual(result.returncode, 0)
                if self.log.exists():
                    self.assertNotIn(" up ", " " + self.log.read_text(encoding="utf-8"))

        canonical = self.compose / "compose.e2e-bootstrap-barrier.yaml"
        canonical.unlink()
        canonical.symlink_to(self.compose / "wrong.yaml")
        result = self.run_helper(self.force_command())
        self.assertNotEqual(result.returncode, 0)

    def test_same_image_force_rejects_invalid_config_and_marker(self) -> None:
        self.write_env(REF_A)
        marker = self.write_barrier()
        config = self.compose / "config/bootstrap-worker.e2e.json"

        config.unlink()
        missing = self.run_helper(self.force_command())
        self.assertNotEqual(missing.returncode, 0)

        marker.unlink(missing_ok=True)
        self.write_barrier()
        config.chmod(0o644)
        insecure = self.run_helper(self.force_command())
        self.assertNotEqual(insecure.returncode, 0)

        marker = self.write_barrier()
        value = json.loads(config.read_text(encoding="utf-8"))
        value["cloud_url"] = "https://REPLACE_WITH_HOST"
        config.write_text(json.dumps(value), encoding="utf-8")
        config.chmod(0o600)
        placeholder = self.run_helper(self.force_command())
        self.assertNotEqual(placeholder.returncode, 0)

        marker = self.write_barrier()
        marker.unlink()
        absent = self.run_helper(self.force_command())
        self.assertNotEqual(absent.returncode, 0)

        marker = self.write_barrier(state="reached")
        not_armed = self.run_helper(self.force_command())
        self.assertNotEqual(not_armed.returncode, 0)

        marker = self.write_barrier()
        payload = json.loads(marker.read_text(encoding="utf-8"))
        payload["run_id"] = "run-other"
        marker.write_text(json.dumps(payload), encoding="utf-8")
        marker.chmod(0o600)
        mismatch = self.run_helper(self.force_command())
        self.assertNotEqual(mismatch.returncode, 0)

    def test_same_image_force_rejects_target_and_precondition_mismatches_before_mutation(self) -> None:
        self.write_env(REF_A)
        target = self.run_helper(self.force_command(REF_B))
        self.assertNotEqual(target.returncode, 0)
        self.assertNotIn(" up ", " " + self.log.read_text(encoding="utf-8"))

        self.log.unlink()
        running = self.run_helper(self.force_command(), FAKE_RUNNING_IMAGE=REF_B)
        self.assertNotEqual(running.returncode, 0)
        self.assertNotIn(" up ", " " + self.log.read_text(encoding="utf-8"))

        self.log.unlink()
        self.write_env(REF_B)
        binding = self.run_helper(self.force_command())
        self.assertNotEqual(binding.returncode, 0)
        self.assertNotIn(" up ", " " + self.log.read_text(encoding="utf-8"))

    def test_valid_same_image_force_recreates_one_worker_without_binding_mutation(self) -> None:
        self.write_env(REF_A)
        self.write_barrier()
        before = (self.compose / ".env").read_bytes()
        result = self.run_helper(self.force_command())
        self.assertEqual(result.returncode, 0, result.stderr)
        log = self.log.read_text(encoding="utf-8")
        self.assertEqual(log.count("up -d --no-deps --force-recreate bootstrap-worker"), 1)
        self.assertNotIn(" pull ", " " + log)
        self.assertNotIn(" cloud", log)
        self.assertNotIn(" postgres", log)
        self.assertNotIn(" reverse-proxy", log)
        self.assertEqual((self.compose / ".env").read_bytes(), before)
        self.assertFalse(list(self.compose.glob(".env.bootstrap-worker-release.*.bak")))
        self.assertIn("previous_container_id=worker-old", result.stdout)
        self.assertIn("container_id=worker-new-1", result.stdout)
        self.assertIn("result=same-image-barrier-recreated", result.stdout)

    def test_same_image_force_fails_when_container_digest_or_health_evidence_is_wrong(self) -> None:
        self.write_env(REF_A)
        self.write_barrier()
        unchanged = self.run_helper(self.force_command(), FAKE_SAME_CONTAINER="1")
        self.assertNotEqual(unchanged.returncode, 0)

        self.state.unlink(missing_ok=True)
        (self.runtime / "container").write_text("worker-old", encoding="utf-8")
        self.log.unlink(missing_ok=True)
        wrong_digest = self.run_helper(self.force_command(), FAKE_AFTER_IMAGE=REF_B)
        self.assertNotEqual(wrong_digest.returncode, 0)

        self.state.unlink(missing_ok=True)
        (self.runtime / "container").write_text("worker-old", encoding="utf-8")
        self.log.unlink(missing_ok=True)
        unhealthy = self.run_helper(self.force_command(), FAKE_HEALTH="unhealthy")
        self.assertNotEqual(unhealthy.returncode, 0)

        self.state.unlink(missing_ok=True)
        (self.runtime / "container").write_text("worker-old", encoding="utf-8")
        self.log.unlink(missing_ok=True)
        cloud_down = self.run_helper(self.force_command(), FAKE_CURL_EXIT="22")
        self.assertNotEqual(cloud_down.returncode, 0)

    def test_barrier_replay_requires_reached_marker_and_recreates_via_helper(self) -> None:
        self.write_env(REF_A)
        for state in ("armed", "consumed", "completed"):
            self.write_barrier(state=state)
            rejected = self.run_helper(self.barrier_command("barrier-replay"))
            self.assertNotEqual(rejected.returncode, 0)
            self.log.unlink(missing_ok=True)
        self.write_barrier(state="reached")
        result = self.run_helper(self.barrier_command("barrier-replay"))
        self.assertEqual(result.returncode, 0, result.stderr)
        log = self.log.read_text(encoding="utf-8")
        self.assertIn("compose.e2e-bootstrap-barrier.yaml", log)
        self.assertNotIn(" pull ", " " + log)
        self.assertIn("result=barrier-replay-recreated", result.stdout)

    def test_barrier_restore_uses_normal_compose_without_binding_mutation(self) -> None:
        self.write_env(REF_A)
        before = (self.compose / ".env").read_bytes()
        result = self.run_helper(self.barrier_command("barrier-restore"))
        self.assertEqual(result.returncode, 0, result.stderr)
        log = self.log.read_text(encoding="utf-8")
        self.assertIn("compose.yaml", log)
        self.assertNotIn("compose.e2e-bootstrap-barrier.yaml", log)
        self.assertNotIn(" pull ", " " + log)
        self.assertFalse(list(self.compose.glob(".env.bootstrap-worker-release.*.bak")))
        self.assertEqual((self.compose / ".env").read_bytes(), before)
        self.assertIn("result=normal-same-image-restored", result.stdout)

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


class BarrierProcedureTests(unittest.TestCase):
    def setUp(self) -> None:
        self.temp = tempfile.TemporaryDirectory()
        self.root = pathlib.Path(self.temp.name)
        self.compose = self.root / "staging-control-plane"
        (self.compose / "config").mkdir(parents=True)
        (self.compose / "barrier-state").mkdir(mode=0o700)
        (self.compose / "compose.yaml").write_text("services:\n  bootstrap-worker: {}\n", encoding="utf-8")
        (self.compose / "compose.e2e-bootstrap-barrier.yaml").write_text("services:\n  bootstrap-worker: {}\n", encoding="utf-8")
        (self.compose / ".env").write_text("OPSI_BOOTSTRAP_WORKER_IMAGE=" + REF_A + "\n", encoding="utf-8")
        (self.compose / "config/bootstrap-worker.json").write_text(
            json.dumps({"cloud_url": "http://cloud:9800", "allow_insecure_internal_cloud_url": True, "production": True, "bootstrap_worker_token_file": "/run/secrets/bootstrap-worker-token"}),
            encoding="utf-8",
        )
        self.bin = self.root / "bin"
        self.bin.mkdir()
        self.runtime = self.root / "runtime"
        self.runtime.mkdir()
        (self.runtime / "container").write_text("worker-old", encoding="utf-8")
        (self.runtime / "counter").write_text("0", encoding="utf-8")
        (self.runtime / "running").touch()
        self.log = self.root / "commands.log"
        self.artifacts = self.root / "artifacts"
        self.artifacts.mkdir(mode=0o700)
        self.state_parent = self.root / "protected"
        self.state_parent.mkdir(mode=0o700)
        self.state = self.state_parent / "bootstrap-state.json"
        self.key = self.root / "operator-key"
        pem_marker = "-----BEGIN OPENSSH " + "PRIVATE KEY-----"
        pem_end = "-----END OPENSSH " + "PRIVATE KEY-----"
        self.key.write_text(pem_marker + "\nfixture\n" + pem_end + "\n", encoding="utf-8")
        self.key.chmod(0o600)
        self.marker = self.compose / ("barrier-state/install_k3s-" + hashlib.sha256(b"boot-factual\0run-order").hexdigest()[:32] + ".json")
        self.write_fake_tools()

    def tearDown(self) -> None:
        self.temp.cleanup()

    def write_fake_tools(self) -> None:
        docker = self.bin / "docker"
        docker.write_text(
            textwrap.dedent(
                r'''
                #!/usr/bin/env bash
                set -eu
                printf 'docker %s\n' "$*" >> "$FAKE_LOG"
                if test "$1" = compose; then
                  case " $* " in
                    *" ps -a -q bootstrap-worker "*) cat "$FAKE_RUNTIME/container" ;;
                    *" ps -q bootstrap-worker "*) test ! -e "$FAKE_RUNTIME/running" || cat "$FAKE_RUNTIME/container" ;;
                    *" stop bootstrap-worker "*) rm -f "$FAKE_RUNTIME/running" ;;
                    *" up -d --no-deps --force-recreate bootstrap-worker "*)
                      if [[ "$*" == *compose.e2e-bootstrap-barrier.yaml* ]]; then
                        if test "${FAKE_BARRIER_REACH_BEFORE_FAIL:-0}" = 1; then
                          python3 - "$FAKE_MARKER" <<'PY'
import json, sys
path = sys.argv[1]
data = json.load(open(path))
data.update({"state": "reached", "process_id": "worker-process-1"})
json.dump(data, open(path, "w"), separators=(",", ":"))
PY
                        fi
                        test "${FAKE_BARRIER_UP_EXIT:-0}" = 0 || exit "$FAKE_BARRIER_UP_EXIT"
                        test -f "$FAKE_MARKER" || exit 31
                        grep -Eq '"state"[[:space:]]*:[[:space:]]*"(armed|reached)"' "$FAKE_MARKER" || exit 32
                      else
                        test "${FAKE_RESTORE_EXIT:-0}" = 0 || exit "$FAKE_RESTORE_EXIT"
                      fi
                      counter=$(( $(cat "$FAKE_RUNTIME/counter") + 1 ))
                      printf '%s\n' "$counter" > "$FAKE_RUNTIME/counter"
                      printf 'worker-new-%s\n' "$counter" > "$FAKE_RUNTIME/container"
                      : > "$FAKE_RUNTIME/running"
                      ;;
                    *) exit 9 ;;
                  esac
                elif test "$1" = inspect; then
                  case " $* " in
                    *State.Health*) printf '%s\n' "${FAKE_HEALTH:-healthy}" ;;
                    *) printf 'image-id\n' ;;
                  esac
                elif test "$1" = image; then
                  printf '["%s"]\n' "${FAKE_IMAGE:-$FAKE_RUNNING_IMAGE}"
                else
                  exit 9
                fi
                '''
            ).lstrip(),
            encoding="utf-8",
        )
        curl = self.bin / "curl"
        curl.write_text(
            textwrap.dedent(
                r'''
                #!/usr/bin/env bash
                set -eu
                original="$*"
                printf 'curl %s\n' "$original" >> "$FAKE_LOG"
                if [[ "$*" == *"/health"* ]]; then exit "${FAKE_HEALTH_CURL_EXIT:-0}"; fi
                if [[ "$*" == *"/api/local/session"* ]]; then printf '%s\n' '{"local_session":"local-session"}'; exit 0; fi
                out=""; status=200; previous=""
                while test "$#" -gt 0; do
                  case "$1" in
                    -o) out="$2"; shift 2 ;;
                    -w) shift 2 ;;
                    -X) previous="$2"; shift 2 ;;
                    *) shift ;;
                  esac
                done
                if test "$previous" = POST; then
                  status=201
                  test "${FAKE_SESSION_CREATE_EXIT:-0}" = 0 || exit "$FAKE_SESSION_CREATE_EXIT"
                  test -z "$out" || printf '%s\n' '{"id":"boot-factual","status":"pending"}' > "$out"
                  printf '%s' "$status"
                  exit 0
                fi
                if [[ "$original" == *"bootstrap-sessions/boot-factual"* ]]; then
                  test -z "$out" || printf '%s\n' '{"id":"boot-factual","status":"completed"}' > "$out"
                fi
                printf '%s' "$status"
                exit 0
                '''
            ).lstrip(),
            encoding="utf-8",
        )
        docker.chmod(0o755)
        curl.chmod(0o755)

    def environment(self, **changes: str) -> dict[str, str]:
        env = os.environ.copy()
        env.update(
            {
                "PATH": str(self.bin) + os.pathsep + env["PATH"],
                "FAKE_LOG": str(self.log),
                "FAKE_RUNTIME": str(self.runtime),
                "FAKE_MARKER": str(self.marker),
                "FAKE_RUNNING_IMAGE": REF_A,
                "OPSI_E2E_PROJECT_ID": "project-1",
                "OPSI_E2E_LOCAL_URL": "http://local",
                "OPSI_E2E_VPS_HOST": "fixture-host",
                "OPSI_E2E_VPS_SSH_USER": "operator",
                "OPSI_E2E_SSH_KEY_PATH": str(self.key),
                "OPSI_E2E_BOOTSTRAP_WORKER_DIGEST": DIGEST_A,
                "OPSI_E2E_RUN_ID": "run-order",
                "OPSI_E2E_ARTIFACT_DIR": str(self.artifacts),
                "OPSI_E2E_STAGING_COMPOSE_DIRECTORY": str(self.compose),
            }
        )
        env.update(changes)
        return env

    def run_mode(self, mode: str, **changes: str) -> subprocess.CompletedProcess[str]:
        return subprocess.run(
            [str(ROOT / "scripts/e2e/verify-k3s.sh"), mode, str(self.state)],
            text=True,
            capture_output=True,
            env=self.environment(**changes),
            check=False,
        )

    def run_prepare(self, **changes: str) -> subprocess.CompletedProcess[str]:
        return self.run_mode("--barrier-prepare", **changes)

    def write_marker(self, state: str, **changes: str) -> None:
        payload = {
            "version": 1,
            "environment": "e2e",
            "session_id": "boot-factual",
            "run_id": "run-order",
            "step": "install_k3s",
            "boundary": "after_execute_before_checkpoint",
            "state": state,
        }
        if state != "armed":
            payload["process_id"] = "worker-process-1"
        payload.update(changes)
        self.marker.write_text(json.dumps(payload, separators=(",", ":")), encoding="utf-8")
        self.marker.chmod(0o600)

    def test_quiesce_session_arm_and_start_order_is_factual(self) -> None:
        result = self.run_prepare()
        self.assertEqual(result.returncode, 0, result.stderr)
        log = self.log.read_text(encoding="utf-8")
        self.assertLess(log.index("stop bootstrap-worker"), log.index("/nodes/bootstrap"))
        self.assertLess(log.index("/nodes/bootstrap"), log.index("up -d --no-deps --force-recreate bootstrap-worker"))
        self.assertNotIn("lease", log)
        state = json.loads(self.state.read_text(encoding="utf-8"))
        self.assertEqual(state["session_id"], "boot-factual")
        self.assertEqual(state["run_id"], "run-order")
        self.assertEqual(state["phase"], "barrier_started")
        self.assertNotIn("PRIVATE KEY", self.log.read_text(encoding="utf-8"))
        self.assertNotIn("local-session", result.stdout + result.stderr)

    def test_session_creation_failure_restores_normal_worker(self) -> None:
        result = self.run_prepare(FAKE_SESSION_CREATE_EXIT="17")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("barrier failure restoration: normal Worker profile restored", result.stdout)
        self.assertTrue((self.runtime / "running").exists())
        self.assertFalse((self.compose / "config/bootstrap-worker.e2e.json").exists())

    def test_arm_failure_restores_normal_worker(self) -> None:
        marker = self.compose / ("barrier-state/install_k3s-" + hashlib.sha256(b"boot-factual\0run-order").hexdigest()[:32] + ".json")
        marker.write_text('{"version":1,"state":"armed"}', encoding="utf-8")
        marker.chmod(0o600)
        result = self.run_prepare()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("barrier failure restoration: normal Worker profile restored", result.stdout)
        self.assertTrue((self.runtime / "running").exists())

    def test_barrier_recreate_failure_restores_normal_worker(self) -> None:
        result = self.run_prepare(FAKE_BARRIER_UP_EXIT="19")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("barrier failure restoration: normal Worker profile restored", result.stdout)
        self.assertTrue((self.runtime / "running").exists())
        self.assertFalse((self.compose / "config/bootstrap-worker.e2e.json").exists())
        self.assertFalse(list((self.compose / "barrier-state").glob("install_k3s-*.json")))

    def test_failure_after_reached_preserves_marker_and_generated_config(self) -> None:
        result = self.run_prepare(FAKE_BARRIER_UP_EXIT="19", FAKE_BARRIER_REACH_BEFORE_FAIL="1")
        self.assertNotEqual(result.returncode, 0)
        marker = self.compose / ("barrier-state/install_k3s-" + hashlib.sha256(b"boot-factual\0run-order").hexdigest()[:32] + ".json")
        self.assertEqual(json.loads(marker.read_text(encoding="utf-8"))["state"], "reached")
        self.assertTrue((self.compose / "config/bootstrap-worker.e2e.json").exists())

    def test_restoration_failure_is_reported_separately(self) -> None:
        result = self.run_prepare(FAKE_SESSION_CREATE_EXIT="17", FAKE_RESTORE_EXIT="23")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("barrier failure restoration failed", result.stdout)
        self.assertFalse((self.runtime / "running").exists())

    def test_resume_uses_existing_session_without_posting_a_second_one(self) -> None:
        prepared = self.run_prepare()
        self.assertEqual(prepared.returncode, 0, prepared.stderr)
        state = json.loads(self.state.read_text(encoding="utf-8"))
        state["phase"] = "replay_started"
        self.state.write_text(json.dumps(state), encoding="utf-8")
        self.state.chmod(0o600)
        self.write_marker("consumed")
        before = self.log.read_text(encoding="utf-8").count("/nodes/bootstrap")
        resumed = self.run_mode("--resume-bootstrap-session", OPSI_E2E_BARRIER_HANDOFF_ONLY="1")
        self.assertEqual(resumed.returncode, 0, resumed.stderr + resumed.stdout)
        self.assertEqual(self.log.read_text(encoding="utf-8").count("/nodes/bootstrap"), before)
        self.assertIn("without creating a second session", resumed.stdout)
        self.assertEqual(json.loads(self.state.read_text(encoding="utf-8"))["phase"], "consumed")

        self.write_marker("completed")
        resumed = self.run_mode("--resume-bootstrap-session", OPSI_E2E_BARRIER_HANDOFF_ONLY="1")
        self.assertEqual(resumed.returncode, 0, resumed.stderr + resumed.stdout)
        self.assertEqual(json.loads(self.state.read_text(encoding="utf-8"))["phase"], "completed")
        self.assertEqual(self.log.read_text(encoding="utf-8").count("/nodes/bootstrap"), before)

    def test_full_barrier_replay_reconciles_completed_marker_and_restores_base_profile(self) -> None:
        prepared = self.run_mode("--barrier-prepare")
        self.assertEqual(prepared.returncode, 0, prepared.stderr + prepared.stdout)
        prepared_container = (self.runtime / "container").read_text(encoding="utf-8").strip()
        self.write_marker("reached")

        restarted = self.run_mode("--barrier-restart")
        self.assertEqual(restarted.returncode, 0, restarted.stderr + restarted.stdout)
        replay_container = (self.runtime / "container").read_text(encoding="utf-8").strip()
        self.assertNotEqual(replay_container, prepared_container)

        self.write_marker("completed")
        resumed = self.run_mode("--resume-bootstrap-session", OPSI_E2E_BARRIER_HANDOFF_ONLY="1")
        self.assertEqual(resumed.returncode, 0, resumed.stderr + resumed.stdout)
        self.assertEqual(json.loads(self.state.read_text(encoding="utf-8"))["phase"], "completed")

        restored = self.run_mode("--barrier-restore")
        self.assertEqual(restored.returncode, 0, restored.stderr + restored.stdout)
        restored_container = (self.runtime / "container").read_text(encoding="utf-8").strip()
        self.assertNotEqual(restored_container, replay_container)
        self.assertEqual(json.loads(self.state.read_text(encoding="utf-8"))["phase"], "normal_restored")

        log = self.log.read_text(encoding="utf-8")
        self.assertEqual(log.count("/nodes/bootstrap"), 1)
        recreates = [line for line in log.splitlines() if "up -d --no-deps --force-recreate bootstrap-worker" in line]
        self.assertEqual(len(recreates), 3, log)
        self.assertEqual(recreates[1].count("compose.e2e-bootstrap-barrier.yaml"), 1)
        self.assertIn("compose.yaml", recreates[2])
        self.assertNotIn("compose.e2e-bootstrap-barrier.yaml", recreates[2])
        self.assertNotRegex(recreates[2], r"\b(cloud|postgres|reverse-proxy)\b")

        orchestration = (ROOT / "scripts/e2e/verify-k3s.sh").read_text(encoding="utf-8")
        for start, end in (("barrier_restart()", "barrier_restore()"), ("barrier_restore()", "resume_bootstrap_session()")):
            body = orchestration.split(start, 1)[1].split(end, 1)[0]
            self.assertNotIn("docker compose", body)
        self.assertIn('python3 "$RELEASE_HELPER" barrier-replay', orchestration)
        self.assertIn("restore_normal_worker", orchestration.split("barrier_restore()", 1)[1].split("resume_bootstrap_session()", 1)[0])

    def test_reached_marker_cannot_be_promoted_by_completed_api_status(self) -> None:
        self.assertEqual(self.run_mode("--barrier-prepare").returncode, 0)
        self.write_marker("reached")
        self.assertEqual(self.run_mode("--barrier-restart").returncode, 0)

        resumed = self.run_mode("--resume-bootstrap-session", OPSI_E2E_BARRIER_HANDOFF_ONLY="1")
        self.assertNotEqual(resumed.returncode, 0)
        self.assertEqual(json.loads(self.state.read_text(encoding="utf-8"))["phase"], "replay_started")
        self.assertEqual(self.log.read_text(encoding="utf-8").count("/nodes/bootstrap"), 1)

    def test_reconciliation_rejects_wrong_marker_identity_and_process(self) -> None:
        self.assertEqual(self.run_mode("--barrier-prepare").returncode, 0)
        self.write_marker("reached")
        self.assertEqual(self.run_mode("--barrier-restart").returncode, 0)

        for changes in ({"session_id": "boot-other"}, {"run_id": "run-other"}, {"process_id": ""}):
            with self.subTest(changes=changes):
                self.write_marker("completed", **changes)
                resumed = self.run_mode("--resume-bootstrap-session", OPSI_E2E_BARRIER_HANDOFF_ONLY="1")
                self.assertNotEqual(resumed.returncode, 0)
                self.assertEqual(json.loads(self.state.read_text(encoding="utf-8"))["phase"], "replay_started")


if __name__ == "__main__":
    unittest.main(verbosity=2)
