"""Reviewer-local Gateway signature submission checks."""

import base64
import contextlib
import io
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest import mock

from scripts import gateway_direct_mac as candidate
from scripts import gateway_direct_mac_approval as approval
from scripts import gateway_direct_mac_submit as submit


class GatewayDirectMacSubmitTest(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.keys = {}
        reviewers = {}
        for reviewer in approval.REVIEWERS:
            key = self.root / reviewer
            subprocess.run(["ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", str(key)],
                           check=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
            self.keys[reviewer] = key
            reviewers[reviewer] = " ".join(key.with_suffix(".pub").read_text().split()[:2])
        self.trust = self.root / "trust.json"
        self.trust.write_bytes(candidate.canonical({
            "schema": approval.TRUST_SCHEMA, "purpose": approval.PURPOSE, "reviewers": reviewers,
        }))
        self.subject = b'{"scope":"gateway-test"}\n'
        self.receipt = None
        self.posts = 0

    def make_packet(self):
        packet = self.root / "packet"
        (packet / "approval").mkdir(parents=True)
        (packet / "build").mkdir()
        (packet / "diagnostic").mkdir()
        (packet / "qualification").mkdir()
        (packet / "approval/gateway-approval-subject.candidate.json").write_bytes(self.subject)
        (packet / "diagnostic/empty.stderr").write_bytes(b"")
        manifest = (f"{candidate.sha256(self.subject)}  approval/gateway-approval-subject.candidate.json\n"
                    f"{candidate.sha256(b'')}  diagnostic/empty.stderr\n").encode()
        (packet / "SHA256SUMS").write_bytes(manifest)
        return packet, manifest

    def fake_api(self, path, payload=None):
        digest = candidate.sha256(self.trust.read_bytes())
        if path == submit.TRUST_API_PATH:
            return {"type": "file", "encoding": "base64",
                    "content": base64.b64encode(self.trust.read_bytes()).decode()}
        if path == "user":
            return {"login": "fredoliveira"}
        if path == f"repos/{submit.REVIEW_REPOSITORY}":
            return {"full_name": submit.REVIEW_REPOSITORY, "private": True}
        if path == f"repos/{submit.REVIEW_REPOSITORY}/issues/999":
            return {"state": "open", "locked": False,
                    "body": f"{submit.SUBJECT_SHA256} {submit.PACKET_MANIFEST_SHA256} {digest}"}
        if path.endswith("/comments?per_page=100&page=1"):
            return [self.receipt] if self.receipt else []
        if path == f"repos/{submit.REVIEW_REPOSITORY}/issues/999/comments":
            self.posts += 1
            self.receipt = {"id": 123, "user": {"login": "fredoliveira"}, "body": payload["body"],
                            "html_url": "https://github.com/sovereign46/models-cli/issues/999#issuecomment-123"}
            return self.receipt
        if path == f"repos/{submit.REVIEW_REPOSITORY}/issues/comments/123":
            return self.receipt
        raise AssertionError(path)

    def test_exact_packet_manifest_includes_empty_files_and_rejects_tampering(self):
        packet, manifest = self.make_packet()
        with mock.patch.object(submit, "PACKET_MANIFEST_SHA256", candidate.sha256(manifest)), \
             mock.patch.object(submit, "SUBJECT_SHA256", candidate.sha256(self.subject)), \
             mock.patch.object(approval, "checked_subject", return_value=self.subject):
            self.assertEqual(submit.checked_packet(packet), self.subject)
            (packet / "diagnostic/empty.stderr").write_bytes(b"tampered")
            with self.assertRaisesRegex(approval.ApprovalError, "packet file differs"):
                submit.checked_packet(packet)

    def test_packet_changed_during_confirmation_cannot_be_signed(self):
        packet, manifest = self.make_packet()

        def mutate_packet(_prompt):
            (packet / "diagnostic/empty.stderr").write_bytes(b"changed after review")
            return candidate.sha256(self.subject)

        with mock.patch.object(submit, "PACKET_MANIFEST_SHA256", candidate.sha256(manifest)), \
             mock.patch.object(submit, "SUBJECT_SHA256", candidate.sha256(self.subject)), \
             mock.patch.object(approval, "checked_subject", return_value=self.subject), \
             mock.patch.object(submit, "gh_api", side_effect=self.fake_api), \
             mock.patch.object(submit, "sign_subject") as signer, \
             mock.patch("builtins.input", side_effect=mutate_packet), \
             contextlib.redirect_stdout(io.StringIO()):
            with self.assertRaisesRegex(approval.ApprovalError, "packet file differs"):
                submit.submit(packet, self.keys["fredoliveira"],
                              "https://github.com/sovereign46/models-cli/issues/999", self.trust)
        signer.assert_not_called()
        self.assertEqual(self.posts, 0)

    def test_recomputed_subject_must_equal_confirmed_subject(self):
        with mock.patch.object(submit, "checked_packet", side_effect=[self.subject, b"changed subject"]), \
             mock.patch.object(submit, "gh_api", side_effect=self.fake_api), \
             mock.patch.object(submit, "sign_subject") as signer, \
             mock.patch("builtins.input", return_value=submit.SUBJECT_SHA256), \
             contextlib.redirect_stdout(io.StringIO()):
            with self.assertRaisesRegex(approval.ApprovalError, "changed during confirmation"):
                submit.submit(self.root, self.keys["fredoliveira"],
                              "https://github.com/sovereign46/models-cli/issues/999", self.trust)
        signer.assert_not_called()
        self.assertEqual(self.posts, 0)

    def test_path_shadow_cannot_replace_ssh_keygen(self):
        shadow = self.root / "shadow"
        shadow.mkdir()
        marker = self.root / "shadow-ran"
        executable = shadow / "ssh-keygen"
        executable.write_text(f"#!/bin/sh\necho ran > {marker}\nexit 90\n")
        executable.chmod(0o755)
        keys = approval.trusted_reviewers(self.trust)
        with mock.patch.dict(os.environ, {"PATH": str(shadow)}):
            submit.checked_private_key(self.keys["fredoliveira"], "fredoliveira", keys["fredoliveira"])
            signature = submit.sign_subject(self.subject, self.keys["fredoliveira"],
                                            "fredoliveira", keys)
        self.assertIn("BEGIN SSH SIGNATURE", signature)
        self.assertFalse(marker.exists())

    def test_signs_locally_and_persists_authenticated_receipt_once(self):
        with mock.patch.object(submit, "checked_packet", return_value=self.subject), \
             mock.patch.object(submit, "gh_api", side_effect=self.fake_api), \
             mock.patch("builtins.input", return_value=submit.SUBJECT_SHA256), \
             contextlib.redirect_stdout(io.StringIO()):
            submit.submit(self.root, self.keys["fredoliveira"],
                          "https://github.com/sovereign46/models-cli/issues/999", self.trust)
            submit.submit(self.root, self.keys["fredoliveira"],
                          "https://github.com/sovereign46/models-cli/issues/999", self.trust)
        self.assertEqual(self.posts, 1)
        record = json.loads(self.receipt["body"])
        self.assertEqual(record["schema"], submit.RECORD_SCHEMA)
        self.assertEqual(record["subjectSha256"], submit.SUBJECT_SHA256)
        signature = self.root / "fredoliveira.sig"
        signature.write_text(record["signature"])
        approval.verify_approval(self.subject, "fredoliveira", signature,
                                 approval.trusted_reviewers(self.trust))

    def test_wrong_private_key_and_wrong_comment_author_fail_closed(self):
        with mock.patch.object(submit, "checked_packet", return_value=self.subject), \
             mock.patch.object(submit, "gh_api", side_effect=self.fake_api), \
             mock.patch("builtins.input", return_value=submit.SUBJECT_SHA256), \
             contextlib.redirect_stdout(io.StringIO()):
            with self.assertRaisesRegex(approval.ApprovalError, "does not match"):
                submit.submit(self.root, self.keys["goncalossilva"],
                              "https://github.com/sovereign46/models-cli/issues/999", self.trust)
        self.assertEqual(self.posts, 0)
        wrong_author = {"id": 123, "user": {"login": "another-user"}, "body": "body",
                        "html_url": "https://github.com/sovereign46/models-cli/issues/999#issuecomment-123"}
        with self.assertRaisesRegex(approval.ApprovalError, "persisted Gateway review comment differs"):
            submit.checked_comment(wrong_author, "fredoliveira", "body", "999")

    def test_conflicting_prior_submission_is_rejected(self):
        self.receipt = {"id": 123, "user": {"login": "fredoliveira"},
                        "body": json.dumps({"schema": submit.RECORD_SCHEMA, "signature": "different"})}
        with mock.patch.object(submit, "gh_api", side_effect=self.fake_api):
            with self.assertRaisesRegex(approval.ApprovalError, "already submitted a different"):
                submit.publish_signature("999", "fredoliveira", {
                    "schema": submit.RECORD_SCHEMA, "signature": "expected",
                })
        self.assertEqual(self.posts, 0)


if __name__ == "__main__":
    unittest.main()
