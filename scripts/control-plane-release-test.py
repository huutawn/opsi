#!/usr/bin/env python3
"""Regression checks for the immutable control-plane publisher."""

from __future__ import annotations

import importlib.util
import json
import pathlib
import re
import subprocess
import sys
import tempfile
import unittest

ROOT = pathlib.Path(__file__).resolve().parents[1]
HELPER = ROOT / "scripts/control-plane-release.py"
WORKFLOW = ROOT / ".github/workflows/publish-control-plane.yml"
OLD_WORKFLOW = ROOT / ".github/workflows/publish-bootstrap-worker.yml"
DOCKERFILE = ROOT / "cloud/Dockerfile"
MAKEFILE = ROOT / "Makefile"
SPEC = importlib.util.spec_from_file_location("control_plane_release", HELPER)
assert SPEC and SPEC.loader
release = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(release)

REVISION = "c" * 40
CLOUD_DIGEST = "sha256:" + "a" * 64
WORKER_DIGEST = "sha256:" + "b" * 64


def manifest() -> dict[str, object]:
    return {
        "schema_version": release.SCHEMA,
        "source_repository": release.SOURCE_REPOSITORY,
        "source_revision": REVISION,
        "workflow_run_id": "123456",
        "created_at": "2026-08-02T12:34:56Z",
        "platform": release.PLATFORM,
        "components": {
            "cloud": {
                "repository": release.COMPONENTS["cloud"][0],
                "target": release.COMPONENTS["cloud"][1],
                "image_digest": CLOUD_DIGEST,
                "image_reference": release.COMPONENTS["cloud"][0] + "@" + CLOUD_DIGEST,
            },
            "bootstrap_worker": {
                "repository": release.COMPONENTS["bootstrap_worker"][0],
                "target": release.COMPONENTS["bootstrap_worker"][1],
                "image_digest": WORKER_DIGEST,
                "image_reference": release.COMPONENTS["bootstrap_worker"][0] + "@" + WORKER_DIGEST,
            },
        },
    }


def image_inspect(component: str, digest: str) -> list[dict[str, object]]:
    repository, target = release.COMPONENTS[component]
    return [
        {
            "RepoTags": [f"{repository}:{REVISION}"],
            "RepoDigests": [f"{repository}@{digest}"],
            "Os": "linux",
            "Architecture": "amd64",
            "Config": {
                "Labels": {
                    "org.opencontainers.image.source": release.SOURCE_URL,
                    "org.opencontainers.image.revision": REVISION,
                    "org.opencontainers.image.version": REVISION,
                    "org.opencontainers.image.created": "2026-08-02T12:34:56Z",
                    "io.opsi.control-plane.component": component,
                    "io.opsi.control-plane.target": target,
                }
            },
        }
    ]


class ManifestTests(unittest.TestCase):
    def test_manifest_is_strict_and_extracts_both_immutable_components(self) -> None:
        value = release.validate_manifest(manifest())
        self.assertEqual(set(value), release.ROOT_KEYS)
        for component, digest in (("cloud", CLOUD_DIGEST), ("bootstrap_worker", WORKER_DIGEST)):
            values = release.component_values(value, component)
            self.assertEqual(values["image_digest"], digest)
            self.assertEqual(values["image_reference"], release.COMPONENTS[component][0] + "@" + digest)

    def test_manifest_command_creates_then_validates_without_overwrite(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            output = pathlib.Path(raw) / "control-plane-release.json"
            command = [
                sys.executable,
                str(HELPER),
                "manifest",
                "--source-revision",
                REVISION,
                "--workflow-run-id",
                "123456",
                "--created-at",
                "2026-08-02T12:34:56Z",
                "--cloud-digest",
                CLOUD_DIGEST,
                "--bootstrap-worker-digest",
                WORKER_DIGEST,
                "--output",
                str(output),
            ]
            created = subprocess.run(command, text=True, capture_output=True, check=False)
            self.assertEqual(created.returncode, 0, created.stderr)
            self.assertEqual(release.load_manifest(output), manifest())
            repeated = subprocess.run(command, text=True, capture_output=True, check=False)
            self.assertNotEqual(repeated.returncode, 0)

    def test_malformed_combined_manifests_fail_closed(self) -> None:
        mutations = []
        unknown = manifest()
        unknown["credential"] = "forbidden"
        mutations.append(unknown)
        missing = manifest()
        del missing["platform"]
        mutations.append(missing)
        uppercase = manifest()
        uppercase["components"]["cloud"]["image_digest"] = "sha256:" + "A" * 64  # type: ignore[index]
        mutations.append(uppercase)
        mutable = manifest()
        mutable["components"]["cloud"]["image_reference"] = "ghcr.io/huutawn/opsi-cloud:latest"  # type: ignore[index]
        mutations.append(mutable)
        wrong_target = manifest()
        wrong_target["components"]["bootstrap_worker"]["target"] = "cloud"  # type: ignore[index]
        mutations.append(wrong_target)
        bad_revision = manifest()
        bad_revision["source_revision"] = "C" * 40
        mutations.append(bad_revision)
        for value in mutations:
            with self.subTest(value=value):
                with self.assertRaises(release.ReleaseError):
                    release.validate_manifest(value)

    def test_duplicate_fields_and_secret_looking_nested_fields_are_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            path = pathlib.Path(raw) / "release.json"
            path.write_text('{"schema_version":"a","schema_version":"b"}', encoding="utf-8")
            with self.assertRaises(release.ReleaseError):
                release.load_manifest(path)
        value = manifest()
        value["components"]["cloud"]["registry_token"] = "forbidden"  # type: ignore[index]
        with self.assertRaises(release.ReleaseError):
            release.validate_manifest(value)


class ImageVerificationTests(unittest.TestCase):
    def test_existing_valid_revision_tag_is_reusable(self) -> None:
        for component, digest in (("cloud", CLOUD_DIGEST), ("bootstrap_worker", WORKER_DIGEST)):
            repository = release.COMPONENTS[component][0]
            self.assertEqual(
                release.verify_image_inspect(image_inspect(component, digest), component, REVISION, f"{repository}:{REVISION}"),
                digest,
            )

    def test_inconsistent_existing_tag_is_rejected(self) -> None:
        cases = []
        wrong_revision = image_inspect("cloud", CLOUD_DIGEST)
        wrong_revision[0]["Config"]["Labels"]["org.opencontainers.image.revision"] = "d" * 40  # type: ignore[index]
        cases.append(wrong_revision)
        wrong_target = image_inspect("cloud", CLOUD_DIGEST)
        wrong_target[0]["Config"]["Labels"]["io.opsi.control-plane.target"] = "bootstrap-worker"  # type: ignore[index]
        cases.append(wrong_target)
        wrong_platform = image_inspect("cloud", CLOUD_DIGEST)
        wrong_platform[0]["Architecture"] = "arm64"
        cases.append(wrong_platform)
        uppercase = image_inspect("cloud", "sha256:" + "A" * 64)
        cases.append(uppercase)
        wrong_repository = image_inspect("cloud", CLOUD_DIGEST)
        wrong_repository[0]["RepoDigests"] = ["ghcr.io/huutawn/other@" + CLOUD_DIGEST]
        cases.append(wrong_repository)
        for value in cases:
            with self.subTest(value=value):
                with self.assertRaises(release.ReleaseError):
                    release.verify_image_inspect(
                        value, "cloud", REVISION, f"{release.COMPONENTS['cloud'][0]}:{REVISION}"
                    )


class WorkflowTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.workflow = WORKFLOW.read_text(encoding="utf-8")
        cls.dockerfile = DOCKERFILE.read_text(encoding="utf-8")
        cls.makefile = MAKEFILE.read_text(encoding="utf-8")

    def test_one_manual_canonical_publisher_replaces_the_old_path(self) -> None:
        self.assertTrue(WORKFLOW.is_file())
        self.assertFalse(OLD_WORKFLOW.exists())
        self.assertEqual(self.workflow.count("workflow_dispatch:"), 1)
        for trigger in ("pull_request:", "push:", "schedule:"):
            self.assertNotIn(trigger, self.workflow)
        self.assertIn("ghcr.io/huutawn/opsi-cloud", self.workflow)
        self.assertIn("ghcr.io/huutawn/opsi-bootstrap-worker", self.workflow)

    def test_exact_trust_guards_concurrency_and_minimal_permissions(self) -> None:
        for guard in (
            'test "$GITHUB_REPOSITORY" = huutawn/opsi',
            'test "$GITHUB_REF" = refs/heads/developer',
            'test "$SOURCE_REVISION" = "$GITHUB_SHA"',
            'test "$CONFIRMATION" = publish-control-plane',
            "grep -Eq '^[0-9a-f]{40}$'",
        ):
            self.assertIn(guard, self.workflow)
        self.assertIn("group: publish-control-plane-${{ inputs.source_revision }}", self.workflow)
        self.assertEqual(self.workflow.count("packages: write"), 1)
        self.assertEqual(self.workflow.count("contents: read"), 2)

    def test_both_targets_use_one_dockerfile_root_context_platform_and_revision(self) -> None:
        self.assertIn("cloud|opsi-cloud|ghcr.io/huutawn/opsi-cloud|cloud", self.workflow)
        self.assertIn(
            "bootstrap_worker|opsi-bootstrap-worker|ghcr.io/huutawn/opsi-bootstrap-worker|bootstrap-worker",
            self.workflow,
        )
        build = self.workflow.split("              docker build \\\n", 1)[1].split("              docker push", 1)[0]
        self.assertIn("--file cloud/Dockerfile \\\n", build)
        self.assertIn('--platform "$PLATFORM" \\\n', build)
        self.assertIn('--target "$target" \\\n', build)
        self.assertIn('--build-arg "VERSION=$GITHUB_SHA" \\\n', build)
        self.assertIn('--tag "$image_tag" \\\n                .\n', build)
        self.assertNotRegex(self.workflow, r"\\\n[ \t]*\n")
        self.assertEqual(self.dockerfile.count("go build -mod=readonly"), 2)
        self.assertIn("-o /out/opsi-cloud ./cmd/opsi-cloud", self.dockerfile)
        self.assertIn("-o /out/opsi-bootstrap-worker ./cmd/opsi-bootstrap-worker", self.dockerfile)

    def test_partial_publish_resume_reuses_only_fully_verified_existing_tags(self) -> None:
        loop = self.workflow.split("          while IFS='|' read -r component package repository target; do", 1)[1].split(
            "          done <<'COMPONENTS'", 1
        )[0]
        self.assertIn('if test -z "$existing_id"; then', loop)
        self.assertIn('docker manifest inspect "$image_tag"', loop)
        self.assertIn("manifest unknown|no such manifest|not found", loop)
        self.assertIn('docker push "$image_tag"', loop)
        self.assertIn("reusing verified immutable tag", loop)
        self.assertLess(loop.index('if test -z "$existing_id"; then'), loop.index('docker pull --platform "$PLATFORM" "$image_tag"'))
        self.assertIn("control-plane-release.py verify-image", loop)
        self.assertNotIn("refusing to overwrite", loop)

    def test_manifest_is_created_only_after_both_images_and_cross_checked_before_upload(self) -> None:
        loop_end = self.workflow.index("          done <<'COMPONENTS'")
        create = self.workflow.index("      - name: Create strict combined manifest")
        cross_check = self.workflow.index("      - name: Cross-check manifest against immutable registry images")
        upload = self.workflow.index("actions/upload-artifact@")
        self.assertLess(loop_end, create)
        self.assertLess(create, cross_check)
        self.assertLess(cross_check, upload)
        self.assertIn("control-plane-release-${{ inputs.source_revision }}", self.workflow)

    def test_no_mutable_tags_pats_unpinned_actions_or_excess_release_scope(self) -> None:
        for forbidden in (":latest", ":staging", "personal access token", "PAT", "refs/heads/main"):
            self.assertNotIn(forbidden, self.workflow)
        uses = re.findall(r"uses:\s*([^\s]+)", self.workflow)
        self.assertTrue(uses)
        for action in uses:
            self.assertRegex(action, r"^[^@]+@[0-9a-f]{40}$")
        gate = self.makefile.split("verify-bootstrap-worker-release:", 1)[1].split("\n\nverify:", 1)[0]
        self.assertIn("scripts/control-plane-release-test.py", gate)
        self.assertIn("scripts/bootstrap-worker-release-test.py", gate)
        self.assertIn("GOWORK=off", gate)


if __name__ == "__main__":
    unittest.main(verbosity=2)
