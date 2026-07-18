#!/usr/bin/env python3

import unittest
from unittest import mock

import verify_r5_005_github_app_preflight as verifier


class GitHubAppPreflightTests(unittest.TestCase):
    def app(self, events=None, permissions=None):
        return {
            "id": 4315525,
            "slug": "opsi-staging-huutawn",
            "events": ["repository"] if events is None else events,
            "permissions": {"metadata": "read"} if permissions is None else permissions,
        }

    def test_repository_is_the_only_required_manual_event(self):
        result = verifier.validate_app(self.app(), 4315525)
        self.assertEqual(result["manual_events"], ["repository"])
        self.assertEqual(result["default_lifecycle_events"], ["installation", "installation_repositories"])

    def test_default_lifecycle_events_are_not_required_in_events_array(self):
        result = verifier.validate_app(self.app(["repository"]), 4315525)
        self.assertEqual(result["manual_events"], ["repository"])

    def test_installation_target_does_not_replace_repository(self):
        with self.assertRaisesRegex(verifier.PreflightError, "repository event"):
            verifier.validate_app(self.app(["installation_target"]), 4315525)

    def test_default_lifecycle_events_may_appear_without_becoming_requirements(self):
        result = verifier.validate_app(
            self.app(["installation", "installation_repositories", "repository"]), 4315525
        )
        self.assertIn("repository", result["manual_events"])

    def test_permissions_remain_metadata_read_only(self):
        for permissions in ({"metadata": "write"}, {"metadata": "read", "contents": "read"}):
            with self.subTest(permissions=permissions):
                with self.assertRaisesRegex(verifier.PreflightError, "Metadata: Read-only"):
                    verifier.validate_app(self.app(permissions=permissions), 4315525)

    def test_hook_configuration_is_sanitized_and_strict(self):
        result = verifier.validate_hook_config(
            {"url": "https://opsidev.site/v1/webhooks/github-app", "content_type": "json", "insecure_ssl": "0", "secret": "not-returned"},
            "https://opsidev.site/v1/webhooks/github-app",
        )
        self.assertNotIn("secret", result)
        with self.assertRaises(verifier.PreflightError):
            verifier.validate_hook_config(
                {"url": "http://opsidev.site/v1/webhooks/github-app", "content_type": "json", "insecure_ssl": "1"},
                "https://opsidev.site/v1/webhooks/github-app",
            )

    def test_installation_and_repository_numeric_identity(self):
        installation = verifier.validate_installation(
            {
                "id": 147333403,
                "account": {"id": 143307746, "login": "huutawn", "type": "User"},
                "suspended_at": None,
                "repository_selection": "selected",
            },
            147333403,
            143307746,
        )
        self.assertFalse(installation["suspended"])
        repository = verifier.validate_repositories(
            {
                "repositories": [
                    {
                        "id": 1304594095,
                        "full_name": "huutawn/opsi-r5-005-fixture",
                        "owner": {"id": 143307746},
                        "default_branch": "main",
                        "private": False,
                        "archived": False,
                        "disabled": False,
                    }
                ]
            },
            1304594095,
            "huutawn/opsi-r5-005-fixture",
            143307746,
            "main",
        )
        self.assertEqual(repository["id"], 1304594095)

    def test_delivery_sanitizer_keeps_only_numeric_identity_and_result(self):
        delivery = verifier.sanitize_delivery(
            {
                "id": 123,
                "guid": "delivery-guid",
                "event": "installation_repositories",
                "action": "added",
                "status_code": 200,
                "redelivery": True,
                "request": {
                    "headers": {"x-hub-signature-256": "must-not-return"},
                    "payload": {
                        "installation": {"id": 147333403},
                        "repositories_added": [{"id": 1304594095, "private_field": "must-not-return"}],
                        "repositories_removed": [],
                        "sender": {"token": "must-not-return"},
                    },
                },
                "response": {"payload": '{"status":"ok","duplicate":true,"debug":"must-not-return"}'},
            }
        )
        self.assertEqual(delivery["installation_id"], 147333403)
        self.assertEqual(delivery["added_repository_ids"], [1304594095])
        self.assertEqual(delivery["response"], {"status": "ok", "duplicate": True})
        serialized = str(delivery)
        self.assertNotIn("signature", serialized)
        self.assertNotIn("must-not-return", serialized)

    @mock.patch.object(verifier, "request_bytes")
    @mock.patch.object(verifier, "request_json")
    def test_redeliver_follows_the_new_attempt_with_the_same_guid(self, request_json, request_bytes):
        request_json.side_effect = [
            {"id": 10, "guid": "same-guid"},
            {
                "id": 11,
                "guid": "same-guid",
                "event": "installation_repositories",
                "action": "added",
                "redelivery": True,
                "status_code": 200,
                "request": {"payload": {"installation": {"id": 147333403}}},
                "response": {"payload": '{"status":"ok","duplicate":false}'},
            },
        ]
        request_bytes.side_effect = [
            (
                200,
                b'[{"id":9,"guid":"same-guid","redelivery":true},{"id":10,"guid":"same-guid","redelivery":false}]',
            ),
            (202, b""),
            (
                200,
                b'[{"id":11,"guid":"same-guid","redelivery":true,"delivered_at":"2026-07-18T14:02:07Z"}]',
            ),
        ]
        result = verifier.redeliver("not-a-real-token", 10, 1)
        self.assertEqual(result["api_id"], 11)
        self.assertEqual(result["response"], {"status": "ok", "duplicate": False})


if __name__ == "__main__":
    unittest.main(verbosity=2)
