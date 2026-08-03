#!/usr/bin/env python3
import importlib.util
import json
import os
import pathlib
import tempfile
import time
import unittest
from unittest import mock


ROOT = pathlib.Path(__file__).resolve().parents[2]
HELPER_PATH = ROOT / "scripts/e2e/second_factor_handoff.py"
HARNESS_PATH = ROOT / "scripts/e2e/verify-k3s.sh"


def load_helper():
    if not HELPER_PATH.is_file():
        raise AssertionError("second-factor handoff helper is missing")
    spec = importlib.util.spec_from_file_location("second_factor_handoff", HELPER_PATH)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class HarnessIntegrationTest(unittest.TestCase):
    def test_harness_does_not_capture_expiring_factor_environment(self):
        source = HARNESS_PATH.read_text()
        self.assertNotIn("OPSI_E2E_TOTP_CODE", source)
        self.assertNotIn("OPSI_E2E_OTP_REQUEST_ID", source)
        self.assertNotIn("OPSI_E2E_OTP_CODE", source)

    def test_harness_consumes_factors_at_rotate_and_reveal_boundary(self):
        source = HARNESS_PATH.read_text()
        secret_create = source.index("secret create failed")
        rotate_handoff = source.index("consume_second_factor rotate")
        rotate_call = source.index("secret rotate failed")
        reveal_handoff = source.index("consume_second_factor reveal")
        reveal_call = source.index("secret reveal failed")
        self.assertLess(secret_create, rotate_handoff)
        self.assertLess(rotate_handoff, rotate_call)
        self.assertLess(rotate_call, reveal_handoff)
        self.assertLess(reveal_handoff, reveal_call)


class HandoffTest(unittest.TestCase):
    def setUp(self):
        self.helper = load_helper()
        self.temp = tempfile.TemporaryDirectory()
        self.root = pathlib.Path(self.temp.name)
        self.handoff = self.root / "handoff"
        self.handoff.mkdir(mode=0o700)
        self.private = self.root / "private"
        self.private.mkdir(mode=0o700)
        self.values = self.private / "redaction-values"
        self.values.touch(mode=0o600)

    def tearDown(self):
        self.temp.cleanup()

    def stage(self, operation, payload, *, mode=0o600):
        path = self.handoff / f"{operation}.json"
        path.write_text(json.dumps(payload))
        path.chmod(mode)
        return path

    def consume(self, operation, *, reject_fingerprint="", now=100):
        output = self.private / f"{operation}-request.json"
        metadata = self.helper.consume_handoff(
            self.handoff,
            operation,
            1,
            output,
            self.values,
            "service-1",
            "secret-1",
            reject_fingerprint=reject_fingerprint,
            wall_clock=lambda: now,
        )
        return metadata, output

    def test_expired_totp_is_rejected_and_removed(self):
        handoff = self.stage(
            "rotate", {"type": "totp", "code": "1" * 6, "period_start_unix": 60}
        )
        with self.assertRaisesRegex(ValueError, "expired"):
            self.consume("rotate", now=100)
        self.assertFalse(handoff.exists())
        self.assertFalse((self.private / "rotate-request.json").exists())

    def test_preflight_accepts_secure_empty_handoff_directory(self):
        self.assertEqual(
            self.helper.validate_handoff_directory(self.handoff), self.handoff
        )

    def test_staging_publishes_one_complete_private_file(self):
        self.helper.stage_handoff(
            self.handoff,
            "rotate",
            {"type": "otp", "request_id": "request-stage", "code": "9" * 6},
        )
        path = self.handoff / "rotate.json"
        self.assertEqual(path.stat().st_mode & 0o777, 0o600)
        self.assertEqual(json.loads(path.read_text())["request_id"], "request-stage")
        self.assertEqual(list(self.handoff.iterdir()), [path])

    def test_fresh_totp_creates_adjacent_rotate_and_reveal_requests(self):
        handoff = self.stage(
            "rotate", {"type": "totp", "code": "8" * 6, "period_start_unix": 90}
        )
        rotate = self.private / "rotate-request.json"
        reveal = self.private / "paired-reveal-request.json"
        metadata = self.helper.consume_handoff(
            self.handoff,
            "rotate",
            1,
            rotate,
            self.values,
            "service-1",
            "secret-1",
            paired_output_path=reveal,
            wall_clock=lambda: 100,
        )
        self.assertEqual(metadata["method"], "totp")
        self.assertEqual(metadata["expires_at_unix"], 120)
        self.assertFalse(handoff.exists())
        self.assertEqual(
            json.loads(rotate.read_text())["totp_code"],
            json.loads(reveal.read_text())["totp_code"],
        )
        self.assertTrue(json.loads(reveal.read_text())["reveal"])

    def test_reused_otp_is_rejected_but_distinct_pairs_pass(self):
        first = {"type": "otp", "request_id": "request-rotate", "code": "2" * 6}
        self.stage("rotate", first)
        rotate, _ = self.consume("rotate")

        reused = self.stage("reveal", first)
        with self.assertRaisesRegex(ValueError, "reused"):
            self.consume("reveal", reject_fingerprint=rotate["fingerprint"])
        self.assertFalse(reused.exists())

        self.stage(
            "reveal", {"type": "otp", "request_id": "request-reveal", "code": "3" * 6}
        )
        reveal, output = self.consume(
            "reveal", reject_fingerprint=rotate["fingerprint"]
        )
        self.assertNotEqual(rotate["fingerprint"], reveal["fingerprint"])
        self.assertTrue(json.loads(output.read_text())["reveal"])

    def test_wrong_ownership_and_mode_are_rejected(self):
        with mock.patch.object(self.helper.os, "geteuid", return_value=os.geteuid() + 1):
            with self.assertRaisesRegex(ValueError, "owned"):
                self.helper.validate_handoff_directory(self.handoff)

        owned = self.stage(
            "rotate", {"type": "totp", "code": "4" * 6, "period_start_unix": 90}
        )
        with mock.patch.object(self.helper.os, "geteuid", return_value=os.geteuid() + 1):
            with self.assertRaisesRegex(ValueError, "owned"):
                self.helper._take_handoff(self.handoff, "rotate")
        self.assertFalse(owned.exists())

        handoff = self.stage(
            "rotate",
            {"type": "totp", "code": "4" * 6, "period_start_unix": 90},
            mode=0o640,
        )
        with self.assertRaisesRegex(ValueError, "0600"):
            self.consume("rotate")
        self.assertFalse(handoff.exists())

    def test_symlinks_are_rejected(self):
        target = self.root / "real-handoff"
        target.mkdir(mode=0o700)
        link = self.root / "handoff-link"
        link.symlink_to(target, target_is_directory=True)
        with self.assertRaisesRegex(ValueError, "symlink"):
            self.helper.validate_handoff_directory(link)

        target_file = self.root / "factor.json"
        target_file.write_text("{}")
        target_file.chmod(0o600)
        handoff = self.handoff / "rotate.json"
        handoff.symlink_to(target_file)
        with self.assertRaisesRegex(ValueError, "symlink"):
            self.consume("rotate")
        self.assertFalse(handoff.exists())

    def test_oversized_and_malformed_input_are_rejected(self):
        oversized = self.handoff / "rotate.json"
        oversized.write_bytes(b"x" * 513)
        oversized.chmod(0o600)
        with self.assertRaisesRegex(ValueError, "size"):
            self.consume("rotate")
        self.assertFalse(oversized.exists())

        cases = (
            b"not-json",
            b'{"type":"otp","request_id":"one","request_id":"two","code":"'
            + b"1" * 6
            + b'"}',
            json.dumps(
                {"type": "totp", "code": "5" * 5, "period_start_unix": 90}
            ).encode(),
            json.dumps(
                {
                    "type": "totp",
                    "code": "5" * 6,
                    "period_start_unix": 90,
                    "seed": "forbidden",
                }
            ).encode(),
            json.dumps({"type": "otp", "request_id": "", "code": "6" * 6}).encode(),
        )
        for content in cases:
            with self.subTest(content=content):
                path = self.handoff / "rotate.json"
                path.write_bytes(content)
                path.chmod(0o600)
                with self.assertRaises(ValueError):
                    self.consume("rotate")
                self.assertFalse(path.exists())

    def test_missing_handoff_times_out_within_bound(self):
        started = time.monotonic()
        with self.assertRaisesRegex(TimeoutError, "timed out"):
            self.consume("rotate")
        self.assertLess(time.monotonic() - started, 2)
        for timeout in (0, 121):
            with self.subTest(timeout=timeout):
                with self.assertRaisesRegex(ValueError, "between 1 and 120"):
                    self.helper.validate_timeout(timeout)

    def test_redaction_and_leak_scan_cover_handoff_values(self):
        code = "7" * 6
        request_id = "request-sensitive"
        self.stage(
            "rotate", {"type": "otp", "request_id": request_id, "code": code}
        )
        _, _ = self.consume("rotate")
        redacted = self.helper.redact_text(
            f'otp_code="{code}" otp_request_id="{request_id}" password="generated"',
            self.helper.read_redaction_values(self.values),
        )
        self.assertNotIn(code, redacted)
        self.assertNotIn(request_id, redacted)
        self.assertNotIn("generated", redacted)

        evidence = self.root / "evidence"
        evidence.mkdir()
        (evidence / "safe.json").write_text(redacted)
        self.helper.scan_artifacts(evidence, self.values)
        (evidence / "leak.txt").write_text(code)
        with self.assertRaisesRegex(ValueError, "sensitive"):
            self.helper.scan_artifacts(evidence, self.values)


if __name__ == "__main__":
    unittest.main()
