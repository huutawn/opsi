#!/usr/bin/env python3
import pathlib
import re
import subprocess
import tempfile
import unittest


ROOT = pathlib.Path(__file__).resolve().parents[1]
WORKFLOW = ROOT / ".github/workflows/publish-agent.yml"


class AgentPublisherTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.workflow = WORKFLOW.read_text(encoding="utf-8")

    def test_workflow_has_one_trusted_revision_only_interface(self) -> None:
        self.assertIn("workflow_dispatch:", self.workflow)
        self.assertEqual(re.findall(r"^      [a-z_]+:\s*$", self.workflow, re.MULTILINE), ["      source_revision:", "      confirmation:"])
        for required in (
            "github.repository == 'huutawn/opsi'",
            "github.ref == 'refs/heads/developer'",
            'test "$SOURCE_REVISION" = "$GITHUB_SHA"',
            'test "$CONFIRMATION" = publish-agent',
            "^[0-9a-f]{40}$",
            "publish-agent-${{ inputs.source_revision }}",
        ):
            self.assertIn(required, self.workflow)

    def test_actions_permissions_and_toolchain_are_fixed(self) -> None:
        self.assertEqual(self.workflow.count("permissions:"), 1)
        self.assertRegex(self.workflow, r"permissions:\n  contents: write\n")
        self.assertNotRegex(self.workflow, r"permissions:\n(?:  .+\n){2,}")
        self.assertNotRegex(self.workflow, r"(?i)\bpat\b")
        self.assertIn("go-version: '1.26.4'", self.workflow)
        actions = re.findall(r"uses:\s*([^\s]+)", self.workflow)
        self.assertTrue(actions)
        for action in actions:
            self.assertRegex(action, r"^[^@]+@[0-9a-f]{40}$")

    def test_release_is_immutable_anonymous_and_agent_only(self) -> None:
        for forbidden in ("latest/download", "--clobber", "personal access token", "refs/heads/main"):
            self.assertNotIn(forbidden, self.workflow.lower())
        self.assertIn("for build in one two; do", self.workflow)
        self.assertEqual(self.workflow.count('"$BUILDER" "$GITHUB_SHA"'), 1)
        self.assertIn("env -u GH_TOKEN -u GITHUB_TOKEN curl", self.workflow)
        self.assertIn("releases/download/$TAG/$asset", self.workflow)
        self.assertNotIn("opsi-cloud", self.workflow)
        self.assertNotIn("opsi-bootstrap-worker", self.workflow)
        publishers = {path.name for path in (ROOT / ".github/workflows").glob("publish-*.yml")}
        self.assertEqual(publishers, {"publish-agent.yml", "publish-control-plane.yml"})

    def test_builder_rejects_non_revision_input_without_writing(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            output = pathlib.Path(raw) / "release"
            result = subprocess.run(
                [ROOT / "scripts/build-agent-release.sh", "A" * 40, output],
                text=True,
                capture_output=True,
                check=False,
            )
            self.assertEqual(result.returncode, 2)
            self.assertFalse(output.exists())

    def test_verifier_rejects_an_extra_asset(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            root = pathlib.Path(raw)
            for name in ("opsi-agent-linux-amd64", "checksums.txt", "release.json", "unexpected"):
                (root / name).touch()
            result = subprocess.run(
                [ROOT / "scripts/verify-agent-release.sh", root, "0" * 40],
                text=True,
                capture_output=True,
                check=False,
            )
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("exactly the three required assets", result.stderr)


if __name__ == "__main__":
    unittest.main()
