#!/usr/bin/env python3
"""Self-checks for Bootstrap Worker deployment and barrier operations."""

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

    def test_worker_manifest_remains_deployment_compatible(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            output = pathlib.Path(raw) / "worker-deploy-release.json"
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
        git = self.bin / "git"
        git.write_text(
            "#!/usr/bin/env bash\nset -eu\ncase \"$*\" in *'remote get-url origin'*) echo fixture-origin;; *':scripts/e2e/staging-barrier-remote.sh'*) printf '%040d\\n' 0 | tr 0 b;; *'rev-parse HEAD'*) printf '%040d\\n' 0 | tr 0 c;; *'diff --quiet --exit-code'*|*'diff --cached --quiet --exit-code'*) exit 0;; *) exit 2;; esac\n",
            encoding="utf-8",
        )
        ssh_keygen = self.bin / "ssh-keygen"
        ssh_keygen.write_text("#!/usr/bin/env bash\necho '256 SHA256:" + "A" * 43 + " fixture (ED25519)'\n", encoding="utf-8")
        ssh = self.bin / "ssh"
        ssh.write_text(
            textwrap.dedent(
                r'''
                #!/usr/bin/env bash
                set -eu
                ulimit -f unlimited
                root="$(dirname "$0")/.."
                printf 'ssh-argv %s\n' "$*" >> "$root/commands.log"
                env | LC_ALL=C sort | sed 's/^/ssh-env /' >> "$root/commands.log"
                request="$(cat)"
                printf 'ssh-stdin %s\n' "$request" >> "$root/commands.log"
                mode="normal"; test ! -e "$root/ssh-mode" || mode="$(cat "$root/ssh-mode")"
                case "$mode" in
                  empty) exit 0 ;;
                  stderr) echo diagnostic >&2; exit 0 ;;
                  malformed) echo '{'; exit 0 ;;
                  multiple) echo '{}{}'; exit 0 ;;
                  oversized) python3 -c 'print("x" * 4097)'; exit 0 ;;
                  nonzero) exit 19 ;;
                esac
                python3 - "$root" "$mode" "$request" <<'PY'
import json, pathlib, sys
root, mode, raw = pathlib.Path(sys.argv[1]), sys.argv[2], sys.argv[3]
request = json.loads(raw)
state_path = root / "remote-state.json"
state = json.loads(state_path.read_text())
phase, before, container_before = request["phase"], state["phase"], state["container"]
if mode == "abort-fail" and phase == "abort": raise SystemExit(23)
if phase == "preflight": after, result = "absent", "preflight-ok"
elif phase == "prepare":
    state = {"phase":"worker_quiesced","session":"","container":"worker-old"}; after, result = state["phase"], "worker-quiesced"
elif phase == "start":
    state = {"phase":"barrier_started","session":request["session_id"],"container":"worker-barrier"}; after, result = state["phase"], "barrier-started"
elif phase == "restart":
    state = {"phase":"replay_started","session":request["session_id"],"container":"worker-replay"}; after, result = state["phase"], "replay-started"
elif phase == "restore":
    state = {"phase":"normal_restored","session":request["session_id"],"container":"worker-normal"}; after, result = state["phase"], "normal-restored"
elif phase == "abort":
    state = {"phase":"absent","session":"","container":"worker-normal"}; after, result = "absent", "pre-session-aborted"
else: after, result = state["phase"], "status-ok"
state_path.write_text(json.dumps(state))
marker_path = root / "remote-marker"
marker = marker_path.read_text().strip() if marker_path.exists() else {"barrier_started":"armed","replay_started":"reached","normal_restored":"completed"}.get(after, "absent")
receipt = {
    "schema_version":"opsi.e2e.staging-barrier-receipt/v1","source_revision":request["source_revision"],
    "run_id":request["run_id"],"phase":phase,"staging_host":request["staging_host"],
    "repository_directory":request["repository_directory"],"repository_identity":request["repository_identity"],
    "compose_directory":request["compose_directory"],"helper_blob":"b"*40,
    "state_before":before,"state_after":after,"worker_digest":request["worker_digest"],
    "session_id":request["session_id"],"worker_container_before":container_before,
    "worker_container_after":state["container"] if after != "absent" or phase == "abort" else "worker-old",
    "marker_state":marker,"result":result,"timestamp":"2026-08-03T12:00:00Z",
}
wrong = {"wrong-schema":("schema_version","wrong"),"wrong-run":("run_id","other-run"),"wrong-phase":("phase","status"),"wrong-revision":("source_revision","d"*40),"wrong-host":("staging_host","other.example")}
if mode in wrong: receipt[wrong[mode][0]] = wrong[mode][1]
print(json.dumps(receipt, separators=(",", ":"), sort_keys=True))
if mode in {"ambiguous-start","timeout-start"} and phase == "start":
    (root / "ssh-mode").unlink(); raise SystemExit(255 if mode.startswith("ambiguous") else 124)
if mode == "start-fail-session" and phase == "start":
    state_path.write_text(json.dumps({"phase":"session_created","session":request["session_id"],"container":"worker-old"}))
    (root / "ssh-mode").unlink(); raise SystemExit(19)
PY
                '''
            ).lstrip(),
            encoding="utf-8",
        )
        for tool in (docker, curl, git, ssh_keygen, ssh):
            tool.chmod(0o755)

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
        self.bin = self.root / "bin"
        self.bin.mkdir()
        self.log = self.root / "commands.log"
        self.remote_state = self.root / "remote-state.json"
        self.remote_state.write_text(json.dumps({"phase": "absent", "session": "", "container": "worker-old"}), encoding="utf-8")
        self.remote_marker = self.root / "remote-marker"
        self.ssh_mode = self.root / "ssh-mode"
        self.artifacts = self.root / "artifacts"
        self.artifacts.mkdir(mode=0o700)
        self.state_parent = self.root / "protected"
        self.state_parent.mkdir(mode=0o700)
        self.state = self.state_parent / "bootstrap-state.json"
        self.key = self.root / "agent-key"
        self.staging_key = self.root / "staging-key"
        self.known_hosts = self.root / "staging-known-hosts"
        pem_marker = "-----BEGIN OPENSSH " + "PRIVATE KEY-----"
        pem_end = "-----END OPENSSH " + "PRIVATE KEY-----"
        self.key.write_text(pem_marker + "\nfixture\n" + pem_end + "\n", encoding="utf-8")
        self.staging_key.write_text(pem_marker + "\nstaging-fixture\n" + pem_end + "\n", encoding="utf-8")
        self.known_hosts.write_text("staging.example ssh-ed25519 AAAA\n", encoding="utf-8")
        self.key.chmod(0o600)
        self.staging_key.chmod(0o600)
        self.known_hosts.chmod(0o600)
        self.write_fake_tools()

    def tearDown(self) -> None:
        self.temp.cleanup()

    def write_fake_tools(self) -> None:
        RuntimeTests.write_fake_tools(self)
        docker = self.bin / "docker"
        docker.write_text(
            textwrap.dedent(
                r'''
                #!/usr/bin/env bash
                set -eu
                printf 'LOCAL_DOCKER_CANARY %s\n' "$*" >> "$(dirname "$0")/../commands.log"
                exit 99
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
                printf 'curl %s\n' "$original" >> "$(dirname "$0")/../commands.log"
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
                  test ! -e "$(dirname "$0")/../session-create-ambiguous" || exit 17
                  if test -e "$(dirname "$0")/../session-create-fail"; then
                    test -z "$out" || printf '%s\n' '{"error":"rejected"}' > "$out"
                    printf '500'
                    exit 0
                  fi
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
                "OPSI_E2E_PROJECT_ID": "project-1",
                "OPSI_E2E_LOCAL_URL": "http://local",
                "OPSI_E2E_VPS_HOST": "fixture-host",
                "OPSI_E2E_VPS_SSH_USER": "operator",
                "OPSI_E2E_SSH_KEY_PATH": str(self.key),
                "OPSI_E2E_BOOTSTRAP_WORKER_DIGEST": DIGEST_A,
                "OPSI_E2E_RUN_ID": "run-order",
                "OPSI_E2E_ARTIFACT_DIR": str(self.artifacts),
                "OPSI_E2E_STAGING_HOST": "staging.example",
                "OPSI_E2E_STAGING_SSH_PORT": "22",
                "OPSI_E2E_STAGING_SSH_USER": "staging-user",
                "OPSI_E2E_STAGING_SSH_KEY_PATH": str(self.staging_key),
                "OPSI_E2E_STAGING_KNOWN_HOSTS_PATH": str(self.known_hosts),
                "OPSI_E2E_STAGING_HOST_KEY_SHA256": "SHA256:" + "A" * 43,
                "OPSI_E2E_STAGING_REPOSITORY_DIRECTORY": "/srv/opsi",
                "OPSI_E2E_STAGING_COMPOSE_DIRECTORY": "/srv/opsi/deploy/staging-control-plane",
                "OPSI_E2E_SOURCE_REVISION": REVISION,
                "OPSI_LOCAL_SESSION_CANARY": "local-only-canary",
                "OPSI_SECOND_FACTOR_CANARY": "second-factor-canary",
                "SSH_AUTH_SOCK": "/tmp/ambient-agent-canary",
                "HTTPS_PROXY": "http://proxy-canary",
                "GIT_SSH_COMMAND": "git-ssh-command-canary",
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
        del changes
        self.remote_marker.write_text(state, encoding="utf-8")

    def test_quiesce_session_arm_and_start_order_is_factual(self) -> None:
        result = self.run_prepare()
        self.assertEqual(result.returncode, 0, result.stderr + result.stdout)
        log = self.log.read_text(encoding="utf-8")
        requests = [json.loads(line.removeprefix("ssh-stdin ")) for line in log.splitlines() if line.startswith("ssh-stdin ")]
        phases = [request["phase"] for request in requests]
        self.assertEqual(phases, ["preflight", "prepare", "start"])
        self.assertLess(log.index('"phase":"prepare"'), log.index("/nodes/bootstrap"))
        self.assertLess(log.index("/nodes/bootstrap"), log.index('"phase":"start"'))
        self.assertNotIn("LOCAL_DOCKER_CANARY", log)
        state = json.loads(self.state.read_text(encoding="utf-8"))
        self.assertEqual(state["session_id"], "boot-factual")
        self.assertEqual(state["run_id"], "run-order")
        self.assertEqual(state["phase"], "barrier_started")
        self.assertEqual(state["source_revision"], REVISION)
        self.assertEqual(state["staging_host"], "staging.example")
        self.assertNotIn("PRIVATE KEY", log)
        self.assertNotIn("local-session", result.stdout + result.stderr)

    def test_session_creation_failure_restores_normal_worker(self) -> None:
        (self.root / "session-create-fail").touch()
        result = self.run_prepare()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("remote normal Worker profile restored before session creation", result.stdout)
        self.assertEqual(json.loads(self.remote_state.read_text())["phase"], "absent")
        self.assertFalse(self.state.exists())

    def test_session_failure_preserves_primary_and_cleanup_errors(self) -> None:
        (self.root / "session-create-fail").touch()
        self.ssh_mode.write_text("abort-fail", encoding="utf-8")
        result = self.run_prepare()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("bootstrap session creation failed", result.stdout)
        self.assertIn("barrier failure restoration failed", result.stdout)
        self.assertEqual(json.loads(self.remote_state.read_text())["phase"], "worker_quiesced")

    def test_ambiguous_bootstrap_post_preserves_stopped_worker(self) -> None:
        (self.root / "session-create-ambiguous").touch()
        result = self.run_prepare()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("bootstrap POST result is ambiguous", result.stdout)
        self.assertEqual(json.loads(self.remote_state.read_text())["phase"], "worker_quiesced")
        self.assertFalse(self.state.exists())

    def test_post_session_failure_preserves_state_and_continues_without_second_post(self) -> None:
        self.ssh_mode.write_text("start-fail-session", encoding="utf-8")
        failed = self.run_prepare()
        self.assertNotEqual(failed.returncode, 0)
        self.assertEqual(json.loads(self.state.read_text())["phase"], "session_created")
        self.assertEqual(json.loads(self.remote_state.read_text())["phase"], "session_created")
        continued = self.run_prepare()
        self.assertEqual(continued.returncode, 0, continued.stderr + continued.stdout)
        self.assertIn("without a second bootstrap POST", continued.stdout)
        self.assertEqual(self.log.read_text().count("/nodes/bootstrap"), 1)

    def test_ambiguous_mutation_uses_status_reconciliation_only(self) -> None:
        self.ssh_mode.write_text("ambiguous-start", encoding="utf-8")
        result = self.run_prepare()
        self.assertEqual(result.returncode, 0, result.stderr + result.stdout)
        phases = [json.loads(line.removeprefix("ssh-stdin "))["phase"] for line in self.log.read_text().splitlines() if line.startswith("ssh-stdin ")]
        self.assertEqual(phases, ["preflight", "prepare", "start", "status"])
        self.assertEqual(phases.count("start"), 1)

    def test_timeout_mutation_uses_status_reconciliation_only(self) -> None:
        self.ssh_mode.write_text("timeout-start", encoding="utf-8")
        result = self.run_prepare()
        self.assertEqual(result.returncode, 0, result.stderr + result.stdout)
        phases = [json.loads(line.removeprefix("ssh-stdin "))["phase"] for line in self.log.read_text().splitlines() if line.startswith("ssh-stdin ")]
        self.assertEqual(phases, ["preflight", "prepare", "start", "status"])

    def test_pinned_ssh_options_and_sanitized_boundary(self) -> None:
        result = self.run_prepare()
        self.assertEqual(result.returncode, 0, result.stderr + result.stdout)
        log = self.log.read_text()
        boundary = "\n".join(line for line in log.splitlines() if line.startswith(("ssh-argv", "ssh-env", "ssh-stdin")))
        for option in ("-F /dev/null", "-T", "BatchMode=yes", "IdentitiesOnly=yes", "StrictHostKeyChecking=yes", "GlobalKnownHostsFile=/dev/null", "ForwardAgent=no", "ForwardX11=no", "ClearAllForwardings=yes", "ControlMaster=no", "PermitLocalCommand=no", "PasswordAuthentication=no", "KbdInteractiveAuthentication=no", "LogLevel=ERROR"):
            self.assertIn(option, log)
        for canary in ("local-only-canary", "second-factor-canary", "ambient-agent-canary", "proxy-canary", "git-ssh-command-canary", "local-session"):
            self.assertNotIn(canary, boundary + result.stdout + result.stderr)
        self.assertNotIn(str(self.key), boundary)

    def test_invalid_transport_and_receipts_fail_before_bootstrap(self) -> None:
        cases = (
            {"OPSI_E2E_STAGING_HOST": ""},
            {"OPSI_E2E_STAGING_HOST": "-unsafe"},
            {"OPSI_E2E_STAGING_SSH_PORT": "0"},
            {"OPSI_E2E_STAGING_SSH_USER": "bad user"},
            {"OPSI_E2E_RUN_ID": "bad run"},
            {"OPSI_E2E_SOURCE_REVISION": "ABC"},
            {"OPSI_E2E_STAGING_REPOSITORY_DIRECTORY": "/srv/../opsi"},
            {"OPSI_E2E_STAGING_COMPOSE_DIRECTORY": "/tmp/other"},
        )
        for changes in cases:
            with self.subTest(changes=changes):
                self.log.unlink(missing_ok=True)
                self.state.unlink(missing_ok=True)
                self.remote_state.write_text(json.dumps({"phase":"absent","session":"","container":"worker-old"}))
                result = self.run_prepare(**changes)
                self.assertNotEqual(result.returncode, 0)
                self.assertNotIn("/nodes/bootstrap", self.log.read_text() if self.log.exists() else "")
        for mode in ("empty", "stderr", "malformed", "multiple", "oversized", "nonzero", "wrong-schema", "wrong-run", "wrong-phase", "wrong-revision", "wrong-host"):
            with self.subTest(mode=mode):
                self.log.unlink(missing_ok=True)
                self.state.unlink(missing_ok=True)
                self.remote_state.write_text(json.dumps({"phase":"absent","session":"","container":"worker-old"}))
                self.ssh_mode.write_text(mode, encoding="utf-8")
                result = self.run_prepare()
                self.assertNotEqual(result.returncode, 0)
                self.assertNotIn("/nodes/bootstrap", self.log.read_text() if self.log.exists() else "")
                self.ssh_mode.unlink()

    def test_protected_staging_files_and_agent_key_separation(self) -> None:
        original_key = self.staging_key.read_text()
        original_known_hosts = self.known_hosts.read_text()
        self.staging_key.chmod(0o644)
        self.assertNotEqual(self.run_prepare().returncode, 0)
        self.staging_key.chmod(0o600)
        self.staging_key.write_text("")
        self.assertNotEqual(self.run_prepare().returncode, 0)
        self.staging_key.write_text("x" * (1024 * 1024 + 1))
        self.assertNotEqual(self.run_prepare().returncode, 0)
        self.staging_key.write_text(original_key)
        key_link = self.root / "staging-key-link"
        key_link.symlink_to(self.staging_key)
        self.assertNotEqual(self.run_prepare(OPSI_E2E_STAGING_SSH_KEY_PATH=str(key_link)).returncode, 0)

        if os.geteuid() == 0:
            wrong_owner = self.root / "wrong-owner-key"
            wrong_owner.write_text(original_key)
            wrong_owner.chmod(0o600)
            os.chown(wrong_owner, 65534, 65534)
        else:
            wrong_owner = pathlib.Path("/etc/hosts")
        wrong_owner_result = self.run_prepare(OPSI_E2E_STAGING_SSH_KEY_PATH=str(wrong_owner))
        self.assertNotEqual(wrong_owner_result.returncode, 0)
        self.assertIn("staging key is not an owned regular file", wrong_owner_result.stderr)

        self.known_hosts.chmod(0o644)
        self.assertNotEqual(self.run_prepare().returncode, 0)
        self.known_hosts.chmod(0o600)
        self.known_hosts.write_text("")
        self.assertNotEqual(self.run_prepare().returncode, 0)
        self.known_hosts.write_text("x" * 16385)
        self.assertNotEqual(self.run_prepare().returncode, 0)
        self.known_hosts.write_text(original_known_hosts)
        known_link = self.root / "known-hosts-link"
        known_link.symlink_to(self.known_hosts)
        self.assertNotEqual(self.run_prepare(OPSI_E2E_STAGING_KNOWN_HOSTS_PATH=str(known_link)).returncode, 0)
        self.known_hosts.write_text("staging.example ssh-ed25519 AAAA\nsecond ssh-ed25519 BBBB\n")
        self.assertNotEqual(self.run_prepare().returncode, 0)
        self.known_hosts.write_text("other.example ssh-ed25519 AAAA\n")
        self.assertNotEqual(self.run_prepare().returncode, 0)
        self.known_hosts.write_text("staging.example ssh-ed25519 AAAA\n")
        self.assertNotEqual(self.run_prepare(OPSI_E2E_STAGING_HOST_KEY_SHA256="SHA256:" + "B" * 43).returncode, 0)
        self.assertNotEqual(self.run_prepare(OPSI_E2E_STAGING_SSH_KEY_PATH=str(self.key)).returncode, 0)
        source = (ROOT / "scripts/e2e/verify-k3s.sh").read_text()
        self.assertGreaterEqual(source.count("info.st_uid != os.geteuid()"), 2)

    def test_resume_uses_existing_session_without_posting_a_second_one(self) -> None:
        prepared = self.run_prepare()
        self.assertEqual(prepared.returncode, 0, prepared.stderr)
        state = json.loads(self.state.read_text(encoding="utf-8"))
        state["phase"] = "replay_started"
        state["current_container_id"] = "worker-replay"
        self.state.write_text(json.dumps(state), encoding="utf-8")
        self.state.chmod(0o600)
        self.remote_state.write_text(json.dumps({"phase":"replay_started","session":"boot-factual","container":"worker-replay"}))
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
        self.write_marker("reached")

        restarted = self.run_mode("--barrier-restart")
        self.assertEqual(restarted.returncode, 0, restarted.stderr + restarted.stdout)
        self.assertEqual(json.loads(self.state.read_text())["current_container_id"], "worker-replay")

        self.write_marker("completed")
        resumed = self.run_mode("--resume-bootstrap-session", OPSI_E2E_BARRIER_HANDOFF_ONLY="1")
        self.assertEqual(resumed.returncode, 0, resumed.stderr + resumed.stdout)
        self.assertEqual(json.loads(self.state.read_text(encoding="utf-8"))["phase"], "completed")

        restored = self.run_mode("--barrier-restore")
        self.assertEqual(restored.returncode, 0, restored.stderr + restored.stdout)
        self.assertEqual(json.loads(self.state.read_text(encoding="utf-8"))["phase"], "normal_restored")
        self.assertEqual(json.loads(self.remote_state.read_text())["container"], "worker-normal")

        log = self.log.read_text(encoding="utf-8")
        self.assertEqual(log.count("/nodes/bootstrap"), 1)
        self.assertNotIn("LOCAL_DOCKER_CANARY", log)

    def test_reached_marker_cannot_be_promoted_by_completed_api_status(self) -> None:
        self.assertEqual(self.run_mode("--barrier-prepare").returncode, 0)
        self.write_marker("reached")
        self.assertEqual(self.run_mode("--barrier-restart").returncode, 0)

        resumed = self.run_mode("--resume-bootstrap-session", OPSI_E2E_BARRIER_HANDOFF_ONLY="1")
        self.assertNotEqual(resumed.returncode, 0)
        self.assertEqual(json.loads(self.state.read_text(encoding="utf-8"))["phase"], "replay_started")
        self.assertEqual(self.log.read_text(encoding="utf-8").count("/nodes/bootstrap"), 1)

    def test_illegal_reuse_and_conflicting_state_fail_closed(self) -> None:
        self.assertEqual(self.run_prepare().returncode, 0)
        self.assertNotEqual(self.run_prepare().returncode, 0)
        self.assertNotEqual(self.run_mode("--barrier-restart").returncode, 0)
        state = json.loads(self.state.read_text(encoding="utf-8"))
        state["source_revision"] = "d" * 40
        self.state.write_text(json.dumps(state), encoding="utf-8")
        self.state.chmod(0o600)
        self.assertNotEqual(self.run_mode("--barrier-restart").returncode, 0)

    def test_remote_helper_rejects_untrusted_request_shapes(self) -> None:
        helper = ROOT / "scripts/e2e/staging-barrier-remote.sh"
        request = {
            "schema_version":"opsi.e2e.staging-barrier-request/v1", "phase":"unknown",
            "source_revision":REVISION, "run_id":"run-request", "staging_host":"staging.example",
            "repository_directory":"/srv/opsi", "repository_identity":"a" * 64,
            "compose_directory":"/srv/opsi/deploy/staging-control-plane",
            "worker_digest":DIGEST_A, "session_id":"", "expected_state":"absent",
        }
        unknown_field = dict(request, phase="preflight", unexpected="value")
        unsafe_host = dict(request, phase="preflight", staging_host="staging.example;id")
        for raw in (
            b"", b"{}", b'{"schema_version":"x","schema_version":"y"}', b"x" * 4097,
            json.dumps(request).encode(), json.dumps(unknown_field).encode(), json.dumps(unsafe_host).encode(),
        ):
            with self.subTest(size=len(raw)):
                result = subprocess.run(["bash", str(helper)], input=raw, capture_output=True, check=False)
                self.assertNotEqual(result.returncode, 0)
                self.assertLessEqual(len(result.stderr), 1024)

class BarrierTrustDomainRegressionTests(unittest.TestCase):
    def test_barrier_has_no_local_staging_docker_path(self) -> None:
        source = (ROOT / "scripts/e2e/verify-k3s.sh").read_text(encoding="utf-8")
        self.assertNotIn("compose_worker()", source)
        self.assertNotIn('python3 "$RELEASE_HELPER" barrier-quiesce', source)

    def test_prepare_requires_revision_bound_remote_receipt_before_bootstrap(self) -> None:
        source = (ROOT / "scripts/e2e/verify-k3s.sh").read_text(encoding="utf-8")
        prepare = source.split("barrier_prepare()", 1)[1].split("load_barrier_context()", 1)[0]
        self.assertIn("staging_barrier_remote prepare", prepare)
        self.assertLess(prepare.index("staging_barrier_remote prepare"), prepare.index('/nodes/bootstrap'))

    def test_separated_operator_fixture_does_not_reach_local_docker_canary(self) -> None:
        fixture = BarrierProcedureTests(methodName="runTest")
        fixture.setUp()
        try:
            result = fixture.run_prepare()
            self.assertEqual(result.returncode, 0, result.stderr + result.stdout)
            log = fixture.log.read_text(encoding="utf-8")
            self.assertNotIn("docker ", log, "baseline local Docker canary was reached")
        finally:
            fixture.tearDown()


class StagingBarrierRepositoryTests(unittest.TestCase):
    def setUp(self) -> None:
        self.temp = tempfile.TemporaryDirectory()
        self.repository = pathlib.Path(self.temp.name) / "repo"
        self.compose = self.repository / "deploy/staging-control-plane"
        self.helper = self.repository / "scripts/e2e/staging-barrier-remote.sh"
        self.helper.parent.mkdir(parents=True)
        self.compose.mkdir(parents=True)
        self.helper.write_bytes((ROOT / "scripts/e2e/staging-barrier-remote.sh").read_bytes())
        self.helper.chmod(0o755)
        for command in (
            ("init", "-q"), ("config", "user.email", "fixture@example.test"),
            ("config", "user.name", "Fixture"), ("add", "."), ("commit", "-qm", "fixture"),
            ("remote", "add", "origin", "fixture-origin"),
        ):
            subprocess.run(["git", "-C", str(self.repository), *command], check=True)
        self.revision = subprocess.check_output(["git", "-C", str(self.repository), "rev-parse", "HEAD"], text=True).strip()

    def tearDown(self) -> None:
        self.temp.cleanup()

    def request(self, **changes: str) -> bytes:
        value = {
            "schema_version":"opsi.e2e.staging-barrier-request/v1", "phase":"preflight",
            "source_revision":self.revision, "run_id":"run-repository-test", "staging_host":"staging.example",
            "repository_directory":str(self.repository), "repository_identity":hashlib.sha256(b"fixture-origin").hexdigest(),
            "compose_directory":str(self.compose), "worker_digest":DIGEST_A, "session_id":"", "expected_state":"absent",
        }
        value.update(changes)
        return json.dumps(value, separators=(",", ":")).encode()

    def run_helper(self, request: bytes) -> subprocess.CompletedProcess[bytes]:
        return subprocess.run([str(self.helper)], input=request, capture_output=True, check=False)

    def test_revision_mismatch_and_dirty_worktree_fail_before_docker(self) -> None:
        original = self.helper.read_bytes()
        mismatch = self.run_helper(self.request(source_revision="d" * 40))
        self.assertNotEqual(mismatch.returncode, 0)
        self.assertIn(b"repository revision mismatch", mismatch.stderr)
        self.helper.write_bytes(self.helper.read_bytes() + b"\n")
        dirty = self.run_helper(self.request())
        self.assertNotEqual(dirty.returncode, 0)
        self.assertIn(b"tracked worktree check failed", dirty.stderr)
        self.helper.write_bytes(original)

    def test_helper_blob_mismatch_fails_before_docker(self) -> None:
        subprocess.run(
            ["git", "-C", str(self.repository), "update-index", "--assume-unchanged", "scripts/e2e/staging-barrier-remote.sh"],
            check=True,
        )
        self.helper.write_bytes(self.helper.read_bytes() + b"\n")
        mismatch = self.run_helper(self.request())
        self.assertNotEqual(mismatch.returncode, 0)
        self.assertIn(b"remote helper blob mismatch", mismatch.stderr)

    def test_symlinked_repository_or_compose_root_is_rejected(self) -> None:
        repository_link = self.repository.parent / "repo-link"
        repository_link.symlink_to(self.repository, target_is_directory=True)
        compose_link = self.repository / "compose-link"
        compose_link.symlink_to(self.compose, target_is_directory=True)
        for changes in ({"repository_directory":str(repository_link)}, {"compose_directory":str(compose_link)}):
            with self.subTest(changes=changes):
                result = self.run_helper(self.request(**changes))
                self.assertNotEqual(result.returncode, 0)
                self.assertIn(b"contains a symlink", result.stderr)


if __name__ == "__main__":
    unittest.main(verbosity=2)
